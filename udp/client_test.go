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
	"github.com/stretchr/testify/require"
)

func TestClientConn_Get(t *testing.T) {
	type args struct {
		path    string
		queries []string
	}
	tests := []struct {
		name              string
		args              args
		wantCode          codes.Code
		wantContentFormat *message.MediaType
		wantPayload       interface{}
		wantErr           bool
	}{
		{
			name: "valid",
			args: args{
				path: "/oic/sec/session",
			},
			wantCode:          codes.BadRequest,
			wantContentFormat: &message.TextPlain,
			wantPayload:       make([]byte, 5330),
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

	cc, err := Dial(l.LocalAddr().String())
	require.NoError(t, err)
	defer cc.Close()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second*20)
			defer cancel()
			got, err := cc.Get(ctx, tt.args.path, tt.args.queries...)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantCode, got.Code())
			if tt.wantContentFormat != nil {
				ct, err := got.ContentFormat()
				require.NoError(t, err)
				require.Equal(t, *tt.wantContentFormat, ct)
				buf := bytes.NewBuffer(nil)
				_, err = buf.ReadFrom(got.Payload())
				require.NoError(t, err)
				require.Equal(t, tt.wantPayload, buf.Bytes())
			}
		})
	}

}

func TestClientConn_Ping(t *testing.T) {
	listenAddress, err := net.ResolveUDPAddr("udp", "")
	require.NoError(t, err)
	conn, err := net.ListenUDP("udp", listenAddress)
	require.NoError(t, err)
	l := coapNet.NewUDPConn(conn)
	require.NoError(t, err)
	defer l.Close()
	var wg sync.WaitGroup
	defer wg.Wait()

	s := NewServer(func(w *ResponseWriter, r *Message) {
		w.SetCode(codes.BadRequest)
		require.NotEmpty(t, w.ClientConn())
	})
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.Serve(l)
	}()

	cc, err := Dial(l.LocalAddr().String())
	require.NoError(t, err)
	defer cc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*200)
	defer cancel()
	err = cc.Ping(ctx)
	require.NoError(t, err)

	ctx, cancel = context.WithTimeout(context.Background(), time.Millisecond*4)
	defer cancel()
	err = cc.Ping(ctx)
	require.NoError(t, err)
}
