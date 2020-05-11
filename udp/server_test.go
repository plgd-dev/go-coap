package udp

import (
	"bytes"
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/go-ocf/go-coap/v2/message"
	"github.com/go-ocf/go-coap/v2/message/codes"
	coapNet "github.com/go-ocf/go-coap/v2/net"
	"github.com/go-ocf/go-coap/v2/udp/client"
	"github.com/go-ocf/go-coap/v2/udp/message/pool"
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

	s := NewServer(WithHandlerFunc(func(w *client.ResponseWriter, r *pool.Message) {
		w.SetResponse(codes.BadRequest, message.TextPlain, bytes.NewReader(make([]byte, 5330)))
		require.NotEmpty(t, w.ClientConn())
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
	assert.Equal(t, codes.BadRequest, got[0].Code())
}
