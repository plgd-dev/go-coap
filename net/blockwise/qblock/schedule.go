package qblock

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise/internal/blocks"
)

type q2SetJob[C Client] struct {
	b                   *Engine[C]
	transport           Transport
	key                 string
	token               message.Token
	szx                 SZX
	mode                message.Type
	nums                []int64
	count               int64
	wholeBody, recovery bool
	generation          *q2SendTransfer
}

func q2JobKey(key string, token message.Token, nums []int64, recovery bool) string {
	jobKey := "q2:" + key
	if !recovery {
		return jobKey
	}
	var recoveryKey bytes.Buffer
	blocks.WriteExchangeKeyPart(&recoveryKey, token)
	for _, num := range nums {
		var encoded [4]byte
		binary.BigEndian.PutUint32(encoded[:], uint32(num)) // #nosec G115 -- requested NUMs were validated as 20-bit values.
		recoveryKey.Write(encoded[:])
	}
	return jobKey + ":recovery:" + recoveryKey.String()
}

func (b *Engine[C]) scheduleQ2Set(ctx context.Context, job *q2SetJob[C], deadline time.Time) error {
	b.q2SendMu.Lock()
	defer b.q2SendMu.Unlock()
	generation := b.q2Senders[job.key]
	if generation == nil || job.generation != nil && generation != job.generation {
		return nil
	}
	job.b, job.generation = b, generation
	job.token = bytes.Clone(job.token)
	return job.transport.ScheduleQBlock(q2JobKey(job.key, job.token, job.nums, job.recovery), deadline,
		func() error { return job.run(ctx) })
}

func (s *q2SetJob[C]) current() bool {
	s.b.q2SendMu.Lock()
	defer s.b.q2SendMu.Unlock()
	return s.b.q2Senders[s.key] == s.generation
}

func (s *q2SetJob[C]) reschedule(ctx context.Context, nums []int64, deadline time.Time) error {
	next := *s
	next.nums = nums
	return s.b.scheduleQ2Set(ctx, &next, deadline)
}

func (s *q2SetJob[C]) recoveryDeadline() time.Time {
	if !s.wholeBody || s.mode != message.NonConfirmable {
		return time.Time{}
	}
	s.b.q2SendMu.Lock()
	defer s.b.q2SendMu.Unlock()
	if s.b.q2Senders[s.key] == s.generation {
		return s.generation.recoveryUntil
	}
	return time.Time{}
}

func (s *q2SetJob[C]) response(ctx context.Context, num int64) (*pool.Message, error) {
	response, err := s.b.buildQBlock2ResponseFrom(ctx, s.key, s.token, s.szx, num, s.generation)
	if err != nil {
		return nil, err
	}
	response.SetType(s.mode)
	if s.recovery {
		response.Remove(message.Observe)
	}
	if s.wholeBody {
		s.b.q2SendMu.Lock()
		if s.b.q2Senders[s.key] == s.generation {
			s.generation.sentThrough = max(s.generation.sentThrough, num)
		}
		s.b.q2SendMu.Unlock()
	}
	return response, nil
}

func (s *q2SetJob[C]) send(ctx context.Context, num int64) error {
	response, err := s.response(ctx, num)
	if err != nil {
		return err
	}
	defer s.b.cc.ReleaseMessage(response)
	return s.transport.SendQBlock(response)
}

func (s *q2SetJob[C]) sendAsync(ctx context.Context, transport AsyncSender) error {
	response, err := s.response(ctx, s.nums[0])
	if err != nil {
		if errors.Is(err, errQBlock2ResponseNotFound) {
			return nil
		}
		return err
	}
	defer s.b.cc.ReleaseMessage(response)
	return transport.SendQBlockAsync(response, func(sendErr error) {
		if !s.current() || ctx.Err() != nil {
			return
		}
		if sendErr == nil {
			sendErr = s.following(ctx, 1)
		}
		if sendErr != nil {
			s.b.errors(sendErr)
		}
	})
}

func (s *q2SetJob[C]) pauseNormalUntil(deadline time.Time) {
	s.b.q2SendMu.Lock()
	if s.b.q2Senders[s.key] == s.generation && deadline.After(s.generation.recoveryUntil) {
		s.generation.recoveryUntil = deadline
	}
	s.b.q2SendMu.Unlock()
	s.transport.DelayQBlock("q2:"+s.key, deadline)
}

func (s *q2SetJob[C]) following(ctx context.Context, end int) error {
	delay := time.Duration(0)
	if s.mode == message.NonConfirmable {
		delay = s.b.q2SetDelay(s.key)
	}
	if end < len(s.nums) {
		deadline := time.Now().Add(delay)
		if s.recovery && s.mode == message.NonConfirmable {
			s.pauseNormalUntil(deadline.Add(delay))
		}
		return s.reschedule(ctx, s.nums[end:], deadline)
	}
	if !s.wholeBody || len(s.nums) == 0 || s.nums[len(s.nums)-1]+1 >= s.count {
		return nil
	}
	next := s.nums[len(s.nums)-1] + 1
	var following []int64
	for num := next; num <= s.b.config.setLast(next, s.count); num++ {
		following = append(following, num)
	}
	return s.reschedule(ctx, following, time.Now().Add(delay))
}

func (s *q2SetJob[C]) run(ctx context.Context) error {
	if !s.current() || len(s.nums) == 0 {
		return nil
	}
	if transport, ok := s.transport.(AsyncSender); ok && s.mode == message.Confirmable {
		return s.sendAsync(ctx, transport)
	}
	end := s.q2SetEnd(s.nums)
	for i, num := range s.nums[:end] {
		if !s.current() {
			return nil
		}
		if deadline := s.recoveryDeadline(); deadline.After(time.Now()) {
			return s.reschedule(ctx, s.nums[i:], deadline)
		}
		err := s.send(ctx, num)
		switch {
		case errors.Is(err, errQBlock2ResponseNotFound):
			return nil
		case err != nil:
			return err
		}
	}
	if s.recovery && s.mode == message.NonConfirmable {
		s.pauseNormalUntil(time.Now().Add(s.b.q2SetDelay(s.key)))
	}
	if !s.current() {
		return nil
	}
	return s.following(ctx, end)
}

func (s *q2SetJob[C]) q2SetEnd(nums []int64) int {
	for i, num := range nums {
		if num/s.b.config.MaxPayloads != nums[0]/s.b.config.MaxPayloads {
			return i
		}
	}
	return len(nums)
}
