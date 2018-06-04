// Package coap provides a CoAP client and server.
package coap

import (
	"bufio"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Interval for stop worker if no load
const idleWorkerTimeout = 10 * time.Second

// Maximum number of workers
const maxWorkersCount = 10000

const coapTimeout time.Duration = 3600 * time.Second

const maxPktLen = 1500

const (
	defaultReadBufferSize  = 4096
	defaultWriteBufferSize = 4096
)

//const tcpIdleTimeout time.Duration = 8 * time.Second

// Handler is implemented by any value that implements ServeCOAP.
type Handler interface {
	ServeCOAP(w ResponseWriter, r Message)
}

// A ResponseWriter interface is used by an COAP handler to
// construct an COAP requestCtx.
type ResponseWriter interface {
	// LocalAddr returns the net.Addr of the server
	LocalAddr() net.Addr
	// RemoteAddr returns the net.Addr of the client that sent the current request.
	RemoteAddr() net.Addr
	// WriteMsg writes a reply back to the client.
	WriteMsg(Message) error
	// Write writes a raw buffer back to the client.
	Write([]byte) (int, error)
	// Close closes the connection.
	Close() error
	// Return type of network
	IsTCP() bool
	// Create message for response via writter
	NewMessage(params MessageParams) Message
}

type requestCtx struct {
	request    Message
	udp        *net.UDPConn // i/o connection if UDP was used
	tcp        net.Conn     // i/o connection if TCP was used
	udpSession *SessionUDP  // oob data to get egress interface right
	s          *Server
}

// ServeMux is an COAP request multiplexer. It matches the
// zone name of each incoming request against a list of
// registered patterns add calls the handler for the pattern
// that most closely matches the zone name. ServeMux is COAPSEC aware, meaning
// that queries for the DS record are redirected to the parent zone (if that
// is also registered), otherwise the child gets the query.
// ServeMux is also safe for concurrent access from multiple goroutines.
type ServeMux struct {
	z map[string]muxEntry
	m *sync.RWMutex
}

type muxEntry struct {
	h       Handler
	pattern string
}

// NewServeMux allocates and returns a new ServeMux.
func NewServeMux() *ServeMux { return &ServeMux{z: make(map[string]muxEntry), m: new(sync.RWMutex)} }

// DefaultServeMux is the default ServeMux used by Serve.
var DefaultServeMux = NewServeMux()

// The HandlerFunc type is an adapter to allow the use of
// ordinary functions as COAP handlers.  If f is a function
// with the appropriate signature, HandlerFunc(f) is a
// Handler object that calls f.
type HandlerFunc func(ResponseWriter, Message)

// ServeCOAP calls f(w, r).
func (f HandlerFunc) ServeCOAP(w ResponseWriter, r Message) {
	f(w, r)
}

// HandleFailed returns a HandlerFunc that returns NotFound for every request it gets.
func HandleFailed(w ResponseWriter, req Message) {
	if req.IsConfirmable() {
		msg := w.NewMessage(MessageParams{
			Type:      Acknowledgement,
			Code:      NotFound,
			MessageID: req.MessageID(),
		})
		w.WriteMsg(msg)
	}
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
func ActivateAndServe(l net.Listener, p net.PacketConn, handler Handler) error {
	server := &Server{Listener: l, PacketConn: p, Handler: handler}
	return server.ActivateAndServe()
}

// Does path match pattern?
func pathMatch(pattern, path string) bool {
	if len(pattern) == 0 {
		// should not happen
		return false
	}
	n := len(pattern)
	if pattern[n-1] != '/' {
		return pattern == path
	}
	return len(path) >= n && path[0:n] == pattern
}

// Find a handler on a handler map given a path string
// Most-specific (longest) pattern wins
func (mux *ServeMux) match(path string) (h Handler, pattern string) {
	mux.m.RLock()
	defer mux.m.RUnlock()
	var n = 0
	for k, v := range mux.z {
		if !pathMatch(k, path) {
			continue
		}
		if h == nil || len(k) > n {
			n = len(k)
			h = v.h
			pattern = v.pattern
		}
	}
	return
}

// Handle adds a handler to the ServeMux for pattern.
func (mux *ServeMux) Handle(pattern string, handler Handler) {
	for pattern != "" && pattern[0] == '/' {
		pattern = pattern[1:]
	}

	if pattern == "" {
		panic("COAP: invalid pattern " + pattern)
	}
	if handler == nil {
		panic("COAP: nil handler")
	}

	mux.m.Lock()
	mux.z[pattern] = muxEntry{h: handler, pattern: pattern}
	mux.m.Unlock()
}

// HandleFunc adds a handler function to the ServeMux for pattern.
func (mux *ServeMux) HandleFunc(pattern string, handler func(ResponseWriter, Message)) {
	mux.Handle(pattern, HandlerFunc(handler))
}

// HandleRemove deregistrars the handler specific for pattern from the ServeMux.
func (mux *ServeMux) HandleRemove(pattern string) {
	if pattern == "" {
		panic("COAP: invalid pattern " + pattern)
	}
	mux.m.Lock()
	delete(mux.z, pattern)
	mux.m.Unlock()
}

// ServeCOAP dispatches the request to the handler whose
// pattern most closely matches the request message. If DefaultServeMux
// is used the correct thing for DS queries is done: a possible parent
// is sought.
// If no handler is found a standard NotFound message is returned
func (mux *ServeMux) ServeCOAP(w ResponseWriter, request Message) {
	h, _ := mux.match(request.PathString())
	if h == nil {
		h = failedHandler()
	}
	h.ServeCOAP(w, request)
}

// Handle registers the handler with the given pattern
// in the DefaultServeMux. The documentation for
// ServeMux explains how patterns are matched.
func Handle(pattern string, handler Handler) { DefaultServeMux.Handle(pattern, handler) }

// HandleRemove deregisters the handle with the given pattern
// in the DefaultServeMux.
func HandleRemove(pattern string) { DefaultServeMux.HandleRemove(pattern) }

// HandleFunc registers the handler function with the given pattern
// in the DefaultServeMux.
func HandleFunc(pattern string, handler func(ResponseWriter, Message)) {
	DefaultServeMux.HandleFunc(pattern, handler)
}

// A Server defines parameters for running an COAP server.
type Server struct {
	// Address to listen on, ":COAP" if empty.
	Addr string
	// if "tcp" or "tcp-tls" (COAP over TLS) it will invoke a TCP listener, otherwise an UDP one
	Net string
	// TCP Listener to use, this is to aid in systemd's socket activation.
	Listener net.Listener
	// TLS connection configuration
	TLSConfig *tls.Config
	// UDP "Listener" to use, this is to aid in systemd's socket activation.
	PacketConn net.PacketConn
	// Handler to invoke, COAP.DefaultServeMux if nil.
	Handler Handler
	// Default buffer size to use to read incoming UDP messages. If not set
	// it defaults to 1500 B.
	UDPSize int
	// The net.Conn.SetReadTimeout value for new connections, defaults to 2 * time.Second.
	ReadTimeout time.Duration
	// The net.Conn.SetWriteTimeout value for new connections, defaults to 2 * time.Second.
	WriteTimeout time.Duration
	// If NotifyStartedFunc is set it is called once the server has started listening.
	NotifyStartedFunc func()

	TCPReadBufferSize  int
	TCPWriteBufferSize int

	readerPool sync.Pool
	writerPool sync.Pool
	// UDP packet or TCP connection queue
	queue chan *requestCtx
	// Workers count
	workersCount int32
	// Shutdown handling
	lock    sync.RWMutex
	started bool
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

func (srv *Server) worker(w *requestCtx) {
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

func (srv *Server) spawnWorker(w *requestCtx) {
	select {
	case srv.queue <- w:
	default:
		go srv.worker(w)
	}
}

// ListenAndServe starts a coapserver on the configured address in *Server.
func (srv *Server) ListenAndServe() error {
	srv.lock.Lock()
	if srv.started {
		srv.lock.Unlock()
		return errors.New("server already started")
	}

	addr := srv.Addr
	if addr == "" {
		addr = ":domain"
	}
	if srv.UDPSize == 0 {
		srv.UDPSize = maxPktLen
	}

	srv.queue = make(chan *requestCtx)
	defer close(srv.queue)

	switch srv.Net {
	case "tcp", "tcp4", "tcp6":
		a, err := net.ResolveTCPAddr(srv.Net, addr)
		if err != nil {
			srv.lock.Unlock()
			return err
		}
		l, err := net.ListenTCP(srv.Net, a)
		if err != nil {
			srv.lock.Unlock()
			return err
		}
		srv.Listener = l
		srv.started = true
		srv.lock.Unlock()
		return srv.serveTCP(l)
	case "tcp-tls", "tcp4-tls", "tcp6-tls":
		network := "tcp"
		if srv.Net == "tcp4-tls" {
			network = "tcp4"
		} else if srv.Net == "tcp6-tls" {
			network = "tcp6"
		}

		l, err := tls.Listen(network, addr, srv.TLSConfig)
		if err != nil {
			srv.lock.Unlock()
			return err
		}
		srv.Listener = l
		srv.started = true
		srv.lock.Unlock()
		return srv.serveTCP(l)
	case "udp", "udp4", "udp6":
		a, err := net.ResolveUDPAddr(srv.Net, addr)
		if err != nil {
			srv.lock.Unlock()
			return err
		}
		l, err := net.ListenUDP(srv.Net, a)
		if err != nil {
			srv.lock.Unlock()
			return err
		}
		if err := setUDPSocketOptions(l); err != nil {
			srv.lock.Unlock()
			return err
		}
		srv.PacketConn = l
		srv.started = true
		srv.lock.Unlock()
		return srv.serveUDP(l)
	}
	srv.lock.Unlock()
	return errors.New("bad network")
}

// ActivateAndServe starts a coapserver with the PacketConn or Listener
// configured in *Server. Its main use is to start a server from systemd.
func (srv *Server) ActivateAndServe() error {
	srv.lock.Lock()
	if srv.started {
		srv.lock.Unlock()
		return errors.New("server already started")
	}

	pConn := srv.PacketConn
	l := srv.Listener
	srv.queue = make(chan *requestCtx)
	defer close(srv.queue)

	if pConn != nil {
		if srv.UDPSize == 0 {
			srv.UDPSize = maxPktLen
		}
		// Check PacketConn interface's type is valid and value
		// is not nil
		if t, ok := pConn.(*net.UDPConn); ok && t != nil {
			if e := setUDPSocketOptions(t); e != nil {
				return e
			}
			srv.started = true
			srv.lock.Unlock()
			return srv.serveUDP(t)
		}
	}
	if l != nil {
		srv.started = true
		srv.lock.Unlock()
		return srv.serveTCP(l)
	}
	srv.lock.Unlock()
	return errors.New("bad listeners")
}

// Shutdown shuts down a server. After a call to Shutdown, ListenAndServe and
// ActivateAndServe will return.
func (srv *Server) Shutdown() error {
	srv.lock.Lock()
	if !srv.started {
		srv.lock.Unlock()
		return errors.New("server not started")
	}
	srv.started = false
	srv.lock.Unlock()

	if srv.PacketConn != nil {
		srv.PacketConn.Close()
	}
	if srv.Listener != nil {
		srv.Listener.Close()
	}
	return nil
}

// getReadTimeout is a helper func to use system timeout if server did not intend to change it.
func (srv *Server) getReadTimeout() time.Duration {
	rtimeout := coapTimeout
	if srv.ReadTimeout != 0 {
		rtimeout = srv.ReadTimeout
	}
	return rtimeout
}

// getReadTimeout is a helper func to use system timeout if server did not intend to change it.
func (srv *Server) getWriteTimeout() time.Duration {
	wtimeout := coapTimeout
	if srv.WriteTimeout != 0 {
		wtimeout = srv.WriteTimeout
	}
	return wtimeout
}

// serveTCP starts a TCP listener for the server.
func (srv *Server) serveTCP(l net.Listener) error {
	defer l.Close()

	if srv.NotifyStartedFunc != nil {
		srv.NotifyStartedFunc()
	}

	doneDescChan := make(chan bool)
	numRunningDesc := 0

	rtimeout := srv.getReadTimeout()

	for {
	LOOP_CLOSE_CHANNEL:
		for {
			select {
			case <-doneDescChan:
				numRunningDesc--
			default:
				break LOOP_CLOSE_CHANNEL
			}
		}

		rw, err := l.Accept()
		srv.lock.RLock()
		if !srv.started {
			srv.lock.RUnlock()
			if rw != nil {
				rw.Close()
			}
			for numRunningDesc > 0 {
				<-doneDescChan
				numRunningDesc--
			}
			return nil
		}
		srv.lock.RUnlock()
		if err != nil {
			if neterr, ok := err.(net.Error); ok && neterr.Temporary() {
				continue
			}
			return err
		}

		numRunningDesc++
		go func() {
			rw.SetReadDeadline(time.Now().Add(rtimeout))
			br := srv.acquireReader(rw)
			defer srv.releaseReader(br)
			for {
				mti, err := readTcpMsgInfo(br)
				if err != nil {
					return
				}
				o, p, err := readTcpMsgBody(mti, br)
				if err != nil {
					return
				}
				msg := TcpMessage{MessageBase{}}

				msg.fill(mti, o, p)

				// We will block poller wait loop when
				// all pool workers are busy.

				srv.spawnWorker(&requestCtx{tcp: rw, s: srv, request: &msg})
			}
		}()
	}
}

// serveUDP starts a UDP listener for the server.
func (srv *Server) serveUDP(l *net.UDPConn) error {
	defer l.Close()

	if srv.NotifyStartedFunc != nil {
		srv.NotifyStartedFunc()
	}

	rtimeout := srv.getReadTimeout()
	// deadline is not used here
	for {
		m, s, err := srv.readUDP(l, rtimeout)
		srv.lock.RLock()
		if !srv.started {
			srv.lock.RUnlock()
			return nil
		}
		srv.lock.RUnlock()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Temporary() {
				continue
			}
			return err
		}
		//TODO: check fortruncated
		/*
			if len(m) < headerSize {
				continue
			}
		*/
		msg, err := ParseDgramMessage(m)
		if err != nil {
			continue
		}
		srv.spawnWorker(&requestCtx{request: msg, udp: l, udpSession: s, s: srv})
	}
}

func (srv *Server) serve(w *requestCtx) {
	srv.serveCOAP(w)
}

func (srv *Server) serveCOAP(w *requestCtx) {
	handler := srv.Handler
	if handler == nil {
		handler = DefaultServeMux
	}

	handler.ServeCOAP(w, w.request) // Writes back to the client
}

func (srv *Server) readUDP(conn *net.UDPConn, timeout time.Duration) ([]byte, *SessionUDP, error) {
	conn.SetReadDeadline(time.Now().Add(timeout))
	m := make([]byte, srv.UDPSize)
	n, s, err := ReadFromSessionUDP(conn, m)
	if err != nil {
		return nil, nil, err
	}
	return m[:n], s, nil
}

// WriteMsg implements the ResponseWriter.WriteMsg method.
func (w *requestCtx) WriteMsg(m Message) (err error) {
	data, err := m.MarshalBinary()
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// Write implements the ResponseWriter.Write method.
func (w *requestCtx) Write(m []byte) (int, error) {
	writeTimeout := w.s.getWriteTimeout()
	switch {
	case w.udp != nil:
		w.udp.SetWriteDeadline(time.Now().Add(writeTimeout))
		n, err := WriteToSessionUDP(w.udp, m, w.udpSession)
		return n, err
	case w.tcp != nil:
		w.tcp.SetWriteDeadline(time.Now().Add(writeTimeout))
		wr := acquireWriter(w)
		n, err := wr.Write(m)
		wr.Flush()
		releaseWriter(w.s, wr)
		return n, err
	}
	panic("not reached")
}

// LocalAddr implements the ResponseWriter.LocalAddr method.
func (w *requestCtx) LocalAddr() net.Addr {
	if w.tcp != nil {
		return w.tcp.LocalAddr()
	}
	return w.udp.LocalAddr()
}

// RemoteAddr implements the ResponseWriter.RemoteAddr method.
func (w *requestCtx) RemoteAddr() net.Addr {
	if w.tcp != nil {
		return w.tcp.RemoteAddr()
	}
	return w.udpSession.RemoteAddr()
}

// Close implements the ResponseWriter.Close method
func (w *requestCtx) Close() error {
	// Can't close the udp conn, as that is actually the listener.
	if w.tcp != nil {
		e := w.tcp.Close()
		w.tcp = nil
		return e
	}
	return nil
}

// NewMessage Create message for response
func (w *requestCtx) NewMessage(p MessageParams) Message {
	if w.IsTCP() {
		return NewTcpMessage(p)
	}
	return NewDgramMessage(p)
}

// Close implements the ResponseWriter.Close method
func (w *requestCtx) IsTCP() bool {
	if w.tcp != nil {
		return true
	}
	return false
}

func (s *Server) acquireReader(tcp net.Conn) *bufio.Reader {
	v := s.readerPool.Get()
	if v == nil {
		n := s.TCPReadBufferSize
		if n <= 0 {
			n = defaultReadBufferSize
		}
		return bufio.NewReaderSize(tcp, n)
	}
	r := v.(*bufio.Reader)
	r.Reset(tcp)
	return r
}

func (s *Server) releaseReader(r *bufio.Reader) {
	s.readerPool.Put(r)
}

func acquireWriter(ctx *requestCtx) *bufio.Writer {
	v := ctx.s.writerPool.Get()
	if v == nil {
		n := ctx.s.TCPWriteBufferSize
		if n <= 0 {
			n = defaultWriteBufferSize
		}
		return bufio.NewWriterSize(ctx.tcp, n)
	}
	w := v.(*bufio.Writer)
	w.Reset(ctx.tcp)
	return w
}

func releaseWriter(s *Server, w *bufio.Writer) {
	s.writerPool.Put(w)
}
