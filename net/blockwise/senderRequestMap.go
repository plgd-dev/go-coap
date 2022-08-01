package blockwise

import (
	"fmt"

	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/pkg/sync"
)

func messageToGuardTransferKey(msg *pool.Message) string {
	code := msg.Code()
	path, _ := msg.Path()
	queries, _ := msg.Queries()

	return fmt.Sprintf("%v:%v:%v", code, path, queries)
}

type senderRequest struct {
	transferKey string
	*messageGuard
	release func()
	lock    bool
}

func setTypeFrom(to *pool.Message, from *pool.Message) {
	to.SetType(from.Type())
}

func (b *BlockWise[C]) newSentRequestMessage(r *pool.Message, lock bool) *senderRequest {
	req := b.cc.AcquireMessage(r.Context())
	req.SetCode(r.Code())
	req.SetToken(r.Token())
	req.ResetOptionsTo(r.Options())
	setTypeFrom(req, r)
	data := &senderRequest{
		transferKey:  messageToGuardTransferKey(req),
		messageGuard: newRequestGuard(req),
		release: func() {
			b.cc.ReleaseMessage(req)
		},
		lock: lock,
	}
	return data
}

type senderRequestMap struct {
	byToken       *sync.Map[uint64, *senderRequest]
	byTransferKey *sync.Map[string, *senderRequest]
}

func newSenderRequestMap() *senderRequestMap {
	return &senderRequestMap{
		byToken:       sync.NewMap[uint64, *senderRequest](),
		byTransferKey: sync.NewMap[string, *senderRequest](),
	}
}

func (m *senderRequestMap) store(req *senderRequest) error {
	if !req.lock {
		m.byToken.Store(req.Token().Hash(), req)
		return nil
	}
	for {
		var err error
		v, loaded := m.byTransferKey.LoadOrStoreWithFunc(req.transferKey, func(value *senderRequest) *senderRequest {
			return value
		}, func() *senderRequest {
			err = req.Acquire(req.Context(), 1)
			return req
		})
		if err != nil {
			return fmt.Errorf("cannot lock message: %w", err)
		}
		if !loaded {
			m.byToken.Store(req.Token().Hash(), req)
			return nil
		}
		err = v.Acquire(req.Context(), 1)
		if err != nil {
			return fmt.Errorf("cannot lock message: %w", err)
		}
		v.Release(1)
	}
}

func (m *senderRequestMap) deleteByToken(token uint64) {
	req1, ok := m.byToken.PullOut(token)
	if !ok {
		return
	}
	req2, ok := m.byTransferKey.Load(req1.transferKey)
	if !ok {
		return
	}
	if req1 == req2 {
		m.byTransferKey.Delete(req1.transferKey)
		req1.Release(1)
	}
}
