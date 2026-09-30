package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
)

type qblockServerConfig struct {
	Retention        time.Duration
	MaxRecords       uint32
	MaxMetadataBytes uint64
}

type qblockServer struct {
	client   *qblockClient
	handler  HandlerFunc
	config   qblockServerConfig
	records  map[qblock.OperationKey]*qblockServerRecord
	byID     map[qblock.TransferID]*qblockServerRecord
	byMID    map[int32]*qblockServerRecord
	metadata uint64
	nextGen  uint64
	closed   bool
}

type qblockServerRecord struct {
	id              qblock.TransferID
	workID          qblockWorkID
	bodyProbeKey    qblockProbeKey
	bodyAnswered    bool
	operation       qblock.OperationKey
	activeOperation qblock.OperationKey
	metadata        qblock.Metadata
	options         message.Options
	tokens          map[string]message.Token
	replyToken      message.Token
	payload         []byte
	ready           bool
	executing       bool
	code            codes.Code
	responseCode    codes.Code
	responseOptions message.Options
	pendingReplies  map[qblockServerReplyKey][]message.Token
	charged         uint64
	responseCharged uint64
	generation      uint64
	mids            map[int32]struct{}
	handlerRunning  bool
	terminal        bool
	writeExpires    time.Time
	expires         time.Time
	retainUntil     time.Time
	writeContext    context.Context
	cancelWrite     context.CancelFunc
}

// qblockServerReplyKey groups Q2 payload actions for reply-token lookup.
// Control acceptance and its complete output batch share actionMu, so the
// FIFO queue is consumed before another control can enqueue an identical key.
type qblockServerReplyKey struct {
	number uint32
	more   bool
}

func replyKey(action qblock.Action) qblockServerReplyKey {
	return qblockServerReplyKey{number: action.Block.Number, more: action.Block.More}
}

func (r *qblockServerRecord) queueReplyTokens(outputs []qblock.Output, token message.Token) {
	for _, output := range outputs {
		if output.Action.Kind != qblock.SendBlock {
			continue
		}
		if r.pendingReplies == nil {
			r.pendingReplies = make(map[qblockServerReplyKey][]message.Token)
		}
		key := replyKey(output.Action)
		r.pendingReplies[key] = append(r.pendingReplies[key], bytes.Clone(token))
	}
}

func (r *qblockServerRecord) takeReplyToken(action qblock.Action) message.Token {
	key := replyKey(action)
	if pending := r.pendingReplies[key]; len(pending) > 0 {
		token := pending[0]
		if len(pending) == 1 {
			delete(r.pendingReplies, key)
		} else {
			r.pendingReplies[key] = pending[1:]
		}
		return bytes.Clone(token)
	}
	return bytes.Clone(r.replyToken)
}

func withQBlockServer(cfg qblockServerConfig) Option {
	return func(opts *ConnOptions) {
		copy := cfg
		opts.qblockServerConfig = &copy
	}
}

func newQBlockServer(client *qblockClient, handler HandlerFunc, cfg qblockServerConfig) *qblockServer {
	if client == nil || client.manager == nil {
		return nil
	}
	if cfg.Retention == 0 {
		cfg.Retention = client.managerConfigLifetime()
	}
	if cfg.MaxRecords == 0 {
		cfg.MaxRecords = client.managerConfigTransfers()
	}
	if cfg.MaxMetadataBytes == 0 {
		cfg.MaxMetadataBytes = 64 << 10
	}
	if cfg.Retention < 0 || cfg.Retention < client.managerConfigLifetime() {
		return nil
	}
	return &qblockServer{
		client: client, handler: handler, config: cfg,
		records: make(map[qblock.OperationKey]*qblockServerRecord),
		byID:    make(map[qblock.TransferID]*qblockServerRecord),
		byMID:   make(map[int32]*qblockServerRecord),
	}
}

func (c *qblockClient) managerConfigLifetime() time.Duration {
	return c.managerConfig.Transfer.Lifetime
}
func (c *qblockClient) managerConfigTransfers() uint32 { return c.managerConfig.MaxTransfers }

func (s *qblockServer) ownsOutput(output qblock.Output) bool {
	s.client.mu.Lock()
	defer s.client.mu.Unlock()
	record := s.byID[output.TransferID]
	return record != nil && record.activeOperation == output.Operation
}

func (s *qblockServer) bindMIDLocked(record *qblockServerRecord, mid int32) {
	if record.mids == nil {
		record.mids = make(map[int32]struct{})
	}
	record.mids[mid] = struct{}{}
	s.byMID[mid] = record
}

func (s *qblockServer) clearMIDsLocked(record *qblockServerRecord) {
	for mid := range record.mids {
		if s.byMID[mid] == record {
			delete(s.byMID, mid)
		}
		delete(record.mids, mid)
	}
}

func (s *qblockServer) acceptPacingFeedbackLocked(record *qblockServerRecord, msg *pool.Message, progressed bool) bool {
	if !progressed || record == nil || record.workID == 0 {
		return false
	}
	c := s.client
	probe := c.currentProbe
	if probe == nil || !probe.server || probe.workID != record.workID || probe.generation != record.generation || probe.transferID != record.id {
		return false
	}
	var number uint32
	if msg.HasOption(message.QBlock2) && probe.kind == qblockProbeBody {
		value, err := msg.GetOptionUint32(message.QBlock2)
		if err != nil {
			return false
		}
		block, err := qblock.DecodeBlock(value)
		if err != nil {
			return false
		}
		number = block.Number
		if block.More {
			if number == 0 {
				return false
			}
			number--
		}
	} else if msg.HasOption(message.QBlock1) && probe.kind == qblockProbeControl {
		value, err := msg.GetOptionUint32(message.QBlock1)
		if err != nil {
			return false
		}
		block, err := qblock.DecodeBlock(value)
		if err != nil {
			return false
		}
		number = block.Number
	} else {
		return false
	}
	if number/c.managerConfig.Transfer.MaxPayloads != probe.set || !slices.Contains(probe.numbers, number) {
		return false
	}
	if !c.acceptPacingFeedbackLocked(probe.key) {
		return false
	}
	if probe.kind == qblockProbeBody {
		record.bodyAnswered = true
	}
	return true
}

func (s *qblockServer) deactivateLocked(record *qblockServerRecord, now time.Time) {
	if record.cancelWrite != nil {
		record.cancelWrite()
	}
	delete(s.byID, record.id)
	s.clearMIDsLocked(record)
	for key, token := range record.tokens {
		s.client.cc.releaseToken(token, tokenOwnerQBlock)
		delete(record.tokens, key)
	}
	record.pendingReplies = nil
	s.releaseResponseMetadataLocked(record)
	record.terminal = true
	if record.workID != 0 {
		s.client.releasePacingWorkLocked(record.workID)
		record.workID = 0
	}
	if !record.handlerRunning && record.expires.IsZero() {
		if record.retainUntil.IsZero() {
			record.retainUntil = now.Add(s.config.Retention)
		}
		record.expires = record.retainUntil
	}
}

// settleHandlerLocked records that a running handler has returned. A handler
// can outlive its Q1 transfer; in that case preserve duplicate suppression
// from the point at which application execution actually settles.
// The caller holds client.mu.
func (s *qblockServer) settleHandlerLocked(record *qblockServerRecord, now time.Time) {
	record.handlerRunning = false
	// The handler has returned; suppression and response repair depend on
	// operation identity, not another retained copy of the upload body.
	record.payload = nil
	if record.retainUntil.IsZero() {
		record.retainUntil = now.Add(s.config.Retention)
	}
	if record.terminal && record.expires.IsZero() {
		record.expires = record.retainUntil
	}
}

// nextRecordDeadlineLocked returns the earliest eligible terminal-record
// cleanup deadline. The caller holds client.mu.
func (s *qblockServer) nextRecordDeadlineLocked() (time.Time, bool) {
	var next time.Time
	for _, record := range s.records {
		if !record.terminal || record.handlerRunning || record.expires.IsZero() {
			continue
		}
		if next.IsZero() || record.expires.Before(next) {
			next = record.expires
		}
	}
	return next, !next.IsZero()
}

// expireRecordsLocked bounds completed duplicate-suppression records without
// expiring a handler still allowed to finish. The caller holds client.mu.
func (s *qblockServer) expireRecordsLocked(now time.Time) {
	if s.closed {
		return
	}
	for _, record := range s.records {
		if !record.terminal || record.handlerRunning || record.expires.IsZero() || now.Before(record.expires) {
			continue
		}
		if s.byID[record.id] != nil {
			_ = s.client.manager.Cancel(record.id, qblock.ErrExpired)
		}
		s.releaseLocked(record)
	}
}

// closeLocked invalidates every record before callbacks can resume. Its
// cancellation outputs are deliberately returned to the shared executor after
// client.mu is released.
func (s *qblockServer) closeLocked() []qblock.Output {
	if s.closed {
		return nil
	}
	s.closed = true
	s.nextGen++
	var outputs []qblock.Output
	for _, record := range s.records {
		if s.byID[record.id] != nil {
			outputs = append(outputs, s.client.manager.Cancel(record.id, qblock.ErrClosed)...)
		}
		s.releaseLocked(record)
	}
	return outputs
}

// handleReset owns Reset processing for packets written by the server role.
// An executing record remains as bounded duplicate suppression, but has no
// live manager/token/MID state and cannot be revived by its late handler.
func (s *qblockServer) handleReset(mid int32) bool {
	s.client.mu.Lock()
	record := s.byMID[mid]
	if record == nil {
		s.client.mu.Unlock()
		return false
	}
	_ = s.client.manager.Cancel(record.id, qblock.ErrCanceled)
	s.deactivateLocked(record, s.client.now())
	if !record.executing {
		s.releaseLocked(record)
	}
	s.client.mu.Unlock()
	s.client.notifyDeadlineChanged()
	return true
}

func (c *qblockClient) handleServerRequest(msg *pool.Message) bool {
	if c.server == nil || msg.Code() < 1 || msg.Code() >= 32 {
		return false
	}
	if !msg.HasOption(message.QBlock1) && !msg.HasOption(message.QBlock2) {
		return false
	}
	c.lockAction()
	var outputs []qblock.Output
	var changed bool
	if msg.HasOption(message.QBlock2) {
		outputs, changed = c.server.handleQ2Control(msg)
	} else {
		outputs, changed = c.server.handleQ1(msg)
	}
	callbacks := c.executeOrdered(outputs)
	callbacks = append(callbacks, c.executePendingOrdered(c.now())...)
	c.actionMu.Unlock()
	if changed {
		c.notifyDeadlineChanged()
	}
	for _, callback := range callbacks {
		callback.run()
	}
	return true
}

func (c *qblockClient) executeServerOutput(output qblock.Output) []qblockCallback {
	c.mu.Lock()
	if c.server == nil {
		c.mu.Unlock()
		return nil
	}
	record := c.server.byID[output.TransferID]
	if record == nil || record.activeOperation != output.Operation {
		c.mu.Unlock()
		return nil
	}
	switch output.Action.Kind {
	case qblock.Deliver:
		record.payload = bytes.Clone(output.Action.Payload)
		record.ready = true
		if record.executing || c.server.handler == nil {
			c.mu.Unlock()
			return nil
		}
		record.executing = true
		record.handlerRunning = true
		payload := bytes.Clone(record.payload)
		options, _ := record.options.Clone()
		code := record.code
		operation := record.operation
		generation := record.generation
		c.mu.Unlock()
		return []qblockCallback{{run: func() {
			c.mu.Lock()
			current := c.server != nil && !c.server.closed && c.server.records[operation] == record && record.generation == generation && !record.terminal
			c.mu.Unlock()
			if !current {
				c.server.finishHandler(operation, generation, false, 0, nil, nil)
				return
			}
			req := c.cc.AcquireMessage(c.cc.Context())
			resp := c.cc.AcquireMessage(c.cc.Context())
			defer c.cc.ReleaseMessage(req)
			defer c.cc.ReleaseMessage(resp)
			req.SetCode(code)
			req.ResetOptionsTo(options)
			req.SetBody(bytes.NewReader(payload))
			writer := responsewriter.New(resp, c.cc, options...)
			c.server.handler(writer, req)
			body := []byte(nil)
			if reader := writer.Message().Body(); reader != nil {
				var err error
				body, err = readQBlockBody(reader, c.managerConfig.Transfer.MaxBodySize)
				if err != nil {
					c.server.finishHandler(operation, generation, false, 0, nil, nil)
					return
				}
			}
			responseOptions := cloneQBlockResponseOptions(writer.Message().Options())
			c.server.finishHandler(operation, generation, writer.Message().IsModified(), writer.Message().Code(), responseOptions, body)
		}}}
	case qblock.Release:
		if record.executing {
			c.server.deactivateLocked(record, c.now())
			c.mu.Unlock()
			return nil
		}
		c.server.releaseLocked(record)
		c.mu.Unlock()
		return nil
	case qblock.SendContinue, qblock.RequestMissing:
		outputs := c.manager.Cancel(record.id, errors.New("unexpected immediate q-block server control"))
		c.mu.Unlock()
		return c.executeOrdered(outputs)
	case qblock.SendBlock:
		token := record.takeReplyToken(output.Action)
		probeKey := qblockProbeKey(0)
		if !record.bodyAnswered && c.probeGate.state == qblockProbeActive && c.probeGate.key == record.bodyProbeKey {
			probeKey = record.bodyProbeKey
		}
		writeContext := record.writeContext
		code := record.responseCode
		options, _ := record.responseOptions.Clone()
		szx := record.metadata.SZX
		size := record.metadata.Size
		etag := bytes.Clone(record.metadata.Identity)
		mid := c.cc.GetMessageID()
		c.server.bindMIDLocked(record, mid)
		c.mu.Unlock()
		if err := c.writeServerQ2Block(writeContext, token, mid, code, options, szx, size, etag, output.Action, probeKey); err != nil {
			c.mu.Lock()
			outputs := c.manager.Cancel(output.TransferID, err)
			c.mu.Unlock()
			return c.executeOrdered(outputs)
		}
		return nil
	case qblock.Complete:
		c.probeGate.settle(record.bodyProbeKey, c.now())
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	return nil
}

func (s *qblockServer) finishHandler(operation qblock.OperationKey, generation uint64, modified bool, code codes.Code, options message.Options, payload []byte) {
	c := s.client
	c.lockAction()
	actionLocked := true
	changed := false
	defer func() {
		if actionLocked {
			c.actionMu.Unlock()
		}
		if changed {
			c.notifyDeadlineChanged()
		}
	}()
	c.mu.Lock()
	record := s.records[operation]
	if record == nil || !record.executing || record.generation != generation || s.closed {
		c.mu.Unlock()
		return
	}
	s.settleHandlerLocked(record, c.now())
	changed = true
	if record.terminal {
		c.mu.Unlock()
		return
	}
	q1id := record.id
	_ = c.manager.Cancel(q1id, nil)
	delete(s.byID, q1id)
	s.clearMIDsLocked(record)
	if !modified {
		s.deactivateLocked(record, c.now())
		c.mu.Unlock()
		return
	}
	if err := s.prepareResponseLocked(record, code, options, payload, c.now(), c.jitter()); err == nil {
		c.mu.Unlock()
		callbacks := c.executePendingOrdered(c.now())
		c.actionMu.Unlock()
		actionLocked = false
		c.notifyDeadlineChanged()
		changed = false
		for _, callback := range callbacks {
			callback.run()
		}
		return
	}
	// Keep a bounded duplicate record even when no representation can be sent.
	s.deactivateLocked(record, c.now())
	c.mu.Unlock()
}

func (s *qblockServer) prepareResponseLocked(record *qblockServerRecord, code codes.Code, options message.Options, payload []byte, now time.Time, jitter float64) error {
	c := s.client
	if record.workID == 0 || uint64(len(payload)) > math.MaxUint32 || c.nextProbeKey == qblockProbeKey(math.MaxUint64) {
		return qblock.ErrLimitExceeded
	}
	responseBytes, err := qblockOptionBytes(options)
	if err != nil {
		return err
	}
	if s.metadata > s.config.MaxMetadataBytes || responseBytes > s.config.MaxMetadataBytes-s.metadata {
		return qblock.ErrLimitExceeded
	}
	wait, err := qblockProbingWait(c.pacingConfig, c.managerConfig.Transfer, jitter)
	if err != nil {
		return err
	}
	etag := sha256.Sum256(append([]byte{byte(code)}, payload...))
	meta := qblock.Metadata{Size: uint32(len(payload)), SZX: record.metadata.SZX, Identity: etag[:8], HasContentFormat: true, ContentFormat: message.TextPlain}
	template := c.cc.AcquireMessage(c.cc.Context())
	template.ResetOptionsTo(options)
	template.SetOptionUint32(message.Size2, meta.Size)
	template.SetOptionBytes(message.ETag, meta.Identity)
	meta.SZX, err = c.selectBodySZX(template, message.QBlock2, meta.Size, meta.SZX)
	c.cc.ReleaseMessage(template)
	if err != nil {
		return err
	}
	q2op, err := qblock.NewOperationKey([]byte("server-q2"), []byte(record.operation), meta.Identity)
	if err != nil {
		return err
	}
	id, err := c.manager.PrepareSender(q2op, record.replyToken, qblock.Q2, meta, payload, now, jitter)
	if err != nil {
		return err
	}
	probeKey := c.nextProbeKey + 1
	work := qblockPendingWork{
		Kind: qblockWorkBody, Server: true, Operation: q2op, TransferID: id,
		Generation: record.generation, Expires: now.Add(c.managerConfig.Transfer.Lifetime),
		ProbeKey: probeKey, NonProbingWait: wait,
	}
	if err := c.workQueue.replace(record.workID, work, false); err != nil {
		_ = c.manager.Cancel(id, err)
		return err
	}
	c.nextProbeKey = probeKey
	record.id, record.activeOperation, record.metadata = id, q2op, meta
	record.responseCode, record.responseOptions = code, options
	record.responseCharged = responseBytes
	s.metadata += responseBytes
	record.bodyProbeKey = probeKey
	record.bodyAnswered = false
	record.writeExpires = work.Expires
	s.byID[id] = record
	return nil
}

func (c *qblockClient) writeServerQ2Block(writeContext context.Context, token message.Token, mid int32, code codes.Code, options message.Options, szx blockwise.SZX, size uint32, etag []byte, action qblock.Action, probeKey qblockProbeKey) error {
	msg := c.cc.AcquireMessage(writeContext)
	defer c.cc.ReleaseMessage(msg)
	msg.SetType(message.NonConfirmable)
	msg.SetToken(token)
	msg.SetMessageID(mid)
	msg.SetCode(code)
	msg.ResetOptionsTo(options)
	value, err := qblock.EncodeBlock(qblock.Block{Number: action.Block.Number, More: action.Block.More, SZX: szx})
	if err != nil {
		return err
	}
	msg.SetOptionUint32(message.QBlock2, value)
	msg.SetOptionUint32(message.Size2, size)
	msg.SetOptionBytes(message.ETag, etag)
	msg.SetBody(bytes.NewReader(action.Payload))
	if err := writeContext.Err(); err != nil {
		return err
	}
	if probeKey != 0 {
		return c.writePacedMessage(probeKey, msg)
	}
	return c.writeQBlockMessage(msg)
}

func (c *qblockClient) writeServerQ1Control(writeContext context.Context, token message.Token, mid int32, szx blockwise.SZX, action qblock.Action, probeKey qblockProbeKey) error {
	if len(token) == 0 {
		return qblock.ErrUnknownTransfer
	}
	msg := c.cc.AcquireMessage(writeContext)
	defer c.cc.ReleaseMessage(msg)
	msg.SetType(message.NonConfirmable)
	msg.SetToken(token)
	msg.SetMessageID(mid)
	switch action.Kind {
	case qblock.SendContinue:
		value, err := qblock.EncodeBlock(qblock.Block{Number: action.Through, More: true, SZX: szx})
		if err != nil {
			return err
		}
		msg.SetCode(codes.Continue)
		msg.SetOptionUint32(message.QBlock1, value)
	case qblock.RequestMissing:
		msg.SetCode(codes.RequestEntityIncomplete)
		msg.SetContentFormat(message.AppMissingBlocksCBORSeq)
		// Measure the actual header, token, and option encoding before
		// allocating the report. A nonempty CBOR sequence adds one marker.
		overhead, err := qblockDatagramSize(msg)
		if err != nil {
			return err
		}
		if overhead+1 >= uint64(c.datagramLimit) {
			return qblock.ErrLimitExceeded
		}
		payload, _, err := qblock.EncodeMissing(action.Numbers, int(uint64(c.datagramLimit)-overhead-1))
		if err != nil {
			return fmt.Errorf("encode q-block missing report: %w", err)
		}
		msg.SetBody(bytes.NewReader(payload))
	default:
		return errors.New("unsupported server q-block output")
	}
	if err := writeContext.Err(); err != nil {
		return err
	}
	if probeKey != 0 {
		return c.writePacedMessage(probeKey, msg)
	}
	return c.writeQBlockMessage(msg)
}

func (s *qblockServer) releaseLocked(record *qblockServerRecord) {
	if record.cancelWrite != nil {
		record.cancelWrite()
	}
	delete(s.byID, record.id)
	delete(s.records, record.operation)
	if record.workID != 0 {
		s.client.releasePacingWorkLocked(record.workID)
		record.workID = 0
	}
	s.clearMIDsLocked(record)
	for key, token := range record.tokens {
		s.client.cc.releaseToken(token, tokenOwnerQBlock)
		delete(record.tokens, key)
	}
	if s.metadata >= record.charged {
		s.metadata -= record.charged
	}
	s.releaseResponseMetadataLocked(record)
}

func (s *qblockServer) releaseResponseMetadataLocked(record *qblockServerRecord) {
	s.metadata -= record.responseCharged
	record.responseCharged = 0
	record.responseOptions = nil
}

// The ordinary Options.Clone retains a minimum-size shared buffer. Retained
// response snapshots use exact storage so their metadata charge matches it.
func cloneQBlockResponseOptions(options message.Options) message.Options {
	result := make(message.Options, len(options))
	for i, option := range options {
		value := make([]byte, len(option.Value))
		copy(value, option.Value)
		result[i] = message.Option{ID: option.ID, Value: value}
	}
	return result
}

var errQBlockServerConfig = errors.New("invalid private q-block server configuration")
