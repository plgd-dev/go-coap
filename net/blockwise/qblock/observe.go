package qblock

import (
	"bytes"
	"context"
	"errors"
	"net"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise/internal/blocks"
)

type qObservation struct {
	request         *pool.Message
	key             string
	szx             SZX
	registrationKey string
}

func qObservationKey(token message.Token, remote net.Addr) string {
	var key bytes.Buffer
	if remote != nil {
		blocks.WriteExchangeKeyPart(&key, []byte(remote.String()))
	}
	blocks.WriteExchangeKeyPart(&key, token)
	return key.String()
}

// CancelQBlockObservation drops a registration and partial outgoing body after
// an Observe cancellation or a transport-correlated Reset.
func (b *Engine[C]) CancelQBlockObservation(token message.Token, remote net.Addr) {
	b.q2SendMu.Lock()
	observation := b.qObservations[qObservationKey(token, remote)]
	if observation != nil {
		delete(b.qObservations, qObservationKey(token, remote))
		b.rememberQCancellationLocked(qObservationKey(token, remote))
		b.cc.ReleaseMessage(observation.request)
		b.qReleaseTransfer()
		if transfer := b.q2Senders[observation.key]; transfer != nil {
			delete(b.q2Senders, observation.key)
			b.q2SendBytes -= transfer.size
			b.qReleaseBytes(transfer.size)
			b.qReleaseTransfer()
			b.cc.ReleaseMessage(transfer.message)
		}
	}
	b.q2SendMu.Unlock()
	if observation != nil {
		if transport, ok := any(b.cc).(Transport); ok {
			transport.CancelQBlock("q2:" + observation.key)
		}
	}
}

func (b *Engine[C]) rememberQCancellationLocked(key string) {
	if b.qCanceledObservations == nil {
		b.qCanceledObservations = make(map[string]time.Time)
	}
	if len(b.qCanceledObservations) >= 128 {
		oldest := ""
		for candidate, expires := range b.qCanceledObservations {
			if oldest == "" || expires.Before(b.qCanceledObservations[oldest]) {
				oldest = candidate
			}
		}
		delete(b.qCanceledObservations, oldest)
	}
	b.qCanceledObservations[key] = time.Now().Add(b.config.NonPartialTimeout())
}

func (b *Engine[C]) registerQObservation(r *pool.Message, remote net.Addr, szx SZX) error {
	b.q2SendMu.Lock()
	defer b.q2SendMu.Unlock()
	if b.q2Closed {
		return qClosedError(true)
	}
	if b.qObservations == nil {
		b.qObservations = make(map[string]*qObservation)
	}
	key := qObservationKey(r.Token(), remote)
	delete(b.qCanceledObservations, key)
	if previous := b.qObservations[key]; previous != nil {
		b.cc.ReleaseMessage(previous.request)
	} else if len(b.qObservations) >= 128 || !b.qReserveTransfer() {
		return ErrQBlockTooLarge
	}
	b.qObservations[key] = &qObservation{request: b.cloneMessage(r), key: q2TransferKey(r, remote), szx: szx, registrationKey: key}
	return nil
}

// WriteNotification sends a registered Q notification and reports whether it
// handled the message. A false result leaves non-Q notifications to the caller.
func (b *Engine[C]) WriteNotification(response *pool.Message, remote net.Addr) (bool, error) {
	request, generation, handled, err := b.registeredQNotification(response, remote)
	if !handled {
		return false, err
	}
	if err != nil {
		return true, err
	}
	defer b.cc.ReleaseMessage(request)
	key, szx := generation.key, generation.szx
	transport, ok := any(b.cc).(Transport)
	if !ok {
		return true, errors.New("Q-Block notification transport is unavailable")
	}
	transport.CancelQBlock("q2:" + key)
	mode := response.Type()
	if mode != message.NonConfirmable {
		mode = message.Confirmable
	}
	request.SetType(mode)
	err = b.cacheQBlock2Notification(response.Context(), key, qTransferKey(request, remote, nil, true), response, mode, generation)
	if err != nil {
		return true, err
	}
	size, err := response.BodySize()
	if err != nil {
		return true, err
	}
	count := max(int64(1), (size+szx.Size()-1)/szx.Size())
	first, err := b.buildQBlock2Response(response.Context(), key, response.Token(), szx, 0)
	if err != nil {
		return true, err
	}
	first.SetType(mode)
	err = transport.SendQBlock(first)
	b.cc.ReleaseMessage(first)
	if err != nil || count == 1 {
		return true, err
	}
	nums, deadline := b.firstNotificationSet(key, mode, count)
	return true, b.scheduleQ2Set(response.Context(), &q2SetJob[C]{
		transport: transport, key: key, token: response.Token(), szx: szx, mode: mode,
		nums: nums, count: count, wholeBody: true,
	}, deadline)
}

func (b *Engine[C]) registeredQNotification(response *pool.Message, remote net.Addr) (*pool.Message, *qObservation, bool, error) {
	if isRequestCode(response.Code()) {
		return nil, nil, false, nil
	}
	b.q2SendMu.Lock()
	observationKey := qObservationKey(response.Token(), remote)
	observation := b.qObservations[observationKey]
	if observation == nil {
		canceled := b.qCanceledObservations[observationKey].After(time.Now())
		b.q2SendMu.Unlock()
		if canceled {
			return nil, nil, true, context.Canceled
		}
		return nil, nil, false, nil
	}
	request := b.cloneMessage(observation.request)
	b.q2SendMu.Unlock()
	return request, observation, true, nil
}

func (b *Engine[C]) firstNotificationSet(key string, mode message.Type, count int64) ([]int64, time.Time) {
	var nums []int64
	for num := int64(1); num <= b.config.setLast(0, count); num++ {
		nums = append(nums, num)
	}

	deadline := time.Now()
	if len(nums) == 0 {
		next := b.config.setLast(0, count) + 1
		for num := next; num <= b.config.setLast(next, count); num++ {
			nums = append(nums, num)
		}
		if mode == message.NonConfirmable {
			deadline = deadline.Add(b.q2SetDelay(key))
		}
	}
	return nums, deadline
}

func (b *Engine[C]) validateQNotificationLocked(generation *qObservation) error {
	if generation != nil && b.qObservations[generation.registrationKey] != generation {
		return context.Canceled
	}
	return nil
}

func (b *Engine[C]) finishQNotificationLocked(generation *qObservation, observePresent bool) {
	if generation == nil || observePresent {
		return
	}
	// Retire the registration, but leave the terminal body available to recovery.
	delete(b.qObservations, generation.registrationKey)
	b.rememberQCancellationLocked(generation.registrationKey)
	b.cc.ReleaseMessage(generation.request)
	b.qReleaseTransfer()
}
