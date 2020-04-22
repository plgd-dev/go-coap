package tcp

import (
	"bytes"
	"context"
	"fmt"
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
		wantContentFormat message.MediaType
		wantPayload       interface{}
		wantErr           bool
	}{
		{
			name: "valid",
			args: args{
				path: "/oic/sec/session",
			},
			wantCode:          codes.BadRequest,
			wantContentFormat: message.TextPlain,
		},
	}

	l, err := coapNet.NewTCPListener("tcp", ":", time.Millisecond*100)
	require.NoError(t, err)
	defer l.Close()
	var wg sync.WaitGroup
	defer wg.Done()

	s := NewServer(func(w *ResponseWriter, r *Request) {
		fmt.Printf("TEST\n")
		w.SetCode(codes.BadRequest)
	})
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := s.Serve(l)
		require.NoError(t, err)
	}()

	cc, err := Dial(l.Addr().String())
	require.NoError(t, err)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			got, err := cc.Get(ctx, tt.args.path, tt.args.queries...)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantCode, got.Code())
			ct, err := got.ContentFormat()
			require.NoError(t, err)
			require.Equal(t, tt.wantContentFormat, ct)
			buf := bytes.NewBuffer(nil)
			err = got.GetPayload(buf)
			require.NoError(t, err)
			require.Equal(t, tt.wantPayload, string(buf.Bytes()))
		})
	}
}
