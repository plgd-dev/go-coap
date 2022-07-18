package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"time"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	"github.com/plgd-dev/go-coap/v2/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v2/net"
	"github.com/plgd-dev/go-coap/v2/net/blockwise"
	"github.com/plgd-dev/go-coap/v2/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v2/net/responsewriter"
	"github.com/plgd-dev/go-coap/v2/pkg/cache"
	coapErrors "github.com/plgd-dev/go-coap/v2/pkg/errors"
	"github.com/plgd-dev/go-coap/v2/pkg/sync"
	"github.com/plgd-dev/go-coap/v2/udp/coder"
	atomicTypes "go.uber.org/atomic"
)

// https://datatracker.ietf.org/doc/html/rfc7252#section-4.8.2
const ExchangeLifetime = 247 * time.Second

type (
	HandlerFunc = func(*responsewriter.ResponseWriter[*ClientConn], *pool.Message)
	ErrorFunc   = func(error)
	GoPoolFunc  = func(func()) error
	EventFunc   = func()
	GetMIDFunc  = func() int32
)

type Session interface {
	Context() context.Context
	Close() error
	MaxMessageSize() uint32
	RemoteAddr() net.Addr
	LocalAddr() net.Addr
	WriteMessage(req *pool.Message) error
	// WriteMulticast sends multicast to the remote multicast address.
	// By default it is sent over all network interfaces and all compatible source IP addresses with hop limit 1.
	// Via opts you can specify the network interface, source IP address, and hop limit.
	WriteMulticastMessage(req *pool.Message, address *net.UDPAddr, opts ...coapNet.MulticastOption) error
	Run(cc *ClientConn) error
	AddOnClose(f EventFunc)
	SetContextValue(key interface{}, val interface{})
	Done() <-chan struct{}
}

type RequestsMap = sync.Map[uint64, *pool.Message]

// ClientConn represents a virtual connection to a conceptual endpoint, to perform COAPs commands.
type ClientConn struct {
	// This field needs to be the first in the struct to ensure proper word alignment on 32-bit platforms.
	// See: https://golang.org/pkg/sync/atomic/#pkg-note-BUG
	sequence uint64

	session           Session
	inactivityMonitor inactivity.Monitor

	blockWise               *blockwise.BlockWise
	handler                 HandlerFunc
	observationTokenHandler *sync.Map[uint64, HandlerFunc]
	observationRequests     *RequestsMap
	transmission            *Transmission
	messagePool             *pool.Pool

	goPool           GoPoolFunc
	errors           ErrorFunc
	responseMsgCache *cache.Cache
	msgIDMutex       *MutexMap

	tokenHandlerContainer *sync.Map[uint64, HandlerFunc]
	midHandlerContainer   *sync.Map[int32, HandlerFunc]
	msgID                 uint32
	blockwiseSZX          blockwise.SZX
}

// Transmission is a threadsafe container for transmission related parameters
type Transmission struct {
	nStart             *atomicTypes.Duration
	acknowledgeTimeout *atomicTypes.Duration
	maxRetransmit      *atomicTypes.Int32
}

func (t *Transmission) SetTransmissionNStart(d time.Duration) {
	t.nStart.Store(d)
}

func (t *Transmission) SetTransmissionAcknowledgeTimeout(d time.Duration) {
	t.acknowledgeTimeout.Store(d)
}

func (t *Transmission) SetTransmissionMaxRetransmit(d int32) {
	t.maxRetransmit.Store(d)
}

func (cc *ClientConn) Transmission() *Transmission {
	return cc.transmission
}

// NewClientConn creates connection over session and observation.
func NewClientConn(
	session Session,
	observationTokenHandler *sync.Map[uint64, HandlerFunc],
	observationRequests *RequestsMap,
	transmissionNStart time.Duration,
	transmissionAcknowledgeTimeout time.Duration,
	transmissionMaxRetransmit uint32,
	handler HandlerFunc,
	blockwiseSZX blockwise.SZX,
	blockWise *blockwise.BlockWise,
	goPool GoPoolFunc,
	errors ErrorFunc,
	getMID GetMIDFunc,
	inactivityMonitor inactivity.Monitor,
	responseMsgCache *cache.Cache,
	messagePool *pool.Pool,
) *ClientConn {
	if errors == nil {
		errors = func(error) {
			// default no-op
		}
	}
	if getMID == nil {
		getMID = message.GetMID
	}

	return &ClientConn{
		msgID:                   uint32(getMID() - 0xffff/2),
		session:                 session,
		observationTokenHandler: observationTokenHandler,
		observationRequests:     observationRequests,
		transmission: &Transmission{
			atomicTypes.NewDuration(transmissionNStart),
			atomicTypes.NewDuration(transmissionAcknowledgeTimeout),
			atomicTypes.NewInt32(int32(transmissionMaxRetransmit)),
		},
		handler:      handler,
		blockwiseSZX: blockwiseSZX,
		blockWise:    blockWise,

		tokenHandlerContainer: sync.NewMap[uint64, HandlerFunc](),
		midHandlerContainer:   sync.NewMap[int32, HandlerFunc](),
		goPool:                goPool,
		errors:                errors,
		msgIDMutex:            NewMutexMap(),
		responseMsgCache:      responseMsgCache,
		inactivityMonitor:     inactivityMonitor,
		messagePool:           messagePool,
	}
}

func (cc *ClientConn) Session() Session {
	return cc.session
}

func (cc *ClientConn) getMID() int32 {
	return int32(uint16(atomic.AddUint32(&cc.msgID, 1)))
}

// Close closes connection without waiting for the end of the Run function.
func (cc *ClientConn) Close() error {
	err := cc.session.Close()
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (cc *ClientConn) do(req *pool.Message) (*pool.Message, error) {
	token := req.Token()
	if token == nil {
		return nil, fmt.Errorf("invalid token")
	}

	respChan := make(chan *pool.Message, 1)
	if _, loaded := cc.tokenHandlerContainer.LoadOrStore(token.Hash(), func(w *responsewriter.ResponseWriter[*ClientConn], r *pool.Message) {
		r.Hijack()
		select {
		case respChan <- r:
		default:
		}
	}); loaded {
		return nil, fmt.Errorf("cannot add token(%v) handler: %w", token, coapErrors.ErrKeyAlreadyExists)
	}
	defer func() {
		_, _ = cc.tokenHandlerContainer.PullOut(token.Hash())
	}()
	err := cc.writeMessage(req)
	if err != nil {
		return nil, fmt.Errorf("cannot write request: %w", err)
	}
	select {
	case <-req.Context().Done():
		return nil, req.Context().Err()
	case <-cc.session.Context().Done():
		return nil, fmt.Errorf("connection was closed: %w", cc.session.Context().Err())
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
func (cc *ClientConn) Do(req *pool.Message) (*pool.Message, error) {
	if cc.blockWise == nil {
		req.UpsertMessageID(cc.getMID())
		return cc.do(req)
	}
	bwresp, err := cc.blockWise.Do(req, cc.blockwiseSZX, cc.session.MaxMessageSize(), func(bwreq blockwise.Message) (blockwise.Message, error) {
		req := bwreq.(*pool.Message)
		if req.Options().HasOption(message.Block1) || req.Options().HasOption(message.Block2) {
			req.SetMessageID(cc.getMID())
		} else {
			req.UpsertMessageID(cc.getMID())
		}
		return cc.do(req)
	})
	if err != nil {
		return nil, err
	}
	return bwresp.(*pool.Message), nil
}

func (cc *ClientConn) writeMessage(req *pool.Message) error {
	respChan := make(chan struct{})

	// Only confirmable messages ever match an message ID
	if req.Type() == message.Confirmable {
		if _, loaded := cc.midHandlerContainer.LoadOrStore(req.MessageID(), func(w *responsewriter.ResponseWriter[*ClientConn], r *pool.Message) {
			close(respChan)
			if r.IsSeparateMessage() {
				// separate message - just accept
				return
			}
			cc.handleBW(w, r)
		}); loaded {
			return fmt.Errorf("cannot insert mid(%v) handler: %w", req.MessageID(), coapErrors.ErrKeyAlreadyExists)
		}
		defer func() {
			_, _ = cc.midHandlerContainer.PullOut(req.MessageID())
		}()
	}

	err := cc.session.WriteMessage(req)
	if err != nil {
		return fmt.Errorf("cannot write request: %w", err)
	}
	if req.Type() != message.Confirmable {
		// If the request is not confirmable, we do not need to wait for a response
		// and skip retransmissions
		close(respChan)
	}

	maxRetransmit := cc.transmission.maxRetransmit.Load()
	for i := int32(0); i < maxRetransmit; i++ {
		select {
		case <-respChan:
			return nil
		case <-req.Context().Done():
			return req.Context().Err()
		case <-cc.Context().Done():
			return fmt.Errorf("connection was closed: %w", cc.Context().Err())
		case <-time.After(cc.transmission.acknowledgeTimeout.Load()):
			select {
			case <-req.Context().Done():
				return req.Context().Err()
			case <-cc.session.Context().Done():
				return fmt.Errorf("connection was closed: %w", cc.Context().Err())
			case <-time.After(cc.transmission.nStart.Load()):
				err = cc.session.WriteMessage(req)
				if err != nil {
					return fmt.Errorf("cannot write request: %w", err)
				}
			}
		}
	}
	return fmt.Errorf("timeout: retransmission(%v) was exhausted", cc.transmission.maxRetransmit.Load())
}

// WriteMessage sends an coap message.
func (cc *ClientConn) WriteMessage(req *pool.Message) error {
	if cc.blockWise == nil {
		req.UpsertMessageID(cc.getMID())
		return cc.writeMessage(req)
	}
	return cc.blockWise.WriteMessage(cc.RemoteAddr(), req, cc.blockwiseSZX, cc.session.MaxMessageSize(), func(bwreq blockwise.Message) error {
		req := bwreq.(*pool.Message)
		if req.Options().HasOption(message.Block1) || req.Options().HasOption(message.Block2) {
			req.SetMessageID(cc.getMID())
			req.UpsertType(message.Confirmable)
		} else {
			req.UpsertMessageID(cc.getMID())
			req.UpsertType(message.Confirmable)
		}
		return cc.writeMessage(req)
	})
}

func newCommonRequest(ctx context.Context, messagePool *pool.Pool, code codes.Code, path string, opts ...message.Option) (*pool.Message, error) {
	token, err := message.GetToken()
	if err != nil {
		return nil, fmt.Errorf("cannot get token: %w", err)
	}
	req := messagePool.AcquireMessage(ctx)
	req.SetCode(code)
	req.SetToken(token)
	req.SetMessageID(message.RandMID())
	req.SetType(message.Confirmable)
	req.ResetOptionsTo(opts)
	if err := req.SetPath(path); err != nil {
		messagePool.ReleaseMessage(req)
		return nil, err
	}
	return req, nil
}

// NewGetRequest creates get request.
//
// Use ctx to set timeout.
func NewGetRequest(ctx context.Context, messagePool *pool.Pool, path string, opts ...message.Option) (*pool.Message, error) {
	return newCommonRequest(ctx, messagePool, codes.GET, path, opts...)
}

// Get issues a GET to the specified path.
//
// Use ctx to set timeout.
//
// An error is returned if by failure to speak COAP (such as a network connectivity problem).
// Any status code doesn't cause an error.
func (cc *ClientConn) Get(ctx context.Context, path string, opts ...message.Option) (*pool.Message, error) {
	req, err := NewGetRequest(ctx, cc.messagePool, path, opts...)
	if err != nil {
		return nil, fmt.Errorf("cannot create get request: %w", err)
	}
	req.SetMessageID(cc.getMID())
	defer cc.ReleaseMessage(req)
	return cc.Do(req)
}

// NewPostRequest creates post request.
//
// Use ctx to set timeout.
//
// An error is returned if by failure to speak COAP (such as a network connectivity problem).
// Any status code doesn't cause an error.
//
// If payload is nil then content format is not used.
func NewPostRequest(ctx context.Context, messagePool *pool.Pool, path string, contentFormat message.MediaType, payload io.ReadSeeker, opts ...message.Option) (*pool.Message, error) {
	req, err := newCommonRequest(ctx, messagePool, codes.POST, path, opts...)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.SetContentFormat(contentFormat)
		req.SetBody(payload)
	}
	return req, nil
}

// Post issues a POST to the specified path.
//
// Use ctx to set timeout.
//
// An error is returned if by failure to speak COAP (such as a network connectivity problem).
// Any status code doesn't cause an error.
//
// If payload is nil then content format is not used.
func (cc *ClientConn) Post(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, opts ...message.Option) (*pool.Message, error) {
	req, err := NewPostRequest(ctx, cc.messagePool, path, contentFormat, payload, opts...)
	if err != nil {
		return nil, fmt.Errorf("cannot create post request: %w", err)
	}
	req.SetMessageID(cc.getMID())
	defer cc.ReleaseMessage(req)
	return cc.Do(req)
}

// NewPutRequest creates put request.
//
// Use ctx to set timeout.
//
// If payload is nil then content format is not used.
func NewPutRequest(ctx context.Context, messagePool *pool.Pool, path string, contentFormat message.MediaType, payload io.ReadSeeker, opts ...message.Option) (*pool.Message, error) {
	req, err := newCommonRequest(ctx, messagePool, codes.PUT, path, opts...)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.SetContentFormat(contentFormat)
		req.SetBody(payload)
	}
	return req, nil
}

// Put issues a PUT to the specified path.
//
// Use ctx to set timeout.
//
// An error is returned if by failure to speak COAP (such as a network connectivity problem).
// Any status code doesn't cause an error.
//
// If payload is nil then content format is not used.
func (cc *ClientConn) Put(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, opts ...message.Option) (*pool.Message, error) {
	req, err := NewPutRequest(ctx, cc.messagePool, path, contentFormat, payload, opts...)
	if err != nil {
		return nil, fmt.Errorf("cannot create put request: %w", err)
	}
	req.SetMessageID(cc.getMID())
	defer cc.ReleaseMessage(req)
	return cc.Do(req)
}

// NewDeleteRequest creates delete request.
//
// Use ctx to set timeout.
func NewDeleteRequest(ctx context.Context, messagePool *pool.Pool, path string, opts ...message.Option) (*pool.Message, error) {
	return newCommonRequest(ctx, messagePool, codes.DELETE, path, opts...)
}

// Delete deletes the resource identified by the request path.
//
// Use ctx to set timeout.
func (cc *ClientConn) Delete(ctx context.Context, path string, opts ...message.Option) (*pool.Message, error) {
	req, err := NewDeleteRequest(ctx, cc.messagePool, path, opts...)
	if err != nil {
		return nil, fmt.Errorf("cannot create delete request: %w", err)
	}
	req.SetMessageID(cc.getMID())
	defer cc.ReleaseMessage(req)
	return cc.Do(req)
}

// Context returns the client's context.
//
// If connections was closed context is cancelled.
func (cc *ClientConn) Context() context.Context {
	return cc.session.Context()
}

// Ping issues a PING to the client and waits for PONG response.
//
// Use ctx to set timeout.
func (cc *ClientConn) Ping(ctx context.Context) error {
	resp := make(chan bool, 1)
	receivedPong := func() {
		select {
		case resp <- true:
		default:
		}
	}
	cancel, err := cc.AsyncPing(receivedPong)
	if err != nil {
		return err
	}
	defer cancel()
	select {
	case <-resp:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// AsyncPing sends ping and receivedPong will be called when pong arrives. It returns cancellation of ping operation.
func (cc *ClientConn) AsyncPing(receivedPong func()) (func(), error) {
	req := cc.AcquireMessage(cc.Context())
	defer cc.ReleaseMessage(req)
	req.SetType(message.Confirmable)
	req.SetCode(codes.Empty)
	mid := cc.getMID()
	req.SetMessageID(mid)
	if _, loaded := cc.midHandlerContainer.LoadOrStore(mid, func(w *responsewriter.ResponseWriter[*ClientConn], r *pool.Message) {
		if r.Type() == message.Reset || r.Type() == message.Acknowledgement {
			receivedPong()
		}
	}); loaded {
		return nil, fmt.Errorf("cannot insert mid(%v) handler: %w", mid, coapErrors.ErrKeyAlreadyExists)
	}
	removeMidHandler := func() {
		_, _ = cc.midHandlerContainer.PullOut(mid)
	}
	if err := cc.session.WriteMessage(req); err != nil {
		removeMidHandler()
		return nil, fmt.Errorf("cannot write request: %w", err)
	}
	return removeMidHandler, nil
}

// Run reads and process requests from a connection, until the connection is closed.
func (cc *ClientConn) Run() error {
	return cc.session.Run(cc)
}

// AddOnClose calls function on close connection event.
func (cc *ClientConn) AddOnClose(f EventFunc) {
	cc.session.AddOnClose(f)
}

func (cc *ClientConn) RemoteAddr() net.Addr {
	return cc.session.RemoteAddr()
}

func (cc *ClientConn) LocalAddr() net.Addr {
	return cc.session.LocalAddr()
}

func (cc *ClientConn) sendPong(w *responsewriter.ResponseWriter[*ClientConn], r *pool.Message) {
	if err := w.SetResponse(codes.Empty, message.TextPlain, nil); err != nil {
		cc.errors(fmt.Errorf("cannot send pong response: %w", err))
	}
}

type bwResponseWriter struct {
	w *responsewriter.ResponseWriter[*ClientConn]
}

func (b *bwResponseWriter) Message() blockwise.Message {
	return b.w.Message()
}

func (b *bwResponseWriter) SetMessage(m blockwise.Message) {
	b.w.ClientConn().ReleaseMessage(b.w.Message())
	b.w.SetMessage(m.(*pool.Message))
}

func (b *bwResponseWriter) RemoteAddr() net.Addr {
	return b.w.ClientConn().RemoteAddr()
}

func (cc *ClientConn) handleBW(w *responsewriter.ResponseWriter[*ClientConn], m *pool.Message) {
	if cc.blockWise != nil {
		bwr := bwResponseWriter{
			w: w,
		}
		cc.blockWise.Handle(&bwr, m, cc.blockwiseSZX, cc.session.MaxMessageSize(), func(bw blockwise.ResponseWriter, br blockwise.Message) {
			rw := bw.(*bwResponseWriter).w
			rm := br.(*pool.Message)
			if h, ok := cc.tokenHandlerContainer.PullOut(m.Token().Hash()); ok {
				h(rw, rm)
				return
			}
			cc.handler(rw, rm)
		})
		return
	}
	if h, ok := cc.tokenHandlerContainer.PullOut(m.Token().Hash()); ok {
		h(w, m)
		return
	}
	cc.handler(w, m)
}

func (cc *ClientConn) handle(w *responsewriter.ResponseWriter[*ClientConn], r *pool.Message) {
	if r.Code() == codes.Empty && r.Type() == message.Confirmable && len(r.Token()) == 0 && len(r.Options()) == 0 && r.Body() == nil {
		cc.sendPong(w, r)
		return
	}
	if h, ok := cc.midHandlerContainer.PullOut(r.MessageID()); ok {
		h(w, r)
		return
	}
	if r.IsSeparateMessage() {
		// msg was processed by token handler - just drop it.
		return
	}
	cc.handleBW(w, r)
}

// Sequence acquires sequence number.
func (cc *ClientConn) Sequence() uint64 {
	return atomic.AddUint64(&cc.sequence, 1)
}

func (cc *ClientConn) responseMsgCacheID(msgID int32) string {
	return fmt.Sprintf("resp-%v-%d", cc.RemoteAddr(), msgID)
}

func (cc *ClientConn) addResponseToCache(resp *pool.Message) error {
	marshaledResp, err := resp.MarshalWithEncoder(coder.DefaultCoder)
	if err != nil {
		return err
	}
	cacheMsg := make([]byte, len(marshaledResp))
	copy(cacheMsg, marshaledResp)
	cc.responseMsgCache.LoadOrStore(cc.responseMsgCacheID(resp.MessageID()), cache.NewElement(cacheMsg, time.Now().Add(ExchangeLifetime), nil))
	return nil
}

func (cc *ClientConn) getResponseFromCache(mid int32, resp *pool.Message) (bool, error) {
	cachedResp := cc.responseMsgCache.Load(cc.responseMsgCacheID(mid))
	if cachedResp == nil {
		return false, nil
	}
	if rawMsg, ok := cachedResp.Data().([]byte); ok {
		_, err := resp.UnmarshalWithDecoder(coder.DefaultCoder, rawMsg)
		if err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// CheckMyMessageID compare client msgID against peer messageID and if it is near < 0xffff/4 then incrase msgID.
// When msgIDs met it can cause issue because cache can send message to which doesn't bellows to request.
func (cc *ClientConn) CheckMyMessageID(req *pool.Message) {
	if req.Type() == message.Confirmable && uint16(req.MessageID())-uint16(atomic.LoadUint32(&cc.msgID)) < 0xffff/4 {
		atomic.AddUint32(&cc.msgID, 0xffff/2)
	}
}

func (cc *ClientConn) checkResponseCache(req *pool.Message, w *responsewriter.ResponseWriter[*ClientConn]) (bool, error) {
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

func isPongOrResetResponse(w *responsewriter.ResponseWriter[*ClientConn]) bool {
	return w.Message().IsModified() && (w.Message().Type() == message.Reset || w.Message().Code() == codes.Empty)
}

func sendJustAcknowledgeMessage(reqType message.Type, w *responsewriter.ResponseWriter[*ClientConn]) bool {
	return reqType == message.Confirmable && !w.Message().IsModified()
}

func (cc *ClientConn) processResponse(reqType message.Type, reqMessageID int32, w *responsewriter.ResponseWriter[*ClientConn]) error {
	switch {
	case isPongOrResetResponse(w):
		if reqType == message.Confirmable {
			w.Message().SetType(message.Acknowledgement)
			w.Message().SetMessageID(reqMessageID)
		} else {
			if w.Message().Type() != message.Reset {
				w.Message().SetType(message.NonConfirmable)
			}
			w.Message().SetMessageID(cc.getMID())
		}
		return nil
	case sendJustAcknowledgeMessage(reqType, w):
		// send message to separate(confirm received) message, if response is not modified
		w.Message().SetCode(codes.Empty)
		w.Message().SetType(message.Acknowledgement)
		w.Message().SetMessageID(reqMessageID)
		w.Message().SetToken(nil)
		err := cc.addResponseToCache(w.Message())
		if err != nil {
			return fmt.Errorf("cannot cache response: %w", err)
		}
		return nil
	case !w.Message().IsModified():
		// don't send response
		return nil
	}

	// send piggybacked response
	w.Message().SetType(message.Confirmable)
	w.Message().SetMessageID(cc.getMID())
	if reqType == message.Confirmable {
		w.Message().SetType(message.Acknowledgement)
		w.Message().SetMessageID(reqMessageID)
	}
	if reqType == message.Confirmable || reqType == message.NonConfirmable {
		err := cc.addResponseToCache(w.Message())
		if err != nil {
			return fmt.Errorf("cannot cache response: %w", err)
		}
	}
	return nil
}

func (cc *ClientConn) processReq(req *pool.Message, w *responsewriter.ResponseWriter[*ClientConn]) error {
	defer cc.inactivityMonitor.Notify()
	reqMid := req.MessageID()

	// The same message ID can not be handled concurrently
	// for deduplication to work
	l := cc.msgIDMutex.Lock(reqMid)
	defer l.Unlock()

	if ok, err := cc.checkResponseCache(req, w); err != nil {
		return err
	} else if ok {
		return nil
	}

	w.Message().SetModified(false)
	reqType := req.Type()
	reqMessageID := req.MessageID()
	cc.handle(w, req)

	return cc.processResponse(reqType, reqMessageID, w)
}

func (cc *ClientConn) Process(datagram []byte) error {
	if uint32(len(datagram)) > cc.session.MaxMessageSize() {
		return fmt.Errorf("max message size(%v) was exceeded %v", cc.session.MaxMessageSize(), len(datagram))
	}
	req := cc.AcquireMessage(cc.Context())
	_, err := req.UnmarshalWithDecoder(coder.DefaultCoder, datagram)
	if err != nil {
		cc.ReleaseMessage(req)
		return err
	}
	closeConnection := func() {
		if errC := cc.Close(); errC != nil {
			cc.errors(fmt.Errorf("cannot close connection: %w", errC))
		}
	}
	req.SetSequence(cc.Sequence())
	cc.CheckMyMessageID(req)
	cc.inactivityMonitor.Notify()
	err = cc.goPool(func() {
		defer func() {
			if !req.IsHijacked() {
				cc.ReleaseMessage(req)
			}
		}()
		resp := cc.AcquireMessage(cc.Context())
		resp.SetToken(req.Token())
		w := responsewriter.New(resp, cc, req.Options())
		defer func() {
			cc.ReleaseMessage(w.Message())
		}()
		errP := cc.processReq(req, w)
		if errP != nil {
			closeConnection()
			cc.errors(fmt.Errorf("cannot write response: %w", errP))
			return
		}
		if !w.Message().IsModified() {
			// nothing to send
			return
		}
		errW := cc.writeMessage(w.Message())
		if errW != nil {
			closeConnection()
			cc.errors(fmt.Errorf("cannot write response: %w", errW))
		}
	})
	if err != nil {
		cc.ReleaseMessage(req)
		return err
	}
	return nil
}

func (cc *ClientConn) Client() *Client {
	return NewClient(cc)
}

// SetContextValue stores the value associated with key to context of connection.
func (cc *ClientConn) SetContextValue(key interface{}, val interface{}) {
	cc.session.SetContextValue(key, val)
}

// Done signalizes that connection is not more processed.
func (cc *ClientConn) Done() <-chan struct{} {
	return cc.session.Done()
}

// CheckExpirations checks and remove expired items from caches.
func (cc *ClientConn) CheckExpirations(now time.Time) {
	cc.inactivityMonitor.CheckInactivity(now, cc)
	if cc.blockWise != nil {
		cc.blockWise.CheckExpirations(now)
	}
}

func (cc *ClientConn) AcquireMessage(ctx context.Context) *pool.Message {
	return cc.messagePool.AcquireMessage(ctx)
}

func (cc *ClientConn) ReleaseMessage(m *pool.Message) {
	cc.messagePool.ReleaseMessage(m)
}

// WriteMulticastMessage sends multicast to the remote multicast address.
// By default it is sent over all network interfaces and all compatible source IP addresses with hop limit 1.
// Via opts you can specify the network interface, source IP address, and hop limit.
func (cc *ClientConn) WriteMulticastMessage(req *pool.Message, address *net.UDPAddr, options ...coapNet.MulticastOption) error {
	if req.Type() == message.Confirmable {
		return fmt.Errorf("multicast messages cannot be confirmable")
	}
	req.SetMessageID(cc.getMID())

	err := cc.session.WriteMulticastMessage(req, address, options...)
	if err != nil {
		return fmt.Errorf("cannot write request: %w", err)
	}
	return nil
}

func (cc *ClientConn) InactivityMonitor() inactivity.Monitor {
	return cc.inactivityMonitor
}
