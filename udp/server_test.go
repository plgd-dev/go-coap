package udp

import (
	"bytes"
	"context"
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
	type args struct {
		timeout       time.Duration
		multicastAddr string
		path          string
		opts          []MulticastOption
	}
	tests := []struct {
		name    string
		args    args
		want    []*Message
		wantErr bool
	}{
		{
			name: "ok",
			args: args{
				timeout:       time.Millisecond * 100,
				multicastAddr: "224.0.1.187:5683",
				path:          "/oic/res",
			},
		},
	}

	l, err := coapNet.ListenUDP("udp", "")
	require.NoError(t, err)
	defer l.Close()
	var wg sync.WaitGroup
	defer wg.Wait()

	s := NewServer(func(w *ResponseWriter, r *Message) {
		w.SetCode(codes.BadRequest)
		w.WriteFrom(message.TextPlain, bytes.NewReader(make([]byte, 5330)))
		require.NotEmpty(t, w.ClientConn())
	})
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := s.Serve(l)
		t.Log(err)
	}()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tt.args.timeout)
			defer cancel()
			recv := &mcastreceiver{}
			err := s.Discover(ctx, tt.args.multicastAddr, tt.args.path, recv.process, tt.args.opts...)
			require.NoError(t, err)
			got := recv.pop()
			assert.Equal(t, tt.want, got)
		})
	}
}
