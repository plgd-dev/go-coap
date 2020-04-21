package tcp

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/go-ocf/go-coap/v2/message"

	"github.com/go-ocf/go-coap/v2/message/codes"

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
			wantCode:          codes.Forbidden,
			wantContentFormat: message.TextPlain,
		},
	}

	cc, err := Dial("127.0.0.1:5683")
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
			ct, errCode := got.ContentFormat()
			require.Equal(t, message.OK, errCode)
			require.Equal(t, tt.wantContentFormat, ct)
			buf := bytes.NewBuffer(nil)
			err = got.GetPayload(buf)
			require.NoError(t, err)
			require.Equal(t, tt.wantPayload, string(buf.Bytes()))
		})
	}
}
