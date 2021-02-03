package dtls_test

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

	piondtls "github.com/pion/dtls/v2"
	"github.com/plgd-dev/go-coap/v2/dtls"
	"github.com/plgd-dev/go-coap/v2/mux"
	"github.com/plgd-dev/go-coap/v2/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v2/udp/client"
	udpMessage "github.com/plgd-dev/go-coap/v2/udp/message"
	"github.com/plgd-dev/go-coap/v2/udp/message/pool"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	coapNet "github.com/plgd-dev/go-coap/v2/net"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientConn_Get(t *testing.T) {
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

	dtlsCfg := &piondtls.Config{
		PSK: func(hint []byte) ([]byte, error) {
			fmt.Printf("Hint: %s \n", hint)
			return []byte{0xAB, 0xC1, 0x23}, nil
		},
		PSKIdentityHint: []byte("Pion DTLS Server"),
		CipherSuites:    []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM_8},
	}
	l, err := coapNet.NewDTLSListener("udp", "", dtlsCfg)
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
	}))
	m.Handle("/b", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.GET, r.Code)
		err := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("b")))
		require.NoError(t, err)
		require.NotEmpty(t, w.Client())
	}))

	s := dtls.NewServer(dtls.WithMux(m))
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := s.Serve(l)
		require.NoError(t, err)
	}()

	cc, err := dtls.Dial(l.Addr().String(), dtlsCfg)
	require.NoError(t, err)
	defer cc.Close()

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

func TestClientConn_Get_SeparateMessage(t *testing.T) {
	dtlsCfg := &piondtls.Config{
		PSK: func(hint []byte) ([]byte, error) {
			fmt.Printf("Hint: %s \n", hint)
			return []byte{0xAB, 0xC1, 0x23}, nil
		},
		PSKIdentityHint: []byte("Pion DTLS Server"),
		CipherSuites:    []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM_8},
	}
	l, err := coapNet.NewDTLSListener("udp", "", dtlsCfg)
	require.NoError(t, err)
	defer l.Close()
	var wg sync.WaitGroup
	defer wg.Wait()

	m := mux.NewRouter()
	m.Handle("/a", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		go func() {
			time.Sleep(time.Second * 1)
			assert.Equal(t, codes.GET, r.Code)
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

	s := dtls.NewServer(dtls.WithMux(m))
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := s.Serve(l)
		require.NoError(t, err)
	}()

	cc, err := dtls.Dial(l.Addr().String(), dtlsCfg, dtls.WithHandlerFunc(func(w *client.ResponseWriter, r *pool.Message) {
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
			dtlsCfg := &piondtls.Config{
				PSK: func(hint []byte) ([]byte, error) {
					fmt.Printf("Hint: %s \n", hint)
					return []byte{0xAB, 0xC1, 0x23}, nil
				},
				PSKIdentityHint: []byte("Pion DTLS Server"),
				CipherSuites:    []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM_8},
			}
			l, err := coapNet.NewDTLSListener("udp", "", dtlsCfg)
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
			}))

			s := dtls.NewServer(dtls.WithMux(m))
			defer s.Stop()

			wg.Add(1)
			go func() {
				defer wg.Done()
				err := s.Serve(l)
				require.NoError(t, err)
			}()

			cc, err := dtls.Dial(l.Addr().String(), dtlsCfg)
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
			dtlsCfg := &piondtls.Config{
				PSK: func(hint []byte) ([]byte, error) {
					fmt.Printf("Hint: %s \n", hint)
					return []byte{0xAB, 0xC1, 0x23}, nil
				},
				PSKIdentityHint: []byte("Pion DTLS Server"),
				CipherSuites:    []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM_8},
			}
			l, err := coapNet.NewDTLSListener("udp", "", dtlsCfg)
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
			}))

			s := dtls.NewServer(dtls.WithMux(m))
			defer s.Stop()

			wg.Add(1)
			go func() {
				defer wg.Done()
				err := s.Serve(l)
				require.NoError(t, err)
			}()

			cc, err := dtls.Dial(l.Addr().String(), dtlsCfg)
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

	dtlsCfg := &piondtls.Config{
		PSK: func(hint []byte) ([]byte, error) {
			fmt.Printf("Hint: %s \n", hint)
			return []byte{0xAB, 0xC1, 0x23}, nil
		},
		PSKIdentityHint: []byte("Pion DTLS Server"),
		CipherSuites:    []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM_8},
	}
	l, err := coapNet.NewDTLSListener("udp", "", dtlsCfg)
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
	}))
	m.Handle("/b", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.DELETE, r.Code)
		err := w.SetResponse(codes.Deleted, message.TextPlain, bytes.NewReader([]byte("b")))
		require.NoError(t, err)
		require.NotEmpty(t, w.Client())
	}))

	s := dtls.NewServer(dtls.WithMux(m))
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := s.Serve(l)
		require.NoError(t, err)
	}()

	cc, err := dtls.Dial(l.Addr().String(), dtlsCfg)
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
	dtlsCfg := &piondtls.Config{
		PSK: func(hint []byte) ([]byte, error) {
			fmt.Printf("Hint: %s \n", hint)
			return []byte{0xAB, 0xC1, 0x23}, nil
		},
		PSKIdentityHint: []byte("Pion DTLS Server"),
		CipherSuites:    []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM_8},
	}
	l, err := coapNet.NewDTLSListener("udp", "", dtlsCfg)
	require.NoError(t, err)
	defer l.Close()
	var wg sync.WaitGroup
	defer wg.Wait()

	s := dtls.NewServer()
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := s.Serve(l)
		require.NoError(t, err)
	}()

	cc, err := dtls.Dial(l.Addr().String(), dtlsCfg)
	require.NoError(t, err)
	defer cc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*200)
	defer cancel()
	err = cc.Ping(ctx)
	require.NoError(t, err)
}

func TestClientConn_HandeShakeFailure(t *testing.T) {
	dtlsCfg := &piondtls.Config{
		PSK: func(hint []byte) ([]byte, error) {
			fmt.Printf("Hint: %s \n", hint)
			return []byte{0xAB, 0xC1, 0x23}, nil
		},
		PSKIdentityHint: []byte("Pion DTLS Server"),
		CipherSuites:    []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM_8},
		ConnectContextMaker: func() (context.Context, func()) {
			return context.WithTimeout(context.Background(), 1*time.Second)
		},
	}
	l, err := coapNet.NewDTLSListener("udp", "", dtlsCfg)
	require.NoError(t, err)
	defer l.Close()
	var wg sync.WaitGroup
	defer wg.Wait()

	s := dtls.NewServer()
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := s.Serve(l)
		require.NoError(t, err)
	}()

	dtlsCfgClient := &piondtls.Config{
		PSK: func(hint []byte) ([]byte, error) {
			fmt.Printf("Hint: %s \n", hint)
			return []byte{0xAB, 0xC1, 0x24}, nil
		},
		PSKIdentityHint: []byte("Pion DTLS Client"),
		CipherSuites:    []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM_8},
		ConnectContextMaker: func() (context.Context, func()) {
			return context.WithTimeout(context.Background(), 1*time.Second)
		},
	}
	_, err = dtls.Dial(l.Addr().String(), dtlsCfgClient)
	require.Error(t, err)
}

func TestClient_InactiveMonitor(t *testing.T) {
	inactivityDetected := false
	defer func() {
		runtime.GC()
	}()

	srvCtx, srvCancel := context.WithTimeout(context.Background(), time.Second*3600)
	defer srvCancel()
	serverCgf, clientCgf, _, err := createDTLSConfig(srvCtx)
	require.NoError(t, err)

	ld, err := coapNet.NewDTLSListener("udp4", "", serverCgf)
	require.NoError(t, err)
	defer ld.Close()

	var checkCloseWg sync.WaitGroup
	defer checkCloseWg.Wait()
	sd := dtls.NewServer(
		dtls.WithOnNewClientConn(func(cc *client.ClientConn, dtlsConn *piondtls.Conn) {
			checkCloseWg.Add(1)
			cc.AddOnClose(func() {
				checkCloseWg.Done()
			})
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

	cc, err := dtls.Dial(ld.Addr().String(), clientCgf,
		dtls.WithInactivityMonitor(100*time.Millisecond, func(cc inactivity.ClientConn) {
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

	time.Sleep(time.Second * 2)

	cc.Close()

	checkCloseWg.Wait()
	require.True(t, inactivityDetected)
}

func TestClient_KeepAliveMonitor(t *testing.T) {
	inactivityDetected := false

	srvCtx, srvCancel := context.WithTimeout(context.Background(), time.Second*3600)
	defer srvCancel()
	serverCgf, clientCgf, _, err := createDTLSConfig(srvCtx)
	require.NoError(t, err)

	ld, err := coapNet.NewDTLSListener("udp4", "", serverCgf)
	require.NoError(t, err)
	defer ld.Close()

	var checkCloseWg sync.WaitGroup
	defer checkCloseWg.Wait()
	sd := dtls.NewServer(
		dtls.WithOnNewClientConn(func(cc *client.ClientConn, tlscon *piondtls.Conn) {
			checkCloseWg.Add(1)
			cc.AddOnClose(func() {
				checkCloseWg.Done()
			})
		}),
		dtls.WithInactivityMonitor(time.Millisecond*10, func(cc inactivity.ClientConn) {
			time.Sleep(time.Millisecond * 500)
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

	cc, err := dtls.Dial(
		ld.Addr().String(),
		clientCgf,
		dtls.WithKeepAlive(3, 100*time.Millisecond, func(cc inactivity.ClientConn) {
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
