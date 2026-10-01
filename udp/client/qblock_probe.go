package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
)

// ErrQBlockProbeInProgress indicates that this connection already has an explicit probe.
var ErrQBlockProbeInProgress = errors.New("q-block capability probe already in progress")

type qblockCapabilityResult struct {
	supported bool
	err       error
}

type qblockCapabilityProbe struct {
	lease  *qblockOwnedLease
	token  message.Token
	mid    int32
	permit *qblockOrdinaryPermit
	result chan qblockCapabilityResult
}

func (p *qblockCapabilityProbe) finish(supported bool, err error) {
	select {
	case p.result <- qblockCapabilityResult{supported, err}:
	default:
	}
}

// ProbeQBlock explicitly checks the peer's RFC 9177 support with a safe CON GET.
// An empty path selects /.well-known/core; another absolute resource path may
// be supplied. The request asks only for block zero (16 bytes), even when the
// resource is larger. It never sends an application payload or fetches more blocks.
//
// true,nil means a valid Q-aware response was received. false,nil means Bad
// Option or a successful response without QBlock2. Errors are inconclusive.
// Results are not cached and do not enable Q payload transfers. At most one
// explicit probe runs per connection. The caller's context bounds the exchange,
// with ExchangeLifetime as an upper limit, and closure cancels pending waits.
func (cc *Conn) ProbeQBlock(ctx context.Context, path string) (bool, error) {
	var owned *qblockOwnedLease
	cc.qblockProbeMu.Lock()
	if cc.qblockProbeBusy {
		cc.qblockProbeMu.Unlock()
		return false, ErrQBlockProbeInProgress
	}
	cc.qblockProbeBusy = true
	cc.qblockProbeMu.Unlock()
	defer func() {
		cc.qblockProbeMu.Lock()
		cc.qblockProbe = nil
		cc.qblockProbeBusy = false
		cc.qblockProbeMu.Unlock()
		if owned != nil {
			owned.drop()
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, ExchangeLifetime)
	defer cancel()
	stop := context.AfterFunc(cc.Context(), cancel)
	defer stop()
	if err := cc.Context().Err(); err != nil {
		return false, err
	}
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
	if peer, ok := cc.RemoteAddr().(*net.UDPAddr); ok && peer != nil && peer.IP.IsMulticast() {
		return false, errors.New("q-block capability probe requires a unicast peer")
	}
	if path == "" {
		path = "/.well-known/core"
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#") || uint64(len(path)) > uint64(cc.qblockProbeLimit) {
		return false, errors.New("invalid q-block probe resource path")
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
	defer cc.releaseOutstandingInteraction()
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
	p := &qblockCapabilityProbe{lease: owned, token: token, mid: req.MessageID(), permit: permit, result: make(chan qblockCapabilityResult, 1)}
	snapshot := cc.AcquireMessage(ctx)
	if err := req.Clone(snapshot); err != nil {
		cc.ReleaseMessage(snapshot)
		return false, err
	}
	deadline, _ := ctx.Deadline()
	elem := &midElement{capability: p, ordinary: permit, start: time.Now(), deadline: deadline,
		handler: func(*responsewriter.ResponseWriter[*Conn], *pool.Message) {}}
	elem.private.msg = snapshot
	if _, loaded := cc.midHandlerContainer.LoadOrStore(p.mid, elem); loaded {
		elem.ReleaseMessage(cc)
		return false, errors.New("q-block probe message ID is already in use")
	}
	defer func() {
		cc.midHandlerContainer.ReplaceWithFunc(p.mid, func(current *midElement, loaded bool) (*midElement, bool) {
			return current, !loaded || current == elem
		})
		elem.ReleaseMessage(cc)
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

func (cc *Conn) handleQBlockProbe(msg *pool.Message, wireSize uint64) bool {
	cc.qblockProbeMu.Lock()
	p := cc.qblockProbe
	if p != nil && p.lease != nil {
		defer p.lease.retain()()
	}
	cc.qblockProbeMu.Unlock()
	if p == nil {
		return false
	}
	tokenMatch := bytes.Equal(msg.Token(), p.token)
	midMatch := msg.MessageID() == p.mid
	isMID := msg.Type() == message.Acknowledgement || msg.Type() == message.Reset
	if (!isMID || !midMatch) && (!tokenMatch || msg.Code() < 64) {
		return false
	}
	// Own malformed candidates too: normal MID routing must not acknowledge them.
	defer cc.ReleaseMessage(msg)
	if wireSize > uint64(cc.qblockProbeLimit) {
		return true
	}
	if isMID {
		if !midMatch {
			return true
		}
		if msg.Code() == codes.Empty {
			if len(msg.Token()) != 0 || len(msg.Options()) != 0 || msg.Body() != nil {
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
	if elem, ok := cc.midHandlerContainer.LoadAndDelete(p.mid); ok {
		elem.ReleaseMessage(cc)
	}
	if msg.Type() == message.Reset {
		p.finish(false, errors.New("q-block capability probe reset by peer"))
		return true
	}
	if msg.Code() == codes.Empty {
		return true
	}
	supported, err := validateQBlockProbeResponse(msg)
	if msg.Type() == message.Confirmable {
		ack := cc.AcquireMessage(cc.Context())
		ack.SetType(message.Acknowledgement)
		ack.SetCode(codes.Empty)
		ack.SetMessageID(msg.MessageID())
		cc.upsertControlInformation(ack)
		if writeErr := cc.writeMessageAsyncOrigin(ack, true); writeErr != nil {
			supported = false
			err = writeErr
		}
		cc.ReleaseMessage(ack)
	}
	p.finish(supported, err)
	return true
}

func validateQBlockProbeResponse(msg *pool.Message) (bool, error) {
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
