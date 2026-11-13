package udp_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	piondtls "github.com/pion/dtls/v3"
	"github.com/plgd-dev/go-coap/v3/dtls"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/blockwise/qblock"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

func TestQBlockServerConfiguredMTU(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(fmt.Sprintf("secure=%v", secure), func(t *testing.T) {
			testQBlockServerConfiguredMTU(t, secure)
		})
	}
}

func TestQBlockMissingReportFitsConfiguredMTU(t *testing.T) {
	const (
		mtu        = 256
		blockCount = 257
	)
	payload := make([]byte, blockCount*16)
	for i := range payload {
		payload[i] = byte(i)
	}
	qConfig := qblock.DefaultConfig()
	qConfig.MaxPayloads = 256
	config := qGoServerOptions{mtu: mtu, blockwise: qConfig, path: "/mtu/recovery/long-path"}
	uri := startQGoServerWithMTU(t, false, blockwise.SZX16, payload,
		new(qWireCounts), new(atomic.Int32), config)
	addr := strings.TrimPrefix(uri, "coap://")
	target, err := net.ResolveUDPAddr("udp4", addr)
	require.NoError(t, err)
	conn, err := net.DialUDP("udp4", nil, target)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))

	var messageID int32
	sendBlock := func(num int64, token []byte) {
		messageID++
		value, encodeErr := blockwise.EncodeQBlockOption(blockwise.SZX16, num, num < blockCount-1)
		require.NoError(t, encodeErr)
		opts, _, pathErr := (message.Options{}).SetPath(make([]byte, 128), config.path)
		require.NoError(t, pathErr)
		opts, _, err = opts.SetUint32(make([]byte, 4), message.QBlock1, value)
		require.NoError(t, err)
		opts, _, err = opts.SetUint32(make([]byte, 4), message.Size1, uint32(len(payload)))
		require.NoError(t, err)
		opts, _, err = opts.SetBytes(make([]byte, 8), message.RequestTag, []byte("12345678"))
		require.NoError(t, err)
		start, end := num*16, min((num+1)*16, int64(len(payload)))
		request := message.Message{
			Code: codes.PUT, Type: message.NonConfirmable, MessageID: messageID,
			Token: token, Options: opts, Payload: payload[start:end],
		}
		qRawSend(t, conn, request)
	}
	readResponse := func(token []byte) message.Message {
		for {
			response := qRawReceive(t, conn)
			if bytes.Equal(response.Token, token) {
				return response
			}
		}
	}

	triggerToken := []byte("trigger1")
	sendBlock(blockCount-1, triggerToken)
	for reportIndex := 0; ; reportIndex++ {
		response := readResponse(triggerToken)
		if response.Code != codes.RequestEntityIncomplete {
			require.Equal(t, codes.Changed, response.Code)
			break
		}
		wireSize, sizeErr := coder.DefaultCoder.Size(response)
		require.NoError(t, sizeErr)
		require.LessOrEqual(t, wireSize, mtu, "missing-block report %d exceeds configured MTU", reportIndex)
		missing, decodeErr := blockwise.DecodeMissingBlocks(nil, response.Payload)
		require.NoError(t, decodeErr)
		require.NotEmpty(t, missing)
		require.Less(t, len(missing), 256, "the 256-block recovery set must be split")
		for _, num := range missing {
			sendBlock(int64(num), []byte{byte(num), 1})
		}
		triggerToken = []byte{byte(reportIndex + 1), 't', 'r', 'i', 'g', 'g', 'e', 'r'}
		sendBlock(blockCount-1, triggerToken)
	}

	highNUM := int64(65535)
	highValue, err := blockwise.EncodeQBlockOption(blockwise.SZX16, highNUM, false)
	require.NoError(t, err)
	opts, _, err := (message.Options{}).SetPath(make([]byte, 128), config.path)
	require.NoError(t, err)
	opts, _, err = opts.SetUint32(make([]byte, 4), message.QBlock1, highValue)
	require.NoError(t, err)
	opts, _, err = opts.SetUint32(make([]byte, 4), message.Size1, uint32(highNUM*16+1))
	require.NoError(t, err)
	opts, _, err = opts.SetBytes(make([]byte, 8), message.RequestTag, []byte("high-num"))
	require.NoError(t, err)
	highNUMToken := []byte("highNUM1")
	qRawSend(t, conn, message.Message{
		Code: codes.PUT, Type: message.NonConfirmable, MessageID: messageID + 1,
		Token: highNUMToken, Options: opts, Payload: []byte{0xaa},
	})
	response := readResponse(highNUMToken)
	require.Equal(t, codes.RequestEntityIncomplete, response.Code)
	wireSize, err := coder.DefaultCoder.Size(response)
	require.NoError(t, err)
	require.LessOrEqual(t, wireSize, mtu)
	missing, err := blockwise.DecodeMissingBlocks(nil, response.Payload)
	require.NoError(t, err)
	require.NotEmpty(t, missing)
}

func testQBlockServerConfiguredMTU(t *testing.T, secure bool) {
	const mtu = 256
	limit := mtu
	if secure {
		limit -= 128
	}
	payload := qPayload()
	uri := startQGoServerWithMTU(t, secure, blockwise.SZX1024, payload,
		new(qWireCounts), new(atomic.Int32), qGoServerOptions{mtu: mtu})
	addr := strings.TrimPrefix(strings.TrimPrefix(uri, "coaps://"), "coap://")
	var largest atomic.Int64
	monitor := func(_ *client.Conn, msg *pool.Message) (bool, error) {
		return measureQContentWire(msg, &largest)
	}
	opts := []udp.Option{
		options.WithQBlockwise(options.QBlockwiseOnly),
		options.WithBlockwise(true, blockwise.SZX1024, time.Minute),
		options.WithRequestMonitor(monitor),
	}
	cc := qDialWithOptions(t, addr, secure, opts...)
	defer cc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response, err := cc.Get(ctx, "/q")
	require.NoError(t, err)
	defer cc.ReleaseMessage(response)
	body, err := response.ReadBody()
	require.NoError(t, err)
	require.Equal(t, payload, body)
	require.Positive(t, largest.Load())
	require.LessOrEqual(t, largest.Load(), int64(limit))
}

func measureQContentWire(msg *pool.Message, largest *atomic.Int64) (bool, error) {
	if msg.Code() != codes.Content || !msg.HasOption(message.QBlock2) {
		return false, nil
	}
	wire, err := msg.MarshalWithEncoder(coder.DefaultCoder)
	if err != nil {
		return false, err
	}
	for previous := largest.Load(); int64(len(wire)) > previous; previous = largest.Load() {
		if largest.CompareAndSwap(previous, int64(len(wire))) {
			break
		}
	}
	return false, nil
}

func qDialWithOptions(t *testing.T, addr string, secure bool, opts ...udp.Option) *client.Conn {
	t.Helper()
	var cc *client.Conn
	var err error
	if secure {
		cc, err = dtls.Dial(addr, dtls.NewDTLSClientOptions(
			piondtls.WithPSK(qLibcoapPSKCallback),
			piondtls.WithPSKIdentityHint([]byte("qblock-test")),
			piondtls.WithCipherSuites(piondtls.TLS_PSK_WITH_AES_128_GCM_SHA256)), opts...)
	} else {
		cc, err = udp.Dial(addr, opts...)
	}
	require.NoError(t, err)
	return cc
}
