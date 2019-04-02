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

	kitNet "github.com/go-ocf/kit/net"
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
		Code:      NotFound,
		MessageID: req.Msg.MessageID(),
		Token:     req.Msg.Token(),
	})
	w.WriteMsgWithContext(req.Ctx, msg)
}

func failedHandler() Handler { return HandlerFunc(HandleFailed) }

// ListenAndServe Starts a server on address and network specified Invoke handler
// for incoming queries.
func ListenAndServe(addr string, network string, handler Handler) error {
	server := &Server{Addr: addr, Net: network, Handler: handler}
	return server.ListenAndServe()
}

// ListenAndServeTLS acts like http.ListenAndServeTLS, more information in
// http://golang.org/pkg/net/http/#ListenAndServeTLS
func ListenAndServeTLS(addr, certFile, keyFile string, handler Handler) error {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return err
	}

	config := tls.Config{
		Certificates: []tls.Certificate{cert},
	}

	server := &Server{
		Addr:      addr,
		Net:       "tcp-tls",
		TLSConfig: &config,
		Handler:   handler,
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
	newSessionUDPFunc func(connection *kitNet.ConnUDP, srv *Server, sessionUDPData *kitNet.ConnUDPContext) (networkSession, error)
	// If newSessionUDPFunc is set it is called when session TCP want to be created
	newSessionTCPFunc func(connection *kitNet.Conn, srv *Server) (networkSession, error)
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
	// Disable tcp signal messages
	DisableTCPSignalMessages bool

	// UDP packet or TCP connection queue
	queue chan *Request
	// Workers count
	workersCount int32

	sessionUDPMapLock sync.Mutex
	sessionUDPMap     map[string]networkSession

	doneLock sync.Mutex
	done     bool
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
	var connUDP *kitNet.ConnUDP
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
		listener, err = kitNet.NewTCPListener(srv.Net, addr, srv.heartBeat())
		if err != nil {
			return fmt.Errorf("cannot listen and serve: %v", err)
		}
		defer listener.Close()
	case "tcp-tls", "tcp4-tls", "tcp6-tls":
		network := strings.TrimSuffix(srv.Net, "-tls")
		listener, err = kitNet.NewTLSListener(network, addr, srv.TLSConfig, srv.heartBeat())
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
		if err := kitNet.SetUDPSocketOptions(l); err != nil {
			return err
		}
		connUDP = kitNet.NewConnUDP(l, srv.heartBeat(), 2)
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
		if err := kitNet.SetUDPSocketOptions(l); err != nil {
			return err
		}
		connUDP = kitNet.NewConnUDP(l, srv.heartBeat(), 2)
		defer connUDP.Close()
		if len(srv.UDPMcastInterfaces) > 0 {
			for _, ifi := range srv.UDPMcastInterfaces {
				if err := connUDP.JoinGroup(&ifi, a); err != nil {
					return err
				}
			}
		} else {
			if err := connUDP.JoinGroup(nil, a); err != nil {
				return err
			}
		}
		if err := connUDP.SetMulticastLoopback(true); err != nil {
			return err
		}
	default:
		return ErrInvalidNetParameter
	}

	return srv.activateAndServe(listener, nil, connUDP)
}

func (srv *Server) initServeUDP(connUDP *kitNet.ConnUDP) error {
	return srv.serveUDP(newShutdownWithContext(srv.doneChan), connUDP)
}

func (srv *Server) initServeTCP(conn *kitNet.Conn) error {
	if srv.NotifyStartedFunc != nil {
		srv.NotifyStartedFunc()
	}
	return srv.serveTCPconnection(newShutdownWithContext(srv.doneChan), conn)
}

// ActivateAndServe starts a coapserver with the PacketConn or Listener
// configured in *Server. Its main use is to start a server from systemd.
func (srv *Server) ActivateAndServe() error {
	if srv.Conn != nil {
		switch c := srv.Conn.(type) {
		case *net.TCPConn, *tls.Conn:
			return srv.activateAndServe(nil, kitNet.NewConn(c, srv.heartBeat()), nil)
		case *net.UDPConn:
			return srv.activateAndServe(nil, nil, kitNet.NewConnUDP(c, srv.heartBeat(), 2))
		}
		return ErrInvalidServerConnParameter
	}
	if srv.Listener != nil {
		return srv.activateAndServe(srv.Listener, nil, nil)
	}

	return ErrInvalidServerListenerParameter
}

func (srv *Server) activateAndServe(listener Listener, conn *kitNet.Conn, connUDP *kitNet.ConnUDP) error {
	srv.doneLock.Lock()
	srv.done = false
	srv.doneChan = make(chan struct{})
	srv.doneLock.Unlock()

	if srv.MaxMessageSize > 0 && srv.MaxMessageSize < uint32(szxToBytes[BlockWiseSzx16]) {
		return ErrInvalidMaxMesssageSizeParameter
	}

	srv.sessionUDPMap = make(map[string]networkSession)

	srv.queue = make(chan *Request)
	defer close(srv.queue)

	if srv.newSessionTCPFunc == nil {
		srv.newSessionTCPFunc = func(connection *kitNet.Conn, srv *Server) (networkSession, error) {
			session, err := newSessionTCP(connection, srv)
			if err != nil {
				return nil, err
			}
			if session.blockWiseEnabled() {
				return &blockWiseSession{networkSession: session}, nil
			}
			return session, nil
		}
	}

	if srv.newSessionUDPFunc == nil {
		srv.newSessionUDPFunc = func(connection *kitNet.ConnUDP, srv *Server, sessionUDPData *kitNet.ConnUDPContext) (networkSession, error) {
			session, err := newSessionUDP(connection, srv, sessionUDPData)
			if err != nil {
				return nil, err
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
		return srv.serveTCP(listener)
	case conn != nil:
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
	if srv.done {
		return fmt.Errorf("already shutdowned")
	}
	srv.done = true
	close(srv.doneChan)
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

func (srv *Server) serveTCPconnection(ctx *shutdownContext, conn *kitNet.Conn) error {
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
			return session.closeWithError(fmt.Errorf("cannot serve tcp connection: %v", err))
		}

		if srv.MaxMessageSize != 0 &&
			uint32(mti.totLen) > srv.MaxMessageSize {
			return session.closeWithError(fmt.Errorf("cannot serve tcp connection: %v", ErrMaxMessageSizeLimitExceeded))
		}

		body := make([]byte, mti.BodyLen())
		err = conn.ReadFullWithContext(ctx, body)
		if err != nil {
			return session.closeWithError(fmt.Errorf("cannot serve tcp connection: %v", err))
		}

		o, p, err := parseTcpOptionsPayload(mti, body)
		if err != nil {
			return session.closeWithError(fmt.Errorf("cannot serve tcp connection: %v", err))
		}

		msg := new(TcpMessage)

		msg.fill(mti, o, p)

		// We will block poller wait loop when
		// all pool workers are busy.
		c := ClientConn{commander: &ClientCommander{session}}
		srv.spawnWorker(&Request{Client: &c, Msg: msg, Ctx: sessCtx, Sequence: c.Sequence()})
	}
}

// serveTCP starts a TCP listener for the server.
func (srv *Server) serveTCP(l Listener) error {
	if srv.NotifyStartedFunc != nil {
		srv.NotifyStartedFunc()
	}

	var wg sync.WaitGroup
	ctx := newShutdownWithContext(srv.doneChan)

	for {
		rw, err := l.AcceptWithContext(ctx)
		if err != nil {
			wg.Wait()
			return fmt.Errorf("cannot serve tcp: %v", err)
		}
		if rw != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				srv.serveTCPconnection(ctx, kitNet.NewConn(rw, srv.heartBeat()))
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
		c := ClientConn{commander: &ClientCommander{v}}
		srv.NotifySessionEndFunc(&c, err)
	}
}

// serveUDP starts a UDP listener for the server.
func (srv *Server) serveUDP(ctx *shutdownContext, connUDP *kitNet.ConnUDP) error {
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

		srv.sessionUDPMapLock.Lock()
		session := srv.sessionUDPMap[s.Key()]
		if session == nil {
			session, err = srv.newSessionUDPFunc(connUDP, srv, s)
			if err != nil {
				return err
			}
			c := ClientConn{commander: &ClientCommander{session}}
			srv.NotifySessionNewFunc(&c)
			srv.sessionUDPMap[s.Key()] = session
			srv.sessionUDPMapLock.Unlock()
		} else {
			srv.sessionUDPMapLock.Unlock()
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
