package client_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

func TestConnPutBlockwiseWithoutTokenLibCoAP(t *testing.T) {
	serverPath, err := findLibCoAPServer()
	if err != nil {
		t.Skip(err)
	}

	portListener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	port := portListener.LocalAddr().(*net.UDPAddr).Port
	require.NoError(t, portListener.Close())

	serverCtx, cancelServer := context.WithCancel(context.Background())
	server := exec.CommandContext(serverCtx, serverPath, "-A", "127.0.0.1", "-p", strconv.Itoa(port), "-d", "10", "-e")
	var serverOutput bytes.Buffer
	server.Stdout = &serverOutput
	server.Stderr = &serverOutput
	require.NoError(t, server.Start())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Wait()
	}()
	serverWaited := false
	defer func() {
		cancelServer()
		if serverWaited {
			return
		}
		select {
		case waitErr := <-serverDone:
			if waitErr != nil {
				var exitErr *exec.ExitError
				require.ErrorAs(t, waitErr, &exitErr)
			}
		case <-time.After(5 * time.Second):
			require.Fail(t, "libcoap server did not stop after cancellation")
		}
	}()

	waitForLibCoAPServer(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	select {
	case waitErr := <-serverDone:
		serverWaited = true
		require.FailNow(t, "libcoap server exited before the test", "error: %v, output: %s", waitErr, serverOutput.String())
	default:
	}

	cc, err := udp.Dial(net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		options.WithBlockwise(true, blockwise.SZX1024, 5*time.Second),
		options.WithTransmission(1, 200*time.Millisecond, 4, 1.5),
	)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, cc.Close())
	}()

	payload := bytes.Repeat([]byte("blockwise-libcoap-payload-"), 200)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := cc.NewPutRequest(ctx, "/tokenless", message.AppOctets, bytes.NewReader(payload))
	require.NoError(t, err)
	defer cc.ReleaseMessage(req)
	req.SetToken(nil)

	resp, err := cc.Do(req)
	require.NoError(t, err)
	defer cc.ReleaseMessage(resp)
	require.Equal(t, codes.Created, resp.Code())
	require.Empty(t, resp.Token())
	require.Equal(t, payload, bodyToBytes(t, resp.Body()))
}

func waitForLibCoAPServer(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	ping := message.Message{Code: codes.Empty, Type: message.Confirmable, MessageID: 1}
	size, err := coder.DefaultCoder.Size(ping)
	require.NoError(t, err)
	request := make([]byte, size)
	_, err = coder.DefaultCoder.Encode(ping, request)
	require.NoError(t, err)

	var lastErr error
	for time.Now().Before(deadline) {
		lastErr = pingLibCoAPServer(address, request, ping.MessageID)
		if lastErr == nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	require.FailNow(t, "libcoap server did not respond to CoAP ping", "address: %s, error: %v", address, lastErr)
}

func pingLibCoAPServer(address string, request []byte, messageID int32) error {
	conn, err := net.Dial("udp", address)
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadlineErr := conn.SetDeadline(time.Now().Add(100 * time.Millisecond)); deadlineErr != nil {
		return deadlineErr
	}
	if _, writeErr := conn.Write(request); writeErr != nil {
		return writeErr
	}
	response := make([]byte, 64)
	n, readErr := conn.Read(response)
	if readErr != nil {
		return readErr
	}
	var decoded message.Message
	if _, decodeErr := coder.DefaultCoder.Decode(response[:n], &decoded); decodeErr != nil {
		return decodeErr
	}
	if decoded.Type != message.Reset || decoded.MessageID != messageID {
		return fmt.Errorf("unexpected response to CoAP ping: type %v, MID %d", decoded.Type, decoded.MessageID)
	}
	return nil
}

func findLibCoAPServer() (string, error) {
	for _, name := range []string{"coap-server-notls", "coap-server"} {
		path, err := exec.LookPath(name)
		if err == nil {
			return path, nil
		}
	}
	return "", errors.New("coap-server not found in PATH")
}
