package blockwise

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/pkg/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBlockWiseContinueSendingMessageConcurrent(t *testing.T) {
	for _, code := range []codes.Code{codes.Content, codes.PUT} {
		t.Run(code.String(), func(t *testing.T) {
			testContinueSendingMessageConcurrent(t, code)
		})
	}
}

func testContinueSendingMessageConcurrent(t *testing.T, code codes.Code) {
	t.Helper()
	cli := newTestClient()
	b := New(cli, time.Hour, func(error) {}, nil)
	token := message.Token{1, 2, 3, 4}
	payload := make([]byte, 8192)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	sending := cli.AcquireMessage(context.Background())
	defer cli.ReleaseMessage(sending)
	sending.SetCode(code)
	sending.SetToken(token)
	sending.SetBody(bytes.NewReader(payload))
	b.sendingMessagesCache.LoadOrStore(token.Hash(), cache.NewElement(newSendingMessage(sending), time.Now().Add(time.Hour), nil))

	errs := make(chan error, 4)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				blockCount := 512
				if code == codes.PUT {
					blockCount--
				}
				num := int64((i*200 + j) % blockCount)
				resp, more, err := continueSendingBlock(b, token, code, num)
				if err != nil {
					errs <- err
					return
				}
				offset := num * SZX16.Size()
				blockType, sizeType := message.Block2, message.Size2
				if code == codes.PUT {
					offset += SZX16.Size()
					blockType, sizeType = message.Block1, message.Size1
				}
				data, readErr := resp.ReadBody()
				assert.NoError(t, readErr)
				assert.Equal(t, payload[offset:offset+SZX16.Size()], data)
				assert.Equal(t, offset+SZX16.Size() < int64(len(payload)), more)
				assert.Equal(t, code, resp.Code())
				assert.Equal(t, token, resp.Token())
				block, optionErr := resp.GetOptionUint32(blockType)
				assert.NoError(t, optionErr)
				szx, blockNum, blockMore, decodeErr := DecodeBlockOption(block)
				assert.NoError(t, decodeErr)
				assert.Equal(t, SZX16, szx)
				assert.Equal(t, offset/SZX16.Size(), blockNum)
				assert.Equal(t, more, blockMore)
				size, sizeErr := resp.GetOptionUint32(sizeType)
				assert.NoError(t, sizeErr)
				assert.Equal(t, uint32(len(payload)), size)
				cli.ReleaseMessage(resp)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

func continueSendingBlock(b *BlockWise[*testClient], token message.Token, code codes.Code, num int64) (*pool.Message, bool, error) {
	block, err := EncodeBlockOption(SZX16, num, false)
	if err != nil {
		return nil, false, err
	}
	r := b.cc.AcquireMessage(context.Background())
	defer b.cc.ReleaseMessage(r)
	r.SetCode(codes.GET)
	r.SetToken(token)
	blockType := message.Block2
	if code == codes.POST || code == codes.PUT {
		blockType = message.Block1
	}
	r.SetOptionUint32(blockType, block)
	w := responsewriter.New(b.cc.AcquireMessage(context.Background()), b.cc)
	more, err := b.continueSendingMessage(w, r, SZX16, 1152, code)
	if err != nil {
		b.cc.ReleaseMessage(w.Message())
		return nil, false, err
	}
	return w.Message(), more, nil
}

type blockingReadSeeker struct {
	io.ReadSeeker
	started   chan struct{}
	unblock   chan struct{}
	once      sync.Once
	skipReads int
}

func (r *blockingReadSeeker) Read(p []byte) (int, error) {
	if r.skipReads > 0 {
		r.skipReads--
		return r.ReadSeeker.Read(p)
	}
	r.once.Do(func() {
		close(r.started)
		<-r.unblock
	})
	return r.ReadSeeker.Read(p)
}

func TestBlockWiseContinueSendingMessageIndependentTokens(t *testing.T) {
	cli := newTestClient()
	b := New(cli, time.Hour, func(error) {}, nil)
	tokenA, tokenB := message.Token{1}, message.Token{2}
	body := &blockingReadSeeker{
		ReadSeeker: bytes.NewReader(make([]byte, 64)),
		started:    make(chan struct{}),
		unblock:    make(chan struct{}),
	}
	unblock := sync.OnceFunc(func() { close(body.unblock) })
	var wg sync.WaitGroup

	sending := cli.AcquireMessage(context.Background())
	defer func() {
		unblock()
		wg.Wait()
		cli.ReleaseMessage(sending)
	}()
	sending.SetCode(codes.Content)
	sending.SetToken(tokenA)
	sending.SetBody(body)
	b.sendingMessagesCache.LoadOrStore(tokenA.Hash(), cache.NewElement(newSendingMessage(sending), time.Now().Add(time.Hour), nil))

	doneA := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, _, err := continueSendingBlock(b, tokenA, codes.Content, 1)
		if resp != nil {
			cli.ReleaseMessage(resp)
		}
		doneA <- err
	}()
	select {
	case <-body.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first token did not reach body.Read")
	}

	doneB := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		other := cli.AcquireMessage(context.Background())
		defer cli.ReleaseMessage(other)
		other.SetCode(codes.Content)
		other.SetToken(tokenB)
		other.SetBody(bytes.NewReader(bytes.Repeat([]byte{42}, 64)))
		// Inserting a token must also remain possible while another body blocks.
		b.sendingMessagesCache.LoadOrStore(tokenB.Hash(), cache.NewElement(newSendingMessage(other), time.Now().Add(time.Hour), nil))
		resp, _, err := continueSendingBlock(b, tokenB, codes.Content, 1)
		if err != nil {
			doneB <- err
			return
		}
		defer cli.ReleaseMessage(resp)
		data, err := resp.ReadBody()
		if err == nil && !bytes.Equal(data, bytes.Repeat([]byte{42}, 16)) {
			err = fmt.Errorf("unexpected second-token payload: %v", data)
		}
		doneB <- err
	}()
	select {
	case err := <-doneB:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("second token blocked behind the first token's body")
	}
	unblock()
	require.NoError(t, <-doneA)
}

func TestBlockWiseDoWaitsForSendingMessage(t *testing.T) {
	cli := newTestClient()
	b := New(cli, time.Hour, func(error) {}, nil)
	token := message.Token{1}
	body := &blockingReadSeeker{
		ReadSeeker: bytes.NewReader(make([]byte, 64)),
		started:    make(chan struct{}),
		unblock:    make(chan struct{}),
		skipReads:  1,
	}
	unblock := sync.OnceFunc(func() { close(body.unblock) })
	callbackEntered := make(chan struct{})
	returnDo := make(chan struct{})
	finishDo := sync.OnceFunc(func() { close(returnDo) })
	var wg sync.WaitGroup
	request := cli.AcquireMessage(context.Background())
	defer func() {
		finishDo()
		unblock()
		wg.Wait()
		cli.ReleaseMessage(request)
	}()
	request.SetCode(codes.PUT)
	request.SetToken(token)
	request.SetBody(body)

	doDone := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := b.Do(request, SZX16, 1152, func(*pool.Message) (*pool.Message, error) {
			close(callbackEntered)
			<-returnDo
			return request, nil
		})
		doDone <- err
	}()
	select {
	case <-callbackEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("Do did not finish preparing the request")
	}
	entry := b.sendingMessagesCache.Load(token.Hash())
	require.NotNil(t, entry)

	continueDone := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, _, err := continueSendingBlock(b, token, codes.PUT, 0)
		if resp != nil {
			cli.ReleaseMessage(resp)
		}
		continueDone <- err
	}()
	select {
	case <-body.started:
	case <-time.After(5 * time.Second):
		t.Fatal("continuation did not reach body.Read")
	}
	finishDo()
	select {
	case err := <-doDone:
		t.Fatalf("Do returned while its request body was still being read: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	unblock()
	require.NoError(t, <-continueDone)
	require.NoError(t, <-doDone)
	require.Nil(t, b.sendingMessagesCache.Load(token.Hash()))

	// Simulate a continuation that loaded the entry before Do removed it.
	b.sendingMessagesCache.LoadOrStore(token.Hash(), entry)
	resp, _, err := continueSendingBlock(b, token, codes.PUT, 0)
	require.ErrorContains(t, err, "cannot find sending message")
	require.Nil(t, resp)
	require.Nil(t, b.getSentRequest(token))
	_, exists := b.getSendingMessageCode(token.Hash())
	require.False(t, exists)
}
