package udp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v2/mux"
	"github.com/plgd-dev/go-coap/v2/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v2/pkg/runner/periodic"
	"golang.org/x/sync/semaphore"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	coapNet "github.com/plgd-dev/go-coap/v2/net"
	"github.com/plgd-dev/go-coap/v2/udp/client"
	udpMessage "github.com/plgd-dev/go-coap/v2/udp/message"
	"github.com/plgd-dev/go-coap/v2/udp/message/pool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const Timeout = time.Second * 8

func TestClientConnGet(t *testing.T) {
	type args struct {
		path string
		opts message.Options
		typ  udpMessage.Type
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
				typ:  udpMessage.NonConfirmable,
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
		err := l.Close()
		require.NoError(t, err)
	}()
	var wg sync.WaitGroup
	defer wg.Wait()

	m := mux.NewRouter()
	err = m.Handle("/a", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.GET, r.Code)
		err := w.SetResponse(codes.BadRequest, message.TextPlain, bytes.NewReader(make([]byte, 5330)))
		require.NoError(t, err)
		require.NotEmpty(t, w.Client())
		require.True(t, r.IsConfirmable)
	}))
	require.NoError(t, err)
	err = m.Handle("/b", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.GET, r.Code)
		err := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("b")))
		require.NoError(t, err)
		require.NotEmpty(t, w.Client())
		assert.True(t, r.IsConfirmable)
	}))
	require.NoError(t, err)
	err = m.Handle("/b-non", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.GET, r.Code)
		err := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("b")))
		require.NoError(t, err)
		require.NotEmpty(t, w.Client())
		assert.False(t, r.IsConfirmable)
	}))
	require.NoError(t, err)
	err = m.Handle("/empty", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.GET, r.Code)
		// Calling SetResponse was failing with an EOF error when the reader is empty
		// https://github.com/plgd-dev/go-coap/issues/157
		err := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte{}))
		require.NoError(t, err)
		require.NotEmpty(t, w.Client())
		assert.True(t, r.IsConfirmable)
	}))
	require.NoError(t, err)

	s := NewServer(WithMux(m))
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := s.Serve(l)
		require.NoError(t, err)
	}()

	cc, err := Dial(l.LocalAddr().String())
	require.NoError(t, err)
	defer func() {
		err := cc.Close()
		require.NoError(t, err)
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
				ct, err := got.ContentFormat()
				assert.NoError(t, err)
				assert.Equal(t, *tt.wantContentFormat, ct)
			}
			if tt.wantPayload != nil {
				buf := bytes.NewBuffer(nil)
				if got.Body() != nil {
					_, err = buf.ReadFrom(got.Body())
					require.NoError(t, err)
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
		err := l.Close()
		require.NoError(t, err)
	}()
	var wg sync.WaitGroup
	defer wg.Wait()

	m := mux.NewRouter()
	err = m.Handle("/a", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		go func() {
			time.Sleep(time.Second * 1)
			assert.Equal(t, codes.GET, r.Code)
			assert.True(t, r.IsConfirmable)
			customResp := message.Message{
				Code:    codes.Content,
				Token:   r.Token,
				Context: r.Context,
				Options: make(message.Options, 0, 16),
				//Body:    bytes.NewReader(make([]byte, 10)),
			}
			optsBuf := make([]byte, 32)
			opts, used, err := customResp.Options.SetContentFormat(optsBuf, message.TextPlain)
			if err == message.ErrTooSmall {
				optsBuf = append(optsBuf, make([]byte, used)...)
				opts, _, err = customResp.Options.SetContentFormat(optsBuf, message.TextPlain)
			}
			if err != nil {
				log.Printf("cannot set options to response: %v", err)
				return
			}
			customResp.Options = opts

			err = w.Client().WriteMessage(&customResp)
			if err != nil {
				log.Printf("cannot set response: %v", err)
			}
		}()
	}))
	require.NoError(t, err)

	s := NewServer(WithMux(m))
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := s.Serve(l)
		require.NoError(t, err)
	}()

	cc, err := Dial(l.LocalAddr().String(), WithHandlerFunc(func(w *client.ResponseWriter, r *pool.Message) {
		assert.NoError(t, fmt.Errorf("none msg expected comes: %+v", r))
	}))
	require.NoError(t, err)
	defer func() {
		err := cc.Close()
		require.NoError(t, err)
		<-cc.Done()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3600)
	defer cancel()

	req, err := client.NewGetRequest(ctx, pool.New(0, 0), "/a")
	require.NoError(t, err)
	req.SetType(udpMessage.Confirmable)
	req.SetMessageID(udpMessage.GetMID())
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
				err := l.Close()
				require.NoError(t, err)
			}()
			var wg sync.WaitGroup
			defer wg.Wait()

			m := mux.NewRouter()
			err = m.Handle("/a", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
				assert.Equal(t, codes.POST, r.Code)
				ct, err := r.Options.GetUint32(message.ContentFormat)
				require.NoError(t, err)
				assert.Equal(t, message.TextPlain, message.MediaType(ct))
				buf, err := ioutil.ReadAll(r.Body)
				require.NoError(t, err)
				assert.Len(t, buf, 7000)

				err = w.SetResponse(codes.BadRequest, message.TextPlain, bytes.NewReader(make([]byte, 5330)))
				require.NoError(t, err)
				require.NotEmpty(t, w.Client())
				assert.True(t, r.IsConfirmable)
			}))
			require.NoError(t, err)
			err = m.Handle("/b", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
				assert.Equal(t, codes.POST, r.Code)
				ct, err := r.Options.GetUint32(message.ContentFormat)
				require.NoError(t, err)
				assert.Equal(t, message.TextPlain, message.MediaType(ct))
				buf, err := ioutil.ReadAll(r.Body)
				require.NoError(t, err)
				assert.Equal(t, buf, []byte("b-send"))
				err = w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("b")))
				require.NoError(t, err)
				require.NotEmpty(t, w.Client())
				assert.True(t, r.IsConfirmable)
			}))
			require.NoError(t, err)

			s := NewServer(WithMux(m))
			defer s.Stop()

			wg.Add(1)
			go func() {
				defer wg.Done()
				err := s.Serve(l)
				require.NoError(t, err)
			}()

			cc, err := Dial(l.LocalAddr().String())
			require.NoError(t, err)
			defer func() {
				err := cc.Close()
				require.NoError(t, err)
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
				err := l.Close()
				require.NoError(t, err)
			}()
			var wg sync.WaitGroup
			defer wg.Wait()

			m := mux.NewRouter()
			err = m.Handle("/a", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
				assert.Equal(t, codes.PUT, r.Code)
				ct, err := r.Options.GetUint32(message.ContentFormat)
				require.NoError(t, err)
				assert.Equal(t, message.TextPlain, message.MediaType(ct))
				buf, err := ioutil.ReadAll(r.Body)
				require.NoError(t, err)
				assert.Len(t, buf, 7000)

				err = w.SetResponse(codes.BadRequest, message.TextPlain, bytes.NewReader(make([]byte, 5330)))
				require.NoError(t, err)
				require.NotEmpty(t, w.Client())
				assert.True(t, r.IsConfirmable)
			}))
			require.NoError(t, err)
			err = m.Handle("/b", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
				assert.Equal(t, codes.PUT, r.Code)
				ct, err := r.Options.GetUint32(message.ContentFormat)
				require.NoError(t, err)
				assert.Equal(t, message.TextPlain, message.MediaType(ct))
				buf, err := ioutil.ReadAll(r.Body)
				require.NoError(t, err)
				assert.Equal(t, buf, []byte("b-send"))
				err = w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("b")))
				require.NoError(t, err)
				require.NotEmpty(t, w.Client())
				assert.True(t, r.IsConfirmable)
			}))
			require.NoError(t, err)

			s := NewServer(WithMux(m))
			defer s.Stop()

			wg.Add(1)
			go func() {
				defer wg.Done()
				err := s.Serve(l)
				require.NoError(t, err)
			}()

			cc, err := Dial(l.LocalAddr().String())
			require.NoError(t, err)
			defer func() {
				err := cc.Close()
				require.NoError(t, err)
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
		err := l.Close()
		require.NoError(t, err)
	}()
	var wg sync.WaitGroup
	defer wg.Wait()

	m := mux.NewRouter()
	err = m.Handle("/a", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.DELETE, r.Code)
		err := w.SetResponse(codes.BadRequest, message.TextPlain, bytes.NewReader(make([]byte, 5330)))
		require.NoError(t, err)
		require.NotEmpty(t, w.Client())
		assert.True(t, r.IsConfirmable)
	}))
	require.NoError(t, err)
	err = m.Handle("/b", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.DELETE, r.Code)
		err := w.SetResponse(codes.Deleted, message.TextPlain, bytes.NewReader([]byte("b")))
		require.NoError(t, err)
		require.NotEmpty(t, w.Client())
		assert.True(t, r.IsConfirmable)
	}))
	require.NoError(t, err)

	s := NewServer(WithMux(m))
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := s.Serve(l)
		require.NoError(t, err)
	}()

	cc, err := Dial(l.LocalAddr().String())
	require.NoError(t, err)
	defer func() {
		err := cc.Close()
		require.NoError(t, err)
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
		err := l.Close()
		require.NoError(t, err)
	}()
	var wg sync.WaitGroup
	defer wg.Wait()

	s := NewServer()
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := s.Serve(l)
		require.NoError(t, err)
	}()

	cc, err := Dial(l.LocalAddr().String())
	require.NoError(t, err)
	defer func() {
		err := cc.Close()
		require.NoError(t, err)
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
		err := ld.Close()
		require.NoError(t, err)
	}()

	var checkCloseWg sync.WaitGroup
	defer checkCloseWg.Wait()
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
		err := sd.Serve(ld)
		require.NoError(t, err)
	}()

	cc, err := Dial(
		ld.LocalAddr().String(),
		WithInactivityMonitor(100*time.Millisecond, func(cc inactivity.ClientConn) {
			require.False(t, inactivityDetected)
			inactivityDetected = true
			err := cc.Close()
			require.NoError(t, err)
		}),
		WithPeriodicRunner(periodic.New(ctx.Done(), time.Millisecond*100)),
	)
	require.NoError(t, err)
	checkCloseWg.Add(1)
	cc.AddOnClose(func() {
		checkCloseWg.Done()
	})

	// send ping to create serverside connection
	ctx, cancel = context.WithTimeout(ctx, time.Second)
	defer cancel()
	err = cc.Ping(ctx)
	require.NoError(t, err)

	err = cc.Ping(ctx)
	require.NoError(t, err)

	// wait for fire inactivity
	time.Sleep(time.Second * 2)

	checkCloseWg.Wait()
	require.True(t, inactivityDetected)
}

func TestClientKeepAliveMonitor(t *testing.T) {
	inactivityDetected := false

	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()

	ld, err := coapNet.NewListenUDP("udp4", "")
	require.NoError(t, err)
	defer func() {
		err := ld.Close()
		require.NoError(t, err)
	}()

	checkCloseWg := semaphore.NewWeighted(2)
	err = checkCloseWg.Acquire(ctx, 2)
	require.NoError(t, err)
	sd := NewServer(
		WithOnNewClientConn(func(cc *client.ClientConn) {
			checkCloseWg.Release(1)
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
		err := sd.Serve(ld)
		require.NoError(t, err)
	}()

	cc, err := Dial(
		ld.LocalAddr().String(),
		WithKeepAlive(3, 100*time.Millisecond, func(cc inactivity.ClientConn) {
			require.False(t, inactivityDetected)
			inactivityDetected = true
			err := cc.Close()
			require.NoError(t, err)
		}),
		WithPeriodicRunner(periodic.New(ctx.Done(), time.Millisecond*10)),
	)
	require.NoError(t, err)
	cc.AddOnClose(func() {
		checkCloseWg.Release(1)
	})

	// send ping to create serverside connection
	ctx, cancel = context.WithTimeout(ctx, time.Second)
	defer cancel()
	err = cc.Ping(ctx)
	require.Error(t, err)

	err = checkCloseWg.Acquire(ctx, 2)
	require.NoError(t, err)
	require.True(t, inactivityDetected)
}
