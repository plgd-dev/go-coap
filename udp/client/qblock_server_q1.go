package client

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
)

func (s *qblockServer) handleQ1(msg *pool.Message) ([]qblock.Output, bool) {
	// Q-Block requests owned by the enabled private role are always consumed,
	// including malformed input, so the generic disabled-Q path stays silent.
	if msg.Type() != message.NonConfirmable || (msg.Code() != codes.POST && msg.Code() != codes.PUT) {
		return nil, false
	}
	// Canonical identity work is covered by the serialized executor envelope.
	preflightOptions, err := canonicalServerRequestOptions(msg.Options())
	if err != nil {
		return nil, false
	}
	preflightOperation, err := serverRequestKey(msg.Code(), preflightOptions)
	if err != nil {
		return nil, false
	}
	s.client.mu.Lock()
	existing := s.records[preflightOperation] != nil
	s.client.mu.Unlock()
	var ownedLease *qblockOwnedLease
	if !existing {
		release, err := s.client.ownedBudget.acquire(s.client.ownedBudget.serverCost)
		if err != nil {
			return nil, false
		}
		ownedLease = newQBlockOwnedLease(release)
		defer func() {
			if ownedLease != nil {
				ownedLease.drop()
			}
		}()
	}
	fragment, options, err := serverQ1Fragment(msg)
	if err != nil {
		return nil, false
	}
	operation, err := serverRequestKey(msg.Code(), options)
	if err != nil {
		return nil, false
	}
	fragment.Operation = operation
	hint, _ := serverUploadResponseHint(msg)

	s.client.mu.Lock()
	if s.closed {
		s.client.mu.Unlock()
		return nil, false
	}
	if record := s.records[operation]; record != nil {
		if (record.responseCeiling == nil) != (hint == nil) || (hint != nil && *record.responseCeiling != *hint) {
			s.client.mu.Unlock()
			return nil, false
		}
		if record.executing {
			s.client.mu.Unlock()
			return nil, false
		}
		outputs := s.receiveLocked(record, fragment, msg)
		s.client.mu.Unlock()
		return outputs, true
	}
	// Reset may retire the record while fragment parsing runs outside mu.
	// Replacement admission must own a lease even when preflight saw a record.
	if ownedLease == nil {
		release, err := s.client.ownedBudget.acquire(s.client.ownedBudget.serverCost)
		if err != nil {
			s.client.mu.Unlock()
			return nil, false
		}
		ownedLease = newQBlockOwnedLease(release)
		defer func() {
			if ownedLease != nil {
				ownedLease.drop()
			}
		}()
	}
	if uint64(len(s.records)) >= uint64(s.config.MaxRecords) {
		s.client.mu.Unlock()
		return nil, false
	}
	charge := uint64(len(operation)) + optionsSize(options)
	if charge > s.config.MaxMetadataBytes-s.metadata {
		s.client.mu.Unlock()
		return nil, false
	}
	capacity, err := qblockControlCapacity(options, s.client.managerConfig.Transfer.MaxPayloads)
	if err != nil {
		s.client.mu.Unlock()
		return nil, false
	}
	workID, err := s.client.workQueue.reserve(capacity)
	if err != nil {
		s.client.mu.Unlock()
		return nil, false
	}
	if err := s.client.cc.claimToken(fragment.Token, tokenOwnerQBlock); err != nil {
		s.client.releasePacingWorkLocked(workID)
		s.client.mu.Unlock()
		return nil, false
	}
	now := s.client.now()
	outputs, err := s.client.manager.StartReceiverDeferred(fragment, now)
	if err != nil {
		s.client.cc.releaseToken(fragment.Token, tokenOwnerQBlock)
		s.client.releasePacingWorkLocked(workID)
		s.client.mu.Unlock()
		return nil, false
	}
	id, ok := s.client.manager.TransferID(operation)
	if !ok {
		// StartReceiver must retain Q1 after any Deliver. Treat a violated
		// manager invariant as a rejected admission without publishing a record.
		s.client.cc.releaseToken(fragment.Token, tokenOwnerQBlock)
		s.client.releasePacingWorkLocked(workID)
		s.client.mu.Unlock()
		return nil, false
	}
	s.nextGen++
	writeContext, cancelWrite := context.WithCancel(s.client.writeContext)
	record := &qblockServerRecord{
		ownedLease:      ownedLease,
		responseCeiling: hint,
		id:              id, workID: workID, operation: operation, activeOperation: operation, metadata: fragment.Metadata, options: options,
		tokens: map[string]message.Token{string(fragment.Token): bytes.Clone(fragment.Token)}, replyToken: bytes.Clone(fragment.Token), code: msg.Code(), charged: charge,
		generation: s.nextGen, mids: make(map[int32]struct{}), writeContext: writeContext, cancelWrite: cancelWrite,
		writeExpires: now.Add(s.client.managerConfig.Transfer.Lifetime),
	}
	ownedLease = nil
	s.records[operation] = record
	s.byID[id] = record
	s.metadata += charge
	if err := s.syncControlsLocked(record, now); err != nil {
		outputs = append(outputs, s.client.manager.Cancel(id, err)...)
	}
	s.client.mu.Unlock()
	return outputs, true
}

func (s *qblockServer) receiveLocked(record *qblockServerRecord, fragment qblock.Fragment, msg *pool.Message) []qblock.Output {
	if fragment.Metadata.Size != record.metadata.Size || fragment.Metadata.SZX != record.metadata.SZX || fragment.Metadata.HasContentFormat != record.metadata.HasContentFormat || fragment.Metadata.ContentFormat != record.metadata.ContentFormat || !bytes.Equal(fragment.Metadata.Identity, record.metadata.Identity) {
		return s.client.manager.Cancel(record.id, errors.New("q-block request metadata changed"))
	}
	key := string(fragment.Token)
	if _, ok := record.tokens[key]; !ok {
		if err := s.client.cc.claimToken(fragment.Token, tokenOwnerQBlock); err != nil {
			return nil
		}
		if err := s.client.manager.BindToken(record.id, fragment.Token); err != nil {
			s.client.cc.releaseToken(fragment.Token, tokenOwnerQBlock)
			return nil
		}
		record.tokens[key] = bytes.Clone(fragment.Token)
	}
	now := s.client.now()
	before, _ := s.client.manager.ReceiverProgress(record.id)
	outputs, err := s.client.manager.Receive(fragment, now)
	if err != nil {
		return s.client.manager.Cancel(record.id, err)
	}
	after, _ := s.client.manager.ReceiverProgress(record.id)
	s.acceptPacingFeedbackLocked(record, msg, after > before)
	record.replyToken = bytes.Clone(fragment.Token)
	if err := s.syncControlsLocked(record, now); err != nil {
		return append(outputs, s.client.manager.Cancel(record.id, err)...)
	}
	return outputs
}

// syncControlsLocked copies the current deferred Q1 control revision and its
// accepted reply token into the server record's existing shared work slot.
func (s *qblockServer) syncControlsLocked(record *qblockServerRecord, _ time.Time) error {
	if record == nil || record.terminal || record.workID == 0 || s.byID[record.id] != record {
		return nil
	}
	slot := s.client.workQueue.slots[record.workID]
	if slot == nil {
		return qblock.ErrUnknownTransfer
	}
	intents := s.client.manager.PendingControls(record.id)
	if len(intents) == 0 {
		if slot.pending != nil && slot.pending.Server && slot.pending.Kind == qblockWorkControls {
			s.client.workQueue.clearPending(record.workID)
		}
		return nil
	}
	if slot.pending != nil && slot.pending.Server && slot.pending.Kind == qblockWorkControls && len(slot.pending.Controls) != 0 {
		current := slot.pending.Controls[0].Intent.Revision
		for _, intent := range intents {
			if intent.Revision == current {
				return nil
			}
		}
	}
	controls := make([]qblockControlWork, 0, len(intents))
	nextKey := s.client.nextProbeKey
	for _, intent := range intents {
		control := qblockControlWork{Intent: intent, ReplyToken: bytes.Clone(record.replyToken)}
		if intent.Action.Kind == qblock.RequestMissing {
			if nextKey == qblockProbeKey(math.MaxUint64) {
				return qblock.ErrLimitExceeded
			}
			nextKey++
			control.ProbeKey = nextKey
		}
		controls = append(controls, control)
	}
	if len(controls) == 0 {
		return nil
	}
	work := qblockPendingWork{
		Kind: qblockWorkControls, Server: true, Operation: record.activeOperation,
		TransferID: record.id, Generation: record.generation,
		Expires: record.writeExpires, ProbeKey: controls[0].ProbeKey,
		Ungated:  controls[0].Intent.Action.Kind == qblock.SendContinue,
		Controls: controls,
	}
	if err := s.client.workQueue.replace(record.workID, work, slot.pending != nil); err != nil {
		return err
	}
	s.client.nextProbeKey = nextKey
	return nil
}

func serverQ1Fragment(msg *pool.Message) (qblock.Fragment, message.Options, error) {
	if msg.HasOption(message.Block1) || msg.HasOption(message.Block2) {
		return qblock.Fragment{}, nil, errors.New("mixed q-block request options")
	}
	if _, err := serverUploadResponseHint(msg); err != nil {
		return qblock.Fragment{}, nil, err
	}
	if err := qblock.ValidateOptions(msg.Options(), true); err != nil {
		return qblock.Fragment{}, nil, err
	}
	if qblockOptionCount(msg, message.QBlock1) != 1 || qblockOptionCount(msg, message.Size1) != 1 || qblockOptionCount(msg, message.RequestTag) == 0 {
		return qblock.Fragment{}, nil, errors.New("q-block request requires QBlock1, Size1, and Request-Tag")
	}
	value, err := msg.GetOptionUint32(message.QBlock1)
	if err != nil {
		return qblock.Fragment{}, nil, err
	}
	block, err := qblock.DecodeBlock(value)
	if err != nil {
		return qblock.Fragment{}, nil, err
	}
	size, err := msg.GetOptionUint32(message.Size1)
	if err != nil {
		return qblock.Fragment{}, nil, err
	}
	payload := []byte(nil)
	if body := msg.Body(); body != nil {
		payload, err = readQBlockBody(body, uint32(16)<<block.SZX)
		if err != nil {
			return qblock.Fragment{}, nil, err
		}
	}
	metadata := qblock.Metadata{Size: size, SZX: block.SZX, Identity: []byte("request")}
	if msg.HasOption(message.ContentFormat) {
		format, err := msg.ContentFormat()
		if err != nil {
			return qblock.Fragment{}, nil, err
		}
		metadata.HasContentFormat = true
		metadata.ContentFormat = format
	}
	body, err := qblock.NewBody(metadata, ^uint32(0))
	if err != nil {
		return qblock.Fragment{}, nil, err
	}
	if _, err = body.Add(metadata, block, payload); err != nil {
		return qblock.Fragment{}, nil, err
	}
	options, err := canonicalServerRequestOptions(msg.Options())
	if err != nil {
		return qblock.Fragment{}, nil, err
	}
	return qblock.Fragment{Token: bytes.Clone(msg.Token()), Kind: qblock.Q1, Metadata: metadata, Block: block, Payload: payload}, options, nil
}

func canonicalServerRequestOptions(opts message.Options) (message.Options, error) {
	copy := cloneQBlockOptions(opts)
	for _, id := range []message.OptionID{message.QBlock1, message.QBlock2, message.Size1, message.Size2, message.Block1, message.Block2, message.ETag} {
		copy = copy.Remove(id)
	}
	return cloneQBlockOptions(copy), nil
}

func serverRequestKey(code codes.Code, opts message.Options) (qblock.OperationKey, error) {
	encoded := make([]byte, 0, int(optionsSize(opts))+2)
	encoded = append(encoded, byte(code))
	for _, option := range opts {
		if len(option.Value) > 0xffff {
			return "", errors.New("request option is too large")
		}
		var header [4]byte
		binary.BigEndian.PutUint16(header[:2], uint16(option.ID))
		binary.BigEndian.PutUint16(header[2:], uint16(len(option.Value)))
		encoded = append(encoded, header[:]...)
		encoded = append(encoded, option.Value...)
	}
	return qblock.NewOperationKey([]byte("server-q1"), encoded)
}

func optionsSize(opts message.Options) uint64 {
	size, _ := qblockOptionBytes(opts)
	return size
}

func serverUploadResponseHint(msg *pool.Message) (*qblock.Block, error) {
	if !msg.HasOption(message.QBlock2) {
		return nil, nil
	}
	if qblockOptionCount(msg, message.QBlock2) != 1 {
		return nil, errors.New("upload requires one response ceiling")
	}
	value, err := msg.GetOptionUint32(message.QBlock2)
	if err != nil {
		return nil, err
	}
	block, err := qblock.DecodeBlock(value)
	if err != nil {
		return nil, err
	}
	if block.Number != 0 || !block.More {
		return nil, errors.New("upload response ceiling requires NUM0/M1")
	}
	return &block, nil
}
