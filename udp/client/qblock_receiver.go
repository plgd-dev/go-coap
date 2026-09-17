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

var (
	errInvalidQBlockReceiverConfig = errors.New("invalid q-block receiver configuration")
	errQBlockControlToken          = errors.New("cannot allocate q-block control token")
	errQBlockMixedResponseOptions  = errors.New("q-block response cannot mix QBlock1 and QBlock2")
)

type qblockReceiverConfig struct {
	Manager qblock.ManagerConfig
	Now     func() time.Time
}

type qblockPending struct {
	token   message.Token
	options message.Options
	fail    func(error)
}

type qblockTransfer struct {
	operation       qblock.OperationKey
	metadata        qblock.Metadata
	tokens          map[string]struct{}
	originalToken   message.Token
	requestOptions  message.Options
	responseOptions message.Options
	responseCode    codes.Code
	fail            func(error)
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

func (r *qblockReceiver) canPrepare(req *pool.Message) bool {
	return r.initErr == nil && req.Code() == codes.GET && !req.HasOption(message.Observe) && req.Body() == nil && !req.HasOption(message.QBlock1) && !req.HasOption(message.QBlock2)
}

// prepare converts an eligible internal GET into the initial Q-Block2 GET.
// All other requests retain their existing client behavior.
func (r *qblockReceiver) prepare(req *pool.Message, fail func(error)) (bool, error) {
	if r.initErr != nil {
		return false, r.initErr
	}
	if !r.canPrepare(req) {
		return false, nil
	}
	token := req.Token()
	if len(token) == 0 {
		return false, errors.New("q-block GET requires token")
	}
	options, err := req.Options().Clone()
	if err != nil {
		return false, err
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
	r.pending[string(token)] = qblockPending{token: message.Token(bytes.Clone(token)), options: options, fail: fail}
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

// Tick advances Q-Block receiver deadlines at the connection-supplied time.
func (r *qblockReceiver) Tick(now time.Time) {
	r.mu.Lock()
	if r.manager == nil {
		r.mu.Unlock()
		return
	}
	outputs := r.manager.Tick(now)
	r.mu.Unlock()
	r.execute(outputs)
}

func (r *qblockReceiver) abandon(token message.Token, err error) {
	if err == nil {
		err = qblock.ErrCanceled
	}
	r.mu.Lock()
	key := string(token)
	if _, ok := r.pending[key]; ok {
		delete(r.pending, key)
		r.mu.Unlock()
		return
	}
	id, ok := r.transferByToken[key]
	if !ok {
		r.mu.Unlock()
		return
	}
	outputs := r.manager.Cancel(id, err)
	r.mu.Unlock()
	r.processOutputs(id, outputs)
}

func (r *qblockReceiver) close() {
	var pending []qblockPending
	var outputs []qblock.Output
	r.mu.Lock()
	for key, record := range r.pending {
		delete(r.pending, key)
		pending = append(pending, record)
	}
	if r.manager != nil {
		for id := range r.transfers {
			outputs = append(outputs, r.manager.Cancel(id, qblock.ErrClosed)...)
		}
	}
	r.mu.Unlock()
	r.execute(outputs)
	for _, record := range pending {
		_, _ = r.cc.tokenHandlerContainer.LoadAndDelete(record.token.Hash())
		record.fail(qblock.ErrClosed)
	}
}

// handle consumes every Q-Block2 response before ordinary token or classic
// blockwise routing. Invalid fragments are intentionally dropped.
func (r *qblockReceiver) handle(msg *pool.Message) bool {
	if !msg.HasOption(message.QBlock2) {
		return false
	}
	r.mu.Lock()
	token := msg.Token()
	if id, ok := r.transferByToken[string(token)]; ok {
		transfer := r.transfers[id]
		if transfer == nil {
			r.mu.Unlock()
			return true
		}
		fragment, _, err := fragmentFromQ2(msg, transfer.operation, &transfer.metadata)
		if err != nil {
			outputs := r.manager.Cancel(id, err)
			r.mu.Unlock()
			r.processOutputs(id, outputs)
			return true
		}
		outputs, err := r.manager.Receive(fragment, r.now())
		if err != nil {
			outputs = r.manager.Cancel(id, err)
			r.mu.Unlock()
			r.processOutputs(id, outputs)
			return true
		}
		r.mu.Unlock()
		r.processOutputs(id, outputs)
		return true
	}
	pending, ok := r.pending[string(token)]
	if !ok {
		r.mu.Unlock()
		return true
	}
	// The operation is established only after all first-fragment validation
	// succeeds. Until then pending and manager state remain unchanged.
	etag, err := qblockETag(msg)
	if err != nil {
		r.mu.Unlock()
		return true
	}
	operation, err := qblock.NewOperationKey(pending.token, etag)
	if err != nil {
		r.mu.Unlock()
		return true
	}
	fragment, metadata, err := fragmentFromQ2(msg, operation, nil)
	if err != nil {
		r.mu.Unlock()
		return true
	}
	responseOptions, err := qblockResponseOptions(msg)
	if err != nil {
		r.mu.Unlock()
		return true
	}
	outputs, err := r.manager.StartReceiver(fragment, r.now())
	if err != nil {
		r.mu.Unlock()
		return true
	}
	id, ok := r.manager.TransferID(operation)
	if !ok && len(outputs) > 0 {
		id = outputs[0].TransferID
		ok = true
	}
	if !ok {
		r.mu.Unlock()
		return true
	}
	transfer := &qblockTransfer{
		operation:       operation,
		metadata:        metadata,
		tokens:          map[string]struct{}{string(token): {}},
		originalToken:   pending.token,
		requestOptions:  pending.options,
		responseOptions: responseOptions,
		responseCode:    msg.Code(),
		fail:            pending.fail,
	}
	r.transfers[id] = transfer
	r.transferByToken[string(token)] = id
	delete(r.pending, string(token))
	r.mu.Unlock()
	r.processOutputs(id, outputs)
	return true
}

type qblockDelivery struct {
	handler  HandlerFunc
	response *pool.Message
}

type qblockFailure struct {
	fail func(error)
	err  error
}

type qblockControl struct {
	id     qblock.TransferID
	action qblock.Action
}

func (r *qblockReceiver) processOutputs(id qblock.TransferID, outputs []qblock.Output) {
	var controls []qblockControl
	var deliveries []qblockDelivery
	var failures []qblockFailure
	r.mu.Lock()
	for _, output := range outputs {
		transfer := r.transfers[id]
		if transfer == nil {
			continue
		}
		switch output.Action.Kind {
		case qblock.SendContinue, qblock.RequestMissing:
			controls = append(controls, qblockControl{id: id, action: output.Action})
		case qblock.Deliver:
			response := r.cc.AcquireMessage(r.cc.Context())
			response.SetCode(transfer.responseCode)
			response.SetToken(transfer.originalToken)
			response.ResetOptionsTo(transfer.responseOptions)
			response.SetBody(bytes.NewReader(output.Action.Payload))
			if handler, ok := r.cc.tokenHandlerContainer.LoadAndDelete(transfer.originalToken.Hash()); ok {
				deliveries = append(deliveries, qblockDelivery{handler: handler, response: response})
			} else {
				r.cc.ReleaseMessage(response)
			}
		case qblock.Complete:
			if output.Action.Err != nil {
				_, _ = r.cc.tokenHandlerContainer.LoadAndDelete(transfer.originalToken.Hash())
				failures = append(failures, qblockFailure{fail: transfer.fail, err: output.Action.Err})
			}
		case qblock.Release:
			for token := range transfer.tokens {
				delete(r.transferByToken, token)
			}
			delete(r.transfers, id)
		}
	}
	r.mu.Unlock()
	for _, control := range controls {
		if err := r.writeControl(control.id, control.action); err != nil {
			r.cancel(control.id, err)
		}
	}
	for _, delivery := range deliveries {
		delivery.handler(nil, delivery.response)
	}
	for _, failure := range failures {
		failure.fail(failure.err)
	}
}

func (r *qblockReceiver) execute(outputs []qblock.Output) {
	for start := 0; start < len(outputs); {
		end := start + 1
		for end < len(outputs) && outputs[end].TransferID == outputs[start].TransferID {
			end++
		}
		r.processOutputs(outputs[start].TransferID, outputs[start:end])
		start = end
	}
}

func (r *qblockReceiver) writeControl(id qblock.TransferID, action qblock.Action) error {
	r.mu.Lock()
	record := r.transfers[id]
	if record == nil {
		r.mu.Unlock()
		return qblock.ErrUnknownTransfer
	}
	szx := record.metadata.SZX
	r.mu.Unlock()

	var blocks []qblock.Block
	switch action.Kind {
	case qblock.SendContinue:
		blocks = append(blocks, qblock.Block{Number: action.Through + 1, More: true, SZX: szx})
	case qblock.RequestMissing:
		for _, number := range action.Numbers {
			blocks = append(blocks, qblock.Block{Number: number, SZX: szx})
		}
	default:
		return nil
	}
	for _, block := range blocks {
		token, err := r.bindControlToken(id)
		if err != nil {
			return err
		}
		r.mu.Lock()
		record := r.transfers[id]
		if record == nil {
			r.mu.Unlock()
			return qblock.ErrUnknownTransfer
		}
		request, err := r.newControlRequest(record, token, block)
		r.mu.Unlock()
		if err != nil {
			return err
		}
		err = r.cc.session.WriteMessage(request)
		r.cc.ReleaseMessage(request)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *qblockReceiver) bindControlToken(id qblock.TransferID) (message.Token, error) {
	for range 32 {
		token, err := r.cc.getToken()
		if err != nil {
			return nil, err
		}
		if len(token) == 0 {
			continue
		}
		if _, ok := r.cc.tokenHandlerContainer.Load(token.Hash()); ok {
			continue
		}
		r.mu.Lock()
		record := r.transfers[id]
		if record == nil {
			r.mu.Unlock()
			return nil, qblock.ErrUnknownTransfer
		}
		err = r.manager.BindToken(id, token)
		if err == nil {
			record.tokens[string(token)] = struct{}{}
			r.transferByToken[string(token)] = id
		}
		r.mu.Unlock()
		if err == nil {
			return message.Token(bytes.Clone(token)), nil
		}
		if errors.Is(err, qblock.ErrTokenInUse) {
			continue
		}
		return nil, err
	}
	return nil, errQBlockControlToken
}

func (r *qblockReceiver) newControlRequest(record *qblockTransfer, token message.Token, block qblock.Block) (*pool.Message, error) {
	value, err := qblock.EncodeBlock(block)
	if err != nil {
		return nil, err
	}
	request := r.cc.AcquireMessage(r.cc.Context())
	request.ResetOptionsTo(record.requestOptions)
	request.Remove(message.QBlock2)
	request.Remove(message.ETag)
	request.Remove(message.Observe)
	request.Remove(message.Size2)
	request.SetCode(codes.GET)
	request.SetToken(token)
	request.SetType(message.NonConfirmable)
	request.SetMessageID(r.cc.GetMessageID())
	request.SetOptionUint32(message.QBlock2, value)
	return request, nil
}

func (r *qblockReceiver) cancel(id qblock.TransferID, err error) {
	r.mu.Lock()
	outputs := r.manager.Cancel(id, err)
	r.mu.Unlock()
	r.processOutputs(id, outputs)
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

// qblockResponseOptions copies the response metadata that an ordinary CoAP
// response would carry. Q-Block2 and Size2 describe wire fragments rather than
// the synthesized complete representation, so they must not escape to the
// original request handler.
func qblockResponseOptions(msg *pool.Message) (message.Options, error) {
	options, err := msg.Options().Clone()
	if err != nil {
		return nil, err
	}
	options = options.Remove(message.QBlock2)
	options = options.Remove(message.Size2)
	return options, nil
}

func fragmentFromQ2(msg *pool.Message, operation qblock.OperationKey, previous *qblock.Metadata) (qblock.Fragment, qblock.Metadata, error) {
	if msg.Code() != codes.Content {
		return qblock.Fragment{}, qblock.Metadata{}, errors.New("q-block response must be 2.05 Content")
	}
	if msg.HasOption(message.QBlock1) {
		return qblock.Fragment{}, qblock.Metadata{}, errQBlockMixedResponseOptions
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
