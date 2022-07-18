package client_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	"github.com/plgd-dev/go-coap/v2/message/pool"
	"github.com/plgd-dev/go-coap/v2/mux"
	coapNet "github.com/plgd-dev/go-coap/v2/net"
	"github.com/plgd-dev/go-coap/v2/net/responsewriter"
	"github.com/plgd-dev/go-coap/v2/udp"
	"github.com/plgd-dev/go-coap/v2/udp/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func bodyToBytes(t *testing.T, r io.Reader) []byte {
	t.Helper()
	buf := bytes.NewBuffer(nil)
	_, err := buf.ReadFrom(r)
	require.NoError(t, err)
	return buf.Bytes()
}

func TestClientConnDeduplication(t *testing.T) {
	l, err := coapNet.NewListenUDP("udp", "")
	require.NoError(t, err)
	defer func() {
		errC := l.Close()
		require.NoError(t, errC)
	}()
	var wg sync.WaitGroup
	defer wg.Wait()

	m := mux.NewRouter()

	cnt := int32(0)
	// The response counts up with every get
	// so we can check if the handler is only called once per message ID
	err = m.Handle("/count", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.GET, r.Code())
		atomic.AddInt32(&cnt, 1)
		errH := w.SetResponse(codes.Content, message.AppOctets, bytes.NewReader([]byte{byte(cnt)}))
		require.NoError(t, errH)
		require.NotEmpty(t, w.Client())
	}))
	require.NoError(t, err)

	s := udp.NewServer(udp.WithMux(m),
		udp.WithErrors(func(err error) {
			require.NoError(t, err)
		}))
	defer s.Stop()
	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		require.NoError(t, errS)
	}()

	cc, err := udp.Dial(l.LocalAddr().String(),
		udp.WithErrors(func(err error) {
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
	getReq, err := client.NewGetRequest(ctx, pool.New(0, 0), "/count")
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

func TestClientConnDeduplicationRetransmission(t *testing.T) {
	l, err := coapNet.NewListenUDP("udp", "")
	require.NoError(t, err)
	defer func() {
		errC := l.Close()
		require.NoError(t, errC)
	}()
	var wg sync.WaitGroup
	defer wg.Wait()

	m := mux.NewRouter()

	cnt := int32(0)
	// The response counts up with every get
	// so we can check if the handler is only called once per message ID
	once := sync.Once{}
	err = m.Handle("/count", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.GET, r.Code())
		atomic.AddInt32(&cnt, 1)
		errH := w.SetResponse(codes.Content, message.AppOctets, bytes.NewReader([]byte{byte(cnt)}))
		require.NoError(t, errH)
		require.NotEmpty(t, w.Client())

		// Only one delay to trigger retransmissions
		once.Do(func() {
			<-time.After(200 * time.Millisecond)
		})
	}))
	require.NoError(t, err)

	s := udp.NewServer(udp.WithMux(m),
		udp.WithErrors(func(err error) {
			require.NoError(t, err)
		}),
	)
	defer s.Stop()
	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		require.NoError(t, errS)
	}()

	cc, err := udp.Dial(l.LocalAddr().String(),
		udp.WithErrors(func(err error) {
			require.NoError(t, err)
		}),
		udp.WithTransmission(20*time.Millisecond, 100*time.Millisecond, 50),
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

func TestClientConnGet(t *testing.T) {
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
		require.NotEmpty(t, w.Client())
	}))
	require.NoError(t, err)
	err = m.Handle("/b", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.GET, r.Code())
		errH := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("b")))
		require.NoError(t, errH)
		require.NotEmpty(t, w.Client())
	}))
	require.NoError(t, err)

	s := udp.NewServer(udp.WithMux(m))
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		require.NoError(t, errS)
	}()

	cc, err := udp.Dial(l.LocalAddr().String())
	require.NoError(t, err)
	defer func() {
		errC := cc.Close()
		require.NoError(t, errC)
	}()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second*3600)
			defer cancel()
			got, err := cc.Get(ctx, tt.args.path, tt.args.opts...)
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
				_, err = buf.ReadFrom(got.Body())
				require.NoError(t, err)
				require.Equal(t, tt.wantPayload, buf.Bytes())
			}
		})
	}
}

func TestClientConnGetSeparateMessage(t *testing.T) {
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

			errW := w.Client().WriteMessage(resp)
			if errW != nil && !errors.Is(errW, context.Canceled) {
				log.Printf("cannot set response: %v", errW)
			}
		}()
	}))
	require.NoError(t, err)

	s := udp.NewServer(udp.WithMux(m))
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		require.NoError(t, errS)
	}()

	cc, err := udp.Dial(l.LocalAddr().String(), udp.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.ClientConn], r *pool.Message) {
		assert.NoError(t, fmt.Errorf("none msg expected comes: %+v", r))
	}))
	require.NoError(t, err)
	defer func() {
		errC := cc.Close()
		require.NoError(t, errC)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3600)
	defer cancel()

	req, err := client.NewGetRequest(ctx, pool.New(0, 0), "/a")
	require.NoError(t, err)
	req.SetType(message.Confirmable)
	req.SetMessageID(message.GetMID())
	resp, err := cc.Do(req)
	require.NoError(t, err)
	assert.Equal(t, codes.Content, resp.Code())
}

func TestClientConnPost(t *testing.T) {
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
				buf, errH := ioutil.ReadAll(r.Body())
				require.NoError(t, errH)
				assert.Len(t, buf, 7000)

				errH = w.SetResponse(codes.BadRequest, message.TextPlain, bytes.NewReader(make([]byte, 5330)))
				require.NoError(t, errH)
				require.NotEmpty(t, w.Client())
			}))
			require.NoError(t, err)
			err = m.Handle("/b", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
				assert.Equal(t, codes.POST, r.Code())
				ct, errH := r.Options().GetUint32(message.ContentFormat)
				require.NoError(t, errH)
				assert.Equal(t, message.TextPlain, message.MediaType(ct))
				buf, errH := ioutil.ReadAll(r.Body())
				require.NoError(t, errH)
				assert.Equal(t, buf, []byte("b-send"))
				errH = w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("b")))
				require.NoError(t, errH)
				require.NotEmpty(t, w.Client())
			}))
			require.NoError(t, err)

			s := udp.NewServer(udp.WithMux(m))
			defer s.Stop()

			wg.Add(1)
			go func() {
				defer wg.Done()
				errS := s.Serve(l)
				require.NoError(t, errS)
			}()

			cc, err := udp.Dial(l.LocalAddr().String())
			require.NoError(t, err)
			defer func() {
				errC := cc.Close()
				require.NoError(t, errC)
			}()

			ctx, cancel := context.WithTimeout(context.Background(), time.Second*3600)
			defer cancel()
			got, err := cc.Post(ctx, tt.args.path, tt.args.contentFormat, tt.args.payload, tt.args.opts...)
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
				_, err = buf.ReadFrom(got.Body())
				require.NoError(t, err)
				require.Equal(t, tt.wantPayload, buf.Bytes())
			}
		})
	}
}

func TestClientConnPut(t *testing.T) {
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
				buf, errH := ioutil.ReadAll(r.Body())
				require.NoError(t, errH)
				assert.Len(t, buf, 7000)

				errH = w.SetResponse(codes.BadRequest, message.TextPlain, bytes.NewReader(make([]byte, 5330)))
				require.NoError(t, errH)
				require.NotEmpty(t, w.Client())
			}))
			require.NoError(t, err)
			err = m.Handle("/b", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
				assert.Equal(t, codes.PUT, r.Code())
				ct, errH := r.Options().GetUint32(message.ContentFormat)
				require.NoError(t, errH)
				assert.Equal(t, message.TextPlain, message.MediaType(ct))
				buf, errH := ioutil.ReadAll(r.Body())
				require.NoError(t, errH)
				assert.Equal(t, buf, []byte("b-send"))
				errH = w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("b")))
				require.NoError(t, errH)
				require.NotEmpty(t, w.Client())
			}))
			require.NoError(t, err)

			s := udp.NewServer(udp.WithMux(m))
			defer s.Stop()

			wg.Add(1)
			go func() {
				defer wg.Done()
				errS := s.Serve(l)
				require.NoError(t, errS)
			}()

			cc, err := udp.Dial(l.LocalAddr().String())
			require.NoError(t, err)
			defer func() {
				errC := cc.Close()
				require.NoError(t, errC)
			}()

			ctx, cancel := context.WithTimeout(context.Background(), time.Second*3600)
			defer cancel()
			got, err := cc.Put(ctx, tt.args.path, tt.args.contentFormat, tt.args.payload, tt.args.opts...)
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
				_, err = buf.ReadFrom(got.Body())
				require.NoError(t, err)
				require.Equal(t, tt.wantPayload, buf.Bytes())
			}
		})
	}
}

func TestClientConnDelete(t *testing.T) {
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
		require.NotEmpty(t, w.Client())
	}))
	require.NoError(t, err)
	err = m.Handle("/b", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.DELETE, r.Code())
		errS := w.SetResponse(codes.Deleted, message.TextPlain, bytes.NewReader([]byte("b")))
		require.NoError(t, errS)
		require.NotEmpty(t, w.Client())
	}))
	require.NoError(t, err)

	s := udp.NewServer(udp.WithMux(m))
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		require.NoError(t, errS)
	}()

	cc, err := udp.Dial(l.LocalAddr().String())
	require.NoError(t, err)
	defer func() {
		errC := cc.Close()
		require.NoError(t, errC)
	}()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second*3600)
			defer cancel()
			got, err := cc.Delete(ctx, tt.args.path, tt.args.opts...)
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
				_, err = buf.ReadFrom(got.Body())
				require.NoError(t, err)
				require.Equal(t, tt.wantPayload, buf.Bytes())
			}
		})
	}
}

func TestClientConnPing(t *testing.T) {
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
		require.NoError(t, errS)
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
