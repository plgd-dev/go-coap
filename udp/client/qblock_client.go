package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net"
	"sync"
	"time"
	"unsafe"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
)

var (
	errInvalidQBlockClientConfig  = errors.New("invalid q-block client configuration")
	errQBlockControlToken         = errors.New("cannot allocate q-block control token")
	errQBlockMixedResponseOptions = errors.New("q-block response cannot mix QBlock1 and QBlock2")
)

type qblockClientConfig struct {
	Endpoint      *qblockEndpointDomain
	MaxOwnedBytes uint64
	MaxMIDEntries uint32
	Manager       qblock.ManagerConfig
	Pacing        *qblockPacingConfig
	Now           func() time.Time
	Clock         qblockClock
	ScheduleMode  qblockScheduleMode
	Jitter        func() float64
	GetRequestTag func() (message.Token, error)
}

type qblockExchange struct {
	workID          qblockWorkID
	generation      uint64
	initialProbeKey qblockProbeKey
	getSZX          blockwise.SZX
	originalToken   message.Token
	requestCode     codes.Code
	requestOpts     message.Options
	requestTag      []byte
	fail            func(error)
	transfers       map[qblock.TransferID]struct{}
	finished        bool
	failureReported bool
	terminalErr     error
	requestContext  context.Context
	cancelContext   context.CancelFunc
	stopConnCancel  func() bool
	callbackRelease func()
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
	bodyProbeKey     qblockProbeKey
	bodyWait         time.Duration
	bodyAnswered     bool
	repairReplies    map[uint32]uint32
	expires          time.Time
	tokens           map[string]message.Token
	mids             map[int32]struct{}
	responseOptions  message.Options
	responseCode     codes.Code
	terminalResponse *pool.Message
}

type qblockClient struct {
	serverOnly               bool
	cc                       *Conn
	now                      func() time.Time
	clock                    qblockClock
	scheduleMode             qblockScheduleMode
	writeContext             context.Context
	cancelWriteContext       context.CancelFunc
	jitter                   func() float64
	getRequestTag            func() (message.Token, error)
	mu                       sync.Mutex
	actionMu                 sync.Mutex
	actionMuContention       func()
	manager                  *qblock.Manager
	managerConfig            qblock.ManagerConfig
	datagramLimit            uint32
	maxMIDEntries            uint32
	maxOwnedBytes            uint64
	ownedBudget              *qblockOwnedBudget
	pacingConfig             qblockPacingConfig
	probeGate                *qblockProbeGate
	endpoint                 *qblockEndpointMember
	endpointWake             chan struct{}
	currentProbe             *qblockProbeCorrelation
	workQueue                *qblockWorkQueue
	nextGeneration           uint64
	nextProbeKey             qblockProbeKey
	initErr                  error
	closed                   bool
	exchangesByOriginalToken map[string]*qblockExchange
	exchangeByTransfer       map[qblock.TransferID]*qblockExchange
	transfers                map[qblock.TransferID]*qblockTransfer
	transferByToken          map[string]*qblockTransfer
	transferByMID            map[int32]*qblockTransfer
	pendingGETByMID          map[int32]*qblockExchange
	pendingMessageRelease    []*pool.Message
	server                   *qblockServer
	callbackSlots            *qblockCallbackSlots
	callbackDispatcher       *qblockCallbackDispatcher
	scheduler                *qblockScheduler
}

func withQBlockClient(cfg qblockClientConfig) Option {
	return func(opts *ConnOptions) {
		opts.createQBlockClient = func(cc *Conn) *qblockClient {
			return newQBlockClient(cc, cfg)
		}
	}
}

func newQBlockClient(cc *Conn, cfg qblockClientConfig) *qblockClient {
	now, clock, configErr := qblockClientClock(cfg)
	writeContext, cancelWriteContext := context.WithCancel(cc.Context())
	jitter := cfg.Jitter
	if jitter == nil {
		jitter = func() float64 { return 0 }
	}
	getRequestTag := cfg.GetRequestTag
	if getRequestTag == nil {
		getRequestTag = cc.getToken
	}
	if cfg.MaxMIDEntries == 0 {
		cfg.MaxMIDEntries = 65536
	}
	manager, err := qblock.NewManager(cfg.Manager)
	if cfg.MaxMIDEntries > 65536 {
		err = errors.Join(err, errInvalidQBlockClientConfig)
	}
	pacing, pacingErr := normalizeQBlockPacingConfig(cfg.Pacing, cfg.Manager)
	err = errors.Join(err, pacingErr)
	if configErr != nil {
		err = errors.Join(err, configErr)
	}
	if err != nil {
		err = errors.Join(errInvalidQBlockClientConfig, err)
	}
	client := &qblockClient{
		cc:                       cc,
		now:                      now,
		clock:                    clock,
		scheduleMode:             cfg.ScheduleMode,
		writeContext:             writeContext,
		cancelWriteContext:       cancelWriteContext,
		jitter:                   jitter,
		getRequestTag:            getRequestTag,
		manager:                  manager,
		managerConfig:            cfg.Manager,
		maxMIDEntries:            cfg.MaxMIDEntries,
		maxOwnedBytes:            cfg.MaxOwnedBytes,
		pacingConfig:             pacing,
		probeGate:                newQBlockProbeGate(pacing.ProbingRate),
		workQueue:                newQBlockWorkQueue(cfg.Manager.MaxTransfers, pacing.MaxIntentBytes),
		initErr:                  err,
		exchangesByOriginalToken: make(map[string]*qblockExchange),
		exchangeByTransfer:       make(map[qblock.TransferID]*qblockExchange),
		transfers:                make(map[qblock.TransferID]*qblockTransfer),
		transferByToken:          make(map[string]*qblockTransfer),
		transferByMID:            make(map[int32]*qblockTransfer),
		pendingGETByMID:          make(map[int32]*qblockExchange),
		callbackSlots:            newQBlockCallbackSlots(cfg.Manager.MaxTransfers),
	}
	if cfg.Endpoint != nil {
		client.endpointWake = make(chan struct{}, 1)
		member, attachErr := cfg.Endpoint.attach(cc.RemoteAddr(), client.endpointWake, cfg.Endpoint.clock.Now())
		client.initErr = errors.Join(client.initErr, attachErr)
		client.endpoint = member
		client.workQueue.onClear = client.withdrawPending
		client.clock = cfg.Endpoint.clock
		client.now = cfg.Endpoint.clock.Now
		if pacing.ProbingRate != cfg.Endpoint.rate {
			client.initErr = errors.Join(client.initErr, errInvalidQBlockClientConfig)
		}
	}
	return client
}

func qblockClientClock(cfg qblockClientConfig) (func() time.Time, qblockClock, error) {
	if cfg.Clock != nil && cfg.Now != nil {
		return time.Now, nil, errInvalidQBlockClientConfig
	}
	switch cfg.ScheduleMode {
	case qblockScheduleManual:
		if cfg.Clock != nil {
			return cfg.Clock.Now, cfg.Clock, nil
		}
		if cfg.Now != nil {
			return cfg.Now, nil, nil
		}
		return time.Now, nil, nil
	case qblockScheduleAutomatic:
		if cfg.Clock == nil {
			return time.Now, nil, errInvalidQBlockClientConfig
		}
		return cfg.Clock.Now, cfg.Clock, nil
	default:
		return time.Now, nil, errInvalidQBlockClientConfig
	}
}

func q1Operation(token, tag message.Token) (qblock.OperationKey, error) {
	return qblock.NewOperationKey([]byte("q1"), token, tag)
}

func q2Operation(token, etag message.Token) (qblock.OperationKey, error) {
	return qblock.NewOperationKey([]byte("q2"), token, etag)
}

func (c *qblockClient) canPrepare(req *pool.Message) bool {
	return !c.serverOnly && c.initErr == nil && req.Code() == codes.GET && !req.HasOption(message.Observe) && req.Body() == nil && !req.HasOption(message.QBlock1) && !req.HasOption(message.QBlock2) && !req.HasOption(message.Block1) && !req.HasOption(message.Block2)
}

func (c *qblockClient) canPrepareQ1(req *pool.Message) bool {
	if c.serverOnly || c.initErr != nil || (req.Code() != codes.POST && req.Code() != codes.PUT) || req.Body() == nil {
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

type qblockPreparation struct {
	Prepared         bool
	OwnsTransmission bool
}

func (c *qblockClient) prepare(req *pool.Message, fail func(error)) (qblockPreparation, error) {
	if c.initErr != nil {
		return qblockPreparation{}, c.initErr
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return qblockPreparation{}, qblock.ErrClosed
	}
	if c.canPrepareQ1(req) {
		return c.prepareQ1(req, fail)
	}
	if !c.canPrepare(req) {
		return qblockPreparation{}, nil
	}
	token := req.Token()
	if len(token) == 0 {
		return qblockPreparation{}, errors.New("q-block GET requires token")
	}
	req.SetType(message.NonConfirmable)
	if err := c.preflightOwnedOptions(req.Options()); err != nil {
		return qblockPreparation{}, err
	}
	callbackRelease, preparationRelease, err := c.acquireExchangeCapacity()
	if err != nil {
		return qblockPreparation{}, err
	}
	defer preparationRelease()
	published := false
	defer func() {
		if !published {
			callbackRelease()
		}
	}()
	szx, err := c.selectGETSZX()
	if err != nil {
		return qblockPreparation{}, err
	}
	value, err := qblock.EncodeBlock(qblock.Block{Number: 0, More: true, SZX: szx})
	if err != nil {
		return qblockPreparation{}, err
	}
	req.SetOptionUint32(message.QBlock2, value)
	tag, err := req.GetOptionBytes(message.RequestTag)
	if err != nil {
		tag = token
	}
	if len(tag) == 0 || len(tag) > 8 {
		return qblockPreparation{}, message.ErrInvalidValueLength
	}
	tag = cloneQBlockBytes(tag)
	req.SetOptionBytes(message.RequestTag, tag)
	size, err := qblockGETSize(req)
	if err != nil {
		return qblockPreparation{}, err
	}
	if size > uint64(c.datagramLimit) {
		return qblockPreparation{}, qblock.ErrLimitExceeded
	}
	options := cloneQBlockOptions(req.Options())
	// Remove mutates the option array; keep the initial GET advertisement
	// separate from the immutable exchange snapshot used by later controls.
	snapshotOptions := cloneQBlockOptions(options)
	snapshotOptions = cloneQBlockOptions(snapshotOptions.Remove(message.QBlock2))
	capacity, err := qblockClientSnapshotCapacity(options, token, nil, c.managerConfig.Transfer.MaxPayloads)
	if err != nil {
		return qblockPreparation{}, err
	}
	expires := c.now().Add(c.managerConfig.Transfer.Lifetime)
	if deadline, ok := req.Context().Deadline(); ok && deadline.Before(expires) {
		expires = deadline
	}
	exchange := &qblockExchange{
		originalToken: message.Token(cloneQBlockBytes(token)),
		requestCode:   req.Code(),
		getSZX:        szx,
		requestTag:    cloneQBlockBytes(tag),
		requestOpts:   snapshotOptions,
		fail:          fail,
		transfers:     make(map[qblock.TransferID]struct{}),
	}
	exchange.callbackRelease = callbackRelease
	exchange.requestContext, exchange.cancelContext = context.WithCancel(req.Context())
	exchange.stopConnCancel = context.AfterFunc(c.cc.Context(), exchange.cancelContext)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		exchange.closeRequestContext()
		exchange.releaseCallbackSlot()
		return qblockPreparation{}, qblock.ErrClosed
	}
	if _, ok := c.exchangesByOriginalToken[string(token)]; ok {
		c.mu.Unlock()
		exchange.closeRequestContext()
		exchange.releaseCallbackSlot()
		return qblockPreparation{}, errors.New("q-block GET token already pending")
	}
	if c.nextGeneration == math.MaxUint64 || c.nextProbeKey == qblockProbeKey(math.MaxUint64) {
		c.mu.Unlock()
		exchange.closeRequestContext()
		exchange.releaseCallbackSlot()
		return qblockPreparation{}, qblock.ErrLimitExceeded
	}
	workID, err := c.workQueue.reserve(capacity)
	if err != nil {
		c.mu.Unlock()
		exchange.closeRequestContext()
		exchange.releaseCallbackSlot()
		return qblockPreparation{}, err
	}
	generation := c.nextGeneration + 1
	probeKey := c.nextProbeKey + 1
	err = c.workQueue.replace(workID, qblockPendingWork{
		Kind: qblockWorkGET, Generation: generation, Expires: expires,
		ProbeKey: probeKey, RequestCode: req.Code(), RequestOptions: options,
		RequestToken: token,
	}, false)
	if err != nil {
		c.releasePacingWorkLocked(workID)
		c.mu.Unlock()
		exchange.closeRequestContext()
		exchange.releaseCallbackSlot()
		return qblockPreparation{}, err
	}
	c.nextGeneration = generation
	c.nextProbeKey = probeKey
	exchange.workID = workID
	exchange.generation = generation
	exchange.initialProbeKey = probeKey
	published = true
	c.exchangesByOriginalToken[string(token)] = exchange
	c.mu.Unlock()
	c.notifyDeadlineChanged()
	return qblockPreparation{Prepared: true, OwnsTransmission: true}, nil
}

func (c *qblockClient) prepareQ1(req *pool.Message, fail func(error)) (qblockPreparation, error) {
	if c.initErr != nil {
		return qblockPreparation{}, c.initErr
	}
	if !c.canPrepareQ1(req) {
		return qblockPreparation{}, nil
	}
	originalToken := req.Token()
	if len(originalToken) == 0 {
		return qblockPreparation{}, errors.New("q-block Q1 requires token")
	}
	if err := c.preflightOwnedOptions(req.Options()); err != nil {
		return qblockPreparation{}, err
	}
	callbackRelease, preparationRelease, err := c.acquireExchangeCapacity()
	if err != nil {
		return qblockPreparation{}, err
	}
	defer preparationRelease()
	admitted := false
	defer func() {
		if !admitted {
			callbackRelease()
		}
	}()
	responseSZX, err := c.selectGETSZX()
	if err != nil {
		return qblockPreparation{}, err
	}
	options := cloneQBlockOptions(req.Options())
	body, err := copyQBlockBody(req.Body(), c.managerConfig.Transfer.MaxBodySize)
	if err != nil {
		return qblockPreparation{}, err
	}
	requestTag, err := c.getRequestTag()
	if err != nil {
		return qblockPreparation{}, err
	}
	requestTag = message.Token(bytes.Clone(requestTag))
	if len(requestTag) == 0 || len(requestTag) > 8 {
		return qblockPreparation{}, errors.New("q-block Request-Tag must contain one to eight bytes")
	}
	initialToken, err := c.cc.claimFreshQBlockToken()
	if err != nil {
		callbackRelease()
		return qblockPreparation{}, err
	}
	requestContext, cancelContext := context.WithCancel(req.Context())
	exchange := &qblockExchange{
		getSZX:          responseSZX,
		originalToken:   message.Token(cloneQBlockBytes(originalToken)),
		requestCode:     req.Code(),
		requestOpts:     options,
		requestTag:      cloneQBlockBytes(requestTag),
		fail:            fail,
		transfers:       make(map[qblock.TransferID]struct{}),
		requestContext:  requestContext,
		cancelContext:   cancelContext,
		callbackRelease: callbackRelease,
	}
	exchange.stopConnCancel = context.AfterFunc(c.cc.Context(), cancelContext)

	c.lockAction()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		c.actionMu.Unlock()
		exchange.closeRequestContext()
		exchange.releaseCallbackSlot()
		c.cc.releaseToken(initialToken, tokenOwnerQBlock)
		return qblockPreparation{}, qblock.ErrClosed
	}
	if _, ok := c.exchangesByOriginalToken[string(originalToken)]; ok {
		c.mu.Unlock()
		c.actionMu.Unlock()
		exchange.closeRequestContext()
		exchange.releaseCallbackSlot()
		c.cc.releaseToken(initialToken, tokenOwnerQBlock)
		return qblockPreparation{}, errors.New("q-block request token already pending")
	}
	err = c.startQ1Locked(exchange, body, initialToken)
	if err != nil {
		c.mu.Unlock()
		c.actionMu.Unlock()
		exchange.closeRequestContext()
		exchange.releaseCallbackSlot()
		c.cc.releaseToken(initialToken, tokenOwnerQBlock)
		return qblockPreparation{}, err
	}
	admitted = true
	c.mu.Unlock()
	callbacks := c.executePendingOrdered(c.now())
	c.actionMu.Unlock()
	for _, callback := range callbacks {
		callback.run()
	}
	c.notifyDeadlineChanged()
	return qblockPreparation{Prepared: true, OwnsTransmission: true}, nil
}

func (e *qblockExchange) closeRequestContext() {
	if e.stopConnCancel != nil {
		e.stopConnCancel()
		e.stopConnCancel = nil
	}
	if e.cancelContext != nil {
		e.cancelContext()
		e.cancelContext = nil
	}
}

func (e *qblockExchange) releaseCallbackSlot() {
	if e.callbackRelease != nil {
		e.callbackRelease()
	}
}

func copyQBlockBody(body io.ReadSeeker, limit uint32) (payload []byte, err error) {
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
	payload, err = readQBlockBody(body, limit)
	return payload, err
}

func (c *qblockClient) startQ1Locked(exchange *qblockExchange, body []byte, initialToken message.Token) error {
	if uint64(len(body)) > uint64(^uint32(0)) {
		return errors.New("q-block body is too large")
	}
	operation, err := q1Operation(exchange.originalToken, exchange.requestTag)
	if err != nil {
		return err
	}
	metadata := qblock.Metadata{
		Size:     uint32(len(body)),
		SZX:      c.cc.blockwiseSZX,
		Identity: bytes.Clone(exchange.requestTag),
	}
	template := c.cc.AcquireMessage(c.cc.Context())
	template.ResetOptionsTo(exchange.requestOpts)
	template.Remove(message.Size1)
	template.Remove(message.RequestTag)
	template.SetOptionBytes(message.RequestTag, exchange.requestTag)
	template.SetOptionUint32(message.Size1, metadata.Size)
	responseHint, err := qblock.EncodeBlock(qblock.Block{More: true, SZX: exchange.getSZX})
	if err != nil {
		c.cc.ReleaseMessage(template)
		return err
	}
	template.SetOptionUint32(message.QBlock2, responseHint)
	metadata.SZX, err = c.selectBodySZX(template, message.QBlock1, metadata.Size, metadata.SZX)
	c.cc.ReleaseMessage(template)
	if err != nil {
		return err
	}
	if exchange.requestOpts.HasOption(message.ContentFormat) {
		contentFormat, err := exchange.requestOpts.ContentFormat()
		if err != nil {
			return err
		}
		metadata.HasContentFormat = true
		metadata.ContentFormat = contentFormat
	}
	capacity, err := qblockClientSnapshotCapacity(exchange.requestOpts, exchange.originalToken, exchange.requestTag, c.managerConfig.Transfer.MaxPayloads)
	if err != nil {
		return err
	}
	if c.nextGeneration == math.MaxUint64 || c.nextProbeKey == qblockProbeKey(math.MaxUint64) {
		return qblock.ErrLimitExceeded
	}
	now := c.now()
	jitter := c.jitter()
	wait, err := qblockProbingWait(c.pacingConfig, c.managerConfig.Transfer, jitter)
	if err != nil {
		return err
	}
	workID, err := c.workQueue.reserve(capacity)
	if err != nil {
		return err
	}
	id, err := c.manager.PrepareSender(operation, initialToken, qblock.Q1, metadata, body, now, jitter)
	if err != nil {
		c.releasePacingWorkLocked(workID)
		return err
	}
	generation := c.nextGeneration + 1
	probeKey := c.nextProbeKey + 1
	err = c.workQueue.replace(workID, qblockPendingWork{
		Kind: qblockWorkBody, Operation: operation, TransferID: id,
		Generation: generation, Expires: now.Add(c.managerConfig.Transfer.Lifetime),
		ProbeKey: probeKey, NonProbingWait: wait,
	}, false)
	if err != nil {
		c.manager.Cancel(id, err)
		c.releasePacingWorkLocked(workID)
		return err
	}
	c.nextGeneration = generation
	c.nextProbeKey = probeKey
	exchange.workID = workID
	exchange.generation = generation
	transfer := &qblockTransfer{
		id:           id,
		exchange:     exchange,
		kind:         qblock.Q1,
		operation:    operation,
		metadata:     metadata,
		requestTag:   bytes.Clone(exchange.requestTag),
		initialToken: message.Token(bytes.Clone(initialToken)),
		bodyProbeKey: probeKey,
		bodyWait:     wait,
		tokens:       map[string]message.Token{string(initialToken): message.Token(bytes.Clone(initialToken))},
		mids:         make(map[int32]struct{}),
	}
	exchange.transfers[id] = struct{}{}
	c.exchangesByOriginalToken[string(exchange.originalToken)] = exchange
	c.exchangeByTransfer[id] = exchange
	c.transfers[id] = transfer
	c.transferByToken[string(initialToken)] = transfer
	return nil
}

func (c *qblockClient) active() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.manager == nil {
		return 0
	}
	return c.manager.Active()
}

// nextDeadlineLocked returns the earliest active transfer or completed-server
// record deadline. The caller holds c.mu.
func (c *qblockClient) nextDeadlineLocked() (time.Time, bool) {
	if c.manager == nil {
		return time.Time{}, false
	}
	deadline, ok := c.manager.NextDeadline()
	if c.workQueue != nil {
		now := c.now()
		c.clearExpiredPacingProbeLocked(now)
		workDeadline, workOK := c.workQueue.nextDeadline(now, c.gate())
		if workOK && (!ok || workDeadline.Before(deadline)) {
			deadline, ok = workDeadline, true
		}
	}
	if c.server == nil {
		return deadline, ok
	}
	recordDeadline, recordOK := c.server.nextRecordDeadlineLocked()
	if recordOK && (!ok || recordDeadline.Before(deadline)) {
		return recordDeadline, true
	}
	return deadline, ok
}

func (c *qblockClient) Tick(now time.Time) {
	if c.automaticScheduling() {
		c.notifyDeadlineChanged()
		return
	}
	c.advanceDue(now)
}

func (c *qblockClient) advanceDue(now time.Time) {
	c.advanceDueWithCallbacks(now, false)
}

func (c *qblockClient) advanceDueWithCallbacks(now time.Time, scheduled bool) {
	c.lockAction()
	if scheduled {
		// A worker can wait behind a blocked packet write for longer than a
		// transfer's lifetime. Sample the automatic clock after taking the gate.
		now = c.clock.Now()
	}
	c.mu.Lock()
	if c.manager == nil || c.closed {
		c.mu.Unlock()
		c.actionMu.Unlock()
		return
	}
	outputs := c.manager.Tick(now)
	for id, transfer := range c.transfers {
		if transfer.kind == qblock.Q2 {
			if err := c.syncPacingControlsLocked(id, now); err != nil {
				outputs = append(outputs, c.manager.Cancel(id, err)...)
			}
		}
	}
	if c.server != nil {
		for _, record := range c.server.byID {
			if record.activeOperation == record.operation {
				if err := c.server.syncControlsLocked(record, now); err != nil {
					outputs = append(outputs, c.manager.Cancel(record.id, err)...)
				}
			}
		}
		c.server.expireRecordsLocked(now)
	}
	var conCallbacks []qblockCallback
	if c.server != nil {
		conCallbacks = c.server.dueCON(now)
	}
	c.mu.Unlock()
	callbacks := c.executeOrdered(outputs)
	callbacks = append(callbacks, conCallbacks...)
	callbacks = append(callbacks, c.executePendingOrdered(now)...)
	c.actionMu.Unlock()
	for _, callback := range callbacks {
		if scheduled && c.callbackDispatcher != nil {
			if !c.callbackDispatcher.submitWithDiscard(callback.run, callback.discard) {
				callback.drop()
			}
			continue
		}
		callback.run()
	}
}

func (c *qblockClient) automaticScheduling() bool {
	return c.scheduleMode == qblockScheduleAutomatic && c.scheduler != nil
}

func (c *qblockClient) schedulerStopped() <-chan struct{} {
	if c.scheduler == nil {
		return nil
	}
	return c.scheduler.stopped
}

func (c *qblockClient) startScheduler() {
	if c.clock == nil || c.scheduler != nil {
		return
	}
	scheduler := newQBlockScheduler(c.clock)
	c.callbackDispatcher = newQBlockCallbackDispatcher(c.managerConfig.MaxTransfers)
	c.scheduler = scheduler
	go c.runSchedulerOwner(scheduler)
	go c.runSchedulerWorker(scheduler)
	c.notifyDeadlineChanged()
}

func (c *qblockClient) stopScheduler() {
	scheduler := c.scheduler
	if scheduler == nil {
		return
	}
	scheduler.stopOnce.Do(func() { close(scheduler.stop) })
}

func (c *qblockClient) notifyDeadlineChanged() {
	if !c.automaticScheduling() {
		return
	}
	select {
	case c.scheduler.notify <- struct{}{}:
	default:
	}
}

func (c *qblockClient) runSchedulerOwner(scheduler *qblockScheduler) {
	defer close(scheduler.stopped)
	defer scheduler.timer.Stop()
	busy := false
	for {
		select {
		case <-scheduler.stop:
			return
		case <-scheduler.notify:
		case <-c.endpointWake:
		case <-scheduler.timer.C():
		case <-scheduler.done:
			busy = false
		}
		if busy {
			scheduler.timer.Stop()
			continue
		}
		busy = c.recomputeSchedulerDeadline(scheduler)
	}
}

func (c *qblockClient) recomputeSchedulerDeadline(scheduler *qblockScheduler) bool {
	c.mu.Lock()
	deadline, ok := c.nextDeadlineLocked()
	c.mu.Unlock()
	if !ok {
		scheduler.timer.Stop()
		return false
	}
	delay := deadline.Sub(scheduler.clock.Now())
	if delay > 0 {
		scheduler.timer.Reset(delay)
		return false
	}
	scheduler.timer.Stop()
	select {
	case scheduler.due <- struct{}{}:
	default:
	}
	return true
}

func (c *qblockClient) runSchedulerWorker(scheduler *qblockScheduler) {
	for {
		select {
		case <-scheduler.stop:
			return
		case <-scheduler.due:
			c.advanceDueWithCallbacks(time.Time{}, true)
			select {
			case scheduler.done <- struct{}{}:
			case <-scheduler.stop:
				return
			}
		}
	}
}

func (c *qblockClient) abandon(token message.Token, err error) {
	if err == nil {
		err = qblock.ErrCanceled
	}
	var outputs []qblock.Output
	var callbacks []qblockCallback
	c.mu.Lock()
	key := string(token)
	if transfer := c.transferByToken[key]; transfer != nil {
		if transfer.exchange.terminalErr == nil {
			transfer.exchange.terminalErr = err
		}
		if transfer.exchange.cancelContext != nil {
			transfer.exchange.cancelContext()
		}
		outputs = c.manager.Cancel(transfer.id, err)
	} else if exchange := c.exchangesByOriginalToken[key]; exchange != nil {
		if exchange.terminalErr == nil {
			exchange.terminalErr = err
		}
		if exchange.cancelContext != nil {
			exchange.cancelContext()
		}
		if len(exchange.transfers) == 0 {
			callbacks = append(callbacks, c.failPendingGETLocked(exchange, exchange.terminalErr)...)
		} else {
			for id := range exchange.transfers {
				outputs = append(outputs, c.manager.Cancel(id, err)...)
			}
		}
	}
	c.mu.Unlock()
	c.drive(outputs)
	for _, callback := range callbacks {
		callback.run()
	}
	c.notifyDeadlineChanged()
}

func (c *qblockClient) close() {
	var outputs []qblock.Output
	var callbacks []qblockCallback
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	if c.cancelWriteContext != nil {
		c.cancelWriteContext()
	}
	for key, exchange := range c.exchangesByOriginalToken {
		if exchange.cancelContext != nil {
			exchange.cancelContext()
		}
		if !exchange.failureReported {
			exchange.failureReported = true
			_, _ = c.cc.tokenHandlerContainer.LoadAndDelete(exchange.originalToken.Hash())
			if exchange.fail != nil {
				fail := exchange.fail
				failure := exchange.terminalErr
				if failure == nil {
					failure = qblock.ErrClosed
				}
				callbacks = append(callbacks, qblockCallback{
					run:     func() { defer exchange.releaseCallbackSlot(); fail(failure) },
					discard: exchange.releaseCallbackSlot,
				})
			} else {
				exchange.releaseCallbackSlot()
			}
		}
		if len(exchange.transfers) == 0 {
			delete(c.exchangesByOriginalToken, key)
			c.clearPendingGETMIDsLocked(exchange)
			c.releasePacingWorkLocked(exchange.workID)
			exchange.closeRequestContext()
		}
	}
	if c.manager != nil {
		for id := range c.transfers {
			outputs = append(outputs, c.manager.Cancel(id, qblock.ErrClosed)...)
		}
		if c.server != nil {
			outputs = append(outputs, c.server.closeLocked()...)
		}
	}
	c.mu.Unlock()
	if c.endpoint != nil {
		c.endpoint.detach(c.now())
	}
	c.stopScheduler()
	c.lockAction()
	callbacks = append(callbacks, c.executeOrdered(outputs)...)
	c.actionMu.Unlock()
	// Close is non-waiting even if a completion callback blocks. Reuse the
	// bounded dispatcher in automatic mode, or create one for manual mode.
	if c.callbackDispatcher == nil && len(callbacks) > 0 {
		c.callbackDispatcher = newQBlockCallbackDispatcher(c.managerConfig.MaxTransfers)
	}
	if c.callbackDispatcher != nil {
		c.callbackDispatcher.stopWithCallbacks(callbacks)
	}
}

func (c *qblockClient) handle(msg *pool.Message) bool {
	c.mu.Lock()
	ownedTerminal := msg.Code() >= 64 && c.transferByToken[string(msg.Token())] != nil
	c.mu.Unlock()
	if ownedTerminal || msg.HasOption(message.QBlock1) || msg.HasOption(message.QBlock2) {
		size, err := qblockIncomingSize(msg)
		if err != nil || size > uint64(c.datagramLimit) {
			return true
		}
	}
	if msg.HasOption(message.QBlock2) && msg.Code() >= 64 {
		announced, err := msg.GetOptionUint32(message.Size2)
		if err != nil || announced > c.managerConfig.Transfer.MaxBodySize {
			return true
		}
	}
	if msg.Type() == message.Reset {
		if c.server != nil && c.server.handleReset(msg.MessageID()) {
			return true
		}
		c.mu.Lock()
		if exchange := c.pendingGETByMID[msg.MessageID()]; exchange != nil {
			callbacks := c.failPendingGETLocked(exchange, qblock.ErrCanceled)
			c.mu.Unlock()
			for _, callback := range callbacks {
				callback.run()
			}
			c.notifyDeadlineChanged()
			return true
		}
		transfer := c.transferByMID[msg.MessageID()]
		if transfer == nil {
			c.mu.Unlock()
			return false
		}
		outputs := c.finishExchangeLocked(transfer.exchange, qblock.ErrCanceled)
		c.mu.Unlock()
		c.drive(outputs)
		c.notifyDeadlineChanged()
		return true
	}
	c.lockAction()
	var callbacks []qblockCallback
	progressed := false
	defer func() {
		c.actionMu.Unlock()
		if progressed {
			c.notifyDeadlineChanged()
		}
		for _, callback := range callbacks {
			callback.run()
		}
	}()
	if msg.HasOption(message.QBlock1) {
		c.mu.Lock()
		token := msg.Token()
		transfer := c.transferByToken[string(token)]
		if transfer == nil || transfer.kind != qblock.Q1 {
			c.mu.Unlock()
			if !msg.HasOption(message.QBlock2) {
				return false
			}
		} else {
			outputs, handled := c.handleQ1ResponseLocked(msg, transfer.id)
			c.mu.Unlock()
			callbacks = append(callbacks, c.executeOrdered(outputs)...)
			callbacks = append(callbacks, c.executePendingOrdered(c.now())...)
			progressed = true
			return handled
		}
	}
	if !msg.HasOption(message.QBlock2) {
		c.mu.Lock()
		token := msg.Token()
		transfer := c.transferByToken[string(token)]
		if transfer == nil || transfer.kind != qblock.Q1 {
			c.mu.Unlock()
			return false
		}
		outputs, handled := c.handleQ1ResponseLocked(msg, transfer.id)
		c.mu.Unlock()
		callbacks = append(callbacks, c.executeOrdered(outputs)...)
		callbacks = append(callbacks, c.executePendingOrdered(c.now())...)
		progressed = true
		return handled
	}
	c.mu.Lock()
	token := msg.Token()
	if transfer := c.transferByToken[string(token)]; transfer != nil {
		if transfer.kind == qblock.Q1 {
			outputs, _ := c.handoffQ1ToQ2Locked(transfer.id, msg)
			c.mu.Unlock()
			callbacks = append(callbacks, c.executeOrdered(outputs)...)
			callbacks = append(callbacks, c.executePendingOrdered(c.now())...)
			progressed = true
			return true
		}
		fragment, _, err := fragmentFromQ2ForCode(msg, transfer.operation, &transfer.metadata, transfer.responseCode)
		if err != nil {
			outputs := c.manager.Cancel(transfer.id, err)
			c.mu.Unlock()
			callbacks = append(callbacks, c.executeOrdered(outputs)...)
			progressed = true
			return true
		}
		now := c.now()
		before, _ := c.manager.ReceiverProgress(transfer.id)
		outputs, err := c.manager.Receive(fragment, now)
		if err != nil {
			outputs = c.manager.Cancel(transfer.id, err)
			c.mu.Unlock()
			callbacks = append(callbacks, c.executeOrdered(outputs)...)
			progressed = true
			return true
		}
		after, stillActive := c.manager.ReceiverProgress(transfer.id)
		acceptedDelivery := false
		for _, output := range outputs {
			if output.TransferID == transfer.id && output.Action.Kind == qblock.Deliver {
				acceptedDelivery = true
				break
			}
		}
		if (stillActive && after > before) || acceptedDelivery {
			c.acceptPacingQ2ProgressLocked(transfer, fragment.Block.Number)
		}
		if err := c.syncPacingControlsLocked(transfer.id, now); err != nil {
			outputs = append(outputs, c.manager.Cancel(transfer.id, err)...)
		}
		c.mu.Unlock()
		callbacks = append(callbacks, c.executeOrdered(outputs)...)
		callbacks = append(callbacks, c.executePendingOrdered(now)...)
		progressed = true
		return true
	}
	exchange, ok := c.exchangesByOriginalToken[string(token)]
	if !ok {
		c.mu.Unlock()
		return true
	}
	size, err := qblockIncomingSize(msg)
	if err != nil || size > uint64(c.datagramLimit) {
		c.mu.Unlock()
		return true
	}
	value, err := msg.GetOptionUint32(message.QBlock2)
	if err != nil {
		c.mu.Unlock()
		return true
	}
	block, err := qblock.DecodeBlock(value)
	if err != nil || block.SZX > exchange.getSZX {
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
	now := c.now()
	outputs, err := c.manager.StartReceiverDeferred(fragment, now)
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
	if slot := c.workQueue.slots[exchange.workID]; slot != nil && slot.pending != nil && slot.pending.Kind == qblockWorkGET {
		// An accepted first fragment supersedes an unsent initial GET. This
		// also protects against a stale queued write after response routing.
		c.workQueue.clearPending(exchange.workID)
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
		requestTag:       cloneQBlockBytes(exchange.requestTag),
		expires:          now.Add(c.managerConfig.Transfer.Lifetime),
	}
	c.clearPendingGETMIDsLocked(exchange)
	exchange.transfers[id] = struct{}{}
	c.exchangeByTransfer[id] = exchange
	c.transfers[id] = transfer
	c.transferByToken[string(token)] = transfer
	c.acceptPacingFeedbackLocked(exchange.initialProbeKey)
	if err := c.syncPacingControlsLocked(id, now); err != nil {
		outputs = append(outputs, c.manager.Cancel(id, err)...)
	}
	c.mu.Unlock()
	callbacks = append(callbacks, c.executeOrdered(outputs)...)
	callbacks = append(callbacks, c.executePendingOrdered(now)...)
	progressed = true
	return true
}

func (c *qblockClient) handleQ1ResponseLocked(msg *pool.Message, id qblock.TransferID) ([]qblock.Output, bool) {
	transfer := c.transfers[id]
	if transfer == nil || transfer.kind != qblock.Q1 {
		return nil, false
	}
	if transfer.terminalResponse != nil {
		return nil, true
	}
	if msg.HasOption(message.QBlock2) {
		outputs, _ := c.handoffQ1ToQ2Locked(id, msg)
		return outputs, true
	}
	blockCount := qblockClientBlockCount(transfer.metadata)
	control, handled, err := q1ControlFromResponseBounded(msg, blockCount, c.datagramLimit, c.managerConfig.Transfer.MaxPayloads)
	if err != nil {
		return c.finishExchangeLocked(transfer.exchange, err), true
	}
	if handled {
		if msg.Code() == codes.Continue {
			value, _ := msg.GetOptionUint32(message.QBlock1)
			block, _ := qblock.DecodeBlock(value)
			if block.SZX != transfer.metadata.SZX {
				return c.finishExchangeLocked(transfer.exchange, errors.New("q-block Continue changed upload SZX")), true
			}
		}
		now := c.now()
		matchesProbe := c.matchesPacingQ1ControlLocked(transfer, control)
		outputs, err := c.manager.Control(control, now)
		if err != nil {
			return c.finishExchangeLocked(transfer.exchange, err), true
		}
		hasSendBlock := false
		for _, output := range outputs {
			if output.TransferID != id || output.Action.Kind != qblock.SendBlock {
				continue
			}
			hasSendBlock = true
			if len(control.Missing) != 0 {
				if transfer.repairReplies == nil {
					transfer.repairReplies = make(map[uint32]uint32)
				}
				transfer.repairReplies[output.Action.Block.Number]++
			}
		}
		if matchesProbe && hasSendBlock && c.acceptPacingFeedbackLocked(transfer.bodyProbeKey) {
			transfer.bodyAnswered = true
		}
		return outputs, true
	}
	response := c.cc.AcquireMessage(c.cc.Context())
	if err := msg.Clone(response); err != nil {
		c.pendingMessageRelease = append(c.pendingMessageRelease, response)
		return c.finishExchangeLocked(transfer.exchange, err), true
	}
	response.SetToken(transfer.exchange.originalToken)
	if transfer.terminalResponse != nil {
		c.pendingMessageRelease = append(c.pendingMessageRelease, transfer.terminalResponse)
	}
	transfer.terminalResponse = response
	outputs, err := c.manager.Control(qblock.Control{Token: message.Token(bytes.Clone(msg.Token()))}, c.now())
	if err != nil {
		c.pendingMessageRelease = append(c.pendingMessageRelease, response)
		transfer.terminalResponse = nil
		return c.finishExchangeLocked(transfer.exchange, err), true
	}
	if len(outputs) != 0 && c.acceptPacingFeedbackLocked(transfer.bodyProbeKey) {
		transfer.bodyAnswered = true
	}
	return outputs, true
}

// handoffQ1ToQ2Locked validates the complete first fragment before ending Q1.
// An error with no outputs leaves the sender live; errors after sender completion
// return terminal outputs so the regular driver reports failure exactly once.
func (c *qblockClient) handoffQ1ToQ2Locked(id qblock.TransferID, msg *pool.Message) ([]qblock.Output, error) {
	sender := c.transfers[id]
	if sender == nil || sender.kind != qblock.Q1 {
		return nil, qblock.ErrUnknownTransfer
	}
	if sender.terminalResponse != nil {
		return nil, nil
	}
	exchange := sender.exchange
	code := msg.Code()
	if code != codes.Created && code != codes.Changed && (exchange.requestCode != codes.POST || (code != codes.Content && code != codes.Deleted)) {
		return nil, errors.New("q-block response code is not applicable to upload")
	}
	etag, err := qblockETag(msg)
	if err != nil {
		return nil, err
	}
	operation, err := q2Operation(exchange.originalToken, etag)
	if err != nil {
		return nil, err
	}
	fragment, metadata, err := fragmentFromQ2ForCode(msg, operation, nil, code)
	if err != nil {
		return nil, err
	}
	if metadata.SZX > exchange.getSZX {
		return nil, errors.New("q-block response exceeds advertised upload response ceiling")
	}
	responseOptions, err := qblockResponseOptions(msg)
	if err != nil {
		return nil, err
	}
	completed, err := c.manager.Control(qblock.Control{Token: fragment.Token}, c.now())
	if err != nil {
		return c.finishExchangeLocked(exchange, err), err
	}
	if c.acceptPacingFeedbackLocked(sender.bodyProbeKey) {
		sender.bodyAnswered = true
	}
	// Keep the response token reserved across the transition. All other Q1
	// tokens and MIDs cease to route before the receiver is started.
	for token := range sender.tokens {
		if token == string(fragment.Token) {
			continue
		}
		delete(c.transferByToken, token)
		c.cc.releaseToken(message.Token([]byte(token)), tokenOwnerQBlock)
		delete(sender.tokens, token)
	}
	for mid := range sender.mids {
		delete(c.transferByMID, mid)
		delete(sender.mids, mid)
	}
	now := c.now()
	outputs, err := c.manager.StartReceiverDeferred(fragment, now)
	if err != nil {
		c.finishExchangeLocked(exchange, err)
		// Q1 has already left the manager, so its completion outputs now carry
		// the handoff error and release the remaining adapter record.
		for i := range completed {
			if completed[i].Action.Kind == qblock.Complete {
				completed[i].Action.Err = err
			}
		}
		return completed, err
	}
	receiverID, ok := c.manager.TransferID(operation)
	if !ok {
		// A one-fragment body is delivered and released by StartReceiver.
		receiverID = outputs[0].TransferID
	}
	receiver := &qblockTransfer{
		id:               receiverID,
		exchange:         exchange,
		kind:             qblock.Q2,
		operation:        operation,
		metadata:         metadata,
		requestTag:       bytes.Clone(sender.requestTag),
		initialToken:     message.Token(bytes.Clone(fragment.Token)),
		initialTokenUsed: true,
		tokens:           map[string]message.Token{string(fragment.Token): message.Token(bytes.Clone(fragment.Token))},
		mids:             make(map[int32]struct{}),
		responseOptions:  responseOptions,
		responseCode:     code,
		expires:          now.Add(c.managerConfig.Transfer.Lifetime),
	}
	exchange.transfers[receiverID] = struct{}{}
	c.exchangeByTransfer[receiverID] = exchange
	c.transfers[receiverID] = receiver
	c.transferByToken[string(fragment.Token)] = receiver
	delete(sender.tokens, string(fragment.Token))
	c.releaseTransferLocked(id)
	if err := c.syncPacingControlsLocked(receiverID, now); err != nil {
		outputs = append(outputs, c.manager.Cancel(receiverID, err)...)
	}
	return outputs, nil
}

func (c *qblockClient) ownsQ1Response(msg *pool.Message) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	transfer := c.transferByToken[string(msg.Token())]
	return transfer != nil && transfer.kind == qblock.Q1
}

func qblockClientBlockCount(metadata qblock.Metadata) uint32 {
	size := uint64(16) << metadata.SZX
	if size == 0 {
		return 0
	}
	count := (uint64(metadata.Size) + size - 1) / size
	if count == 0 {
		return 1
	}
	return uint32(count)
}

func (c *qblockClient) drive(outputs []qblock.Output) {
	c.lockAction()
	callbacks := c.executeOrdered(outputs)
	callbacks = append(callbacks, c.executePendingOrdered(c.now())...)
	c.actionMu.Unlock()
	for _, callback := range callbacks {
		callback.run()
	}
}

func (c *qblockClient) lockAction() {
	if !c.actionMu.TryLock() {
		if c.actionMuContention != nil {
			c.actionMuContention()
		}
		c.actionMu.Lock()
	}
}

type qblockCallback struct {
	run     func()
	discard func()
}

func (c qblockCallback) drop() {
	if c.discard != nil {
		c.discard()
	}
}

func (c *qblockClient) executeOrdered(outputs []qblock.Output) []qblockCallback {
	var callbacks []qblockCallback
	type bodyBurst struct {
		key   qblockProbeKey
		final bool
	}
	bodyBursts := make(map[qblock.TransferID]bodyBurst)
	c.mu.Lock()
	for _, output := range outputs {
		if output.Action.Kind == qblock.SendBlock {
			if transfer := c.transfers[output.TransferID]; transfer != nil && transfer.kind == qblock.Q1 && transfer.bodyProbeKey != 0 {
				burst := bodyBursts[output.TransferID]
				burst.key = transfer.bodyProbeKey
				burst.final = burst.final || !output.Action.Block.More
				bodyBursts[output.TransferID] = burst
			}
		}
	}
	c.mu.Unlock()
	for _, output := range outputs {
		callbacks = append(callbacks, c.executeOutput(output)...)
	}
	c.mu.Lock()
	for id, burst := range bodyBursts {
		if burst.final || c.transfers[id] == nil {
			c.gate().settle(burst.key, c.now())
		}
	}
	messages := c.pendingMessageRelease
	c.pendingMessageRelease = nil
	c.mu.Unlock()
	for _, msg := range messages {
		c.cc.ReleaseMessage(msg)
	}
	return callbacks
}

func (c *qblockClient) executeOutput(output qblock.Output) []qblockCallback {
	if c.server != nil && c.server.ownsOutput(output) {
		return c.executeServerOutput(output)
	}
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
		// All client receivers use deferred controls. An immediate action is
		// a contract violation and must never reach the socket ungated.
		return c.executeOrdered(c.cancelTransfer(output.TransferID, errors.New("unexpected immediate q-block client control")))
	case qblock.Deliver:
		return c.prepareDelivery(output.TransferID, output.Action.Payload)
	case qblock.Complete:
		if output.Action.Err != nil {
			return c.prepareFailure(output.TransferID, output.Action.Err)
		}
		return c.prepareTerminalResponse(output.TransferID)
	case qblock.Release:
		c.mu.Lock()
		c.releaseTransferLocked(output.TransferID)
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
	bodyAnswered := transfer.bodyAnswered
	repairReply := transfer.repairReplies[action.Block.Number] > 0
	if repairReply {
		transfer.repairReplies[action.Block.Number]--
		if transfer.repairReplies[action.Block.Number] == 0 {
			delete(transfer.repairReplies, action.Block.Number)
		}
	}
	bodyProbeActive := c.gate().ownsActive(transfer.bodyProbeKey)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	defer c.cc.ReleaseMessage(request)
	if err := request.Context().Err(); err != nil {
		return err
	}

	if bodyAnswered || (repairReply && !bodyProbeActive) {
		return c.writeQBlockMessage(request)
	}
	return c.writePacedMessage(transfer.bodyProbeKey, request)
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
	requestContext := transfer.exchange.requestContext
	if requestContext == nil {
		requestContext = c.cc.Context()
	}
	request := c.cc.AcquireMessage(requestContext)
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
	if err := c.reserveMIDLocked(mid); err != nil {
		c.cc.ReleaseMessage(request)
		return nil, err
	}
	request.SetMessageID(mid)
	request.SetOptionBytes(message.RequestTag, transfer.requestTag)
	request.SetOptionUint32(message.Size1, transfer.metadata.Size)
	request.SetOptionUint32(message.QBlock1, value)
	responseHint, err := qblock.EncodeBlock(qblock.Block{More: true, SZX: transfer.exchange.getSZX})
	if err != nil {
		c.cc.ReleaseMessage(request)
		return nil, err
	}
	request.SetOptionUint32(message.QBlock2, responseHint)
	request.SetBody(bytes.NewReader(bytes.Clone(action.Payload)))
	transfer.mids[mid] = struct{}{}
	c.transferByMID[mid] = transfer
	return request, nil
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
	c.mu.Lock()
	transfer := c.transfers[id]
	if transfer == nil {
		c.mu.Unlock()
		return nil, qblock.ErrUnknownTransfer
	}
	requestContext := transfer.exchange.requestContext
	if requestContext == nil {
		requestContext = c.writeContext
	}
	request := c.cc.AcquireMessage(requestContext)
	request.ResetOptionsTo(transfer.exchange.requestOpts)
	request.Remove(message.QBlock2)
	request.Remove(message.ETag)
	request.Remove(message.Observe)
	request.Remove(message.Size2)
	request.SetCode(transfer.exchange.requestCode)
	request.SetToken(token)
	request.SetType(message.NonConfirmable)
	mid := c.cc.GetMessageID()
	if err := c.reserveMIDLocked(mid); err != nil {
		c.cc.ReleaseMessage(request)
		c.mu.Unlock()
		return nil, err
	}
	request.SetMessageID(mid)
	request.SetOptionUint32(message.QBlock2, value)
	request.SetOptionBytes(message.RequestTag, transfer.requestTag)
	transfer.mids[mid] = struct{}{}
	c.transferByMID[mid] = transfer
	c.mu.Unlock()
	return request, nil
}

func (c *qblockClient) prepareDelivery(id qblock.TransferID, payload []byte) []qblockCallback {
	c.mu.Lock()
	transfer := c.transfers[id]
	if transfer == nil || transfer.kind != qblock.Q2 || c.closed {
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
		transfer.exchange.releaseCallbackSlot()
		c.mu.Unlock()
		c.cc.ReleaseMessage(response)
		return nil
	}
	exchange := transfer.exchange
	c.mu.Unlock()
	return []qblockCallback{{
		run: func() {
			defer exchange.releaseCallbackSlot()
			c.mu.Lock()
			closed := c.closed
			c.mu.Unlock()
			if closed {
				c.cc.ReleaseMessage(response)
				return
			}
			handler(nil, response)
		},
		discard: func() { c.cc.ReleaseMessage(response); exchange.releaseCallbackSlot() },
	}}
}

func (c *qblockClient) prepareTerminalResponse(id qblock.TransferID) []qblockCallback {
	c.mu.Lock()
	transfer := c.transfers[id]
	if transfer == nil || transfer.kind != qblock.Q1 || c.closed {
		c.mu.Unlock()
		return nil
	}
	if transfer.terminalResponse == nil {
		transfer.exchange.releaseCallbackSlot()
		c.mu.Unlock()
		return nil
	}
	response := transfer.terminalResponse
	transfer.terminalResponse = nil
	handler, ok := c.cc.tokenHandlerContainer.LoadAndDelete(transfer.exchange.originalToken.Hash())
	if !ok {
		transfer.exchange.releaseCallbackSlot()
		c.mu.Unlock()
		c.cc.ReleaseMessage(response)
		return nil
	}
	exchange := transfer.exchange
	c.mu.Unlock()
	return []qblockCallback{{
		run: func() {
			defer exchange.releaseCallbackSlot()
			c.mu.Lock()
			closed := c.closed
			c.mu.Unlock()
			if closed {
				c.cc.ReleaseMessage(response)
				return
			}
			handler(nil, response)
		},
		discard: func() { c.cc.ReleaseMessage(response); exchange.releaseCallbackSlot() },
	}}
}

func (c *qblockClient) prepareFailure(id qblock.TransferID, err error) []qblockCallback {
	c.mu.Lock()
	exchange := c.exchangeByTransfer[id]
	if exchange == nil || exchange.failureReported {
		c.mu.Unlock()
		return nil
	}
	exchange.failureReported = true
	_, _ = c.cc.tokenHandlerContainer.LoadAndDelete(exchange.originalToken.Hash())
	fail := exchange.fail
	if fail == nil {
		exchange.releaseCallbackSlot()
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	return []qblockCallback{{
		run:     func() { defer exchange.releaseCallbackSlot(); fail(err) },
		discard: exchange.releaseCallbackSlot,
	}}
}

func (c *qblockClient) cancelTransfer(id qblock.TransferID, err error) []qblock.Output {
	c.mu.Lock()
	if exchange := c.exchangeByTransfer[id]; exchange != nil && exchange.terminalErr == nil {
		exchange.terminalErr = err
	}
	outputs := c.manager.Cancel(id, err)
	c.mu.Unlock()
	return outputs
}

func (c *qblockClient) finishExchangeLocked(exchange *qblockExchange, err error) []qblock.Output {
	if exchange == nil || exchange.finished {
		return nil
	}
	exchange.finished = true
	delete(c.exchangesByOriginalToken, string(exchange.originalToken))
	c.clearPendingGETMIDsLocked(exchange)
	if err != nil {
		if exchange.terminalErr == nil {
			exchange.terminalErr = err
		}
		if exchange.cancelContext != nil {
			exchange.cancelContext()
		}
		_, _ = c.cc.tokenHandlerContainer.LoadAndDelete(exchange.originalToken.Hash())
	}
	ids := make([]qblock.TransferID, 0, len(exchange.transfers))
	for id := range exchange.transfers {
		ids = append(ids, id)
	}
	var outputs []qblock.Output
	for _, id := range ids {
		outputs = append(outputs, c.manager.Cancel(id, err)...)
	}
	return outputs
}

func (c *qblockClient) releaseTransferLocked(id qblock.TransferID) {
	transfer := c.transfers[id]
	if transfer == nil {
		return
	}
	if transfer.terminalResponse != nil {
		c.pendingMessageRelease = append(c.pendingMessageRelease, transfer.terminalResponse)
		transfer.terminalResponse = nil
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
			c.releasePacingWorkLocked(transfer.exchange.workID)
			transfer.exchange.closeRequestContext()
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
	options := cloneQBlockOptions(msg.Options())
	options = options.Remove(message.QBlock2)
	options = options.Remove(message.Size2)
	return cloneQBlockOptions(options), nil
}

func fragmentFromQ2(msg *pool.Message, operation qblock.OperationKey, previous *qblock.Metadata) (qblock.Fragment, qblock.Metadata, error) {
	return fragmentFromQ2ForCode(msg, operation, previous, codes.Content)
}

func fragmentFromQ2ForCode(msg *pool.Message, operation qblock.OperationKey, previous *qblock.Metadata, allowedCode codes.Code) (qblock.Fragment, qblock.Metadata, error) {
	if msg.Code() != allowedCode {
		return qblock.Fragment{}, qblock.Metadata{}, errors.New("q-block response code changed or is not applicable")
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
		payload, err = readQBlockBody(body, uint32(16)<<block.SZX)
		if err != nil {
			return qblock.Fragment{}, qblock.Metadata{}, err
		}
	}
	// Validate layout independently of the manager's resource budget, before
	// a handoff can mutate the sender. The manager still enforces its own limits.
	body, err := qblock.NewBody(metadata, ^uint32(0))
	if err != nil {
		return qblock.Fragment{}, qblock.Metadata{}, err
	}
	if _, err := body.Add(metadata, block, payload); err != nil {
		return qblock.Fragment{}, qblock.Metadata{}, err
	}
	return qblock.Fragment{Operation: operation, Token: message.Token(bytes.Clone(msg.Token())), Kind: qblock.Q2, Metadata: metadata, Block: block, Payload: payload}, metadata, nil
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

// The caller holds mu. Every new NON packet must have distinct live ownership.
func (c *qblockClient) reserveMIDLocked(mid int32) error {
	if !message.ValidateMID(mid) {
		return qblock.ErrLimitExceeded
	}
	if c.transferByMID[mid] != nil || c.pendingGETByMID[mid] != nil {
		return qblock.ErrLimitExceeded
	}
	count := len(c.transferByMID) + len(c.pendingGETByMID)
	if c.server != nil {
		if c.server.byMID[mid] != nil || c.server.conByMID[mid] != nil {
			return qblock.ErrLimitExceeded
		}
		count += len(c.server.byMID) + len(c.server.conByMID)
	}
	if uint64(count) >= uint64(c.maxMIDEntries) {
		return qblock.ErrLimitExceeded
	}
	return nil
}

func (c *qblockClient) acquireExchangeCapacity() (func(), func(), error) {
	release, err := c.ownedBudget.acquire(c.ownedBudget.clientCost)
	if err != nil {
		return nil, nil, err
	}
	slot, ok := c.callbackSlots.tryAcquire()
	if !ok {
		release()
		return nil, nil, qblock.ErrLimitExceeded
	}
	var mu sync.Mutex
	refs := 2
	drop := func() {
		mu.Lock()
		refs--
		last := refs == 0
		mu.Unlock()
		if last {
			release()
		}
	}
	var callbackOnce, preparationOnce sync.Once
	return func() { callbackOnce.Do(func() { slot(); drop() }) }, func() { preparationOnce.Do(drop) }, nil
}

func (c *qblockClient) preflightOwnedOptions(options message.Options) error {
	size, err := qblockOptionBytes(options)
	if err != nil {
		return err
	}
	max, ok := qblockCheckedMul(uint64(c.datagramLimit), uint64(unsafe.Sizeof(message.Option{}))+1)
	if !ok || size > max {
		return qblock.ErrLimitExceeded
	}
	return nil
}

func (c *qblockClient) clearPendingGETMIDsLocked(exchange *qblockExchange) {
	for mid, owner := range c.pendingGETByMID {
		if owner == exchange {
			delete(c.pendingGETByMID, mid)
		}
	}
}
