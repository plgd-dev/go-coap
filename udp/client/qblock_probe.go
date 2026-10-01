package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
)

// ErrQBlockProbeInProgress indicates that this connection already has an explicit probe.
var ErrQBlockProbeInProgress = errors.New("q-block capability probe already in progress")

type qblockCapabilityResult struct {
	supported bool
	err       error
}

type qblockCapabilityProbe struct {
	generation         *qblockProbeGeneration
	cc                 *Conn
	lease              *qblockOwnedLease
	token              message.Token
	mid                int32
	permit             *qblockOrdinaryPermit
	result             chan qblockCapabilityResult
	elem               *midElement
	releaseInteraction func()
}

func (p *qblockCapabilityProbe) finish(supported bool, err error) {
	p.complete(supported, err, qblockCapabilityUnknown)
}

func (p *qblockCapabilityProbe) complete(supported bool, err error, evidence qblockCapability) {
	p.cc.publishQBlockProbe(p.generation, supported, err, evidence)
	select {
	case p.result <- qblockCapabilityResult{supported, err}:
	default:
	}
}

func (cc *Conn) probeQBlockWire(ctx context.Context, path string, g *qblockProbeGeneration) (bool, error) {
	var owned *qblockOwnedLease
	defer func() {
		if owned != nil {
			owned.drop()
		}
	}()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if c := cc.qblockClient; c != nil {
		if c.initErr != nil {
			return false, c.initErr
		}
		// The existing client envelope covers the request, retransmit snapshot,
		// bounded response handling and control bookkeeping, including ingress
		// that is still completing after the caller cancels.
		release, err := c.ownedBudget.acquire(c.ownedBudget.clientCost)
		if err != nil {
			return false, err
		}
		owned = newQBlockOwnedLease(release)
	}
	var token message.Token
	for range 32 {
		candidate, err := cc.getToken()
		if err != nil {
			return false, err
		}
		if len(candidate) == 0 || len(candidate) > 8 {
			continue
		}
		candidate = cloneQBlockBytes(candidate)
		if cc.claimToken(candidate, tokenOwnerQBlockProbe) == nil {
			token = candidate
			break
		}
	}
	if token == nil {
		return false, errors.New("cannot allocate q-block probe token")
	}
	defer cc.releaseToken(token, tokenOwnerQBlockProbe)
	req := cc.AcquireMessage(ctx)
	defer cc.ReleaseMessage(req)
	req.SetType(message.Confirmable)
	req.SetCode(codes.GET)
	req.SetToken(token)
	req.SetMessageID(cc.GetMessageID())
	if err := req.SetPath(path); err != nil {
		return false, err
	}
	// RFC 9177 4.4: M=0 requests this block only. SZX=0 requests 16 bytes.
	req.SetOptionUint32(message.QBlock2, 0)
	cc.upsertControlInformation(req)
	size, err := qblockDatagramSize(req)
	if err != nil {
		return false, err
	}
	if size > uint64(cc.qblockProbeLimit) {
		return false, qblock.ErrLimitExceeded
	}
	cc.receivedMessageReader.TryToReplaceLoop()
	if err := cc.acquireOutstandingInteraction(ctx); err != nil {
		return false, err
	}
	releaseInteraction := sync.OnceFunc(cc.releaseOutstandingInteraction)
	defer releaseInteraction()
	permit, err := cc.acquireOrdinary(req)
	if err != nil {
		return false, err
	}
	if permit != nil {
		defer func() { permit.finish(false, permit.member.domain.clock.Now()) }()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	p := &qblockCapabilityProbe{generation: g, cc: cc, lease: owned, token: token, mid: req.MessageID(), permit: permit, result: make(chan qblockCapabilityResult, 1), releaseInteraction: releaseInteraction}
	snapshot := cc.AcquireMessage(ctx)
	if err := req.Clone(snapshot); err != nil {
		cc.ReleaseMessage(snapshot)
		return false, err
	}
	deadline, _ := ctx.Deadline()
	elem := &midElement{capability: p, ordinary: permit, start: time.Now(), deadline: deadline,
		handler: func(*responsewriter.ResponseWriter[*Conn], *pool.Message) {}}
	elem.private.msg = snapshot
	p.elem = elem
	if _, loaded := cc.midHandlerContainer.LoadOrStore(p.mid, elem); loaded {
		elem.ReleaseMessage(cc)
		return false, errors.New("q-block probe message ID is already in use")
	}
	defer func() {
		cc.releaseQBlockProbeMID(p)
	}()
	cc.qblockProbeMu.Lock()
	cc.qblockProbe = p
	cc.qblockProbeMu.Unlock()
	if err := cc.writeOrdinary(req, permit); err != nil {
		return false, err
	}
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case result := <-p.result:
		if err := cc.Context().Err(); err != nil {
			return false, err
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		return result.supported, result.err
	}
}

func (cc *Conn) releaseQBlockProbeMID(p *qblockCapabilityProbe) {
	cc.midHandlerContainer.ReplaceWithFunc(p.mid, func(current *midElement, loaded bool) (*midElement, bool) {
		return current, !loaded || current == p.elem
	})
	p.elem.ReleaseMessage(cc)
}

func (cc *Conn) handleQBlockProbe(msg *pool.Message, wire []byte) bool {
	cc.qblockProbeMu.Lock()
	p := cc.qblockProbe
	if p != nil && p.lease != nil {
		defer p.lease.retain()()
	}
	cc.qblockProbeMu.Unlock()
	if p == nil {
		return false
	}
	return cc.handleQBlockProbeResponse(p, msg, wire)
}

func (cc *Conn) handleQBlockProbeResponse(p *qblockCapabilityProbe, msg *pool.Message, wire []byte) bool {
	tokenMatch := bytes.Equal(msg.Token(), p.token)
	midMatch := msg.MessageID() == p.mid
	isMID := msg.Type() == message.Acknowledgement || msg.Type() == message.Reset
	if (!isMID || !midMatch) && (!tokenMatch || msg.Code() < 64) {
		return false
	}
	// Own malformed candidates too: normal MID routing must not acknowledge them.
	defer cc.ReleaseMessage(msg)
	if uint64(len(wire)) > uint64(cc.qblockProbeLimit) {
		return true
	}
	if isMID {
		if !midMatch {
			return true
		}
		if msg.Code() == codes.Empty {
			if len(wire) != 4 {
				return true
			}
		} else if msg.Type() != message.Acknowledgement || msg.Code() < 64 || !tokenMatch {
			return true
		}
	} else if (msg.Type() != message.Confirmable && msg.Type() != message.NonConfirmable) || !tokenMatch || msg.Code() < 64 {
		return true
	}
	if p.permit != nil {
		p.permit.finish(true, p.permit.member.domain.clock.Now())
	}
	cc.releaseQBlockProbeMID(p)
	p.releaseInteraction()
	if msg.Type() == message.Reset {
		p.finish(false, errors.New("q-block capability probe reset by peer"))
		return true
	}
	if msg.Code() == codes.Empty {
		return true
	}
	// Re-decode only correlated probe replies with all options preserved. The
	// ordinary decoder can discard illegal lengths, hiding malformed duplicates.
	_, err := msg.UnmarshalWithDecoder(qblockProbeDecoder{}, wire)
	var supported bool
	if err == nil {
		supported, err = validateQBlockProbeResponse(msg)
	}
	if msg.Type() == message.Confirmable {
		ack := cc.AcquireMessage(cc.Context())
		ack.SetType(message.Acknowledgement)
		ack.SetCode(codes.Empty)
		ack.SetMessageID(msg.MessageID())
		cc.upsertControlInformation(ack)
		// CoAP duplicate-response handling must outlive this explicit probe.
		// This stores an empty ACK, not a capability result or response body.
		writeErr := cc.addResponseToCache(ack)
		if writeErr == nil {
			writeErr = cc.writeMessageAsyncOrigin(ack, true)
		}
		if writeErr != nil {
			supported = false
			err = writeErr
		}
		cc.ReleaseMessage(ack)
	}
	evidence := qblockCapabilityUnknown
	if err == nil {
		if supported {
			evidence = qblockCapabilitySupported
		} else if msg.Code() == codes.BadOption {
			evidence = qblockCapabilityUnsupported
		}
	}
	p.complete(supported, err, evidence)
	return true
}

type qblockProbeDecoder struct{}

func (qblockProbeDecoder) Decode(data []byte, msg *message.Message) (int, error) {
	msg.Options = msg.Options[:0]
	n, err := coder.DefaultCoder.DecodeWithOptionDefs(data, msg, nil)
	if err != nil {
		return n, err
	}
	// A payload marker without payload is malformed, even though the shared
	// decoder represents it as an absent body.
	if len(msg.Payload) == 0 {
		size, sizeErr := coder.DefaultCoder.Size(*msg)
		if sizeErr != nil {
			return n, sizeErr
		}
		if size != len(data) {
			return n, errors.New("empty payload marker in q-block probe response")
		}
	}
	return n, nil
}

func validateQBlockProbeResponse(msg *pool.Message) (bool, error) {
	for _, opt := range msg.Options() {
		if def, ok := message.CoapOptionDefs[opt.ID]; ok && (uint64(len(opt.Value)) < uint64(def.MinLen) || uint64(len(opt.Value)) > uint64(def.MaxLen)) {
			return false, message.ErrInvalidValueLength
		}
	}
	if err := qblock.ValidateOptions(msg.Options(), false); err != nil {
		return false, err
	}
	if msg.HasOption(message.QBlock1) || msg.HasOption(message.Observe) {
		return false, errors.New("unexpected option in q-block probe response")
	}
	if msg.Code() == codes.BadOption {
		return false, nil
	}
	if msg.Code() < 64 || msg.Code() >= 96 {
		return false, fmt.Errorf("q-block probe response: %v", msg.Code())
	}
	if !msg.HasOption(message.QBlock2) {
		return false, nil
	}
	if msg.Code() != codes.Content || qblockOptionCount(msg, message.QBlock2) != 1 || qblockOptionCount(msg, message.ETag) != 1 || qblockOptionCount(msg, message.Size2) != 1 {
		return false, errors.New("invalid q-block probe response metadata")
	}
	value, err := msg.GetOptionUint32(message.QBlock2)
	if err != nil {
		return false, err
	}
	block, err := qblock.DecodeBlock(value)
	if err != nil {
		return false, err
	}
	if block.Number != 0 || block.SZX != 0 {
		return false, errors.New("q-block probe response changed requested block")
	}
	etag, err := msg.GetOptionBytes(message.ETag)
	if err != nil || len(etag) == 0 || len(etag) > 8 {
		return false, errors.New("invalid q-block probe ETag")
	}
	size, err := msg.GetOptionUint32(message.Size2)
	if err != nil {
		return false, err
	}
	length, err := msg.BodySize()
	if err != nil || length < 0 || length > 16 || (block.More && (length != 16 || size <= 16)) || (!block.More && uint64(size) != uint64(length)) {
		return false, errors.New("inconsistent q-block probe body size")
	}
	return true, nil
}
