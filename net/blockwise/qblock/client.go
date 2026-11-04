package qblock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
)

type q1BodySender[C Client] struct {
	b               *Engine[C]
	request         *pool.Message
	sender          *Sender
	exchange        Exchange
	tag             []byte
	size            uint32
	szx             SZX
	blockCount      int64
	attempts        map[int64]int
	sentRequests    int
	next            int64
	retries         int
	pendingRecovery []int64
	delay           time.Duration
	deadline        time.Time
}

// DoQBlock1 sends payload sets without waiting for individual NON responses.
// CON payloads retain the transport's normal ACK and retransmission machinery.
func (b *Engine[C]) DoQBlock1(r *pool.Message, szx SZX, maxBodySize int64,
	_ func(*pool.Message) (*pool.Message, error),
) (*pool.Message, error) {
	if !qBodyMethod(r.Code()) || r.Body() == nil {
		return nil, errors.New("Q-Block1 requires a request body")
	}
	size, err := r.BodySize()
	if err != nil {
		return nil, fmt.Errorf("cannot get size of payload: %w", err)
	}
	if size < 0 || size > maxBodySize || size > int64(^uint32(0)) {
		return nil, ErrQBlockTooLarge
	}
	sender, err := NewSender(r.Body(), szx, size)
	if err != nil {
		return nil, err
	}
	transport, ok := any(b.cc).(Transport)
	if !ok {
		return nil, errors.New("Q-Block burst transport is unavailable")
	}
	exchange, err := transport.OpenQBlockExchange(r.Context(), r.Token())
	if err != nil {
		return nil, err
	}
	defer exchange.Close()
	tag, tagErr := r.RequestTag()
	if tagErr != nil {
		tag, err = message.GetToken()
		if err != nil {
			return nil, err
		}
	}
	s := &q1BodySender[C]{
		b: b, request: r, sender: sender, exchange: exchange, tag: tag,
		size: uint32(size), szx: szx, blockCount: max(int64(1), (size+szx.Size()-1)/szx.Size()),
		attempts: make(map[int64]int), delay: b.config.nonDelay(),
	}
	if r.Type() == message.Confirmable {
		s.delay = 0
	}
	return s.run()
}

func (s *q1BodySender[C]) sendBlock(num int64) error {
	if num < 0 || num >= s.blockCount || s.attempts[num] > s.b.config.NonMaxRetransmit {
		return fmt.Errorf("Q-Block1 invalid or exhausted recovery NUM %d", num)
	}
	s.attempts[num]++
	payload, more, err := s.sender.Block(num)
	if err != nil {
		return err
	}
	value, _ := EncodeQBlockOption(s.szx, num, more)
	part := s.b.cloneMessage(s.request)
	defer s.b.cc.ReleaseMessage(part)
	if s.sentRequests > 0 {
		token, tokenErr := s.exchange.NewToken()
		if tokenErr != nil {
			return tokenErr
		}
		part.SetToken(token)
	}
	s.sentRequests++
	part.Remove(message.Block1)
	part.Remove(message.Block2)
	part.SetOptionUint32(message.QBlock1, value)
	q2, _ := EncodeQBlockOption(s.szx, 0, true)
	part.SetOptionUint32(message.QBlock2, q2)
	part.SetOptionUint32(message.Size1, s.size)
	if tagErr := part.SetRequestTag(s.tag); tagErr != nil {
		return tagErr
	}
	part.SetBody(bytes.NewReader(payload))
	return s.exchange.Send(part)
}

func (s *q1BodySender[C]) send(nums []int64) error {
	for _, num := range nums {
		if err := s.sendBlock(num); err != nil {
			return err
		}
	}
	return nil
}

func (s *q1BodySender[C]) sendSet() error {
	last := s.b.config.setLast(s.next, s.blockCount)
	nums := make([]int64, 0, s.b.config.MaxPayloads)
	for num := s.next; num <= last; num++ {
		nums = append(nums, num)
	}
	s.next = last + 1
	return s.send(nums)
}

func (s *q1BodySender[C]) timeout(receiveErr error) error {
	if s.request.Context().Err() != nil {
		return s.request.Context().Err()
	}
	switch {
	case len(s.pendingRecovery) > 0:
		s.retries++
		return s.send(s.pendingRecovery)
	case s.next < s.blockCount:
		return s.sendSet()
	case s.retries >= s.b.config.NonMaxRetransmit:
		return fmt.Errorf("Q-Block1 final response recovery exhausted: %w", receiveErr)
	default:
		s.retries++
		return s.send([]int64{s.blockCount - 1})
	}
}

func (s *q1BodySender[C]) continuation(response *pool.Message) (bool, error) {
	num, valid := q1ContinueNUM(response)
	s.b.cc.ReleaseMessage(response)
	if !valid {
		return false, nil
	}
	wasRecovering := len(s.pendingRecovery) > 0
	if wasRecovering && num >= s.pendingRecovery[len(s.pendingRecovery)-1] {
		s.pendingRecovery = nil
		s.retries = 0
	}
	if wasRecovering && len(s.pendingRecovery) == 0 && s.next < s.blockCount {
		s.deadline = time.Now().Add(s.delay)
		return false, nil
	}
	if num != s.next-1 || s.next >= s.blockCount {
		return false, nil
	}
	return true, s.sendSet()
}

func q1ContinueNUM(response *pool.Message) (int64, bool) {
	value, err := response.GetOptionUint32(message.QBlock1)
	if err != nil {
		return 0, false
	}
	_, num, _, decodeErr := DecodeQBlockOption(value)
	return num, decodeErr == nil
}

func (s *q1BodySender[C]) missing(response *pool.Message) error {
	defer s.b.cc.ReleaseMessage(response)
	format, formatErr := response.ContentFormat()
	report, readErr := readQBody(response)
	if formatErr != nil || readErr != nil || format != message.AppMissingBlocksCBORSeq {
		return errors.New("invalid Q-Block1 missing-block report")
	}
	missing, err := DecodeMissingBlocks(nil, report)
	if err != nil || len(missing) > q1MaxTrackedBlocks {
		return errors.New("invalid Q-Block1 missing-block sequence")
	}
	s.pendingRecovery = qMissingSet(missing, s.b.config.MaxPayloads)
	return s.send(s.pendingRecovery)
}

func (s *q1BodySender[C]) final(response *pool.Message) (*pool.Message, error) {
	if response.Code() == codes.BadOption {
		s.b.cc.ReleaseMessage(response)
		return nil, ErrQBlockUnsupported
	}
	if !response.HasOption(message.QBlock2) {
		response.SetToken(s.request.Token())
		return response, nil
	}
	request := s.b.cloneMessage(s.request)
	defer s.b.cc.ReleaseMessage(request)
	request.SetOptionUint32(message.Size1, s.size)
	if err := request.SetRequestTag(s.tag); err != nil {
		s.b.cc.ReleaseMessage(response)
		return nil, err
	}
	return s.b.ReceiveQBlock2(request, s.szx, s.exchange, response)
}

func (s *q1BodySender[C]) receive() (*pool.Message, error) {
	ctx, cancel := context.WithDeadline(s.request.Context(), s.deadline)
	defer cancel()
	return s.exchange.Receive(ctx)
}

func (s *q1BodySender[C]) run() (*pool.Message, error) {
	if err := s.sendSet(); err != nil {
		return nil, err
	}
	s.deadline = time.Now().Add(s.delay)
	if s.next == s.blockCount {
		s.deadline = time.Now().Add(s.b.config.NonReceiveTimeout + time.Second)
	}
	for {
		response, receiveErr := s.receive()
		switch {
		case receiveErr != nil:
			if err := s.timeout(receiveErr); err != nil {
				return nil, err
			}
		default:
			switch response.Code() {
			case codes.Continue:
				advance, err := s.continuation(response)
				if err != nil {
					return nil, err
				}
				if !advance {
					continue
				}
			case codes.RequestEntityIncomplete:
				if err := s.missing(response); err != nil {
					return nil, err
				}
			default:
				return s.final(response)
			}
		}
		wait := s.delay
		if s.next == s.blockCount || len(s.pendingRecovery) > 0 {
			wait = (s.b.config.NonReceiveTimeout + time.Second) << s.retries
		}
		s.deadline = time.Now().Add(wait)
	}
}

func readQBody(r *pool.Message) ([]byte, error) {
	if r.Body() == nil {
		return nil, nil
	}
	body := r.Body()
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(body)
	_, seekErr := body.Seek(0, io.SeekStart)
	if err != nil {
		return nil, err
	}
	return data, seekErr
}
