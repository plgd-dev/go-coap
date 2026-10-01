package client

import (
	"bytes"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
)

// qblockProbeCorrelation is the one attempted packet (or current body set)
// whose validated response may release the connection's scalar debt. It is
// retained under the owning work slot's reserved control capacity.
type qblockProbeCorrelation struct {
	key        qblockProbeKey
	kind       qblockProbeKind
	server     bool
	workID     qblockWorkID
	generation uint64
	transferID qblock.TransferID
	token      message.Token
	set        uint32
	numbers    []uint32
}

func (c *qblockClient) releasePacingWorkLocked(id qblockWorkID) {
	if c.currentProbe != nil && c.currentProbe.workID == id {
		c.probeGate.settle(c.currentProbe.key, c.now())
		c.currentProbe = nil
	} else if slot := c.workQueue.slots[id]; slot != nil && slot.pending != nil {
		// A reservation canceled before its first write has zero debt and
		// should release its active gate immediately.
		c.probeGate.settle(slot.pending.ProbeKey, c.now())
	}
	c.workQueue.release(id)
}

func (c *qblockClient) clearExpiredPacingProbeLocked(now time.Time) {
	c.probeGate.ready(now)
	if c.currentProbe != nil && c.probeGate.key != c.currentProbe.key {
		c.currentProbe = nil
	}
}

func (c *qblockClient) recordPacingAttemptLocked(key qblockProbeKey, msg *pool.Message) {
	var id qblockWorkID
	var generation uint64
	var transferID qblock.TransferID
	var server bool
	for candidateID, slot := range c.workQueue.slots {
		if slot.pending != nil && slot.pending.ProbeKey == key {
			id, generation, transferID = candidateID, slot.pending.Generation, slot.pending.TransferID
			server = slot.pending.Server
			break
		}
	}
	if id == 0 {
		for _, transfer := range c.transfers {
			if transfer.bodyProbeKey == key && transfer.exchange != nil && c.workQueue.slots[transfer.exchange.workID] != nil {
				id, generation, transferID = transfer.exchange.workID, transfer.exchange.generation, transfer.id
				break
			}
		}
	}
	if id == 0 && c.server != nil {
		for _, record := range c.server.byID {
			if record.bodyProbeKey == key && record.workID != 0 && c.workQueue.slots[record.workID] != nil {
				id, generation, transferID, server = record.workID, record.generation, record.id, true
				break
			}
		}
	}
	if id == 0 {
		return
	}
	probe := &qblockProbeCorrelation{
		key: key, kind: c.probeGate.kind, server: server, workID: id, generation: generation,
		transferID: transferID, token: bytes.Clone(msg.Token()),
	}
	optionID := message.QBlock2
	if probe.kind == qblockProbeBody && !probe.server {
		optionID = message.QBlock1
	}
	if value, err := msg.GetOptionUint32(optionID); err == nil {
		if block, err := qblock.DecodeBlock(value); err == nil {
			probe.set = block.Number / c.managerConfig.Transfer.MaxPayloads
			probe.numbers = []uint32{block.Number}
		}
	}
	if probe.server && probe.kind == qblockProbeControl && len(probe.numbers) == 0 {
		if slot := c.workQueue.slots[id]; slot != nil && slot.pending != nil && len(slot.pending.Controls) != 0 {
			probe.numbers = slices.Clone(slot.pending.Controls[0].Intent.Action.Numbers)
			if len(probe.numbers) != 0 {
				probe.set = probe.numbers[0] / c.managerConfig.Transfer.MaxPayloads
			}
		}
	}
	if probe.kind == qblockProbeBody && c.currentProbe != nil && c.currentProbe.key == key && c.currentProbe.set == probe.set {
		probe.numbers = append(append([]uint32(nil), c.currentProbe.numbers...), probe.numbers...)
		if uint32(len(probe.numbers)) > c.managerConfig.Transfer.MaxPayloads {
			probe.numbers = probe.numbers[len(probe.numbers)-int(c.managerConfig.Transfer.MaxPayloads):]
		}
	}
	c.currentProbe = probe
}

// qblockDatagramSize measures the encoded UDP payload without leaving a
// pooled message's body reader at a different position for the later write.
func qblockDatagramSize(msg *pool.Message) (uint64, error) {
	if !message.ValidateMID(msg.MessageID()) {
		return 0, fmt.Errorf("invalid MessageID(%v)", msg.MessageID())
	}
	if !message.ValidateType(msg.Type()) {
		return 0, fmt.Errorf("invalid Type(%v)", msg.Type())
	}
	return qblockIncomingSize(msg)
}

func (c *qblockClient) drivePending(now time.Time) {
	c.lockAction()
	callbacks := c.executePendingOrdered(now)
	c.actionMu.Unlock()
	for _, callback := range callbacks {
		callback.run()
	}
	c.notifyDeadlineChanged()
}

// syncPacingControlsLocked snapshots only the currently owned receiver
// revision. A partially transmitted batch keeps its packet index until the
// receiver replaces that revision with new progress.
func (c *qblockClient) syncPacingControlsLocked(id qblock.TransferID, _ time.Time) error {
	transfer := c.transfers[id]
	if transfer == nil || transfer.kind != qblock.Q2 || transfer.exchange == nil {
		return nil
	}
	slot := c.workQueue.slots[transfer.exchange.workID]
	if slot == nil {
		return qblock.ErrUnknownTransfer
	}
	intents := c.manager.PendingControls(id)
	if len(intents) == 0 {
		if slot.pending != nil && slot.pending.Kind == qblockWorkControls {
			c.workQueue.clearPending(transfer.exchange.workID)
		}
		return nil
	}
	if slot.pending != nil && slot.pending.Kind == qblockWorkControls && len(slot.pending.Controls) != 0 {
		current := slot.pending.Controls[0].Intent.Revision
		for _, intent := range intents {
			if intent.Revision == current {
				return nil
			}
		}
	}
	var controls []qblockControlWork
	nextKey := c.nextProbeKey
	for _, intent := range intents {
		switch intent.Action.Kind {
		case qblock.SendContinue:
			if nextKey == qblockProbeKey(math.MaxUint64) {
				return qblock.ErrLimitExceeded
			}
			nextKey++
			controls = append(controls, qblockControlWork{Intent: intent, ProbeKey: nextKey})
		case qblock.RequestMissing:
			if nextKey == qblockProbeKey(math.MaxUint64) {
				return qblock.ErrLimitExceeded
			}
			nextKey++
			controls = append(controls, qblockControlWork{Intent: intent, ProbeKey: nextKey})
		}
	}
	if len(controls) == 0 {
		c.workQueue.clearPending(transfer.exchange.workID)
		return nil
	}
	work := qblockPendingWork{
		Kind: qblockWorkControls, Operation: transfer.operation, TransferID: id,
		Generation: transfer.exchange.generation, Expires: transfer.expires,
		ProbeKey:       controls[0].ProbeKey,
		RequestCode:    transfer.exchange.requestCode,
		RequestOptions: transfer.exchange.requestOpts,
		RequestToken:   transfer.exchange.originalToken,
		Controls:       controls,
	}
	retainOrder := slot.pending != nil
	if err := c.workQueue.replace(transfer.exchange.workID, work, retainOrder); err != nil {
		return err
	}
	c.nextProbeKey = nextKey
	return nil
}

// executePendingOrdered runs under actionMu. The first wired path is the
// initial GET; subsequent body and control paths join the same gate here.
func (c *qblockClient) executePendingOrdered(now time.Time) []qblockCallback {
	var callbacks []qblockCallback
	for {
		c.mu.Lock()
		if c.closed || c.workQueue == nil {
			c.mu.Unlock()
			return callbacks
		}
		c.clearExpiredPacingProbeLocked(now)
		id, work, ok := c.workQueue.next(now, c.probeGate)
		if !ok {
			c.mu.Unlock()
			return callbacks
		}
		if work.Kind == qblockWorkControls {
			var moreCallbacks []qblockCallback
			var keepScanning bool
			if work.Server {
				moreCallbacks, keepScanning = c.executeServerPacingControlOrdered(id, work, now)
			} else {
				moreCallbacks, keepScanning = c.executePacingControlOrdered(id, work, now)
			}
			callbacks = append(callbacks, moreCallbacks...)
			if keepScanning {
				continue
			}
			return callbacks
		}
		if work.Kind == qblockWorkBody {
			if work.Server {
				record := (*qblockServerRecord)(nil)
				if c.server != nil {
					record = c.server.byID[work.TransferID]
				}
				if record == nil || record.workID != id || record.generation != work.Generation || record.activeOperation != work.Operation {
					c.releasePacingWorkLocked(id)
					c.mu.Unlock()
					continue
				}
				failure := record.writeContext.Err()
				if failure == nil && !work.Expires.IsZero() && !now.Before(work.Expires) {
					failure = qblock.ErrExpired
				}
				if failure != nil {
					outputs := c.manager.Cancel(record.id, failure)
					c.mu.Unlock()
					callbacks = append(callbacks, c.executeOrdered(outputs)...)
					continue
				}
				if !c.probeGate.admit(work.ProbeKey, qblockProbeBody, work.NonProbingWait, now) {
					c.mu.Unlock()
					return callbacks
				}
				outputs, err := c.manager.ActivateSender(record.id, now)
				if err != nil {
					outputs = c.manager.Cancel(record.id, err)
				}
				c.mu.Unlock()
				callbacks = append(callbacks, c.executeOrdered(outputs)...)
				c.mu.Lock()
				if err != nil {
					c.probeGate.settle(work.ProbeKey, c.now())
				}
				if slot := c.workQueue.slots[id]; slot != nil && slot.pending != nil && slot.pending.Generation == work.Generation {
					c.workQueue.clearPending(id)
				}
				c.mu.Unlock()
				return callbacks
			}
			transfer := c.transfers[work.TransferID]
			if transfer == nil || transfer.operation != work.Operation || transfer.exchange.workID != id || transfer.exchange.generation != work.Generation {
				c.releasePacingWorkLocked(id)
				c.mu.Unlock()
				continue
			}
			failure := transfer.exchange.requestContext.Err()
			if failure == nil && !work.Expires.IsZero() && !now.Before(work.Expires) {
				failure = qblock.ErrExpired
			}
			if failure != nil {
				outputs := c.manager.Cancel(transfer.id, failure)
				c.mu.Unlock()
				callbacks = append(callbacks, c.executeOrdered(outputs)...)
				continue
			}
			if !c.probeGate.admit(work.ProbeKey, qblockProbeBody, work.NonProbingWait, now) {
				c.mu.Unlock()
				return callbacks
			}
			outputs, err := c.manager.ActivateSender(transfer.id, now)
			if err != nil {
				outputs = c.manager.Cancel(transfer.id, err)
			}
			c.mu.Unlock()
			callbacks = append(callbacks, c.executeOrdered(outputs)...)
			c.mu.Lock()
			if err != nil {
				c.probeGate.settle(work.ProbeKey, c.now())
			}
			if slot := c.workQueue.slots[id]; slot != nil && slot.pending != nil && slot.pending.Generation == work.Generation {
				c.workQueue.clearPending(id)
			}
			c.mu.Unlock()
			return callbacks
		}
		if work.Kind != qblockWorkGET {
			c.mu.Unlock()
			return callbacks
		}
		exchange := c.exchangesByOriginalToken[string(work.RequestToken)]
		if exchange == nil || exchange.workID != id || exchange.generation != work.Generation {
			c.releasePacingWorkLocked(id)
			c.mu.Unlock()
			continue
		}
		failure := exchange.requestContext.Err()
		if failure == nil && !work.Expires.IsZero() && !now.Before(work.Expires) {
			failure = qblock.ErrExpired
		}
		if failure != nil {
			callbacks = append(callbacks, c.failPendingGETLocked(exchange, failure)...)
			c.mu.Unlock()
			continue
		}
		if !c.probeGate.admit(work.ProbeKey, qblockProbeControl, 0, now) {
			c.mu.Unlock()
			return callbacks
		}
		request := c.cc.AcquireMessage(exchange.requestContext)
		request.ResetOptionsTo(work.RequestOptions)
		request.SetCode(work.RequestCode)
		request.SetToken(work.RequestToken)
		request.SetType(message.NonConfirmable)
		request.SetMessageID(c.cc.GetMessageID())
		c.mu.Unlock()

		err := c.writePacedMessage(work.ProbeKey, request)
		c.cc.ReleaseMessage(request)
		c.mu.Lock()
		c.probeGate.settle(work.ProbeKey, c.now())
		if c.exchangesByOriginalToken[string(work.RequestToken)] == exchange && exchange.workID == id {
			if err != nil {
				callbacks = append(callbacks, c.failPendingGETLocked(exchange, err)...)
			} else {
				c.workQueue.clearPending(id)
			}
		}
		c.mu.Unlock()
		return callbacks
	}
}

// executePacingControlOrdered enters with mu held and always exits unlocked.
// It sends at most one control datagram, then either resumes ungated work or
// leaves a separately paced next packet at the tail of the queue.
func (c *qblockClient) executePacingControlOrdered(id qblockWorkID, work qblockPendingWork, now time.Time) ([]qblockCallback, bool) {
	transfer := c.transfers[work.TransferID]
	if transfer == nil || transfer.kind != qblock.Q2 || transfer.operation != work.Operation || transfer.exchange.workID != id || transfer.exchange.generation != work.Generation || len(work.Controls) == 0 {
		c.releasePacingWorkLocked(id)
		c.mu.Unlock()
		return nil, true
	}
	control := work.Controls[0]
	current := false
	for _, intent := range c.manager.PendingControls(transfer.id) {
		if intent.Revision == control.Intent.Revision {
			current = true
			break
		}
	}
	if !current {
		err := c.syncPacingControlsLocked(transfer.id, now)
		if err != nil {
			outputs := c.manager.Cancel(transfer.id, err)
			c.mu.Unlock()
			return c.executeOrdered(outputs), true
		}
		c.mu.Unlock()
		return nil, true
	}
	failure := transfer.exchange.requestContext.Err()
	if failure == nil && !work.Expires.IsZero() && !now.Before(work.Expires) {
		failure = qblock.ErrExpired
	}
	if failure != nil {
		outputs := c.manager.Cancel(transfer.id, failure)
		c.mu.Unlock()
		return c.executeOrdered(outputs), true
	}
	if !c.probeGate.admit(control.ProbeKey, qblockProbeControl, 0, now) {
		c.mu.Unlock()
		return nil, false
	}
	block := qblock.Block{SZX: transfer.metadata.SZX}
	if control.Intent.Action.Kind == qblock.SendContinue {
		block.Number = control.Intent.Action.Through + 1
		block.More = true
	} else {
		if control.PacketIndex < 0 || control.PacketIndex >= len(control.Intent.Action.Numbers) {
			outputs := c.manager.Cancel(transfer.id, qblock.ErrInvalidRepair)
			c.mu.Unlock()
			return c.executeOrdered(outputs), true
		}
		block.Number = control.Intent.Action.Numbers[control.PacketIndex]
	}
	c.mu.Unlock()

	token, err := c.bindControlToken(work.TransferID)
	var request *pool.Message
	if err == nil {
		request, err = c.newControlRequest(work.TransferID, token, block)
	}
	if err == nil {
		if err = c.writeContext.Err(); err == nil {
			err = request.Context().Err()
		}
		if err == nil {
			err = c.writePacedMessage(control.ProbeKey, request)
		}
	}
	if request != nil {
		c.cc.ReleaseMessage(request)
	}
	c.mu.Lock()
	c.probeGate.settle(control.ProbeKey, c.now())
	transfer = c.transfers[work.TransferID]
	slot := c.workQueue.slots[id]
	if transfer == nil || slot == nil || slot.pending == nil || slot.pending.Generation != work.Generation || len(slot.pending.Controls) == 0 || slot.pending.Controls[0].Intent.Revision != control.Intent.Revision || slot.pending.Controls[0].PacketIndex != control.PacketIndex {
		c.mu.Unlock()
		return nil, false
	}
	if err != nil {
		outputs := c.manager.Cancel(transfer.id, err)
		c.mu.Unlock()
		return c.executeOrdered(outputs), false
	}
	var outputs []qblock.Output
	if control.Intent.Action.Kind == qblock.RequestMissing && control.PacketIndex+1 < len(control.Intent.Action.Numbers) {
		if c.nextProbeKey == qblockProbeKey(math.MaxUint64) {
			err = qblock.ErrLimitExceeded
		} else {
			c.nextProbeKey++
			work.Controls[0].PacketIndex++
			work.Controls[0].ProbeKey = c.nextProbeKey
			work.ProbeKey = c.nextProbeKey
			err = c.workQueue.replace(id, work, false)
		}
	} else {
		outputs = c.manager.CommitControl(transfer.id, control.Intent.Revision, c.now())
		remaining := work.Controls[1:]
		if len(remaining) != 0 {
			work.Controls = remaining
			work.ProbeKey = remaining[0].ProbeKey
			err = c.workQueue.replace(id, work, false)
		} else {
			c.workQueue.clearPending(id)
		}
	}
	if err != nil {
		outputs = append(outputs, c.manager.Cancel(transfer.id, err)...)
	}
	c.mu.Unlock()
	callbacks := c.executeOrdered(outputs)
	return callbacks, false
}

// executeServerPacingControlOrdered enters with mu held and exits unlocked.
// Continue responses are ungated; a NON 4.08 report is one paced datagram.
func (c *qblockClient) executeServerPacingControlOrdered(id qblockWorkID, work qblockPendingWork, now time.Time) ([]qblockCallback, bool) {
	var record *qblockServerRecord
	if c.server != nil {
		record = c.server.byID[work.TransferID]
	}
	if record == nil || record.workID != id || record.generation != work.Generation || record.activeOperation != work.Operation || len(work.Controls) == 0 {
		c.releasePacingWorkLocked(id)
		c.mu.Unlock()
		return nil, true
	}
	control := work.Controls[0]
	current := false
	for _, intent := range c.manager.PendingControls(record.id) {
		if intent.Revision == control.Intent.Revision {
			current = true
			break
		}
	}
	if !current {
		err := c.server.syncControlsLocked(record, now)
		if err != nil {
			outputs := c.manager.Cancel(record.id, err)
			c.mu.Unlock()
			return c.executeOrdered(outputs), true
		}
		c.mu.Unlock()
		return nil, true
	}
	failure := record.writeContext.Err()
	if failure == nil && !work.Expires.IsZero() && !now.Before(work.Expires) {
		failure = qblock.ErrExpired
	}
	if failure != nil {
		outputs := c.manager.Cancel(record.id, failure)
		c.mu.Unlock()
		return c.executeOrdered(outputs), true
	}
	ungated := control.Intent.Action.Kind == qblock.SendContinue
	if !ungated && !c.probeGate.admit(control.ProbeKey, qblockProbeControl, 0, now) {
		c.mu.Unlock()
		return nil, false
	}
	token := bytes.Clone(control.ReplyToken)
	writeContext := record.writeContext
	szx := record.metadata.SZX
	mid := c.cc.GetMessageID()
	c.server.bindMIDLocked(record, mid)
	c.mu.Unlock()

	err := c.writeServerQ1Control(writeContext, token, mid, szx, control.Intent.Action, control.ProbeKey)
	c.mu.Lock()
	if !ungated {
		c.probeGate.settle(control.ProbeKey, c.now())
	}
	record = c.server.byID[work.TransferID]
	slot := c.workQueue.slots[id]
	if record == nil || slot == nil || slot.pending == nil || slot.pending.Generation != work.Generation || len(slot.pending.Controls) == 0 || slot.pending.Controls[0].Intent.Revision != control.Intent.Revision {
		c.mu.Unlock()
		return nil, false
	}
	if err != nil {
		outputs := c.manager.Cancel(record.id, err)
		c.mu.Unlock()
		return c.executeOrdered(outputs), false
	}
	outputs := c.manager.CommitControl(record.id, control.Intent.Revision, c.now())
	remaining := work.Controls[1:]
	if len(remaining) != 0 {
		work.Controls = remaining
		work.ProbeKey = remaining[0].ProbeKey
		work.Ungated = remaining[0].Intent.Action.Kind == qblock.SendContinue
		err = c.workQueue.replace(id, work, false)
	} else {
		c.workQueue.clearPending(id)
	}
	if err != nil {
		outputs = append(outputs, c.manager.Cancel(record.id, err)...)
	}
	c.mu.Unlock()
	return c.executeOrdered(outputs), ungated
}

// failPendingGETLocked removes a pre-receiver exchange without spending a
// transfer slot. Its callback executes only after actionMu and mu are released.
func (c *qblockClient) failPendingGETLocked(exchange *qblockExchange, err error) []qblockCallback {
	exchange.finished = true
	exchange.failureReported = true
	delete(c.exchangesByOriginalToken, string(exchange.originalToken))
	c.releasePacingWorkLocked(exchange.workID)
	_, _ = c.cc.tokenHandlerContainer.LoadAndDelete(exchange.originalToken.Hash())
	exchange.closeRequestContext()
	if exchange.fail == nil {
		exchange.releaseCallbackSlot()
		return nil
	}
	return []qblockCallback{{
		run:     func() { defer exchange.releaseCallbackSlot(); exchange.fail(err) },
		discard: exchange.releaseCallbackSlot,
	}}
}

func (c *qblockClient) writePacedMessage(key qblockProbeKey, msg *pool.Message) error {
	if err := c.writeContext.Err(); err != nil {
		return err
	}
	if err := msg.Context().Err(); err != nil {
		return err
	}
	size, err := qblockDatagramSize(msg)
	if err != nil {
		return err
	}
	if size > uint64(c.datagramLimit) {
		return qblock.ErrLimitExceeded
	}
	c.mu.Lock()
	owned := false
	if !c.closed && c.probeGate.state == qblockProbeActive && c.probeGate.key == key {
		for _, slot := range c.workQueue.slots {
			if slot.pending != nil && slot.pending.ProbeKey == key {
				owned = true
				break
			}
		}
		if !owned {
			for _, transfer := range c.transfers {
				if transfer.bodyProbeKey == key && transfer.exchange != nil && c.workQueue.slots[transfer.exchange.workID] != nil {
					owned = true
					break
				}
			}
		}
		if !owned && c.server != nil {
			for _, record := range c.server.byID {
				if record.bodyProbeKey == key && record.workID != 0 && c.workQueue.slots[record.workID] != nil {
					owned = true
					break
				}
			}
		}
	}
	if !owned {
		c.mu.Unlock()
		return qblock.ErrCanceled
	}
	if err := msg.Context().Err(); err != nil {
		c.mu.Unlock()
		return err
	}
	c.probeGate.charge(key, size)
	c.recordPacingAttemptLocked(key, msg)
	c.mu.Unlock()
	return c.cc.session.WriteMessage(msg)
}

func (c *qblockClient) acceptPacingFeedbackLocked(key qblockProbeKey) bool {
	probe := c.currentProbe
	if probe == nil || probe.key != key || c.workQueue.slots[probe.workID] == nil {
		return false
	}
	if probe.transferID != 0 {
		if probe.server {
			if c.server == nil {
				return false
			}
			record := c.server.byID[probe.transferID]
			if record == nil || record.workID != probe.workID || record.generation != probe.generation {
				return false
			}
		} else {
			transfer := c.transfers[probe.transferID]
			if transfer == nil || transfer.exchange == nil || transfer.exchange.workID != probe.workID || transfer.exchange.generation != probe.generation {
				return false
			}
		}
	} else {
		exchange := c.exchangesByOriginalToken[string(probe.token)]
		if exchange == nil || exchange.workID != probe.workID || exchange.generation != probe.generation {
			return false
		}
	}
	if !c.probeGate.feedback(key) {
		return false
	}
	c.currentProbe = nil
	return true
}

func (c *qblockClient) matchesPacingQ1ControlLocked(transfer *qblockTransfer, control qblock.Control) bool {
	probe := c.currentProbe
	if probe == nil || probe.kind != qblockProbeBody || probe.transferID != transfer.id ||
		probe.workID != transfer.exchange.workID || probe.generation != transfer.exchange.generation {
		return false
	}
	if control.Continue != nil {
		return *control.Continue/c.managerConfig.Transfer.MaxPayloads == probe.set && slices.Contains(probe.numbers, *control.Continue)
	}
	for _, number := range control.Missing {
		if number/c.managerConfig.Transfer.MaxPayloads == probe.set && slices.Contains(probe.numbers, number) {
			return true
		}
	}
	return false
}

func (c *qblockClient) acceptPacingQ2ProgressLocked(transfer *qblockTransfer, number uint32) bool {
	probe := c.currentProbe
	if probe == nil || probe.kind != qblockProbeControl || probe.transferID != transfer.id ||
		probe.workID != transfer.exchange.workID || probe.generation != transfer.exchange.generation ||
		!slices.Contains(probe.numbers, number) {
		return false
	}
	return c.acceptPacingFeedbackLocked(probe.key)
}
