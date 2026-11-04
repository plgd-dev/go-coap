package qblock

import (
	"errors"
	"fmt"
	"net"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
)

// Handle processes Q requests and observation cancellations. It returns false
// for messages that should continue through the legacy or application handler.
func (b *Engine[C]) Handle(w *responsewriter.ResponseWriter[C], r *pool.Message, maxSZX SZX,
	remoteAddr net.Addr, next func(*responsewriter.ResponseWriter[C], *pool.Message),
) bool {
	if !isRequestCode(r.Code()) {
		return false
	}
	if observe, err := r.Observe(); err == nil && observe == 1 {
		b.CancelQBlockObservation(r.Token(), remoteAddr)
	}
	if b.q1ResponseContinuation(w, r, maxSZX, remoteAddr) {
		return true
	}
	if r.HasOption(message.QBlock1) {
		if err := b.handleQBlock1(w, r, min(maxSZX, qBlockMaxSZX), remoteAddr, next); err != nil {
			b.respondQ1Error(w, r, remoteAddr, err)
		}
		return true
	}
	if r.HasOption(message.QBlock2) {
		b.dispatchQBlock2(w, r, maxSZX, remoteAddr, next)
		return true
	}
	return false
}

func (b *Engine[C]) q1ResponseContinuation(w *responsewriter.ResponseWriter[C], r *pool.Message,
	maxSZX SZX, remoteAddr net.Addr,
) bool {
	if !r.HasOption(message.QBlock1) || !r.HasOption(message.QBlock2) {
		return false
	}
	size, err := r.BodySize()
	requestSize, _ := r.GetOptionUint32(message.Size1)
	if err != nil || size != 0 || requestSize == 0 {
		return false
	}
	// Upload response continuations may retain the original Q1 control,
	// but must not be interpreted as another request-body payload.
	request := b.cloneMessage(r)
	defer b.cc.ReleaseMessage(request)
	request.Remove(message.QBlock1)
	b.dispatchQBlock2(w, request, maxSZX, remoteAddr, func(w *responsewriter.ResponseWriter[C], _ *pool.Message) {
		if err := w.SetResponse(codes.BadRequest, message.TextPlain, nil); err != nil {
			b.errors(err)
		}
	})
	return true
}

func (b *Engine[C]) respondQ1Error(w *responsewriter.ResponseWriter[C], r *pool.Message, remoteAddr net.Addr, err error) {
	w.Message().SetBody(nil)
	code := codes.BadRequest
	switch {
	case errors.Is(err, errReceivingMessagesCacheFull), errors.Is(err, ErrQBlockScheduleFull):
		b.sendServiceUnavailable(w, r.Token())
	case errors.Is(err, ErrRequestEntityTooLarge), errors.Is(err, ErrQBlockTooLarge):
		b.sendEntityTooLarge(w, r.Token())
	default:
		if errors.Is(err, errQBlockBadOption) {
			code = codes.BadOption
		}
		if setErr := w.SetResponse(code, message.TextPlain, nil); setErr != nil {
			b.errors(fmt.Errorf("cannot send invalid Q-Block1 response: %w", setErr))
		}
	}
	b.cacheQ1FinalError(r, remoteAddr, w.Message())
	b.errors(fmt.Errorf("handle Q-Block1 request(%v): %w", r, err))
}
