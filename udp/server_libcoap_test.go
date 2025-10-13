package udp

import (
	"bytes"
	"embed"
	"fmt"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/mux"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"math/rand"
	"os/exec"
	"sync"
	"testing"
)

//go:embed testdata/big.txt
var assets embed.FS

func TestConnGetBlockwise(t *testing.T) {

	bigPayload10KiB, err := assets.ReadFile("testdata/big.txt")
	require.NoError(t, err)
	require.NotEmpty(t, bigPayload10KiB)

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
		useToken          bool
	}{
		{
			name: "block-with-token",
			args: args{
				path: "/big",
			},
			wantCode:          codes.Content,
			wantContentFormat: &message.TextPlain,
			wantPayload:       bigPayload10KiB,
			wantErr:           false,
			useToken:          true,
		},
		{
			name: "block-without-token",
			args: args{
				path: "/big",
			},
			wantCode:          codes.Content,
			wantContentFormat: &message.TextPlain,
			wantPayload:       bigPayload10KiB,
			wantErr:           false,
			useToken:          false,
		},
	}

	// verify that the binary coap-client is installed
	_, err = exec.LookPath("coap-client")
	if err != nil {
		t.Error("coap-client not found in PATH")
		return
	}

	l, err := coapNet.NewListenUDP("udp", "[::]:5685")
	require.NoError(t, err)
	defer func() {
		errC := l.Close()
		require.NoError(t, errC)
	}()
	var wg sync.WaitGroup
	defer wg.Wait()

	m := mux.NewRouter()

	err = m.Handle("/big", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.GET, r.Code())
		errS := w.SetResponse(codes.Content, message.AppOctets, bytes.NewReader([]byte(bigPayload10KiB)))
		require.NoError(t, errS)
		require.NotEmpty(t, w.Conn())
		require.Equal(t, message.Confirmable, r.Type())
	}))
	require.NoError(t, err)

	s := NewServer(options.WithMux(m))
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		assert.NoError(t, errS)
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

			laddr := l.LocalAddr().String()

			receivedBody, err := sendCoAPRequestLibCoAP(laddr, "get", tt.args.path, false, tt.useToken)

			if tt.wantErr {
				require.Error(t, err)
				return
			} else {
				require.NoError(t, err)
			}

			if tt.wantPayload != nil {
				if wantPayloadBytes, ok := tt.wantPayload.([]byte); ok {
					// add 1 for the extra newline added by coap-client
					require.Len(t, receivedBody, len(wantPayloadBytes)+1)
					require.Equal(t, wantPayloadBytes, receivedBody[0:len(wantPayloadBytes)])
				}
			}

		})
	}
}

func TestConnPutBlockwise(t *testing.T) {
	bigPayload10KiB, err := assets.ReadFile("testdata/big.txt")
	require.NoError(t, err)
	require.NotEmpty(t, bigPayload10KiB)

	type args struct {
		path string
		opts message.Options
		typ  message.Type
	}
	tests := []struct {
		name        string
		args        args
		wantCode    codes.Code
		wantErr     bool
		useToken    bool
		wantPayload []byte
	}{
		{
			name: "block-put-with-token",
			args: args{
				path: "/big",
			},
			wantCode: codes.Changed,
			wantErr:  false,
			useToken: true,
		},
		{
			name: "block-put-without-token",
			args: args{
				path: "/big",
			},
			wantCode: codes.Changed,
			wantErr:  false,
			useToken: false,
		},
	}

	_, err = exec.LookPath("coap-client")
	if err != nil {
		t.Error("coap-client not found in PATH")
		return
	}

	l, err := coapNet.NewListenUDP("udp", "[::]:5686")
	require.NoError(t, err)
	defer func() {
		errC := l.Close()
		require.NoError(t, errC)
	}()
	var wg sync.WaitGroup
	defer wg.Wait()

	m := mux.NewRouter()

	err = m.Handle("/big", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.PUT, r.Code())
		errS := w.SetResponse(codes.Changed, message.TextPlain, nil)
		received, err := r.ReadBody()
		assert.NoError(t, err)
		assert.Equal(t, bigPayload10KiB, received)
		assert.NoError(t, errS)
		assert.NotEmpty(t, w.Conn())
		assert.Equal(t, message.Confirmable, r.Type())
	}))
	require.NoError(t, err)

	s := NewServer(options.WithMux(m))
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		assert.NoError(t, errS)
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
			laddr := l.LocalAddr().String()
			_, err := sendCoAPRequestLibCoAP(laddr, "put", tt.args.path, true, tt.useToken)
			if tt.wantErr {
				require.Error(t, err)
				return
			} else {
				require.NoError(t, err)
			}

		})
	}
}

func TestConnPostBlockwise(t *testing.T) {
	bigPayload10KiB, err := assets.ReadFile("testdata/big.txt")
	require.NoError(t, err)
	require.NotEmpty(t, bigPayload10KiB)

	type args struct {
		path string
		opts message.Options
		typ  message.Type
	}
	tests := []struct {
		name        string
		args        args
		wantCode    codes.Code
		wantErr     bool
		useToken    bool
		wantPayload []byte
	}{
		{
			name: "block-post-with-token",
			args: args{
				path: "/big",
			},
			wantCode:    codes.Content,
			wantErr:     false,
			useToken:    true,
			wantPayload: []byte(bigPayload10KiB),
		},
		{
			name: "block-post-without-token",
			args: args{
				path: "/big",
			},
			wantCode:    codes.Content,
			wantErr:     false,
			useToken:    false,
			wantPayload: []byte(bigPayload10KiB),
		},
	}

	_, err = exec.LookPath("coap-client")
	if err != nil {
		t.Error("coap-client not found in PATH")
		return
	}

	l, err := coapNet.NewListenUDP("udp", "[::]:5687")
	require.NoError(t, err)
	defer func() {
		errC := l.Close()
		require.NoError(t, errC)
	}()
	var wg sync.WaitGroup
	defer wg.Wait()

	m := mux.NewRouter()

	err = m.Handle("/big", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		assert.Equal(t, codes.POST, r.Code())
		received, err := r.ReadBody()
		errS := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte(bigPayload10KiB)))
		assert.NoError(t, err)
		assert.Equal(t, bigPayload10KiB, received)
		assert.NoError(t, errS)
		assert.NotEmpty(t, w.Conn())
		assert.Equal(t, message.Confirmable, r.Type())
	}))
	require.NoError(t, err)

	s := NewServer(options.WithMux(m))
	defer s.Stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		assert.NoError(t, errS)
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
			laddr := l.LocalAddr().String()
			receivedBody, err := sendCoAPRequestLibCoAP(laddr, "post", tt.args.path, true, tt.useToken)
			if tt.wantErr {
				require.Error(t, err)
				return
			} else {
				require.NoError(t, err)
			}
			if tt.wantPayload != nil {
				// add 1 for the extra newline added by coap-client
				require.Len(t, receivedBody, len(tt.wantPayload)+1)
				require.Equal(t, tt.wantPayload, receivedBody[0:len(tt.wantPayload)])
			}
		})
	}
}

func sendCoAPRequestLibCoAP(address string, method string, path string, payload bool, token bool) ([]byte, error) {
	// Check if coap-client is available
	bigPayload10KiB, err := assets.ReadFile("testdata/big.txt")
	if err != nil {
		return nil, fmt.Errorf("cannot read big payload: %v", err)
	}
	coapClientPath, err := exec.LookPath("coap-client")
	if err != nil {
		return nil, fmt.Errorf("coap-client not found in PATH")
	}

	// Build coap-client command arguments
	var args []string
	args = append(args, "-m", method)
	if token {
		rand.Uint64()
		args = append(args, "-T", fmt.Sprintf("%d", rand.Uint64()))
	}
	if payload {
		args = append(args, "-b", "1024")
		args = append(args, "-e", string(bigPayload10KiB))
	}
	// Construct URI
	uri := fmt.Sprintf("coap://%s%s", address, path)
	args = append(args, uri)

	// Prepare command
	cmd := exec.Command(coapClientPath, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	// Execute command
	err = cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("coap-client error: %v, output: %s", err, out.String())
	}
	return out.Bytes(), nil
}
