package observation

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	"github.com/plgd-dev/go-coap/v2/message/pool"
	"github.com/plgd-dev/go-coap/v2/net/responsewriter"
	"github.com/plgd-dev/go-coap/v2/pkg/errors"
	coapSync "github.com/plgd-dev/go-coap/v2/pkg/sync"
	"go.uber.org/atomic"
)

type Client interface {
	Context() context.Context
	WriteMessage(req *pool.Message) error
	Do(req *pool.Message) (*pool.Message, error)
	ReleaseMessage(msg *pool.Message)
	AcquireMessage(ctx context.Context) *pool.Message
}

// The HandlerFunc type is an adapter to allow the use of
// ordinary functions as COAP handlers.
type HandlerFunc[C Client] func(*responsewriter.ResponseWriter[C], *pool.Message)

type Handler[C Client] struct {
	cc           C
	observations *coapSync.Map[uint64, *Observation[C]]
	next         HandlerFunc[C]
}

func (h *Handler[C]) Handle(w *responsewriter.ResponseWriter[C], r *pool.Message) {
	if o, ok := h.observations.Load(r.Token().Hash()); ok {
		o.handle(w, r)
		return
	}
	h.next(w, r)
}

func (h *Handler[C]) client() C {
	return h.cc
}

func (h *Handler[C]) NewObservation(req *pool.Message, observeFunc func(req *pool.Message)) (*Observation[C], error) {
	observe, err := req.Observe()
	if err != nil {
		return nil, fmt.Errorf("cannot get observe option: %w", err)
	}
	if observe != 0 {
		return nil, fmt.Errorf("invalid value of observe(%v): expected 0", observe)
	}
	token := req.Token()
	if len(token) == 0 {
		return nil, fmt.Errorf("empty token")
	}
	options, err := req.Options().Clone()
	if err != nil {
		return nil, fmt.Errorf("cannot clone options: %w", err)
	}
	respObservationChan := make(chan respObservationMessage, 1)
	o := newObservation(message.Message{
		Token:   req.Token(),
		Code:    req.Code(),
		Options: options,
	}, h, observeFunc, respObservationChan)
	defer func(err *error) {
		if *err != nil {
			o.cleanUp()
		}
	}(&err)
	if _, loaded := h.observations.LoadOrStore(token.Hash(), o); loaded {
		err = errors.ErrKeyAlreadyExists
		return nil, err
	}

	err = h.cc.WriteMessage(req)
	if err != nil {
		return nil, err
	}
	select {
	case <-req.Context().Done():
		err = req.Context().Err()
		return nil, err
	case <-h.cc.Context().Done():
		err = fmt.Errorf("connection was closed: %w", h.cc.Context().Err())
		return nil, err
	case resp := <-respObservationChan:
		if resp.code != codes.Content {
			err = fmt.Errorf("unexpected return code(%v)", resp.code)
			return nil, err
		}
		if resp.notSupported {
			o.cleanUp()
		}
		return o, nil
	}
}

func (h *Handler[C]) GetObservation(key uint64) (*Observation[C], bool) {
	return h.observations.Load(key)
}

func (h *Handler[C]) pullOutObservation(key uint64) (*Observation[C], bool) {
	return h.observations.PullOut(key)
}

func NewHandler[C Client](cc C, next HandlerFunc[C]) *Handler[C] {
	return &Handler[C]{
		cc:           cc,
		observations: coapSync.NewMap[uint64, *Observation[C]](),
		next:         next,
	}
}

type respObservationMessage struct {
	code         codes.Code
	notSupported bool
}

// Observation represents subscription to resource on the server
type Observation[C Client] struct {
	req                 message.Message
	observeFunc         func(req *pool.Message)
	respObservationChan chan respObservationMessage
	waitForResponse     atomic.Bool
	observationHandler  *Handler[C]

	mutex       sync.Mutex
	obsSequence uint32    // guarded by mutex
	lastEvent   time.Time // guarded by mutex
}

func (o *Observation[C]) Canceled() bool {
	_, ok := o.observationHandler.GetObservation(o.req.Token.Hash())
	return !ok
}

func newObservation[C Client](req message.Message, observationHandler *Handler[C], observeFunc func(req *pool.Message), respObservationChan chan respObservationMessage) *Observation[C] {
	return &Observation[C]{
		req:                 req,
		obsSequence:         0,
		waitForResponse:     *atomic.NewBool(true),
		respObservationChan: respObservationChan,
		observeFunc:         observeFunc,
		observationHandler:  observationHandler,
	}
}

func (o *Observation[C]) handle(w *responsewriter.ResponseWriter[C], r *pool.Message) {
	code := r.Code()
	notSupported := !r.HasOption(message.Observe)
	if o.waitForResponse.CAS(true, false) {
		select {
		case o.respObservationChan <- respObservationMessage{
			code:         code,
			notSupported: notSupported,
		}:
		default:
		}
		o.respObservationChan = nil
	}
	if o.wantBeNotified(r) {
		o.observeFunc(r)
	}
}

func (o *Observation[C]) cleanUp() bool {
	// we can ignore err during cleanUp, if err != nil then some other
	// part of code already removed the handler for the token
	_, ok := o.observationHandler.pullOutObservation(o.req.Token.Hash())
	return ok
}

func (o *Observation[C]) client() C {
	return o.observationHandler.client()
}

func (o *Observation[C]) Request() message.Message {
	return o.req
}

// Cancel remove observation from server. For recreate observation use Observe.
func (o *Observation[C]) Cancel(ctx context.Context) error {
	if !o.cleanUp() {
		// observation was already cleanup
		return nil
	}

	req := o.client().AcquireMessage(ctx)
	defer o.client().ReleaseMessage(req)
	req.SetCode(codes.GET)
	req.SetObserve(1)
	if path, err := o.req.Options.Path(); err == nil {
		if err := req.SetPath(path); err != nil {
			return fmt.Errorf("cannot set path(%v): %w", path, err)
		}
	}
	req.SetToken(o.req.Token)
	resp, err := o.client().Do(req)
	if err != nil {
		return err
	}
	defer o.client().ReleaseMessage(resp)
	if resp.Code() != codes.Content {
		return fmt.Errorf("unexpected return code(%v)", resp.Code())
	}
	return nil
}

func (o *Observation[C]) wantBeNotified(r *pool.Message) bool {
	obsSequence, err := r.Observe()
	if err != nil {
		return true
	}
	now := time.Now()

	o.mutex.Lock()
	defer o.mutex.Unlock()
	if ValidSequenceNumber(o.obsSequence, obsSequence, o.lastEvent, now) {
		o.obsSequence = obsSequence
		o.lastEvent = now
		return true
	}

	return false
}
