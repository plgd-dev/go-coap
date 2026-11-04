package qblock

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/net/blockwise/internal/blocks"
)

type q2SetJob[C Client] struct {
	b                   *Engine[C]
	transport           Transport
	key                 string
	ctx                 context.Context
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

func (b *Engine[C]) scheduleQ2Set(ctx context.Context, transport Transport, key string,
	token message.Token, szx SZX, mode message.Type, nums []int64, count int64,
	wholeBody, recovery bool, deadline time.Time, expected ...*q2SendTransfer,
) error {
	b.q2SendMu.Lock()
	defer b.q2SendMu.Unlock()
	generation := b.q2Senders[key]
	if generation == nil || len(expected) != 0 && generation != expected[0] {
		return nil
	}
	job := &q2SetJob[C]{
		b: b, transport: transport, key: key, ctx: ctx, token: token, szx: szx, mode: mode,
		nums: nums, count: count, wholeBody: wholeBody, recovery: recovery, generation: generation,
	}
	return transport.ScheduleQBlock(q2JobKey(key, token, nums, recovery), deadline, job.run)
}

func (s *q2SetJob[C]) current() bool {
	s.b.q2SendMu.Lock()
	defer s.b.q2SendMu.Unlock()
	return s.b.q2Senders[s.key] == s.generation
}

func (s *q2SetJob[C]) reschedule(nums []int64, deadline time.Time) error {
	return s.b.scheduleQ2Set(s.ctx, s.transport, s.key, s.token, s.szx, s.mode, nums, s.count,
		s.wholeBody, s.recovery, deadline, s.generation)
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

func (s *q2SetJob[C]) send(num int64) error {
	response, err := s.b.buildQBlock2ResponseFrom(s.ctx, s.key, s.token, s.szx, num, s.generation)
	if err != nil {
		return err
	}
	defer s.b.cc.ReleaseMessage(response)
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
	return s.transport.SendQBlock(response)
}

func (s *q2SetJob[C]) pauseNormalUntil(deadline time.Time) {
	s.b.q2SendMu.Lock()
	if s.b.q2Senders[s.key] == s.generation && deadline.After(s.generation.recoveryUntil) {
		s.generation.recoveryUntil = deadline
	}
	s.b.q2SendMu.Unlock()
	s.transport.DelayQBlock("q2:"+s.key, deadline)
}

func (s *q2SetJob[C]) following(end int) error {
	delay := time.Duration(0)
	if s.mode == message.NonConfirmable {
		delay = s.b.q2SetDelay(s.key)
	}
	if end < len(s.nums) {
		deadline := time.Now().Add(delay)
		if s.recovery && s.mode == message.NonConfirmable {
			s.pauseNormalUntil(deadline.Add(delay))
		}
		return s.reschedule(s.nums[end:], deadline)
	}
	if !s.wholeBody || len(s.nums) == 0 || s.nums[len(s.nums)-1]+1 >= s.count {
		return nil
	}
	next := s.nums[len(s.nums)-1] + 1
	var following []int64
	for num := next; num <= s.b.config.setLast(next, s.count); num++ {
		following = append(following, num)
	}
	return s.reschedule(following, time.Now().Add(delay))
}

func (s *q2SetJob[C]) run() error {
	if !s.current() {
		return nil
	}
	end := s.q2SetEnd(s.nums)
	for i, num := range s.nums[:end] {
		if !s.current() {
			return nil
		}
		if deadline := s.recoveryDeadline(); deadline.After(time.Now()) {
			return s.reschedule(s.nums[i:], deadline)
		}
		err := s.send(num)
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
	return s.following(end)
}

func (s *q2SetJob[C]) q2SetEnd(nums []int64) int {
	for i, num := range nums {
		if num/s.b.config.MaxPayloads != nums[0]/s.b.config.MaxPayloads {
			return i
		}
	}
	return len(nums)
}
