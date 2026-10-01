package udp_test

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

func TestQBlockCapabilityProbeUDP(t *testing.T) {
	for _, classic := range []bool{false, true} {
		t.Run(map[bool]string{false: "classic_disabled", true: "classic_enabled"}[classic], func(t *testing.T) {
			peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			require.NoError(t, err)
			defer peer.Close()
			require.NoError(t, peer.SetDeadline(time.Now().Add(3*time.Second)))
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cc, err := udp.Dial(peer.LocalAddr().String(), options.WithContext(ctx), options.WithBlockwise(classic, blockwise.SZX16, time.Second))
			require.NoError(t, err)
			defer cc.Close()
			result := make(chan error, 1)
			go func() {
				supported, err := cc.ProbeQBlock(ctx, "/probe")
				if err == nil && !supported {
					err = net.ErrClosed
				}
				result <- err
			}()
			buf := make([]byte, 2048)
			n, addr, err := peer.ReadFromUDP(buf)
			require.NoError(t, err)
			req := pool.NewMessage(ctx)
			_, err = req.UnmarshalWithDecoder(qblock.Decoder{}, buf[:n])
			require.NoError(t, err)
			require.Equal(t, message.Confirmable, req.Type())
			require.Equal(t, codes.GET, req.Code())
			require.Nil(t, req.Body())
			path, err := req.Path()
			require.NoError(t, err)
			require.Equal(t, "/probe", path)
			value, err := req.GetOptionUint32(message.QBlock2)
			require.NoError(t, err)
			require.Zero(t, value)
			require.Len(t, req.Options(), 2)
			resp := pool.NewMessage(ctx)
			resp.SetType(message.Acknowledgement)
			resp.SetCode(codes.Content)
			resp.SetMessageID(req.MessageID())
			resp.SetToken(req.Token())
			resp.SetOptionUint32(message.QBlock2, 8)
			resp.SetOptionBytes(message.ETag, []byte{1})
			resp.SetOptionUint32(message.Size2, 1048576)
			resp.SetBody(bytes.NewReader([]byte("0123456789abcdef")))
			wire, err := resp.MarshalWithEncoder(coder.DefaultCoder)
			require.NoError(t, err)
			_, err = peer.WriteToUDP(wire, addr)
			require.NoError(t, err)
			require.NoError(t, <-result)
			// The next packet must be this explicitly issued ordinary GET, not a
			// continuation of the probe or an automatic switch to Q payload traffic.
			go func() {
				r, err := cc.Get(ctx, "/ordinary")
				if r != nil {
					cc.ReleaseMessage(r)
				}
				result <- err
			}()
			n, addr, err = peer.ReadFromUDP(buf)
			require.NoError(t, err)
			req = pool.NewMessage(ctx)
			_, err = req.UnmarshalWithDecoder(qblock.Decoder{}, buf[:n])
			require.NoError(t, err)
			path, err = req.Path()
			require.NoError(t, err)
			require.Equal(t, "/ordinary", path)
			require.False(t, req.HasOption(message.QBlock2))
			resp = pool.NewMessage(ctx)
			resp.SetType(message.Acknowledgement)
			resp.SetCode(codes.Content)
			resp.SetMessageID(req.MessageID())
			resp.SetToken(req.Token())
			resp.SetBody(bytes.NewReader([]byte("ordinary")))
			wire, err = resp.MarshalWithEncoder(coder.DefaultCoder)
			require.NoError(t, err)
			_, err = peer.WriteToUDP(wire, addr)
			require.NoError(t, err)
			require.NoError(t, <-result)
		})
	}
}
