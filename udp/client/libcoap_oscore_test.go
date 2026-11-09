package client_test

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
			stopLibCoAPServer(t, cancel, serverDone)
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

	observeCtx, cancelObserve := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelObserve()
	observed := make(chan struct{}, 4)
	observation, err := cc.Observe(observeCtx, "/time", func(*pool.Message) {
		observed <- struct{}{}
	})
	require.NoError(t, err)
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
			stopLibCoAPServer(t, cancel, serverDone)
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
		MasterSecret: []byte("incorrect oscore secret"),
		MasterSalt:   []byte{0x9e, 0x7c, 0xa9, 0x22, 0x23, 0x78, 0x63, 0x40},
		SenderID:     []byte("client"),
		RecipientID:  []byte("server"),
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts:      []*oscore.Context{wrongContext},
		RequireOSCORE: true,
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
	if response != nil {
		cc.ReleaseMessage(response)
	}
	require.Error(t, err, "go-coap accepted a plaintext response to a request protected with an invalid OSCORE context")
	select {
	case clientErr := <-clientErrors:
		require.ErrorIs(t, clientErr, oscore.ErrOSCORERequired)
	case <-time.After(time.Second):
		require.Fail(t, "go-coap client did not report the rejected plaintext response")
	}
	require.NoError(t, cc.Context().Err(), "rejecting an unauthenticated response closed the UDP connection")
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
		MasterSecret: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		MasterSalt:   []byte{0x9e, 0x7c, 0xa9, 0x22, 0x23, 0x78, 0x63, 0x40},
		SenderID:     []byte(senderID),
		RecipientID:  []byte(recipientID),
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts:      []*oscore.Context{securityContext},
		RequireOSCORE: true,
	})
	require.NoError(t, err)
	return processor
}

func stopLibCoAPServer(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			var exitErr *exec.ExitError
			require.ErrorAs(t, err, &exitErr)
		}
	case <-time.After(5 * time.Second):
		require.Fail(t, "libcoap server did not stop after cancellation")
	}
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
