package udp_test

import (
	"bytes"
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	coapNet "github.com/plgd-dev/go-coap/v2/net"
	"github.com/plgd-dev/go-coap/v2/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v2/udp"
	"github.com/plgd-dev/go-coap/v2/udp/client"
	"github.com/plgd-dev/go-coap/v2/udp/message/pool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mcastreceiver struct {
	msgs []*pool.Message
	sync.Mutex
}

func (m *mcastreceiver) process(cc *client.ClientConn, resp *pool.Message) {
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
func TestServer_DiscoverIotivity(t *testing.T) {
	timeout := time.Millisecond * 500
	multicastAddr := "224.0.1.187:5683"
	path := "/oic/res"

	var wg sync.WaitGroup
	defer wg.Wait()

	ld, err := coapNet.NewListenUDP("udp4", "")
	require.NoError(t, err)
	defer ld.Close()

	sd := NewServer()
	defer sd.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := sd.Serve(ld)
		require.NoError(t, err)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	recv := &mcastreceiver{}
	err = sd.Discover(ctx, multicastAddr, path, recv.process)
	require.NoError(t, err)
	got := recv.pop()
	assert.Greater(t, len(got), 1)
	assert.Equal(t, codes.Content, got[0].Code())
	buf, err := ioutil.ReadAll(got[0].Body())
	require.NoError(t, err)
}
*/

func TestServer_Discover(t *testing.T) {
	timeout := time.Millisecond * 500
	multicastAddr := "224.0.1.187:5684"
	path := "/oic/res"

	l, err := coapNet.NewListenUDP("udp4", multicastAddr)
	require.NoError(t, err)
	defer l.Close()

	ifaces, err := net.Interfaces()
	require.NoError(t, err)

	a, err := net.ResolveUDPAddr("udp4", multicastAddr)
	require.NoError(t, err)

	for _, iface := range ifaces {
		err := l.JoinGroup(&iface, a)
		if err != nil {
			t.Logf("cannot JoinGroup(%v, %v): %v", iface, a, err)
		}
	}
	err = l.SetMulticastLoopback(true)
	require.NoError(t, err)

	var wg sync.WaitGroup
	defer wg.Wait()

	s := udp.NewServer(udp.WithHandlerFunc(func(w *client.ResponseWriter, r *pool.Message) {
		w.SetResponse(codes.BadRequest, message.TextPlain, bytes.NewReader(make([]byte, 5330)))
		require.NotNil(t, w.ClientConn())
	}))
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := s.Serve(l)
		require.NoError(t, err)
	}()

	ld, err := coapNet.NewListenUDP("udp4", "")
	require.NoError(t, err)
	defer ld.Close()

	sd := udp.NewServer()
	defer sd.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := sd.Serve(ld)
		require.NoError(t, err)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	recv := &mcastreceiver{}
	err = sd.Discover(ctx, multicastAddr, path, recv.process)
	require.NoError(t, err)
	got := recv.pop()
	assert.Greater(t, len(got), 1)
	assert.Equal(t, codes.BadRequest, got[0].Code())
}

func TestServer_CleanUpConns(t *testing.T) {
	ld, err := coapNet.NewListenUDP("udp4", "")
	require.NoError(t, err)
	defer ld.Close()

	var checkCloseWg sync.WaitGroup
	defer checkCloseWg.Wait()
	sd := udp.NewServer(udp.WithOnNewClientConn(func(cc *client.ClientConn) {
		checkCloseWg.Add(1)
		cc.AddOnClose(func() {
			checkCloseWg.Done()
		})
	}))
	defer sd.Stop()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := sd.Serve(ld)
		require.NoError(t, err)
	}()

	cc, err := udp.Dial(ld.LocalAddr().String())
	require.NoError(t, err)
	checkCloseWg.Add(1)
	cc.AddOnClose(func() {
		checkCloseWg.Done()
	})
	defer cc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = cc.Ping(ctx)
	require.NoError(t, err)
}

func TestServer_InactiveMonitor(t *testing.T) {
	inactivityDetected := false

	ld, err := coapNet.NewListenUDP("udp4", "")
	require.NoError(t, err)
	defer ld.Close()

	var checkCloseWg sync.WaitGroup
	defer checkCloseWg.Wait()
	sd := udp.NewServer(
		udp.WithOnNewClientConn(func(cc *client.ClientConn) {
			checkCloseWg.Add(1)
			cc.AddOnClose(func() {
				checkCloseWg.Done()
			})
		}),
		udp.WithInactivityMonitor(100*time.Millisecond, func(cc inactivity.ClientConn) {
			require.False(t, inactivityDetected)
			inactivityDetected = true
			cc.Close()
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
		err := sd.Serve(ld)
		require.NoError(t, err)
	}()

	cc, err := udp.Dial(
		ld.LocalAddr().String(),
	)
	require.NoError(t, err)
	checkCloseWg.Add(1)
	cc.AddOnClose(func() {
		checkCloseWg.Done()
	})

	// send ping to create serverside connection
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = cc.Ping(ctx)
	require.NoError(t, err)

	err = cc.Ping(ctx)
	require.NoError(t, err)

	// wait for fire inactivity
	time.Sleep(time.Second * 2)

	cc.Close()

	checkCloseWg.Wait()
	require.True(t, inactivityDetected)
}

func TestServer_KeepAliveMonitor(t *testing.T) {
	inactivityDetected := false

	ld, err := coapNet.NewListenUDP("udp4", "")
	require.NoError(t, err)
	defer ld.Close()

	var checkCloseWg sync.WaitGroup
	defer checkCloseWg.Wait()
	sd := udp.NewServer(
		udp.WithOnNewClientConn(func(cc *client.ClientConn) {
			checkCloseWg.Add(1)
			cc.AddOnClose(func() {
				checkCloseWg.Done()
			})
		}),
		udp.WithKeepAlive(3, 100*time.Millisecond, func(cc inactivity.ClientConn) {
			require.False(t, inactivityDetected)
			inactivityDetected = true
			cc.Close()
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
		err := sd.Serve(ld)
		require.NoError(t, err)
	}()

	cc, err := udp.Dial(
		ld.LocalAddr().String(),
		udp.WithInactivityMonitor(time.Millisecond*10, func(cc inactivity.ClientConn) {
			time.Sleep(time.Millisecond * 500)
			cc.Close()
		}),
	)
	require.NoError(t, err)
	checkCloseWg.Add(1)
	cc.AddOnClose(func() {
		checkCloseWg.Done()
	})

	// send ping to create serverside connection
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cc.Ping(ctx)

	checkCloseWg.Wait()
	require.True(t, inactivityDetected)
}
