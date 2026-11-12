package client_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	piondtls "github.com/pion/dtls/v3"
	piondtlsnet "github.com/pion/dtls/v3/pkg/net"
	coapDTLS "github.com/plgd-dev/go-coap/v3/dtls"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/oscore"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/stretchr/testify/require"
)

func TestLibCoAPServerOSCORE(t *testing.T) {
	serverPath, err := findLibCoAPServer()
	if err != nil {
		requireLibCoAPOSCORE(t, err.Error())
	}
	serverConfig := writeLibCoAPOSCOREConfig(t, "server", "client")
	port := availableUDPPort(t)
	ctx, cancel := context.WithCancel(context.Background())
	serverArgs := []string{"-A", "127.0.0.1", "-p", strconv.Itoa(port), "-v", "8", "-E", serverConfig}
	if libCoAPQBlockRequired() {
		serverArgs = append(serverArgs, "-L", "7")
	} else {
		serverArgs = append(serverArgs, "-e")
	}
	server := exec.CommandContext(ctx, serverPath, serverArgs...)
	var serverOutput lockedBuffer
	server.Stdout = &serverOutput
	server.Stderr = &serverOutput
	require.NoError(t, server.Start())
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Wait() }()
	serverExited := false
	defer func() {
		if !serverExited {
			stopLibCoAPServerProcess(t, cancel, server, serverDone)
		}
	}()

	select {
	case serverErr := <-serverDone:
		serverExited = true
		cancel()
		if strings.Contains(serverOutput.String(), "OSCORE support not enabled") {
			requireLibCoAPOSCORE(t, "installed libcoap was built without OSCORE support")
		}
		require.FailNow(t, "libcoap server exited before the test", "error: %v, output: %s", serverErr, serverOutput.String())
	case <-time.After(100 * time.Millisecond):
	}

	processor := newLibCoAPOSCOREProcessor(t, "client", "server")
	dialOptions := []udp.Option{
		options.WithOSCORE(processor),
		options.WithBlockwise(true, blockwise.SZX1024, 5*time.Second),
	}
	if libCoAPQBlockRequired() {
		dialOptions = append(dialOptions, options.WithQBlockwise(options.QBlockwiseOnly))
	}
	cc, err := udp.Dial(net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), dialOptions...)
	require.NoError(t, err)
	defer func() { require.NoError(t, cc.Close()) }()

	requestCtx, cancelRequest := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelRequest()
	request, err := cc.NewGetRequest(requestCtx, "/time")
	require.NoError(t, err)
	defer cc.ReleaseMessage(request)
	response, err := cc.Do(request)
	require.NoError(t, err, "libcoap output: %s", serverOutput.String())
	defer cc.ReleaseMessage(response)
	body, err := response.ReadBody()
	require.NoError(t, err)
	require.Equal(t, codes.Content, response.Code(), "libcoap output: %s; response payload: %s", serverOutput.String(), body)
	require.False(t, response.HasOption(message.OSCORE), "OSCORE option should be removed after decryption")
	require.NotEmpty(t, body)

	payload := bytes.Repeat([]byte("libcoap-oscore-blockwise-"), 200)
	putRequest, err := cc.NewPutRequest(requestCtx, "/example_data", message.AppOctets, bytes.NewReader(payload))
	require.NoError(t, err)
	defer cc.ReleaseMessage(putRequest)
	putResponse, err := cc.Do(putRequest)
	require.NoError(t, err)
	defer cc.ReleaseMessage(putResponse)
	require.Equal(t, codes.Created, putResponse.Code(), "libcoap output: %s", serverOutput.String())
	putBody, err := putResponse.ReadBody()
	require.NoError(t, err)
	if libCoAPQBlockRequired() {
		require.Empty(t, putBody, "Q-Block upload test does not require the libcoap server to echo a multi-block Q-Block2 body")
	} else {
		require.Equal(t, payload, putBody)
	}

	observeCtx, cancelObserve := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelObserve()
	observeConn, err := udp.Dial(
		net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		options.WithOSCORE(processor),
		options.WithBlockwise(true, blockwise.SZX1024, 5*time.Second),
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, observeConn.Close()) }()
	observed := make(chan struct{}, 4)
	observation, err := observeConn.Observe(observeCtx, "/time", func(*pool.Message) {
		observed <- struct{}{}
	})
	require.NoError(t, err, "libcoap output: %s", serverOutput.String())
	select {
	case <-observed:
	case <-observeCtx.Done():
		require.Fail(t, "libcoap OSCORE Observe registration produced no response")
	}
	select {
	case <-observed:
	case <-observeCtx.Done():
		require.Fail(t, "libcoap OSCORE Observe produced no notification")
	}
	if !observation.Canceled() {
		require.NoError(t, observation.Cancel(observeCtx))
	}
}

func TestLibCoAPClientOSCOREObserveNotifications(t *testing.T) {
	clientPath, err := exec.LookPath("coap-client-notls")
	if err != nil {
		clientPath, err = exec.LookPath("coap-client")
	}
	if err != nil {
		requireLibCoAPOSCORE(t, "coap-client not found in PATH")
	}

	listener, err := coapNet.NewListenUDP("udp", "127.0.0.1:0")
	require.NoError(t, err)
	var handled atomic.Int32
	server := udp.NewServer(
		options.WithOSCORE(newLibCoAPOSCOREProcessor(t, "server", "client")),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], req *pool.Message) {
			observe, observeErr := req.Observe()
			if observeErr != nil {
				t.Errorf("get Observe request: %v", observeErr)
				return
			}
			switch observe {
			case 0:
				if responseErr := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("initial"))); responseErr != nil {
					t.Errorf("set initial Observe response: %v", responseErr)
					return
				}
				w.Message().SetObserve(0)
				cc := w.Conn()
				token := append(message.Token(nil), req.Token()...)
				go func() {
					time.Sleep(100 * time.Millisecond)
					for sequence := uint32(1); sequence <= 2; sequence++ {
						notification := cc.AcquireMessage(cc.Context())
						notification.SetCode(codes.Content)
						notification.SetToken(token)
						notification.SetObserve(sequence)
						notification.SetBody(bytes.NewReader([]byte("notify")))
						writeErr := cc.WriteMessage(notification)
						cc.ReleaseMessage(notification)
						if writeErr != nil {
							t.Errorf("send OSCORE notification: %v", writeErr)
							return
						}
						handled.Add(1)
					}
				}()
			case 1:
				if responseErr := w.SetResponse(codes.Content, message.TextPlain, nil); responseErr != nil {
					t.Errorf("set Observe cancellation response: %v", responseErr)
				}
			default:
				t.Errorf("unexpected Observe value %d", observe)
			}
		}),
	)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		server.Stop()
		select {
		case serverErr := <-serverDone:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			require.Fail(t, "go-coap OSCORE Observe server did not stop")
		}
	}()

	clientConfig := writeLibCoAPOSCOREConfig(t, "client", "server")
	outputPath := t.TempDir() + "/observe.txt"
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, clientPath, "-E", clientConfig, "-s", "3", "-o", outputPath,
		"-B", "5", "coap://"+listener.LocalAddr().String()+"/observe")
	output, err := cmd.CombinedOutput()
	if strings.Contains(string(output), "OSCORE support not enabled") {
		requireLibCoAPOSCORE(t, "installed libcoap was built without OSCORE support")
	}
	require.NoError(t, err, "libcoap client output: %s", output)
	body, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	require.GreaterOrEqual(t, strings.Count(string(body), "notify"), 2, "libcoap output: %s", output)
	require.Equal(t, int32(2), handled.Load())
}

func TestLibCoAPServerRejectsWrongOSCOREContext(t *testing.T) {
	serverPath, err := findLibCoAPServer()
	if err != nil {
		requireLibCoAPOSCORE(t, err.Error())
	}
	serverConfig := writeLibCoAPOSCOREConfig(t, "server", "client")
	port := availableUDPPort(t)
	ctx, cancel := context.WithCancel(context.Background())
	server := exec.CommandContext(ctx, serverPath, "-A", "127.0.0.1", "-p", strconv.Itoa(port), "-e", "-v", "8", "-E", serverConfig)
	var serverOutput lockedBuffer
	server.Stdout = &serverOutput
	server.Stderr = &serverOutput
	require.NoError(t, server.Start())
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Wait() }()
	serverExited := false
	defer func() {
		if !serverExited {
			stopLibCoAPServerProcess(t, cancel, server, serverDone)
		}
	}()

	select {
	case serverErr := <-serverDone:
		serverExited = true
		cancel()
		if strings.Contains(serverOutput.String(), "OSCORE support not enabled") {
			requireLibCoAPOSCORE(t, "installed libcoap was built without OSCORE support")
		}
		require.FailNow(t, "libcoap server exited before the test", "error: %v, output: %s", serverErr, serverOutput.String())
	case <-time.After(100 * time.Millisecond):
	}

	wrongContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          []byte("incorrect oscore secret"),
		MasterSalt:            []byte{0x9e, 0x7c, 0xa9, 0x22, 0x23, 0x78, 0x63, 0x40},
		SenderID:              []byte("client"),
		RecipientID:           []byte("server"),
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts:        []*oscore.Context{wrongContext},
		PlaintextPolicy: oscore.PlaintextDisabled,
	})
	require.NoError(t, err)
	clientErrors := make(chan error, 1)
	cc, err := udp.Dial(
		net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		options.WithOSCORE(processor),
		options.WithTransmission(1, 100*time.Millisecond, 1, 1),
		options.WithErrors(func(err error) {
			select {
			case clientErrors <- err:
			default:
			}
		}),
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, cc.Close()) }()

	requestCtx, cancelRequest := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelRequest()
	request, err := cc.NewGetRequest(requestCtx, "/time")
	require.NoError(t, err)
	defer cc.ReleaseMessage(request)
	response, err := cc.Do(request)
	require.NoError(t, err)
	require.NotNil(t, response)
	defer cc.ReleaseMessage(response)
	require.True(t, oscore.IsUnprotectedProcessingError(response.Context()),
		"plaintext OSCORE processing errors must be identified as unauthenticated")
	require.True(t, oscore.IsUnprotectedProcessingErrorCode(response.Code()),
		"unexpected unprotected response code: %v", response.Code())
	select {
	case clientErr := <-clientErrors:
		require.Fail(t, "client reported an error for an allowed OSCORE processing error", "error: %v", clientErr)
	default:
	}
	require.NoError(t, cc.Context().Err(), "handling an unauthenticated processing error closed the UDP connection")
}

func TestLibCoAPClientOSCORE(t *testing.T) {
	clientPath, err := exec.LookPath("coap-client-notls")
	if err != nil {
		clientPath, err = exec.LookPath("coap-client")
	}
	if err != nil {
		requireLibCoAPOSCORE(t, "coap-client not found in PATH")
	}
	listener, err := coapNet.NewListenUDP("udp", "127.0.0.1:0")
	require.NoError(t, err)
	serverProcessor := newLibCoAPOSCOREProcessor(t, "server", "client")
	responsePayload := []byte("go-coap oscore")
	if libCoAPQBlockRequired() {
		responsePayload = bytes.Repeat([]byte("go-coap-oscore-qblock-"), 240)
	}
	server := udp.NewServer(
		options.WithOSCORE(serverProcessor),
		options.WithQBlockwise(options.QBlockwiseOnly),
		options.WithBlockwise(true, blockwise.SZX1024, 5*time.Second),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], req *pool.Message) {
			if req.Code() != codes.GET {
				t.Errorf("unexpected request code %v", req.Code())
			}
			if responseErr := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader(responsePayload)); responseErr != nil {
				t.Errorf("set content response: %v", responseErr)
			}
		}),
	)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		server.Stop()
		select {
		case serverErr := <-serverDone:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			require.Fail(t, "go-coap server did not stop")
		}
	}()

	clientConfig := writeLibCoAPOSCOREConfig(t, "client", "server")
	responsePath := t.TempDir() + "/response.txt"
	serverAddr := listener.LocalAddr().String()
	commandCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	args := []string{"-E", clientConfig, "-o", responsePath, "-B", "5"}
	if libCoAPQBlockRequired() {
		args = append(args, "-O", "31,0x0e")
	}
	args = append(args, "coap://"+serverAddr+"/test")
	cmd := exec.CommandContext(commandCtx, clientPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil && strings.Contains(string(output), "OSCORE support not enabled") {
		requireLibCoAPOSCORE(t, "installed libcoap was built without OSCORE support")
	}
	require.NoError(t, err, "libcoap client output: %s", output)
	response, err := os.ReadFile(responsePath)
	require.NoError(t, err)
	require.Equal(t, responsePayload, response)
}

func TestLibCoAPServerOSCOREDTLS(t *testing.T) {
	serverPath, err := findLibCoAPServer()
	if err != nil {
		requireLibCoAPOSCORE(t, err.Error())
	}
	basePort, releasePorts := reserveLibCoAPServerPorts(t)
	serverPort := basePort + 1
	serverConfig := writeLibCoAPOSCOREConfig(t, "server", "client")
	ctx, cancel := context.WithCancel(context.Background())
	server := exec.CommandContext(ctx, serverPath, "-A", "127.0.0.1", "-p", strconv.Itoa(basePort),
		"-v", "8", "-k", "0xabc123", "-E", serverConfig, "-e")
	var serverOutput lockedBuffer
	server.Stdout = &serverOutput
	server.Stderr = &serverOutput
	releasePorts()
	require.NoError(t, server.Start())
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Wait() }()
	serverExited := false
	defer func() {
		if !serverExited {
			stopLibCoAPServerProcess(t, cancel, server, serverDone)
		}
	}()

	processor := newLibCoAPOSCOREProcessor(t, "client", "server")
	var cc *client.Conn
	var response *pool.Message
	var readyErr error
	cc, response, serverExited, readyErr = waitForLibCoAPDTLSOSCORE(
		serverDone,
		net.JoinHostPort("127.0.0.1", strconv.Itoa(serverPort)),
		processor,
		&serverOutput,
	)
	if serverExited {
		cancel()
		if strings.Contains(serverOutput.String(), "OSCORE support not enabled") ||
			strings.Contains(serverOutput.String(), "DTLS support not enabled") {
			requireLibCoAPOSCORE(t, "libcoap server lacks OSCORE or DTLS support")
		}
		require.FailNow(t, "libcoap server exited before becoming ready", "error: %v, output: %s", readyErr, serverOutput.String())
	}
	require.NoError(t, readyErr, "libcoap output: %s", serverOutput.String())
	defer func() { require.NoError(t, cc.Close()) }()
	defer cc.ReleaseMessage(response)
	require.Equal(t, codes.Content, response.Code(), "libcoap output: %s", serverOutput.String())
	body, err := response.ReadBody()
	require.NoError(t, err)
	require.NotEmpty(t, body)
}

func TestLibCoAPClientOSCOREDTLSRejectsInvalidContextAndReplay(t *testing.T) {
	clientPath, err := exec.LookPath("coap-client-notls")
	if err != nil {
		clientPath, err = exec.LookPath("coap-client")
	}
	if err != nil {
		requireLibCoAPOSCORE(t, "coap-client not found in PATH")
	}

	listener, err := coapNet.NewDTLSListener(
		"udp",
		"127.0.0.1:0",
		coapNet.NewDTLSServerOptions(
			piondtls.WithPSK(func([]byte) ([]byte, error) { return []byte{0xab, 0xc1, 0x23}, nil }),
			piondtls.WithPSKIdentityHint([]byte("go-coap-server")),
			piondtls.WithCipherSuites(piondtls.TLS_PSK_WITH_AES_128_GCM_SHA256),
		),
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, listener.Close()) }()

	var handled atomic.Int32
	serverErrors := make(chan error, 8)
	server := coapDTLS.NewServer(
		options.WithOSCORE(newLibCoAPOSCOREProcessor(t, "server", "client")),
		options.WithErrors(func(serverErr error) {
			select {
			case serverErrors <- serverErr:
			default:
			}
		}),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
			handled.Add(1)
			if responseErr := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("dtls oscore"))); responseErr != nil {
				t.Errorf("set DTLS OSCORE response: %v", responseErr)
			}
		}),
	)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		server.Stop()
		select {
		case serverErr := <-serverDone:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			require.Fail(t, "go-coap DTLS server did not stop")
		}
	}()

	validConfig := writeLibCoAPOSCOREConfig(t, "client", "server")
	invalidConfig := writeLibCoAPOSCOREConfigWithSecret(t, "client", "server", "11111111111111111111111111111111")
	runClient := func(config, responseName string) ([]byte, error) {
		responsePath := t.TempDir() + "/" + responseName
		commandCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		cmd := exec.CommandContext(
			commandCtx,
			clientPath,
			"-E", config,
			"-k", "0xabc123",
			"-u", "libcoap-client",
			"-o", responsePath,
			"-B", "2",
			"coaps://"+listener.Addr().String()+"/test",
		)
		output, commandErr := cmd.CombinedOutput()
		if commandErr != nil && (strings.Contains(string(output), "OSCORE support not enabled") ||
			strings.Contains(string(output), "DTLS support not enabled")) {
			requireLibCoAPOSCORE(t, "libcoap client lacks OSCORE or DTLS support")
		}
		response, readErr := os.ReadFile(responsePath)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			require.NoError(t, readErr)
		}
		return response, commandErr
	}

	_, _ = runClient(invalidConfig, "invalid-response.txt")
	select {
	case serverErr := <-serverErrors:
		require.ErrorContains(t, serverErr, "authentication failed")
	case <-time.After(2 * time.Second):
		require.Fail(t, "go-coap DTLS server did not report invalid OSCORE authentication")
	}
	require.Zero(t, handled.Load(), "invalid OSCORE context reached application handler")

	response, commandErr := runClient(validConfig, "valid-response.txt")
	require.NoError(t, commandErr)
	require.Equal(t, []byte("dtls oscore"), response)
	require.EqualValues(t, 1, handled.Load())

	_, _ = runClient(validConfig, "replay-response.txt")
	select {
	case serverErr := <-serverErrors:
		require.ErrorIs(t, serverErr, oscore.ErrReplay, "replayed OSCORE request error = %v", serverErr)
	case <-time.After(2 * time.Second):
		require.Fail(t, "go-coap DTLS server did not reject the replayed OSCORE request")
	}
	require.EqualValues(t, 1, handled.Load(), "replayed request reached application handler")
}

func TestLibCoAPClientRejectsWrongOSCOREContext(t *testing.T) {
	clientPath, err := exec.LookPath("coap-client-notls")
	if err != nil {
		clientPath, err = exec.LookPath("coap-client")
	}
	if err != nil {
		requireLibCoAPOSCORE(t, "coap-client not found in PATH")
	}
	listener, err := coapNet.NewListenUDP("udp", "127.0.0.1:0")
	require.NoError(t, err)
	var handled atomic.Int32
	serverErrors := make(chan error, 1)
	serverProcessor := newLibCoAPOSCOREProcessor(t, "server", "client")
	server := udp.NewServer(
		options.WithOSCORE(serverProcessor),
		options.WithErrors(func(err error) {
			select {
			case serverErrors <- err:
			default:
			}
		}),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
			handled.Add(1)
			if responseErr := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("unexpected"))); responseErr != nil {
				t.Errorf("set content response: %v", responseErr)
			}
		}),
	)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		server.Stop()
		select {
		case serverErr := <-serverDone:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			require.Fail(t, "go-coap server did not stop")
		}
	}()

	clientConfig := writeLibCoAPOSCOREConfigWithSecret(t, "client", "server", "11111111111111111111111111111111")
	responsePath := t.TempDir() + "/response.txt"
	commandCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, clientPath, "-E", clientConfig, "-o", responsePath, "-B", "2",
		"coap://"+listener.LocalAddr().String()+"/test")
	output, err := cmd.CombinedOutput()
	if err != nil && strings.Contains(string(output), "OSCORE support not enabled") {
		requireLibCoAPOSCORE(t, "installed libcoap was built without OSCORE support")
	}
	if response, readErr := os.ReadFile(responsePath); readErr == nil {
		require.Empty(t, response, "mismatched credentials produced a response; libcoap output: %s", output)
	} else {
		require.ErrorIs(t, readErr, os.ErrNotExist)
	}
	select {
	case serverErr := <-serverErrors:
		require.ErrorContains(t, serverErr, "authentication failed")
	case <-time.After(time.Second):
		require.Fail(t, "go-coap server did not report invalid OSCORE authentication")
	}
	require.Zero(t, handled.Load(), "request with invalid OSCORE authentication reached application handler")
}

func TestLibCoAPClientRejectsTamperedOSCORERequest(t *testing.T) {
	clientPath, err := exec.LookPath("coap-client-notls")
	if err != nil {
		clientPath, err = exec.LookPath("coap-client")
	}
	if err != nil {
		requireLibCoAPOSCORE(t, "coap-client not found in PATH")
	}

	listener, err := coapNet.NewListenUDP("udp", "127.0.0.1:0")
	require.NoError(t, err)
	var handled atomic.Int32
	serverErrors := make(chan error, 1)
	server := udp.NewServer(
		options.WithOSCORE(newLibCoAPOSCOREProcessor(t, "server", "client")),
		options.WithErrors(func(err error) {
			select {
			case serverErrors <- err:
			default:
			}
		}),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
			handled.Add(1)
			if responseErr := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("unexpected"))); responseErr != nil {
				t.Errorf("set content response: %v", responseErr)
			}
		}),
	)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		server.Stop()
		select {
		case serverErr := <-serverDone:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			require.Fail(t, "go-coap server did not stop")
		}
	}()

	serverAddr, ok := listener.LocalAddr().(*net.UDPAddr)
	require.True(t, ok, "go-coap UDP listener has unexpected address type %T", listener.LocalAddr())
	relay := newLibCoAPOSCORERelay(t, serverAddr, func(datagram []byte) ([][]byte, error) {
		if len(datagram) == 0 {
			return nil, os.ErrInvalid
		}
		datagram[len(datagram)-1] ^= 0x01
		return [][]byte{datagram}, nil
	})

	clientConfig := writeLibCoAPOSCOREConfig(t, "client", "server")
	responsePath := t.TempDir() + "/response.txt"
	commandCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, clientPath, "-E", clientConfig, "-o", responsePath, "-B", "2",
		"coap://"+relay.LocalAddr().String()+"/test")
	output, err := cmd.CombinedOutput()
	if strings.Contains(string(output), "OSCORE support not enabled") {
		requireLibCoAPOSCORE(t, "installed libcoap was built without OSCORE support")
	}
	require.NoError(t, commandCtx.Err(), "libcoap command timed out; command error: %v, output: %s", err, output)
	if response, readErr := os.ReadFile(responsePath); readErr == nil {
		require.Empty(t, response, "tampered OSCORE request produced a response; libcoap output: %s", output)
	} else {
		require.ErrorIs(t, readErr, os.ErrNotExist)
	}
	select {
	case serverErr := <-serverErrors:
		require.ErrorContains(t, serverErr, "authentication failed")
	case <-time.After(time.Second):
		require.Fail(t, "go-coap server did not report tampered OSCORE authentication")
	}
	require.Zero(t, handled.Load(), "tampered OSCORE request reached application handler")
}

func TestLibCoAPClientReplayOSCORERequest(t *testing.T) {
	clientPath, err := exec.LookPath("coap-client-notls")
	if err != nil {
		clientPath, err = exec.LookPath("coap-client")
	}
	if err != nil {
		requireLibCoAPOSCORE(t, "coap-client not found in PATH")
	}

	listener, err := coapNet.NewListenUDP("udp", "127.0.0.1:0")
	require.NoError(t, err)
	var handled atomic.Int32
	serverErrors := make(chan error, 1)
	server := udp.NewServer(
		options.WithOSCORE(newLibCoAPOSCOREProcessor(t, "server", "client")),
		options.WithErrors(func(err error) {
			select {
			case serverErrors <- err:
			default:
			}
		}),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
			handled.Add(1)
			if responseErr := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("replay test"))); responseErr != nil {
				t.Errorf("set content response: %v", responseErr)
			}
		}),
	)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		server.Stop()
		select {
		case serverErr := <-serverDone:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			require.Fail(t, "go-coap server did not stop")
		}
	}()

	serverAddr, ok := listener.LocalAddr().(*net.UDPAddr)
	require.True(t, ok, "go-coap UDP listener has unexpected address type %T", listener.LocalAddr())
	relay := newLibCoAPOSCORERelay(t, serverAddr, func(datagram []byte) ([][]byte, error) {
		if len(datagram) < 4 {
			return nil, os.ErrInvalid
		}
		replayed := append([]byte(nil), datagram...)
		replayed[3]++
		return [][]byte{datagram, replayed}, nil
	})

	clientConfig := writeLibCoAPOSCOREConfig(t, "client", "server")
	responsePath := t.TempDir() + "/response.txt"
	commandCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, clientPath, "-E", clientConfig, "-o", responsePath, "-B", "5",
		"coap://"+relay.LocalAddr().String()+"/test")
	output, err := cmd.CombinedOutput()
	if strings.Contains(string(output), "OSCORE support not enabled") {
		requireLibCoAPOSCORE(t, "installed libcoap was built without OSCORE support")
	}
	require.NoError(t, err, "libcoap client failed: %s", output)
	require.NoError(t, commandCtx.Err())
	response, err := os.ReadFile(responsePath)
	require.NoError(t, err)
	require.Equal(t, []byte("replay test"), response)
	select {
	case serverErr := <-serverErrors:
		require.ErrorIs(t, serverErr, oscore.ErrReplay)
	case <-time.After(time.Second):
		require.Fail(t, "go-coap server did not report the replayed OSCORE request")
	}
	require.Equal(t, int32(1), handled.Load(), "replayed OSCORE request reached application handler")
}

func newLibCoAPOSCORERelay(
	t *testing.T,
	serverAddr *net.UDPAddr,
	transform func([]byte) ([][]byte, error),
) *net.UDPConn {
	t.Helper()
	relay, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	stopRelay := make(chan struct{})
	relayDone := make(chan struct{})
	relayErrors := make(chan error, 1)
	go func() {
		defer close(relayDone)
		reportError := func(err error) {
			select {
			case <-stopRelay:
			default:
				select {
				case relayErrors <- err:
				default:
				}
			}
		}
		buffer := make([]byte, 65535)
		var clientAddr *net.UDPAddr
		for {
			n, addr, err := relay.ReadFromUDP(buffer)
			if err != nil {
				select {
				case <-stopRelay:
					return
				default:
					reportError(err)
					return
				}
			}
			if addr.IP.Equal(serverAddr.IP) && addr.Port == serverAddr.Port {
				if clientAddr != nil {
					if _, writeErr := relay.WriteToUDP(buffer[:n], clientAddr); writeErr != nil {
						reportError(writeErr)
						return
					}
				}
				continue
			}
			clientAddr = &net.UDPAddr{
				IP:   append(net.IP(nil), addr.IP...),
				Port: addr.Port,
				Zone: addr.Zone,
			}
			requests, err := transform(append([]byte(nil), buffer[:n]...))
			if err != nil {
				reportError(err)
				return
			}
			for _, request := range requests {
				if _, err := relay.WriteToUDP(request, serverAddr); err != nil {
					reportError(err)
					return
				}
			}
		}
	}()
	t.Cleanup(func() {
		close(stopRelay)
		require.NoError(t, relay.Close())
		<-relayDone
		select {
		case err := <-relayErrors:
			require.NoError(t, err)
		default:
		}
	})
	return relay
}

func availableUDPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	port := listener.LocalAddr().(*net.UDPAddr).Port
	require.NoError(t, listener.Close())
	return port
}

func reserveLibCoAPServerPorts(t *testing.T) (int, func()) {
	t.Helper()
	for range 64 {
		base, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		basePort := base.LocalAddr().(*net.UDPAddr).Port
		if basePort >= 65535 {
			_ = base.Close()
			continue
		}
		dtls, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: basePort + 1})
		if err != nil {
			_ = base.Close()
			continue
		}
		tcpBase, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: basePort})
		if err != nil {
			_ = dtls.Close()
			_ = base.Close()
			continue
		}
		tcpDTLS, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: basePort + 1})
		if err != nil {
			_ = tcpBase.Close()
			_ = dtls.Close()
			_ = base.Close()
			continue
		}
		var once sync.Once
		release := func() {
			once.Do(func() {
				require.NoError(t, tcpDTLS.Close())
				require.NoError(t, tcpBase.Close())
				require.NoError(t, dtls.Close())
				require.NoError(t, base.Close())
			})
		}
		t.Cleanup(release)
		return basePort, release
	}
	t.Fatal("could not reserve consecutive UDP/TCP ports for libcoap DTLS")
	return 0, func() {}
}

func waitForLibCoAPDTLSOSCORE(
	serverDone <-chan error,
	address string,
	processor *oscore.Processor,
	serverOutput *lockedBuffer,
) (*client.Conn, *pool.Message, bool, error) {
	deadline := time.Now().Add(8 * time.Second)
	lastErr := errors.New("no DTLS readiness attempt completed")
	for time.Now().Before(deadline) {
		select {
		case serverErr := <-serverDone:
			return nil, nil, true, libCoAPServerExitError(serverErr)
		default:
		}

		requestCtx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
		dtlsConn, err := dialLibCoAPDTLSWithHandshakeTimeout(requestCtx, address)
		if err == nil {
			cc := coapDTLS.Client(
				dtlsConn,
				options.WithOSCORE(processor),
				options.WithCloseSocket(),
			)
			var request *pool.Message
			request, err = cc.NewGetRequest(requestCtx, "/time")
			if err == nil {
				var response *pool.Message
				response, err = cc.Do(request)
				cc.ReleaseMessage(request)
				if err == nil {
					cancel()
					return cc, response, false, nil
				}
			}
			_ = cc.Close()
		}
		cancel()
		lastErr = err
		select {
		case serverErr := <-serverDone:
			return nil, nil, true, libCoAPServerExitError(serverErr)
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil, nil, false, fmt.Errorf("libcoap DTLS OSCORE server did not become ready: %w; output: %s", lastErr, serverOutput.String())
}

func TestLibCoAPDTLSHandshakeTimeout(t *testing.T) {
	listener, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { require.NoError(t, listener.Close()) }()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	conn, err := dialLibCoAPDTLSWithHandshakeTimeout(ctx, listener.LocalAddr().String())
	require.Error(t, err)
	require.Nil(t, conn)
	require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	require.Less(t, time.Since(started), time.Second)
}

func dialLibCoAPDTLSWithHandshakeTimeout(ctx context.Context, address string) (*piondtls.Conn, error) {
	rawConn, err := (&net.Dialer{}).DialContext(ctx, "udp", address)
	if err != nil {
		return nil, fmt.Errorf("dial libcoap DTLS server: %w", err)
	}
	dtlsConn, err := piondtls.ClientWithOptions(
		piondtlsnet.PacketConnFromConn(rawConn),
		rawConn.RemoteAddr(),
		piondtls.WithPSK(func([]byte) ([]byte, error) { return []byte{0xab, 0xc1, 0x23}, nil }),
		piondtls.WithPSKIdentityHint([]byte("go-coap-client")),
		piondtls.WithCipherSuites(piondtls.TLS_PSK_WITH_AES_128_GCM_SHA256),
	)
	if err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("create libcoap DTLS client: %w", err)
	}
	if err := dtlsConn.HandshakeContext(ctx); err != nil {
		_ = dtlsConn.Close()
		return nil, fmt.Errorf("complete libcoap DTLS handshake: %w", err)
	}
	return dtlsConn, nil
}

func libCoAPServerExitError(err error) error {
	if err != nil {
		return fmt.Errorf("libcoap server exited: %w", err)
	}
	return errors.New("libcoap server exited before becoming ready")
}

func stopLibCoAPServerProcess(t *testing.T, cancel context.CancelFunc, command *exec.Cmd, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			var exitErr *exec.ExitError
			require.ErrorAs(t, err, &exitErr)
		}
	case <-time.After(5 * time.Second):
		if command.Process != nil {
			killErr := command.Process.Kill()
			if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
				t.Errorf("kill libcoap server after shutdown timeout: %v", killErr)
			}
		}
		select {
		case err := <-done:
			if err != nil {
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr)
			}
		case <-time.After(5 * time.Second):
			require.Fail(t, "libcoap server was not reaped after termination")
		}
	}
}

func writeLibCoAPOSCOREConfig(t *testing.T, senderID, recipientID string) string {
	return writeLibCoAPOSCOREConfigWithSecret(t, senderID, recipientID, "0102030405060708090a0b0c0d0e0f10")
}

func writeLibCoAPOSCOREConfigWithSecret(t *testing.T, senderID, recipientID, masterSecret string) string {
	t.Helper()
	config := "master_secret,hex," + masterSecret + "\n" +
		"master_salt,hex,9e7ca92223786340\n" +
		"sender_id,ascii," + senderID + "\n" +
		"recipient_id,ascii," + recipientID + "\n" +
		"rfc8613_b_1_2,bool,false\n"
	path := t.TempDir() + "/oscore.conf"
	require.NoError(t, os.WriteFile(path, []byte(config), 0o600))
	return path
}

func newLibCoAPOSCOREProcessor(t *testing.T, senderID, recipientID string) *oscore.Processor {
	t.Helper()
	securityContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		MasterSalt:            []byte{0x9e, 0x7c, 0xa9, 0x22, 0x23, 0x78, 0x63, 0x40},
		SenderID:              []byte(senderID),
		RecipientID:           []byte(recipientID),
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts:        []*oscore.Context{securityContext},
		PlaintextPolicy: oscore.PlaintextDisabled,
	})
	require.NoError(t, err)
	return processor
}

func requireLibCoAPOSCORE(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv("GOCOAP_REQUIRE_LIBCOAP_OSCORE") == "1" {
		t.Fatalf("required libcoap OSCORE interoperability unavailable: %s", reason)
	}
	t.Skip(reason)
}

func libCoAPQBlockRequired() bool {
	return os.Getenv("LIBCOAP_QBLOCK_REQUIRED") == "1"
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}
