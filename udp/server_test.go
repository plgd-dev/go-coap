package udp_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"log"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/mux"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/options/config"
	"github.com/plgd-dev/go-coap/v3/pkg/runner/periodic"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/plgd-dev/go-coap/v3/udp/server"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	"golang.org/x/sync/semaphore"
)

type mcastreceiver struct {
	msgs []*pool.Message
	sync.Mutex
}

func (m *mcastreceiver) process(_ *client.Conn, resp *pool.Message) {
	m.Lock()
	defer m.Unlock()
	resp.Hijack()
	m.msgs = append(m.msgs, resp)
}

func (m *mcastreceiver) pop() []*pool.Message {
	m.Lock()
	defer m.Unlock()
	r := m.msgs
	m.msgs = nil
	return r
}

/*
func TestServerDiscoverIotivity(t *testing.T) {
	timeout := time.Millisecond * 500
	multicastAddr := "224.0.1.187:5683"
	path := "/oic/res"

	var wg sync.WaitGroup
	defer wg.Wait()

	ld, err := coapNet.NewListenUDP("udp4", "")
	require.NoError(t, err)
	defer func() {
		errC := ld.Close()
		require.NoError(t, errC)
	}()

	sd := NewServer()
	defer sd.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := sd.Serve(ld)
		require.NoError(t, errS)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	recv := &mcastreceiver{}
	err = sd.Discover(ctx, multicastAddr, path, recv.process)
	require.NoError(t, err)
	got := recv.pop()
	assert.Greater(t, len(got), 1)
	assert.Equal(t, codes.Content, got[0].Code())
	buf, err := io.ReadAll(got[0].Body())
	require.NoError(t, err)
}
*/

func TestServerDiscover(t *testing.T) {
	ifs, err := net.Interfaces()
	require.NoError(t, err)
	log.Printf("ifs:%v", ifs)
	var iface net.Interface
	for _, i := range ifs {
		if i.Flags&net.FlagMulticast == net.FlagMulticast && i.Flags&net.FlagUp == net.FlagUp && isRunningInterface(i) {
			iface = i
			log.Printf("first available multicast if:%v", iface)
			break
		}
	}
	require.NotEmpty(t, iface)

	type args struct {
		opts []coapNet.MulticastOption
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "valid any interface",
			args: args{
				opts: []coapNet.MulticastOption{coapNet.WithAnyMulticastInterface()},
			},
		},
		{
			name: "valid first interface",
			args: args{
				opts: []coapNet.MulticastOption{coapNet.WithMulticastInterface(iface)},
			},
		},
		{
			name: "valid all interfaces",
			args: args{
				opts: []coapNet.MulticastOption{coapNet.WithAllMulticastInterface()},
			},
		},
	}

	timeout := time.Millisecond * 500
	multicastAddr := "224.0.1.187:9999"
	path := "/oic/res"

	l, err := coapNet.NewListenUDP("udp4", multicastAddr)
	require.NoError(t, err)
	defer func() {
		errC := l.Close()
		require.NoError(t, errC)
	}()

	ifaces, err := net.Interfaces()
	require.NoError(t, err)

	a, err := net.ResolveUDPAddr("udp4", multicastAddr)
	require.NoError(t, err)

	for i := range ifaces {
		iface := ifaces[i]
		errJ := l.JoinGroup(&iface, a)
		if errJ != nil {
			t.Logf("cannot JoinGroup(%v, %v): %v", iface, a, errJ)
		}
	}
	err = l.SetMulticastLoopback(true)
	require.NoError(t, err)

	var wg sync.WaitGroup
	defer wg.Wait()

	s := udp.NewServer(options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
		errS := w.SetResponse(codes.BadRequest, message.TextPlain, bytes.NewReader(make([]byte, 5330)))
		require.NoError(t, errS)
		require.NotNil(t, w.Conn())
	}))
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		require.NoError(t, errS)
	}()

	ld, err := coapNet.NewListenUDP("udp4", "")
	require.NoError(t, err)
	defer func() {
		errC := ld.Close()
		require.NoError(t, errC)
	}()

	sd := udp.NewServer()
	defer sd.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := sd.Serve(ld)
		require.NoError(t, errS)
	}()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			recv := &mcastreceiver{}
			err = sd.Discover(ctx, multicastAddr, path, recv.process, tt.args.opts...)
			require.NoError(t, err)
			got := recv.pop()
			require.NotEmpty(t, got)
			require.Equal(t, codes.BadRequest, got[0].Code())
		})
	}
}

func TestServerCleanUpConns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	ld, err := coapNet.NewListenUDP("udp4", "")
	require.NoError(t, err)
	defer func() {
		errC := ld.Close()
		require.NoError(t, errC)
	}()

	checkClose := semaphore.NewWeighted(2)
	err = checkClose.Acquire(ctx, 2)
	require.NoError(t, err)
	defer func() {
		errA := checkClose.Acquire(ctx, 2)
		require.NoError(t, errA)
	}()

	sd := udp.NewServer(options.WithOnNewConn(func(cc *client.Conn) {
		cc.AddOnClose(func() {
			checkClose.Release(1)
		})
	}))
	defer sd.Stop()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := sd.Serve(ld)
		require.NoError(t, errS)
	}()

	cc, err := udp.Dial(ld.LocalAddr().String())
	require.NoError(t, err)
	cc.AddOnClose(func() {
		checkClose.Release(1)
	})
	defer func() {
		errC := cc.Close()
		require.NoError(t, errC)
		<-cc.Done()
	}()
	err = cc.Ping(ctx)
	require.NoError(t, err)
}

func TestServerInactiveMonitor(t *testing.T) {
	var inactivityDetected atomic.Bool

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*8)
	defer cancel()

	ld, err := coapNet.NewListenUDP("udp4", "")
	require.NoError(t, err)
	defer func() {
		errC := ld.Close()
		require.NoError(t, errC)
	}()

	checkClose := semaphore.NewWeighted(2)
	err = checkClose.Acquire(ctx, 2)
	require.NoError(t, err)
	sd := udp.NewServer(
		options.WithOnNewConn(func(cc *client.Conn) {
			cc.AddOnClose(func() {
				checkClose.Release(1)
			})
		}),
		options.WithInactivityMonitor(100*time.Millisecond, func(cc *client.Conn) {
			require.False(t, inactivityDetected.Load())
			inactivityDetected.Store(true)
			errC := cc.Close()
			require.NoError(t, errC)
		}),
		options.WithPeriodicRunner(periodic.New(ctx.Done(), time.Millisecond*10)),
		options.WithReceivedMessageQueueSize(32),
		options.WithProcessReceivedMessageFunc(func(req *pool.Message, cc *client.Conn, handler config.HandlerFunc[*client.Conn]) {
			cc.ProcessReceivedMessageWithHandler(req, handler)
		}),
	)

	var serverWg sync.WaitGroup
	defer func() {
		sd.Stop()
		serverWg.Wait()
	}()
	serverWg.Add(1)
	go func() {
		defer serverWg.Done()
		errS := sd.Serve(ld)
		require.NoError(t, errS)
	}()

	cc, err := udp.Dial(
		ld.LocalAddr().String(),
	)
	require.NoError(t, err)
	cc.AddOnClose(func() {
		checkClose.Release(1)
	})

	// send ping to create serverside connection
	ctxPing, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	err = cc.Ping(ctxPing)
	require.NoError(t, err)

	err = cc.Ping(ctxPing)
	require.NoError(t, err)

	// wait for fire inactivity
	time.Sleep(time.Second * 2)

	err = cc.Close()
	require.NoError(t, err)
	<-cc.Done()

	err = checkClose.Acquire(ctx, 2)
	require.NoError(t, err)
	require.True(t, inactivityDetected.Load())
}

func TestServerKeepAliveMonitor(t *testing.T) {
	var inactivityDetected atomic.Bool

	ld, err := coapNet.NewListenUDP("udp4", "")
	require.NoError(t, err)
	defer func() {
		errC := ld.Close()
		require.NoError(t, errC)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*8)
	defer cancel()

	checkClose := semaphore.NewWeighted(2)
	err = checkClose.Acquire(ctx, 2)
	require.NoError(t, err)
	sd := udp.NewServer(
		options.WithOnNewConn(func(cc *client.Conn) {
			cc.AddOnClose(func() {
				checkClose.Release(1)
			})
		}),
		options.WithKeepAlive(3, 100*time.Millisecond, func(cc *client.Conn) {
			require.False(t, inactivityDetected.Load())
			inactivityDetected.Store(true)
			errC := cc.Close()
			require.NoError(t, errC)
		}),
		options.WithPeriodicRunner(periodic.New(ctx.Done(), time.Millisecond*10)),
	)

	var serverWg sync.WaitGroup
	defer func() {
		sd.Stop()
		serverWg.Wait()
	}()
	serverWg.Add(1)
	go func() {
		defer serverWg.Done()
		errS := sd.Serve(ld)
		require.NoError(t, errS)
	}()

	cc, err := udp.Dial(
		ld.LocalAddr().String(),
		options.WithInactivityMonitor(time.Millisecond*10, func(cc *client.Conn) {
			time.Sleep(time.Millisecond * 500)
			errC := cc.Close()
			require.NoError(t, errC)
		}),
		options.WithPeriodicRunner(periodic.New(ctx.Done(), time.Millisecond*10)),
	)
	require.NoError(t, err)
	cc.AddOnClose(func() {
		checkClose.Release(1)
	})

	// send ping to create serverside connection
	ctx, cancel = context.WithTimeout(ctx, time.Second)
	defer cancel()
	err = cc.Ping(ctx)
	require.NoError(t, err)

	err = checkClose.Acquire(ctx, 2)
	require.NoError(t, err)
	require.True(t, inactivityDetected.Load())
}

func TestServerNewClient(t *testing.T) {
	newServer := func(l *coapNet.UDPConn) (*server.Server, func()) {
		var wg sync.WaitGroup
		s := udp.NewServer()
		wg.Add(1)
		go func() {
			defer wg.Done()
			errS := s.Serve(l)
			require.NoError(t, errS)
		}()
		return s, func() {
			s.Stop()
			wg.Wait()
		}
	}

	l, err := coapNet.NewListenUDP("udp", "[::1]:0")
	require.NoError(t, err)
	defer func() {
		errC := l.Close()
		require.NoError(t, errC)
	}()
	_, server0Shutdown := newServer(l)
	defer server0Shutdown()

	l1, err := coapNet.NewListenUDP("udp", "[::1]:0")
	require.NoError(t, err)
	defer func() {
		errC := l1.Close()
		require.NoError(t, errC)
	}()

	s1, server1shutdown := newServer(l1)
	defer server1shutdown()

	peer, err := net.ResolveUDPAddr("udp", l.LocalAddr().String())
	require.NoError(t, err)

	time.Sleep(time.Second)

	cc, err := s1.NewConn(peer)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*1)
	defer cancel()
	err = cc.Ping(ctx)
	require.NoError(t, err)
	err = cc.Close()
	require.NoError(t, err)

	// repeat ping - new client should be created
	cc, err = s1.NewConn(peer)
	require.NoError(t, err)
	err = cc.Ping(ctx)
	require.NoError(t, err)
}

func TestCheckForLossOrder(t *testing.T) {
	ld, err := coapNet.NewListenUDP("udp4", "")
	require.NoError(t, err)
	defer func() {
		errC := ld.Close()
		require.NoError(t, errC)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*8)
	defer cancel()

	const numMessages = 1000
	arrivedMessages := make([]uint64, 0, numMessages)
	var arrivedMessagesLock sync.Mutex

	sd := udp.NewServer(options.WithHandlerFunc(func(resp *responsewriter.ResponseWriter[*client.Conn], req *pool.Message) {
		errH := resp.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("1234")))
		require.NoError(t, errH)
		arrivedMessagesLock.Lock()
		defer arrivedMessagesLock.Unlock()
		arrivedMessages = append(arrivedMessages, binary.LittleEndian.Uint64(req.Token()))
	}))

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := sd.Serve(ld)
		require.NoError(t, errS)
	}()
	defer func() {
		sd.Stop()
		wg.Wait()
	}()

	cc, err := udp.Dial(ld.LocalAddr().String())
	require.NoError(t, err)
	defer func() {
		errC := cc.Close()
		require.NoError(t, errC)
		<-cc.Done()
	}()
	for i := 0; i < numMessages; i++ {
		p, err := cc.NewGetRequest(ctx, "/tmp")
		require.NoError(t, err)
		bs := make([]byte, 8)
		binary.LittleEndian.PutUint64(bs, uint64(i))
		p.SetToken(bs)
		_, err = cc.Do(p)
		require.NoError(t, err)
	}
	arrivedMessagesLock.Lock()
	defer arrivedMessagesLock.Unlock()
	require.Len(t, arrivedMessages, numMessages)
	for idx, v := range arrivedMessages {
		require.Equal(t, uint64(idx), v)
	}
}

// The objective of the test is to confirm the system's behavior during reconnections.
// It specifically examines situations where a connection (cc) is established but eventually
// closed due to inactivity. Subsequently, a new cc is created. The test focuses on verifying
// the system's ability to handle cases where the message ID of the new cc is no longer
// initialized with the same value as the previous cc. The desired outcome is for the receiver
// to correctly identify the distinctiveness of the message ID and effectively process the
// incoming message without any instances of message loss.
func TestServerReconnectNewClient(t *testing.T) {
	newServer := func(l *coapNet.UDPConn, closedConn chan struct{}) (*server.Server, func()) {
		var wg sync.WaitGroup
		var opts []server.Option
		if closedConn != nil {
			opts = append(opts, options.WithInactivityMonitor(time.Second, func(cc *client.Conn) {
				log.Printf("inactivityMonitor")
				errC := cc.Close()
				require.NoError(t, errC)
				closedConn <- struct{}{}
			}))
		}
		s := udp.NewServer(opts...)
		wg.Add(1)
		go func() {
			defer wg.Done()
			errS := s.Serve(l)
			require.NoError(t, errS)
		}()
		return s, func() {
			s.Stop()
			wg.Wait()
		}
	}

	l, err := coapNet.NewListenUDP("udp", "[::1]:0")
	require.NoError(t, err)
	defer func() {
		errC := l.Close()
		require.NoError(t, errC)
	}()
	_, server0Shutdown := newServer(l, nil)
	defer server0Shutdown()

	l1, err := coapNet.NewListenUDP("udp", "[::1]:0")
	require.NoError(t, err)
	defer func() {
		errC := l1.Close()
		require.NoError(t, errC)
	}()

	connClosed := make(chan struct{}, 1)
	s1, server1shutdown := newServer(l1, connClosed)
	defer server1shutdown()

	peer, err := net.ResolveUDPAddr("udp", l.LocalAddr().String())
	require.NoError(t, err)

	time.Sleep(time.Second)

	cc, err := s1.NewConn(peer)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*1)
	defer cancel()
	// increment messageID
	_, err = cc.Get(ctx, "/tmp")
	require.NoError(t, err)
	// increment messageID
	_, err = cc.Get(ctx, "/tmp")
	// increment messageID
	require.NoError(t, err)
	// increment messageID
	_, err = cc.Get(ctx, "/tmp")
	require.NoError(t, err)
	// increment messageID
	_, err = cc.Get(ctx, "/tmp")
	require.NoError(t, err)
	<-connClosed

	checkMessageID := make(map[int32]bool)
	for i := 0; i < 100; i++ {
		checkMessageID[cc.GetMessageID()] = true
	}

	// new client
	cc, err = s1.NewConn(peer)
	require.NoError(t, err)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second*1)
	defer cancel()
	// test if get is successful
	_, err = cc.Get(ctx, "/tmp")
	require.NoError(t, err)
	for i := 0; i < 100; i++ {
		require.False(t, checkMessageID[cc.GetMessageID()])
	}
}

func TestMuxPath(t *testing.T) {
	r := mux.NewRouter()
	err := r.Handle("/", mux.HandlerFunc(handleA))
	require.NoError(t, err)
	err = r.Handle("/a", mux.HandlerFunc(handleA))
	require.NoError(t, err)

	l, err := coapNet.NewListenUDP("udp", ":5688")
	require.NoError(t, err)
	defer func() {
		errC := l.Close()
		require.NoError(t, errC)
	}()
	s := udp.NewServer(options.WithMux(r))
	go func() {
		errL := s.Serve(l)
		require.NoError(t, errL)
	}()

	type args struct {
		path string
	}
	tests := []struct {
		name     string
		args     args
		wantCode codes.Code
	}{
		{
			name:     "empty path",
			args:     args{path: ""},
			wantCode: codes.Content,
		},
		{
			name:     "root path",
			args:     args{path: "/"},
			wantCode: codes.Content,
		},
		{
			name:     "a path",
			args:     args{path: "/a"},
			wantCode: codes.Content,
		},
		{
			name:     "not found path",
			args:     args{path: "/b"},
			wantCode: codes.NotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testPath(t, tt.args.path, tt.wantCode)
		})
	}
}

func testPath(t *testing.T, path string, expCode codes.Code) {
	co, err := udp.Dial("localhost:5688")
	require.NoError(t, err)
	defer func() {
		errC := co.Close()
		require.NoError(t, errC)
	}()
	resp, err := co.Get(context.Background(), path)
	require.NoError(t, err)
	require.Equal(t, expCode, resp.Code())
}

func handleA(w mux.ResponseWriter, _ *mux.Message) {
	err := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("hello world")))
	if err != nil {
		log.Printf("cannot set response: %v", err)
	}
}
