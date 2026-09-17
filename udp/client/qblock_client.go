package client

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
)

var (
	errInvalidQBlockClientConfig  = errors.New("invalid q-block client configuration")
	errQBlockControlToken         = errors.New("cannot allocate q-block control token")
	errQBlockMixedResponseOptions = errors.New("q-block response cannot mix QBlock1 and QBlock2")
)

type qblockClientConfig struct {
	Manager       qblock.ManagerConfig
	Now           func() time.Time
	Jitter        func() float64
	GetRequestTag func() (message.Token, error)
}

type qblockExchange struct {
	originalToken message.Token
	requestCode   codes.Code
	requestOpts   message.Options
	requestTag    []byte
	fail          func(error)
	transfers     map[qblock.TransferID]struct{}
	finished      bool
}

type qblockTransfer struct {
	id               qblock.TransferID
	exchange         *qblockExchange
	kind             qblock.Kind
	operation        qblock.OperationKey
	metadata         qblock.Metadata
	requestTag       []byte
	initialToken     message.Token
	initialTokenUsed bool
	tokens           map[string]message.Token
	mids             map[int32]struct{}
	responseOptions  message.Options
	responseCode     codes.Code
}

type qblockClient struct {
	cc                       *Conn
	now                      func() time.Time
	jitter                   func() float64
	getRequestTag            func() (message.Token, error)
	mu                       sync.Mutex
	actionMu                 sync.Mutex
	actionMuContention       func()
	manager                  *qblock.Manager
	initErr                  error
	exchangesByOriginalToken map[string]*qblockExchange
	exchangeByTransfer       map[qblock.TransferID]*qblockExchange
	transfers                map[qblock.TransferID]*qblockTransfer
	transferByToken          map[string]*qblockTransfer
	transferByMID            map[int32]*qblockTransfer
}

func withQBlockClient(cfg qblockClientConfig) Option {
	return func(opts *ConnOptions) {
		opts.createQBlockClient = func(cc *Conn) *qblockClient {
			return newQBlockClient(cc, cfg)
		}
	}
}

func newQBlockClient(cc *Conn, cfg qblockClientConfig) *qblockClient {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	jitter := cfg.Jitter
	if jitter == nil {
		jitter = func() float64 { return 0 }
	}
	getRequestTag := cfg.GetRequestTag
	if getRequestTag == nil {
		getRequestTag = cc.getToken
	}
	manager, err := qblock.NewManager(cfg.Manager)
	if err != nil {
		err = errors.Join(errInvalidQBlockClientConfig, err)
	}
	return &qblockClient{
		cc:                       cc,
		now:                      now,
		jitter:                   jitter,
		getRequestTag:            getRequestTag,
		manager:                  manager,
		initErr:                  err,
		exchangesByOriginalToken: make(map[string]*qblockExchange),
		exchangeByTransfer:       make(map[qblock.TransferID]*qblockExchange),
		transfers:                make(map[qblock.TransferID]*qblockTransfer),
		transferByToken:          make(map[string]*qblockTransfer),
		transferByMID:            make(map[int32]*qblockTransfer),
	}
}

func q1Operation(token, tag message.Token) (qblock.OperationKey, error) {
	return qblock.NewOperationKey([]byte("q1"), token, tag)
}

func q2Operation(token, etag message.Token) (qblock.OperationKey, error) {
	return qblock.NewOperationKey([]byte("q2"), token, etag)
}

func (c *qblockClient) canPrepare(req *pool.Message) bool {
	return c.initErr == nil && req.Code() == codes.GET && !req.HasOption(message.Observe) && req.Body() == nil && !req.HasOption(message.QBlock1) && !req.HasOption(message.QBlock2)
}

func (c *qblockClient) canPrepareQ1(req *pool.Message) bool {
	if c.initErr != nil || (req.Code() != codes.POST && req.Code() != codes.PUT) || req.Body() == nil {
		return false
	}
	if req.HasOption(message.Observe) || req.HasOption(message.QBlock1) || req.HasOption(message.QBlock2) || req.HasOption(message.Block1) || req.HasOption(message.Block2) {
		return false
	}
	controlMessage := req.ControlMessage()
	if controlMessage != nil && controlMessage.Dst.IsMulticast() {
		return false
	}
	remoteAddress, ok := c.cc.session.RemoteAddr().(*net.UDPAddr)
	return !ok || remoteAddress == nil || !remoteAddress.IP.IsMulticast()
}

func (c *qblockClient) prepare(req *pool.Message, fail func(error)) (bool, error) {
	if c.initErr != nil {
		return false, c.initErr
	}
	if c.canPrepareQ1(req) {
		return c.prepareQ1(req, fail)
	}
	if !c.canPrepare(req) {
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
	value, err := qblock.EncodeBlock(qblock.Block{Number: 0, More: true, SZX: c.cc.blockwiseSZX})
	if err != nil {
		return false, err
	}
	exchange := &qblockExchange{
		originalToken: message.Token(bytes.Clone(token)),
		requestCode:   req.Code(),
		requestOpts:   options,
		fail:          fail,
		transfers:     make(map[qblock.TransferID]struct{}),
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.exchangesByOriginalToken[string(token)]; ok {
		return false, errors.New("q-block GET token already pending")
	}
	req.SetType(message.NonConfirmable)
	req.SetOptionUint32(message.QBlock2, value)
	c.exchangesByOriginalToken[string(token)] = exchange
	return true, nil
}

func (c *qblockClient) prepareQ1(req *pool.Message, fail func(error)) (bool, error) {
	if c.initErr != nil {
		return false, c.initErr
	}
	if !c.canPrepareQ1(req) {
		return false, nil
	}
	originalToken := req.Token()
	if len(originalToken) == 0 {
		return false, errors.New("q-block Q1 requires token")
	}
	options, err := req.Options().Clone()
	if err != nil {
		return false, err
	}
	body, err := copyQBlockBody(req.Body())
	if err != nil {
		return false, err
	}
	requestTag, err := c.getRequestTag()
	if err != nil {
		return false, err
	}
	requestTag = message.Token(bytes.Clone(requestTag))
	if len(requestTag) == 0 || len(requestTag) > 8 {
		return false, errors.New("q-block Request-Tag must contain one to eight bytes")
	}
	initialToken, err := c.cc.claimFreshQBlockToken()
	if err != nil {
		return false, err
	}
	exchange := &qblockExchange{
		originalToken: message.Token(bytes.Clone(originalToken)),
		requestCode:   req.Code(),
		requestOpts:   options,
		requestTag:    bytes.Clone(requestTag),
		fail:          fail,
		transfers:     make(map[qblock.TransferID]struct{}),
	}

	c.mu.Lock()
	if _, ok := c.exchangesByOriginalToken[string(originalToken)]; ok {
		c.mu.Unlock()
		c.cc.releaseToken(initialToken, tokenOwnerQBlock)
		return false, errors.New("q-block request token already pending")
	}
	outputs, err := c.startQ1Locked(exchange, body, initialToken)
	if err != nil {
		c.mu.Unlock()
		c.cc.releaseToken(initialToken, tokenOwnerQBlock)
		return false, err
	}
	c.mu.Unlock()
	c.drive(outputs)
	return true, nil
}

func copyQBlockBody(body io.ReadSeeker) (payload []byte, err error) {
	position, err := body.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	if _, err = body.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	defer func() {
		if _, restoreErr := body.Seek(position, io.SeekStart); restoreErr != nil {
			err = errors.Join(err, restoreErr)
		}
	}()
	payload, err = io.ReadAll(body)
	return payload, err
}

func (c *qblockClient) startQ1Locked(exchange *qblockExchange, body []byte, initialToken message.Token) ([]qblock.Output, error) {
	if uint64(len(body)) > uint64(^uint32(0)) {
		return nil, errors.New("q-block body is too large")
	}
	operation, err := q1Operation(exchange.originalToken, exchange.requestTag)
	if err != nil {
		return nil, err
	}
	metadata := qblock.Metadata{
		Size:     uint32(len(body)),
		SZX:      c.cc.blockwiseSZX,
		Identity: bytes.Clone(exchange.requestTag),
	}
	if exchange.requestOpts.HasOption(message.ContentFormat) {
		contentFormat, err := exchange.requestOpts.ContentFormat()
		if err != nil {
			return nil, err
		}
		metadata.HasContentFormat = true
		metadata.ContentFormat = contentFormat
	}
	outputs, err := c.manager.StartSender(operation, initialToken, qblock.Q1, metadata, body, c.now(), c.jitter())
	if err != nil {
		return nil, err
	}
	id, ok := c.manager.TransferID(operation)
	if !ok {
		return nil, qblock.ErrUnknownTransfer
	}
	transfer := &qblockTransfer{
		id:           id,
		exchange:     exchange,
		kind:         qblock.Q1,
		operation:    operation,
		metadata:     metadata,
		requestTag:   bytes.Clone(exchange.requestTag),
		initialToken: message.Token(bytes.Clone(initialToken)),
		tokens:       map[string]message.Token{string(initialToken): message.Token(bytes.Clone(initialToken))},
		mids:         make(map[int32]struct{}),
	}
	exchange.transfers[id] = struct{}{}
	c.exchangesByOriginalToken[string(exchange.originalToken)] = exchange
	c.exchangeByTransfer[id] = exchange
	c.transfers[id] = transfer
	c.transferByToken[string(initialToken)] = transfer
	return outputs, nil
}

func (c *qblockClient) active() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.manager == nil {
		return 0
	}
	return c.manager.Active()
}

func (c *qblockClient) Tick(now time.Time) {
	c.mu.Lock()
	if c.manager == nil {
		c.mu.Unlock()
		return
	}
	outputs := c.manager.Tick(now)
	c.mu.Unlock()
	c.drive(outputs)
}

func (c *qblockClient) abandon(token message.Token, err error) {
	if err == nil {
		err = qblock.ErrCanceled
	}
	var outputs []qblock.Output
	c.mu.Lock()
	key := string(token)
	if transfer := c.transferByToken[key]; transfer != nil {
		outputs = c.manager.Cancel(transfer.id, err)
	} else if exchange := c.exchangesByOriginalToken[key]; exchange != nil {
		if len(exchange.transfers) == 0 {
			delete(c.exchangesByOriginalToken, key)
		} else {
			for id := range exchange.transfers {
				outputs = append(outputs, c.manager.Cancel(id, err)...)
			}
		}
	}
	c.mu.Unlock()
	c.drive(outputs)
}

func (c *qblockClient) close() {
	var pending []*qblockExchange
	var outputs []qblock.Output
	c.mu.Lock()
	for key, exchange := range c.exchangesByOriginalToken {
		if len(exchange.transfers) == 0 {
			delete(c.exchangesByOriginalToken, key)
			pending = append(pending, exchange)
		}
	}
	if c.manager != nil {
		for id := range c.transfers {
			outputs = append(outputs, c.manager.Cancel(id, qblock.ErrClosed)...)
		}
	}
	c.mu.Unlock()
	c.drive(outputs)
	for _, exchange := range pending {
		_, _ = c.cc.tokenHandlerContainer.LoadAndDelete(exchange.originalToken.Hash())
		exchange.fail(qblock.ErrClosed)
	}
}

func (c *qblockClient) handle(msg *pool.Message) bool {
	if !msg.HasOption(message.QBlock2) {
		return false
	}
	c.mu.Lock()
	token := msg.Token()
	if transfer := c.transferByToken[string(token)]; transfer != nil {
		fragment, _, err := fragmentFromQ2(msg, transfer.operation, &transfer.metadata)
		if err != nil {
			outputs := c.manager.Cancel(transfer.id, err)
			c.mu.Unlock()
			c.drive(outputs)
			return true
		}
		outputs, err := c.manager.Receive(fragment, c.now())
		if err != nil {
			outputs = c.manager.Cancel(transfer.id, err)
			c.mu.Unlock()
			c.drive(outputs)
			return true
		}
		c.mu.Unlock()
		c.drive(outputs)
		return true
	}
	exchange, ok := c.exchangesByOriginalToken[string(token)]
	if !ok {
		c.mu.Unlock()
		return true
	}
	etag, err := qblockETag(msg)
	if err != nil {
		c.mu.Unlock()
		return true
	}
	operation, err := q2Operation(exchange.originalToken, etag)
	if err != nil {
		c.mu.Unlock()
		return true
	}
	fragment, metadata, err := fragmentFromQ2(msg, operation, nil)
	if err != nil {
		c.mu.Unlock()
		return true
	}
	responseOptions, err := qblockResponseOptions(msg)
	if err != nil {
		c.mu.Unlock()
		return true
	}
	outputs, err := c.manager.StartReceiver(fragment, c.now())
	if err != nil {
		c.mu.Unlock()
		return true
	}
	id, ok := c.manager.TransferID(operation)
	if !ok && len(outputs) > 0 {
		id = outputs[0].TransferID
		ok = true
	}
	if !ok {
		c.mu.Unlock()
		return true
	}
	transfer := &qblockTransfer{
		id:               id,
		exchange:         exchange,
		kind:             qblock.Q2,
		operation:        operation,
		metadata:         metadata,
		initialToken:     message.Token(bytes.Clone(token)),
		initialTokenUsed: true,
		tokens:           map[string]message.Token{string(token): message.Token(bytes.Clone(token))},
		mids:             make(map[int32]struct{}),
		responseOptions:  responseOptions,
		responseCode:     msg.Code(),
	}
	exchange.transfers[id] = struct{}{}
	c.exchangeByTransfer[id] = exchange
	c.transfers[id] = transfer
	c.transferByToken[string(token)] = transfer
	c.mu.Unlock()
	c.drive(outputs)
	return true
}

func (c *qblockClient) drive(outputs []qblock.Output) {
	if !c.actionMu.TryLock() {
		if c.actionMuContention != nil {
			c.actionMuContention()
		}
		c.actionMu.Lock()
	}
	callbacks := c.executeOrdered(outputs)
	c.actionMu.Unlock()
	for _, callback := range callbacks {
		callback()
	}
}

func (c *qblockClient) executeOrdered(outputs []qblock.Output) []func() {
	var callbacks []func()
	for _, output := range outputs {
		callbacks = append(callbacks, c.executeOutput(output)...)
	}
	return callbacks
}

func (c *qblockClient) executeOutput(output qblock.Output) []func() {
	c.mu.Lock()
	transfer := c.transfers[output.TransferID]
	if transfer == nil || transfer.operation != output.Operation {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	switch output.Action.Kind {
	case qblock.SendBlock:
		if err := c.writeQ1Block(output.TransferID, output.Action); err != nil {
			return c.executeOrdered(c.cancelTransfer(output.TransferID, err))
		}
	case qblock.SendContinue, qblock.RequestMissing:
		if err := c.writeQ2Control(output.TransferID, output.Action); err != nil {
			return c.executeOrdered(c.cancelTransfer(output.TransferID, err))
		}
	case qblock.Deliver:
		return c.prepareDelivery(output.TransferID, output.Action.Payload)
	case qblock.Complete:
		if output.Action.Err != nil {
			return c.prepareFailure(output.TransferID, output.Action.Err)
		}
	case qblock.Release:
		c.mu.Lock()
		c.cleanupTransferLocked(output.TransferID)
		c.mu.Unlock()
	}
	return nil
}

func (c *qblockClient) writeQ1Block(id qblock.TransferID, action qblock.Action) error {
	c.mu.Lock()
	transfer := c.transfers[id]
	if transfer == nil || transfer.kind != qblock.Q1 {
		c.mu.Unlock()
		return qblock.ErrUnknownTransfer
	}
	var token message.Token
	if !transfer.initialTokenUsed {
		token = message.Token(bytes.Clone(transfer.initialToken))
		transfer.initialTokenUsed = true
	} else {
		var err error
		token, err = c.bindQ1TokenLocked(id)
		if err != nil {
			c.mu.Unlock()
			return err
		}
	}
	request, err := c.newQ1Request(transfer, token, action)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	defer c.cc.ReleaseMessage(request)

	return c.cc.session.WriteMessage(request)
}

func (c *qblockClient) bindQ1TokenLocked(id qblock.TransferID) (message.Token, error) {
	transfer := c.transfers[id]
	if transfer == nil || transfer.kind != qblock.Q1 {
		return nil, qblock.ErrUnknownTransfer
	}
	token, err := c.cc.claimFreshQBlockToken()
	if err != nil {
		return nil, err
	}
	if err := c.manager.BindToken(id, token); err != nil {
		c.cc.releaseToken(token, tokenOwnerQBlock)
		return nil, err
	}
	transfer.tokens[string(token)] = message.Token(bytes.Clone(token))
	c.transferByToken[string(token)] = transfer
	return message.Token(bytes.Clone(token)), nil
}

func (c *qblockClient) newQ1Request(transfer *qblockTransfer, token message.Token, action qblock.Action) (*pool.Message, error) {
	value, err := qblock.EncodeBlock(action.Block)
	if err != nil {
		return nil, err
	}
	request := c.cc.AcquireMessage(c.cc.Context())
	request.ResetOptionsTo(transfer.exchange.requestOpts)
	request.Remove(message.QBlock1)
	request.Remove(message.QBlock2)
	request.Remove(message.Block1)
	request.Remove(message.Block2)
	request.Remove(message.RequestTag)
	request.Remove(message.Size1)
	request.SetCode(transfer.exchange.requestCode)
	request.SetToken(token)
	request.SetType(message.NonConfirmable)
	mid := c.cc.GetMessageID()
	request.SetMessageID(mid)
	request.SetOptionBytes(message.RequestTag, transfer.requestTag)
	request.SetOptionUint32(message.Size1, transfer.metadata.Size)
	request.SetOptionUint32(message.QBlock1, value)
	request.SetBody(bytes.NewReader(bytes.Clone(action.Payload)))
	transfer.mids[mid] = struct{}{}
	c.transferByMID[mid] = transfer
	return request, nil
}

func (c *qblockClient) writeQ2Control(id qblock.TransferID, action qblock.Action) error {
	c.mu.Lock()
	transfer := c.transfers[id]
	if transfer == nil {
		c.mu.Unlock()
		return qblock.ErrUnknownTransfer
	}
	szx := transfer.metadata.SZX
	c.mu.Unlock()

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
		token, err := c.bindControlToken(id)
		if err != nil {
			return err
		}
		request, err := c.newControlRequest(id, token, block)
		if err != nil {
			return err
		}
		err = c.cc.session.WriteMessage(request)
		c.cc.ReleaseMessage(request)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *qblockClient) bindControlToken(id qblock.TransferID) (message.Token, error) {
	for range 32 {
		token, err := c.cc.getToken()
		if err != nil {
			return nil, err
		}
		token = message.Token(bytes.Clone(token))
		if len(token) == 0 {
			continue
		}
		if _, ok := c.cc.tokenHandlerContainer.Load(token.Hash()); ok {
			continue
		}
		if err := c.cc.claimToken(token, tokenOwnerQBlock); err != nil {
			continue
		}
		c.mu.Lock()
		transfer := c.transfers[id]
		if transfer == nil {
			c.mu.Unlock()
			c.cc.releaseToken(token, tokenOwnerQBlock)
			return nil, qblock.ErrUnknownTransfer
		}
		err = c.manager.BindToken(id, token)
		if err == nil {
			transfer.tokens[string(token)] = message.Token(bytes.Clone(token))
			c.transferByToken[string(token)] = transfer
		}
		c.mu.Unlock()
		if err == nil {
			return message.Token(bytes.Clone(token)), nil
		}
		c.cc.releaseToken(token, tokenOwnerQBlock)
		if errors.Is(err, qblock.ErrTokenInUse) {
			continue
		}
		return nil, err
	}
	return nil, errQBlockControlToken
}

func (c *qblockClient) newControlRequest(id qblock.TransferID, token message.Token, block qblock.Block) (*pool.Message, error) {
	value, err := qblock.EncodeBlock(block)
	if err != nil {
		return nil, err
	}
	request := c.cc.AcquireMessage(c.cc.Context())
	c.mu.Lock()
	transfer := c.transfers[id]
	if transfer == nil {
		c.mu.Unlock()
		c.cc.ReleaseMessage(request)
		return nil, qblock.ErrUnknownTransfer
	}
	request.ResetOptionsTo(transfer.exchange.requestOpts)
	request.Remove(message.QBlock2)
	request.Remove(message.ETag)
	request.Remove(message.Observe)
	request.Remove(message.Size2)
	request.SetCode(codes.GET)
	request.SetToken(token)
	request.SetType(message.NonConfirmable)
	mid := c.cc.GetMessageID()
	request.SetMessageID(mid)
	request.SetOptionUint32(message.QBlock2, value)
	transfer.mids[mid] = struct{}{}
	c.transferByMID[mid] = transfer
	c.mu.Unlock()
	return request, nil
}

func (c *qblockClient) prepareDelivery(id qblock.TransferID, payload []byte) []func() {
	c.mu.Lock()
	transfer := c.transfers[id]
	if transfer == nil || transfer.kind != qblock.Q2 {
		c.mu.Unlock()
		return nil
	}
	response := c.cc.AcquireMessage(c.cc.Context())
	response.SetCode(transfer.responseCode)
	response.SetToken(transfer.exchange.originalToken)
	response.ResetOptionsTo(transfer.responseOptions)
	response.SetBody(bytes.NewReader(payload))
	handler, ok := c.cc.tokenHandlerContainer.LoadAndDelete(transfer.exchange.originalToken.Hash())
	if !ok {
		c.cc.ReleaseMessage(response)
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	return []func(){func() { handler(nil, response) }}
}

func (c *qblockClient) prepareFailure(id qblock.TransferID, err error) []func() {
	c.mu.Lock()
	exchange := c.exchangeByTransfer[id]
	if exchange == nil || exchange.fail == nil {
		c.mu.Unlock()
		return nil
	}
	_, _ = c.cc.tokenHandlerContainer.LoadAndDelete(exchange.originalToken.Hash())
	fail := exchange.fail
	c.mu.Unlock()
	return []func(){func() { fail(err) }}
}

func (c *qblockClient) cancelTransfer(id qblock.TransferID, err error) []qblock.Output {
	c.mu.Lock()
	outputs := c.manager.Cancel(id, err)
	c.mu.Unlock()
	return outputs
}

func (c *qblockClient) cleanupTransferLocked(id qblock.TransferID) {
	transfer := c.transfers[id]
	if transfer == nil {
		return
	}
	for token := range transfer.tokens {
		delete(c.transferByToken, token)
		if token != string(transfer.exchange.originalToken) {
			c.cc.releaseToken(message.Token([]byte(token)), tokenOwnerQBlock)
		}
	}
	for mid := range transfer.mids {
		delete(c.transferByMID, mid)
	}
	delete(c.transfers, id)
	delete(c.exchangeByTransfer, id)
	if transfer.exchange != nil {
		delete(transfer.exchange.transfers, id)
		if len(transfer.exchange.transfers) == 0 {
			transfer.exchange.finished = true
			delete(c.exchangesByOriginalToken, string(transfer.exchange.originalToken))
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
