package client

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/blockwise/qblock"
	"github.com/plgd-dev/go-coap/v3/net/observation"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
)

func (cc *Conn) qBlockEligible(req *pool.Message) bool {
	if len(req.Token()) == 0 || req.HasOption(message.ProxyURI) || req.HasOption(message.ProxyScheme) {
		return false
	}
	if remote, ok := cc.RemoteAddr().(*net.UDPAddr); ok && remote.IP.IsMulticast() {
		return false
	}
	if req.HasOption(message.Observe) && req.Code() != codes.GET {
		return false
	}
	bodyMethod := req.Code() == codes.POST || req.Code() == codes.PUT ||
		req.Code() == codes.FETCH || req.Code() == codes.PATCH || req.Code() == codes.IPATCH
	return (req.Code() == codes.GET || bodyMethod) && (req.Body() == nil || bodyMethod)
}

func (cc *Conn) selectQBlock(req *pool.Message) (bool, error) {
	eligible := cc.qBlockEligible(req)
	if cc.qBlockwiseMode == blockwise.QBlockwiseOnly {
		if !eligible || !cc.qBlockAllowed() {
			return false, blockwise.ErrQBlockUnsupported
		}
		return true, nil
	}
	if cc.qBlockwiseMode != blockwise.QBlockwiseAuto || !cc.qBlockAllowed() || !eligible {
		return false, nil
	}
	supported, err := cc.qBlockCapability(req)
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, blockwise.ErrQBlockUnsupported) {
		cc.errors(fmt.Errorf("Q-Block capability probe was inconclusive: %w", err))
		return false, nil
	}
	return supported, err
}

func (cc *Conn) doQBlock(req *pool.Message) (*pool.Message, error) {
	req.UpsertType(message.Confirmable)
	if req.Body() != nil {
		szx, err := cc.qBlockRequestSZX(req)
		if err != nil {
			return nil, err
		}
		return cc.blockWise.DoQBlock1(req, szx, 16<<20, cc.doInternal)
	}
	szx, err := cc.qBlockResponseSZX()
	if err != nil {
		return nil, err
	}
	return cc.blockWise.DoQBlock2(req, szx, cc.doInternal)
}

func qBlockOrigin(options message.Options) (string, message.Options) {
	var route []byte
	var originOptions message.Options
	for _, option := range options {
		if option.ID != message.URIHost && option.ID != message.URIPort {
			continue
		}
		route = binary.BigEndian.AppendUint16(route, uint16(option.ID))
		route = binary.BigEndian.AppendUint64(route, uint64(len(option.Value)))
		route = append(route, option.Value...)
		originOptions = append(originOptions, message.Option{ID: option.ID, Value: append([]byte(nil), option.Value...)})
	}
	return string(route), originOptions
}

func (cc *Conn) prepareQObservation(req *pool.Message) error {
	if req.Code() != codes.GET || req.Body() != nil {
		return blockwise.ErrQBlockUnsupported
	}
	szx, err := cc.qBlockResponseSZX()
	if err != nil {
		return err
	}
	value, err := blockwise.EncodeQBlockOption(szx, 0, true)
	if err != nil {
		return err
	}
	req.SetOptionUint32(message.QBlock2, value)
	if req.HasOption(message.RequestTag) {
		return nil
	}
	tag, err := message.GetToken()
	if err != nil {
		return err
	}
	return req.SetRequestTag(tag)
}

type qBlockRoute struct {
	cc          *Conn
	tokens      map[string]struct{}
	mu          sync.Mutex
	closed      bool
	closeErr    error
	contextDone <-chan struct{}
	stopContext func() bool
	messages    chan *pool.Message
	done        chan struct{}
}

func hasQBlock(msg *pool.Message) bool {
	return msg.HasOption(message.QBlock1) || msg.HasOption(message.QBlock2)
}

type qCapabilityResult struct {
	route     string
	supported bool
	err       error
}

var qBlockRetainedTokens atomic.Int64

func reserveQBlockToken() bool {
	for {
		current := qBlockRetainedTokens.Load()
		if current >= 262144 {
			return false
		}
		if qBlockRetainedTokens.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

func (r *qBlockRoute) NewToken() (message.Token, error) {
	token, err := r.cc.GetToken()
	if err != nil {
		return nil, err
	}
	if len(token) == 0 {
		return nil, errors.New("Q-Block token generator returned an empty token")
	}
	r.cc.qRoutesMu.Lock()
	defer r.cc.qRoutesMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.cc.Context().Err() != nil {
		return nil, context.Canceled
	}
	select {
	case <-r.contextDone:
		return nil, context.Canceled
	default:
	}
	key := string(token)
	if r.cc.qRoutes[key] != nil {
		return nil, errors.New("Q-Block token generator returned an active token")
	}
	if len(r.tokens) >= 131072 || !reserveQBlockToken() {
		return nil, errors.New("Q-Block token capacity exhausted")
	}
	r.cc.qRoutes[key] = r
	r.tokens[key] = struct{}{}
	return token, nil
}

func (r *qBlockRoute) Send(msg *pool.Message) error {
	r.cc.qRoutesMu.Lock()
	bound := r.cc.qRoutes[string(msg.Token())] == r
	r.cc.qRoutesMu.Unlock()
	if !bound {
		return errors.New("Q-Block request token is not bound to this body")
	}
	return r.cc.SendQBlock(msg)
}

func (r *qBlockRoute) Receive(ctx context.Context) (*pool.Message, error) {
	r.cc.receivedMessageReader.TryToReplaceLoop()
	select {
	case msg := <-r.messages:
		msg.SetContext(ctx)
		return msg, nil
	default:
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-r.cc.Context().Done():
		return nil, r.cc.Context().Err()
	case <-r.done:
		r.mu.Lock()
		err := r.closeErr
		r.mu.Unlock()
		return nil, err
	case msg := <-r.messages:
		msg.SetContext(ctx)
		return msg, nil
	}
}

func (r *qBlockRoute) Close() {
	r.closeWithError(context.Canceled)
}

func (r *qBlockRoute) closeWithError(err error) {
	r.cc.qRoutesMu.Lock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		r.cc.qRoutesMu.Unlock()
		return
	}
	r.closed = true
	r.closeErr = err
	r.stopContext()
	close(r.done)
	for token := range r.tokens {
		delete(r.cc.qRoutes, token)
	}
	qBlockRetainedTokens.Add(-int64(len(r.tokens)))
	clear(r.tokens)
	r.cc.qActiveRoutes--
	r.cc.qRoutesMu.Unlock()
	for {
		select {
		case msg := <-r.messages:
			r.cc.ReleaseMessage(msg)
		default:
			return
		}
	}
}

func (r *qBlockRoute) retireAliases(token message.Token) {
	r.cc.qRoutesMu.Lock()
	defer r.cc.qRoutesMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.tokens {
		if key != string(token) {
			delete(r.tokens, key)
			delete(r.cc.qRoutes, key)
			qBlockRetainedTokens.Add(-1)
		}
	}
}

// OpenQBlockExchange reserves a full token for the lifetime of a Q body.
func (cc *Conn) OpenQBlockExchange(ctx context.Context, token message.Token) (blockwise.QBlockExchange, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(token) == 0 {
		return nil, errors.New("Q-Block requires a nonempty transfer token")
	}
	key := string(token)
	cc.qRoutesMu.Lock()
	defer cc.qRoutesMu.Unlock()
	if err := cc.Context().Err(); err != nil {
		return nil, err
	}
	if cc.qRoutes == nil {
		cc.qRoutes = make(map[string]*qBlockRoute)
	}
	if cc.qRoutes[key] != nil || cc.qActiveRoutes >= 128 {
		return nil, errors.New("Q-Block transfer token is active or route capacity exhausted")
	}
	if !reserveQBlockToken() {
		return nil, errors.New("Q-Block token capacity exhausted")
	}
	route := &qBlockRoute{
		cc: cc, contextDone: ctx.Done(), tokens: map[string]struct{}{key: {}},
		messages: make(chan *pool.Message, max(32, 2*cc.qParameters.MaxPayloads)), done: make(chan struct{}),
	}
	route.stopContext = context.AfterFunc(ctx, func() { route.closeWithError(ctx.Err()) })
	cc.qRoutes[key] = route
	cc.qActiveRoutes++
	return route, nil
}

func (cc *Conn) routeQBlockResponse(msg *pool.Message) bool {
	if msg.Code() < codes.Created {
		return false
	}
	cc.qRoutesMu.Lock()
	route := cc.qRoutes[string(msg.Token())]
	cc.qRoutesMu.Unlock()
	if route == nil {
		return false
	}
	cc.qBlockPeerProgress()
	route.mu.Lock()
	defer route.mu.Unlock()
	if route.closed {
		return true
	}
	copyMsg := cc.AcquireMessage(cc.Context())
	if err := msg.Clone(copyMsg); err != nil {
		cc.ReleaseMessage(copyMsg)
		cc.errors(fmt.Errorf("clone Q-Block response: %w", err))
		return true
	}
	select {
	case route.messages <- copyMsg:
	default:
		cc.ReleaseMessage(copyMsg)
		cc.errors(errors.New("Q-Block response route capacity exhausted"))
	}
	return true
}

// CancelObservation releases any partial notification and its recovery aliases.
func (cc *Conn) CancelObservation(token message.Token) {
	cc.qRoutesMu.Lock()
	route := cc.qRoutes[string(token)]
	cc.qRoutesMu.Unlock()
	if route != nil {
		route.Close()
	}
}

func (cc *Conn) startQBlockNotification(msg *pool.Message) bool {
	if !cc.qBlockAllowed() || msg.Code() < codes.Created || !msg.HasOption(message.QBlock2) {
		return false
	}
	req, szx, consume := cc.qBlockNotificationRequest(msg)
	if !consume {
		return false
	}
	if req == nil {
		return true
	}
	exchange, initial := cc.prepareQBlockNotification(req, msg)
	if exchange == nil {
		return true
	}
	cc.receivedMessageReader.TryToReplaceLoop()
	go cc.runQBlockObservation(req, szx, exchange, initial)
	return true
}

func (cc *Conn) qBlockNotificationRequest(msg *pool.Message) (*pool.Message, blockwise.SZX, bool) {
	req, ok := cc.observationHandler.GetObservationRequest(msg.Token())
	if !ok {
		return nil, 0, false
	}
	if !req.HasOption(message.QBlock2) {
		cc.ReleaseMessage(req)
		return nil, 0, false
	}
	value, err := req.GetOptionUint32(message.QBlock2)
	if err != nil {
		cc.ReleaseMessage(req)
		return nil, 0, true
	}
	szx, _, _, err := blockwise.DecodeQBlockOption(value)
	if err != nil {
		cc.ReleaseMessage(req)
		return nil, 0, true
	}
	return req, szx, true
}

func (cc *Conn) prepareQBlockNotification(req, msg *pool.Message) (blockwise.QBlockExchange, *pool.Message) {
	req.SetType(message.Confirmable)
	if msg.Type() == message.NonConfirmable {
		req.SetType(message.NonConfirmable)
	}
	exchange, err := cc.OpenQBlockExchange(cc.Context(), req.Token())
	if err != nil {
		cc.ReleaseMessage(req)
		cc.errors(err)
		return nil, nil
	}
	if _, active := cc.observationHandler.GetObservation(req.Token().Hash()); !active {
		exchange.Close()
		cc.ReleaseMessage(req)
		return nil, nil
	}
	initial := cc.AcquireMessage(cc.Context())
	if err := msg.Clone(initial); err != nil {
		exchange.Close()
		cc.ReleaseMessage(initial)
		cc.ReleaseMessage(req)
		cc.errors(err)
		return nil, nil
	}
	return exchange, initial
}

func (cc *Conn) runQBlockObservation(req *pool.Message, szx blockwise.SZX, exchange blockwise.QBlockExchange, initial *pool.Message) {
	defer cc.ReleaseMessage(req)
	defer exchange.Close()
	var sequence uint32
	var lastEvent time.Time
	for {
		req.SetType(message.Confirmable)
		if initial.Type() == message.NonConfirmable {
			req.SetType(message.NonConfirmable)
		}
		response, err := cc.receiveQNotification(req, szx, exchange, initial)
		if route, ok := exchange.(*qBlockRoute); ok {
			route.retireAliases(req.Token())
		}
		if errors.Is(err, context.Canceled) || cc.Context().Err() != nil {
			return
		}
		if err != nil {
			cc.errors(fmt.Errorf("receive Q Observe notification: %w", err))
		} else {
			if nextSequence, observeErr := response.Observe(); observeErr == nil {
				sequence, lastEvent = nextSequence, time.Now()
			}
			active := cc.observationHandler.HandleNotification(response)
			cc.ReleaseMessage(response)
			if !active {
				return
			}
		}
		initial, err = cc.nextQBlockNotification(req.Token(), exchange, sequence, lastEvent)
		if err != nil {
			return
		}
	}
}

func (cc *Conn) receiveQNotification(req *pool.Message, szx blockwise.SZX, exchange blockwise.QBlockExchange, initial *pool.Message) (*pool.Message, error) {
	if !initial.HasOption(message.QBlock2) {
		return initial, nil
	}
	return cc.blockWise.ReceiveQBlock2(req, szx, exchange, initial)
}

func (cc *Conn) nextQBlockNotification(token message.Token, exchange blockwise.QBlockExchange, sequence uint32, lastEvent time.Time) (*pool.Message, error) {
	for {
		response, err := exchange.Receive(cc.Context())
		if err != nil {
			return nil, err
		}
		if string(response.Token()) != string(token) {
			cc.ReleaseMessage(response)
			continue
		}
		if !response.HasOption(message.Observe) {
			return response, nil
		}
		next, observeErr := response.Observe()
		if observeErr == nil && observation.ValidSequenceNumber(sequence, next, lastEvent, time.Now()) {
			return response, nil
		}
		cc.ReleaseMessage(response)
	}
}

// SendQBlockAsync retains a message copy and awaits its ACK outside the shared
// deadline worker. Pending writes, including NSTART waiters, are bounded.
func (cc *Conn) SendQBlockAsync(msg *pool.Message, done func(error)) error {
	cc.qJobsMu.Lock()
	if err := cc.Context().Err(); err != nil {
		cc.qJobsMu.Unlock()
		return err
	}
	if cc.qAsyncSends >= 128 {
		cc.qJobsMu.Unlock()
		return blockwise.ErrQBlockScheduleFull
	}
	cc.qAsyncSends++
	cc.qJobsMu.Unlock()
	release := func() {
		cc.qJobsMu.Lock()
		cc.qAsyncSends--
		cc.qJobsMu.Unlock()
	}
	ctx, cancel := context.WithCancel(msg.Context())
	stop := context.AfterFunc(cc.Context(), cancel)
	copyMsg := cc.AcquireMessage(ctx)
	if err := msg.Clone(copyMsg); err != nil {
		cc.ReleaseMessage(copyMsg)
		stop()
		cancel()
		release()
		return err
	}
	copyMsg.SetContext(ctx)
	go func() {
		err := cc.SendQBlock(copyMsg)
		cc.ReleaseMessage(copyMsg)
		stop()
		cancel()
		release()
		done(err)
	}()
	return nil
}

// SendQBlock uses normal CON retransmission and NSTART handling. NON stays NON.
func (cc *Conn) SendQBlock(msg *pool.Message) error {
	if !cc.qBlockAllowed() {
		return blockwise.ErrQBlockDisabled
	}
	msg.UpsertType(message.Confirmable)
	msg.SetMessageID(cc.GetMessageID())
	if err := cc.validateQBlockDatagram(msg); err != nil {
		return err
	}
	if msg.Type() == message.NonConfirmable {
		if err := cc.qBlockProbeRate(msg); err != nil {
			return err
		}
	}
	return cc.writeMessage(msg)
}

func (cc *Conn) validateQBlockDatagram(msg *pool.Message) error {
	wire, err := msg.MarshalWithEncoder(coder.DefaultCoder)
	if err != nil {
		return err
	}
	if uint64(len(wire)) > uint64(cc.qDatagramLimit) {
		return fmt.Errorf("%w: Q-Block datagram size %d exceeds %d", blockwise.ErrQBlockTooLarge, len(wire), cc.qDatagramLimit)
	}
	return nil
}

func (cc *Conn) qBlockPeerProgress() {
	cc.qRateMu.Lock()
	cc.qPeerActivity = time.Now()
	if cc.qRateWake != nil {
		select {
		case cc.qRateWake <- struct{}{}:
		default:
		}
	}
	cc.qRateMu.Unlock()
}

func (cc *Conn) qBlockRequestSZX(req *pool.Message) (blockwise.SZX, error) {
	header := cc.AcquireMessage(req.Context())
	defer cc.ReleaseMessage(header)
	header.SetCode(req.Code())
	header.SetToken(req.Token())
	header.SetType(req.Type())
	header.SetMessageID(0)
	header.ResetOptionsTo(req.Options())
	header.SetOptionUint32(message.QBlock1, 0xfffff6)
	header.SetOptionUint32(message.QBlock2, 0xfffff6)
	header.SetOptionUint32(message.Size1, ^uint32(0))
	if !header.HasOption(message.RequestTag) {
		if err := header.SetRequestTag(make([]byte, 8)); err != nil {
			return 0, err
		}
	}
	wire, err := header.MarshalWithEncoder(coder.DefaultCoder)
	if err != nil {
		return 0, err
	}
	for szx := min(cc.blockwiseSZX, blockwise.SZX1024); ; szx-- {
		if int64(len(wire))+1+szx.Size() <= int64(cc.qDatagramLimit) {
			return szx, nil
		}
		if szx == blockwise.SZX16 {
			return 0, blockwise.ErrQBlockTooLarge
		}
	}
}

func (cc *Conn) qBlockResponseSZX() (blockwise.SZX, error) {
	for szx := min(cc.blockwiseSZX, blockwise.SZX1024); ; szx-- {
		if szx.Size()+96 <= int64(cc.qDatagramLimit) {
			return szx, nil
		}
		if szx == blockwise.SZX16 {
			return 0, blockwise.ErrQBlockTooLarge
		}
	}
}

func (cc *Conn) qBlockProbingDelay(size int64) time.Duration {
	parameters := cc.qParameters
	if parameters == (qblock.Config{}) {
		parameters = qblock.DefaultConfig()
	}
	jitter := time.Duration(rand.Int64N(max(int64(1), int64(parameters.NonTimeout/2)))) // #nosec G404 -- protocol pacing jitter, not token generation.
	limit := parameters.NonProbingWait(parameters.NonTimeout + jitter)
	return min(time.Duration(size)*time.Second, limit)
}

// PROBING_RATE is one byte/second to a peer that has stopped responding.
// Charge a Q1 body once (not each payload); charge NON recovery controls
// individually. A received Continue or payload immediately makes the peer
// responsive again. The RFC's jittered NON_PROBING_WAIT caps either wait.
func (cc *Conn) qBlockProbeRate(msg *pool.Message) error {
	control, bodyTag, size, charge, err := qBlockRateIdentity(msg)
	if err != nil || !charge {
		return err
	}
	for {
		deadline, wake := cc.qBlockRateDeadline(control, bodyTag, size)
		if deadline.IsZero() {
			return nil
		}
		timer := time.NewTimer(time.Until(deadline))
		select {
		case <-msg.Context().Done():
			timer.Stop()
			return msg.Context().Err()
		case <-cc.Context().Done():
			timer.Stop()
			return cc.Context().Err()
		case <-wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func qBlockRateIdentity(msg *pool.Message) (bool, string, int64, bool, error) {
	control := msg.Code() == codes.RequestEntityIncomplete ||
		(msg.Code() == codes.GET || msg.Code() == codes.FETCH) && msg.HasOption(message.QBlock2)
	if control {
		wire, err := msg.MarshalWithEncoder(coder.DefaultCoder)
		if err != nil {
			return true, "", 0, false, err
		}
		return true, "", int64(len(wire)), true, nil
	}
	if !msg.HasOption(message.QBlock1) || msg.Code() < codes.GET || msg.Code() >= 32 {
		return false, "", 0, false, nil
	}
	value, _ := msg.GetOptionUint32(message.QBlock1)
	_, num, _, err := blockwise.DecodeQBlockOption(value)
	if err != nil || num != 0 {
		return false, "", 0, false, err
	}
	tag, err := msg.RequestTag()
	if err != nil {
		return false, "", 0, false, err
	}
	bodySize, _ := msg.GetOptionUint32(message.Size1)
	return false, string(tag), int64(bodySize), true, nil
}

func (cc *Conn) qBlockRateDeadline(control bool, bodyTag string, size int64) (time.Time, <-chan struct{}) {
	cc.qRateMu.Lock()
	defer cc.qRateMu.Unlock()
	if cc.qRateWake == nil {
		cc.qRateWake = make(chan struct{}, 1)
	}
	last, deadline := cc.qLastControl, cc.qControlNotBefore
	if !control {
		if bodyTag == cc.qBodyTag {
			return time.Time{}, nil
		}
		last, deadline = cc.qLastBody, cc.qBodyNotBefore
	}
	if last.IsZero() || cc.qPeerActivity.After(last) || !deadline.After(time.Now()) {
		now := time.Now()
		if control {
			cc.qLastControl, cc.qControlNotBefore = now, now.Add(cc.qBlockProbingDelay(size))
		} else {
			cc.qLastBody, cc.qBodyNotBefore, cc.qBodyTag = now, now.Add(cc.qBlockProbingDelay(size)), bodyTag
		}
		return time.Time{}, nil
	}
	return deadline, cc.qRateWake
}

type qBlockJob struct {
	deadline time.Time
	run      func() error
}

func (cc *Conn) closeQBlocks() {
	cc.qRoutesMu.Lock()
	qBlockRetainedTokens.Add(-int64(len(cc.qNotificationMIDs)))
	cc.qNotificationMIDs = nil
	routes := make(map[*qBlockRoute]struct{}, cc.qActiveRoutes)
	for _, route := range cc.qRoutes {
		routes[route] = struct{}{}
	}
	cc.qRoutesMu.Unlock()
	for route := range routes {
		route.Close()
	}
	cc.qJobsMu.Lock()
	clear(cc.qJobs)
	cc.qJobsMu.Unlock()
	if cc.blockWise != nil {
		cc.blockWise.CloseQBlockTransfers()
	}
}

type qNotificationMID struct {
	token   message.Token
	expires time.Time
}

func (cc *Conn) rememberQNotification(msg *pool.Message) error {
	if !cc.qBlockAllowed() {
		return nil
	}
	cc.qRoutesMu.Lock()
	defer cc.qRoutesMu.Unlock()
	if err := cc.Context().Err(); err != nil {
		return err
	}
	mid := msg.MessageID()
	if _, exists := cc.qNotificationMIDs[mid]; exists {
		delete(cc.qNotificationMIDs, mid)
		qBlockRetainedTokens.Add(-1)
	}
	if !msg.HasOption(message.Observe) || !msg.HasOption(message.QBlock2) ||
		msg.Code() < codes.Created || msg.Type() == message.Acknowledgement {
		return nil
	}
	if len(cc.qNotificationMIDs) >= 4096 {
		for oldest, entry := range cc.qNotificationMIDs {
			if !entry.expires.After(time.Now()) {
				delete(cc.qNotificationMIDs, oldest)
				qBlockRetainedTokens.Add(-1)
			}
		}
	}
	if len(cc.qNotificationMIDs) >= 4096 || !reserveQBlockToken() {
		return blockwise.ErrQBlockTooLarge
	}
	if cc.qNotificationMIDs == nil {
		cc.qNotificationMIDs = make(map[int32]qNotificationMID)
	}
	cc.qNotificationMIDs[mid] = qNotificationMID{token: append(message.Token(nil), msg.Token()...), expires: time.Now().Add(ExchangeLifetime)}
	return nil
}

func (cc *Conn) forgetQNotificationToken(token message.Token) {
	cc.qRoutesMu.Lock()
	defer cc.qRoutesMu.Unlock()
	for mid, entry := range cc.qNotificationMIDs {
		if string(entry.token) == string(token) {
			delete(cc.qNotificationMIDs, mid)
			qBlockRetainedTokens.Add(-1)
		}
	}
	if len(cc.qNotificationMIDs) == 0 {
		cc.qNotificationMIDs = nil
	}
}

func (cc *Conn) checkQObservationCancellation(msg *pool.Message) {
	if msg.Code() != codes.GET {
		return
	}
	if observe, err := msg.Observe(); err == nil && observe == 1 {
		cc.forgetQNotificationToken(msg.Token())
	}
}

func (cc *Conn) handleQNotificationAcknowledgement(msg *pool.Message) {
	if msg.Type() != message.Reset && msg.Type() != message.Acknowledgement {
		return
	}
	cc.qRoutesMu.Lock()
	entry, exists := cc.qNotificationMIDs[msg.MessageID()]
	if exists {
		delete(cc.qNotificationMIDs, msg.MessageID())
		qBlockRetainedTokens.Add(-1)
	}
	cc.qRoutesMu.Unlock()
	if exists && entry.expires.After(time.Now()) && msg.Type() == message.Reset && cc.blockWise != nil {
		cc.blockWise.CancelQBlockObservation(entry.token, cc.RemoteAddr())
		cc.forgetQNotificationToken(entry.token)
	}
}

// ScheduleQBlock adds or replaces a bounded per-body deadline. One worker and
// one timer serve the connection, irrespective of the number of blocks.
func (cc *Conn) ScheduleQBlock(key string, deadline time.Time, run func() error) error {
	cc.qJobsMu.Lock()
	defer cc.qJobsMu.Unlock()
	if cc.Context().Err() != nil {
		return cc.Context().Err()
	}
	if cc.qJobs == nil {
		cc.qJobs = make(map[string]qBlockJob)
		cc.qJobsWake = make(chan struct{}, 1)
		go cc.runQBlockJobs()
	}
	if _, exists := cc.qJobs[key]; !exists && len(cc.qJobs) >= 128 {
		return blockwise.ErrQBlockScheduleFull
	}
	if previous, exists := cc.qJobs[key]; exists && previous.deadline.Before(deadline) {
		return nil
	}
	cc.qJobs[key] = qBlockJob{deadline: deadline, run: run}
	select {
	case cc.qJobsWake <- struct{}{}:
	default:
	}
	return nil
}

// CancelQBlock removes a pending body deadline.
func (cc *Conn) CancelQBlock(key string) {
	cc.qJobsMu.Lock()
	delete(cc.qJobs, key)
	cc.qJobsMu.Unlock()
}

// DelayQBlock postpones a pending body set after selective recovery.
func (cc *Conn) DelayQBlock(key string, deadline time.Time) {
	cc.qJobsMu.Lock()
	if job, ok := cc.qJobs[key]; ok && job.deadline.Before(deadline) {
		job.deadline = deadline
		cc.qJobs[key] = job
	}
	cc.qJobsMu.Unlock()
}

func (cc *Conn) runQBlockJobs() {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		cc.qJobsMu.Lock()
		next, job := cc.nextQBlockJobLocked()
		if next != "" && !job.deadline.After(time.Now()) {
			delete(cc.qJobs, next)
			cc.qJobsMu.Unlock()
			if err := job.run(); err != nil && cc.Context().Err() == nil {
				cc.errors(fmt.Errorf("Q-Block scheduled transfer: %w", err))
			}
			continue
		}
		cc.qJobsMu.Unlock()
		delay := time.Hour
		if next != "" {
			delay = max(time.Until(job.deadline), 0)
		}
		timer.Reset(delay)
		select {
		case <-cc.Context().Done():
			cc.qJobsMu.Lock()
			clear(cc.qJobs)
			cc.qJobsMu.Unlock()
			return
		case <-cc.qJobsWake:
			stopQBlockTimer(timer)
		case <-timer.C:
		}
	}
}

func (cc *Conn) nextQBlockJobLocked() (string, qBlockJob) {
	var next string
	var job qBlockJob
	for key, candidate := range cc.qJobs {
		if next == "" || candidate.deadline.Before(job.deadline) {
			next, job = key, candidate
		}
	}
	return next, job
}

func stopQBlockTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}
