package udp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v2/mux"
	"github.com/plgd-dev/go-coap/v2/net/monitor/inactivity"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	coapNet "github.com/plgd-dev/go-coap/v2/net"
	"github.com/plgd-dev/go-coap/v2/udp/client"
	udpMessage "github.com/plgd-dev/go-coap/v2/udp/message"
	"github.com/plgd-dev/go-coap/v2/udp/message/pool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientConn_Get(t *testing.T) {
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
	defer l.Close()
	var wg sync.WaitGroup
	defer wg.Wait()

	m := mux.NewRouter()
	m.Handle("/a", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.GET, r.Code)
		err := w.SetResponse(codes.BadRequest, message.TextPlain, bytes.NewReader(make([]byte, 5330)))
		require.NoError(t, err)
		require.NotEmpty(t, w.Client())
		require.True(t, r.IsConfirmable)
	}))
	m.Handle("/b", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.GET, r.Code)
		err := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("b")))
		require.NoError(t, err)
		require.NotEmpty(t, w.Client())
		assert.True(t, r.IsConfirmable)
	}))
	m.Handle("/b-non", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.GET, r.Code)
		err := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("b")))
		require.NoError(t, err)
		require.NotEmpty(t, w.Client())
		assert.False(t, r.IsConfirmable)
	}))
	m.Handle("/empty", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.GET, r.Code)
		// Calling SetResponse was failing with an EOF error when the reader is empty
		// https://github.com/plgd-dev/go-coap/issues/157
		err := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte{}))
		require.NoError(t, err)
		require.NotEmpty(t, w.Client())
		assert.True(t, r.IsConfirmable)
	}))

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
	defer cc.Close()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second*3600)
			defer cancel()

			req, err := client.NewGetRequest(ctx, tt.args.path, tt.args.opts...)
			require.NoError(t, err)

			req.SetType(tt.args.typ)

			got, err := cc.Do(req)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantCode, got.Code())
			fmt.Printf("seq %v\n", got.Sequence())
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

func TestClientConn_Get_SeparateMessage(t *testing.T) {
	l, err := coapNet.NewListenUDP("udp", "")
	require.NoError(t, err)
	defer l.Close()
	var wg sync.WaitGroup
	defer wg.Wait()

	m := mux.NewRouter()
	m.Handle("/a", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
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
				opts, used, err = customResp.Options.SetContentFormat(optsBuf, message.TextPlain)
			}
			if err != nil {
				log.Printf("cannot set options to response: %v", err)
				return
			}
			optsBuf = optsBuf[:used]
			customResp.Options = opts

			err = w.Client().WriteMessage(&customResp)
			if err != nil {
				log.Printf("cannot set response: %v", err)
			}
		}()
	}))

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
	defer cc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3600)
	defer cancel()

	req, err := client.NewGetRequest(ctx, "/a")
	require.NoError(t, err)
	req.SetType(udpMessage.Confirmable)
	req.SetMessageID(udpMessage.GetMID())
	resp, err := cc.Do(req)
	require.NoError(t, err)
	assert.Equal(t, codes.Content, resp.Code())

}

func TestClientConn_Post(t *testing.T) {
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
			defer l.Close()
			var wg sync.WaitGroup
			defer wg.Wait()

			m := mux.NewRouter()
			m.Handle("/a", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
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
			m.Handle("/b", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
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
			defer cc.Close()

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

func TestClientConn_Put(t *testing.T) {
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
			defer l.Close()
			var wg sync.WaitGroup
			defer wg.Wait()

			m := mux.NewRouter()
			m.Handle("/a", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
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
			m.Handle("/b", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
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
			defer cc.Close()

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

func TestClientConn_Delete(t *testing.T) {
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
	defer l.Close()
	var wg sync.WaitGroup
	defer wg.Wait()

	m := mux.NewRouter()
	m.Handle("/a", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.DELETE, r.Code)
		err := w.SetResponse(codes.BadRequest, message.TextPlain, bytes.NewReader(make([]byte, 5330)))
		require.NoError(t, err)
		require.NotEmpty(t, w.Client())
		assert.True(t, r.IsConfirmable)
	}))
	m.Handle("/b", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.DELETE, r.Code)
		err := w.SetResponse(codes.Deleted, message.TextPlain, bytes.NewReader([]byte("b")))
		require.NoError(t, err)
		require.NotEmpty(t, w.Client())
		assert.True(t, r.IsConfirmable)
	}))

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
	defer cc.Close()

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

func TestClientConn_Ping(t *testing.T) {
	l, err := coapNet.NewListenUDP("udp", "")
	require.NoError(t, err)
	defer l.Close()
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
	defer cc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*200)
	defer cancel()
	err = cc.Ping(ctx)
	require.NoError(t, err)
}

func TestClient_InactiveMonitor(t *testing.T) {
	inactivityDetected := false
	defer func() {
		runtime.GC()
	}()

	ld, err := coapNet.NewListenUDP("udp4", "")
	require.NoError(t, err)
	defer ld.Close()

	var checkCloseWg sync.WaitGroup
	defer checkCloseWg.Wait()
	sd := NewServer()

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
			cc.Close()
		}),
	)
	require.NoError(t, err)
	checkCloseWg.Add(1)
	cc.AddOnClose(func() {
		checkCloseWg.Done()
	})

	// send ping to create serverside connection
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
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

func TestClient_KeepAliveMonitor(t *testing.T) {
	inactivityDetected := false

	ld, err := coapNet.NewListenUDP("udp4", "")
	require.NoError(t, err)
	defer ld.Close()

	var checkCloseWg sync.WaitGroup
	defer checkCloseWg.Wait()
	sd := NewServer(
		WithOnNewClientConn(func(cc *client.ClientConn) {
			checkCloseWg.Add(1)
			cc.AddOnClose(func() {
				checkCloseWg.Done()
			})
		}),
		WithInactivityMonitor(time.Millisecond*10, func(cc inactivity.ClientConn) {
			time.Sleep(time.Millisecond * 500)
			cc.Close()
		}),
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
			cc.Close()
		}),
	)
	require.NoError(t, err)
	checkCloseWg.Add(1)
	cc.AddOnClose(func() {
		checkCloseWg.Done()
	})

	// send ping to create serverside connection
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cc.Ping(ctx)

	checkCloseWg.Wait()
	require.True(t, inactivityDetected)
}
