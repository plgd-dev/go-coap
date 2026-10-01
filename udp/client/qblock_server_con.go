package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"slices"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/noresponse"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
)

type qblockServerCONRecord struct {
	ackDone         chan struct{}
	mid             int32
	token           message.Token
	options         message.Options
	control         *coapNet.ControlMessage
	sequence        uint64
	block           qblock.Block
	ctx             context.Context
	cancel          context.CancelFunc
	lease           *qblockOwnedLease
	charged         uint64
	responseCharged uint64
	expires         time.Time
	running         bool
	terminal        bool
	ack             []byte
	ackDue          time.Time
	separate        bool
	response        []byte
	responseMID     int32
	permit          *qblockOrdinaryPermit
	retryDue        time.Time
	interval        time.Duration
	retries         uint32
	maxRetries      uint32
	sending         bool
	acknowledged    bool
}

func (s *qblockServer) recordCountLocked() int { return len(s.records) + len(s.conRequests) }

func sameCONRequest(r *qblockServerCONRecord, msg *pool.Message) bool {
	return msg.Code() == codes.GET && bytes.Equal(r.token, msg.Token()) && slices.EqualFunc(r.options, msg.Options(), func(a, b message.Option) bool { return a.ID == b.ID && bytes.Equal(a.Value, b.Value) })
}

func serverCONBlock(msg *pool.Message) (qblock.Block, error) {
	if msg.Code() != codes.GET || len(msg.Token()) > 8 || qblockOptionCount(msg, message.QBlock2) != 1 || msg.HasOption(message.QBlock1) || msg.HasOption(message.Block1) || msg.HasOption(message.Block2) || msg.HasOption(message.Observe) {
		return qblock.Block{}, qblock.ErrUnsupportedOperation
	}
	size, err := qblockBodySize(msg.Body())
	if err != nil || size != 0 {
		return qblock.Block{}, qblock.ErrUnsupportedOperation
	}
	for _, opt := range msg.Options() {
		if def, ok := message.CoapOptionDefs[opt.ID]; ok && (uint32(len(opt.Value)) < def.MinLen || uint32(len(opt.Value)) > def.MaxLen) {
			return qblock.Block{}, qblock.ErrUnsupportedOperation
		}
	}
	value, err := msg.GetOptionUint32(message.QBlock2)
	if err != nil {
		return qblock.Block{}, err
	}
	block, err := qblock.DecodeBlock(value)
	if err != nil || block.More {
		return qblock.Block{}, qblock.ErrUnsupportedOperation
	}
	return block, nil
}

// Admission and publication are serialized with other Q writes. The handler
// owns a retained envelope and runs without either runtime lock.
func (s *qblockServer) handleCONRequest(msg *pool.Message) bool {
	if msg.Type() != message.Confirmable || msg.Code() < 1 || msg.Code() >= 32 {
		return false
	}
	c := s.client
	c.lockAction()
	c.mu.Lock()
	r := s.conRequests[msg.MessageID()]
	if r != nil {
		var wire []byte
		if !r.terminal && !s.closed && c.writeContext.Err() == nil && c.now().Before(r.expires) && sameCONRequest(r, msg) {
			wire = r.ack
		}
		release := r.lease.retain()
		c.mu.Unlock()
		if len(wire) > 0 {
			err := s.writeCONWire(r, wire)
			c.actionMu.Unlock()
			release()
			if err != nil {
				c.cc.errors(err)
			}
			return true
		}
		c.actionMu.Unlock()
		release()
		return true
	}
	if !msg.HasOption(message.QBlock1) && !msg.HasOption(message.QBlock2) {
		c.mu.Unlock()
		c.actionMu.Unlock()
		return false
	}
	if s.closed || c.closed {
		c.mu.Unlock()
		c.actionMu.Unlock()
		return true
	}
	c.mu.Unlock()
	block, err := serverCONBlock(msg)
	if err != nil {
		err := s.rejectCON(msg, codes.BadOption)
		c.actionMu.Unlock()
		if err != nil {
			c.cc.errors(err)
		}
		return true
	}
	c.mu.Lock()
	charge := optionsSize(msg.Options()) + uint64(len(msg.Token()))
	release, err := c.ownedBudget.acquire(c.ownedBudget.serverCost)
	if err != nil || uint64(s.recordCountLocked()) >= uint64(s.config.MaxRecords) || charge > s.config.MaxMetadataBytes-s.metadata {
		if release != nil {
			release()
		}
		c.mu.Unlock()
		err := s.rejectCON(msg, codes.ServiceUnavailable)
		c.actionMu.Unlock()
		if err != nil {
			c.cc.errors(err)
		}
		return true
	}
	ctx, cancel := context.WithCancel(c.writeContext)
	r = &qblockServerCONRecord{mid: msg.MessageID(), token: cloneQBlockBytes(msg.Token()), options: cloneQBlockOptions(msg.Options()), control: cloneQBlockControl(msg.ControlMessage()), sequence: msg.Sequence(), block: block, ctx: ctx, cancel: cancel, lease: newQBlockOwnedLease(release), charged: charge, expires: c.now().Add(ExchangeLifetime), running: true}
	r.ackDone = make(chan struct{})
	r.ackDue = c.now().Add(min(time.Second, max(time.Nanosecond, c.cc.transmission.acknowledgeTimeout.Load()/2)))
	s.conRequests[r.mid] = r
	s.metadata += charge
	handlerRelease := r.lease.retain()
	c.mu.Unlock()
	c.actionMu.Unlock()
	c.notifyDeadlineChanged()
	defer handlerRelease()
	req := c.cc.AcquireMessage(ctx)
	resp := c.cc.AcquireMessage(ctx)
	defer c.cc.ReleaseMessage(req)
	req.SetCode(codes.GET)
	req.SetType(message.Confirmable)
	req.SetMessageID(r.mid)
	req.SetToken(r.token)
	req.SetSequence(r.sequence)
	req.ResetOptionsTo(r.options)
	req.SetControlMessage(cloneQBlockControl(r.control))
	writer := responsewriter.New(resp, c.cc, r.options...)
	if s.handler != nil {
		s.handler(writer, req)
	}
	defer c.cc.ReleaseMessage(writer.Message())
	wire := s.captureCONResponse(r, writer.Message())
	c.lockAction()
	c.mu.Lock()
	r.running = false
	current := s.conRequests[r.mid] == r && !r.terminal && !s.closed && c.writeContext.Err() == nil && c.now().Before(r.expires)
	if current {
		decoded := c.cc.AcquireMessage(r.ctx)
		_, decodeErr := decoded.UnmarshalWithDecoder(qblock.Decoder{}, wire)
		responseCharge := optionsSize(decoded.Options())
		c.cc.ReleaseMessage(decoded)
		if decodeErr != nil || responseCharge > s.config.MaxMetadataBytes-s.metadata {
			wire = s.simpleCONWire(r, codes.InternalServerError)
			responseCharge = 0
		}
		r.responseCharged = responseCharge
		s.metadata += responseCharge
	}
	separate := current && r.separate
	if current && !separate {
		r.ack = wire
		r.ackDue = time.Time{}
	} else if !current {
		s.releaseCONLocked(r)
	}
	c.mu.Unlock()
	var writeErr error
	if current && !separate {
		writeErr = s.writeCONWire(r, wire)
	}
	c.actionMu.Unlock()
	if writeErr != nil {
		c.cc.errors(writeErr)
	}
	if separate {
		s.sendSeparateCON(r, wire)
	}
	c.notifyDeadlineChanged()
	return true
}

func (s *qblockServer) rejectCON(req *pool.Message, code codes.Code) error {
	r := &qblockServerCONRecord{mid: req.MessageID(), token: req.Token(), control: req.ControlMessage(), ctx: s.client.writeContext}
	if v, err := req.GetOptionUint32(message.NoResponse); err == nil && noresponse.IsNoResponseCode(code, v) != nil {
		code = codes.Empty
	}
	return s.writeCONWire(r, s.simpleCONWire(r, code))
}

func (s *qblockServer) simpleCONWire(r *qblockServerCONRecord, code codes.Code) []byte {
	msg := s.client.cc.AcquireMessage(r.ctx)
	defer s.client.cc.ReleaseMessage(msg)
	msg.SetType(message.Acknowledgement)
	msg.SetMessageID(r.mid)
	msg.SetCode(code)
	if code != codes.Empty {
		msg.SetToken(r.token)
	}
	wire, _ := msg.MarshalWithEncoder(coder.DefaultCoder)
	return cloneQBlockBytes(wire)
}

func (s *qblockServer) captureCONResponse(r *qblockServerCONRecord, resp *pool.Message) []byte {
	if !resp.IsModified() {
		return s.simpleCONWire(r, codes.Empty)
	}
	if v, err := r.options.GetUint32(message.NoResponse); err == nil && noresponse.IsNoResponseCode(resp.Code(), v) != nil {
		return s.simpleCONWire(r, codes.Empty)
	}
	code := resp.Code()
	if code < 64 {
		return s.simpleCONWire(r, codes.InternalServerError)
	}
	for _, opt := range resp.Options() {
		if def, ok := message.CoapOptionDefs[opt.ID]; ok && (uint32(len(opt.Value)) < def.MinLen || uint32(len(opt.Value)) > def.MaxLen) {
			return s.simpleCONWire(r, codes.InternalServerError)
		}
	}
	if qblockOptionCount(resp, message.ETag) > 1 {
		return s.simpleCONWire(r, codes.InternalServerError)
	}
	optionBytes, err := qblockOptionBytes(resp.Options())
	if err != nil || optionBytes > s.config.MaxMetadataBytes || s.client.preflightOwnedOptions(resp.Options()) != nil {
		return s.simpleCONWire(r, codes.InternalServerError)
	}
	block := r.block
	offset := uint64(block.Number) * (uint64(16) << block.SZX)
	if code != codes.Content {
		offset = 0
		block.Number = 0
	}
	block.SZX = min(block.SZX, s.client.cc.blockwiseSZX)
	block.Number = uint32(offset / (uint64(16) << block.SZX))
	if block.Number > 0xfffff {
		return s.simpleCONWire(r, codes.BadRequest)
	}
	payload, size, digest, err := captureCONBody(resp.Body(), s.client.managerConfig.Transfer.MaxBodySize, offset, func() uint32 {
		if code != codes.Content {
			return uint32(s.client.datagramLimit)
		}
		return uint32(16) << block.SZX
	}())
	if err != nil {
		return s.simpleCONWire(r, codes.InternalServerError)
	}
	if offset > 0 && offset >= uint64(size) {
		return s.simpleCONWire(r, codes.BadRequest)
	}
	out := s.client.cc.AcquireMessage(r.ctx)
	defer s.client.cc.ReleaseMessage(out)
	out.SetCode(code)
	out.SetType(message.Acknowledgement)
	out.SetMessageID(r.mid)
	out.SetToken(r.token)
	opts := make(message.Options, 0, len(resp.Options()))
	for _, opt := range resp.Options() {
		switch opt.ID {
		case message.QBlock1, message.QBlock2, message.Block1, message.Block2, message.Observe, message.Size2:
			continue
		}
		opts = append(opts, opt)
	}
	out.ResetOptionsTo(opts)
	if code == codes.Content {
		block.More = offset+uint64(len(payload)) < uint64(size)
		value, _ := qblock.EncodeBlock(block)
		out.SetOptionUint32(message.QBlock2, value)
		out.SetOptionUint32(message.Size2, size)
		if !out.HasOption(message.ETag) {
			out.SetOptionBytes(message.ETag, digest)
		}
	} else {
		// Error responses are bounded by the datagram and have no Q metadata.
		if size > uint32(len(payload)) {
			return s.simpleCONWire(r, code)
		}
	}
	for {
		out.SetBody(bytes.NewReader(payload))
		wire, err := out.MarshalWithEncoder(coder.DefaultCoder)
		if err == nil && len(wire) <= int(s.client.datagramLimit) {
			return cloneQBlockBytes(wire)
		}
		if code != codes.Content {
			return s.simpleCONWire(r, code)
		}
		if block.SZX == 0 {
			return s.simpleCONWire(r, codes.RequestEntityTooLarge)
		}
		block.SZX--
		block.Number = uint32(offset / (uint64(16) << block.SZX))
		if block.Number > 0xfffff {
			return s.simpleCONWire(r, codes.BadRequest)
		}
		payload = payload[:min(len(payload), int(16)<<block.SZX)]
		block.More = offset+uint64(len(payload)) < uint64(size)
		value, _ := qblock.EncodeBlock(block)
		out.SetOptionUint32(message.QBlock2, value)
	}

}

// Stream the representation once, retaining only the selected block and a
// fixed scratch buffer; neither claimed size nor cursor drives allocations.
func captureCONBody(body io.ReadSeeker, limit uint32, offset uint64, blockSize uint32) (payload []byte, size uint32, digest []byte, err error) {
	hash := sha256.New()
	if body == nil {
		return nil, 0, hash.Sum(nil)[:8], nil
	}
	pos, err := body.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, 0, nil, err
	}
	defer func() { _, restore := body.Seek(pos, io.SeekStart); err = errors.Join(err, restore) }()
	if _, err = body.Seek(0, io.SeekStart); err != nil {
		return nil, 0, nil, err
	}
	var scratch [1024]byte
	payload = make([]byte, 0, blockSize)
	var total uint64
	reader := io.LimitReader(body, int64(limit)+1)
	for {
		n, e := reader.Read(scratch[:])
		if n > 0 {
			hash.Write(scratch[:n])
			begin := max(total, offset)
			end := min(total+uint64(n), offset+uint64(blockSize))
			if end > begin {
				payload = append(payload, scratch[begin-total:end-total]...)
			}
			total += uint64(n)
			if total > uint64(limit) {
				return nil, 0, nil, qblock.ErrLimitExceeded
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, 0, nil, e
		}
		if n == 0 {
			return nil, 0, nil, io.ErrNoProgress
		}
	}
	return payload, uint32(total), hash.Sum(nil)[:8], nil
}

func (s *qblockServer) writeCONWire(r *qblockServerCONRecord, wire []byte) error {
	if r.ctx.Err() != nil || len(wire) == 0 || len(wire) > int(s.client.datagramLimit) {
		return nil
	}
	msg := s.client.cc.AcquireMessage(r.ctx)
	defer s.client.cc.ReleaseMessage(msg)
	if _, err := msg.UnmarshalWithDecoder(qblock.Decoder{}, wire); err != nil {
		return nil
	}
	if r.control != nil {
		msg.SetControlMessage(&coapNet.ControlMessage{Src: cloneQBlockBytes(r.control.Dst), IfIndex: r.control.IfIndex})
	}
	return s.client.cc.session.WriteMessage(msg)
}

func (s *qblockServer) nextCONDeadlineLocked() (time.Time, bool) {
	var next time.Time
	for _, r := range s.conRequests {
		if r.terminal {
			continue
		}
		for _, deadline := range []time.Time{r.expires, r.ackDue, r.retryDue} {
			if !deadline.IsZero() && (next.IsZero() || deadline.Before(next)) {
				next = deadline
			}
		}
	}
	return next, !next.IsZero()
}
func (s *qblockServer) releaseCONLocked(r *qblockServerCONRecord) {
	r.terminal = true
	r.ackDue = time.Time{}
	r.retryDue = time.Time{}
	s.settleCONLocked(r)
	r.cancel()
	if r.running {
		return
	}
	if s.conRequests[r.mid] == r {
		delete(s.conRequests, r.mid)
		s.metadata -= r.charged + r.responseCharged
		r.lease.drop()
	}
}
func (s *qblockServer) expireCONLocked(now time.Time) {
	for _, r := range s.conRequests {
		if !now.Before(r.expires) {
			s.releaseCONLocked(r)
		}
	}
}
func (s *qblockServer) closeCONLocked() {
	for _, r := range s.conRequests {
		s.releaseCONLocked(r)
	}
}

// settleCONLocked unpublishes feedback before releasing endpoint ownership.
func (s *qblockServer) settleCONLocked(r *qblockServerCONRecord) {
	if s.conByMID[r.responseMID] == r {
		delete(s.conByMID, r.responseMID)
	}
	r.acknowledged = true
	r.retryDue = time.Time{}
	if r.permit != nil {
		r.permit.finish(false, s.client.now())
		r.permit = nil
	}
}

func (s *qblockServer) handleCONFeedback(msg *pool.Message) bool {
	if msg.Type() != message.Acknowledgement && msg.Type() != message.Reset {
		return false
	}
	c := s.client
	c.lockAction()
	c.mu.Lock()
	r := s.conByMID[msg.MessageID()]
	if r == nil {
		c.mu.Unlock()
		c.actionMu.Unlock()
		return false
	}
	if r.permit != nil {
		r.permit.finish(true, c.now())
	}
	s.settleCONLocked(r)
	c.mu.Unlock()
	c.actionMu.Unlock()
	c.notifyDeadlineChanged()
	return true
}

func (s *qblockServer) sendSeparateCON(r *qblockServerCONRecord, wire []byte) {
	c := s.client
	select {
	case <-r.ackDone:
	case <-r.ctx.Done():
		return
	}
	msg := c.cc.AcquireMessage(r.ctx)
	defer c.cc.ReleaseMessage(msg)
	if _, err := msg.UnmarshalWithDecoder(qblock.Decoder{}, wire); err != nil || msg.Code() == codes.Empty {
		return
	}
	msg.SetType(message.Confirmable)
	c.lockAction()
	c.mu.Lock()
	if r.terminal || s.closed || !c.now().Before(r.expires) {
		c.mu.Unlock()
		c.actionMu.Unlock()
		return
	}
	var mid int32
	var err error
	for range 32 {
		mid = c.cc.GetMessageID()
		_, ordinary := c.cc.midHandlerContainer.Load(mid)
		err = c.reserveMIDLocked(mid)
		if !ordinary && err == nil {
			break
		}
		err = qblock.ErrLimitExceeded
	}
	// Reserve local namespace before waiting for endpoint admission.
	if err != nil {
		c.mu.Unlock()
		c.actionMu.Unlock()
		c.cc.errors(err)
		return
	}
	r.responseMID = mid
	r.acknowledged = false
	s.conByMID[mid] = r
	c.mu.Unlock()
	c.actionMu.Unlock()
	msg.SetMessageID(mid)
	permit, err := c.cc.acquireOrdinary(msg)
	if err != nil {
		c.lockAction()
		c.mu.Lock()
		s.settleCONLocked(r)
		c.mu.Unlock()
		c.actionMu.Unlock()
		c.cc.errors(err)
		return
	}
	c.lockAction()
	c.mu.Lock()
	if r.terminal || s.closed || r.acknowledged || !c.now().Before(r.expires) {
		if permit != nil {
			permit.finish(false, c.now())
		}
		s.settleCONLocked(r)
		c.mu.Unlock()
		c.actionMu.Unlock()
		return
	}
	r.permit = permit
	if permit != nil {
		permit.serverResponse = true
	}
	r.response, _ = msg.MarshalWithEncoder(coder.DefaultCoder)
	r.response = cloneQBlockBytes(r.response)
	r.interval = time.Duration(float64(c.cc.transmission.acknowledgeTimeout.Load()) * (1 + c.jitter()/2))
	r.interval = max(time.Nanosecond, r.interval)
	r.maxRetries = c.cc.transmission.maxRetransmit.Load()
	r.retryDue = c.now().Add(r.interval)
	c.mu.Unlock()
	writeErr := s.writeSeparateCON(r)
	c.actionMu.Unlock()
	if writeErr != nil {
		c.cc.errors(writeErr)
	}
	c.notifyDeadlineChanged()
}

func (s *qblockServer) writeSeparateCON(r *qblockServerCONRecord) error {
	c := s.client
	if r.ctx.Err() != nil {
		return nil
	}
	msg := c.cc.AcquireMessage(r.ctx)
	defer c.cc.ReleaseMessage(msg)
	if _, err := msg.UnmarshalWithDecoder(qblock.Decoder{}, r.response); err != nil {
		return nil
	}
	if r.control != nil {
		msg.SetControlMessage(&coapNet.ControlMessage{Src: cloneQBlockBytes(r.control.Dst), IfIndex: r.control.IfIndex})
	}
	if err := c.cc.writeOrdinary(msg, r.permit); err != nil {
		c.mu.Lock()
		s.settleCONLocked(r)
		c.mu.Unlock()
		return err
	}
	return nil
}

// Called under client.mu; output callbacks take the action gate before writes.
func (s *qblockServer) dueCON(now time.Time) []qblockCallback {
	var callbacks []qblockCallback
	for _, r := range s.conRequests {
		if r.terminal || r.sending {
			continue
		}
		ack := !r.ackDue.IsZero() && !now.Before(r.ackDue)
		retry := !r.retryDue.IsZero() && !now.Before(r.retryDue)
		if !ack && !retry {
			continue
		}
		if ack {
			r.separate = true
			r.ackDue = time.Time{}
			r.ack = s.simpleCONWire(r, codes.Empty)
		} else {
			if r.retries >= r.maxRetries {
				s.settleCONLocked(r)
				continue
			}
			r.retries++
			r.interval *= 2
			r.retryDue = now.Add(r.interval)
		}
		r.sending = true
		release := r.lease.retain()
		callbacks = append(callbacks, qblockCallback{run: func() {
			defer release()
			c := s.client
			c.lockAction()
			c.mu.Lock()
			current := s.conRequests[r.mid] == r && !r.terminal && !s.closed && c.now().Before(r.expires)
			r.sending = false
			if !ack {
				current = current && !r.acknowledged
			}
			c.mu.Unlock()
			var writeErr error
			if current {
				if ack {
					writeErr = s.writeCONWire(r, r.ack)
					close(r.ackDone)
				} else {
					writeErr = s.writeSeparateCON(r)
				}
			}
			c.actionMu.Unlock()
			if writeErr != nil {
				c.cc.errors(writeErr)
			}
			c.notifyDeadlineChanged()
		}, discard: release})
	}
	return callbacks
}
