package udp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	"github.com/plgd-dev/go-coap/v2/message/pool"
	"github.com/plgd-dev/go-coap/v2/mux"
	coapNet "github.com/plgd-dev/go-coap/v2/net"
	"github.com/plgd-dev/go-coap/v2/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v2/net/responsewriter"
	"github.com/plgd-dev/go-coap/v2/pkg/runner/periodic"
	"github.com/plgd-dev/go-coap/v2/udp/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/semaphore"
)

const Timeout = time.Second * 8

func TestClientConnGet(t *testing.T) {
	type args struct {
		path string
		opts message.Options
		typ  message.Type
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
			name: "ok-b-non",
			args: args{
				path: "/b-non",
				typ:  message.NonConfirmable,
			},
			wantCode:          codes.Content,
			wantContentFormat: &message.TextPlain,
			wantPayload:       []byte("b"),
		},
		{
			name: "ok-empty",
			args: args{
				path: "/empty",
			},
			wantCode:          codes.Content,
			wantContentFormat: &message.TextPlain,
			wantPayload:       []byte(nil),
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
		errS := w.SetResponse(codes.BadRequest, message.TextPlain, bytes.NewReader(make([]byte, 5330)))
		require.NoError(t, errS)
		require.NotEmpty(t, w.Client())
		require.True(t, r.Type() == message.Confirmable)
	}))
	require.NoError(t, err)
	err = m.Handle("/b", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.GET, r.Code())
		errS := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("b")))
		require.NoError(t, errS)
		require.NotEmpty(t, w.Client())
		assert.True(t, r.Type() == message.Confirmable)
	}))
	require.NoError(t, err)
	err = m.Handle("/b-non", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.GET, r.Code())
		errS := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("b")))
		require.NoError(t, errS)
		require.NotEmpty(t, w.Client())
		assert.False(t, r.Type() == message.Confirmable)
	}))
	require.NoError(t, err)
	err = m.Handle("/empty", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.GET, r.Code())
		// Calling SetResponse was failing with an EOF error when the reader is empty
		// https://github.com/plgd-dev/go-coap/issues/157
		errS := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte{}))
		require.NoError(t, errS)
		require.NotEmpty(t, w.Client())
		assert.True(t, r.Type() == message.Confirmable)
	}))
	require.NoError(t, err)

	s := NewServer(WithMux(m))
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		require.NoError(t, errS)
	}()

	cc, err := Dial(l.LocalAddr().String())
	require.NoError(t, err)
	defer func() {
		errC := cc.Close()
		require.NoError(t, errC)
		<-cc.Done()
	}()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second*3600)
			defer cancel()

			req, err := client.NewGetRequest(ctx, pool.New(0, 0), tt.args.path, tt.args.opts...)
			require.NoError(t, err)

			req.SetType(tt.args.typ)

			got, err := cc.Do(req)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantCode, got.Code())
			assert.Greater(t, got.Sequence(), uint64(0))
			if tt.wantContentFormat != nil {
				ct, errC := got.ContentFormat()
				assert.NoError(t, errC)
				assert.Equal(t, *tt.wantContentFormat, ct)
			}
			if tt.wantPayload != nil {
				buf := bytes.NewBuffer(nil)
				if got.Body() != nil {
					_, errR := buf.ReadFrom(got.Body())
					require.NoError(t, errR)
				}

				require.Equal(t, tt.wantPayload, buf.Bytes())
			} else {
				require.Nil(t, got.Body())
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
			assert.True(t, r.Type() == message.Confirmable)
			customResp := message.Message{
				Code:    codes.Content,
				Token:   r.Token(),
				Options: make(message.Options, 0, 16),
				// Body:    bytes.NewReader(make([]byte, 10)),
			}
			optsBuf := make([]byte, 32)
			opts, used, errS := customResp.Options.SetContentFormat(optsBuf, message.TextPlain)
			if errors.Is(errS, message.ErrTooSmall) {
				optsBuf = append(optsBuf, make([]byte, used)...)
				opts, _, errS = customResp.Options.SetContentFormat(optsBuf, message.TextPlain)
			}
			if errS != nil {
				log.Printf("cannot set options to response: %v", errS)
				return
			}
			customResp.Options = opts
			msg := pool.NewMessage(r.Context())
			msg.SetMessage(customResp)
			errW := w.Client().WriteMessage(msg)
			if errW != nil && !errors.Is(errW, context.Canceled) {
				log.Printf("cannot set response: %v", errW)
			}
		}()
	}))
	require.NoError(t, err)

	s := NewServer(WithMux(m))
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		require.NoError(t, errS)
	}()

	cc, err := Dial(l.LocalAddr().String(), WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.ClientConn], r *pool.Message) {
		assert.NoError(t, fmt.Errorf("none msg expected comes: %+v", r))
	}))
	require.NoError(t, err)
	defer func() {
		errC := cc.Close()
		require.NoError(t, errC)
		<-cc.Done()
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
				assert.True(t, r.Type() == message.Confirmable)
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
				assert.True(t, r.Type() == message.Confirmable)
			}))
			require.NoError(t, err)

			s := NewServer(WithMux(m))
			defer s.Stop()

			wg.Add(1)
			go func() {
				defer wg.Done()
				errS := s.Serve(l)
				require.NoError(t, errS)
			}()

			cc, err := Dial(l.LocalAddr().String())
			require.NoError(t, err)
			defer func() {
				errC := cc.Close()
				require.NoError(t, errC)
				<-cc.Done()
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
				assert.True(t, r.Type() == message.Confirmable)
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
				assert.True(t, r.Type() == message.Confirmable)
			}))
			require.NoError(t, err)

			s := NewServer(WithMux(m))
			defer s.Stop()

			wg.Add(1)
			go func() {
				defer wg.Done()
				errS := s.Serve(l)
				require.NoError(t, errS)
			}()

			cc, err := Dial(l.LocalAddr().String())
			require.NoError(t, err)
			defer func() {
				errC := cc.Close()
				require.NoError(t, errC)
				<-cc.Done()
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
		errH := w.SetResponse(codes.BadRequest, message.TextPlain, bytes.NewReader(make([]byte, 5330)))
		require.NoError(t, errH)
		require.NotEmpty(t, w.Client())
		assert.True(t, r.Type() == message.Confirmable)
	}))
	require.NoError(t, err)
	err = m.Handle("/b", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.DELETE, r.Code())
		errH := w.SetResponse(codes.Deleted, message.TextPlain, bytes.NewReader([]byte("b")))
		require.NoError(t, errH)
		require.NotEmpty(t, w.Client())
		assert.True(t, r.Type() == message.Confirmable)
	}))
	require.NoError(t, err)

	s := NewServer(WithMux(m))
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		require.NoError(t, errS)
	}()

	cc, err := Dial(l.LocalAddr().String())
	require.NoError(t, err)
	defer func() {
		errC := cc.Close()
		require.NoError(t, errC)
		<-cc.Done()
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

	s := NewServer()
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		require.NoError(t, errS)
	}()

	cc, err := Dial(l.LocalAddr().String())
	require.NoError(t, err)
	defer func() {
		errC := cc.Close()
		require.NoError(t, errC)
		<-cc.Done()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	err = cc.Ping(ctx)
	require.NoError(t, err)
}

func TestClientInactiveMonitor(t *testing.T) {
	inactivityDetected := false

	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()

	ld, err := coapNet.NewListenUDP("udp4", "")
	require.NoError(t, err)
	defer func() {
		errC := ld.Close()
		require.NoError(t, errC)
	}()

	checkClose := semaphore.NewWeighted(1)
	err = checkClose.Acquire(ctx, 1)
	require.NoError(t, err)

	sd := NewServer(
		WithPeriodicRunner(periodic.New(ctx.Done(), time.Millisecond*10)),
	)

	var serverWg sync.WaitGroup
	defer func() {
		sd.Stop()
		serverWg.Wait()
	}()
	serverWg.Add(1)
	go func() {
		defer serverWg.Done()
		errS := sd.Serve(ld)
		require.NoError(t, errS)
	}()

	cc, err := Dial(
		ld.LocalAddr().String(),
		WithInactivityMonitor(100*time.Millisecond, func(cc inactivity.ClientConn) {
			require.False(t, inactivityDetected)
			inactivityDetected = true
			errC := cc.Close()
			require.NoError(t, errC)
		}),
		WithPeriodicRunner(periodic.New(ctx.Done(), time.Millisecond*100)),
	)
	require.NoError(t, err)
	cc.AddOnClose(func() {
		checkClose.Release(1)
	})

	// send ping to create serverside connection
	ctxPing, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	err = cc.Ping(ctxPing)
	require.NoError(t, err)

	err = cc.Ping(ctx)
	require.NoError(t, err)

	// wait for fire inactivity
	time.Sleep(time.Second * 2)

	err = checkClose.Acquire(ctx, 1)
	require.NoError(t, err)
	require.True(t, inactivityDetected)
}

func TestClientKeepAliveMonitor(t *testing.T) {
	inactivityDetected := false

	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()

	ld, err := coapNet.NewListenUDP("udp4", "")
	require.NoError(t, err)
	defer func() {
		errC := ld.Close()
		require.NoError(t, errC)
	}()

	checkClose := semaphore.NewWeighted(2)
	err = checkClose.Acquire(ctx, 2)
	require.NoError(t, err)
	sd := NewServer(
		WithOnNewClientConn(func(cc *client.ClientConn) {
			checkClose.Release(1)
		}),
		WithGoPool(func(f func()) error {
			time.Sleep(time.Millisecond * 500)
			f()
			return nil
		}),
		WithPeriodicRunner(periodic.New(ctx.Done(), time.Millisecond*10)),
	)

	var serverWg sync.WaitGroup
	defer func() {
		sd.Stop()
		serverWg.Wait()
	}()
	serverWg.Add(1)
	go func() {
		defer serverWg.Done()
		errS := sd.Serve(ld)
		require.NoError(t, errS)
	}()

	cc, err := Dial(
		ld.LocalAddr().String(),
		WithKeepAlive(3, 100*time.Millisecond, func(cc inactivity.ClientConn) {
			require.False(t, inactivityDetected)
			inactivityDetected = true
			errC := cc.Close()
			require.NoError(t, errC)
		}),
		WithPeriodicRunner(periodic.New(ctx.Done(), time.Millisecond*10)),
	)
	require.NoError(t, err)
	cc.AddOnClose(func() {
		checkClose.Release(1)
	})

	// send ping to create serverside connection
	ctxPing, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	err = cc.Ping(ctxPing)
	require.Error(t, err)

	err = checkClose.Acquire(ctx, 2)
	require.NoError(t, err)
	require.True(t, inactivityDetected)
}
