package client

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	"github.com/plgd-dev/go-coap/v2/net/observation"
	"github.com/plgd-dev/go-coap/v2/udp/message/pool"
	"go.uber.org/atomic"
)

func NewObservationHandler(obsertionTokenHandler *HandlerContainer, next HandlerFunc) HandlerFunc {
	return func(w *ResponseWriter, r *pool.Message) {
		v, err := obsertionTokenHandler.Get(r.Token())
		if err == nil {
			v(w, r)
			return
		}
		obs, err := r.Observe()
		if err == nil && obs > 1 {
			w.SendReset()
			return
		}
		next(w, r)
	}
}

type respObservationMessage struct {
	code         codes.Code
	notSupported bool
}

//Observation represents subscription to resource on the server
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

func (o *Observation) Canceled() bool {
	_, ok := o.cc.observationRequests.Load(o.token.Hash())
	return !ok
}

func (o *Observation) cleanUp() bool {
	// we can ignore err during cleanUp, if err != nil then some other
	// part of code already removed the handler for the token
	_, _ = o.cc.observationTokenHandler.Pop(o.token)
	registeredRequest, ok := o.cc.observationRequests.PullOut(o.token.Hash())
	if ok {
		o.cc.ReleaseMessage(registeredRequest.(*pool.Message))
	}
	return ok
}

func (o *Observation) handler(w *ResponseWriter, r *pool.Message) {
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

// Cancel remove observation from server. For recreate observation use Observe.
func (o *Observation) Cancel(ctx context.Context) error {
	if !o.cleanUp() {
		// observation was already cleanup
		return nil
	}
	req, err := NewGetRequest(ctx, o.cc.messagePool, o.path)
	if err != nil {
		return fmt.Errorf("cannot cancel observation request: %w", err)
	}
	defer o.cc.ReleaseMessage(req)
	req.SetObserve(1)
	req.SetToken(o.token)
	resp, err := o.cc.Do(req)
	if err != nil {
		return err
	}
	defer o.cc.ReleaseMessage(resp)
	if resp.Code() != codes.Content {
		return fmt.Errorf("unexpected return code(%v)", resp.Code())
	}
	return err
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

// Observe subscribes for every change of resource on path. It can return canceled observation and it happens when resource doesn't support observation.
// This is detected when the first notification doesn't contains observe option.
func (cc *ClientConn) Observe(ctx context.Context, path string, observeFunc func(msg *pool.Message), opts ...message.Option) (*Observation, error) {
	req, err := NewGetRequest(ctx, cc.messagePool, path, opts...)
	if err != nil {
		return nil, fmt.Errorf("cannot create observe request: %w", err)
	}
	token := req.Token()
	req.SetObserve(0)
	respObservationChan := make(chan respObservationMessage, 1)
	o := newObservation(token, path, cc, observeFunc, respObservationChan)

	cc.observationRequests.Store(token.Hash(), req)
	err = o.cc.observationTokenHandler.Insert(token.Hash(), o.handler)
	defer func(err *error) {
		if *err != nil {
			o.cleanUp()
		}
	}(&err)
	if err != nil {
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
