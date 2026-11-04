package udp_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/mux"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/client"
)

// BenchmarkBlockwiseDownload measures complete application-visible transfers.
// The Auto capability probe is warmed separately so it does not distort the
// steady-state measurement. Loopback numbers are not a WAN throughput claim.
func BenchmarkBlockwiseDownload(b *testing.B) {
	benchmarkBlockwiseDownload(b, 0)
}

func BenchmarkBlockwiseDownload50ms(b *testing.B) {
	benchmarkBlockwiseDownload(b, 50*time.Millisecond)
}

func benchmarkBlockwiseDownload(b *testing.B, rtt time.Duration) {
	for _, mode := range []options.QBlockwiseMode{options.QBlockwiseDisabled, options.QBlockwiseAuto} {
		for _, size := range []int{10 * 1024, 1024 * 1024} {
			b.Run(fmt.Sprintf("mode=%d/bytes=%d", mode, size), func(b *testing.B) {
				payload := make([]byte, size)
				for i := range payload {
					payload[i] = byte(i*37 + i/251)
				}
				router := mux.NewRouter()
				if err := router.Handle("/bench", mux.HandlerFunc(func(w mux.ResponseWriter, _ *mux.Message) {
					if err := w.SetResponse(codes.Content, message.AppOctets, bytes.NewReader(payload)); err != nil {
						b.Errorf("set benchmark response: %v", err)
					}
				})); err != nil {
					b.Fatal(err)
				}
				listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
				if err != nil {
					b.Fatal(err)
				}
				var peer atomic.Pointer[client.Conn]
				server := udp.NewServer(options.WithMux(router),
					options.WithQBlockwise(mode), options.WithBlockwise(true, blockwise.SZX1024, time.Minute),
					options.WithOnNewConn(func(cc *client.Conn) { peer.Store(cc) }),
				)
				done := make(chan error, 1)
				go func() { done <- server.Serve(listener) }()
				b.Cleanup(func() {
					server.Stop()
					if closeErr := listener.Close(); closeErr != nil {
						b.Error(closeErr)
					}
					if serveErr := <-done; serveErr != nil {
						b.Error(serveErr)
					}
				})
				addr := listener.LocalAddr().String()
				if rtt > 0 {
					addr = qLatencyRelay(b, addr, rtt/2)
				}
				dial := func() *client.Conn {
					conn, dialErr := udp.Dial(addr,
						options.WithQBlockwise(mode), options.WithBlockwise(true, blockwise.SZX1024, time.Minute))
					if dialErr != nil {
						b.Fatal(dialErr)
					}
					return conn
				}
				conn := dial()
				b.Cleanup(func() {
					if err := conn.Close(); err != nil {
						b.Error(err)
					}
				})
				download := func() {
					ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
					defer cancel()
					req, err := conn.NewGetRequest(ctx, "/bench")
					if err != nil {
						b.Fatal(err)
					}
					defer conn.ReleaseMessage(req)
					req.SetType(message.NonConfirmable)
					resp, err := conn.Do(req)
					if err != nil {
						b.Fatal(err)
					}
					defer conn.ReleaseMessage(resp)
					if resp.Code() != codes.Content {
						b.Fatalf("unexpected response: %v", resp.Code())
					}
					got, err := resp.ReadBody()
					if err != nil {
						b.Fatal(err)
					}
					if !bytes.Equal(payload, got) {
						b.Fatalf("benchmark payload differs: got %d bytes, want %d", len(got), size)
					}
				}

				download()
				reset := func() {
					if closeErr := conn.Close(); closeErr != nil {
						b.Fatal(closeErr)
					}
					if cc := peer.Swap(nil); cc != nil {
						if closeErr := cc.Close(); closeErr != nil {
							b.Fatal(closeErr)
						}
					}
					conn = dial()
					download()
				}
				b.SetBytes(int64(size))
				b.ReportAllocs()
				b.ResetTimer()
				for i := range b.N {
					if i > 0 && i%8 == 0 {
						b.StopTimer()
						reset()
						b.StartTimer()
					}
					download()
				}
				b.StopTimer()
			})
		}
	}
}

// Each direction has one reader and one deadline worker. Fixed-delay FIFO
// scheduling preserves bursts rather than sleeping once per forwarded packet.
func qLatencyRelay(b *testing.B, target string, delay time.Duration) string {
	b.Helper()
	local, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		b.Fatal(err)
	}
	addr, err := net.ResolveUDPAddr("udp4", target)
	if err != nil {
		local.Close()
		b.Fatal(err)
	}
	remote, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		local.Close()
		b.Fatal(err)
	}
	type packet struct {
		data []byte
		due  time.Time
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	var peerMu sync.Mutex
	var peer *net.UDPAddr
	direction := func(read func([]byte) (int, error), write func([]byte) error) {
		packets := make(chan packet, 256)
		wg.Go(func() {
			defer close(packets)
			buf := make([]byte, 65535)
			for {
				n, err := read(buf)
				if err != nil {
					if !errors.Is(err, net.ErrClosed) {
						b.Errorf("read latency relay: %v", err)
					}
					return
				}
				p := packet{data: bytes.Clone(buf[:n]), due: time.Now().Add(delay)}
				select {
				case packets <- p:
				case <-done:
					return
				}
			}
		})
		wg.Go(func() {
			timer := time.NewTimer(time.Hour)
			defer timer.Stop()
			for {
				select {
				case <-done:
					return
				case p, ok := <-packets:
					if !ok {
						return
					}
					if wait := time.Until(p.due); wait > 0 {
						timer.Reset(wait)
						select {
						case <-timer.C:
						case <-done:
							return
						}
					}
					if err := write(p.data); err != nil {
						if !errors.Is(err, net.ErrClosed) {
							b.Errorf("write latency relay: %v", err)
						}
						return
					}
				}
			}
		})
	}
	direction(func(buf []byte) (int, error) {
		n, from, err := local.ReadFromUDP(buf)
		if err == nil {
			peerMu.Lock()
			peer = from
			peerMu.Unlock()
		}
		return n, err
	}, func(data []byte) error {
		_, err := remote.Write(data)
		return err
	})
	direction(remote.Read, func(data []byte) error {
		peerMu.Lock()
		to := peer
		peerMu.Unlock()
		if to == nil {
			return errors.New("latency relay received response without a request")
		}
		_, err := local.WriteToUDP(data, to)
		return err
	})
	b.Cleanup(func() {
		close(done)
		local.Close()
		remote.Close()
		wg.Wait()
	})
	return local.LocalAddr().String()
}
