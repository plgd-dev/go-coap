package coap

// A client implementation.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	coapNet "github.com/go-ocf/go-coap/net"
	dtls "github.com/pion/dtls/v2"
)

// A ClientConn represents a connection to a COAP server.
type ClientConn struct {
	srv          *Server
	client       *Client
	commander    *ClientCommander
	shutdownSync chan error
	multicast    bool
}

// A Client defines parameters for a COAP client.
type Client struct {
	Net            string        // if "tcp" or "tcp-tls" (COAP over TLS) a TCP query will be initiated, otherwise an UDP one (default is "" for UDP) or "udp-mcast" for multicast
	MaxMessageSize uint32        // Max message size that could be received from peer. If not set it defaults to 1152 B.
	TLSConfig      *tls.Config   // TLS connection configuration
	DTLSConfig     *dtls.Config  // TLS connection configuration
	DialTimeout    time.Duration // set Timeout for dialer
	ReadTimeout    time.Duration // net.ClientConn.SetReadTimeout value for connections, defaults to 1 hour - overridden by Timeout when that value is non-zero
	WriteTimeout   time.Duration // net.ClientConn.SetWriteTimeout value for connections, defaults to 1 hour - overridden by Timeout when that value is non-zero
	HeartBeat      time.Duration // Defines wake up interval from operations Read, Write over connection. defaults is 100ms.

	Handler              HandlerFunc     // default handler for handling messages from server
	NotifySessionEndFunc func(err error) // if NotifySessionEndFunc is set it is called when TCP/UDP session was ended.

	BlockWiseTransfer    *bool         // Use blockWise transfer for transfer payload (default for UDP it's enabled, for TCP it's disable)
	BlockWiseTransferSzx *BlockWiseSzx // Set maximal block size of payload that will be send in fragment

	DisableTCPSignalMessageCSM      bool // Disable send tcp signal CSM message
	DisablePeerTCPSignalMessageCSMs bool // Disable processes Capabilities and Settings Messages from client - iotivity sends max message size without blockwise.
	MulticastHopLimit               int  //sets the hop limit field value for future outgoing multicast packets. default is 2.

	Errors func(err error) // Report errors

	// Keepalive setup
	KeepAlive KeepAlive
}

func (c *Client) readTimeout() time.Duration {
	if c.ReadTimeout != 0 {
		return c.ReadTimeout
	}
	return coapTimeout
}

func (c *Client) writeTimeout() time.Duration {
	if c.WriteTimeout != 0 {
		return c.WriteTimeout
	}
	return coapTimeout
}

func listenUDP(network, address string) (*net.UDPAddr, *net.UDPConn, error) {
	var a *net.UDPAddr
	var err error
	if a, err = net.ResolveUDPAddr(network, address); err != nil {
		return nil, nil, err
	}
	var udpConn *net.UDPConn
	if udpConn, err = net.ListenUDP(network, a); err != nil {
		return nil, nil, err
	}
	return a, udpConn, nil
}

func (c *Client) Dial(address string) (clientConn *ClientConn, err error) {
	ctx := context.Background()
	if c.DialTimeout > 0 {
		ctxTimeout, cancel := context.WithTimeout(context.Background(), c.DialTimeout)
		defer cancel()
		ctx = ctxTimeout
	}

	return c.DialWithContext(ctx, address)
}

func setSessionUDPDataToClient(s networkSession, sessionUDPData *coapNet.ConnUDPContext) {
	switch u := s.(type) {
	case *sessionUDP:
		u.sessionUDPData = sessionUDPData
	case *keepAliveSession:
		setSessionUDPDataToClient(u.networkSession, sessionUDPData)
	case *blockWiseSession:
		setSessionUDPDataToClient(u.networkSession, sessionUDPData)
	}
}

// DialWithContext connects to the address on the named network.
func (c *Client) DialWithContext(ctx context.Context, address string) (clientConn *ClientConn, err error) {
	var conn net.Conn
	var network string
	var sessionUPDData *coapNet.ConnUDPContext

	dialer := &net.Dialer{Timeout: c.DialTimeout}
	BlockWiseTransfer := false
	BlockWiseTransferSzx := BlockWiseSzx1024
	multicast := false
	multicastHop := c.MulticastHopLimit
	if multicastHop == 0 {
		multicastHop = 2
	}

	err = validateKeepAlive(c.KeepAlive)
	if err != nil {
		return nil, fmt.Errorf("keepalive: %w", err)
	}

	switch c.Net {
	case "tcp-tls", "tcp4-tls", "tcp6-tls":
		network = strings.TrimSuffix(c.Net, "-tls")
		conn, err = tls.DialWithDialer(dialer, network, address, c.TLSConfig)
		if err != nil {
			return nil, err
		}
		BlockWiseTransferSzx = BlockWiseSzxBERT
	case "tcp", "tcp4", "tcp6":
		network = c.Net
		conn, err = dialer.DialContext(ctx, c.Net, address)
		if err != nil {
			return nil, err
		}
		BlockWiseTransferSzx = BlockWiseSzxBERT
	case "udp", "udp4", "udp6", "":
		network = c.Net
		if network == "" {
			network = "udp"
		}
		if conn, err = dialer.DialContext(ctx, network, address); err != nil {
			return nil, err
		}
		sessionUPDData = coapNet.NewConnUDPContext(conn.(*net.UDPConn).RemoteAddr().(*net.UDPAddr))
		BlockWiseTransfer = true
	case "udp-dtls", "udp4-dtls", "udp6-dtls":
		network = c.Net
		Net := strings.TrimSuffix(c.Net, "-dtls")
		addr, err := net.ResolveUDPAddr(Net, address)
		if err != nil {
			return nil, fmt.Errorf("cannot resolve udp address: %v", err)
		}
		if conn, err = dtls.Dial(Net, addr, c.DTLSConfig); err != nil {
			return nil, err
		}
		BlockWiseTransfer = true
	case "udp-mcast", "udp4-mcast", "udp6-mcast":
		var err error
		network = strings.TrimSuffix(c.Net, "-mcast")
		multicastAddress, err := net.ResolveUDPAddr(network, address)
		if err != nil {
			return nil, fmt.Errorf("cannot resolve multicast address: %v", err)
		}
		listenAddress, err := net.ResolveUDPAddr(network, "")
		if err != nil {
			return nil, fmt.Errorf("cannot resolve multicast listen address: %v", err)
		}
		udpConn, err := net.ListenUDP(network, listenAddress)
		if err != nil {
			return nil, fmt.Errorf("cannot listen address: %v", err)
		}
		sessionUPDData = coapNet.NewConnUDPContext(multicastAddress)
		conn = udpConn
		BlockWiseTransfer = true
		multicast = true
	default:
		return nil, ErrInvalidNetParameter
	}

	if c.BlockWiseTransfer != nil {
		BlockWiseTransfer = *c.BlockWiseTransfer
	}

	if c.BlockWiseTransferSzx != nil {
		BlockWiseTransferSzx = *c.BlockWiseTransferSzx
	}

	started := make(chan struct{})

	//sync := make(chan bool)
	clientConn = &ClientConn{
		srv: &Server{
			Net:                             network,
			TLSConfig:                       c.TLSConfig,
			Conn:                            conn,
			ReadTimeout:                     c.readTimeout(),
			WriteTimeout:                    c.writeTimeout(),
			MaxMessageSize:                  c.MaxMessageSize,
			BlockWiseTransfer:               &BlockWiseTransfer,
			BlockWiseTransferSzx:            &BlockWiseTransferSzx,
			DisableTCPSignalMessageCSM:      c.DisableTCPSignalMessageCSM,
			DisablePeerTCPSignalMessageCSMs: c.DisablePeerTCPSignalMessageCSMs,
			KeepAlive:                       c.KeepAlive,
			Errors:                          c.Errors,
			NotifyStartedFunc: func() {
				close(started)
			},
			NotifySessionEndFunc: func(s *ClientConn, err error) {
				if c.NotifySessionEndFunc != nil {
					c.NotifySessionEndFunc(err)
				}
			},
			newSessionTCPFunc: func(connection *coapNet.Conn, srv *Server) (networkSession, error) {
				return clientConn.commander.networkSession, nil
			},
			newSessionDTLSFunc: func(connection *coapNet.Conn, srv *Server) (networkSession, error) {
				return clientConn.commander.networkSession, nil
			},
			newSessionUDPFunc: func(connection *coapNet.ConnUDP, srv *Server, sessionUDPData *coapNet.ConnUDPContext) (networkSession, error) {
				if sessionUDPData.RemoteAddr().String() == clientConn.commander.networkSession.RemoteAddr().String() {
					setSessionUDPDataToClient(clientConn.commander.networkSession, sessionUDPData)
					return clientConn.commander.networkSession, nil
				}
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
			},
			Handler: c.Handler,
		},
		shutdownSync: make(chan error, 1),
		multicast:    multicast,
		commander:    &ClientCommander{},
	}

	switch clientConn.srv.Conn.(type) {
	case *net.TCPConn, *tls.Conn:
		session, err := newSessionTCP(coapNet.NewConn(clientConn.srv.Conn, clientConn.srv.heartBeat()), clientConn.srv)
		if err != nil {
			clientConn.srv.Conn.Close()
			return nil, err
		}
		if clientConn.srv.KeepAlive.Enable {
			session = newKeepAliveSession(session, clientConn.srv)
		}
		if session.blockWiseEnabled() {
			clientConn.commander.networkSession = &blockWiseSession{networkSession: session}
		} else {
			clientConn.commander.networkSession = session
		}
	case *dtls.Conn:
		session, err := newSessionDTLS(coapNet.NewConn(clientConn.srv.Conn, clientConn.srv.heartBeat()), clientConn.srv)
		if err != nil {
			clientConn.srv.Conn.Close()
			return nil, err
		}
		if clientConn.srv.KeepAlive.Enable {
			session = newKeepAliveSession(session, clientConn.srv)
		}
		if session.blockWiseEnabled() {
			clientConn.commander.networkSession = &blockWiseSession{networkSession: session}
		} else {
			clientConn.commander.networkSession = session
		}
	case *net.UDPConn:
		session, err := newSessionUDP(coapNet.NewConnUDP(clientConn.srv.Conn.(*net.UDPConn), clientConn.srv.heartBeat(), multicastHop, clientConn.srv.Errors), clientConn.srv, sessionUPDData)
		if err != nil {
			clientConn.srv.Conn.Close()
			return nil, err
		}
		if clientConn.srv.KeepAlive.Enable {
			session = newKeepAliveSession(session, clientConn.srv)
		}
		if session.blockWiseEnabled() {
			clientConn.commander.networkSession = &blockWiseSession{networkSession: session}
		} else {
			clientConn.commander.networkSession = session
		}
	default:
		clientConn.srv.Conn.Close()
		return nil, fmt.Errorf("unknown connection type %T", clientConn.srv.Conn)
	}

	go func() {
		err := clientConn.srv.ActivateAndServe()
		select {
		case clientConn.shutdownSync <- err:
		}
	}()
	clientConn.client = c

	select {
	case <-started:
	case err := <-clientConn.shutdownSync:
		clientConn.srv.Conn.Close()
		return nil, err
	}

	return clientConn, nil
}

func (co *ClientConn) networkSession() networkSession {
	return co.commander.networkSession
}

// LocalAddr implements the networkSession.LocalAddr method.
func (co *ClientConn) LocalAddr() net.Addr {
	return co.commander.LocalAddr()
}

// RemoteAddr implements the networkSession.RemoteAddr method.
func (co *ClientConn) RemoteAddr() net.Addr {
	return co.commander.RemoteAddr()
}

// PeerCertificates implements the networkSession.PeerCertificates method.
func (co *ClientConn) PeerCertificates() []*x509.Certificate {
	return co.commander.PeerCertificates()
}

func (co *ClientConn) Exchange(m Message) (Message, error) {
	return co.commander.ExchangeWithContext(context.Background(), m)
}

// ExchangeContext performs a synchronous query. It sends the message m to the address
// contained in a and waits for a reply.
//
// ExchangeContext does not retry a failed query, nor will it fall back to TCP in
// case of truncation.
// To specify a local address or a timeout, the caller has to set the `Client.Dialer`
// attribute appropriately
func (co *ClientConn) ExchangeWithContext(ctx context.Context, m Message) (Message, error) {
	if co.multicast {
		return nil, ErrNotSupported
	}
	return co.commander.ExchangeWithContext(ctx, m)
}

// NewMessage Create message for request
func (co *ClientConn) NewMessage(p MessageParams) Message {
	return co.commander.NewMessage(p)
}

// NewGetRequest creates get request
func (co *ClientConn) NewGetRequest(path string) (Message, error) {
	return co.commander.NewGetRequest(path)
}

// NewPostRequest creates post request
func (co *ClientConn) NewPostRequest(path string, contentFormat MediaType, body io.Reader) (Message, error) {
	return co.commander.NewPostRequest(path, contentFormat, body)
}

// NewPutRequest creates put request
func (co *ClientConn) NewPutRequest(path string, contentFormat MediaType, body io.Reader) (Message, error) {
	return co.commander.NewPutRequest(path, contentFormat, body)
}

// NewDeleteRequest creates delete request
func (co *ClientConn) NewDeleteRequest(path string) (Message, error) {
	return co.commander.NewDeleteRequest(path)
}

func (co *ClientConn) WriteMsg(m Message) error {
	return co.commander.WriteMsgWithContext(context.Background(), m)
}

// WriteContextMsg sends direct a message through the connection
func (co *ClientConn) WriteMsgWithContext(ctx context.Context, m Message) error {
	return co.commander.WriteMsgWithContext(ctx, m)
}

// Ping send a ping message and wait for a pong response
func (co *ClientConn) Ping(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return co.PingWithContext(ctx)
}

// Ping send a ping message and wait for a pong response
func (co *ClientConn) PingWithContext(ctx context.Context) error {
	return co.commander.PingWithContext(ctx)
}

// GetContext retrieve the resource identified by the request path
func (co *ClientConn) Get(path string) (Message, error) {
	return co.GetWithContext(context.Background(), path)
}

func (co *ClientConn) GetWithContext(ctx context.Context, path string) (Message, error) {
	if co.multicast {
		return nil, ErrNotSupported
	}
	return co.commander.GetWithContext(ctx, path)
}

func (co *ClientConn) Post(path string, contentFormat MediaType, body io.Reader) (Message, error) {
	return co.PostWithContext(context.Background(), path, contentFormat, body)
}

// Post update the resource identified by the request path
func (co *ClientConn) PostWithContext(ctx context.Context, path string, contentFormat MediaType, body io.Reader) (Message, error) {
	if co.multicast {
		return nil, ErrNotSupported
	}
	return co.commander.PostWithContext(ctx, path, contentFormat, body)
}

func (co *ClientConn) Put(path string, contentFormat MediaType, body io.Reader) (Message, error) {
	return co.PutWithContext(context.Background(), path, contentFormat, body)
}

// PutContext create the resource identified by the request path
func (co *ClientConn) PutWithContext(ctx context.Context, path string, contentFormat MediaType, body io.Reader) (Message, error) {
	if co.multicast {
		return nil, ErrNotSupported
	}
	return co.commander.PutWithContext(ctx, path, contentFormat, body)
}

func (co *ClientConn) Delete(path string) (Message, error) {
	return co.DeleteWithContext(context.Background(), path)
}

// Delete delete the resource identified by the request path
func (co *ClientConn) DeleteWithContext(ctx context.Context, path string) (Message, error) {
	if co.multicast {
		return nil, ErrNotSupported
	}
	return co.commander.DeleteWithContext(ctx, path)
}

func (co *ClientConn) Observe(path string, observeFunc func(req *Request)) (*Observation, error) {
	return co.ObserveWithContext(context.Background(), path, observeFunc)
}

func (co *ClientConn) ObserveWithContext(
	ctx context.Context,
	path string,
	observeFunc func(req *Request),
	options ...func(Message),
) (*Observation, error) {
	if co.multicast {
		return nil, ErrNotSupported
	}
	return co.commander.ObserveWithContext(ctx, path, observeFunc, options...)
}

// Close close connection
func (co *ClientConn) Close() error {
	var err error
	if co.srv != nil {
		err = co.srv.Shutdown()
	} else {
		err = co.commander.Close()
	}
	if err != nil {
		return err
	}
	if co.shutdownSync != nil {
		err = <-co.shutdownSync
	}
	return err
}

// Sequence discontinuously unique growing number for connection.
func (co *ClientConn) Sequence() uint64 {
	return co.commander.Sequence()
}

// Dial connects to the address on the named network.
func Dial(network, address string) (*ClientConn, error) {
	client := Client{Net: network}
	return client.DialWithContext(context.Background(), address)
}

// DialTimeout acts like Dial but takes a timeout.
func DialTimeout(network, address string, timeout time.Duration) (*ClientConn, error) {
	client := Client{Net: network, DialTimeout: timeout}
	return client.DialWithContext(context.Background(), address)
}

func fixNetTLS(network string) string {
	if !strings.HasSuffix(network, "-tls") {
		network += "-tls"
	}
	return network
}

func fixNetDTLS(network string) string {
	if !strings.HasSuffix(network, "-dtls") {
		network += "-dtls"
	}
	return network
}

// DialTLS connects to the address on the named network with TLS.
func DialTLS(network, address string, tlsConfig *tls.Config) (conn *ClientConn, err error) {
	client := Client{Net: fixNetTLS(network), TLSConfig: tlsConfig}
	return client.DialWithContext(context.Background(), address)
}

// DialDTLS connects to the address on the named network with DTLS.
func DialDTLS(network, address string, config *dtls.Config) (conn *ClientConn, err error) {
	client := Client{Net: fixNetDTLS(network), DTLSConfig: config}
	return client.DialWithContext(context.Background(), address)
}

// DialTLSWithTimeout acts like DialTLS but takes a timeout.
func DialTLSWithTimeout(network, address string, tlsConfig *tls.Config, timeout time.Duration) (conn *ClientConn, err error) {
	client := Client{Net: fixNetTLS(network), DialTimeout: timeout, TLSConfig: tlsConfig}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return client.DialWithContext(ctx, address)
}

// DialDTLSWithTimeout acts like DialwriteDeadlineDTLS but takes a timeout.
func DialDTLSWithTimeout(network, address string, config *dtls.Config, timeout time.Duration) (conn *ClientConn, err error) {
	client := Client{Net: fixNetDTLS(network), DialTimeout: timeout, DTLSConfig: config}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return client.DialWithContext(ctx, address)
}
