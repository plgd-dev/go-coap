package client

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
)

var errInvalidQBlockReceiverConfig = errors.New("invalid q-block receiver configuration")

type qblockReceiverConfig struct {
	Manager qblock.ManagerConfig
	Now     func() time.Time
}

type qblockPending struct {
	token message.Token
	fail  func(error)
}

type qblockTransfer struct {
	operation qblock.OperationKey
	metadata  qblock.Metadata
	tokens    map[string]struct{}
	pending   qblockPending
}

// qblockReceiver is an internal connection adapter for Q-Block2 downloads.
// It deliberately has no exported construction path.
type qblockReceiver struct {
	cc              *Conn
	now             func() time.Time
	mu              sync.Mutex
	manager         *qblock.Manager
	initErr         error
	pending         map[string]qblockPending
	transfers       map[qblock.TransferID]*qblockTransfer
	transferByToken map[string]qblock.TransferID
}

func withQBlockReceiver(cfg qblockReceiverConfig) Option {
	return func(opts *ConnOptions) {
		opts.createQBlockReceiver = func(cc *Conn) *qblockReceiver {
			return newQBlockReceiver(cc, cfg)
		}
	}
}

func newQBlockReceiver(cc *Conn, cfg qblockReceiverConfig) *qblockReceiver {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	manager, err := qblock.NewManager(cfg.Manager)
	if err != nil {
		err = errors.Join(errInvalidQBlockReceiverConfig, err)
	}
	return &qblockReceiver{
		cc:              cc,
		now:             now,
		manager:         manager,
		initErr:         err,
		pending:         make(map[string]qblockPending),
		transfers:       make(map[qblock.TransferID]*qblockTransfer),
		transferByToken: make(map[string]qblock.TransferID),
	}
}

// prepare converts an eligible internal GET into the initial Q-Block2 GET.
// All other requests retain their existing client behavior.
func (r *qblockReceiver) prepare(req *pool.Message, fail func(error)) (bool, error) {
	if r.initErr != nil {
		return false, r.initErr
	}
	if req.Code() != codes.GET || req.HasOption(message.Observe) || req.Body() != nil || req.HasOption(message.QBlock1) || req.HasOption(message.QBlock2) {
		return false, nil
	}
	token := req.Token()
	if len(token) == 0 {
		return false, errors.New("q-block GET requires token")
	}
	value, err := qblock.EncodeBlock(qblock.Block{Number: 0, More: true, SZX: r.cc.blockwiseSZX})
	if err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.pending[string(token)]; ok {
		return false, errors.New("q-block GET token already pending")
	}
	req.SetType(message.NonConfirmable)
	req.SetOptionUint32(message.QBlock2, value)
	r.pending[string(token)] = qblockPending{token: message.Token(bytes.Clone(token)), fail: fail}
	return true, nil
}

func (r *qblockReceiver) active() uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.manager == nil {
		return 0
	}
	return r.manager.Active()
}

func (r *qblockReceiver) abandon(token message.Token) {
	r.mu.Lock()
	delete(r.pending, string(token))
	r.mu.Unlock()
}

// handle consumes every Q-Block2 response before ordinary token or classic
// blockwise routing. Invalid fragments are intentionally dropped.
func (r *qblockReceiver) handle(msg *pool.Message) bool {
	if !msg.HasOption(message.QBlock2) {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	token := msg.Token()
	if id, ok := r.transferByToken[string(token)]; ok {
		transfer := r.transfers[id]
		if transfer == nil {
			return true
		}
		fragment, _, err := fragmentFromQ2(msg, transfer.operation, &transfer.metadata)
		if err != nil {
			r.processOutputsLocked(id, r.manager.Cancel(id, err))
			return true
		}
		outputs, err := r.manager.Receive(fragment, r.now())
		if err != nil {
			r.processOutputsLocked(id, r.manager.Cancel(id, err))
			return true
		}
		r.processOutputsLocked(id, outputs)
		return true
	}
	pending, ok := r.pending[string(token)]
	if !ok {
		return true
	}
	// The operation is established only after all first-fragment validation
	// succeeds. Until then pending and manager state remain unchanged.
	etag, err := qblockETag(msg)
	if err != nil {
		return true
	}
	operation, err := qblock.NewOperationKey(pending.token, etag)
	if err != nil {
		return true
	}
	fragment, metadata, err := fragmentFromQ2(msg, operation, nil)
	if err != nil {
		return true
	}
	outputs, err := r.manager.StartReceiver(fragment, r.now())
	if err != nil {
		return true
	}
	id, ok := r.manager.TransferID(operation)
	if !ok {
		return true
	}
	transfer := &qblockTransfer{
		operation: operation,
		metadata:  metadata,
		tokens:    map[string]struct{}{string(token): {}},
		pending:   pending,
	}
	r.transfers[id] = transfer
	r.transferByToken[string(token)] = id
	delete(r.pending, string(token))
	r.processOutputsLocked(id, outputs)
	return true
}

func (r *qblockReceiver) processOutputsLocked(id qblock.TransferID, outputs []qblock.Output) {
	for _, output := range outputs {
		transfer := r.transfers[id]
		if transfer == nil {
			continue
		}
		switch output.Action.Kind {
		case qblock.Deliver:
			response := r.cc.AcquireMessage(r.cc.Context())
			response.SetCode(codes.Content)
			response.SetToken(transfer.pending.token)
			if transfer.metadata.HasContentFormat {
				response.SetContentFormat(transfer.metadata.ContentFormat)
			}
			if err := response.SetETag(transfer.metadata.Identity); err != nil {
				r.cc.ReleaseMessage(response)
				transfer.pending.fail(err)
				continue
			}
			response.SetBody(bytes.NewReader(output.Action.Payload))
			if handler, ok := r.cc.tokenHandlerContainer.LoadAndDelete(transfer.pending.token.Hash()); ok {
				handler(nil, response)
			} else {
				r.cc.ReleaseMessage(response)
			}
		case qblock.Complete:
			if output.Action.Err != nil {
				_, _ = r.cc.tokenHandlerContainer.LoadAndDelete(transfer.pending.token.Hash())
				transfer.pending.fail(output.Action.Err)
			}
		case qblock.Release:
			for token := range transfer.tokens {
				delete(r.transferByToken, token)
			}
			delete(r.transfers, id)
		}
	}
}

func qblockETag(msg *pool.Message) ([]byte, error) {
	if qblockOptionCount(msg, message.ETag) != 1 {
		return nil, errors.New("q-block response requires one ETag")
	}
	etag, err := msg.ETag()
	if err != nil || len(etag) == 0 {
		return nil, errors.New("q-block response requires ETag")
	}
	return bytes.Clone(etag), nil
}

func fragmentFromQ2(msg *pool.Message, operation qblock.OperationKey, previous *qblock.Metadata) (qblock.Fragment, qblock.Metadata, error) {
	if msg.Code() != codes.Content {
		return qblock.Fragment{}, qblock.Metadata{}, errors.New("q-block response must be 2.05 Content")
	}
	if err := qblock.ValidateOptions(msg.Options(), false); err != nil {
		return qblock.Fragment{}, qblock.Metadata{}, err
	}
	if qblockOptionCount(msg, message.QBlock2) != 1 {
		return qblock.Fragment{}, qblock.Metadata{}, errors.New("q-block response requires one QBlock2")
	}
	blockValue, err := msg.GetOptionUint32(message.QBlock2)
	if err != nil {
		return qblock.Fragment{}, qblock.Metadata{}, err
	}
	block, err := qblock.DecodeBlock(blockValue)
	if err != nil {
		return qblock.Fragment{}, qblock.Metadata{}, err
	}
	if qblockOptionCount(msg, message.Size2) != 1 {
		return qblock.Fragment{}, qblock.Metadata{}, errors.New("q-block response requires one Size2")
	}
	size, err := msg.GetOptionUint32(message.Size2)
	if err != nil {
		return qblock.Fragment{}, qblock.Metadata{}, err
	}
	etag, err := qblockETag(msg)
	if err != nil {
		return qblock.Fragment{}, qblock.Metadata{}, err
	}
	metadata := qblock.Metadata{Size: size, SZX: block.SZX, Identity: etag}
	if msg.HasOption(message.ContentFormat) {
		format, err := msg.ContentFormat()
		if err != nil {
			return qblock.Fragment{}, qblock.Metadata{}, err
		}
		metadata.HasContentFormat = true
		metadata.ContentFormat = format
	}
	if previous != nil && (!bytes.Equal(previous.Identity, metadata.Identity) || previous.Size != metadata.Size || previous.SZX != metadata.SZX || previous.HasContentFormat != metadata.HasContentFormat || (previous.HasContentFormat && previous.ContentFormat != metadata.ContentFormat)) {
		return qblock.Fragment{}, qblock.Metadata{}, errors.New("q-block response metadata changed")
	}
	payload := []byte(nil)
	if body := msg.Body(); body != nil {
		payload, err = io.ReadAll(body)
		if err != nil {
			return qblock.Fragment{}, qblock.Metadata{}, err
		}
	}
	return qblock.Fragment{Operation: operation, Token: msg.Token(), Kind: qblock.Q2, Metadata: metadata, Block: block, Payload: payload}, metadata, nil
}

func qblockOptionCount(msg *pool.Message, id message.OptionID) int {
	count := 0
	for _, option := range msg.Options() {
		if option.ID == id {
			count++
		}
	}
	return count
}
