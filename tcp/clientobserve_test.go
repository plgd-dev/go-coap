package tcp

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	"github.com/plgd-dev/go-coap/v2/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v2/net"
	"github.com/plgd-dev/go-coap/v2/net/responsewriter"
	"github.com/stretchr/testify/require"
)

func TestClientConnObserve(t *testing.T) {
	type args struct {
		path      string
		payload   []byte
		numEvents uint32
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "10bytes",
			args: args{
				path:      "/tmp",
				numEvents: 1,
				payload:   make([]byte, 10),
			},
		},
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
			defer func() {
				errC := l.Close()
				require.NoError(t, errC)
			}()
			var wg sync.WaitGroup
			defer wg.Wait()

			s := NewServer(WithHandlerFunc(func(w *responsewriter.ResponseWriter[*ClientConn], r *pool.Message) {
				switch r.Code() {
				case codes.PUT, codes.POST, codes.DELETE:
					errS := w.SetResponse(codes.NotFound, message.TextPlain, nil)
					require.NoError(t, errS)
				case codes.GET:
				default:
					return
				}
				obs, errO := r.Observe()
				if errO != nil {
					errS := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader(tt.args.payload))
					require.NoError(t, errS)
					return
				}
				require.NoError(t, errO)
				require.NotNil(t, w.ClientConn())
				token := r.Token()
				switch obs {
				case 0:
					cc := w.ClientConn()
					for i := uint32(0); i < tt.args.numEvents; i++ {
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
						etag, errE := message.GetETag(p)
						require.NoError(t, errE)
						req := cc.AcquireMessage(cc.Context())
						defer cc.ReleaseMessage(req)
						req.SetCode(codes.Content)
						req.SetContentFormat(message.TextPlain)
						req.SetObserve(i + 2)
						req.SetBody(p)
						errE = req.SetETag(etag)
						require.NoError(t, errE)
						req.SetToken(token)
						errW := cc.WriteMessage(req)
						require.NoError(t, errW)
					}
				case 1:
					errS := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("close")))
					require.NoError(t, errS)
				default:
					errS := w.SetResponse(codes.BadRequest, message.TextPlain, nil)
					require.NoError(t, errS)
				}
			}))
			defer s.Stop()

			wg.Add(1)
			go func() {
				defer wg.Done()
				errS := s.Serve(l)
				require.NoError(t, errS)
			}()

			cc, err := Dial(l.Addr().String())
			require.NoError(t, err)
			defer func() {
				errC := cc.Close()
				require.NoError(t, errC)
			}()

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
			require.NoError(t, err)
		})
	}
}

func TestClientConnObserveNotSupported(t *testing.T) {
	type args struct {
		path    string
		payload []byte
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "10bytes",
			args: args{
				path:    "/tmp",
				payload: make([]byte, 10),
			},
		},
		{
			name: "5000bytes",
			args: args{
				path:    "/tmp",
				payload: make([]byte, 5000),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, err := coapNet.NewTCPListener("tcp", "")
			require.NoError(t, err)
			defer func() {
				errC := l.Close()
				require.NoError(t, errC)
			}()
			var wg sync.WaitGroup
			defer wg.Wait()

			s := NewServer(WithHandlerFunc(func(w *responsewriter.ResponseWriter[*ClientConn], r *pool.Message) {
				switch r.Code() {
				case codes.PUT, codes.POST, codes.DELETE:
					errS := w.SetResponse(codes.NotFound, message.TextPlain, nil)
					require.NoError(t, errS)
				case codes.GET:
				default:
					return
				}
				obs, errO := r.Observe()
				if errO != nil {
					errS := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader(tt.args.payload))
					require.NoError(t, errS)
					return
				}
				require.NoError(t, errO)
				require.NotNil(t, w.ClientConn())
				token := r.Token()
				switch obs {
				case 0:
					cc := w.ClientConn()
					tmpPay := make([]byte, len(tt.args.payload))
					copy(tmpPay, tt.args.payload)
					p := bytes.NewReader(tt.args.payload)
					etag, errE := message.GetETag(p)
					require.NoError(t, errE)
					req := cc.AcquireMessage(cc.Context())
					defer cc.ReleaseMessage(req)
					req.SetCode(codes.Content)
					req.SetContentFormat(message.TextPlain)
					req.SetBody(p)
					errE = req.SetETag(etag)
					require.NoError(t, errE)
					req.SetToken(token)
					errW := cc.WriteMessage(req)
					require.NoError(t, errW)
				case 1:
					errS := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("close")))
					require.NoError(t, errS)
				default:
					errS := w.SetResponse(codes.BadRequest, message.TextPlain, nil)
					require.NoError(t, errS)
				}
			}))
			defer s.Stop()

			wg.Add(1)
			go func() {
				defer wg.Done()
				errS := s.Serve(l)
				require.NoError(t, errS)
			}()

			cc, err := Dial(l.Addr().String())
			require.NoError(t, err)
			defer func() {
				errC := cc.Close()
				require.NoError(t, errC)
			}()

			ctx, cancel := context.WithTimeout(context.Background(), time.Second*3600)
			defer cancel()
			obs := &observer{
				t:                        t,
				done:                     make(chan bool, 1),
				numEvents:                1,
				observeOptionNotRequired: true,
			}
			got, err := cc.Observe(ctx, tt.args.path, obs.observe)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			<-obs.done
			err = got.Cancel(ctx)
			require.NoError(t, err)
		})
	}
}

/*
func TestClientConnObserveIotivityLite(t *testing.T) {
	cc, err := Dial("10.112.112.10:60956")
	require.NoError(t, err)
	defer func() {
		errC := cc.Close()
		require.NoError(t, errC)
	}()

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
	done                     chan bool
	numEvents                uint32
	observeOptionNotRequired bool
}

func (o *observer) observe(req *pool.Message) {
	req.Hijack()
	o.Lock()
	defer o.Unlock()
	o.msgs = append(o.msgs, req)
	if o.observeOptionNotRequired {
		o.done <- true
		return
	}
	obs, err := req.Observe()
	require.NoError(o.t, err)
	if obs > o.numEvents {
		select {
		case o.done <- true:
		default:
		}
	}
}
