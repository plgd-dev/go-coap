package qblock

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/stretchr/testify/require"
)

func TestQBlockTransferKeySizeIsIndependentOfRequestOptions(t *testing.T) {
	newRequest := func(optionCount int) *pool.Message {
		request := pool.NewMessage(context.Background())
		options := make(message.Options, optionCount)
		for i := range options {
			options[i] = message.Option{ID: 300, Value: []byte{byte(i)}}
		}
		request.SetMessage(message.Message{Code: codes.GET, Options: options})
		request.SetToken([]byte("token"))
		return request
	}

	small := newRequest(1)
	large := newRequest(5000)
	remote := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5683}
	otherRemote := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5684}

	smallQ1Key := q1TransferKey(small, remote, []byte("tag"))
	largeQ1Key := q1TransferKey(large, remote, []byte("tag"))
	require.Len(t, smallQ1Key, sha256.Size)
	require.Len(t, largeQ1Key, len(smallQ1Key))
	require.NotEqual(t, smallQ1Key, largeQ1Key, "distinct requests must have distinct identities")
	require.NotEqual(t, smallQ1Key, q1TransferKey(small, remote, []byte("other-tag")))
	require.NotEqual(t, smallQ1Key, q1TransferKey(small, otherRemote, []byte("tag")))

	smallQ2Key := q2TransferKey(small, remote)
	largeQ2Key := q2TransferKey(large, remote)
	require.Len(t, largeQ2Key, len(smallQ2Key))
	require.NotEqual(t, smallQ2Key, largeQ2Key, "distinct requests must have distinct identities")
	require.NotEqual(t, smallQ2Key, q2TransferKey(small, otherRemote))
}

func TestQBlockObserveRegistrationStoresConstantSizeIdentity(t *testing.T) {
	cc := newTestClient()
	engine := New(cc, nil)
	defer engine.CloseQBlockTransfers()

	request := pool.NewMessage(context.Background())
	request.SetMessage(message.Message{
		Code:    codes.GET,
		Options: make(message.Options, 5000),
	})
	for i := range request.Options() {
		request.Options()[i] = message.Option{ID: 300, Value: []byte{byte(i)}}
	}
	request.SetToken([]byte("observe"))
	request.SetObserve(0)
	remote := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5683}

	require.NoError(t, engine.registerQObservation(request, remote, SZX16))
	observation := engine.qObservations[qObservationKey(request.Token(), remote)]
	require.NotNil(t, observation)
	require.Len(t, observation.identity, sha256.Size)
	require.Equal(t, qTransferKey(request, remote, nil, true), observation.identity)
}

func TestQBlockServerRetentionCoversReceiveWindow(t *testing.T) {
	for _, age := range []uint32{0, 1, 10, 1000} {
		t.Run(fmt.Sprint(age), func(t *testing.T) {
			cc := newTestClient()
			engine := New(cc, func(err error) { t.Error(err) })
			defer engine.CloseQBlockTransfers()
			config := DefaultConfig()
			config.NonReceiveTimeout = 7 * time.Second
			require.NoError(t, engine.SetConfig(config))
			response := cc.AcquireMessage(context.Background())
			defer cc.ReleaseMessage(response)
			response.SetCode(codes.Content)
			response.SetOptionUint32(message.MaxAge, age)
			response.SetBody(bytes.NewReader(make([]byte, 32)))
			before := time.Now()
			require.NoError(t, engine.cacheQBlock2Response(context.Background(), "body", "identity",
				response, message.NonConfirmable))
			want := min(config.NonPartialTimeout(), max(config.NonReceiveTimeout, time.Duration(age)*time.Second))
			require.WithinDuration(t, before.Add(want), engine.q2Senders["body"].expires, time.Second)
			part, err := engine.buildQBlock2Response(context.Background(), "body", []byte("token"), SZX16, 1)
			require.NoError(t, err)
			defer cc.ReleaseMessage(part)
			gotAge, err := part.GetOptionUint32(message.MaxAge)
			require.NoError(t, err)
			require.Equal(t, age, gotAge, "retention must not increase cache freshness")
		})
	}
}
