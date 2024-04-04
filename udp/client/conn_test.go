package client_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/mux"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

const testNumParallel = 64

func bodyToBytes(t *testing.T, r io.Reader) []byte {
	t.Helper()
	buf := bytes.NewBuffer(nil)
	_, err := buf.ReadFrom(r)
	require.NoError(t, err)
	return buf.Bytes()
}

func TestConnDeduplication(t *testing.T) {
	l, err := coapNet.NewListenUDP("udp", "")
	require.NoError(t, err)
	defer func() {
		errC := l.Close()
		require.NoError(t, errC)
	}()
	var wg sync.WaitGroup
	defer wg.Wait()

	m := mux.NewRouter()

	var cnt atomic.Int32
	// The response counts up with every get
	// so we can check if the handler is only called once per message ID
	err = m.Handle("/count", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.GET, r.Code())
		cnt.Add(1)
		errH := w.SetResponse(codes.Content, message.AppOctets, bytes.NewReader([]byte{byte(cnt.Load())}))
		require.NoError(t, errH)
		require.NotEmpty(t, w.Conn())
	}))
	require.NoError(t, err)

	s := udp.NewServer(options.WithMux(m),
		options.WithErrors(func(err error) {
			require.NoError(t, err)
		}))
	defer s.Stop()
	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		assert.NoError(t, errS)
	}()

	cc, err := udp.Dial(l.LocalAddr().String(),
		options.WithErrors(func(err error) {
			require.NoError(t, err)
		}),
	)
	require.NoError(t, err)
	defer func() {
		errC := cc.Close()
		require.NoError(t, errC)
	}()

	// Setup done - Run Tests

	// Process several datagrams that reflect retries and block transfer with duplicate messages
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*60)
	defer cancel()

	var got *pool.Message

	// Same request, executed twice (needs the same token)
	getReq, err := cc.NewGetRequest(ctx, "/count")
	getReq.SetMessageID(1)

	require.NoError(t, err)
	got, err = cc.Do(getReq)
	require.NoError(t, err)
	require.Equal(t, codes.Content.String(), got.Code().String())
	ct, err := got.ContentFormat()
	require.NoError(t, err)
	require.Equal(t, message.AppOctets, ct)
	require.Equal(t, []byte{1}, bodyToBytes(t, got.Body()))

	// Do the same request with the same message ID
	// and expect the response from the first request
	got, err = cc.Do(getReq)
	require.NoError(t, err)
	require.Equal(t, codes.Content.String(), got.Code().String())
	ct, err = got.ContentFormat()
	require.NoError(t, err)
	require.Equal(t, message.AppOctets, ct)
	require.Equal(t, []byte{1}, bodyToBytes(t, got.Body()))
}

func TestConnDeduplicationRetransmission(t *testing.T) {
	l, err := coapNet.NewListenUDP("udp", "")
	require.NoError(t, err)
	defer func() {
		errC := l.Close()
		require.NoError(t, errC)
	}()
	var wg sync.WaitGroup
	defer wg.Wait()

	m := mux.NewRouter()

	var cnt atomic.Int32
	// The response counts up with every get
	// so we can check if the handler is only called once per message ID
	once := sync.Once{}
	err = m.Handle("/count", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.GET, r.Code())
		cnt.Add(1)
		errH := w.SetResponse(codes.Content, message.AppOctets, bytes.NewReader([]byte{byte(cnt.Load())}))
		require.NoError(t, errH)

		// Only one delay to trigger retransmissions
		once.Do(func() {
			<-time.After(200 * time.Millisecond)
		})
	}))
	require.NoError(t, err)

	s := udp.NewServer(options.WithMux(m),
		options.WithErrors(func(err error) {
			require.NoError(t, err)
		}),
	)
	defer s.Stop()
	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		assert.NoError(t, errS)
	}()

	cc, err := udp.Dial(l.LocalAddr().String(),
		options.WithErrors(func(err error) {
			require.NoError(t, err)
		}),
		options.WithTransmission(1, 100*time.Millisecond, 50),
	)
	require.NoError(t, err)
	defer func() {
		errC := cc.Close()
		require.NoError(t, errC)
	}()

	// Setup done - Run Tests

	// Process several datagrams that reflect retries and block transfer with duplicate messages
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*60)
	defer cancel()

	var got *pool.Message

	// First request should get "1" as result
	// and is repeated due to retransmission parameters
	got, err = cc.Get(ctx, "/count")
	require.NoError(t, err)
	require.Equal(t, codes.Content.String(), got.Code().String())
	ct, err := got.ContentFormat()
	require.NoError(t, err)
	require.Equal(t, message.AppOctets, ct)
	require.Equal(t, []byte{1}, bodyToBytes(t, got.Body()))

	// Second request should get "2" as result
	// verifying that the handler was not called on retransmissions
	got, err = cc.Get(ctx, "/count")
	require.NoError(t, err)
	require.Equal(t, codes.Content.String(), got.Code().String())
	ct, err = got.ContentFormat()
	require.NoError(t, err)
	require.Equal(t, message.AppOctets, ct)
	require.Equal(t, []byte{2}, bodyToBytes(t, got.Body()))
}

func testParallelConnGet(t *testing.T, numParallel int) {
	type args struct {
		path string
		opts message.Options
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
			name: "ok-a",
			args: args{
				path: "/a",
			},
			wantCode:          codes.BadRequest,
			wantContentFormat: &message.TextPlain,
			wantPayload:       make([]byte, 5330),
		},
		{
			name: "ok-b",
			args: args{
				path: "/b",
			},
			wantCode:          codes.Content,
			wantContentFormat: &message.TextPlain,
			wantPayload:       []byte("b"),
		},
		{
			name: "notfound",
			args: args{
				path: "/c",
			},
			wantCode: codes.NotFound,
		},
	}

	l, err := coapNet.NewListenUDP("udp", "")
	require.NoError(t, err)
	defer func() {
		errC := l.Close()
		require.NoError(t, errC)
	}()
	var wg sync.WaitGroup
	defer wg.Wait()

	m := mux.NewRouter()
	err = m.Handle("/a", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.GET, r.Code())
		errH := w.SetResponse(codes.BadRequest, message.TextPlain, bytes.NewReader(make([]byte, 5330)))
		require.NoError(t, errH)
		require.NotEmpty(t, w.Conn())
	}))
	require.NoError(t, err)
	err = m.Handle("/b", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.GET, r.Code())
		errH := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("b")))
		require.NoError(t, errH)
		require.NotEmpty(t, w.Conn())
	}))
	require.NoError(t, err)

	s := udp.NewServer(options.WithMux(m))
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		assert.NoError(t, errS)
	}()

	cc, err := udp.Dial(l.LocalAddr().String())
	require.NoError(t, err)
	defer func() {
		errC := cc.Close()
		require.NoError(t, errC)
	}()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var wg sync.WaitGroup
			wg.Add(numParallel)
			for i := 0; i < numParallel; i++ {
				go func() {
					defer wg.Done()
					ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
					defer cancel()
					got, err := cc.Get(ctx, tt.args.path, tt.args.opts...)
					if tt.wantErr {
						assert.Error(t, err)
						return
					}
					assert.NoError(t, err)
					assert.Equal(t, tt.wantCode, got.Code())
					if tt.wantContentFormat != nil {
						ct, err := got.ContentFormat()
						assert.NoError(t, err)
						assert.Equal(t, *tt.wantContentFormat, ct)
						buf := bytes.NewBuffer(nil)
						_, err = buf.ReadFrom(got.Body())
						assert.NoError(t, err)
						assert.Equal(t, tt.wantPayload, buf.Bytes())
					}
				}()
			}
			wg.Wait()
		})
	}
}

func TestParallelConnGet(t *testing.T) {
	testParallelConnGet(t, testNumParallel)
}

func TestConnGet(t *testing.T) {
	testParallelConnGet(t, 1)
}

func TestConnGetSeparateMessage(t *testing.T) {
	l, err := coapNet.NewListenUDP("udp", "")
	require.NoError(t, err)
	defer func() {
		errC := l.Close()
		require.NoError(t, errC)
	}()
	var wg sync.WaitGroup
	defer wg.Wait()

	m := mux.NewRouter()
	err = m.Handle("/a", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		r.Hijack()
		go func() {
			time.Sleep(time.Second * 1)
			assert.Equal(t, codes.GET, r.Code())
			customResp := message.Message{
				Code:    codes.Content,
				Token:   r.Token(),
				Options: make(message.Options, 0, 16),
				// Body:    bytes.NewReader(make([]byte, 10)),
			}
			optsBuf := make([]byte, 32)
			opts, used, errH := customResp.Options.SetContentFormat(optsBuf, message.TextPlain)
			if errors.Is(errH, message.ErrTooSmall) {
				optsBuf = append(optsBuf, make([]byte, used)...)
				opts, _, errH = customResp.Options.SetContentFormat(optsBuf, message.TextPlain)
			}
			if errH != nil {
				log.Printf("cannot set options to response: %v", errH)
				return
			}
			customResp.Options = opts
			resp := pool.NewMessage(r.Context())
			resp.SetMessage(customResp)

			errW := w.Conn().WriteMessage(resp)
			if errW != nil && !errors.Is(errW, context.Canceled) {
				log.Printf("cannot set response: %v", errW)
			}
		}()
	}))
	require.NoError(t, err)

	s := udp.NewServer(options.WithMux(m))
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		assert.NoError(t, errS)
	}()

	cc, err := udp.Dial(l.LocalAddr().String(), options.WithHandlerFunc(func(_ *responsewriter.ResponseWriter[*client.Conn], r *pool.Message) {
		require.Failf(t, "Unexpected msg", "Received unexpected message: %+v", r)
	}))
	require.NoError(t, err)
	defer func() {
		errC := cc.Close()
		require.NoError(t, errC)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()

	req, err := cc.NewGetRequest(ctx, "/a")
	require.NoError(t, err)
	req.SetType(message.Confirmable)
	req.SetMessageID(message.GetMID())
	resp, err := cc.Do(req)
	require.NoError(t, err)
	assert.Equal(t, codes.Content, resp.Code())
}

func testConnPost(t *testing.T, numParallel int) {
	type args struct {
		path          string
		contentFormat message.MediaType
		payload       io.ReadSeeker
		opts          message.Options
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
			name: "ok-a",
			args: args{
				path:          "/a",
				contentFormat: message.TextPlain,
				payload:       bytes.NewReader(make([]byte, 7000)),
			},
			wantCode:          codes.BadRequest,
			wantContentFormat: &message.TextPlain,
			wantPayload:       make([]byte, 5330),
		},
		{
			name: "ok-b",
			args: args{
				path:          "/b",
				contentFormat: message.TextPlain,
				payload:       bytes.NewReader([]byte("b-send")),
			},
			wantCode:          codes.Content,
			wantContentFormat: &message.TextPlain,
			wantPayload:       []byte("b"),
		},
		{
			name: "notfound",
			args: args{
				path:          "/c",
				contentFormat: message.TextPlain,
				payload:       bytes.NewReader(make([]byte, 21)),
			},
			wantCode: codes.NotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, err := coapNet.NewListenUDP("udp", "")
			require.NoError(t, err)
			defer func() {
				errC := l.Close()
				require.NoError(t, errC)
			}()
			var wg sync.WaitGroup
			defer wg.Wait()

			m := mux.NewRouter()
			err = m.Handle("/a", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
				assert.Equal(t, codes.POST, r.Code())
				ct, errH := r.Options().GetUint32(message.ContentFormat)
				require.NoError(t, errH)
				assert.Equal(t, message.TextPlain, message.MediaType(ct))
				buf, errH := io.ReadAll(r.Body())
				require.NoError(t, errH)
				assert.Len(t, buf, 7000)

				errH = w.SetResponse(codes.BadRequest, message.TextPlain, bytes.NewReader(make([]byte, 5330)))
				require.NoError(t, errH)
				require.NotEmpty(t, w.Conn())
			}))
			require.NoError(t, err)
			err = m.Handle("/b", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
				assert.Equal(t, codes.POST, r.Code())
				ct, errH := r.Options().GetUint32(message.ContentFormat)
				require.NoError(t, errH)
				assert.Equal(t, message.TextPlain, message.MediaType(ct))
				buf, errH := io.ReadAll(r.Body())
				require.NoError(t, errH)
				assert.Equal(t, buf, []byte("b-send"))
				errH = w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("b")))
				require.NoError(t, errH)
				require.NotEmpty(t, w.Conn())
			}))
			require.NoError(t, err)

			s := udp.NewServer(options.WithMux(m))
			defer s.Stop()

			wg.Add(1)
			go func() {
				defer wg.Done()
				errS := s.Serve(l)
				assert.NoError(t, errS)
			}()

			cc, err := udp.Dial(l.LocalAddr().String())
			require.NoError(t, err)
			defer func() {
				errC := cc.Close()
				require.NoError(t, errC)
			}()

			var wgNumParallel sync.WaitGroup
			wgNumParallel.Add(numParallel)
			for i := 0; i < numParallel; i++ {
				go func() {
					defer wgNumParallel.Done()
					ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
					defer cancel()
					got, err := cc.Post(ctx, tt.args.path, tt.args.contentFormat, tt.args.payload, tt.args.opts...)
					if tt.wantErr {
						assert.Error(t, err)
						return
					}
					assert.NoError(t, err)
					assert.Equal(t, tt.wantCode, got.Code())
					if tt.wantContentFormat != nil {
						ct, err := got.ContentFormat()
						assert.NoError(t, err)
						assert.Equal(t, *tt.wantContentFormat, ct)
						buf := bytes.NewBuffer(nil)
						_, err = buf.ReadFrom(got.Body())
						assert.NoError(t, err)
						assert.Equal(t, tt.wantPayload, buf.Bytes())
					}
				}()
			}
			wgNumParallel.Wait()
		})
	}
}

func TestConnPost(t *testing.T) {
	testConnPost(t, 1)
}

func TestParallelConnPost(t *testing.T) {
	testConnPost(t, testNumParallel)
}

func testConnPut(t *testing.T, numParallel int) {
	type args struct {
		path          string
		contentFormat message.MediaType
		payload       io.ReadSeeker
		opts          message.Options
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
			name: "ok-a",
			args: args{
				path:          "/a",
				contentFormat: message.TextPlain,
				payload:       bytes.NewReader(make([]byte, 7000)),
			},
			wantCode:          codes.BadRequest,
			wantContentFormat: &message.TextPlain,
			wantPayload:       make([]byte, 5330),
		},
		{
			name: "ok-b",
			args: args{
				path:          "/b",
				contentFormat: message.TextPlain,
				payload:       bytes.NewReader([]byte("b-send")),
			},
			wantCode:          codes.Content,
			wantContentFormat: &message.TextPlain,
			wantPayload:       []byte("b"),
		},
		{
			name: "notfound",
			args: args{
				path:          "/c",
				contentFormat: message.TextPlain,
				payload:       bytes.NewReader(make([]byte, 21)),
			},
			wantCode: codes.NotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, err := coapNet.NewListenUDP("udp", "")
			require.NoError(t, err)
			defer func() {
				errC := l.Close()
				require.NoError(t, errC)
			}()
			var wg sync.WaitGroup
			defer wg.Wait()

			m := mux.NewRouter()
			err = m.Handle("/a", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
				assert.Equal(t, codes.PUT, r.Code())
				ct, errH := r.Options().GetUint32(message.ContentFormat)
				require.NoError(t, errH)
				assert.Equal(t, message.TextPlain, message.MediaType(ct))
				buf, errH := io.ReadAll(r.Body())
				require.NoError(t, errH)
				assert.Len(t, buf, 7000)

				errH = w.SetResponse(codes.BadRequest, message.TextPlain, bytes.NewReader(make([]byte, 5330)))
				require.NoError(t, errH)
				require.NotEmpty(t, w.Conn())
			}))
			require.NoError(t, err)
			err = m.Handle("/b", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
				assert.Equal(t, codes.PUT, r.Code())
				ct, errH := r.Options().GetUint32(message.ContentFormat)
				require.NoError(t, errH)
				assert.Equal(t, message.TextPlain, message.MediaType(ct))
				buf, errH := io.ReadAll(r.Body())
				require.NoError(t, errH)
				assert.Equal(t, buf, []byte("b-send"))
				errH = w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("b")))
				require.NoError(t, errH)
				require.NotEmpty(t, w.Conn())
			}))
			require.NoError(t, err)

			s := udp.NewServer(options.WithMux(m))
			defer s.Stop()

			wg.Add(1)
			go func() {
				defer wg.Done()
				errS := s.Serve(l)
				assert.NoError(t, errS)
			}()

			cc, err := udp.Dial(l.LocalAddr().String())
			require.NoError(t, err)
			defer func() {
				errC := cc.Close()
				require.NoError(t, errC)
			}()
			var wgNumParallel sync.WaitGroup
			wgNumParallel.Add(numParallel)
			for i := 0; i < numParallel; i++ {
				go func() {
					defer wgNumParallel.Done()
					ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
					defer cancel()
					got, err := cc.Put(ctx, tt.args.path, tt.args.contentFormat, tt.args.payload, tt.args.opts...)
					if tt.wantErr {
						assert.Error(t, err)
						return
					}
					assert.NoError(t, err)
					assert.Equal(t, tt.wantCode, got.Code())
					if tt.wantContentFormat != nil {
						ct, err := got.ContentFormat()
						assert.NoError(t, err)
						assert.Equal(t, *tt.wantContentFormat, ct)
						buf := bytes.NewBuffer(nil)
						_, err = buf.ReadFrom(got.Body())
						assert.NoError(t, err)
						assert.Equal(t, tt.wantPayload, buf.Bytes())
					}
				}()
			}
			wgNumParallel.Wait()
		})
	}
}

func TestConnPut(t *testing.T) {
	testConnPut(t, 1)
}

func TestParallelConnPut(t *testing.T) {
	testConnPut(t, testNumParallel)
}

func testConnDelete(t *testing.T, numParallel int) {
	type args struct {
		path string
		opts message.Options
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
			name: "ok-a",
			args: args{
				path: "/a",
			},
			wantCode:          codes.BadRequest,
			wantContentFormat: &message.TextPlain,
			wantPayload:       make([]byte, 5330),
		},
		{
			name: "ok-b",
			args: args{
				path: "/b",
			},
			wantCode:          codes.Deleted,
			wantContentFormat: &message.TextPlain,
			wantPayload:       []byte("b"),
		},
		{
			name: "notfound",
			args: args{
				path: "/c",
			},
			wantCode: codes.NotFound,
		},
	}

	l, err := coapNet.NewListenUDP("udp", "")
	require.NoError(t, err)
	defer func() {
		errC := l.Close()
		require.NoError(t, errC)
	}()
	var wg sync.WaitGroup
	defer wg.Wait()

	m := mux.NewRouter()
	err = m.Handle("/a", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.DELETE, r.Code())
		errS := w.SetResponse(codes.BadRequest, message.TextPlain, bytes.NewReader(make([]byte, 5330)))
		require.NoError(t, errS)
		require.NotEmpty(t, w.Conn())
	}))
	require.NoError(t, err)
	err = m.Handle("/b", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.DELETE, r.Code())
		errS := w.SetResponse(codes.Deleted, message.TextPlain, bytes.NewReader([]byte("b")))
		require.NoError(t, errS)
		require.NotEmpty(t, w.Conn())
	}))
	require.NoError(t, err)

	s := udp.NewServer(options.WithMux(m))
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		assert.NoError(t, errS)
	}()

	cc, err := udp.Dial(l.LocalAddr().String())
	require.NoError(t, err)
	defer func() {
		errC := cc.Close()
		require.NoError(t, errC)
	}()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var wgNumParallel sync.WaitGroup
			wgNumParallel.Add(numParallel)
			for i := 0; i < numParallel; i++ {
				go func() {
					defer wgNumParallel.Done()
					ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
					defer cancel()
					got, err := cc.Delete(ctx, tt.args.path, tt.args.opts...)
					if tt.wantErr {
						assert.Error(t, err)
						return
					}
					assert.NoError(t, err)
					assert.Equal(t, tt.wantCode, got.Code())
					if tt.wantContentFormat != nil {
						ct, err := got.ContentFormat()
						assert.NoError(t, err)
						assert.Equal(t, *tt.wantContentFormat, ct)
						buf := bytes.NewBuffer(nil)
						_, err = buf.ReadFrom(got.Body())
						assert.NoError(t, err)
						assert.Equal(t, tt.wantPayload, buf.Bytes())
					}
				}()
			}
			wgNumParallel.Wait()
		})
	}
}

func TestConnDelete(t *testing.T) {
	testConnDelete(t, 1)
}

func TestParallelConnDelete(t *testing.T) {
	testConnDelete(t, testNumParallel)
}

func TestConnPing(t *testing.T) {
	l, err := coapNet.NewListenUDP("udp", "")
	require.NoError(t, err)
	defer func() {
		errC := l.Close()
		require.NoError(t, errC)
	}()
	var wg sync.WaitGroup
	defer wg.Wait()

	s := udp.NewServer()
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		assert.NoError(t, errS)
	}()

	cc, err := udp.Dial(l.LocalAddr().String())
	require.NoError(t, err)
	defer func() {
		errC := cc.Close()
		require.NoError(t, errC)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*200)
	defer cancel()
	err = cc.Ping(ctx)
	require.NoError(t, err)
}

func TestConnRequestMonitorCloseConnection(t *testing.T) {
	l, err := coapNet.NewListenUDP("udp", "")
	require.NoError(t, err)
	defer func() {
		errC := l.Close()
		require.NoError(t, errC)
	}()
	var wg sync.WaitGroup
	defer wg.Wait()

	m := mux.NewRouter()

	// The response counts up with every get
	// so we can check if the handler is only called once per message ID
	err = m.Handle("/test", mux.HandlerFunc(func(w mux.ResponseWriter, _ *mux.Message) {
		errH := w.SetResponse(codes.Content, message.TextPlain, nil)
		require.NoError(t, errH)
	}))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*8)
	defer cancel()
	ctxRegMonitor, cancelReqMonitor := context.WithCancel(ctx)
	defer cancelReqMonitor()
	reqMonitorErr := make(chan struct{}, 1)
	testEOFError := errors.New("test error")
	s := udp.NewServer(
		options.WithMux(m),
		options.WithRequestMonitor(func(_ *client.Conn, req *pool.Message) (bool, error) {
			if req.Code() == codes.DELETE {
				return false, testEOFError
			}
			return false, nil
		}),
		options.WithErrors(func(err error) {
			t.Log(err)
			if errors.Is(err, testEOFError) {
				if errors.Is(err, testEOFError) {
					select {
					case reqMonitorErr <- struct{}{}:
						cancelReqMonitor()
					default:
					}
				}
			}
		}),
		options.WithOnNewConn(func(c *client.Conn) {
			t.Log("new conn")
			c.AddOnClose(func() {
				t.Log("close conn")
			})
		}))
	defer s.Stop()
	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		assert.NoError(t, errS)
	}()

	cc, err := udp.Dial(l.LocalAddr().String(),
		options.WithErrors(func(err error) {
			t.Log(err)
		}),
	)
	require.NoError(t, err)
	defer func() {
		errC := cc.Close()
		require.NoError(t, errC)
	}()

	// Setup done - Run Tests
	// Same request, executed twice (needs the same token)
	getReq, err := cc.NewGetRequest(ctx, "/test")
	getReq.SetMessageID(1)

	require.NoError(t, err)
	got, err := cc.Do(getReq)
	require.NoError(t, err)
	require.Equal(t, codes.Content.String(), got.Code().String())

	// New request but with DELETE code to trigger EOF error from request monitor
	deleteReq, err := cc.NewDeleteRequest(ctxRegMonitor, "/test")
	require.NoError(t, err)
	deleteReq.SetMessageID(2)
	_, err = cc.Do(deleteReq)
	require.Error(t, err)
	select {
	case <-reqMonitorErr:
	case <-ctx.Done():
		require.Fail(t, "request monitor not called")
	}
}

func TestConnRequestMonitorDropRequest(t *testing.T) {
	l, err := coapNet.NewListenUDP("udp", "")
	require.NoError(t, err)
	defer func() {
		errC := l.Close()
		require.NoError(t, errC)
	}()
	var wg sync.WaitGroup
	defer wg.Wait()

	m := mux.NewRouter()

	// The response counts up with every get
	// so we can check if the handler is only called once per message ID
	err = m.Handle("/test", mux.HandlerFunc(func(w mux.ResponseWriter, _ *mux.Message) {
		errH := w.SetResponse(codes.Content, message.TextPlain, nil)
		require.NoError(t, errH)
	}))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
	defer cancel()
	s := udp.NewServer(
		options.WithMux(m),
		options.WithRequestMonitor(func(_ *client.Conn, req *pool.Message) (bool, error) {
			if req.Code() == codes.DELETE {
				t.Log("drop request")
				return true, nil
			}
			return false, nil
		}),
		options.WithErrors(func(err error) {
			require.NoError(t, err)
		}),
		options.WithOnNewConn(func(c *client.Conn) {
			t.Log("new conn")
			c.AddOnClose(func() {
				t.Log("close conn")
			})
		}))
	defer s.Stop()
	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		assert.NoError(t, errS)
	}()

	cc, err := udp.Dial(l.LocalAddr().String(),
		options.WithErrors(func(err error) {
			t.Log(err)
		}),
	)
	require.NoError(t, err)
	defer func() {
		errC := cc.Close()
		require.NoError(t, errC)
	}()

	// Setup done - Run Tests
	// Same request, executed twice (needs the same token)
	getReq, err := cc.NewGetRequest(ctx, "/test")
	getReq.SetMessageID(1)

	require.NoError(t, err)
	got, err := cc.Do(getReq)
	require.NoError(t, err)
	require.Equal(t, codes.Content.String(), got.Code().String())

	// New request but with DELETE code to trigger EOF error from request monitor
	deleteReq, err := cc.NewDeleteRequest(ctx, "/test")
	require.NoError(t, err)
	deleteReq.SetMessageID(2)
	_, err = cc.Do(deleteReq)
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
