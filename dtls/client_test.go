package dtls_test

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

	piondtls "github.com/pion/dtls/v2"
	"github.com/plgd-dev/go-coap/v2/dtls"
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
	}))
	require.NoError(t, err)
	err = m.Handle("/b", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.GET, r.Code())
		errS := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("b")))
		require.NoError(t, errS)
		require.NotEmpty(t, w.Client())
	}))
	require.NoError(t, err)

	s := dtls.NewServer(dtls.WithMux(m))
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		require.NoError(t, errS)
	}()

	cc, err := dtls.Dial(l.Addr().String(), dtlsCfg)
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
				Type:    message.Confirmable,
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
			resp := pool.NewMessage(r.Context())
			resp.SetMessage(customResp)
			errW := w.Client().WriteMessage(resp)
			if errW != nil && !errors.Is(errW, context.Canceled) {
				log.Printf("cannot set response: %v", errW)
			}
		}()
	}))
	require.NoError(t, err)

	s := dtls.NewServer(dtls.WithMux(m))
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		require.NoError(t, errS)
	}()

	cc, err := dtls.Dial(l.Addr().String(), dtlsCfg, dtls.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.ClientConn], r *pool.Message) {
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

				err = w.SetResponse(codes.BadRequest, message.TextPlain, bytes.NewReader(make([]byte, 5330)))
				require.NoError(t, err)
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

			s := dtls.NewServer(dtls.WithMux(m))
			defer s.Stop()

			wg.Add(1)
			go func() {
				defer wg.Done()
				errS := s.Serve(l)
				require.NoError(t, errS)
			}()

			cc, err := dtls.Dial(l.Addr().String(), dtlsCfg)
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

			s := dtls.NewServer(dtls.WithMux(m))
			defer s.Stop()

			wg.Add(1)
			go func() {
				defer wg.Done()
				errS := s.Serve(l)
				require.NoError(t, errS)
			}()

			cc, err := dtls.Dial(l.Addr().String(), dtlsCfg)
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
	}))
	require.NoError(t, err)
	err = m.Handle("/b", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.DELETE, r.Code())
		errH := w.SetResponse(codes.Deleted, message.TextPlain, bytes.NewReader([]byte("b")))
		require.NoError(t, errH)
		require.NotEmpty(t, w.Client())
	}))
	require.NoError(t, err)

	s := dtls.NewServer(dtls.WithMux(m))
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		require.NoError(t, errS)
	}()

	cc, err := dtls.Dial(l.Addr().String(), dtlsCfg)
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
	defer func() {
		errC := l.Close()
		require.NoError(t, errC)
	}()
	var wg sync.WaitGroup
	defer wg.Wait()

	s := dtls.NewServer()
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		require.NoError(t, errS)
	}()

	cc, err := dtls.Dial(l.Addr().String(), dtlsCfg)
	require.NoError(t, err)
	defer func() {
		errC := cc.Close()
		require.NoError(t, errC)
		<-cc.Done()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*200)
	defer cancel()
	err = cc.Ping(ctx)
	require.NoError(t, err)
}

func TestClientConnHandeShakeFailure(t *testing.T) {
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
	defer func() {
		errC := l.Close()
		require.NoError(t, errC)
	}()
	var wg sync.WaitGroup
	defer wg.Wait()

	s := dtls.NewServer()
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		require.NoError(t, errS)
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

func TestClientInactiveMonitor(t *testing.T) {
	inactivityDetected := false

	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	serverCgf, clientCgf, _, err := createDTLSConfig(ctx)
	require.NoError(t, err)

	ld, err := coapNet.NewDTLSListener("udp4", "", serverCgf)
	require.NoError(t, err)
	defer func() {
		errC := ld.Close()
		require.NoError(t, errC)
	}()

	checkClose := semaphore.NewWeighted(2)
	err = checkClose.Acquire(ctx, 2)
	require.NoError(t, err)
	sd := dtls.NewServer(
		dtls.WithOnNewClientConn(func(cc *client.ClientConn, dtlsConn *piondtls.Conn) {
			cc.AddOnClose(func() {
				checkClose.Release(1)
			})
		}),
		dtls.WithPeriodicRunner(periodic.New(ctx.Done(), time.Millisecond*10)),
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

	cc, err := dtls.Dial(ld.Addr().String(), clientCgf,
		dtls.WithInactivityMonitor(100*time.Millisecond, func(cc inactivity.ClientConn) {
			require.False(t, inactivityDetected)
			inactivityDetected = true
			errC := cc.Close()
			require.NoError(t, errC)
		}),
		dtls.WithPeriodicRunner(periodic.New(ctx.Done(), time.Millisecond*10)),
	)
	require.NoError(t, err)
	cc.AddOnClose(func() {
		checkClose.Release(1)
	})

	// send ping to create serverside connection
	ctx, cancel = context.WithTimeout(ctx, time.Second)
	defer cancel()
	err = cc.Ping(ctx)
	require.NoError(t, err)

	err = cc.Ping(ctx)
	require.NoError(t, err)

	time.Sleep(time.Second * 2)

	err = cc.Close()
	require.NoError(t, err)
	<-cc.Done()

	err = checkClose.Acquire(ctx, 2)
	require.NoError(t, err)
	require.True(t, inactivityDetected)
}

func TestClientKeepAliveMonitor(t *testing.T) {
	inactivityDetected := false

	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	serverCgf, clientCgf, _, err := createDTLSConfig(ctx)
	require.NoError(t, err)

	ld, err := coapNet.NewDTLSListener("udp4", "", serverCgf)
	require.NoError(t, err)
	defer func() {
		errC := ld.Close()
		require.NoError(t, errC)
	}()

	checkClose := semaphore.NewWeighted(2)
	err = checkClose.Acquire(ctx, 2)
	require.NoError(t, err)
	sd := dtls.NewServer(
		dtls.WithOnNewClientConn(func(cc *client.ClientConn, tlscon *piondtls.Conn) {
			cc.AddOnClose(func() {
				checkClose.Release(1)
			})
		}),
		dtls.WithGoPool(func(f func()) error {
			time.Sleep(time.Millisecond * 500)
			f()
			return nil
		}),
		dtls.WithPeriodicRunner(periodic.New(ctx.Done(), time.Millisecond*10)),
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

	cc, err := dtls.Dial(
		ld.Addr().String(),
		clientCgf,
		dtls.WithKeepAlive(3, 100*time.Millisecond, func(cc inactivity.ClientConn) {
			require.False(t, inactivityDetected)
			inactivityDetected = true
			errC := cc.Close()
			require.NoError(t, errC)
		}),
		dtls.WithPeriodicRunner(periodic.New(ctx.Done(), time.Millisecond*10)),
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
