package qblock

import (
	"context"
	"errors"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/stretchr/testify/require"
)

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
	part, added, err := receiver.buildRequestPart([]int64{0, 1, 2}, false)
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
