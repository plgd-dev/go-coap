package qblock

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/stretchr/testify/require"
)

type copyRequestBodySeeker struct {
	reader        *bytes.Reader
	failSeekCall  int
	seekErr       error
	readErr       error
	seekCallCount int
}

func (s *copyRequestBodySeeker) Read(p []byte) (int, error) {
	if s.readErr != nil {
		return 0, s.readErr
	}
	return s.reader.Read(p)
}

func (s *copyRequestBodySeeker) Seek(offset int64, whence int) (int64, error) {
	s.seekCallCount++
	if s.seekCallCount == s.failSeekCall {
		return 0, s.seekErr
	}
	return s.reader.Seek(offset, whence)
}

type budgetedQBlockClient struct {
	*testClient
	baseSize          uint64
	perOptionOverhead uint64
	limit             uint64
	validations       int
}

func (c *budgetedQBlockClient) QBlockDatagramBudget(*pool.Message) (uint64, uint64, uint64, error) {
	return c.baseSize, c.perOptionOverhead, c.limit, nil
}

func (c *budgetedQBlockClient) ValidateQBlockDatagram(*pool.Message) error {
	c.validations++
	return nil
}

func TestBuildRequestPartUsesLargestFittingOptionPrefix(t *testing.T) {
	client := &budgetedQBlockClient{
		testClient:        newTestClient(),
		baseSize:          10,
		perOptionOverhead: 1,
		limit:             17,
	}
	engine := New(client, nil)
	request := client.AcquireMessage(context.Background())
	defer client.ReleaseMessage(request)
	request.SetCode(codes.GET)

	receiver := q2BodyReceiver[*budgetedQBlockClient]{
		b: engine, req: request, szx: SZX16,
	}
	part, added, err := receiver.buildRequestPart([]int64{0, 1, 2}, false, false)
	require.NoError(t, err)
	defer client.ReleaseMessage(part)

	require.Equal(t, 2, added)
	count := 0
	for _, option := range part.Options() {
		if option.ID == message.QBlock2 {
			count++
		}
	}
	require.Equal(t, 2, count)
	require.Equal(t, 1, client.validations, "validate only the selected final request")
}

func TestQBlock2RecoveryRequestUsesAliasAndOmitsObserve(t *testing.T) {
	client := newTestClient()
	engine := New(client, nil)
	request := client.AcquireMessage(context.Background())
	defer client.ReleaseMessage(request)
	request.SetCode(codes.GET)
	request.SetToken(message.Token("observe"))
	request.SetObserve(0)
	require.NoError(t, request.SetRequestTag([]byte("tag")))
	exchange := &qObserveExchange{}
	receiver := q2BodyReceiver[*testClient]{
		b: engine, req: request, exchange: exchange, szx: SZX16, maxSZX: SZX16,
	}

	part, _, err := receiver.buildRequestPart([]int64{0}, false, true)
	require.NoError(t, err)
	defer client.ReleaseMessage(part)
	require.False(t, part.HasOption(message.Observe))
	require.Equal(t, message.Token("recovery"), part.Token())
}

func TestQBlock2RegistrationRequestPreservesObserveAndToken(t *testing.T) {
	client := newTestClient()
	engine := New(client, nil)
	request := client.AcquireMessage(context.Background())
	defer client.ReleaseMessage(request)
	request.SetCode(codes.GET)
	request.SetToken(message.Token("observe"))
	request.SetObserve(0)
	require.NoError(t, request.SetRequestTag([]byte("tag")))
	receiver := q2BodyReceiver[*testClient]{
		b: engine, req: request, exchange: &qObserveExchange{}, szx: SZX16, maxSZX: SZX16,
	}

	part, _, err := receiver.buildRequestPart([]int64{0}, true, false)
	require.NoError(t, err)
	defer client.ReleaseMessage(part)
	require.True(t, part.HasOption(message.Observe))
	require.Equal(t, message.Token("observe"), part.Token())
}

func TestQBlock2FetchRecoveryPreservesSelectorBody(t *testing.T) {
	client := newTestClient()
	engine := New(client, nil)
	request := client.AcquireMessage(context.Background())
	defer client.ReleaseMessage(request)
	request.SetCode(codes.FETCH)
	request.SetToken(message.Token("observe"))
	request.SetBody(bytes.NewReader([]byte("selector")))
	receiver := q2BodyReceiver[*testClient]{
		b: engine, req: request, exchange: &qObserveExchange{}, szx: SZX16, maxSZX: SZX16,
	}

	part, _, err := receiver.buildRequestPart([]int64{0}, true, true)
	require.NoError(t, err)
	defer client.ReleaseMessage(part)
	body, err := part.ReadBody()
	require.NoError(t, err)
	require.Equal(t, []byte("selector"), body)
}

func TestCopyRequestBodyErrorsAndEmptyBody(t *testing.T) {
	seekErr := errors.New("seek failed")
	readErr := errors.New("read failed")
	for _, test := range []struct {
		name          string
		body          io.ReadSeeker
		wantErr       error
		wantNilCopied bool
	}{
		{name: "nil body", wantNilCopied: true},
		{
			name:    "position seek fails",
			body:    &copyRequestBodySeeker{reader: bytes.NewReader([]byte("body")), failSeekCall: 1, seekErr: seekErr},
			wantErr: seekErr,
		},
		{
			name:    "rewind seek fails",
			body:    &copyRequestBodySeeker{reader: bytes.NewReader([]byte("body")), failSeekCall: 2, seekErr: seekErr},
			wantErr: seekErr,
		},
		{
			name:    "read fails",
			body:    &copyRequestBodySeeker{reader: bytes.NewReader([]byte("body")), readErr: readErr},
			wantErr: readErr,
		},
		{
			name:    "restore seek fails",
			body:    &copyRequestBodySeeker{reader: bytes.NewReader([]byte("body")), failSeekCall: 3, seekErr: seekErr},
			wantErr: seekErr,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := pool.NewMessage(context.Background())
			destination := pool.NewMessage(context.Background())
			if test.body != nil {
				source.SetBody(test.body)
			}

			err := copyRequestBody(source, destination)
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
				return
			}

			require.NoError(t, err)
			if test.wantNilCopied {
				require.Nil(t, destination.Body())
				return
			}
		})
	}
}

func TestQBlockOptionSizeIncrementIncludesFollowingOptions(t *testing.T) {
	request := pool.NewMessage(context.Background())
	request.SetCode(codes.GET)
	request.SetOptionBytes(message.URIHost, []byte("device"))
	request.SetOptionBytes(message.ProxyURI, []byte("coap://proxy/path"))

	baseWireSize, err := encodedOptionsSize(request.Options())
	require.NoError(t, err)
	tailAdjustment, previousID, err := qBlockOptionTailAdjustment(request.Options())
	require.NoError(t, err)

	value, err := EncodeQBlockOption(SZX16, 0, false)
	require.NoError(t, err)
	option, err := newQBlockMessageOption(value)
	require.NoError(t, err)
	optionSize, err := encodedOptionSize(option, previousID)
	require.NoError(t, err)
	request.AddOptionUint32(message.QBlock2, value)
	actualWireSize, err := encodedOptionsSize(request.Options())
	require.NoError(t, err)

	require.Equal(t, int64(actualWireSize-baseWireSize), int64(optionSize)+tailAdjustment)
}

func encodedOptionsSize(options message.Options) (int, error) {
	size, err := options.Marshal(nil)
	if err != nil && !errors.Is(err, message.ErrTooSmall) {
		return 0, err
	}
	return size, nil
}
