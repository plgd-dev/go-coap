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

	if readinessErr := waitForLibCoAPServer(serverDone, &serverWaited,
		net.JoinHostPort("127.0.0.1", strconv.Itoa(port))); readinessErr != nil {
		require.FailNow(t, "libcoap server did not become ready", "%v; output: %s", readinessErr, serverOutput.String())
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

func findLibCoAPServer() (string, error) {
	for _, name := range []string{"coap-server-notls", "coap-server"} {
		path, err := exec.LookPath(name)
		if err == nil {
			return path, nil
		}
	}
	return "", errors.New("coap-server not found in PATH")
}

func waitForLibCoAPServer(serverDone <-chan error, serverWaited *bool, address string) error {
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case serverErr := <-serverDone:
			*serverWaited = true
			return fmt.Errorf("libcoap server exited before becoming ready: %w", serverErr)
		default:
		}

		lastErr = probeLibCoAPServer(address)
		if lastErr == nil {
			return nil
		}

		select {
		case serverErr := <-serverDone:
			*serverWaited = true
			if serverErr != nil {
				return fmt.Errorf("libcoap server exited before becoming ready: %w", serverErr)
			}
			return errors.New("libcoap server exited before becoming ready")
		case <-time.After(50 * time.Millisecond):
		}
	}
	if lastErr == nil {
		return errors.New("timed out waiting for libcoap server response")
	}
	return fmt.Errorf("timed out waiting for libcoap server response: %w", lastErr)
}

func probeLibCoAPServer(address string) error {
	cc, err := udp.Dial(address, options.WithErrors(func(error) {}))
	if err != nil {
		return err
	}
	defer cc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	req, err := cc.NewGetRequest(ctx, "/")
	if err != nil {
		return err
	}
	defer cc.ReleaseMessage(req)
	resp, err := cc.Do(req)
	if err != nil {
		return err
	}
	cc.ReleaseMessage(resp)
	return nil
}
