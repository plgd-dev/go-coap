package tcp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	"github.com/plgd-dev/go-coap/v2/message/pool"
	"github.com/plgd-dev/go-coap/v2/net/observation"
	"github.com/plgd-dev/go-coap/v2/net/responsewriter"
	"github.com/plgd-dev/go-coap/v2/pkg/errors"
	coapSync "github.com/plgd-dev/go-coap/v2/pkg/sync"
	"go.uber.org/atomic"
)

func NewObservationHandler(observationTokenHandler *coapSync.Map[uint64, HandlerFunc], next HandlerFunc) HandlerFunc {
	return func(w *responsewriter.ResponseWriter[*ClientConn], r *pool.Message) {
		if v, ok := observationTokenHandler.Load(r.Token().Hash()); ok {
			v(w, r)
			return
		}
		next(w, r)
	}
}

type respObservationMessage struct {
	code         codes.Code
	notSupported bool
}

// Observation represents subscription to resource on the server
type Observation struct {
	token               message.Token
	path                string
	cc                  *ClientConn
	observeFunc         func(req *pool.Message)
	respObservationChan chan respObservationMessage
	waitForResponse     atomic.Bool

	mutex       sync.Mutex
	obsSequence uint32    // guarded by mutex
	lastEvent   time.Time // guarded by mutex
}

func (o *Observation) Canceled() bool {
	_, ok := o.cc.observationRequests.Load(o.token.Hash())
	return !ok
}

func newObservation(token message.Token, path string, cc *ClientConn, observeFunc func(req *pool.Message), respObservationChan chan respObservationMessage) *Observation {
	return &Observation{
		token:               token,
		path:                path,
		obsSequence:         0,
		cc:                  cc,
		waitForResponse:     *atomic.NewBool(true),
		respObservationChan: respObservationChan,
		observeFunc:         observeFunc,
	}
}

func (o *Observation) handler(w *responsewriter.ResponseWriter[*ClientConn], r *pool.Message) {
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

func (o *Observation) cleanUp() bool {
	// we can ignore err during cleanUp, if err != nil then some other
	// part of code already removed the handler for the token
	_, _ = o.cc.observationTokenHandler.PullOut(o.token.Hash())
	_, ok := o.cc.observationRequests.PullOut(o.token.Hash())
	return ok
}

// Cancel remove observation from server. For recreate observation use Observe.
func (o *Observation) Cancel(ctx context.Context) error {
	if !o.cleanUp() {
		// observation was already cleanup
		return nil
	}
	req, err := NewGetRequest(ctx, o.cc.session.messagePool, o.path)
	if err != nil {
		return fmt.Errorf("cannot cancel observation request: %w", err)
	}
	defer o.cc.session.messagePool.ReleaseMessage(req)
	req.SetObserve(1)
	req.SetToken(o.token)
	resp, err := o.cc.Do(req)
	if err != nil {
		return err
	}
	defer o.cc.session.messagePool.ReleaseMessage(resp)
	if resp.Code() != codes.Content {
		return fmt.Errorf("unexpected return code(%v)", resp.Code())
	}
	return nil
}

func (o *Observation) wantBeNotified(r *pool.Message) bool {
	obsSequence, err := r.Observe()
	if err != nil {
		return true
	}
	now := time.Now()

	o.mutex.Lock()
	defer o.mutex.Unlock()
	if observation.ValidSequenceNumber(o.obsSequence, obsSequence, o.lastEvent, now) {
		o.obsSequence = obsSequence
		o.lastEvent = now
		return true
	}

	return false
}

// NewObserveRequest creates observe request.
//
// Use ctx to set timeout.
func NewObserveRequest(ctx context.Context, messagePool *pool.Pool, path string, opts ...message.Option) (*pool.Message, error) {
	req, err := NewGetRequest(ctx, messagePool, path, opts...)
	if err != nil {
		return nil, fmt.Errorf("cannot create observe request: %w", err)
	}
	req.SetObserve(0)
	return req, nil
}

// ObserveRequest subscribes for every change of resource on path.
func (cc *ClientConn) ObserveRequest(req *pool.Message, observeFunc func(req *pool.Message)) (*Observation, error) {
	path, err := req.Path()
	if err != nil {
		return nil, fmt.Errorf("cannot get path: %w", err)
	}
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
	respObservationChan := make(chan respObservationMessage, 1)
	o := newObservation(token, path, cc, observeFunc, respObservationChan)

	options, err := req.Options().Clone()
	if err != nil {
		return nil, fmt.Errorf("cannot clone options: %w", err)
	}

	obs := observationMessage{
		ctx: req.Context(),
		Message: message.Message{
			Token:   req.Token(),
			Code:    req.Code(),
			Options: options,
		},
	}
	defer func(err *error) {
		if *err != nil {
			o.cleanUp()
		}
	}(&err)
	cc.observationRequests.Store(token.Hash(), obs)
	if _, loaded := o.cc.observationTokenHandler.LoadOrStore(token.Hash(), o.handler); loaded {
		err = errors.ErrKeyAlreadyExists
		return nil, err
	}

	err = cc.WriteMessage(req)
	if err != nil {
		return nil, err
	}
	select {
	case <-req.Context().Done():
		err = req.Context().Err()
		return nil, err
	case <-cc.Context().Done():
		err = fmt.Errorf("connection was closed: %w", cc.Context().Err())
		return nil, err
	case respObservationMessage := <-respObservationChan:
		if respObservationMessage.code != codes.Content {
			err = fmt.Errorf("unexpected return code(%v)", respObservationMessage.code)
			return nil, err
		}
		if respObservationMessage.notSupported {
			o.cleanUp()
		}
		return o, nil
	}
}

// Observe subscribes for every change of resource on path.
func (cc *ClientConn) Observe(ctx context.Context, path string, observeFunc func(req *pool.Message), opts ...message.Option) (*Observation, error) {
	req, err := NewObserveRequest(ctx, cc.session.messagePool, path, opts...)
	if err != nil {
		return nil, err
	}
	defer cc.session.messagePool.ReleaseMessage(req)
	return cc.ObserveRequest(req, observeFunc)
}
