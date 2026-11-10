package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
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
	"github.com/plgd-dev/go-coap/v3/options/config"
	"github.com/plgd-dev/go-coap/v3/pkg/cache"
	coapErrors "github.com/plgd-dev/go-coap/v3/pkg/errors"
	"github.com/plgd-dev/go-coap/v3/pkg/fn"
	pkgMath "github.com/plgd-dev/go-coap/v3/pkg/math"
	pkgRand "github.com/plgd-dev/go-coap/v3/pkg/rand"
	coapSync "github.com/plgd-dev/go-coap/v3/pkg/sync"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"go.uber.org/atomic"
	"golang.org/x/sync/semaphore"
)

// https://datatracker.ietf.org/doc/html/rfc7252#section-4.8.2
const ExchangeLifetime = 247 * time.Second

var retransmissionRNG = pkgRand.NewRand(time.Now().UnixNano())

type (
	HandlerFunc                 = func(*responsewriter.ResponseWriter[*Conn], *pool.Message)
	ErrorFunc                   = func(error)
	EventFunc                   = func()
	GetMIDFunc                  = func() int32
	CreateInactivityMonitorFunc = func() InactivityMonitor
	RequestMonitorFunc          = func(cc *Conn, req *pool.Message) (drop bool, err error)
)

type InactivityMonitor interface {
	Notify()
	CheckInactivity(now time.Time, cc *Conn)
}

type pendingConfirmableInactivityMonitor struct {
	InactivityMonitor
}

// DeferInactivityWhileConfirmableMessagesPending postpones inactivity checks
// until all outstanding Confirmable messages have completed.
func DeferInactivityWhileConfirmableMessagesPending(monitor InactivityMonitor) InactivityMonitor {
	return pendingConfirmableInactivityMonitor{InactivityMonitor: monitor}
}

func (m pendingConfirmableInactivityMonitor) CheckInactivity(now time.Time, cc *Conn) {
	if cc.midHandlerContainer.Length() > 0 {
		return
	}
	m.InactivityMonitor.CheckInactivity(now, cc)
}

// MalformedMessageError contains best-effort CoAP header metadata extracted
// during decode failure. HasHeader indicates whether Type and MessageID are
// valid and can be used by callers for protocol-level decisions.
type MalformedMessageError struct {
	Type           message.Type
	MessageID      int32
	HasHeader      bool
	InvalidVersion bool
	Err            error
}

func (e *MalformedMessageError) Error() string {
	return e.Err.Error()
}

func (e *MalformedMessageError) Unwrap() error {
	return e.Err
}

type Session interface {
	Context() context.Context
	Close() error
	MaxMessageSize() uint32
	RemoteAddr() net.Addr
	LocalAddr() net.Addr
	// NetConn returns the underlying connection that is wrapped by Session. The Conn returned is shared by all invocations of NetConn, so do not modify it.
	NetConn() net.Conn
	WriteMessage(req *pool.Message) error
	// WriteMulticast sends multicast to the remote multicast address.
	// By default it is sent over all network interfaces and all compatible source IP addresses with hop limit 1.
	// Via opts you can specify the network interface, source IP address, and hop limit.
	WriteMulticastMessage(req *pool.Message, address *net.UDPAddr, opts ...coapNet.MulticastOption) error
	Run(cc *Conn) error
	AddOnClose(f EventFunc)
	SetContextValue(key interface{}, val interface{})
	Done() <-chan struct{}
}

type RequestsMap = coapSync.Map[uint64, *pool.Message]

const (
	errFmtWriteRequest  = "cannot write request: %w"
	errFmtWriteResponse = "cannot write response: %w"
)

type midElement struct {
	handler            HandlerFunc
	start              time.Time
	deadline           time.Time
	acknowledgeTimeout time.Duration
	retransmit         atomic.Uint32
	retransmitMu       sync.Mutex
	retransmitting     bool
	retransmitFailures uint32
	waitForAcknowledge chan error

	private struct {
		sync.Mutex
		msg *pool.Message
	}
}

func (m *midElement) ReleaseMessage(cc *Conn) {
	m.private.Lock()
	defer m.private.Unlock()
	if m.private.msg != nil {
		cc.ReleaseMessage(m.private.msg)
		m.private.msg = nil
	}
}

func (m *midElement) IsExpired(now time.Time, maxRetransmit uint32) bool {
	if !m.deadline.IsZero() && now.After(m.deadline) {
		// remove element if deadline is exceeded
		return true
	}
	m.retransmitMu.Lock()
	defer m.retransmitMu.Unlock()
	retransmit := m.retransmit.Load()
	if retransmit > maxRetransmit {
		return true
	}
	return retransmit == maxRetransmit &&
		now.After(m.start.Add(retransmissionDelay(m.acknowledgeTimeout, retransmit)))
}

func (m *midElement) Retransmit(now time.Time) bool {
	m.retransmitMu.Lock()
	defer m.retransmitMu.Unlock()
	if m.retransmitting || !now.After(m.start.Add(retransmissionDelay(m.acknowledgeTimeout, m.retransmit.Load()))) {
		return false
	}
	m.retransmitting = true
	return true
}

func (m *midElement) MarkRetransmitted(now time.Time) {
	m.retransmitMu.Lock()
	m.start = now
	m.retransmit.Inc()
	m.retransmitting = false
	m.retransmitMu.Unlock()
}

func (m *midElement) MarkRetransmissionFailed(maxRetransmit uint32) bool {
	m.retransmitMu.Lock()
	m.retransmitFailures++
	exhausted := m.retransmitFailures >= maxRetransmit
	m.retransmitting = false
	m.retransmitMu.Unlock()
	return exhausted
}

func retransmissionDelay(acknowledgeTimeout time.Duration, retransmit uint32) time.Duration {
	if acknowledgeTimeout <= 0 {
		return acknowledgeTimeout
	}
	if retransmit >= 63 {
		return time.Duration(math.MaxInt64)
	}
	multiplier := int64(1) << retransmit
	if int64(acknowledgeTimeout) > math.MaxInt64/multiplier {
		return time.Duration(math.MaxInt64)
	}
	return acknowledgeTimeout * time.Duration(multiplier)
}

func (m *midElement) GetMessage(cc *Conn) (*pool.Message, bool, error) {
	m.private.Lock()
	defer m.private.Unlock()
	if m.private.msg == nil {
		return nil, false, nil
	}
	msg := cc.AcquireMessage(m.private.msg.Context())
	if err := m.private.msg.Clone(msg); err != nil {
		cc.ReleaseMessage(msg)
		return nil, false, err
	}
	return msg, true, nil
}

// MessageCache is a cache of CoAP messages.
type MessageCache interface {
	Load(key string, msg *pool.Message) (bool, error)
	Store(key string, msg *pool.Message) error
	CheckExpirations(time.Time)
}

// messageCache is a CoAP message cache backed by an in-memory cache.
type messageCache struct {
	c *cache.Cache[string, []byte]
}

// newMessageCache constructs a new CoAP message cache.
func newMessageCache() *messageCache {
	return &messageCache{
		c: cache.NewCache[string, []byte](),
	}
}

// Load loads a message from the cache if one exists with key.
func (m *messageCache) Load(key string, msg *pool.Message) (bool, error) {
	cachedResp := m.c.Load(key)
	if cachedResp == nil {
		return false, nil
	}
	if rawMsg := cachedResp.Data(); len(rawMsg) > 0 {
		_, err := msg.UnmarshalWithDecoder(coder.DefaultCoder, rawMsg)
		if err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// Store stores a message in the cache.
func (m *messageCache) Store(key string, msg *pool.Message) error {
	marshaledResp, err := msg.MarshalWithEncoder(coder.DefaultCoder)
	if err != nil {
		return err
	}
	cacheMsg := make([]byte, len(marshaledResp))
	copy(cacheMsg, marshaledResp)
	m.c.LoadOrStore(key, cache.NewElement(cacheMsg, time.Now().Add(ExchangeLifetime), nil))
	return nil
}

// CheckExpirations checks the cache for any expirations.
func (m *messageCache) CheckExpirations(now time.Time) {
	m.c.CheckExpirations(now)
}

type pendingMessage struct {
	sequence uint64
	expires  time.Time
}

// Conn represents a virtual connection to a conceptual endpoint, to perform COAPs commands.
type Conn struct {
	// This field needs to be the first in the struct to ensure proper word alignment on 32-bit platforms.
	// See: https://golang.org/pkg/sync/atomic/#pkg-note-BUG
	sequence atomic.Uint64

	session Session
	*client.Client[*Conn]
	inactivityMonitor InactivityMonitor
	requestMonitor    RequestMonitorFunc

	blockWise          *blockwise.BlockWise[*Conn]
	observationHandler *observation.Handler[*Conn]
	transmission       *Transmission
	messagePool        *pool.Pool

	processReceivedMessage config.ProcessReceivedMessageFunc[*Conn]
	errors                 ErrorFunc
	responseMsgCache       MessageCache
	msgIDMutex             *MutexMap

	tokenHandlerContainer *coapSync.Map[uint64, HandlerFunc]
	midHandlerContainer   *coapSync.Map[int32, *midElement]
	pendingMessagesMutex  sync.Mutex
	pendingMessages       map[int32]pendingMessage
	maxPendingMessages    uint32
	tokenlessHandler      client.TokenlessHandler[HandlerFunc]
	msgID                 atomic.Uint32
	blockwiseSZX          blockwise.SZX
	qBlockwiseMode        blockwise.QBlockwiseMode
	qParameters           blockwise.QBlockwiseConfig
	qProbeMu              sync.Mutex
	qProbeWait            chan struct{}
	qProbeSupported       bool
	qProbeExpires         time.Time
	qProbeRetry           time.Time
	qProbeRoute           string
	qProbeResult          *qCapabilityResult
	qConfigErr            error
	qDatagramLimit        uint32
	qRateMu               sync.Mutex
	qRateWake             chan struct{}
	qPeerActivity         time.Time
	qLastControl          time.Time
	qControlNotBefore     time.Time
	qLastBody             time.Time
	qBodyNotBefore        time.Time
	qBodyTag              string
	qRoutesMu             sync.Mutex
	qRoutes               map[string]*qBlockRoute
	qNotificationMIDs     map[int32]qNotificationMID
	qActiveRoutes         int
	qJobsMu               sync.Mutex
	qJobs                 map[string]qBlockJob
	qJobsWake             chan struct{}
	qAsyncSends           int

	oscoreProcessor                               *oscore.Processor
	oscoreProxy                                   bool
	oscoreOuterBlockwise                          bool
	oscoreOuterBlockwiseMaxReassembledPayloadSize uint32
	oscoreOuterBlockwiseErr                       error
	oscoreMu                                      sync.Mutex
	oscoreExchanges                               map[string]udpOSCOREExchange
	oscoreRequests                                map[int32]udpOSCORERequest
	oscoreResponses                               map[int32]udpOSCOREResponse
	oscoreObservations                            map[string]udpOSCORERequestContext

	localAddr      atomic.Pointer[net.IP]
	interfaceIndex atomic.Int64

	/*
		An outstanding interaction is either a CON for which an ACK has not
		yet been received but is still expected (message layer) or a request
		for which neither a response nor an Acknowledgment message has yet
		been received but is still expected (which may both occur at the same
		time, counting as one outstanding interaction).
	*/
	numOutstandingInteraction *semaphore.Weighted
	receivedMessageReader     *client.ReceivedMessageReader[*Conn]
}

// Transmission is a threadsafe container for transmission related parameters
type Transmission struct {
	nStart                  *atomic.Uint32
	acknowledgeTimeout      *atomic.Duration
	acknowledgeRandomFactor *atomic.Float64
	maxRetransmit           *atomic.Uint32
}

// SetTransmissionNStart changing the nStart value will only effect requests queued after the change. The requests waiting here already before the change will get unblocked when enough weight has been released.
func (t *Transmission) SetTransmissionNStart(d uint32) {
	t.nStart.Store(d)
}

func (t *Transmission) SetTransmissionAcknowledgeTimeout(d time.Duration) {
	t.acknowledgeTimeout.Store(d)
}

func (t *Transmission) SetTransmissionAcknowledgeRandomFactor(factor float64) {
	t.acknowledgeRandomFactor.Store(factor)
}

func (t *Transmission) SetTransmissionMaxRetransmit(d uint32) {
	t.maxRetransmit.Store(d)
}

func (cc *Conn) Transmission() *Transmission {
	return cc.transmission
}

func (t *Transmission) nextAcknowledgeTimeout() time.Duration {
	timeout := t.acknowledgeTimeout.Load()
	factor := t.acknowledgeRandomFactor.Load()
	if timeout <= 0 || factor <= 1 {
		return timeout
	}
	if math.IsNaN(factor) || math.IsInf(factor, 0) {
		return timeout
	}
	random := float64(retransmissionRNG.Uint32()) / (1 << 32)
	randomizedTimeout := float64(timeout) * (1 + random*(factor-1))
	if randomizedTimeout >= float64(math.MaxInt64) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(randomizedTimeout)
}

type ConnOptions struct {
	createBlockWise   func(cc *Conn) *blockwise.BlockWise[*Conn]
	inactivityMonitor InactivityMonitor
	requestMonitor    RequestMonitorFunc
	responseMsgCache  MessageCache
}

type Option = func(opts *ConnOptions)

// WithBlockWise enables block-wise transfer for the connection.
func WithBlockWise(createBlockWise func(cc *Conn) *blockwise.BlockWise[*Conn]) Option {
	return func(opts *ConnOptions) {
		opts.createBlockWise = createBlockWise
	}
}

// WithInactivityMonitor enables inactivity monitor for the connection.
func WithInactivityMonitor(inactivityMonitor InactivityMonitor) Option {
	return func(opts *ConnOptions) {
		opts.inactivityMonitor = inactivityMonitor
	}
}

// WithRequestMonitor enables request monitoring for the connection.
// It is called for each CoAP message received from the peer before it is processed.
// If it returns an error, the connection is closed.
// If it returns true, the message is dropped.
func WithRequestMonitor(requestMonitor RequestMonitorFunc) Option {
	return func(opts *ConnOptions) {
		opts.requestMonitor = requestMonitor
	}
}

// WithResponseMessageCache sets the cache used for response messages. All
// response messages are submitted to the cache, but it is up to the cache
// implementation to determine which messages are stored and for how long.
// Caching responses enables sending the same Acknowledgment for retransmitted
// confirmable messages within an EXCHANGE_LIFETIME. It may be desirable to
// relax this behavior in some scenarios.
// https://datatracker.ietf.org/doc/html/rfc7252#section-4.5
// The default response message cache stores all responses with an expiration of
// 247 seconds, which is EXCHANGE_LIFETIME when using default CoAP transmission
// parameters.
// https://datatracker.ietf.org/doc/html/rfc7252#section-4.8.2
func WithResponseMessageCache(cache MessageCache) Option {
	return func(opts *ConnOptions) {
		opts.responseMsgCache = cache
	}
}

func NewConnWithOpts(session Session, cfg *Config, opts ...Option) *Conn {
	normalizeConnConfig(cfg)
	cfgOpts := resolveConnOptions(opts)
	cc := newConn(session, cfg, cfgOpts)
	cc.initializeBlockwise()
	cc.initializeQBlockwise(cfg)
	cc.qDatagramLimit = session.MaxMessageSize()
	if cfg.MTU != 0 {
		cc.qDatagramLimit = min(cc.qDatagramLimit, uint32(cfg.MTU))
		if cfg.QBlockwiseSecure {
			// Reserve a conservative DTLS record/header/MAC allowance; cipher
			// suites have different record expansion, unlike plain UDP.
			cc.qDatagramLimit -= min(cc.qDatagramLimit, 128)
		}
	}
	cc.AddOnClose(func() {
		cc.closeQBlocks()
	})
	return cc
}

func normalizeConnConfig(cfg *Config) {
	if cfg.Errors == nil {
		cfg.Errors = func(error) {
			// default no-op
		}
	}
	if cfg.GetMID == nil {
		cfg.GetMID = message.GetMID
	}
	if cfg.GetToken == nil {
		cfg.GetToken = message.GetToken
	}
	if cfg.ReceivedMessageQueueSize < 0 {
		cfg.ReceivedMessageQueueSize = 0
	}
}

func resolveConnOptions(opts []Option) ConnOptions {
	cfgOpts := ConnOptions{
		createBlockWise: func(*Conn) *blockwise.BlockWise[*Conn] {
			return nil
		},
		inactivityMonitor: inactivity.NewNilMonitor[*Conn](),
		requestMonitor: func(*Conn, *pool.Message) (bool, error) {
			return false, nil
		},
	}
	for _, o := range opts {
		o(&cfgOpts)
	}
	// Only construct cache if one was not set via options.
	if cfgOpts.responseMsgCache == nil {
		cfgOpts.responseMsgCache = newMessageCache()
	}
	return cfgOpts
}

func newConn(session Session, cfg *Config, cfgOpts ConnOptions) *Conn {
	cc := Conn{
		session: session,
		transmission: &Transmission{
			nStart:                  atomic.NewUint32(cfg.TransmissionNStart),
			acknowledgeTimeout:      atomic.NewDuration(cfg.TransmissionAcknowledgeTimeout),
			acknowledgeRandomFactor: atomic.NewFloat64(cfg.TransmissionAcknowledgeRandomFactor),
			maxRetransmit:           atomic.NewUint32(cfg.TransmissionMaxRetransmit),
		},
		blockwiseSZX:   cfg.BlockwiseSZX,
		qBlockwiseMode: cfg.QBlockwiseMode,

		tokenHandlerContainer:  coapSync.NewMap[uint64, HandlerFunc](),
		midHandlerContainer:    coapSync.NewMap[int32, *midElement](),
		processReceivedMessage: cfg.ProcessReceivedMessage,
		oscoreProcessor:        cfg.OSCORE,
		oscoreProxy:            cfg.OSCOREProxy,
		oscoreOuterBlockwise:   cfg.OSCOREOuterBlockwise,
		oscoreOuterBlockwiseMaxReassembledPayloadSize: cfg.OSCOREOuterBlockwiseMaxReassembledPayloadSize,
		oscoreExchanges:           make(map[string]udpOSCOREExchange),
		oscoreRequests:            make(map[int32]udpOSCORERequest),
		oscoreResponses:           make(map[int32]udpOSCOREResponse),
		oscoreObservations:        make(map[string]udpOSCORERequestContext),
		errors:                    cfg.Errors,
		msgIDMutex:                NewMutexMap(),
		responseMsgCache:          cfgOpts.responseMsgCache,
		inactivityMonitor:         cfgOpts.inactivityMonitor,
		requestMonitor:            cfgOpts.requestMonitor,
		messagePool:               cfg.MessagePool,
		maxPendingMessages:        cfg.MaxPendingMessages,
		numOutstandingInteraction: semaphore.NewWeighted(math.MaxInt64),
	}
	cc.msgID.Store(pkgMath.CastTo[uint32](cfg.GetMID() - 0xffff/2))
	cc.blockWise = cfgOpts.createBlockWise(&cc)
	return &cc
}

func (cc *Conn) initializeBlockwise() {
	if cc.oscoreOuterBlockwise {
		switch {
		case cc.oscoreProcessor == nil && !cc.oscoreProxy:
			cc.oscoreOuterBlockwiseErr = errors.New("OSCORE Outer Blockwise requires OSCORE or OSCORE proxy mode")
		case cc.blockWise == nil:
			cc.oscoreOuterBlockwiseErr = errors.New("OSCORE Outer Blockwise requires Blockwise")
		default:
			if cc.oscoreOuterBlockwiseMaxReassembledPayloadSize == 0 {
				cc.oscoreOuterBlockwiseMaxReassembledPayloadSize = oscore.MaxUnfragmentedSize
			}
			cc.blockWise.SetMaxReassembledPayloadSize(int64(cc.oscoreOuterBlockwiseMaxReassembledPayloadSize))
		}
		if cc.oscoreOuterBlockwiseErr != nil {
			cc.errors(cc.oscoreOuterBlockwiseErr)
		}
	}
}

func (cc *Conn) initializeQBlockwise(cfg *Config) {
	limitParallelRequests := limitparallelrequests.New(cfg.LimitClientParallelRequests, cfg.LimitClientEndpointParallelRequests, cc.do, cc.doObserve)
	cc.observationHandler = observation.NewHandler(cc, cfg.Handler, limitParallelRequests.Do)
	cc.Client = client.New(cc, cc.observationHandler, cfg.GetToken, limitParallelRequests)
	if cc.processReceivedMessage == nil {
		cc.processReceivedMessage = processReceivedMessage
	}
	cc.receivedMessageReader = client.NewReceivedMessageReader(cc, cfg.ReceivedMessageQueueSize)
	cc.qConfigErr = blockwise.ValidateQBlockwiseConfig(cfg.QBlockwiseMode, cfg.BlockwiseEnable)
	parameters, parametersErr := cfg.QBlockwiseConfig.Resolve()
	if parametersErr != nil {
		cc.qConfigErr = parametersErr
	} else {
		cc.qParameters = parameters
		if cc.blockWise != nil {
			cc.qConfigErr = errors.Join(cc.qConfigErr, cc.blockWise.SetQBlockwiseConfig(parameters))
		}
	}
}

// NewConn creates connection over session and observation.
func NewConn(
	session Session,
	createBlockWise func(cc *Conn) *blockwise.BlockWise[*Conn],
	inactivityMonitor InactivityMonitor,
	cfg *Config,
) *Conn {
	return NewConnWithOpts(session, cfg, WithBlockWise(createBlockWise), WithInactivityMonitor(inactivityMonitor))
}

func processReceivedMessage(req *pool.Message, cc *Conn, handler config.HandlerFunc[*Conn]) {
	cc.ProcessReceivedMessageWithHandler(req, handler)
}

// ProcessReceivedMessage tracks in-flight duplicates until the configured
// processor returns. Processors dispatching asynchronous work must either wait
// for completion before returning or manage subsequent deduplication themselves.
func (cc *Conn) ProcessReceivedMessage(req *pool.Message) {
	mid, sequence := req.MessageID(), req.Sequence()
	defer cc.deletePendingMessage(mid, sequence)
	cc.processReceivedMessage(req, cc, cc.handleReq)
}

func (cc *Conn) Session() Session {
	return cc.session
}

func (cc *Conn) GetMessageID() int32 {
	// To prevent collisions during reconnections, it is important to always increment the global counter.
	// For example, if a connection (cc) is established and later closed due to inactivity, a new cc may
	// be created shortly after. However, if the new cc is initialized with the same message ID as the
	// previous one, the receiver may mistakenly treat the incoming message as a duplicate and discard it.
	// Hence, by incrementing the global counter, we can ensure unique message IDs and avoid such issues.
	message.GetMID()
	return int32(pkgMath.CastTo[uint16](cc.msgID.Inc()))
}

// Close closes connection without waiting for the end of the Run function.
func (cc *Conn) Close() error {
	err := cc.session.Close()
	cc.pendingMessagesMutex.Lock()
	clear(cc.pendingMessages)
	cc.pendingMessagesMutex.Unlock()
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

	err = cc.writeMessage(req)
	if err != nil {
		return nil, fmt.Errorf(errFmtWriteRequest, err)
	}
	cc.receivedMessageReader.TryToReplaceLoop()
	select {
	case <-req.Context().Done():
		return nil, req.Context().Err()
	case <-cc.Context().Done():
		return nil, fmt.Errorf("connection was closed: %w", cc.session.Context().Err())
	case resp := <-respChan:
		return resp, nil
	}
}

func requestMayHaveBeenSent(err error) bool {
	var notSent *client.RequestNotSentError
	return !errors.As(err, &notSent)
}

// Do sends an coap message and returns an coap response.
//
// An error is returned if by failure to speak COAP (such as a network connectivity problem).
// Any status code doesn't cause an error.
//
// Caller is responsible to release request and response.
func (cc *Conn) do(req *pool.Message) (*pool.Message, error) {
	if cc.oscoreOuterBlockwiseErr != nil {
		return nil, cc.oscoreOuterBlockwiseErr
	}
	if cc.qConfigErr != nil {
		return nil, cc.qConfigErr
	}
	hasQOption := req.HasOption(message.QBlock1) || req.HasOption(message.QBlock2)
	if hasQOption && !cc.qBlockAllowed() {
		return nil, blockwise.ErrQBlockDisabled
	}
	if hasQOption && (req.HasOption(message.Block1) || req.HasOption(message.Block2)) {
		return nil, blockwise.ErrInvalidOptionBlock1
	}
	if hasQOption {
		return cc.doInternal(req)
	}
	if cc.blockWise == nil {
		return cc.doInternal(req)
	}
	useQ, selectErr := cc.selectQBlock(req)
	if selectErr != nil {
		return nil, selectErr
	}
	if useQ {
		return cc.doQBlock(req)
	}
	if cc.oscoreOuterBlockwise {
		return cc.doOuterBlockwise(req)
	}
	return cc.doBlockwise(req)
}

func (cc *Conn) doOuterBlockwise(req *pool.Message) (*pool.Message, error) {
	wireReq, finish, err := cc.protectOutgoingRequest(req)
	if err != nil {
		return nil, err
	}
	err = cc.checkOuterBlockwisePayloadSize(wireReq)
	if err != nil {
		if finish != nil {
			finish(false)
		}
		return nil, err
	}
	wireReq.SetContext(context.WithValue(wireReq.Context(), oscoreOuterBlockwiseContextKey{}, true))
	sent := false
	if finish != nil {
		defer func() { finish(sent) }()
	}
	resp, blockwiseErr := cc.blockWise.Do(wireReq, cc.blockwiseSZX, cc.session.MaxMessageSize(), cc.RemoteAddr(), func(bwReq *pool.Message) (*pool.Message, error) {
		if bwReq.Options().HasOption(message.Block1) || bwReq.Options().HasOption(message.Block2) {
			bwReq.SetMessageID(cc.GetMessageID())
		}
		response, requestErr := cc.doInternal(bwReq)
		sent = sent || requestMayHaveBeenSent(requestErr)
		return response, requestErr
	})
	if blockwiseErr != nil {
		return nil, blockwiseErr
	}
	if resp.HasOption(message.OSCORE) || cc.hasOSCOREExchange(resp.Token()) {
		if err = cc.unprotectIncomingResponse(resp); err != nil {
			cc.ReleaseMessage(resp)
			return nil, fmt.Errorf("cannot unprotect reassembled OSCORE response: %w", err)
		}
	}
	return resp, nil
}

func (cc *Conn) doBlockwise(req *pool.Message) (*pool.Message, error) {
	resp, err := cc.blockWise.Do(req, cc.blockwiseSZX, cc.session.MaxMessageSize(), cc.RemoteAddr(), func(bwReq *pool.Message) (*pool.Message, error) {
		if bwReq.Options().HasOption(message.Block1) || bwReq.Options().HasOption(message.Block2) {
			bwReq.SetMessageID(cc.GetMessageID())
		}
		return cc.doInternal(bwReq)
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (cc *Conn) qBlockCapability(request *pool.Message) (bool, error) {
	ctx := request.Context()
	routeKey, originOptions := qBlockOrigin(request.Options())
	for {
		cc.qProbeMu.Lock()
		if cc.qProbeRoute == routeKey && time.Now().Before(cc.qProbeExpires) {
			supported := cc.qProbeSupported
			cc.qProbeMu.Unlock()
			return supported, nil
		}
		if cc.qProbeRoute == routeKey && time.Now().Before(cc.qProbeRetry) {
			cc.qProbeMu.Unlock()
			return false, nil
		}
		wait, result := cc.qProbeWait, cc.qProbeResult
		if wait == nil {
			wait = make(chan struct{})
			result = &qCapabilityResult{route: routeKey}
			cc.qProbeWait, cc.qProbeResult = wait, result
			cc.qProbeRoute = routeKey
			cc.qProbeExpires, cc.qProbeRetry = time.Time{}, time.Time{}
			// A caller does not own the lifetime of the shared probe.
			go cc.runQBlockProbe(originOptions, result, wait)
		}
		cc.qProbeMu.Unlock()
		if err := cc.waitQBlockCapability(ctx, wait); err != nil {
			return false, err
		}
		if result.route != routeKey {
			continue
		}
		return result.supported, result.err
	}
}

func (cc *Conn) runQBlockProbe(originOptions message.Options, result *qCapabilityResult, wait chan struct{}) {
	supported, err := cc.probeQBlock(cc.Context(), originOptions)
	cc.qProbeMu.Lock()
	cc.qProbeSupported = supported
	result.supported, result.err = supported, err
	if err == nil {
		cc.qProbeExpires = time.Now().Add(10 * time.Minute)
	} else {
		cc.qProbeRetry = time.Now().Add(time.Second)
	}
	cc.qProbeWait = nil
	close(wait)
	cc.qProbeMu.Unlock()
}

func (cc *Conn) waitQBlockCapability(ctx context.Context, wait <-chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-cc.Context().Done():
		return cc.Context().Err()
	case <-wait:
		return nil
	}
}

func (cc *Conn) probeQBlock(ctx context.Context, originOptions message.Options) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req := cc.AcquireMessage(probeCtx)
	defer cc.ReleaseMessage(req)
	req.SetCode(codes.GET)
	req.SetType(message.Confirmable)
	token, err := message.GetToken()
	if err != nil {
		return false, err
	}
	req.SetToken(token)
	req.ResetOptionsTo(originOptions)
	if pathErr := req.SetPath("/.well-known/core"); pathErr != nil {
		return false, pathErr
	}
	option, err := blockwise.EncodeQBlockOption(blockwise.SZX16, 0, false)
	if err != nil {
		return false, err
	}
	req.SetOptionUint32(message.QBlock2, option)
	response, err := cc.doInternal(req)
	if err != nil {
		return false, err
	}
	defer cc.ReleaseMessage(response)
	if response.Code() == codes.BadOption {
		if len(originOptions) != 0 && !qProbeBadOptionRelevant(response) {
			return false, blockwise.ErrQBlockUnsupported
		}
		return false, nil
	}
	if response.Code() < codes.Created || response.Code() >= codes.BadRequest {
		return false, blockwise.ErrQBlockUnsupported
	}
	if qOptionCount(response, message.QBlock2) != 1 {
		return false, blockwise.ErrQBlockUnsupported
	}
	responseOption, err := response.GetOptionUint32(message.QBlock2)
	if err != nil {
		return false, blockwise.ErrQBlockUnsupported
	}
	szx, num, _, err := blockwise.DecodeQBlockOption(responseOption)
	if err != nil || szx != blockwise.SZX16 || num != 0 {
		return false, blockwise.ErrQBlockUnsupported
	}
	return true, nil
}

func qProbeBadOptionRelevant(response *pool.Message) bool {
	diagnostic, err := response.ReadBody()
	if err != nil {
		return false
	}
	text := strings.ToLower(string(diagnostic))
	if strings.Contains(text, "q-block2") {
		return true
	}
	if !strings.Contains(text, "option") {
		return false
	}
	for _, number := range strings.FieldsFunc(text, func(r rune) bool { return r < '0' || r > '9' }) {
		if number == "31" {
			return true
		}
	}
	return false
}

func qOptionCount(msg *pool.Message, id message.OptionID) int {
	count := 0
	for _, option := range msg.Options() {
		if option.ID == id {
			count++
		}
	}
	return count
}

// DoObserve subscribes for every change with request.
func (cc *Conn) doObserve(req *pool.Message, observeFunc func(req *pool.Message)) (client.Observation, error) {
	if cc.qConfigErr != nil {
		return nil, cc.qConfigErr
	}
	useQ, err := cc.selectQBlock(req)
	if err != nil {
		return nil, err
	}
	if useQ {
		if prepareErr := cc.prepareQObservation(req); prepareErr != nil {
			return nil, prepareErr
		}
	}
	return cc.observationHandler.NewObservation(req, observeFunc)
}

func (cc *Conn) releaseOutstandingInteraction() {
	cc.numOutstandingInteraction.Release(1)
}

func (cc *Conn) acquireOutstandingInteraction(ctx context.Context) error {
	nStart := cc.Transmission().nStart.Load()
	if nStart == 0 {
		return fmt.Errorf("invalid NStart value %v", nStart)
	}
	n := math.MaxInt64 - int64(cc.Transmission().nStart.Load()) + 1
	err := cc.numOutstandingInteraction.Acquire(ctx, n)
	if err != nil {
		return err
	}
	cc.numOutstandingInteraction.Release(n - 1)
	return nil
}

func notifyWaitForAcknowledge(waitForAcknowledge chan error, err error) {
	if waitForAcknowledge == nil {
		return
	}
	select {
	case waitForAcknowledge <- err:
	default:
	}
}

func (cc *Conn) waitForAcknowledge(req *pool.Message, waitForResponseChan chan error) error {
	cc.receivedMessageReader.TryToReplaceLoop()
	select {
	case err := <-waitForResponseChan:
		return err
	case <-req.Context().Done():
		return req.Context().Err()
	case <-cc.Context().Done():
		return fmt.Errorf("connection was closed: %w", cc.Context().Err())
	}
}

func (cc *Conn) prepareWriteMessage(req *pool.Message, handler HandlerFunc, waitForAcknowledge chan error) (func(), error) {
	var closeFns fn.FuncList

	// Only confirmable messages ever match an message ID
	switch req.Type() {
	case message.Confirmable:
		msg := cc.AcquireMessage(req.Context())
		if err := req.Clone(msg); err != nil {
			cc.ReleaseMessage(msg)
			return nil, fmt.Errorf("cannot clone message: %w", err)
		}
		if req.Code() >= codes.GET && req.Code() < 32 || req.HasOption(message.QBlock1) || req.HasOption(message.QBlock2) {
			if err := cc.acquireOutstandingInteraction(req.Context()); err != nil {
				cc.ReleaseMessage(msg)
				return nil, err
			}
			closeFns = append(closeFns, func() {
				cc.releaseOutstandingInteraction()
			})
		}
		deadline, _ := req.Context().Deadline()
		if _, loaded := cc.midHandlerContainer.LoadOrStore(req.MessageID(), &midElement{
			handler:            handler,
			start:              time.Now(),
			deadline:           deadline,
			acknowledgeTimeout: cc.transmission.nextAcknowledgeTimeout(),
			waitForAcknowledge: waitForAcknowledge,
			private: struct {
				sync.Mutex
				msg *pool.Message
			}{msg: msg},
		}); loaded {
			closeFns.Execute()
			return nil, fmt.Errorf("cannot insert mid(%v) handler: %w", req.MessageID(), coapErrors.ErrKeyAlreadyExists)
		}
		closeFns = append(closeFns, func() {
			_, _ = cc.midHandlerContainer.LoadAndDelete(req.MessageID())
		})
	case message.NonConfirmable:
		/* TODO need to acquireOutstandingInteraction
		if req.Code() >= codes.GET && req.Code() <= codes.DELETE {
		}
		*/
	}
	return closeFns.ToFunction(), nil
}

func (cc *Conn) writeMessageAsync(req *pool.Message) error {
	req.UpsertType(message.Confirmable)
	req.UpsertMessageID(cc.GetMessageID())
	if err := cc.rememberQNotification(req); err != nil {
		return err
	}
	wireReq, releaseWireReq, err := cc.protectOutgoingRequest(req)
	if err != nil {
		return &client.RequestNotSentError{Err: err}
	}
	sent := false
	if releaseWireReq != nil {
		defer func() { releaseWireReq(sent) }()
	}
	closeFn, err := cc.prepareWriteMessage(wireReq, func(*responsewriter.ResponseWriter[*Conn], *pool.Message) {
		// do nothing
	}, nil)
	if err != nil {
		return &client.RequestNotSentError{Err: err}
	}
	defer closeFn()
	if err := cc.session.WriteMessage(wireReq); err != nil {
		return fmt.Errorf(errFmtWriteRequest, err)
	}
	sent = true
	return nil
}

func (cc *Conn) writeMessage(req *pool.Message) error {
	req.UpsertType(message.Confirmable)
	req.UpsertMessageID(cc.GetMessageID())
	cc.upsertControlInformation(req)

	if req.Type() != message.Confirmable {
		return cc.writeMessageAsync(req)
	}
	respChan := make(chan error, 1)
	if err := cc.rememberQNotification(req); err != nil {
		return err
	}
	wireReq, releaseWireReq, err := cc.protectOutgoingRequest(req)
	if err != nil {
		return &client.RequestNotSentError{Err: err}
	}
	sent := false
	if releaseWireReq != nil {
		defer func() { releaseWireReq(sent) }()
	}
	closeFn, err := cc.prepareWriteMessage(wireReq, func(_ *responsewriter.ResponseWriter[*Conn], response *pool.Message) {
		var responseErr error
		if response.Type() == message.Reset && req.HasOption(message.Observe) && req.HasOption(message.QBlock2) {
			responseErr = errors.New("q-block Observe notification reset by peer")
		}
		notifyWaitForAcknowledge(respChan, responseErr)
	}, respChan)
	if err != nil {
		return &client.RequestNotSentError{Err: err}
	}
	defer closeFn()
	if err := cc.session.WriteMessage(wireReq); err != nil {
		return fmt.Errorf(errFmtWriteRequest, err)
	}
	sent = true
	if err := cc.waitForAcknowledge(req, respChan); err != nil {
		return fmt.Errorf(errFmtWriteRequest, err)
	}
	return nil
}

// WriteMessage sends an coap message.
func (cc *Conn) WriteMessage(req *pool.Message) error {
	if cc.oscoreOuterBlockwiseErr != nil {
		return cc.oscoreOuterBlockwiseErr
	}
	if cc.qConfigErr != nil {
		return cc.qConfigErr
	}
	if handled, err := cc.writeQBlockMessage(req); handled {
		return err
	}
	if cc.blockWise == nil {
		return cc.writeMessage(req)
	}
	if cc.shouldUseOuterBlockwise(req) {
		return cc.writeOuterBlockwise(req)
	}
	return cc.writeBlockwise(req)
}

func (cc *Conn) writeQBlockMessage(req *pool.Message) (bool, error) {
	if handled, err := cc.writeQBlockFetchObserve(req); handled {
		return true, err
	}
	if !req.HasOption(message.QBlock1) && !req.HasOption(message.QBlock2) {
		return false, nil
	}
	if !cc.qBlockAllowed() {
		return true, blockwise.ErrQBlockDisabled
	}
	if req.HasOption(message.Block1) || req.HasOption(message.Block2) {
		return true, blockwise.ErrInvalidOptionBlock1
	}
	return true, cc.writeMessage(req)
}

func (cc *Conn) shouldUseOuterBlockwise(req *pool.Message) bool {
	return cc.oscoreOuterBlockwise && req.Code() >= codes.GET && req.Code() <= codes.IPATCH
}

func (cc *Conn) checkOuterBlockwisePayloadSize(req *pool.Message) error {
	size, err := req.BodySize()
	if err != nil {
		return fmt.Errorf("cannot determine OSCORE Outer Blockwise payload size: %w", err)
	}
	limit := int64(cc.oscoreOuterBlockwiseMaxReassembledPayloadSize)
	if limit > 0 && size > limit {
		return fmt.Errorf("%w: OSCORE Outer Blockwise payload size %d exceeds configured limit %d",
			blockwise.ErrRequestEntityTooLarge, size, limit)
	}
	return nil
}

func (cc *Conn) writeOuterBlockwise(req *pool.Message) error {
	wireReq, finish, err := cc.protectOutgoingRequest(req)
	if err != nil {
		return err
	}
	err = cc.checkOuterBlockwisePayloadSize(wireReq)
	if err != nil {
		if finish != nil {
			finish(false)
		}
		return err
	}
	wireReq.SetContext(context.WithValue(wireReq.Context(), oscoreOuterBlockwiseContextKey{}, true))
	sent := false
	if finish != nil {
		defer func() { finish(sent) }()
	}
	err = cc.blockWise.WriteMessage(wireReq, cc.blockwiseSZX, cc.session.MaxMessageSize(), cc.RemoteAddr(), func(bwReq *pool.Message) error {
		if bwReq.Options().HasOption(message.Block1) || bwReq.Options().HasOption(message.Block2) {
			bwReq.SetMessageID(cc.GetMessageID())
		}
		writeErr := cc.writeMessage(bwReq)
		sent = sent || requestMayHaveBeenSent(writeErr)
		return writeErr
	})
	return err
}

func (cc *Conn) writeBlockwise(req *pool.Message) error {
	return cc.blockWise.WriteMessage(req, cc.blockwiseSZX, cc.session.MaxMessageSize(), cc.RemoteAddr(), func(bwReq *pool.Message) error {
		if bwReq.Options().HasOption(message.Block1) || bwReq.Options().HasOption(message.Block2) {
			bwReq.SetMessageID(cc.GetMessageID())
		}
		return cc.writeMessage(bwReq)
	})
}

func (cc *Conn) writeQBlockFetchObserve(req *pool.Message) (bool, error) {
	if req.Code() != codes.FETCH || req.Body() == nil || !req.HasOption(message.QBlock2) {
		return false, nil
	}
	observe, err := req.Observe()
	if err != nil || observe != 0 {
		return false, nil
	}
	if !cc.qBlockAllowed() {
		return true, blockwise.ErrQBlockDisabled
	}
	szx, err := cc.qBlockRequestSZX(req)
	if err != nil {
		return true, err
	}
	response, err := cc.blockWise.DoQBlock1(req, szx, 16<<20, cc.doInternal)
	if err != nil {
		return true, err
	}
	defer cc.ReleaseMessage(response)
	if !cc.observationHandler.HandleNotification(response) {
		return true, errors.New("Q-Block FETCH Observe response has no active observation")
	}
	return true, nil
}

// Context returns the client's context.
//
// If connections was closed context is cancelled.
func (cc *Conn) Context() context.Context {
	return cc.session.Context()
}

// AsyncPing sends ping and receivedPong will be called when pong arrives. It returns cancellation of ping operation.
func (cc *Conn) AsyncPing(receivedPong func()) (func(), error) {
	req := cc.AcquireMessage(cc.Context())
	req.SetType(message.Confirmable)
	req.SetCode(codes.Empty)
	mid := cc.GetMessageID()
	req.SetMessageID(mid)
	// Keepalive pings must reuse the original destination IP as the packet source
	// (e.g. Fly.io public IP), same as regular request/response writes.
	cc.upsertControlInformation(req)
	if _, loaded := cc.midHandlerContainer.LoadOrStore(mid, &midElement{
		handler: func(_ *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
			if r.Type() == message.Reset || r.Type() == message.Acknowledgement {
				receivedPong()
			}
		},
		start:              time.Now(),
		deadline:           time.Time{}, // no deadline
		acknowledgeTimeout: cc.transmission.nextAcknowledgeTimeout(),
		private: struct {
			sync.Mutex
			msg *pool.Message
		}{msg: req},
	}); loaded {
		return nil, fmt.Errorf("cannot insert mid(%v) handler: %w", mid, coapErrors.ErrKeyAlreadyExists)
	}
	removeMidHandler := func() {
		if elem, ok := cc.midHandlerContainer.LoadAndDelete(mid); ok {
			elem.ReleaseMessage(cc)
		}
	}
	if err := cc.session.WriteMessage(req); err != nil {
		removeMidHandler()
		return nil, fmt.Errorf(errFmtWriteRequest, err)
	}
	return removeMidHandler, nil
}

// Run reads and process requests from a connection, until the connection is closed.
func (cc *Conn) Run() error {
	return cc.session.Run(cc)
}

// AddOnClose calls function on close connection event.
func (cc *Conn) AddOnClose(f EventFunc) {
	cc.session.AddOnClose(f)
}

func (cc *Conn) RemoteAddr() net.Addr {
	return cc.session.RemoteAddr()
}

func (cc *Conn) LocalAddr() net.Addr {
	return cc.session.LocalAddr()
}

func (cc *Conn) sendPong(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
	if err := w.SetResponse(codes.Empty, message.TextPlain, nil); err != nil {
		cc.errors(fmt.Errorf("cannot send pong response: %w", err))
	}
	if r.Type() == message.Confirmable {
		// RFC 7252 §4.2: An Empty Confirmable message (CoAP Ping) is rejected
		// with a matching Reset message.
		w.Message().SetType(message.Reset)
		w.Message().SetMessageID(r.MessageID())
	} else {
		if w.Message().Type() != message.Reset {
			w.Message().SetType(message.NonConfirmable)
		}
		w.Message().SetMessageID(cc.GetMessageID())
	}
}

func (cc *Conn) handle(w *responsewriter.ResponseWriter[*Conn], m *pool.Message) {
	cc.checkQObservationCancellation(m)
	if m.IsSeparateMessage() {
		// msg was processed by token handler - just drop it.
		return
	}
	if cc.routeQBlockResponse(m) {
		return
	}
	if cc.startQBlockNotification(m) {
		return
	}
	if probeSZX, ok := cc.qDiscoveryRequestSZX(m); ok && cc.qBlockAllowed() {
		cc.handleQDiscovery(w, probeSZX)
		return
	}
	if cc.handleBlockwise(w, m, cc.handleResponse) {
		return
	}
	cc.handleResponse(w, m)
}

func (cc *Conn) handleQDiscovery(w *responsewriter.ResponseWriter[*Conn], probeSZX blockwise.SZX) {
	const discovery = "</>;rt=\"core\""
	if err := w.SetResponse(codes.Content, message.AppLinkFormat, bytes.NewReader([]byte(discovery))); err != nil {
		cc.errors(fmt.Errorf("cannot set Q-Block discovery response: %w", err))
		return
	}
	option, err := blockwise.EncodeQBlockOption(probeSZX, 0, false)
	if err != nil {
		cc.errors(fmt.Errorf("cannot encode Q-Block discovery response: %w", err))
		return
	}
	w.Message().SetOptionUint32(message.QBlock2, option)
	w.Message().SetOptionUint32(message.Size2, uint32(len(discovery)))
	etag := sha256.Sum256([]byte(discovery))
	w.Message().SetOptionBytes(message.ETag, etag[:8])
}

func (cc *Conn) handleResponse(rw *responsewriter.ResponseWriter[*Conn], rm *pool.Message) {
	if cc.oscoreOuterBlockwise && cc.oscoreProcessor != nil &&
		(rm.HasOption(message.OSCORE) || cc.hasOSCOREExchange(rm.Token())) {
		if err := cc.unprotectIncomingResponse(rm); err != nil {
			cc.errors(fmt.Errorf("cannot unprotect reassembled OSCORE response: %w", err))
			return
		}
	}
	if len(rm.Token()) == 0 && rm.Code() >= codes.Created {
		if h, ok := cc.tokenlessHandler.Load(true); ok {
			h(rw, rm)
			return
		}
	}
	if h, ok := cc.tokenHandlerContainer.LoadAndDelete(rm.Token().Hash()); ok {
		h(rw, rm)
		return
	}
	cc.observationHandler.Handle(rw, rm)
}

func (cc *Conn) handleBlockwise(w *responsewriter.ResponseWriter[*Conn], m *pool.Message, next HandlerFunc) bool {
	if cc.blockWise == nil {
		return false
	}
	maxSZX, err := cc.messageBlockwiseSZX(m)
	if err != nil {
		cc.errors(fmt.Errorf("cannot size Q-Block response: %w", err))
		if responseErr := w.SetResponse(codes.RequestEntityTooLarge, message.TextPlain, nil); responseErr != nil {
			cc.errors(responseErr)
		}
		return true
	}
	next = cc.outerBlockwiseRequestHandler(m, next)
	cc.blockWise.Handle(w, m, maxSZX, cc.session.MaxMessageSize(), cc.RemoteAddr(), next)
	return true
}

func (cc *Conn) messageBlockwiseSZX(m *pool.Message) (blockwise.SZX, error) {
	maxSZX := cc.blockwiseSZX
	if !m.HasOption(message.QBlock1) && !m.HasOption(message.QBlock2) {
		return maxSZX, nil
	}
	responseSZX, err := cc.qBlockResponseSZX()
	if err != nil {
		return 0, err
	}
	return min(maxSZX, responseSZX), nil
}

func (cc *Conn) outerBlockwiseRequestHandler(m *pool.Message, next HandlerFunc) HandlerFunc {
	if !cc.oscoreOuterBlockwise || !cc.isOSCORERequestMessage(m) {
		return next
	}
	if cc.oscoreProcessor == nil {
		if !cc.oscoreProxy {
			return next
		}
		return func(rw *responsewriter.ResponseWriter[*Conn], rm *pool.Message) {
			next(rw, rm)
			if !rw.Message().HasOption(message.OSCORE) {
				return
			}
			if err := cc.checkOuterBlockwisePayloadSize(rw.Message()); err != nil {
				cc.errors(err)
				clearResponse(rw.Message())
			}
		}
	}
	return func(rw *responsewriter.ResponseWriter[*Conn], rm *pool.Message) {
		if !hasOSCORERequestContext(rm) {
			processed, err := cc.processReassembledOuterOSCORERequest(rw, rm, m.MessageID())
			if err != nil {
				cc.errors(err)
				return
			}
			if !processed {
				return
			}
			rw.Message().ResetOptionsTo(rm.Options())
			m.SetContext(rm.Context())
		}
		next(rw, rm)
		if !hasOSCORERequestContext(rm) {
			return
		}
		oversized, err := cc.oscoreResponseExceedsOuterBlockwiseLimit(rm, rw.Message())
		if err != nil {
			cc.errors(fmt.Errorf("cannot size OSCORE response: %w", err))
			clearResponse(rw.Message())
			return
		}
		if oversized {
			cc.errors(fmt.Errorf("%w: protected OSCORE response exceeds configured Outer Blockwise limit",
				blockwise.ErrRequestEntityTooLarge))
			clearResponse(rw.Message())
			rw.Message().SetCode(codes.RequestEntityTooLarge)
		}
		if err := cc.protectResponseForRequest(rm, rw.Message()); err != nil {
			cc.errors(fmt.Errorf("cannot protect response to reassembled OSCORE request: %w", err))
			clearResponse(rw.Message())
			return
		}
		if err := cc.checkOuterBlockwisePayloadSize(rw.Message()); err != nil {
			cc.errors(err)
			clearResponse(rw.Message())
		}
		m.SetContext(rm.Context())
	}
}

func (cc *Conn) processReassembledOuterOSCORERequest(
	rw *responsewriter.ResponseWriter[*Conn], rm *pool.Message, messageID int32,
) (bool, error) {
	rm.SetMessageID(messageID)
	datagram, err := rm.MarshalWithEncoder(coder.DefaultCoder)
	if err != nil {
		return false, fmt.Errorf("cannot marshal reassembled OSCORE request: %w", err)
	}
	drop, err := cc.processIncomingOSCORERequest(rm, datagram)
	if err != nil {
		return false, fmt.Errorf("cannot process reassembled OSCORE request: %w", err)
	}
	if drop {
		return false, nil
	}
	rw.Message().ResetOptionsTo(rm.Options())
	return true, nil
}

func clearResponse(response *pool.Message) {
	response.SetCode(codes.Empty)
	response.ResetOptionsTo(nil)
	response.SetBody(nil)
	response.SetModified(false)
}

func (cc *Conn) qBlockAllowed() bool {
	return cc.qConfigErr == nil && cc.blockWise != nil && cc.qBlockwiseMode != blockwise.QBlockwiseDisabled
}

func (cc *Conn) qDiscoveryRequestSZX(r *pool.Message) (blockwise.SZX, bool) {
	if r.Code() != codes.GET || r.HasOption(message.QBlock1) || r.HasOption(message.Block1) || r.HasOption(message.Block2) {
		return 0, false
	}
	path := make([]string, 0, 2)
	for _, option := range r.Options() {
		if option.ID == message.URIPath {
			path = append(path, string(option.Value))
		}
	}
	if len(path) != 2 || path[0] != ".well-known" || path[1] != "core" {
		return 0, false
	}
	value, err := r.GetOptionUint32(message.QBlock2)
	if err != nil || qOptionCount(r, message.QBlock2) != 1 {
		return 0, false
	}
	szx, num, _, err := blockwise.DecodeQBlockOption(value)
	return szx, err == nil && szx <= blockwise.SZX1024 && num == 0
}

// Sequence acquires sequence number.
func (cc *Conn) Sequence() uint64 {
	return cc.sequence.Add(1)
}

// getResponseFromCache gets a message from the response message cache.
func (cc *Conn) getResponseFromCache(mid int32, resp *pool.Message) (bool, error) {
	return cc.responseMsgCache.Load(strconv.Itoa(int(mid)), resp)
}

// addResponseToCache adds a message to the response message cache under the request's message ID.
func (cc *Conn) addResponseToCache(reqMessageID int32, resp *pool.Message) error {
	return cc.responseMsgCache.Store(strconv.Itoa(int(reqMessageID)), resp)
}

// checkMyMessageID compare client msgID against peer messageID and if it is near < 0xffff/4 then increase msgID.
// When msgIDs met it can cause issue because cache can send message to which doesn't bellows to request.
func (cc *Conn) checkMyMessageID(req *pool.Message) {
	if req.Type() == message.Confirmable {
		for {
			oldID := cc.msgID.Load()
			if pkgMath.CastTo[uint16](req.MessageID())-pkgMath.CastTo[uint16](cc.msgID.Load()) >= 0xffff/4 {
				return
			}
			newID := oldID + 0xffff/2
			if cc.msgID.CompareAndSwap(oldID, newID) {
				break
			}
		}
	}
}

func (cc *Conn) checkResponseCache(req *pool.Message, w *responsewriter.ResponseWriter[*Conn]) (bool, error) {
	if req.Type() == message.Confirmable || req.Type() == message.NonConfirmable {
		if ok, err := cc.getResponseFromCache(req.MessageID(), w.Message()); ok {
			w.Message().SetMessageID(req.MessageID())
			w.Message().SetType(message.NonConfirmable)
			if req.Type() == message.Confirmable {
				// req could be changed from NonConfirmation to confirmation message.
				w.Message().SetType(message.Acknowledgement)
			}
			return true, nil
		} else if err != nil {
			return false, fmt.Errorf("cannot unmarshal response from cache: %w", err)
		}
	}
	return false, nil
}

func isPongOrResetResponse(w *responsewriter.ResponseWriter[*Conn]) bool {
	return w.Message().IsModified() && (w.Message().Type() == message.Reset || w.Message().Code() == codes.Empty)
}

func sendJustAcknowledgeMessage(reqType message.Type, w *responsewriter.ResponseWriter[*Conn]) bool {
	return reqType == message.Confirmable && !w.Message().IsModified()
}

func (cc *Conn) processResponse(reqType message.Type, reqMessageID int32, qResponse bool,
	cacheResponse bool, w *responsewriter.ResponseWriter[*Conn],
) error {
	switch {
	case isPongOrResetResponse(w):
		if reqType == message.Confirmable {
			w.Message().SetType(message.Acknowledgement)
			w.Message().SetMessageID(reqMessageID)
		} else {
			if w.Message().Type() != message.Reset {
				w.Message().SetType(message.NonConfirmable)
			}
			w.Message().SetMessageID(cc.GetMessageID())
		}
		return nil
	case sendJustAcknowledgeMessage(reqType, w):
		// send message to separate(confirm received) message, if response is not modified
		w.Message().SetCode(codes.Empty)
		w.Message().SetType(message.Acknowledgement)
		w.Message().SetMessageID(reqMessageID)
		w.Message().SetToken(nil)
		// Empty ACKs are unprotected and must be cached so duplicate protected
		// CON requests can be acknowledged while a separate response is pending.
		return cc.cacheResponseIfRequested(true, reqMessageID, "cannot cache response", w.Message())
	case isQNonResponse(reqType, qResponse, w.Message()):
		w.Message().SetType(message.NonConfirmable)
		w.Message().SetMessageID(cc.GetMessageID())
		return cc.cacheResponseIfRequested(cacheResponse, reqMessageID, "cannot cache Q-Block response", w.Message())
	case !w.Message().IsModified():
		// don't send response
		return nil
	}

	// send piggybacked response
	w.Message().SetType(message.Confirmable)
	w.Message().SetMessageID(cc.GetMessageID())
	if reqType == message.Confirmable {
		w.Message().SetType(message.Acknowledgement)
		w.Message().SetMessageID(reqMessageID)
	}
	if reqType == message.Confirmable || reqType == message.NonConfirmable {
		return cc.cacheResponseIfRequested(cacheResponse, reqMessageID, "cannot cache response", w.Message())
	}
	return nil
}

func (cc *Conn) cacheResponseIfRequested(cacheResponse bool, reqMessageID int32, errorMessage string, response *pool.Message) error {
	if !cacheResponse {
		return nil
	}
	if err := cc.addResponseToCache(reqMessageID, response); err != nil {
		return fmt.Errorf("%s: %w", errorMessage, err)
	}
	return nil
}

func shouldCacheResponse(reqType message.Type, response *pool.Message) bool {
	if reqType != message.Confirmable && reqType != message.NonConfirmable {
		return false
	}
	if response.Code() == codes.Empty {
		return response.Type() == message.Acknowledgement
	}
	return response.Code() >= codes.Created && response.Code() < 192 &&
		response.Type() != message.Reset
}

func hasOSCORERequestContext(req *pool.Message) bool {
	if req.Context() == nil {
		return false
	}
	_, ok := req.Context().Value(udpOSCORERequestContextKey{}).(udpOSCORERequestContext)
	return ok
}

func isQNonResponse(reqType message.Type, qResponse bool, response *pool.Message) bool {
	return reqType == message.NonConfirmable && response.IsModified() &&
		(qResponse || response.HasOption(message.QBlock1) || response.HasOption(message.QBlock2))
}

func (cc *Conn) handleReq(w *responsewriter.ResponseWriter[*Conn], req *pool.Message) {
	defer cc.inactivityMonitor.Notify()
	reqMid := req.MessageID()

	// The same message ID can not be handled concurrently
	// for deduplication to work
	l := cc.msgIDMutex.Lock(reqMid)
	defer l.Unlock()

	if ok, err := cc.checkResponseCache(req, w); err != nil {
		cc.closeConnection()
		cc.errors(fmt.Errorf(errFmtWriteResponse, err))
		return
	} else if ok {
		return
	}

	w.Message().SetModified(false)
	reqType := req.Type()
	reqMessageID := req.MessageID()
	qResponse := req.HasOption(message.QBlock1) || req.HasOption(message.QBlock2)
	cacheResponse := !hasOSCORERequestContext(req)
	cc.handle(w, req)

	err := cc.processResponse(reqType, reqMessageID, qResponse, cacheResponse, w)
	if err != nil {
		cc.closeConnection()
		cc.errors(fmt.Errorf(errFmtWriteResponse, err))
	}
}

func (cc *Conn) closeConnection() {
	if errC := cc.Close(); errC != nil {
		cc.errors(fmt.Errorf("cannot close connection: %w", errC))
	}
}

func (cc *Conn) ProcessReceivedMessageWithHandler(req *pool.Message, handler config.HandlerFunc[*Conn]) {
	generation := req.Generation()
	reqType, reqMessageID := req.Type(), req.MessageID()
	qResponse := req.HasOption(message.QBlock1) || req.HasOption(message.QBlock2)
	defer func() {
		cc.messagePool.ReleaseMessageIfNotHijacked(req, generation)
	}()
	ctx, afterWrite := responsewriter.WithPostWriteCallbacks(req.Context())
	req.SetContext(ctx)
	resp := cc.AcquireMessage(cc.Context())
	resp.SetToken(req.Token())
	cc.setControlInformation(req.ControlMessage())

	w := responsewriter.New(resp, cc, req.Options()...)
	defer func() {
		cc.ReleaseMessage(w.Message())
	}()
	handler(w, req)
	select {
	case <-cc.Context().Done():
		return
	default:
	}
	if !w.Message().IsModified() {
		// nothing to send
		return
	}
	if qResponse {
		if err := cc.validateQBlockDatagram(w.Message()); err != nil {
			cc.errors(err)
			w.Message().SetCode(codes.RequestEntityTooLarge)
			w.Message().ResetOptionsTo(nil)
			w.Message().SetBody(nil)
		}
	}
	if req.Code() >= codes.GET && req.Code() <= codes.IPATCH {
		if err := cc.protectResponseForRequest(req, w.Message()); err != nil {
			cc.closeConnection()
			cc.errors(fmt.Errorf(errFmtWriteResponse, err))
			return
		}
	}
	if hasOSCORERequestContext(req) && shouldCacheResponse(reqType, w.Message()) {
		if err := cc.addResponseToCache(reqMessageID, w.Message()); err != nil {
			cc.closeConnection()
			cc.errors(fmt.Errorf("cannot cache protected response: %w", err))
			return
		}
	}
	cc.upsertControlInformation(w.Message())
	errW := cc.writeMessageAsync(w.Message())
	if errW != nil {
		cc.closeConnection()
		cc.errors(fmt.Errorf(errFmtWriteResponse, errW))
		return
	}
	afterWrite()
}

func (cc *Conn) handlePong(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
	cc.sendPong(w, r)
}

func (cc *Conn) setControlInformation(cm *coapNet.ControlMessage) {
	if cm == nil {
		cc.interfaceIndex.Store(0)
		cc.localAddr.Store(nil)
		return
	}

	cc.interfaceIndex.Store(int64(cm.GetIfIndex()))
	if len(cm.Dst) == 0 || cm.Dst.IsMulticast() {
		cc.localAddr.Store(nil)
		return
	}

	dst := make(net.IP, len(cm.Dst))
	copy(dst, cm.Dst)
	cc.localAddr.Store(&dst)
}

func (cc *Conn) upsertControlInformation(msg *pool.Message) {
	ifIndex := int(cc.interfaceIndex.Load())
	localAddrPtr := cc.localAddr.Load()
	if ifIndex < 1 && localAddrPtr == nil {
		return
	}

	var localAddr net.IP
	if localAddrPtr != nil {
		localAddr = *localAddrPtr
	}

	msg.UpsertControlMessage(&coapNet.ControlMessage{IfIndex: ifIndex, Src: localAddr})
}

func (cc *Conn) handleSpecialMessages(r *pool.Message) bool {
	cc.handleQNotificationAcknowledgement(r)
	// ping request
	if r.IsPing(false) {
		cc.ProcessReceivedMessageWithHandler(r, cc.handlePong)
		return true
	}

	// if waits for concrete message handler
	if elem, ok := cc.midHandlerContainer.LoadAndDelete(r.MessageID()); ok {
		elem.ReleaseMessage(cc)
		resp := cc.AcquireMessage(cc.Context())
		resp.SetToken(r.Token())
		w := responsewriter.New(resp, cc, r.Options()...)
		defer func() {
			cc.ReleaseMessage(w.Message())
		}()
		elem.handler(w, r)
		// we just confirmed that message was processed for cc.writeMessage
		// the body of the message is need to be processed by the loopOverReceivedMessageQueue goroutine
		return false
	}
	// separate message
	if r.IsSeparateMessage() {
		// msg was processed by token handler - just drop it.
		return true
	}
	return false
}

func newMalformedMessageError(err error, datagram []byte, req *pool.Message) *MalformedMessageError {
	malformedErr := &MalformedMessageError{Err: err}
	if len(datagram) >= 1 && datagram[0]>>6 != 1 {
		malformedErr.InvalidVersion = true
		return malformedErr
	}
	if len(datagram) >= 4 {
		malformedErr.HasHeader = true
		malformedErr.Type = req.Type()
		malformedErr.MessageID = req.MessageID()
	}
	return malformedErr
}

func (cc *Conn) processUnknownCriticalOptions(req *pool.Message) (bool, error) {
	if (req.Type() != message.Confirmable && req.Type() != message.NonConfirmable) || req.Code() < codes.GET || req.Code() >= 32 {
		return false, nil
	}
	qAllowed := cc.qBlockAllowed() && qBlockCanUseUnknownCriticalOption(req)
	unknownCriticalOpts := client.GetUnknownCriticalOptionsWithQBlock(req.Options(), qAllowed)
	if (cc.oscoreProxy || cc.oscoreOuterBlockwise) && req.HasOption(message.OSCORE) {
		filtered := unknownCriticalOpts[:0]
		for _, optionID := range unknownCriticalOpts {
			if optionID != message.OSCORE {
				filtered = append(filtered, optionID)
			}
		}
		unknownCriticalOpts = filtered
	}
	if len(unknownCriticalOpts) == 0 {
		return false, nil
	}
	var err error
	if req.Type() == message.Confirmable {
		// RFC 7252 section 5.4.1: a NON message with an unrecognized critical
		// option must be rejected (section 4.3), so it is dropped without a reply.
		err = cc.respondUnknownCriticalOptions(req, unknownCriticalOpts)
	}
	cc.ReleaseMessage(req)
	return true, err
}

func qBlockCanUseUnknownCriticalOption(req *pool.Message) bool {
	if req.HasOption(message.ProxyURI) || req.HasOption(message.ProxyScheme) ||
		req.HasOption(message.Block1) || req.HasOption(message.Block2) {
		return false
	}
	if !req.HasOption(message.Observe) {
		return true
	}
	if req.Code() != codes.GET && req.Code() != codes.FETCH {
		return false
	}
	return req.Code() != codes.GET || !req.HasOption(message.QBlock1)
}

func (cc *Conn) Process(cm *coapNet.ControlMessage, datagram []byte) error {
	if cc.oscoreOuterBlockwiseErr != nil {
		return cc.oscoreOuterBlockwiseErr
	}
	if pkgMath.CastTo[uint32](len(datagram)) > cc.session.MaxMessageSize() {
		return fmt.Errorf("max message size(%v) was exceeded %v", cc.session.MaxMessageSize(), len(datagram))
	}
	req := cc.AcquireMessage(cc.Context())
	_, err := req.UnmarshalWithDecoder(coder.DefaultCoder, datagram)
	if err != nil {
		malformedErr := newMalformedMessageError(err, datagram, req)
		cc.ReleaseMessage(req)
		return malformedErr
	}
	req.SetControlMessage(cm)
	req.SetSequence(cc.Sequence())
	if isQMulticastRequest(req, cm) {
		if cc.oscoreProcessor != nil {
			cc.ReleaseMessage(req)
			return oscore.ErrUnsupportedOption
		}
		cc.ReleaseMessage(req)
		return nil
	}
	if cc.processIncomingOSCOREMessage(req, datagram) {
		return nil
	}
	if handled, processErr := cc.processUnknownCriticalOptions(req); handled {
		return processErr
	}
	cc.checkMyMessageID(req)
	drop, monitorErr := cc.requestMonitor(cc, req)
	if monitorErr != nil {
		cc.ReleaseMessage(req)
		return fmt.Errorf("request monitor: %w", monitorErr)
	}
	if drop {
		cc.ReleaseMessage(req)
		return nil
	}
	return cc.enqueueIncomingRequest(req)
}

func (cc *Conn) enqueueIncomingRequest(req *pool.Message) error {
	if hasQBlock(req) {
		cc.qBlockPeerProgress()
	}
	cc.inactivityMonitor.Notify()
	if cc.handleSpecialMessages(req) {
		return nil
	}
	if req.Type() == message.Confirmable || req.Type() == message.NonConfirmable {
		// Retransmissions received before the response is sent need no queued reply.
		duplicate, err := cc.addPendingMessage(req.MessageID(), req.Sequence(), time.Now())
		if err != nil {
			cc.ReleaseMessage(req)
			cc.errors(err)
			return nil
		}
		if duplicate {
			cc.ReleaseMessage(req)
			return nil
		}
	}
	reqMessageID, sequence := req.MessageID(), req.Sequence()
	select {
	case cc.receivedMessageReader.C() <- req:
	case <-cc.Context().Done():
		cc.deletePendingMessage(reqMessageID, sequence)
		cc.ReleaseMessage(req)
	}
	return nil
}

func (cc *Conn) processIncomingOSCOREMessage(req *pool.Message, datagram []byte) bool {
	if cc.oscoreProcessor == nil {
		return false
	}
	if cc.oscoreOuterBlockwise && (req.HasOption(message.Block1) || req.HasOption(message.Block2)) {
		cc.normalizeOuterBlockwiseCode(req)
		return false
	}
	if cc.isOSCORERequestMessage(req) {
		return cc.processIncomingOSCORERequestMessage(req, datagram)
	}
	if req.Code() >= codes.Created && req.Code() < 192 ||
		req.HasOption(message.OSCORE) || cc.hasOSCOREExchange(req.Token()) {
		return cc.processIncomingOSCOREResponse(req, datagram)
	}
	return false
}

func (cc *Conn) isOSCORERequestMessage(req *pool.Message) bool {
	if req.HasOption(message.OSCORE) {
		start, end, err := req.Options().Find(message.OSCORE)
		if err != nil || end-start != 1 {
			return req.Code() >= codes.GET && req.Code() <= codes.IPATCH
		}
		option, err := oscore.UnmarshalOption(req.Options()[start].Value)
		if err == nil {
			return option.SenderIDSet
		}
		return req.Code() >= codes.GET && req.Code() <= codes.IPATCH
	}
	if cc.hasOSCOREExchange(req.Token()) {
		return false
	}
	return req.Code() >= codes.GET && req.Code() <= codes.IPATCH
}

func (cc *Conn) normalizeOuterBlockwiseCode(req *pool.Message) {
	if req.HasOption(message.OSCORE) {
		start, end, err := req.Options().Find(message.OSCORE)
		if err != nil || end-start != 1 {
			return
		}
		option, err := oscore.UnmarshalOption(req.Options()[start].Value)
		if err != nil {
			return
		}
		if option.SenderIDSet && req.Code() >= codes.Created && req.Code() < 192 {
			req.SetCode(codes.POST)
		} else if !option.SenderIDSet && req.Code() >= codes.GET && req.Code() <= codes.IPATCH {
			req.SetCode(codes.Content)
		}
		return
	}
	if cc.hasOSCOREExchange(req.Token()) && req.Code() >= codes.GET && req.Code() <= codes.IPATCH {
		req.SetCode(codes.Content)
	}
}

func (cc *Conn) processIncomingOSCOREResponse(req *pool.Message, datagram []byte) bool {
	protectedConfirmable := req.Type() == message.Confirmable && req.HasOption(message.OSCORE)
	if protectedConfirmable {
		handled, err := cc.processIncomingOSCOREResponseRetransmission(req, datagram)
		if err != nil {
			cc.ReleaseMessage(req)
			cc.errors(fmt.Errorf("cannot acknowledge duplicate OSCORE response: %w", err))
			return true
		}
		if handled {
			cc.ReleaseMessage(req)
			return true
		}
	}
	if err := cc.unprotectIncomingResponse(req); err != nil {
		cc.ReleaseMessage(req)
		cc.errors(fmt.Errorf("cannot unprotect OSCORE response: %w", err))
		return true
	}
	if protectedConfirmable {
		cc.recordIncomingOSCOREResponse(req.MessageID(), datagram)
	}
	return false
}

func (cc *Conn) processIncomingOSCORERequestMessage(req *pool.Message, datagram []byte) bool {
	drop, err := cc.processIncomingOSCORERequest(req, datagram)
	if err != nil {
		cc.ReleaseMessage(req)
		cc.errors(fmt.Errorf("cannot process incoming OSCORE request: %w", err))
		return true
	}
	if drop || req.Code() == codes.Empty {
		cc.ReleaseMessage(req)
		return true
	}
	return false
}

func (cc *Conn) processIncomingOSCOREResponseRetransmission(req *pool.Message, datagram []byte) (bool, error) {
	digest := sha256.Sum256(datagram)
	cc.oscoreMu.Lock()
	cached, exists := cc.oscoreResponses[req.MessageID()]
	if exists && time.Now().After(cached.expires) {
		delete(cc.oscoreResponses, req.MessageID())
		exists = false
	}
	cc.oscoreMu.Unlock()
	if !exists {
		return false, nil
	}
	if cached.digest != digest {
		ackErr := cc.retransmitCachedOSCOREAcknowledgment(req)
		protocolErr := fmt.Errorf("%w: Message ID reused for different protected response", oscore.ErrInvalidOSCOREMessage)
		return true, errors.Join(protocolErr, ackErr)
	}
	return true, cc.retransmitCachedOSCOREAcknowledgment(req)
}

func (cc *Conn) retransmitCachedOSCOREAcknowledgment(req *pool.Message) error {
	resp := cc.AcquireMessage(req.Context())
	defer cc.ReleaseMessage(resp)
	ok, err := cc.getResponseFromCache(req.MessageID(), resp)
	if err != nil {
		return fmt.Errorf("load cached acknowledgment: %w", err)
	}
	if !ok || resp.Code() != codes.Empty || resp.Type() != message.Acknowledgement {
		return nil
	}
	resp.SetMessageID(req.MessageID())
	resp.SetControlMessage(responseControlMessage(req.ControlMessage()))
	return cc.session.WriteMessage(resp)
}

func responseControlMessage(request *coapNet.ControlMessage) *coapNet.ControlMessage {
	if request == nil {
		return nil
	}
	response := &coapNet.ControlMessage{IfIndex: request.GetIfIndex()}
	if len(request.Dst) > 0 && !request.Dst.IsMulticast() {
		response.Src = append(net.IP(nil), request.Dst...)
	}
	return response
}

func (cc *Conn) recordIncomingOSCOREResponse(mid int32, datagram []byte) {
	cc.oscoreMu.Lock()
	if cc.oscoreResponses == nil {
		cc.oscoreResponses = make(map[int32]udpOSCOREResponse)
	}
	cc.oscoreResponses[mid] = udpOSCOREResponse{
		digest:  sha256.Sum256(datagram),
		expires: time.Now().Add(ExchangeLifetime),
	}
	cc.oscoreMu.Unlock()
}

func isQMulticastRequest(req *pool.Message, cm *coapNet.ControlMessage) bool {
	return req.Code() >= codes.GET && req.Code() < 32 && req.HasOption(message.QBlock2) && cm != nil && cm.Dst.IsMulticast()
}

func (cc *Conn) respondUnknownCriticalOptions(req *pool.Message, unknownCriticalOpts []message.OptionID) error {
	resp := cc.AcquireMessage(req.Context())
	defer cc.ReleaseMessage(resp)

	resp.SetCode(codes.BadOption)
	resp.SetToken(req.Token())
	resp.SetType(message.Acknowledgement)
	resp.SetMessageID(req.MessageID())
	resp.SetContentFormat(message.TextPlain)
	resp.SetBody(bytes.NewReader([]byte(client.FormatUnknownCriticalOptionsDiagnostic(unknownCriticalOpts))))

	cc.setControlInformation(req.ControlMessage())
	cc.upsertControlInformation(resp)

	if err := cc.session.WriteMessage(resp); err != nil {
		return fmt.Errorf(errFmtWriteResponse, err)
	}
	return nil
}

// SetContextValue stores the value associated with key to context of connection.
func (cc *Conn) SetContextValue(key interface{}, val interface{}) {
	cc.session.SetContextValue(key, val)
}

// Done signalizes that connection is not more processed.
func (cc *Conn) Done() <-chan struct{} {
	return cc.session.Done()
}

func (cc *Conn) checkMidHandlerContainer(now time.Time, maxRetransmit uint32, key int32, value *midElement) {
	if value.IsExpired(now, maxRetransmit) {
		current, loaded := cc.midHandlerContainer.LoadAndDelete(key)
		if !loaded || current != value {
			return
		}
		value.ReleaseMessage(cc)
		expired := fmt.Errorf(errFmtWriteRequest, context.DeadlineExceeded)
		notifyWaitForAcknowledge(value.waitForAcknowledge, expired)
		cc.errors(expired)
		return
	}
	if !value.Retransmit(now) {
		return
	}
	msg, ok, err := value.GetMessage(cc)
	if err != nil {
		cc.midHandlerContainer.Delete(key)
		value.ReleaseMessage(cc)
		cc.errors(fmt.Errorf(errFmtWriteRequest, err))
		return
	}
	if !ok {
		value.MarkRetransmitted(time.Now())
		return
	}
	defer cc.ReleaseMessage(msg)
	err = cc.session.WriteMessage(msg)
	if err == nil {
		value.MarkRetransmitted(time.Now())
		return
	}
	if value.MarkRetransmissionFailed(maxRetransmit) {
		removed := false
		cc.midHandlerContainer.ReplaceWithFunc(key, func(current *midElement, loaded bool) (*midElement, bool) {
			if loaded && current == value {
				removed = true
				return current, true
			}
			return current, !loaded
		})
		if removed {
			notifyWaitForAcknowledge(value.waitForAcknowledge, err)
			value.ReleaseMessage(cc)
		}
	}
	cc.errors(fmt.Errorf(errFmtWriteRequest, err))
}

// CheckExpirations checks and remove expired items from caches.
func (cc *Conn) CheckExpirations(now time.Time) {
	cc.pendingMessagesMutex.Lock()
	cc.expirePendingMessages(now)
	cc.pendingMessagesMutex.Unlock()
	cc.expireOSCORERequests(now)
	cc.inactivityMonitor.CheckInactivity(now, cc)
	cc.responseMsgCache.CheckExpirations(now)
	if cc.blockWise != nil {
		cc.blockWise.CheckExpirations(now)
	}
	maxRetransmit := cc.transmission.maxRetransmit.Load()
	x := struct {
		now           time.Time
		maxRetransmit uint32
		cc            *Conn
	}{
		now:           now,
		maxRetransmit: maxRetransmit,
		cc:            cc,
	}
	cc.midHandlerContainer.Range(func(key int32, value *midElement) bool {
		x.cc.checkMidHandlerContainer(x.now, x.maxRetransmit, key, value)
		return true
	})
}

func (cc *Conn) AcquireMessage(ctx context.Context) *pool.Message {
	return cc.messagePool.AcquireMessage(ctx)
}

// ReleaseMessage returns m to the message pool. Owners must release hijacked
// messages when they are no longer needed; this does not affect deduplication.
func (cc *Conn) ReleaseMessage(m *pool.Message) {
	cc.messagePool.ReleaseMessage(m)
}

func (cc *Conn) addPendingMessage(mid int32, sequence uint64, now time.Time) (bool, error) {
	if cc.maxPendingMessages == 0 {
		return false, nil
	}
	cc.pendingMessagesMutex.Lock()
	defer cc.pendingMessagesMutex.Unlock()
	if pending, ok := cc.pendingMessages[mid]; ok {
		if now.Before(pending.expires) {
			return true, nil
		}
		delete(cc.pendingMessages, mid)
	}
	if uint64(len(cc.pendingMessages)) >= uint64(cc.maxPendingMessages) {
		// Reclaim capacity without scanning on every admission.
		cc.expirePendingMessages(now)
		if uint64(len(cc.pendingMessages)) >= uint64(cc.maxPendingMessages) {
			return false, fmt.Errorf("in-flight message limit (%d) exceeded", cc.maxPendingMessages)
		}
	}
	if cc.pendingMessages == nil {
		cc.pendingMessages = make(map[int32]pendingMessage)
	}
	cc.pendingMessages[mid] = pendingMessage{sequence: sequence, expires: now.Add(ExchangeLifetime)}
	return false, nil
}

// expirePendingMessages requires pendingMessagesMutex to be held.
func (cc *Conn) expirePendingMessages(now time.Time) {
	for mid, pending := range cc.pendingMessages {
		if !now.Before(pending.expires) {
			delete(cc.pendingMessages, mid)
		}
	}
}

func (cc *Conn) deletePendingMessage(mid int32, sequence uint64) {
	cc.pendingMessagesMutex.Lock()
	defer cc.pendingMessagesMutex.Unlock()
	if pending, ok := cc.pendingMessages[mid]; ok && pending.sequence == sequence {
		delete(cc.pendingMessages, mid)
	}
}

// WriteMulticastMessage sends multicast to the remote multicast address.
// By default it is sent over all network interfaces and all compatible source IP addresses with hop limit 1.
// Via opts you can specify the network interface, source IP address, and hop limit.
func (cc *Conn) WriteMulticastMessage(req *pool.Message, address *net.UDPAddr, options ...coapNet.MulticastOption) error {
	if cc.oscoreProcessor != nil {
		return oscore.ErrUnsupportedOption
	}
	if req.Type() == message.Confirmable {
		return errors.New("multicast messages cannot be confirmable")
	}
	req.UpsertMessageID(cc.GetMessageID())

	err := cc.session.WriteMulticastMessage(req, address, options...)
	if err != nil {
		return fmt.Errorf(errFmtWriteRequest, err)
	}
	return nil
}

func (cc *Conn) InactivityMonitor() InactivityMonitor {
	return cc.inactivityMonitor
}

// NetConn returns the underlying connection that is wrapped by cc. The Conn returned is shared by all invocations of NetConn, so do not modify it.
func (cc *Conn) NetConn() net.Conn {
	return cc.session.NetConn()
}
