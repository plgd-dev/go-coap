package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/client"
	limitparallelrequests "github.com/plgd-dev/go-coap/v3/net/client/limitParallelRequests"
	"github.com/plgd-dev/go-coap/v3/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v3/net/observation"
	"github.com/plgd-dev/go-coap/v3/net/oscore"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	coapErrors "github.com/plgd-dev/go-coap/v3/pkg/errors"
	coapSync "github.com/plgd-dev/go-coap/v3/pkg/sync"
	"github.com/plgd-dev/go-coap/v3/pkg/sync/atomic"
)

type InactivityMonitor interface {
	Notify()
	CheckInactivity(now time.Time, cc *Conn)
}

type TCPSignalReceivedHandler func(codes.Code)

type (
	HandlerFunc                 = func(*responsewriter.ResponseWriter[*Conn], *pool.Message)
	ErrorFunc                   = func(error)
	EventFunc                   = func()
	GetMIDFunc                  = func() int32
	CreateInactivityMonitorFunc = func() InactivityMonitor
	RequestMonitorFunc          = func(cc *Conn, req *pool.Message) (drop bool, err error)
)

type Notifier interface {
	Notify()
}

// Conn represents a virtual connection to a conceptual endpoint, to perform COAPs commands.
type Conn struct {
	*client.Client[*Conn]
	session                         *Session
	observationHandler              *observation.Handler[*Conn]
	processReceivedMessage          func(req *pool.Message, cc *Conn, handler HandlerFunc)
	tokenHandlerContainer           *coapSync.Map[uint64, HandlerFunc]
	tokenlessHandler                client.TokenlessHandler[HandlerFunc]
	blockWise                       *blockwise.BlockWise[*Conn]
	blockwiseSZX                    blockwise.SZX
	peerMaxMessageSize              atomic.Uint32
	disablePeerTCPSignalMessageCSMs bool
	tcpSignalReceivedHandler        TCPSignalReceivedHandler
	handlerMutex                    sync.RWMutex
	peerBlockWiseTranferEnabled     atomic.Bool

	receivedMessageReader *client.ReceivedMessageReader[*Conn]

	oscoreProcessor    *oscore.Processor
	oscoreMu           sync.Mutex
	oscoreExchanges    map[string]tcpOSCOREExchange
	oscoreObservations map[string]tcpOSCORERequestContext
}

type tcpOSCOREExchange struct {
	context               *oscore.Context
	exchange              oscore.Exchange
	observe               bool
	expires               time.Time
	hasObserveResponse    bool
	hasNotificationNumber bool
	notificationNumber    uint64
	cancelContext         *oscore.Context
	cancelExchange        oscore.Exchange
	cancelExpires         time.Time
}

type tcpOSCORERequestContextKey struct{}

type tcpOSCORERequestContext struct {
	context  *oscore.Context
	exchange oscore.Exchange
	options  message.Options
}

func cloneTCPOSCOREOptions(options message.Options) message.Options {
	cloned := make(message.Options, len(options))
	for i, option := range options {
		cloned[i] = message.Option{ID: option.ID, Value: append([]byte(nil), option.Value...)}
	}
	return cloned
}

func tcpOSCOREResponseNeedsPartialIV(binding tcpOSCORERequestContext, options message.Options) bool {
	return binding.exchange.IsObserve() || options.HasOption(message.Observe) ||
		options.HasOption(message.QBlock1) || options.HasOption(message.QBlock2)
}

type ConnOptions struct {
	CreateBlockWise   func(cc *Conn) *blockwise.BlockWise[*Conn]
	InactivityMonitor InactivityMonitor
	RequestMonitor    RequestMonitorFunc
}

type Option = func(opts *ConnOptions)

// WithBlockWise enables block-wise transfer for the connection.
func WithBlockWise(createBlockWise func(cc *Conn) *blockwise.BlockWise[*Conn]) Option {
	return func(opts *ConnOptions) {
		opts.CreateBlockWise = createBlockWise
	}
}

// WithInactivityMonitor enables inactivity monitor for the connection.
func WithInactivityMonitor(inactivityMonitor InactivityMonitor) Option {
	return func(opts *ConnOptions) {
		opts.InactivityMonitor = inactivityMonitor
	}
}

// WithRequestMonitor enables request monitoring for the connection.
// It is called for each CoAP message received from the peer before it is processed.
// If it returns an error, the connection is closed.
// If it returns true, the message is dropped.
func WithRequestMonitor(requestMonitor RequestMonitorFunc) Option {
	return func(opts *ConnOptions) {
		opts.RequestMonitor = requestMonitor
	}
}

// NewConn creates connection over session and observation.
func NewConn(
	connection *coapNet.Conn,
	createBlockWise func(cc *Conn) *blockwise.BlockWise[*Conn],
	inactivityMonitor InactivityMonitor,
	cfg *Config,
) *Conn {
	return NewConnWithOpts(connection, cfg, WithBlockWise(createBlockWise), WithInactivityMonitor(inactivityMonitor))
}

func NewConnWithOpts(connection *coapNet.Conn, cfg *Config, opts ...Option) *Conn {
	if cfg.GetToken == nil {
		cfg.GetToken = message.GetToken
	}
	cfgOpts := ConnOptions{
		CreateBlockWise: func(*Conn) *blockwise.BlockWise[*Conn] {
			return nil
		},
		InactivityMonitor: inactivity.NewNilMonitor[*Conn](),
		RequestMonitor: func(*Conn, *pool.Message) (bool, error) {
			return false, nil
		},
	}
	for _, o := range opts {
		o(&cfgOpts)
	}
	cc := Conn{
		tokenHandlerContainer:           coapSync.NewMap[uint64, HandlerFunc](),
		blockwiseSZX:                    cfg.BlockwiseSZX,
		disablePeerTCPSignalMessageCSMs: cfg.DisablePeerTCPSignalMessageCSMs,
		oscoreProcessor:                 cfg.OSCORE,
		oscoreExchanges:                 make(map[string]tcpOSCOREExchange),
		oscoreObservations:              make(map[string]tcpOSCORERequestContext),
	}
	limitParallelRequests := limitparallelrequests.New(cfg.LimitClientParallelRequests, cfg.LimitClientEndpointParallelRequests, cc.do, cc.doObserve)
	cc.observationHandler = observation.NewHandler(&cc, cfg.Handler, limitParallelRequests.Do)
	cc.Client = client.New(&cc, cc.observationHandler, cfg.GetToken, limitParallelRequests)
	cc.blockWise = cfgOpts.CreateBlockWise(&cc)
	session := NewSession(cfg.Ctx,
		connection,
		cfg.MaxMessageSize,
		cfg.Errors,
		cfg.DisableTCPSignalMessageCSM,
		cfg.CloseSocket,
		cfgOpts.InactivityMonitor,
		cfgOpts.RequestMonitor,
		cfg.ConnectionCacheSize,
		cfg.MessagePool,
	)
	cc.session = session
	if cc.processReceivedMessage == nil {
		cc.processReceivedMessage = processReceivedMessage
	}
	cc.receivedMessageReader = client.NewReceivedMessageReader(&cc, cfg.ReceivedMessageQueueSize)
	return &cc
}

func processReceivedMessage(req *pool.Message, cc *Conn, handler HandlerFunc) {
	cc.ProcessReceivedMessageWithHandler(req, handler)
}

func (cc *Conn) ProcessReceivedMessage(req *pool.Message) {
	if cc.oscoreProcessor != nil && req.Code() >= codes.Created && req.Code() < 192 {
		if err := cc.unprotectResponse(req); err != nil {
			cc.session.errors(fmt.Errorf("cannot unprotect OSCORE response: %w", err))
			cc.session.messagePool.ReleaseMessage(req)
			return
		}
	}
	cc.processReceivedMessage(req, cc, cc.handle)
}

func (cc *Conn) Session() *Session {
	return cc.session
}

// Close closes connection without wait of ends Run function.
func (cc *Conn) Close() error {
	err := cc.session.Close()
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (cc *Conn) doInternal(req *pool.Message) (_ *pool.Message, err error) {
	respChan, removeHandler, err := client.RegisterRequestHandler(req.Token(), cc.tokenHandlerContainer, &cc.tokenlessHandler)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = removeHandler(err)
	}()

	if err = cc.writeMessage(req); err != nil {
		return nil, fmt.Errorf("cannot write request: %w", err)
	}

	cc.receivedMessageReader.TryToReplaceLoop()

	select {
	case <-req.Context().Done():
		return nil, req.Context().Err()
	case <-cc.session.Context().Done():
		return nil, fmt.Errorf("connection was closed: %w", cc.Context().Err())
	case resp := <-respChan:
		return resp, nil
	}
}

// Do sends an coap message and returns an coap response.
//
// An error is returned if by failure to speak COAP (such as a network connectivity problem).
// Any status code doesn't cause an error.
//
// Caller is responsible to release request and response.
func (cc *Conn) do(req *pool.Message) (*pool.Message, error) {
	if !cc.peerBlockWiseTranferEnabled.Load() || cc.blockWise == nil {
		return cc.doInternal(req)
	}
	resp, err := cc.blockWise.Do(req, cc.blockwiseSZX, cc.session.maxMessageSize, cc.RemoteAddr(), cc.doInternal)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (cc *Conn) writeMessage(req *pool.Message) error {
	if cc.oscoreProcessor != nil && req.Code() >= codes.GET && req.Code() <= codes.IPATCH {
		return cc.writeProtectedRequest(req)
	}
	if cc.oscoreProcessor != nil && req.Code() >= codes.Created && req.Code() < 192 {
		if err := cc.protectObservationResponse(req); err != nil {
			return err
		}
	}
	return cc.session.WriteMessage(req)
}

func poolMessageToOSCOREMessage(msg *pool.Message) (message.Message, error) {
	payload, err := msg.ReadBody()
	if err != nil {
		return message.Message{}, err
	}
	return message.Message{
		Code:      msg.Code(),
		Token:     msg.Token(),
		Options:   msg.Options(),
		Payload:   payload,
		Type:      msg.Type(),
		MessageID: msg.MessageID(),
	}, nil
}

func (cc *Conn) writeProtectedRequest(req *pool.Message) error {
	original, err := poolMessageToOSCOREMessage(req)
	if err != nil {
		return fmt.Errorf("read request for OSCORE: %w", err)
	}
	protected, context, exchange, err := cc.oscoreProcessor.ProtectRequestWithContext(original)
	if err != nil {
		return err
	}
	observe := req.HasOption(message.Observe)
	key := string(req.Token())
	observeValue, observeErr := req.Options().Observe()
	isObserveRequest := observeErr == nil
	expires := time.Now().Add(247 * time.Second)
	cc.oscoreMu.Lock()
	previous, hadPrevious := cc.oscoreExchanges[key]
	next := tcpOSCOREExchange{
		context:  context,
		exchange: exchange,
		observe:  observe,
		expires:  expires,
	}
	if isObserveRequest && observeValue == 1 {
		if previousExchange, ok := cc.oscoreExchanges[key]; ok && previousExchange.observe {
			previous.cancelContext = context
			previous.cancelExchange = exchange
			previous.cancelExpires = time.Now().Add(247 * time.Second)
			next = previous
		}
	}
	cc.oscoreExchanges[key] = next
	cc.oscoreMu.Unlock()

	wireMessage := cc.AcquireMessage(req.Context())
	wireMessage.SetMessage(protected)
	defer cc.ReleaseMessage(wireMessage)
	if err := cc.session.WriteMessage(wireMessage); err != nil {
		cc.oscoreMu.Lock()
		if isObserveRequest && observeValue == 1 && hadPrevious {
			cc.oscoreExchanges[key] = previous
		} else {
			delete(cc.oscoreExchanges, key)
		}
		cc.oscoreMu.Unlock()
		return err
	}
	return nil
}

func (cc *Conn) unprotectResponse(response *pool.Message) error {
	key := string(response.Token())
	exchange, ok := cc.oscoreExchangeForResponse(key)
	if !ok {
		if !response.HasOption(message.OSCORE) {
			return nil
		}
		return oscore.ErrNoSecurityContext
	}
	if !response.HasOption(message.OSCORE) {
		if oscore.IsUnprotectedProcessingErrorCode(response.Code()) {
			cc.removeTCPOSCOREExchangeForProcessingError(key, exchange.exchange)
			response.SetContext(oscore.ContextWithUnprotectedProcessingError(response.Context()))
			return nil
		}
		return oscore.ErrOSCORERequired
	}
	outer, err := poolMessageToOSCOREMessage(response)
	if err != nil {
		return fmt.Errorf("read OSCORE response: %w", err)
	}
	plaintext, usedCancelExchange, err := cc.unprotectTCPResponseWithExchange(outer, exchange)
	if err != nil {
		return err
	}
	responseOption, err := oscore.UnmarshalOption(findTCPOSCOREOption(response.Options()))
	if err != nil {
		return fmt.Errorf("decode OSCORE response option: %w", err)
	}
	hasObserve := plaintext.Options.HasOption(message.Observe)
	if err := cc.updateTCPOSCOREExchangeResponse(key, exchange, responseOption, usedCancelExchange, hasObserve); err != nil {
		return err
	}
	response.SetMessage(plaintext)
	if hasObserve && len(responseOption.PartialIV) > 0 {
		observation.SetAuthenticatedSequenceNumber(response, tcpPartialIVNumber(responseOption.PartialIV))
	}
	return nil
}

func (cc *Conn) oscoreExchangeForResponse(key string) (tcpOSCOREExchange, bool) {
	cc.oscoreMu.Lock()
	exchange, ok := cc.oscoreExchanges[key]
	if ok && !exchange.expires.IsZero() && time.Now().After(exchange.expires) {
		delete(cc.oscoreExchanges, key)
		ok = false
	}
	cc.oscoreMu.Unlock()
	return exchange, ok
}

func (cc *Conn) unprotectTCPResponseWithExchange(
	outer message.Message,
	exchange tcpOSCOREExchange,
) (message.Message, bool, error) {
	plaintext, err := cc.oscoreProcessor.UnprotectResponse(outer, exchange.context, exchange.exchange)
	usedCancelExchange := false
	if err != nil && exchange.cancelContext != nil &&
		(exchange.cancelExpires.IsZero() || time.Now().Before(exchange.cancelExpires)) {
		plaintext, err = cc.oscoreProcessor.UnprotectResponse(outer, exchange.cancelContext, exchange.cancelExchange)
		usedCancelExchange = err == nil
	}
	if err != nil {
		return message.Message{}, false, err
	}
	return plaintext, usedCancelExchange, nil
}

func (cc *Conn) removeTCPOSCOREExchangeForProcessingError(key string, exchange oscore.Exchange) {
	cc.oscoreMu.Lock()
	defer cc.oscoreMu.Unlock()
	if current, exists := cc.oscoreExchanges[key]; exists && current.exchange == exchange {
		delete(cc.oscoreExchanges, key)
	}
}

func (cc *Conn) updateTCPOSCOREExchangeResponse(
	key string,
	exchange tcpOSCOREExchange,
	responseOption oscore.OptionValue,
	usedCancelExchange bool,
	hasObserve bool,
) error {
	cc.oscoreMu.Lock()
	defer cc.oscoreMu.Unlock()
	current, exists := cc.oscoreExchanges[key]
	if !exists || current.exchange != exchange.exchange {
		return nil
	}
	if err := updateTCPObserveResponse(&current, responseOption, usedCancelExchange, hasObserve); err != nil {
		return err
	}
	if usedCancelExchange || !hasObserve {
		delete(cc.oscoreExchanges, key)
		return nil
	}
	cc.oscoreExchanges[key] = current
	return nil
}

func updateTCPObserveResponse(
	current *tcpOSCOREExchange,
	responseOption oscore.OptionValue,
	usedCancelExchange bool,
	hasObserve bool,
) error {
	if !current.observe || usedCancelExchange {
		return nil
	}
	if current.hasObserveResponse && len(responseOption.PartialIV) == 0 {
		return oscore.ErrReplay
	}
	if len(responseOption.PartialIV) > 0 {
		notificationNumber := tcpPartialIVNumber(responseOption.PartialIV)
		if current.hasNotificationNumber && notificationNumber <= current.notificationNumber {
			return oscore.ErrReplay
		}
		current.hasNotificationNumber = true
		current.notificationNumber = notificationNumber
	}
	current.hasObserveResponse = true
	if hasObserve {
		current.expires = time.Time{}
	}
	return nil
}

func (cc *Conn) unprotectIncomingRequest(req *pool.Message) error {
	if cc.oscoreProcessor == nil {
		return nil
	}
	outer, err := poolMessageToOSCOREMessage(req)
	if err != nil {
		return fmt.Errorf("read incoming request: %w", err)
	}
	plaintext, securityContext, exchange, err := cc.oscoreProcessor.ProcessIncomingRequest(outer)
	if err != nil {
		return err
	}
	if securityContext != nil {
		req.SetContext(context.WithValue(req.Context(), tcpOSCORERequestContextKey{}, tcpOSCORERequestContext{
			context:  securityContext,
			exchange: exchange,
			options:  cloneTCPOSCOREOptions(plaintext.Options),
		}))
	}
	req.SetMessage(plaintext)
	return nil
}

func (cc *Conn) protectObservationResponse(resp *pool.Message) error {
	if cc.oscoreProcessor == nil || resp.HasOption(message.OSCORE) {
		return nil
	}
	key := string(resp.Token())
	cc.oscoreMu.Lock()
	binding, ok := cc.oscoreObservations[key]
	cc.oscoreMu.Unlock()
	if !ok {
		return nil
	}
	response, err := poolMessageToOSCOREMessage(resp)
	if err != nil {
		return fmt.Errorf("read Observe notification for OSCORE: %w", err)
	}
	protected, err := binding.context.ProtectResponseWithPartialIV(response, binding.exchange)
	if err != nil {
		return fmt.Errorf("protect OSCORE Observe notification: %w", err)
	}
	resp.SetMessage(protected)
	if !resp.HasOption(message.Observe) {
		cc.oscoreMu.Lock()
		delete(cc.oscoreObservations, key)
		cc.oscoreMu.Unlock()
	}
	return nil
}

func findTCPOSCOREOption(options message.Options) []byte {
	start, end, err := options.Find(message.OSCORE)
	if err != nil || end-start != 1 {
		return nil
	}
	return options[start].Value
}

func tcpPartialIVNumber(partialIV []byte) uint64 {
	var number uint64
	for _, b := range partialIV {
		number = number<<8 | uint64(b)
	}
	return number
}

// WriteMessage sends an coap message.
func (cc *Conn) WriteMessage(req *pool.Message) error {
	if !cc.peerBlockWiseTranferEnabled.Load() || cc.blockWise == nil {
		return cc.writeMessage(req)
	}
	return cc.blockWise.WriteMessage(req, cc.blockwiseSZX, cc.Session().maxMessageSize, cc.RemoteAddr(), cc.writeMessage)
}

// Context returns the client's context.
//
// If connections was closed context is cancelled.
func (cc *Conn) Context() context.Context {
	return cc.session.Context()
}

// AsyncPing sends ping and receivedPong will be called when pong arrives. It returns cancellation of ping operation.
func (cc *Conn) AsyncPing(receivedPong func()) (func(), error) {
	token, err := message.GetToken()
	if err != nil {
		return nil, fmt.Errorf("cannot get token: %w", err)
	}
	req := cc.session.messagePool.AcquireMessage(cc.Context())
	req.SetToken(token)
	req.SetCode(codes.Ping)
	defer cc.ReleaseMessage(req)

	if _, loaded := cc.tokenHandlerContainer.LoadOrStore(token.Hash(), func(_ *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
		if r.Code() == codes.Pong {
			receivedPong()
		}
	}); loaded {
		return nil, fmt.Errorf("cannot add token handler: %w", coapErrors.ErrKeyAlreadyExists)
	}
	removeTokenHandler := func() {
		_, _ = cc.tokenHandlerContainer.LoadAndDelete(token.Hash())
	}
	err = cc.session.WriteMessage(req)
	if err != nil {
		removeTokenHandler()
		return nil, fmt.Errorf("cannot write request: %w", err)
	}
	return removeTokenHandler, nil
}

// Run reads and process requests from a connection, until the connection is not closed.
func (cc *Conn) Run() (err error) {
	return cc.session.Run(cc)
}

func (cc *Conn) SetTCPSignalReceivedHandler(handler TCPSignalReceivedHandler) {
	cc.handlerMutex.Lock()
	defer cc.handlerMutex.Unlock()
	cc.tcpSignalReceivedHandler = handler
}

// AddOnClose calls function on close connection event.
func (cc *Conn) AddOnClose(f EventFunc) {
	cc.session.AddOnClose(f)
}

// RemoteAddr gets remote address.
func (cc *Conn) RemoteAddr() net.Addr {
	return cc.session.RemoteAddr()
}

func (cc *Conn) LocalAddr() net.Addr {
	return cc.session.LocalAddr()
}

// Sequence acquires sequence number.
func (cc *Conn) Sequence() uint64 {
	return cc.session.Sequence()
}

// SetContextValue stores the value associated with key to context of connection.
func (cc *Conn) SetContextValue(key interface{}, val interface{}) {
	cc.session.SetContextValue(key, val)
}

// Done signalizes that connection is not more processed.
func (cc *Conn) Done() <-chan struct{} {
	return cc.session.Done()
}

// CheckExpirations checks and remove expired items from caches.
func (cc *Conn) CheckExpirations(now time.Time) {
	cc.session.CheckExpirations(now, cc)
	if cc.blockWise != nil {
		cc.blockWise.CheckExpirations(now)
	}
	cc.oscoreMu.Lock()
	for token, exchange := range cc.oscoreExchanges {
		if !exchange.expires.IsZero() && !now.Before(exchange.expires) {
			delete(cc.oscoreExchanges, token)
			continue
		}
		if exchange.cancelContext != nil && !exchange.cancelExpires.IsZero() &&
			!now.Before(exchange.cancelExpires) {
			exchange.cancelContext = nil
			exchange.cancelExchange = oscore.Exchange{}
			exchange.cancelExpires = time.Time{}
			cc.oscoreExchanges[token] = exchange
		}
	}
	cc.oscoreMu.Unlock()
}

func (cc *Conn) AcquireMessage(ctx context.Context) *pool.Message {
	return cc.session.AcquireMessage(ctx)
}

func (cc *Conn) ReleaseMessage(m *pool.Message) {
	cc.session.ReleaseMessage(m)
}

// NetConn returns the underlying connection that is wrapped by cc. The Conn returned is shared by all invocations of NetConn, so do not modify it.
func (cc *Conn) NetConn() net.Conn {
	return cc.session.NetConn()
}

// DoObserve subscribes for every change with request.
func (cc *Conn) doObserve(req *pool.Message, observeFunc func(req *pool.Message)) (client.Observation, error) {
	return cc.observationHandler.NewObservation(req, observeFunc)
}

func (cc *Conn) CancelObservation(token message.Token) {
	key := string(token)
	cc.oscoreMu.Lock()
	delete(cc.oscoreObservations, key)
	if exchange, ok := cc.oscoreExchanges[key]; ok && exchange.observe {
		exchange.expires = time.Now().Add(247 * time.Second)
		cc.oscoreExchanges[key] = exchange
	}
	cc.oscoreMu.Unlock()
}

func (cc *Conn) ProcessReceivedMessageWithHandler(req *pool.Message, handler HandlerFunc) {
	generation := req.Generation()
	requestObserve, requestObserveErr := req.Observe()
	requestToken := string(req.Token())
	reqContext := req.Context()
	origResp := cc.AcquireMessage(cc.Context())
	origResp.SetToken(req.Token())
	w := responsewriter.New(origResp, cc, req.Options()...)
	handler(w, req)
	defer cc.ReleaseMessage(w.Message())
	var (
		requestBinding    tcpOSCORERequestContext
		hasRequestBinding bool
	)
	if reqContext != nil {
		requestBinding, hasRequestBinding = reqContext.Value(tcpOSCORERequestContextKey{}).(tcpOSCORERequestContext)
	}
	cc.session.messagePool.ReleaseMessageIfNotHijacked(req, generation)
	if !w.Message().IsModified() {
		return
	}
	if cc.oscoreProcessor != nil && hasRequestBinding {
		if err := cc.protectTCPResponseForRequest(
			w.Message(), requestBinding, requestToken, requestObserve, requestObserveErr,
		); err != nil {
			cc.session.errors(err)
			return
		}
	}
	err := cc.Session().WriteMessage(w.Message())
	if err != nil {
		if errC := cc.Close(); errC != nil {
			cc.Session().errors(fmt.Errorf("cannot close connection: %w", errC))
		}
		cc.Session().errors(fmt.Errorf("cannot write response to %v: %w", cc.RemoteAddr(), err))
	}
}

func (cc *Conn) protectTCPResponseForRequest(
	response *pool.Message,
	binding tcpOSCORERequestContext,
	token string,
	observeValue uint32,
	observeErr error,
) error {
	outer, err := poolMessageToOSCOREMessage(response)
	if err != nil {
		return fmt.Errorf("read response for OSCORE: %w", err)
	}
	var protected message.Message
	if tcpOSCOREResponseNeedsPartialIV(binding, outer.Options) {
		protected, err = binding.context.ProtectResponseWithPartialIV(outer, binding.exchange)
	} else {
		protected, err = binding.context.ProtectResponse(outer, binding.exchange)
	}
	if err != nil {
		return fmt.Errorf("protect OSCORE response: %w", err)
	}
	response.SetMessage(protected)
	cc.oscoreMu.Lock()
	defer cc.oscoreMu.Unlock()
	switch {
	case observeErr == nil && observeValue == 1:
		if current, exists := cc.oscoreObservations[token]; exists &&
			oscore.MatchesObserveRegistration(current.options, binding.options) {
			delete(cc.oscoreObservations, token)
		}
	case observeErr == nil && observeValue == 0 &&
		response.Code() >= codes.Created && response.Code() < codes.BadRequest &&
		response.HasOption(message.Observe):
		cc.oscoreObservations[token] = binding
	case binding.exchange.IsObserve() && !response.HasOption(message.Observe):
		delete(cc.oscoreObservations, token)
	}
	return nil
}

func (cc *Conn) blockwiseHandle(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
	cc.handleResponse(w, r, false)
}

func (cc *Conn) handleResponse(w *responsewriter.ResponseWriter[*Conn], r *pool.Message, removeHandler bool) {
	if len(r.Token()) == 0 && r.Code() >= codes.Created {
		if h, ok := cc.tokenlessHandler.Load(removeHandler); ok {
			h(w, r)
			return
		}
	} else if removeHandler {
		if h, ok := cc.tokenHandlerContainer.LoadAndDelete(r.Token().Hash()); ok {
			h(w, r)
			return
		}
	} else if h, ok := cc.tokenHandlerContainer.Load(r.Token().Hash()); ok {
		h(w, r)
		return
	}
	cc.observationHandler.Handle(w, r)
}

func (cc *Conn) handle(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
	if cc.blockWise != nil && cc.peerBlockWiseTranferEnabled.Load() {
		cc.blockWise.Handle(w, r, cc.blockwiseSZX, cc.Session().maxMessageSize, cc.RemoteAddr(), cc.blockwiseHandle)
		return
	}
	cc.handleResponse(w, r, true)
}

func (cc *Conn) sendPong(token message.Token) error {
	req := cc.AcquireMessage(cc.Context())
	defer cc.ReleaseMessage(req)
	req.SetCode(codes.Pong)
	req.SetToken(token)
	return cc.Session().WriteMessage(req)
}

func (cc *Conn) handleTCPSignalReceived(code codes.Code) {
	cc.handlerMutex.RLock()
	defer cc.handlerMutex.RUnlock()
	if cc.tcpSignalReceivedHandler != nil {
		cc.tcpSignalReceivedHandler(code)
	}
}

func (cc *Conn) handleSignals(r *pool.Message) bool {
	switch r.Code() {
	case codes.CSM:
		if cc.disablePeerTCPSignalMessageCSMs {
			return true
		}
		if size, err := r.GetOptionUint32(message.TCPMaxMessageSize); err == nil {
			cc.peerMaxMessageSize.Store(size)
		}
		if r.HasOption(message.TCPBlockWiseTransfer) {
			cc.peerBlockWiseTranferEnabled.Store(true)
		}

		// signal CSM message is received.
		cc.handleTCPSignalReceived(codes.CSM)
		return true
	case codes.Ping:
		// if r.HasOption(message.TCPCustody) {
		// TODO
		// }
		if err := cc.sendPong(r.Token()); err != nil && !coapNet.IsConnectionBrokenError(err) {
			cc.Session().errors(fmt.Errorf("cannot handle ping signal: %w", err))
		}

		cc.handleTCPSignalReceived(codes.Ping)
		return true
	case codes.Pong:
		if h, ok := cc.tokenHandlerContainer.LoadAndDelete(r.Token().Hash()); ok {
			cc.processReceivedMessage(r, cc, h)
		}

		cc.handleTCPSignalReceived(codes.Pong)
		return true
	case codes.Release:
		// if r.HasOption(message.TCPAlternativeAddress) {
		// TODO
		// }

		cc.handleTCPSignalReceived(codes.Release)
		return true
	case codes.Abort:
		// if r.HasOption(message.TCPBadCSMOption) {
		// TODO
		// }

		cc.handleTCPSignalReceived(codes.Abort)
		return true
	}
	return false
}

func (cc *Conn) pushToReceivedMessageQueue(r *pool.Message) {
	if cc.handleSignals(r) {
		return
	}
	select {
	case cc.receivedMessageReader.C() <- r:
	case <-cc.Context().Done():
	}
}
