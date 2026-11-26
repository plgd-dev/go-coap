package server

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options/config"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

func TestEndpointMessageIDsEviction(t *testing.T) {
	ids := newEndpointMessageIDs(2)
	seeds := 0
	initial := func() uint32 {
		seeds++
		return uint32(seeds)
	}
	first := ids.get("first", initial)
	second := ids.get("second", initial)
	require.Same(t, first, ids.get("first", initial))
	third := ids.get("third", initial)
	require.NotSame(t, first, third)
	require.Same(t, first, ids.get("first", initial))
	require.NotSame(t, second, ids.get("second", initial))
	require.Equal(t, 4, seeds)
	require.Len(t, ids.entries, 2)
}

type endpointMessageIDLimitOption uint32

func (o endpointMessageIDLimitOption) UDPServerApply(cfg *Config) {
	cfg.MaxEndpointMessageIDs = uint32(o)
}

func TestServerEndpointMessageIDLimit(t *testing.T) {
	for _, limit := range []uint32{0, 1, 2} {
		t.Run(strconv.FormatUint(uint64(limit), 10), func(t *testing.T) {
			s := New(endpointMessageIDLimitOption(limit))
			defer s.Stop()
			initial := func() uint32 { return 100 }
			first := s.messageIDs.get("first", initial)
			first.Add(1)
			second := s.messageIDs.get("second", initial)
			require.NotSame(t, first, second)
			got := s.messageIDs.get("first", initial)
			if limit == 1 {
				require.NotSame(t, first, got)
				require.Equal(t, uint32(100), got.Load())
				require.Len(t, s.messageIDs.entries, 1)
			} else {
				require.Same(t, first, got)
				require.Equal(t, uint32(101), got.Load())
				require.Len(t, s.messageIDs.entries, 2)
			}
		})
	}
}

func TestEndpointMessageIDsUnlimited(t *testing.T) {
	ids := newEndpointMessageIDs(0)
	initial := func() uint32 { return 100 }
	first := ids.get("first", initial)
	for i := 0; i < int(DefaultMaxEndpointMessageIDs)+1; i++ {
		ids.get(strconv.Itoa(i), initial)
	}
	require.Len(t, ids.entries, int(DefaultMaxEndpointMessageIDs)+2)
	require.Same(t, first, ids.get("first", initial))
}

func TestServerMessageIDsSurviveReconnectAndOtherPeers(t *testing.T) {
	s := New()
	defer s.Stop()
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	local := listener.LocalAddr().(*net.UDPAddr)
	victim := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
	attacker := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12346}
	first, created := s.getOrCreateConn(listener, victim, local)
	require.True(t, created)
	var last int32
	previous := make(map[int32]bool)
	for range 20 {
		last = first.GetMessageID()
		previous[last] = true
	}
	getClose(first)()

	other, created := s.getOrCreateConn(listener, attacker, local)
	require.True(t, created)
	for range 1 << 16 {
		other.GetMessageID()
		message.GetMID()
	}

	reconnected, created := s.getOrCreateConn(listener, victim, local)
	require.True(t, created)
	for i := int32(1); i <= 20; i++ {
		id := reconnected.GetMessageID()
		require.Equal(t, (last+i)&0xffff, id)
		require.False(t, previous[id], "reconnected peer must not reuse a previous message ID")
	}
	getClose(reconnected)()
	getClose(other)()
}

func TestServerMessageIDsSharedAcrossLocalAddresses(t *testing.T) {
	s := New()
	defer s.Stop()
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	remote := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
	local1 := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5683}
	local2 := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 2), Port: 5683}
	first, created := s.getOrCreateConn(listener, remote, local1)
	require.True(t, created)
	second, created := s.getOrCreateConn(listener, remote, local2)
	require.True(t, created)
	last := first.GetMessageID()
	require.Equal(t, (last+1)&0xffff, second.GetMessageID())
	ids := make(chan int32, 2000)
	var wg sync.WaitGroup
	for _, conn := range []*client.Conn{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 1000 {
				ids <- conn.GetMessageID()
			}
		}()
	}
	wg.Wait()
	close(ids)
	seen := make(map[int32]bool)
	for id := range ids {
		require.False(t, seen[id])
		seen[id] = true
	}
	getClose(first)()
	getClose(second)()
}

func TestIncomingMessageIDsDoNotAdvanceSharedSequence(t *testing.T) {
	const errorMode = "error"
	for _, mode := range []string{"accepted", "dropped", errorMode} {
		for _, seed := range []uint32{1000, 65534} {
			t.Run(mode+"/"+strconv.FormatUint(uint64(seed), 10), func(t *testing.T) {
				s := New()
				defer s.Stop()
				completed := make(chan struct{}, 2)
				monitorErr := errors.New("monitor rejected request")
				s.cfg.RequestMonitor = func(_ *client.Conn, _ *pool.Message) (bool, error) {
					switch mode {
					case "dropped":
						return true, nil
					case errorMode:
						return false, monitorErr
					default:
						return false, nil
					}
				}
				s.cfg.Handler = func(w *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
					if err := w.SetResponse(codes.Content, message.TextPlain, nil); err != nil {
						t.Errorf("set response: %v", err)
					}
				}
				s.cfg.ProcessReceivedMessage = func(req *pool.Message, cc *client.Conn, handler config.HandlerFunc[*client.Conn]) {
					cc.ProcessReceivedMessageWithHandler(req, handler)
					completed <- struct{}{}
				}
				listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
				require.NoError(t, err)
				defer listener.Close()
				remote := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
				local1 := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5683}
				local2 := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 2), Port: 5683}
				s.messageIDs.get(remote.String(), func() uint32 { return seed })
				first, created := s.getOrCreateConn(listener, remote, local1)
				require.True(t, created)
				second, created := s.getOrCreateConn(listener, remote, local2)
				require.True(t, created)
				last := first.GetMessageID()
				req := second.AcquireMessage(context.Background())
				defer second.ReleaseMessage(req)
				req.SetType(message.Confirmable)
				req.SetCode(codes.GET)
				req.SetToken([]byte{1})
				for _, mid := range []int32{last, (last + 32767) & 0xffff} {
					req.SetMessageID(mid)
					datagram, err := req.MarshalWithEncoder(coder.DefaultCoder)
					require.NoError(t, err)
					err = second.Process(nil, datagram)
					if mode == errorMode {
						require.ErrorIs(t, err, monitorErr)
					} else {
						require.NoError(t, err)
					}
					if mode == "accepted" {
						select {
						case <-completed:
						case <-time.After(time.Second):
							t.Fatal("request was not processed")
						}
					}
				}
				require.Equal(t, (last+1)&0xffff, first.GetMessageID())
				require.Equal(t, (last+2)&0xffff, second.GetMessageID())
				getClose(first)()
				reconnected, created := s.getOrCreateConn(listener, remote, local1)
				require.True(t, created)
				require.Equal(t, (last+3)&0xffff, reconnected.GetMessageID())
				getClose(reconnected)()
				getClose(second)()
			})
		}
	}
}
