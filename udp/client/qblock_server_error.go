package client

import (
	"bytes"
	"context"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/noresponse"
	"github.com/plgd-dev/go-coap/v3/message/pool"
)

func qblockApplicationError(code codes.Code) bool { return code >= 128 && code < 192 }

func qblockSuppressResponse(options message.Options, code codes.Code) bool {
	value, err := options.GetUint32(message.NoResponse)
	return err == nil && noresponse.IsNoResponseCode(code, value) != nil
}

// Errors are one ordinary datagram. Never truncate a diagnostic body or turn
// an application error into a Q2 representation that the client cannot accept.
func (c *qblockClient) setOrdinaryError(msg *pool.Message, code codes.Code, options message.Options, payload []byte) {
	msg.SetType(message.NonConfirmable)
	msg.SetCode(code)
	msg.ResetOptionsTo(options)
	for _, id := range []message.OptionID{message.QBlock1, message.QBlock2, message.Block1, message.Block2, message.Size1, message.Size2, message.Observe} {
		msg.Remove(id)
	}
	valid := true
	for _, opt := range msg.Options() {
		if def, ok := message.CoapOptionDefs[opt.ID]; ok && (uint32(len(opt.Value)) < def.MinLen || uint32(len(opt.Value)) > def.MaxLen) {
			valid = false
			break
		}
	}
	if len(payload) > 0 {
		msg.SetBody(bytes.NewReader(payload))
	}
	size, err := qblockDatagramSize(msg)
	if !valid || err != nil || size > uint64(c.datagramLimit) {
		msg.ResetOptionsTo(nil)
		msg.SetBody(nil)
	}
}

// The caller holds mu and the record's handler lease through the eventual
// detached write, even when close/expiry releases the duplicate record.
func (s *qblockServer) prepareErrorLocked(record *qblockServerRecord, code codes.Code, options message.Options, payload []byte) *pool.Message {
	if qblockSuppressResponse(record.options, code) {
		return nil
	}
	c := s.client
	msg := c.cc.AcquireMessage(record.writeContext)
	msg.SetToken(record.replyToken)
	msg.SetControlMessage(cloneQBlockControl(record.requestControl))
	mid := c.cc.GetMessageID()
	if _, exists := c.cc.midHandlerContainer.Load(mid); exists {
		c.cc.ReleaseMessage(msg)
		return nil
	}
	if err := s.bindMIDLocked(record, mid); err != nil {
		c.cc.ReleaseMessage(msg)
		return nil
	}
	msg.SetMessageID(mid)
	c.setOrdinaryError(msg, code, options, payload)
	return msg
}

// Missing required Q1 metadata gets a bounded ordinary error without admitting
// a transfer or claiming the incoming token. A live matching operation is untouched.
func (c *qblockClient) rejectMissingQ1Metadata(req *pool.Message) bool {
	if req.Type() != message.NonConfirmable || (req.Code() != codes.POST && req.Code() != codes.PUT) || (req.HasOption(message.RequestTag) && req.HasOption(message.Size1)) {
		return false
	}
	return c.rejectQ1Error(req, codes.BadRequest, nil)
}

// An announced Q1 body above the configured limit cannot be assembled. Reject
// it before receiver admission, and advertise the maximum body size in the
// bounded ordinary 4.13 response when it fits in the response datagram.
func (c *qblockClient) rejectOversizedQ1(req *pool.Message) bool {
	if req.Type() != message.NonConfirmable || (req.Code() != codes.POST && req.Code() != codes.PUT) || !req.HasOption(message.RequestTag) || !req.HasOption(message.Size1) {
		return false
	}
	maxSize := c.managerConfig.Transfer.MaxBodySize
	return c.rejectQ1Error(req, codes.RequestEntityTooLarge, &maxSize)
}

func (c *qblockClient) rejectQ1Error(req *pool.Message, code codes.Code, responseSize1 *uint32) bool {
	if qblockSuppressResponse(req.Options(), code) {
		return true
	}
	c.lockAction()
	c.mu.Lock()
	if c.closed || c.server == nil || c.server.closed {
		c.mu.Unlock()
		c.actionMu.Unlock()
		return true
	}
	release, err := c.ownedBudget.acquire(c.ownedBudget.serverCost)
	if err != nil {
		c.mu.Unlock()
		c.actionMu.Unlock()
		return true
	}
	ctx, cancel := context.WithTimeout(c.writeContext, c.managerConfig.Transfer.Lifetime)
	msg := c.cc.AcquireMessage(ctx)
	msg.SetToken(req.Token())
	msg.SetControlMessage(cloneQBlockControl(req.ControlMessage()))
	admitted := false
	for range 32 {
		mid := c.cc.GetMessageID()
		if _, exists := c.cc.midHandlerContainer.Load(mid); exists {
			continue
		}
		if c.reserveMIDLocked(mid) == nil {
			msg.SetMessageID(mid)
			c.terminalWriteMIDs[mid] = struct{}{}
			admitted = true
			break
		}
	}
	c.setOrdinaryError(msg, code, nil, nil)
	if responseSize1 != nil {
		msg.SetOptionUint32(message.Size1, *responseSize1)
		// Include the endpoint's control options in the bound calculation, as
		// writeQBlockMessage will do before writing the response.
		c.cc.upsertControlInformation(msg)
		if size, err := qblockDatagramSize(msg); err != nil || size > uint64(c.datagramLimit) {
			msg.Remove(message.Size1)
		}
	}
	c.mu.Unlock()
	c.actionMu.Unlock()
	if admitted {
		_ = c.writeQBlockMessage(msg)
		c.mu.Lock()
		delete(c.terminalWriteMIDs, msg.MessageID())
		c.mu.Unlock()
	}
	c.cc.ReleaseMessage(msg)
	cancel()
	release()
	return true
}

// Complete an owned initial GET on an ordinary terminal error without creating
// a receiver. The callback keeps the existing bounded client envelope alive.
func (c *qblockClient) completeGETErrorLocked(msg *pool.Message) ([]qblockCallback, bool) {
	exchange := c.exchangesByOriginalToken[string(msg.Token())]
	if exchange == nil || exchange.requestCode != codes.GET || exchange.finished || len(exchange.transfers) != 0 || !qblockApplicationError(msg.Code()) || msg.Type() != message.NonConfirmable || msg.HasOption(message.Block1) || msg.HasOption(message.Block2) {
		return nil, false
	}
	response := c.cc.AcquireMessage(c.cc.Context())
	if err := msg.Clone(response); err != nil {
		c.cc.ReleaseMessage(response)
		return c.failPendingGETLocked(exchange, err), true
	}
	handler, ok := c.cc.tokenHandlerContainer.LoadAndDelete(exchange.originalToken.Hash())
	c.acceptPacingFeedbackLocked(exchange.initialProbeKey)
	exchange.finished = true
	delete(c.exchangesByOriginalToken, string(exchange.originalToken))
	c.clearPendingGETMIDsLocked(exchange)
	c.releasePacingWorkLocked(exchange.workID)
	exchange.closeRequestContext()
	if !ok {
		c.cc.ReleaseMessage(response)
		exchange.releaseCallbackSlot()
		return nil, true
	}
	response.SetToken(exchange.originalToken)
	return []qblockCallback{{run: func() {
		defer exchange.releaseCallbackSlot()
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if closed {
			c.cc.ReleaseMessage(response)
			return
		}
		handler(nil, response)
	}, discard: func() { c.cc.ReleaseMessage(response); exchange.releaseCallbackSlot() }}}, true
}
