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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mcastreceiver struct {
	msgs []*Message
	sync.Mutex
}

func (m *mcastreceiver) process(cc *ClientConn, resp *Message) {
	m.Lock()
	defer m.Unlock()
	resp.Hijack()
	m.msgs = append(m.msgs, resp)
}

func (m *mcastreceiver) pop() []*Message {
	m.Lock()
	defer m.Unlock()
	r := m.msgs
	m.msgs = nil
	return r
}

func TestServer_Discover(t *testing.T) {
	timeout := time.Millisecond * 100
	multicastAddr := "224.0.1.187:5684"
	path := "/oic/res"

	l, err := coapNet.ListenUDP("udp", multicastAddr)
	require.NoError(t, err)
	defer l.Close()

	ifaces, err := net.Interfaces()
	require.NoError(t, err)

	a, err := net.ResolveUDPAddr("udp", multicastAddr)
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

	s := NewServer(WithHandler(func(w *ResponseWriter, r *Message) {
		w.SetResponse(codes.BadRequest, message.TextPlain, bytes.NewReader(make([]byte, 5330)))
		require.NotEmpty(t, w.ClientConn())
	}))
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := s.Serve(l)
		t.Log(err)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	recv := &mcastreceiver{}
	err = s.Discover(ctx, multicastAddr, path, recv.process)
	require.NoError(t, err)
	got := recv.pop()
	assert.Greater(t, len(got), 1)
	assert.Equal(t, codes.GET, got[0].Code())
}
