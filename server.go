// Package coap provides a CoAP client and server.
package coap

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-ocf/go-coap/codes"
	coapNet "github.com/go-ocf/go-coap/net"
	dtls "github.com/pion/dtls/v2"
)

// Interval for stop worker if no load
const idleWorkerTimeout = 10 * time.Second

// Maximum number of workers
const maxWorkersCount = 10000

const coapTimeout time.Duration = 3600 * time.Second

const waitTimer time.Duration = 100 * time.Millisecond

const syncTimeout time.Duration = 30 * time.Second

const maxMessageSize = 1152

const (
	defaultReadBufferSize  = 4096
	defaultWriteBufferSize = 4096
)

// Listener defined used by coap
type Listener interface {
	Close() error
	AcceptWithContext(ctx context.Context) (net.Conn, error)
}

//DefaultPort default unsecure port for COAP server
const DefaultPort = 5683

//DefaultSecurePort default secure port for COAP server
const DefaultSecurePort = 5684

//const tcpIdleTimeout time.Duration = 8 * time.Second

// Handler is implemented by any value that implements ServeCOAP.
type Handler interface {
	ServeCOAP(w ResponseWriter, r *Request)
}

// The HandlerFunc type is an adapter to allow the use of
// ordinary functions as COAP handlers.  If f is a function
// with the appropriate signature, HandlerFunc(f) is a
// Handler object that calls f.
type HandlerFunc func(ResponseWriter, *Request)

// ServeCOAP calls f(w, r).
func (f HandlerFunc) ServeCOAP(w ResponseWriter, r *Request) {
	f(w, r)
}

// HandleFailed returns a HandlerFunc that returns NotFound for every request it gets.
func HandleFailed(w ResponseWriter, req *Request) {
	msg := req.Client.NewMessage(MessageParams{
		Type:      Acknowledgement,
		Code:      codes.NotFound,
		MessageID: req.Msg.MessageID(),
		Token:     req.Msg.Token(),
	})
	w.WriteMsgWithContext(req.Ctx, msg)
}

func failedHandler() Handler { return HandlerFunc(HandleFailed) }

// ListenAndServe Starts a server on address and network specified Invoke handler
// for incoming queries.
func ListenAndServe(network string, addr string, handler Handler) error {
	server := &Server{Addr: addr, Net: network, Handler: handler}
	return server.ListenAndServe()
}

// ListenAndServeTLS acts like http.ListenAndServeTLS, more information in
// http://golang.org/pkg/net/http/#ListenAndServeTLS
func ListenAndServeTLS(network, addr string, config *tls.Config, handler Handler) error {
	server := &Server{
		Addr:      addr,
		Net:       fixNetTLS(network),
		TLSConfig: config,
		Handler:   handler,
	}

	return server.ListenAndServe()
}

// ListenAndServeDTLS acts like ListenAndServeTLS, more information in
// http://golang.org/pkg/net/http/#ListenAndServeTLS
func ListenAndServeDTLS(network string, addr string, config *dtls.Config, handler Handler) error {
	server := &Server{
		Addr:       addr,
		Net:        fixNetDTLS(network),
		DTLSConfig: config,
		Handler:    handler,
	}

	return server.ListenAndServe()
}

// ActivateAndServe activates a server with a listener from systemd,
// l and p should not both be non-nil.
// If both l and p are not nil only p will be used.
// Invoke handler for incoming queries.
func ActivateAndServe(l Listener, p net.Conn, handler Handler) error {
	server := &Server{Listener: l, Conn: p, Handler: handler}
	return server.ActivateAndServe()
}

// A Server defines parameters for running an COAP server.
type Server struct {
	// Address to listen on, ":COAP" if empty.
	Addr string
	// if "tcp" or "tcp-tls" (COAP over TLS) it will invoke a TCP listener, otherwise an UDP one
	Net string
	// TCP Listener to use, this is to aid in systemd's socket activation.
	Listener Listener
	// TLS connection configuration
	TLSConfig *tls.Config
	// DTLSConfig connection configuration
	DTLSConfig *dtls.Config
	// UDP/TCP "Listener/Connection" to use, this is to aid in systemd's socket activation.
	Conn net.Conn
	// Handler to invoke, COAP.DefaultServeMux if nil.
	Handler Handler
	// Max message size that could be received from peer. Min 16bytes. If not set
	// it defaults is unlimited.
	MaxMessageSize uint32
	// The net.Conn.SetReadTimeout value for new connections, defaults to 1hour.
	ReadTimeout time.Duration
	// The net.Conn.SetWriteTimeout value for new connections, defaults to 1hour.
	WriteTimeout time.Duration
	// If NotifyStartedFunc is set it is called once the server has started listening.
	NotifyStartedFunc func()
	// Defines wake up interval from operations Read, Write over connection. defaults is 100ms.
	HeartBeat time.Duration
	// If newSessionUDPFunc is set it is called when session UDP want to be created
	newSessionUDPFunc func(connection *coapNet.ConnUDP, srv *Server, sessionUDPData *coapNet.ConnUDPContext) (networkSession, error)
	// If newSessionUDPFunc is set it is called when session TCP want to be created
	newSessionTCPFunc func(connection *coapNet.Conn, srv *Server) (networkSession, error)
	// If newSessionUDPFunc is set it is called when session DTLS want to be created
	newSessionDTLSFunc func(connection *coapNet.Conn, srv *Server) (networkSession, error)
	// If NotifyNewSession is set it is called when new TCP/UDP session was created.
	NotifySessionNewFunc func(w *ClientConn)
	// If NotifyNewSession is set it is called when TCP/UDP session was ended.
	NotifySessionEndFunc func(w *ClientConn, err error)
	// The interfaces that will be used for udp-mcast (default uses the system assigned for multicast)
	UDPMcastInterfaces []net.Interface
	// Use blockWise transfer for transfer payload (default for UDP it's enabled, for TCP it's disable)
	BlockWiseTransfer *bool
	// Set maximal block size of payload that will be send in fragment
	BlockWiseTransferSzx *BlockWiseSzx
	// Disable send tcp signal CSM message
	DisableTCPSignalMessageCSM bool
	// Disable processes Capabilities and Settings Messages from client - iotivity sends max message size without blockwise.
	DisablePeerTCPSignalMessageCSMs bool
	// Keepalive setup
	KeepAlive KeepAlive
	// Report errors
	Errors func(err error)

	// UDP packet or TCP connection queue
	queue chan *Request
	// Workers count
	workersCount int32

	sessionUDPMapLock sync.Mutex
	sessionUDPMap     map[string]networkSession

	doneLock sync.Mutex
	doneChan chan struct{}
}

func (srv *Server) workerChannelHandler(inUse bool, timeout *time.Timer) bool {
	select {
	case w, ok := <-srv.queue:
		if !ok {
			return false
		}
		inUse = true
		srv.serve(w)
	case <-timeout.C:
		if !inUse {
			return false
		}
		inUse = false
		timeout.Reset(idleWorkerTimeout)
	}
	return true
}

func (srv *Server) worker(w *Request) {
	srv.serve(w)

	for {
		count := atomic.LoadInt32(&srv.workersCount)
		if count > maxWorkersCount {
			return
		}
		if atomic.CompareAndSwapInt32(&srv.workersCount, count, count+1) {
			break
		}
	}

	defer atomic.AddInt32(&srv.workersCount, -1)

	inUse := false
	timeout := time.NewTimer(idleWorkerTimeout)
	defer timeout.Stop()
	for srv.workerChannelHandler(inUse, timeout) {
	}
}

func (srv *Server) spawnWorker(w *Request) {
	select {
	case srv.queue <- w:
	default:
		go srv.worker(w)
	}
}

// ListenAndServe starts a coapserver on the configured address in *Server.
func (srv *Server) ListenAndServe() error {
	var listener Listener
	var connUDP *coapNet.ConnUDP
	addr := srv.Addr
	var err error
	if addr == "" {
		switch {
		case strings.Contains(srv.Net, "-tls"):
			addr = ":" + strconv.Itoa(DefaultSecurePort)
		default:
			addr = ":" + strconv.Itoa(DefaultPort)
		}
	}

	switch srv.Net {
	case "tcp", "tcp4", "tcp6":
		listener, err = coapNet.NewTCPListener(srv.Net, addr, srv.heartBeat())
		if err != nil {
			return fmt.Errorf("cannot listen and serve: %v", err)
		}
		defer listener.Close()
	case "tcp-tls", "tcp4-tls", "tcp6-tls":
		network := strings.TrimSuffix(srv.Net, "-tls")
		listener, err = coapNet.NewTLSListener(network, addr, srv.TLSConfig, srv.heartBeat())
		if err != nil {
			return fmt.Errorf("cannot listen and serve: %v", err)
		}
		defer listener.Close()
	case "udp", "udp4", "udp6":
		a, err := net.ResolveUDPAddr(srv.Net, addr)
		if err != nil {
			return err
		}
		l, err := net.ListenUDP(srv.Net, a)
		if err != nil {
			return err
		}
		connUDP = coapNet.NewConnUDP(l, srv.heartBeat(), 2, srv.Errors)
		defer connUDP.Close()
	case "udp-mcast", "udp4-mcast", "udp6-mcast":
		network := strings.TrimSuffix(srv.Net, "-mcast")

		a, err := net.ResolveUDPAddr(network, addr)
		if err != nil {
			return err
		}
		l, err := net.ListenUDP(network, a)
		if err != nil {
			return err
		}
		connUDP = coapNet.NewConnUDP(l, srv.heartBeat(), 2, srv.Errors)
		defer connUDP.Close()
		ifaces := srv.UDPMcastInterfaces
		if len(ifaces) == 0 {
			ifaces, err = net.Interfaces()
			if err != nil {
				return err
			}
		}
		for _, iface := range ifaces {
			if err := connUDP.JoinGroup(&iface, a); err != nil && srv.Errors != nil {
				srv.Errors(fmt.Errorf("cannot JoinGroup(%v, %v): %w", iface, a, err))
			}
		}
		if err := connUDP.SetMulticastLoopback(true); err != nil {
			return err
		}
	case "udp-dtls", "udp4-dtls", "udp6-dtls":
		network := strings.TrimSuffix(srv.Net, "-dtls")
		listener, err = coapNet.NewDTLSListener(network, addr, srv.DTLSConfig, srv.heartBeat())
		if err != nil {
			return fmt.Errorf("cannot listen and serve: %v", err)
		}
		defer listener.Close()
	default:
		return ErrInvalidNetParameter
	}

	return srv.activateAndServe(listener, nil, connUDP)
}

func (srv *Server) initServeUDP(connUDP *coapNet.ConnUDP) error {
	doneChan, err := srv.initDone()
	if err != nil {
		return err
	}

	return srv.serveUDP(newShutdownWithContext(doneChan), connUDP)
}

func (srv *Server) initServeTCP(conn *coapNet.Conn) error {
	doneChan, err := srv.initDone()
	if err != nil {
		return err
	}

	if srv.NotifyStartedFunc != nil {
		srv.NotifyStartedFunc()
	}
	return srv.serveTCPConnection(newShutdownWithContext(doneChan), conn)
}

func (srv *Server) initDone() (<-chan struct{}, error) {
	srv.doneLock.Lock()
	defer srv.doneLock.Unlock()
	if srv.doneChan != nil {
		return nil, fmt.Errorf("server already serve connections")
	}
	doneChan := make(chan struct{})
	srv.doneChan = doneChan
	return doneChan, nil
}

func (srv *Server) initServeDTLS(conn *coapNet.Conn) error {
	doneChan, err := srv.initDone()
	if err != nil {
		return err
	}
	if srv.NotifyStartedFunc != nil {
		srv.NotifyStartedFunc()
	}
	return srv.serveDTLSConnection(newShutdownWithContext(doneChan), conn)
}

// ActivateAndServe starts a coapserver with the PacketConn or Listener
// configured in *Server. Its main use is to start a server from systemd.
func (srv *Server) ActivateAndServe() error {
	if srv.Conn != nil {
		switch c := srv.Conn.(type) {
		case *net.TCPConn:
			if srv.Net == "" {
				srv.Net = "tcp"
			}
			return srv.activateAndServe(nil, coapNet.NewConn(c, srv.heartBeat()), nil)
		case *tls.Conn:
			if srv.Net == "" {
				srv.Net = "tcp-tls"
			}
			return srv.activateAndServe(nil, coapNet.NewConn(c, srv.heartBeat()), nil)
		case *dtls.Conn:
			if srv.Net == "" {
				srv.Net = "udp-dtls"
			}
			return srv.activateAndServe(nil, coapNet.NewConn(c, srv.heartBeat()), nil)
		case *net.UDPConn:
			if srv.Net == "" {
				srv.Net = "udp"
			}
			return srv.activateAndServe(nil, nil, coapNet.NewConnUDP(c, srv.heartBeat(), 2, srv.Errors))
		}
		return ErrInvalidServerConnParameter
	}
	if srv.Listener != nil {
		return srv.activateAndServe(srv.Listener, nil, nil)
	}

	return ErrInvalidServerListenerParameter
}

func (srv *Server) activateAndServe(listener Listener, conn *coapNet.Conn, connUDP *coapNet.ConnUDP) error {
	if srv.MaxMessageSize > 0 && srv.MaxMessageSize < uint32(szxToBytes[BlockWiseSzx16]) {
		return ErrInvalidMaxMesssageSizeParameter
	}

	err := validateKeepAlive(srv.KeepAlive)
	if err != nil {
		return fmt.Errorf("keepalive: %w", err)
	}

	srv.sessionUDPMapLock.Lock()
	srv.sessionUDPMap = make(map[string]networkSession)
	srv.sessionUDPMapLock.Unlock()

	srv.queue = make(chan *Request)
	defer close(srv.queue)

	if srv.newSessionTCPFunc == nil {
		srv.newSessionTCPFunc = func(connection *coapNet.Conn, srv *Server) (networkSession, error) {
			session, err := newSessionTCP(connection, srv)
			if err != nil {
				return nil, err
			}
			if srv.KeepAlive.Enable {
				session = newKeepAliveSession(session, srv)
			}
			if session.blockWiseEnabled() {
				return &blockWiseSession{networkSession: session}, nil
			}
			return session, nil
		}
	}

	if srv.newSessionDTLSFunc == nil {
		srv.newSessionDTLSFunc = func(connection *coapNet.Conn, srv *Server) (networkSession, error) {
			session, err := newSessionDTLS(connection, srv)
			if err != nil {
				return nil, err
			}
			if srv.KeepAlive.Enable {
				session = newKeepAliveSession(session, srv)
			}
			if session.blockWiseEnabled() {
				return &blockWiseSession{networkSession: session}, nil
			}
			return session, nil
		}
	}

	if srv.newSessionUDPFunc == nil {
		srv.newSessionUDPFunc = func(connection *coapNet.ConnUDP, srv *Server, sessionUDPData *coapNet.ConnUDPContext) (networkSession, error) {
			session, err := newSessionUDP(connection, srv, sessionUDPData)
			if err != nil {
				return nil, err
			}
			if srv.KeepAlive.Enable {
				session = newKeepAliveSession(session, srv)
			}
			if session.blockWiseEnabled() {
				return &blockWiseSession{networkSession: session}, nil
			}
			return session, nil
		}
	}

	if srv.NotifySessionNewFunc == nil {
		srv.NotifySessionNewFunc = func(w *ClientConn) {}
	}

	if srv.NotifySessionEndFunc == nil {
		srv.NotifySessionEndFunc = func(w *ClientConn, err error) {}
	}

	switch {
	case listener != nil:
		if _, ok := listener.(*coapNet.DTLSListener); ok {
			return srv.serveDTLSListener(listener)
		}
		return srv.serveTCPListener(listener)
	case conn != nil:
		if strings.HasSuffix(srv.Net, "-dtls") {
			return srv.initServeDTLS(conn)
		}
		return srv.initServeTCP(conn)
	case connUDP != nil:
		return srv.initServeUDP(connUDP)
	}

	return ErrInvalidServerListenerParameter
}

// Shutdown shuts down a server. After a call to Shutdown, ListenAndServe and
// ActivateAndServe will return.
func (srv *Server) Shutdown() error {
	srv.doneLock.Lock()
	defer srv.doneLock.Unlock()
	if srv.doneChan == nil {
		return fmt.Errorf("already shutdowned")
	}
	close(srv.doneChan)
	srv.doneChan = nil
	return nil
}

// readTimeout is a helper func to use system timeout if server did not intend to change it.
func (srv *Server) readTimeout() time.Duration {
	if srv.ReadTimeout != 0 {
		return srv.ReadTimeout
	}
	return coapTimeout
}

// readTimeout is a helper func to use system timeout if server did not intend to change it.
func (srv *Server) writeTimeout() time.Duration {
	if srv.WriteTimeout != 0 {
		return srv.WriteTimeout
	}
	return coapTimeout
}

// readTimeout is a helper func to use system timeout if server did not intend to change it.
func (srv *Server) heartBeat() time.Duration {
	if srv.HeartBeat != 0 {
		return srv.HeartBeat
	}
	return time.Millisecond * 100
}

func (srv *Server) serveDTLSConnection(ctx *shutdownContext, conn *coapNet.Conn) error {
	session, err := srv.newSessionDTLSFunc(conn, srv)
	if err != nil {
		return err
	}
	c := ClientConn{commander: &ClientCommander{session}}
	srv.NotifySessionNewFunc(&c)

	sessCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for {
		m := make([]byte, ^uint16(0))
		n, err := conn.ReadWithContext(ctx, m)
		if err != nil {
			err := fmt.Errorf("cannot serve DTLS connection %v", err)
			srv.closeSessions(err)
			return err
		}
		msg, err := ParseDgramMessage(m[:n])
		if err != nil {
			continue
		}

		// We will block poller wait loop when
		// all pool workers are busy.
		c := ClientConn{commander: &ClientCommander{session}}
		srv.spawnWorker(&Request{Client: &c, Msg: msg, Ctx: sessCtx, Sequence: c.Sequence()})
	}
}

// serveListener starts a DTLS listener for the server.
func (srv *Server) serveDTLSListener(l Listener) error {
	doneChan, err := srv.initDone()
	if err != nil {
		return err
	}

	if srv.NotifyStartedFunc != nil {
		srv.NotifyStartedFunc()
	}

	var wg sync.WaitGroup
	ctx := newShutdownWithContext(doneChan)

	for {
		rw, err := l.AcceptWithContext(ctx)
		if err != nil {
			switch err {
			case ErrServerClosed, context.DeadlineExceeded, context.Canceled:
				wg.Wait()
				return fmt.Errorf("cannot accept DTLS: %v", err)
			default:
				continue
			}
		}
		if rw != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				srv.serveDTLSConnection(ctx, coapNet.NewConn(rw, srv.heartBeat()))
			}()
		}
	}
}

func (srv *Server) serveTCPConnection(ctx *shutdownContext, conn *coapNet.Conn) error {
	session, err := srv.newSessionTCPFunc(conn, srv)
	if err != nil {
		return err
	}
	c := ClientConn{commander: &ClientCommander{session}}
	srv.NotifySessionNewFunc(&c)

	sessCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for {
		mti, err := readTcpMsgInfo(ctx, conn)
		if err != nil {
			return session.closeWithError(fmt.Errorf("cannot serve TCP connection: %v", err))
		}

		if srv.MaxMessageSize != 0 &&
			uint32(mti.totLen) > srv.MaxMessageSize {
			return session.closeWithError(fmt.Errorf("cannot serve TCP connection: %v", ErrMaxMessageSizeLimitExceeded))
		}

		body := make([]byte, mti.BodyLen())
		err = conn.ReadFullWithContext(ctx, body)
		if err != nil {
			return session.closeWithError(fmt.Errorf("cannot serve TCP connection: %v", err))
		}

		o, p, err := parseTcpOptionsPayload(mti, body)
		if err != nil {
			return session.closeWithError(fmt.Errorf("cannot serve TCP connection: %v", err))
		}

		msg := new(TcpMessage)

		msg.fill(mti, o, p)

		// We will block poller wait loop when
		// all pool workers are busy.
		c := ClientConn{commander: &ClientCommander{session}}
		srv.spawnWorker(&Request{Client: &c, Msg: msg, Ctx: sessCtx, Sequence: c.Sequence()})
	}
}

// serveListener starts a TCP listener for the server.
func (srv *Server) serveTCPListener(l Listener) error {
	doneChan, err := srv.initDone()
	if err != nil {
		return err
	}

	if srv.NotifyStartedFunc != nil {
		srv.NotifyStartedFunc()
	}

	var wg sync.WaitGroup
	ctx := newShutdownWithContext(doneChan)

	for {
		rw, err := l.AcceptWithContext(ctx)
		if err != nil {
			switch err {
			case ErrServerClosed, context.DeadlineExceeded, context.Canceled:
				wg.Wait()
				return fmt.Errorf("cannot accept TCP: %v", err)
			default:
				continue
			}
		}
		if rw != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				srv.serveTCPConnection(ctx, coapNet.NewConn(rw, srv.heartBeat()))
			}()
		}
	}
}

func (srv *Server) closeSessions(err error) {
	srv.sessionUDPMapLock.Lock()
	tmp := srv.sessionUDPMap
	srv.sessionUDPMap = make(map[string]networkSession)
	srv.sessionUDPMapLock.Unlock()
	for _, v := range tmp {
		v.closeWithError(err)
	}
}

func (srv *Server) getOrCreateUDPSession(connUDP *coapNet.ConnUDP, s *coapNet.ConnUDPContext) (networkSession, error) {
	srv.sessionUDPMapLock.Lock()
	defer srv.sessionUDPMapLock.Unlock()
	session := srv.sessionUDPMap[s.Key()]
	var err error
	if session == nil {
		session, err = srv.newSessionUDPFunc(connUDP, srv, s)
		if err != nil {
			return nil, err
		}
		c := ClientConn{commander: &ClientCommander{session}}
		srv.NotifySessionNewFunc(&c)
		srv.sessionUDPMap[s.Key()] = session
	}
	return session, nil
}

// serveUDP starts a UDP listener for the server.
func (srv *Server) serveUDP(ctx *shutdownContext, connUDP *coapNet.ConnUDP) error {
	if srv.NotifyStartedFunc != nil {
		srv.NotifyStartedFunc()
	}

	sessCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for {
		m := make([]byte, ^uint16(0))
		n, s, err := connUDP.ReadWithContext(ctx, m)
		if err != nil {
			err := fmt.Errorf("cannot serve UDP connection %v", err)
			srv.closeSessions(err)
			return err
		}
		m = m[:n]

		session, err := srv.getOrCreateUDPSession(connUDP, s)
		if err != nil {
			return err
		}

		msg, err := ParseDgramMessage(m)
		if err != nil {
			continue
		}
		c := ClientConn{commander: &ClientCommander{session}}
		srv.spawnWorker(&Request{Msg: msg, Client: &c, Ctx: sessCtx, Sequence: c.Sequence()})
	}
}

func (srv *Server) serve(r *Request) {
	w := responseWriterFromRequest(r)
	handlePairMsg(w, r, func(w ResponseWriter, r *Request) {
		handleSignalMsg(w, r, func(w ResponseWriter, r *Request) {
			handleBySessionTokenHandler(w, r, func(w ResponseWriter, r *Request) {
				handleBlockWiseMsg(w, r, srv.serveCOAP)
			})
		})
	})
}

func (srv *Server) serveCOAP(w ResponseWriter, r *Request) {
	handler := srv.Handler
	if handler == nil || reflect.ValueOf(handler).IsNil() {
		handler = DefaultServeMux
	}
	handler.ServeCOAP(w, r) // Writes back to the client
}
