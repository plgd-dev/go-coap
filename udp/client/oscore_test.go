package client

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/observation"
	"github.com/plgd-dev/go-coap/v3/net/oscore"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

func TestObserveCancellationRequiresMatchingRegistrationOptions(t *testing.T) {
	const token = "observe-token"
	registrationOptions := message.Options{
		{ID: message.Observe, Value: []byte{0}},
		{ID: message.URIPath, Value: []byte("resource")},
	}
	cc := &Conn{
		oscoreObservations: map[string]udpOSCORERequestContext{
			token: {options: registrationOptions, exchange: oscore.Exchange{Observe: true}},
		},
	}
	req := pool.NewMessage(context.Background())
	resp := pool.NewMessage(context.Background())
	cancellationOptions := message.Options{
		{ID: message.Observe, Value: []byte{1}},
		{ID: message.URIPath, Value: []byte("different-resource")},
	}
	req.SetMessage(message.Message{Code: codes.GET, Token: message.Token(token), Options: cancellationOptions})
	resp.SetCode(codes.Content)
	binding := udpOSCORERequestContext{options: cancellationOptions}
	require.NoError(t, cc.updateOSCOREObservationForResponse(req, resp, binding))
	if _, exists := cc.oscoreObservations[token]; !exists {
		t.Fatal("cancellation with different request options removed the Observe registration")
	}

	cancellationOptions[1].Value = []byte("resource")
	req.SetMessage(message.Message{Code: codes.GET, Token: message.Token(token), Options: cancellationOptions})
	binding.options = cancellationOptions
	require.NoError(t, cc.updateOSCOREObservationForResponse(req, resp, binding))
	if _, exists := cc.oscoreObservations[token]; exists {
		t.Fatal("cancellation with matching request options did not remove the Observe registration")
	}
}

func TestUnprotectedErrorResponseOnlyTerminatesOSCOREExchange(t *testing.T) {
	for _, test := range []struct {
		name                string
		code                codes.Code
		wantErr             error
		wantRetained        bool
		wantProcessingError bool
	}{
		{name: "decryption error", code: codes.BadRequest, wantProcessingError: true},
		{name: "unknown context", code: codes.Unauthorized, wantProcessingError: true},
		{name: "decode error", code: codes.BadOption, wantProcessingError: true},
		{name: "application not found", code: codes.NotFound, wantErr: oscore.ErrOSCORERequired, wantRetained: true},
		{name: "application server error", code: codes.InternalServerError, wantErr: oscore.ErrOSCORERequired, wantRetained: true},
		{name: "plaintext success", code: codes.Content, wantErr: oscore.ErrOSCORERequired, wantRetained: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			securityContext, err := oscore.NewContext(oscore.ContextConfig{
				MasterSecret: []byte("processing error response test"),
				SenderID:     []byte("client"),
				RecipientID:  []byte("server"),
			})
			require.NoError(t, err)
			request, exchange, err := securityContext.ProtectRequest(message.Message{
				Code: codes.GET, Token: message.Token{1},
			})
			require.NoError(t, err)
			cc := &Conn{oscoreExchanges: make(map[string]udpOSCOREExchange)}
			record := &udpOSCOREExchangeRecord{
				exchange: udpOSCOREExchange{
					context:  securityContext,
					exchange: exchange,
					expires:  time.Now().Add(time.Minute),
				},
				state: udpOSCOREExchangeSent,
			}
			record.exchange.record = record
			cc.oscoreExchanges[string(request.Token)] = record.exchange
			response := pool.NewMessage(context.Background())
			response.SetMessage(message.Message{Code: test.code, Token: request.Token})

			err = cc.unprotectIncomingResponse(response)
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
			} else {
				require.NoError(t, err)
			}
			_, retained := cc.oscoreExchanges[string(request.Token)]
			require.Equal(t, test.wantRetained, retained)
			require.Equal(t, test.wantProcessingError, oscore.IsUnprotectedProcessingError(response.Context()))
		})
	}
}

func TestProtectOutgoingRequestPreservesControlMessage(t *testing.T) {
	securityContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: []byte("control message test secret"),
		SenderID:     []byte("client"),
		RecipientID:  []byte("server"),
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{Contexts: []*oscore.Context{securityContext}})
	require.NoError(t, err)

	session := newRecordingSession()
	defer session.Close()
	cfg := DefaultConfig
	cfg.OSCORE = processor
	cc := NewConnWithOpts(session, &cfg)
	defer cc.Close()

	request := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(request)
	request.SetCode(codes.GET)
	request.SetToken(message.Token{0x42})
	request.SetControlMessage(&coapNet.ControlMessage{
		Dst:     net.IPv4(203, 0, 113, 10),
		Src:     net.IPv4(192, 0, 2, 20),
		IfIndex: 7,
	})

	wireRequest, release, err := cc.protectOutgoingRequest(request)
	require.NoError(t, err)
	require.NotNil(t, release)
	defer release(false)

	controlMessage := wireRequest.ControlMessage()
	require.NotNil(t, controlMessage)
	require.True(t, controlMessage.Dst.Equal(request.ControlMessage().Dst))
	require.True(t, controlMessage.Src.Equal(request.ControlMessage().Src))
	require.Equal(t, request.ControlMessage().IfIndex, controlMessage.IfIndex)
}

func TestOSCOREOuterBlockwiseUsesConfiguredMaxReassembledPayloadSize(t *testing.T) {
	for _, test := range []struct {
		name string
		size uint32
		want int64
	}{
		{name: "configured", size: 4096, want: 4096},
		{name: "zero uses default", size: 0, want: oscore.MaxUnfragmentedSize},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := newRecordingSession()
			defer session.Close()
			cfg := DefaultConfig
			securityContext, err := oscore.NewContext(oscore.ContextConfig{
				MasterSecret: []byte("outer blockwise limit test"),
				SenderID:     []byte("client"),
				RecipientID:  []byte("server"),
			})
			require.NoError(t, err)
			cfg.OSCORE, err = oscore.NewProcessor(oscore.ProcessorConfig{Contexts: []*oscore.Context{securityContext}})
			require.NoError(t, err)
			cfg.OSCOREOuterBlockwise = true
			cfg.OSCOREOuterBlockwiseMaxReassembledPayloadSize = test.size
			createBlockWise := func(cc *Conn) *blockwise.BlockWise[*Conn] {
				return blockwise.New(cc, time.Minute, func(error) {}, nil)
			}
			cc := NewConnWithOpts(session, &cfg, WithBlockWise(createBlockWise))
			defer cc.Close()

			request := cc.AcquireMessage(context.Background())
			defer cc.ReleaseMessage(request)
			request.SetCode(codes.POST)
			request.SetToken(message.Token{0x42})
			request.SetType(message.Confirmable)
			block, err := blockwise.EncodeBlockOption(blockwise.SZX1024, 0, true)
			require.NoError(t, err)
			request.SetOptionUint32(message.Block1, block)
			request.SetOptionUint32(message.Size1, uint32(test.want+1))
			request.SetBody(bytes.NewReader(make([]byte, blockwise.SZX1024.Size())))
			response := cc.AcquireMessage(context.Background())
			defer cc.ReleaseMessage(response)
			w := responsewriter.New(response, cc)
			called := false
			cc.blockWise.Handle(w, request, blockwise.SZX1024, 64*1024, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5683},
				func(*responsewriter.ResponseWriter[*Conn], *pool.Message) { called = true })
			require.False(t, called)
			require.Equal(t, codes.RequestEntityTooLarge, w.Message().Code())
		})
	}
}

func TestFailedOSCOREWriteRemovesExchange(t *testing.T) {
	securityContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: []byte("failed OSCORE write test"),
		SenderID:     []byte("client"),
		RecipientID:  []byte("server"),
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{Contexts: []*oscore.Context{securityContext}})
	require.NoError(t, err)
	session := newRecordingSession()
	defer session.Close()
	writeErr := errors.New("write failed")
	session.writeErr = writeErr
	cfg := DefaultConfig
	cfg.OSCORE = processor
	cc := NewConnWithOpts(session, &cfg)
	defer cc.Close()

	request := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(request)
	request.SetCode(codes.GET)
	request.SetToken(message.Token{0x73})
	require.ErrorIs(t, cc.WriteMessage(request), writeErr)
	cc.oscoreMu.Lock()
	_, exists := cc.oscoreExchanges[string(request.Token())]
	cc.oscoreMu.Unlock()
	require.False(t, exists, "failed writes must not retain an OSCORE exchange")
}

func TestOSCOREExchangeSurvivesAcknowledgmentTimeoutAfterWrite(t *testing.T) {
	securityContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: []byte("OSCORE acknowledgment timeout test"),
		SenderID:     []byte("client"),
		RecipientID:  []byte("server"),
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{Contexts: []*oscore.Context{securityContext}})
	require.NoError(t, err)
	session := newRecordingSession()
	defer session.Close()
	cfg := DefaultConfig
	cfg.OSCORE = processor
	cc := NewConnWithOpts(session, &cfg)
	defer cc.Close()

	requestCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	request := cc.AcquireMessage(requestCtx)
	defer cc.ReleaseMessage(request)
	request.SetCode(codes.GET)
	request.SetToken(message.Token{0x75})
	err = cc.WriteMessage(request)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, 1, session.writtenCount(), "the protected request was sent before the ACK wait expired")
	cc.oscoreMu.Lock()
	_, exists := cc.oscoreExchanges[string(request.Token())]
	cc.oscoreMu.Unlock()
	require.True(t, exists, "ACK timeout must not roll back an exchange for a request already sent")
}

func TestFailedOSCOREWriteDoesNotRemoveReplacementExchange(t *testing.T) {
	securityContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: []byte("replacement OSCORE exchange test"),
		SenderID:     []byte("client"),
		RecipientID:  []byte("server"),
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{Contexts: []*oscore.Context{securityContext}})
	require.NoError(t, err)
	session := newRecordingSession()
	defer session.Close()
	cfg := DefaultConfig
	cfg.OSCORE = processor
	cc := NewConnWithOpts(session, &cfg)
	defer cc.Close()

	makeRequest := func() *pool.Message {
		request := cc.AcquireMessage(context.Background())
		request.SetCode(codes.GET)
		request.SetToken(message.Token{0x74})
		return request
	}
	firstRequest := makeRequest()
	defer cc.ReleaseMessage(firstRequest)
	_, finishFirst, err := cc.protectOutgoingRequest(firstRequest)
	require.NoError(t, err)
	finishFirst(true)
	cc.oscoreMu.Lock()
	firstExchange := cc.oscoreExchanges[string(message.Token{0x74})].exchange
	cc.oscoreMu.Unlock()
	secondRequest := makeRequest()
	defer cc.ReleaseMessage(secondRequest)
	_, finishSecond, err := cc.protectOutgoingRequest(secondRequest)
	require.NoError(t, err)

	cc.oscoreMu.Lock()
	secondExchange := cc.oscoreExchanges[string(message.Token{0x74})].exchange
	cc.oscoreMu.Unlock()
	finishSecond(false)
	cc.oscoreMu.Lock()
	current := cc.oscoreExchanges[string(message.Token{0x74})].exchange
	cc.oscoreMu.Unlock()
	require.Equal(t, firstExchange.RequestPartialIV, current.RequestPartialIV,
		"failed replacement must restore the earlier request that was sent")
	require.NotEqual(t, firstExchange.RequestPartialIV, secondExchange.RequestPartialIV)
}

func TestFailedConcurrentOSCOREExchangesDoNotRestoreFailedEntry(t *testing.T) {
	securityContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: []byte("failed concurrent OSCORE exchanges test"),
		SenderID:     []byte("client"),
		RecipientID:  []byte("server"),
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{Contexts: []*oscore.Context{securityContext}})
	require.NoError(t, err)
	session := newRecordingSession()
	defer session.Close()
	cfg := DefaultConfig
	cfg.OSCORE = processor
	cc := NewConnWithOpts(session, &cfg)
	defer cc.Close()

	makeRequest := func() *pool.Message {
		request := cc.AcquireMessage(context.Background())
		request.SetCode(codes.GET)
		request.SetToken(message.Token{0x76})
		return request
	}
	firstRequest := makeRequest()
	defer cc.ReleaseMessage(firstRequest)
	_, finishFirst, err := cc.protectOutgoingRequest(firstRequest)
	require.NoError(t, err)
	secondRequest := makeRequest()
	defer cc.ReleaseMessage(secondRequest)
	_, finishSecond, err := cc.protectOutgoingRequest(secondRequest)
	require.NoError(t, err)

	cc.oscoreMu.Lock()
	_, exists := cc.oscoreExchanges[string(message.Token{0x76})]
	cc.oscoreMu.Unlock()
	require.True(t, exists, "the newer pending exchange must be tracked before rollback")
	finishFirst(false)
	finishSecond(false)
	cc.oscoreMu.Lock()
	_, exists = cc.oscoreExchanges[string(message.Token{0x76})]
	cc.oscoreMu.Unlock()
	require.False(t, exists, "rolling back a newer failed request must not restore an older failed request")
}

func TestThreeConcurrentOSCOREExchangeRollbacksRestoreLatestSentEntry(t *testing.T) {
	tests := []struct {
		name        string
		finishOrder func(finish [3]func(bool))
		wantIndex   int
	}{
		{
			name: "first sent, second and third fail",
			finishOrder: func(finish [3]func(bool)) {
				finish[0](true)
				finish[1](false)
				finish[2](false)
			},
			wantIndex: 0,
		},
		{
			name: "second sent, first and third fail",
			finishOrder: func(finish [3]func(bool)) {
				finish[1](true)
				finish[0](false)
				finish[2](false)
			},
			wantIndex: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			securityContext, err := oscore.NewContext(oscore.ContextConfig{
				MasterSecret: []byte("three concurrent OSCORE exchanges test"),
				SenderID:     []byte("client"),
				RecipientID:  []byte("server"),
			})
			require.NoError(t, err)
			processor, err := oscore.NewProcessor(oscore.ProcessorConfig{Contexts: []*oscore.Context{securityContext}})
			require.NoError(t, err)
			session := newRecordingSession()
			defer session.Close()
			cfg := DefaultConfig
			cfg.OSCORE = processor
			cc := NewConnWithOpts(session, &cfg)
			defer cc.Close()

			var finish [3]func(bool)
			var piv [3][]byte
			for i := range finish {
				request := cc.AcquireMessage(context.Background())
				defer cc.ReleaseMessage(request)
				request.SetCode(codes.GET)
				request.SetToken(message.Token{0x77})
				_, release, protectErr := cc.protectOutgoingRequest(request)
				require.NoError(t, protectErr)
				finish[i] = release
				cc.oscoreMu.Lock()
				piv[i] = append([]byte(nil), cc.oscoreExchanges[string(request.Token())].exchange.RequestPartialIV...)
				cc.oscoreMu.Unlock()
			}

			tt.finishOrder(finish)
			cc.oscoreMu.Lock()
			current, exists := cc.oscoreExchanges[string(message.Token{0x77})]
			cc.oscoreMu.Unlock()
			require.True(t, exists)
			require.Equal(t, piv[tt.wantIndex], current.exchange.RequestPartialIV)
		})
	}
}

func TestOSCOREExchangeChainIsBoundedPerToken(t *testing.T) {
	securityContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: []byte("bounded OSCORE exchange chain test"),
		SenderID:     []byte("client"),
		RecipientID:  []byte("server"),
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{Contexts: []*oscore.Context{securityContext}})
	require.NoError(t, err)
	session := newRecordingSession()
	defer session.Close()
	cfg := DefaultConfig
	cfg.OSCORE = processor
	cc := NewConnWithOpts(session, &cfg)
	defer cc.Close()

	finish := make([]func(bool), 0, maxOSCOREExchangeRecordsPerToken)
	for range maxOSCOREExchangeRecordsPerToken {
		request := cc.AcquireMessage(context.Background())
		request.SetCode(codes.GET)
		request.SetToken(message.Token{0x78})
		_, release, protectErr := cc.protectOutgoingRequest(request)
		cc.ReleaseMessage(request)
		require.NoError(t, protectErr)
		finish = append(finish, release)
	}

	finish[0](false)
	request := cc.AcquireMessage(context.Background())
	request.SetCode(codes.GET)
	request.SetToken(message.Token{0x78})
	_, release, err := cc.protectOutgoingRequest(request)
	cc.ReleaseMessage(request)
	require.NoError(t, err, "failed ancestors must be pruned to make room")
	finish = append(finish, release)

	request = cc.AcquireMessage(context.Background())
	request.SetCode(codes.GET)
	request.SetToken(message.Token{0x78})
	_, release, err = cc.protectOutgoingRequest(request)
	cc.ReleaseMessage(request)
	require.ErrorContains(t, err, "too many pending OSCORE exchanges")
	require.Nil(t, release)

	cc.oscoreMu.Lock()
	record := cc.oscoreExchanges[string(message.Token{0x78})].record
	count := 0
	for ; record != nil; record = record.previous {
		count++
	}
	cc.oscoreMu.Unlock()
	require.Equal(t, maxOSCOREExchangeRecordsPerToken, count)

	for i := len(finish) - 1; i >= 0; i-- {
		finish[i](false)
	}
	cc.oscoreMu.Lock()
	_, exists := cc.oscoreExchanges[string(message.Token{0x78})]
	cc.oscoreMu.Unlock()
	require.False(t, exists)
}

func TestOSCOREMessageTransformsPreserveControlMessage(t *testing.T) {
	masterSecret := []byte("OSCORE control message preservation test")
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: masterSecret,
		SenderID:     []byte("client"),
		RecipientID:  []byte("server"),
	})
	require.NoError(t, err)
	serverContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: masterSecret,
		SenderID:     []byte("server"),
		RecipientID:  []byte("client"),
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts:      []*oscore.Context{serverContext},
		RequireOSCORE: true,
	})
	require.NoError(t, err)
	session := newRecordingSession()
	defer session.Close()
	cfg := DefaultConfig
	cfg.OSCORE = processor
	cc := NewConnWithOpts(session, &cfg)
	defer cc.Close()

	requestOptions, _, err := (message.Options{}).SetObserve(nil, 0)
	require.NoError(t, err)
	protectedRequest, _, err := clientContext.ProtectRequest(message.Message{
		Code:      codes.GET,
		Token:     message.Token{0x51},
		Options:   requestOptions,
		Type:      message.Confirmable,
		MessageID: 51,
	})
	require.NoError(t, err)
	request := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(request)
	request.SetMessage(protectedRequest)
	requestControl := &coapNet.ControlMessage{
		Dst:     net.IPv4(203, 0, 113, 51),
		IfIndex: 5,
	}
	request.SetControlMessage(requestControl)
	wireRequest := pool.NewMessage(context.Background())
	wireRequest.SetMessage(protectedRequest)
	datagram, err := wireRequest.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)

	drop, err := cc.processIncomingOSCORERequest(request, datagram)
	require.NoError(t, err)
	require.False(t, drop)
	require.Same(t, requestControl, request.ControlMessage(), "request decryption must preserve receive metadata")

	responseOptions, _, err := (message.Options{}).SetObserve(nil, 0)
	require.NoError(t, err)
	response := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(response)
	response.SetCode(codes.Content)
	response.SetToken(request.Token())
	response.ResetOptionsTo(responseOptions)
	responseControl := &coapNet.ControlMessage{
		Src:     net.IPv4(203, 0, 113, 52),
		IfIndex: 6,
	}
	response.SetControlMessage(responseControl)
	require.NoError(t, cc.protectResponseForRequest(request, response))
	require.Same(t, responseControl, response.ControlMessage(), "response protection must preserve selected egress metadata")

	notificationOptions, _, err := (message.Options{}).SetObserve(make([]byte, 4), 1)
	require.NoError(t, err)
	notification := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(notification)
	notification.SetCode(codes.Content)
	notification.SetToken(request.Token())
	notification.ResetOptionsTo(notificationOptions)
	notificationControl := &coapNet.ControlMessage{
		Src:     net.IPv4(203, 0, 113, 53),
		IfIndex: 7,
	}
	notification.SetControlMessage(notificationControl)
	require.NoError(t, cc.protectObservationResponse(notification))
	require.Same(t, notificationControl, notification.ControlMessage(), "notification protection must preserve selected egress metadata")
}

func TestRetransmitCachedOSCOREResponse(t *testing.T) {
	const mid = 1234
	masterSecret := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: masterSecret,
		SenderID:     []byte("client"),
		RecipientID:  []byte("server"),
	})
	require.NoError(t, err)
	serverContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: masterSecret,
		SenderID:     []byte("server"),
		RecipientID:  []byte("client"),
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts:      []*oscore.Context{serverContext},
		RequireOSCORE: true,
	})
	require.NoError(t, err)

	outerRequest, clientExchange, err := clientContext.ProtectRequest(message.Message{
		Code:      codes.GET,
		Token:     message.Token{1},
		Type:      message.Confirmable,
		MessageID: mid,
	})
	require.NoError(t, err)
	_, serverExchange, err := serverContext.UnprotectRequest(outerRequest)
	require.NoError(t, err)
	response, err := serverContext.ProtectResponse(message.Message{
		Code:      codes.Content,
		Token:     outerRequest.Token,
		Type:      message.Acknowledgement,
		MessageID: mid,
		Payload:   []byte("cached"),
	}, serverExchange)
	require.NoError(t, err)

	session := newRecordingSession()
	defer session.Close()
	cfg := DefaultConfig
	cfg.OSCORE = processor
	cc := NewConnWithOpts(session, &cfg)
	cachedResponse := cc.AcquireMessage(context.Background())
	cachedResponse.SetMessage(response)
	require.NoError(t, cc.addResponseToCache(mid, cachedResponse))
	cc.ReleaseMessage(cachedResponse)

	retransmittedRequest := cc.AcquireMessage(context.Background())
	retransmittedRequest.SetMessage(outerRequest)
	require.NoError(t, cc.retransmitOSCOREResponse(retransmittedRequest))
	cc.ReleaseMessage(retransmittedRequest)

	got := session.lastWritten()
	require.NotNil(t, got)
	require.Equal(t, codes.Changed, got.Code())
	require.Equal(t, message.Acknowledgement, got.Type())
	require.Equal(t, int32(mid), got.MessageID())
	require.True(t, got.HasOption(message.OSCORE))
	body, err := got.ReadBody()
	require.NoError(t, err)
	require.True(t, bytes.Equal(response.Payload, body), "retransmitted ciphertext differs")

	opened, err := clientContext.UnprotectResponse(message.Message{
		Code:      got.Code(),
		Token:     got.Token(),
		Options:   got.Options(),
		Payload:   body,
		Type:      got.Type(),
		MessageID: got.MessageID(),
	}, clientExchange)
	require.NoError(t, err)
	require.Equal(t, []byte("cached"), opened.Payload)
}

func TestOSCOREObserveNotificationNumberRejectsOutOfOrder(t *testing.T) {
	const masterSecret = "oscore observe replay test secret"
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: []byte(masterSecret),
		SenderID:     []byte("client"),
		RecipientID:  []byte("server"),
	})
	require.NoError(t, err)
	serverContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret:         []byte(masterSecret),
		SenderID:             []byte("server"),
		RecipientID:          []byte("client"),
		SenderSequenceNumber: 10,
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{Contexts: []*oscore.Context{clientContext}})
	require.NoError(t, err)
	observeOptions, _, err := (message.Options{}).SetObserve(nil, 0)
	require.NoError(t, err)
	request, exchange, err := clientContext.ProtectRequest(message.Message{
		Code:    codes.GET,
		Token:   message.Token{1},
		Options: observeOptions,
	})
	require.NoError(t, err)
	_, serverExchange, err := serverContext.UnprotectRequest(request)
	require.NoError(t, err)

	var notifications []message.Message
	for sequence := uint32(1); sequence <= 2; sequence++ {
		options, _, err := (message.Options{}).SetObserve(make([]byte, 4), sequence)
		require.NoError(t, err)
		notification, err := serverContext.ProtectResponseWithPartialIV(message.Message{
			Code:    codes.Content,
			Token:   request.Token,
			Options: options,
		}, serverExchange)
		require.NoError(t, err)
		notifications = append(notifications, notification)
	}

	cc := Conn{
		oscoreProcessor: processor,
		oscoreExchanges: map[string]udpOSCOREExchange{
			string(request.Token): {
				context:  clientContext,
				exchange: exchange,
				observe:  true,
			},
		},
	}
	deliver := func(notification message.Message) error {
		response := &pool.Message{}
		response.SetMessage(notification)
		return cc.unprotectIncomingResponse(response)
	}
	require.NoError(t, deliver(notifications[1]))
	require.ErrorIs(t, deliver(notifications[0]), oscore.ErrReplay)
	require.ErrorIs(t, deliver(notifications[1]), oscore.ErrReplay)
}

func TestOSCOREObserveNotificationPreservesFullPartialIVForOrdering(t *testing.T) {
	const masterSecret = "OSCORE Observe full Partial IV test"
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: []byte(masterSecret),
		SenderID:     []byte("client"),
		RecipientID:  []byte("server"),
	})
	require.NoError(t, err)
	serverContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret:         []byte(masterSecret),
		SenderID:             []byte("server"),
		RecipientID:          []byte("client"),
		SenderSequenceNumber: 0x1000000,
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{Contexts: []*oscore.Context{clientContext}})
	require.NoError(t, err)
	requestOptions, _, err := (message.Options{}).SetObserve(nil, 0)
	require.NoError(t, err)
	request, clientExchange, err := clientContext.ProtectRequest(message.Message{
		Code:    codes.GET,
		Token:   message.Token{0x72},
		Options: requestOptions,
	})
	require.NoError(t, err)
	_, serverExchange, err := serverContext.UnprotectRequest(request)
	require.NoError(t, err)
	protected, err := serverContext.ProtectResponseWithPartialIV(message.Message{
		Code:    codes.Content,
		Token:   request.Token,
		Options: message.Options{{ID: message.Observe}},
	}, serverExchange)
	require.NoError(t, err)

	cc := Conn{
		oscoreProcessor: processor,
		oscoreExchanges: map[string]udpOSCOREExchange{
			string(request.Token): {context: clientContext, exchange: clientExchange, observe: true},
		},
	}
	response := pool.NewMessage(context.Background())
	response.SetMessage(protected)
	require.NoError(t, cc.unprotectIncomingResponse(response))
	sequence, ok := observation.AuthenticatedSequenceNumber(response.Context())
	require.True(t, ok)
	require.Equal(t, uint64(0x1000000), sequence)
	observeValue, err := response.Observe()
	require.NoError(t, err)
	require.Zero(t, observeValue, "the on-wire Observe sequence remains the low 24 bits")
}

func TestOSCOREQBlockRecoveryExchangeSurvivesFinalBlockFirst(t *testing.T) {
	const masterSecret = "OSCORE Q-Block recovery exchange test"
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: []byte(masterSecret),
		SenderID:     []byte("client"),
		RecipientID:  []byte("server"),
	})
	require.NoError(t, err)
	serverContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: []byte(masterSecret),
		SenderID:     []byte("server"),
		RecipientID:  []byte("client"),
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{Contexts: []*oscore.Context{clientContext}})
	require.NoError(t, err)
	session := newRecordingSession()
	defer session.Close()
	cfg := DefaultConfig
	cfg.OSCORE = processor
	cc := NewConnWithOpts(session, &cfg)
	defer cc.Close()

	request := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(request)
	request.SetCode(codes.GET)
	request.SetToken(message.Token{0x71})
	requestOption, err := blockwise.EncodeQBlockOption(blockwise.SZX16, 0, true)
	require.NoError(t, err)
	request.SetOptionUint32(message.QBlock2, requestOption)
	wireRequest, releaseWireRequest, err := cc.protectOutgoingRequest(request)
	require.NoError(t, err)
	defer releaseWireRequest(false)
	cc.oscoreMu.Lock()
	qBlock2Tracked := cc.oscoreExchanges[string(request.Token())].qBlock2
	cc.oscoreMu.Unlock()
	require.True(t, qBlock2Tracked)

	requestPayload, err := wireRequest.ReadBody()
	require.NoError(t, err)
	_, serverExchange, err := serverContext.UnprotectRequest(message.Message{
		Code:    wireRequest.Code(),
		Token:   wireRequest.Token(),
		Options: wireRequest.Options(),
		Payload: requestPayload,
	})
	require.NoError(t, err)
	deliver := func(num int64, more bool) {
		responseOptions := message.Options{}
		option, encodeErr := blockwise.EncodeQBlockOption(blockwise.SZX16, num, more)
		require.NoError(t, encodeErr)
		responseOptions, _, encodeErr = responseOptions.SetUint32(make([]byte, 4), message.QBlock2, option)
		require.NoError(t, encodeErr)
		protected, protectErr := serverContext.ProtectResponseWithPartialIV(message.Message{
			Code:    codes.Content,
			Token:   request.Token(),
			Options: responseOptions,
			Payload: []byte{byte(num)},
		}, serverExchange)
		require.NoError(t, protectErr)
		response := pool.NewMessage(context.Background())
		response.SetMessage(protected)
		require.NoError(t, cc.unprotectIncomingResponse(response))
	}

	deliver(4, false)
	cc.oscoreMu.Lock()
	_, retained := cc.oscoreExchanges[string(request.Token())]
	cc.oscoreMu.Unlock()
	require.True(t, retained, "final Q-Block2 response must not retire the recovery exchange")

	deliver(2, true)
	cc.oscoreMu.Lock()
	_, retained = cc.oscoreExchanges[string(request.Token())]
	cc.oscoreMu.Unlock()
	require.True(t, retained, "recovery exchange remains available for all requested blocks")
}

func TestDuplicateOSCOREConfirmableResponseIsAcknowledged(t *testing.T) {
	const mid = 1234
	masterSecret := []byte("duplicate OSCORE response test secret")
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: masterSecret,
		SenderID:     []byte("client"),
		RecipientID:  []byte("server"),
	})
	require.NoError(t, err)
	serverContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: masterSecret,
		SenderID:     []byte("server"),
		RecipientID:  []byte("client"),
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{Contexts: []*oscore.Context{clientContext}})
	require.NoError(t, err)
	observeOptions, _, err := (message.Options{}).SetObserve(nil, 0)
	require.NoError(t, err)
	outerRequest, exchange, err := clientContext.ProtectRequest(message.Message{
		Code:      codes.GET,
		Token:     message.Token{1},
		Options:   observeOptions,
		Type:      message.Confirmable,
		MessageID: 1,
	})
	require.NoError(t, err)
	_, serverExchange, err := serverContext.UnprotectRequest(outerRequest)
	require.NoError(t, err)
	notificationOptions, _, err := (message.Options{}).SetObserve(make([]byte, 4), 1)
	require.NoError(t, err)
	notification, err := serverContext.ProtectResponseWithPartialIV(message.Message{
		Code:      codes.Content,
		Token:     outerRequest.Token,
		Options:   notificationOptions,
		Type:      message.Confirmable,
		MessageID: mid,
	}, serverExchange)
	require.NoError(t, err)
	wireMessage := pool.NewMessage(context.Background())
	wireMessage.SetMessage(notification)
	datagram, err := wireMessage.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)

	session := newRecordingSession()
	defer session.Close()
	var reportedErrors []error
	cfg := DefaultConfig
	cfg.OSCORE = processor
	cfg.Errors = func(err error) { reportedErrors = append(reportedErrors, err) }
	cc := NewConnWithOpts(session, &cfg)
	cc.oscoreExchanges[string(outerRequest.Token)] = udpOSCOREExchange{
		context:  clientContext,
		exchange: exchange,
		observe:  true,
	}
	ack := cc.AcquireMessage(context.Background())
	ack.SetCode(codes.Empty)
	ack.SetType(message.Acknowledgement)
	ack.SetMessageID(mid)
	require.NoError(t, cc.addResponseToCache(mid, ack))
	cc.ReleaseMessage(ack)

	first := cc.AcquireMessage(context.Background())
	first.SetMessage(notification)
	require.False(t, cc.processIncomingOSCOREMessage(first, datagram),
		"the first protected notification must continue to normal processing")
	cc.ReleaseMessage(first)

	duplicate := cc.AcquireMessage(context.Background())
	duplicate.SetMessage(notification)
	localAddress := net.ParseIP("203.0.113.12").To4()
	duplicate.SetControlMessage(&coapNet.ControlMessage{Dst: localAddress, IfIndex: 6})
	require.True(t, cc.processIncomingOSCOREMessage(duplicate, datagram),
		"an exact retransmission must be handled before application delivery")
	require.Equal(t, 1, session.writtenCount())
	written := session.lastWritten()
	require.NotNil(t, written)
	require.Equal(t, codes.Empty, written.Code())
	require.Equal(t, message.Acknowledgement, written.Type())
	require.Equal(t, int32(mid), written.MessageID())
	controlMessage := written.ControlMessage()
	require.NotNil(t, controlMessage)
	require.True(t, controlMessage.Src.Equal(localAddress))
	require.Equal(t, 6, controlMessage.IfIndex)
	cc.ReleaseMessage(duplicate)

	changedOptions, _, err := (message.Options{}).SetObserve(make([]byte, 4), 2)
	require.NoError(t, err)
	changedNotification, err := serverContext.ProtectResponseWithPartialIV(message.Message{
		Code:      codes.Content,
		Token:     outerRequest.Token,
		Options:   changedOptions,
		Type:      message.Confirmable,
		MessageID: mid,
		Payload:   []byte("different PIV"),
	}, serverExchange)
	require.NoError(t, err)
	changedWireMessage := pool.NewMessage(context.Background())
	changedWireMessage.SetMessage(changedNotification)
	changedDatagram, err := changedWireMessage.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)

	conflictingResponse := cc.AcquireMessage(context.Background())
	conflictingResponse.SetMessage(changedNotification)
	require.True(t, cc.processIncomingOSCOREMessage(conflictingResponse, changedDatagram),
		"reuse of an active Message ID with a different response must not reach the application")
	require.Len(t, reportedErrors, 1)
	require.ErrorIs(t, reportedErrors[0], oscore.ErrInvalidOSCOREMessage)
	require.Equal(t, 2, session.writtenCount(), "the cached ACK must also be retransmitted")
	cc.ReleaseMessage(conflictingResponse)
	require.NoError(t, cc.Close())
}

func TestProtectedRequestCachesProtectedResponseForRetransmission(t *testing.T) {
	const mid = 2345
	masterSecret := []byte("protected response cache test secret")
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: masterSecret,
		SenderID:     []byte("client"),
		RecipientID:  []byte("server"),
	})
	require.NoError(t, err)
	serverContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: masterSecret,
		SenderID:     []byte("server"),
		RecipientID:  []byte("client"),
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts:      []*oscore.Context{serverContext},
		RequireOSCORE: true,
	})
	require.NoError(t, err)
	const secret = "sensitive protected response"
	var handlerCalls atomic.Int32
	session := newRecordingSession()
	defer session.Close()
	cfg := DefaultConfig
	cfg.OSCORE = processor
	cfg.Errors = func(err error) { require.NoError(t, err) }
	cfg.Handler = func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		handlerCalls.Inc()
		require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte(secret))))
	}
	cc := NewConnWithOpts(session, &cfg)
	defer cc.Close()

	protectedRequest, exchange, err := clientContext.ProtectRequest(message.Message{
		Code:      codes.GET,
		Token:     message.Token{0x42},
		Type:      message.Confirmable,
		MessageID: mid,
	})
	require.NoError(t, err)
	wireRequest := pool.NewMessage(context.Background())
	wireRequest.SetMessage(protectedRequest)
	datagram, err := wireRequest.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	require.NoError(t, cc.Process(nil, datagram))
	require.Eventually(t, func() bool { return session.writtenCount() == 1 }, time.Second, time.Millisecond)

	cachedResponse := cc.AcquireMessage(context.Background())
	ok, err := cc.getResponseFromCache(mid, cachedResponse)
	require.NoError(t, err)
	require.True(t, ok, "the protected response should be cached")
	require.True(t, cachedResponse.HasOption(message.OSCORE), "cache must contain the protected response")
	cachedBody, err := cachedResponse.ReadBody()
	require.NoError(t, err)
	require.NotEqual(t, []byte(secret), cachedBody, "plaintext response must never enter the retransmission cache")

	firstResponse := session.lastWritten()
	require.NotNil(t, firstResponse)
	require.True(t, firstResponse.HasOption(message.OSCORE))
	firstBody, err := firstResponse.ReadBody()
	require.NoError(t, err)
	opened, err := clientContext.UnprotectResponse(message.Message{
		Code:      firstResponse.Code(),
		Token:     firstResponse.Token(),
		Options:   firstResponse.Options(),
		Payload:   firstBody,
		Type:      firstResponse.Type(),
		MessageID: firstResponse.MessageID(),
	}, exchange)
	require.NoError(t, err)
	require.Equal(t, []byte(secret), opened.Payload)

	require.NoError(t, cc.Process(nil, datagram))
	require.Eventually(t, func() bool { return session.writtenCount() == 2 }, time.Second, time.Millisecond)
	require.EqualValues(t, 1, handlerCalls.Load(), "retransmitted request must not invoke the handler again")
	retransmittedResponse := session.lastWritten()
	require.NotNil(t, retransmittedResponse)
	require.True(t, retransmittedResponse.HasOption(message.OSCORE))
	retransmittedBody, err := retransmittedResponse.ReadBody()
	require.NoError(t, err)
	require.Equal(t, firstBody, retransmittedBody, "retransmission must return the original ciphertext")
	cc.ReleaseMessage(cachedResponse)
}

func TestProtectedRequestCachesEmptyAcknowledgementForRetransmission(t *testing.T) {
	const mid = 3456
	masterSecret := []byte("protected empty ACK cache test secret")
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: masterSecret,
		SenderID:     []byte("client"),
		RecipientID:  []byte("server"),
	})
	require.NoError(t, err)
	serverContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: masterSecret,
		SenderID:     []byte("server"),
		RecipientID:  []byte("client"),
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts:      []*oscore.Context{serverContext},
		RequireOSCORE: true,
	})
	require.NoError(t, err)

	var handlerCalls atomic.Int32
	session := newRecordingSession()
	defer session.Close()
	cfg := DefaultConfig
	cfg.OSCORE = processor
	cfg.Errors = func(err error) { require.NoError(t, err) }
	cfg.Handler = func(_ *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		handlerCalls.Inc()
	}
	cc := NewConnWithOpts(session, &cfg)
	defer cc.Close()

	protectedRequest, _, err := clientContext.ProtectRequest(message.Message{
		Code:      codes.GET,
		Token:     message.Token{0x43},
		Type:      message.Confirmable,
		MessageID: mid,
	})
	require.NoError(t, err)
	wireRequest := pool.NewMessage(context.Background())
	wireRequest.SetMessage(protectedRequest)
	datagram, err := wireRequest.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)

	require.NoError(t, cc.Process(nil, datagram))
	require.Eventually(t, func() bool { return session.writtenCount() == 1 }, time.Second, time.Millisecond)
	firstAck := session.lastWritten()
	require.NotNil(t, firstAck)
	require.Equal(t, codes.Empty, firstAck.Code())
	require.Equal(t, message.Acknowledgement, firstAck.Type())

	require.NoError(t, cc.Process(nil, datagram))
	require.Eventually(t, func() bool { return session.writtenCount() == 2 }, time.Second, time.Millisecond)
	retransmittedAck := session.lastWritten()
	require.NotNil(t, retransmittedAck)
	require.Equal(t, codes.Empty, retransmittedAck.Code())
	require.Equal(t, message.Acknowledgement, retransmittedAck.Type())
	require.Equal(t, int32(mid), retransmittedAck.MessageID())
	require.EqualValues(t, 1, handlerCalls.Load(), "duplicate protected request must not invoke the handler again")
}
