package tcp

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	coapNet "github.com/plgd-dev/go-coap/v2/net"
	"github.com/plgd-dev/go-coap/v2/tcp/message/pool"
	"github.com/stretchr/testify/require"
)

func TestClientConn_Observe(t *testing.T) {
	type args struct {
		path      string
		payload   []byte
		numEvents int
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		/*
			{
				name: "10bytes",
				args: args{
					path:      "/tmp",
					numEvents: 1,
					payload:   make([]byte, 10),
				},
			},
		*/
		{
			name: "5000bytes",
			args: args{
				path:      "/tmp",
				numEvents: 20,
				payload:   make([]byte, 5000),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, err := coapNet.NewTCPListener("tcp", "")
			require.NoError(t, err)
			defer l.Close()
			var wg sync.WaitGroup
			defer wg.Wait()

			s := NewServer(WithHandlerFunc(func(w *ResponseWriter, r *pool.Message) {
				switch r.Code() {
				case codes.PUT, codes.POST, codes.DELETE:
					w.SetResponse(codes.NotFound, message.TextPlain, nil)
				case codes.GET:
				default:
					return
				}
				obs, err := r.Observe()
				if err != nil {
					err := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader(tt.args.payload))
					require.NoError(t, err)
					return
				}
				require.NoError(t, err)
				require.NotNil(t, w.ClientConn())
				token := r.Token()
				switch obs {
				case 0:
					cc := w.ClientConn()
					for i := 0; i < tt.args.numEvents; i++ {
						tmpPay := make([]byte, len(tt.args.payload))
						copy(tmpPay, tt.args.payload)
						if len(tmpPay) > 0 {
							if i == tt.args.numEvents-1 {
								tmpPay[0] = 0
							} else {
								tmpPay[0] = byte(i) + 1
							}
						}
						p := bytes.NewReader(tt.args.payload)
						etag, err := message.GetETag(p)
						require.NoError(t, err)
						req := pool.AcquireMessage(cc.Context())
						defer pool.ReleaseMessage(req)
						req.SetCode(codes.Content)
						req.SetContentFormat(message.TextPlain)
						req.SetObserve(uint32(i) + 2)
						req.SetBody(p)
						req.SetETag(etag)
						req.SetToken(token)
						err = cc.WriteMessage(req)
						require.NoError(t, err)
					}
				case 1:
					err := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("close")))
					require.NoError(t, err)
				default:
					err := w.SetResponse(codes.BadRequest, message.TextPlain, nil)
					require.NoError(t, err)
				}
			}))
			defer s.Stop()

			wg.Add(1)
			go func() {
				defer wg.Done()
				err := s.Serve(l)
				require.NoError(t, err)
			}()

			cc, err := Dial(l.Addr().String())
			require.NoError(t, err)
			defer cc.Close()

			ctx, cancel := context.WithTimeout(context.Background(), time.Second*3600)
			defer cancel()
			obs := &observer{
				t:         t,
				done:      make(chan bool, 1),
				numEvents: tt.args.numEvents,
			}
			got, err := cc.Observe(ctx, tt.args.path, obs.observe)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			<-obs.done
			err = got.Cancel(ctx)
		})
	}
}

/*
func TestClientConn_ObserveIotivityLite(t *testing.T) {
	cc, err := Dial("10.112.112.10:60956")
	require.NoError(t, err)
	defer cc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
	defer cancel()
	obs, err := cc.Observe(ctx, "/light/2", func(cc *ClientConn, req *pool.Message) {
		fmt.Printf("observe %+v\n", req)
	})
	require.NoError(t, err)
	fmt.Printf("Running\n")
	time.Sleep(time.Second * 3600)

	defer obs.Cancel(context.Background())
}
*/

type observer struct {
	t    require.TestingT
	msgs []*pool.Message
	sync.Mutex
	done      chan bool
	numEvents int
}

func (o *observer) observe(req *pool.Message) {
	req.Hijack()
	o.Lock()
	defer o.Unlock()
	o.msgs = append(o.msgs, req)
	obs, err := req.Observe()
	require.NoError(o.t, err)
	fmt.Printf("OBS %v\n", obs)
	if obs > uint32(o.numEvents) {
		select {
		case o.done <- true:
		default:
		}
	}
}
