package udp

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	"github.com/go-ocf/go-coap/v2/message/codes"
)

//Observation represents subscription to resource on the server
type Observation struct {
	token       []byte
	path        string
	obsSequence uint32
	etag        []byte
	cc          *ClientConn

	mutex sync.Mutex
}

func NewObservatiomHandler(obsertionTokenHandler *HandlerContainer, next func(w *ResponseWriter, r *Message)) func(w *ResponseWriter, r *Message) {
	return func(w *ResponseWriter, r *Message) {
		v, err := obsertionTokenHandler.Get(r.Token())
		if err != nil {
			next(w, r)
			return
		}
		v(w, r)
	}
}

// Cancel remove observation from server. For recreate observation use Observe.
func (o *Observation) Cancel(ctx context.Context) error {
	req, err := NewGetRequest(ctx, o.path)
	if err != nil {
		return fmt.Errorf("cannot cancel observation request: %w", err)
	}
	defer ReleaseRequest(req)
	req.SetObserve(1)
	req.SetToken(o.token)
	o.cc.observationTokenHandler.Pop(o.token)
	return o.cc.session.WriteRequest(req)
}

func (o *Observation) wantBeNotified(r *Message) bool {
	obsSequence, err := r.Observe()
	if err != nil {
		return true
	}

	o.mutex.Lock()
	defer o.mutex.Unlock()
	//obs starts with 0, after that check obsSequence
	if obsSequence != 0 && o.obsSequence > obsSequence {
		return false
	}
	o.obsSequence = obsSequence

	etag, err := r.ETag()
	if err == nil {
		if bytes.Equal(o.etag, etag) {
			return false
		}
		o.etag = make([]byte, len(etag))
		copy(o.etag, etag)
	}
	return true
}

// Observe subscribes for every change of resource on path.
func (cc *ClientConn) Observe(ctx context.Context, path string, observeFunc func(req *Message)) (*Observation, error) {
	req, err := NewGetRequest(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("cannot create observe request: %w", err)
	}
	defer ReleaseRequest(req)
	req.SetObserve(0)
	o := &Observation{
		token:       req.Token(),
		path:        path,
		obsSequence: 0,
		cc:          cc,
	}
	respChan := make(chan *Message, 1)
	err = o.cc.observationTokenHandler.Insert(req.Token(), func(w *ResponseWriter, r *Message) {
		if o.wantBeNotified(r) {
			observeFunc(req)
		}
		select {
		case respChan <- r:
			r.Hijack()
		default:
		}
	})
	if err != nil {
		return nil, err
	}

	err = cc.session.WriteRequest(req)
	select {
	case <-req.ctx.Done():
		o.cc.observationTokenHandler.Pop(req.Token())
		return nil, req.ctx.Err()
	case <-cc.Context().Done():
		o.cc.observationTokenHandler.Pop(req.Token())
		return nil, fmt.Errorf("connection was closed: %w", req.ctx.Err())
	case resp := <-respChan:
		defer ReleaseRequest(resp)
		if resp.Code() != codes.Content {
			o.cc.observationTokenHandler.Pop(req.Token())
			return nil, fmt.Errorf("unexected return code(%v)", resp.Code())
		}
		return o, nil
	}
}
