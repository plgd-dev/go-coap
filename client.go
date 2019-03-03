package coap

// A client implementation.

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"strings"
	"time"
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
	DialTimeout    time.Duration // set Timeout for dialer
	ReadTimeout    time.Duration // net.ClientConn.SetReadTimeout value for connections, defaults to 1 hour - overridden by Timeout when that value is non-zero
	WriteTimeout   time.Duration // net.ClientConn.SetWriteTimeout value for connections, defaults to 1 hour - overridden by Timeout when that value is non-zero
	HeartBeat      time.Duration // Defines wake up interval from operations Read, Write over connection. defaults is 100ms.

	Handler              HandlerFunc     // default handler for handling messages from server
	NotifySessionEndFunc func(err error) // if NotifySessionEndFunc is set it is called when TCP/UDP session was ended.

	BlockWiseTransfer    *bool         // Use blockWise transfer for transfer payload (default for UDP it's enabled, for TCP it's disable)
	BlockWiseTransferSzx *BlockWiseSzx // Set maximal block size of payload that will be send in fragment

	DisableTCPSignalMessages bool // Disable tcp signal messages
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
	if err := setUDPSocketOptions(udpConn); err != nil {
		return nil, nil, err
	}
	return a, udpConn, nil
}

func (c *Client) Dial(address string) (clientConn *ClientConn, err error) {
	return c.DialContext(context.Background(), address)
}

// DialContext connects to the address on the named network.
func (c *Client) DialContext(ctx context.Context, address string) (clientConn *ClientConn, err error) {

	var conn net.Conn
	var network string
	var sessionUPDData *SessionUDPData

	dialer := &net.Dialer{Timeout: c.DialTimeout}
	BlockWiseTransfer := false
	BlockWiseTransferSzx := BlockWiseSzx1024
	multicast := false

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
		conn, err = dialer.Dial(c.Net, address)
		if err != nil {
			return nil, err
		}
		BlockWiseTransferSzx = BlockWiseSzxBERT
	case "udp", "udp4", "udp6", "":
		network = c.Net
		if network == "" {
			network = "udp"
		}
		if conn, err = dialer.Dial(network, address); err != nil {
			return nil, err
		}
		sessionUPDData = &SessionUDPData{raddr: conn.(*net.UDPConn).RemoteAddr().(*net.UDPAddr)}
		BlockWiseTransfer = true
	case "udp-mcast", "udp4-mcast", "udp6-mcast":
		network = strings.TrimSuffix(c.Net, "-mcast")
		a, udpConn, err := listenUDP(network, address)
		if err != nil {
			return nil, err
		}
		sessionUPDData = &SessionUDPData{raddr: a}
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

	//sync := make(chan bool)
	clientConn = &ClientConn{
		srv: &Server{
			Net:                      network,
			TLSConfig:                c.TLSConfig,
			Conn:                     conn,
			ReadTimeout:              c.readTimeout(),
			WriteTimeout:             c.writeTimeout(),
			MaxMessageSize:           c.MaxMessageSize,
			BlockWiseTransfer:        &BlockWiseTransfer,
			BlockWiseTransferSzx:     &BlockWiseTransferSzx,
			DisableTCPSignalMessages: c.DisableTCPSignalMessages,
			NotifyStartedFunc: func() {
				/*
					timeout := c.syncTimeout()
					select {
					case sync <- true:
					case <-time.After(timeout):
						log.Fatal("Client cannot send start: Timeout")
					}
				*/
			},
			NotifySessionEndFunc: func(s *ClientCommander, err error) {
				if c.NotifySessionEndFunc != nil {
					c.NotifySessionEndFunc(err)
				}
			},
			newSessionTCPFunc: func(ctx context.Context, connection *ConnTCP, srv *Server) (networkSession, error) {
				return clientConn.commander.networkSession, nil
			},
			newSessionUDPFunc: func(ctx context.Context, connection *ConnUDP, srv *Server, sessionUDPData *SessionUDPData) (networkSession, error) {
				if sessionUDPData.RemoteAddr().String() == clientConn.commander.networkSession.RemoteAddr().String() {
					if s, ok := clientConn.commander.networkSession.(*blockWiseSession); ok {
						s.networkSession.(*sessionUDP).sessionUDPData = sessionUDPData
					} else {
						clientConn.commander.networkSession.(*sessionUDP).sessionUDPData = sessionUDPData
					}
					return clientConn.commander.networkSession, nil
				}
				session, err := newSessionUDP(ctx, connection, srv, sessionUDPData)
				if err != nil {
					return nil, err
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
		session, err := newSessionTCP(ctx, NewConnTCP(clientConn.srv.Conn, clientConn.srv.heartBeat()), clientConn.srv)
		if err != nil {
			return nil, err
		}
		if session.blockWiseEnabled() {
			clientConn.commander.networkSession = &blockWiseSession{networkSession: session}
		} else {
			clientConn.commander.networkSession = session
		}
	case *net.UDPConn:
		// WriteContextMsgUDP returns error when addr is filled in SessionUDPData for connected socket
		setUDPSocketOptions(clientConn.srv.Conn.(*net.UDPConn))
		session, err := newSessionUDP(ctx, NewConnUDP(clientConn.srv.Conn.(*net.UDPConn), clientConn.srv.heartBeat()), clientConn.srv, sessionUPDData)
		if err != nil {
			return nil, err
		}
		if session.blockWiseEnabled() {
			clientConn.commander.networkSession = &blockWiseSession{networkSession: session}
		} else {
			clientConn.commander.networkSession = session
		}
	}

	go func() {
		//timeout := c.syncTimeout()
		err := clientConn.srv.ActivateAndServe()
		select {
		case clientConn.shutdownSync <- err:
			/*
				case <-time.After(timeout):
					log.Fatal("Client cannot send shutdown: Timeout")
			*/
		}

	}()
	/*
		select {
		case <-sync:
		case <-time.After(c.syncTimeout()):
			log.Fatal("Client cannot recv start: Timeout")
		}
	*/
	clientConn.client = c

	return clientConn, nil
}

// LocalAddr implements the networkSession.LocalAddr method.
func (co *ClientConn) LocalAddr() net.Addr {
	return co.commander.LocalAddr()
}

// RemoteAddr implements the networkSession.RemoteAddr method.
func (co *ClientConn) RemoteAddr() net.Addr {
	return co.commander.RemoteAddr()
}

func (co *ClientConn) Exchange(m Message) (Message, error) {
	return co.commander.ExchangeContext(context.Background(), m)
}

// ExchangeContext performs a synchronous query. It sends the message m to the address
// contained in a and waits for a reply.
//
// ExchangeContext does not retry a failed query, nor will it fall back to TCP in
// case of truncation.
// To specify a local address or a timeout, the caller has to set the `Client.Dialer`
// attribute appropriately
func (co *ClientConn) ExchangeContext(ctx context.Context, m Message) (Message, error) {
	if co.multicast {
		return nil, ErrNotSupported
	}
	return co.commander.ExchangeContext(ctx, m)
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
	return co.commander.WriteContextMsg(context.Background(), m)
}

// WriteContextMsg sends direct a message through the connection
func (co *ClientConn) WriteContextMsg(ctx context.Context, m Message) error {
	return co.commander.WriteContextMsg(ctx, m)
}

// Ping send a ping message and wait for a pong response
func (co *ClientConn) Ping(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return co.PingContext(ctx)
}

// Ping send a ping message and wait for a pong response
func (co *ClientConn) PingContext(ctx context.Context) error {
	return co.commander.PingContext(ctx)
}

// GetContext retrieve the resource identified by the request path
func (co *ClientConn) Get(path string) (Message, error) {
	return co.GetContext(context.Background(), path)
}

func (co *ClientConn) GetContext(ctx context.Context, path string) (Message, error) {
	if co.multicast {
		return nil, ErrNotSupported
	}
	return co.commander.GetContext(ctx, path)
}

func (co *ClientConn) Post(path string, contentFormat MediaType, body io.Reader) (Message, error) {
	return co.PostContext(context.Background(), path, contentFormat, body)
}

// Post update the resource identified by the request path
func (co *ClientConn) PostContext(ctx context.Context, path string, contentFormat MediaType, body io.Reader) (Message, error) {
	if co.multicast {
		return nil, ErrNotSupported
	}
	return co.commander.PostContext(ctx, path, contentFormat, body)
}

func (co *ClientConn) Put(path string, contentFormat MediaType, body io.Reader) (Message, error) {
	return co.PutContext(context.Background(), path, contentFormat, body)
}

// PutContext create the resource identified by the request path
func (co *ClientConn) PutContext(ctx context.Context, path string, contentFormat MediaType, body io.Reader) (Message, error) {
	if co.multicast {
		return nil, ErrNotSupported
	}
	return co.commander.PutContext(ctx, path, contentFormat, body)
}

func (co *ClientConn) Delete(path string) (Message, error) {
	return co.DeleteContext(context.Background(), path)
}

// Delete delete the resource identified by the request path
func (co *ClientConn) DeleteContext(ctx context.Context, path string) (Message, error) {
	if co.multicast {
		return nil, ErrNotSupported
	}
	return co.commander.DeleteContext(ctx, path)
}

func (co *ClientConn) Observe(path string, observeFunc func(req *Request)) (*Observation, error) {
	return co.ObserveContext(context.Background(), path, observeFunc)
}

func (co *ClientConn) ObserveContext(ctx context.Context, path string, observeFunc func(req *Request)) (*Observation, error) {
	if co.multicast {
		return nil, ErrNotSupported
	}
	return co.commander.ObserveContext(ctx, path, observeFunc)
}

// Close close connection
func (co *ClientConn) Close() error {
	co.srv.Shutdown()
	select {
	case <-co.shutdownSync:
		/*
			case <-time.After(co.client.syncTimeout()):
				log.Fatal("Client cannot recv shutdown: Timeout")
		*/
	}
	return nil
}

// Dial connects to the address on the named network.
func Dial(network, address string) (*ClientConn, error) {
	client := Client{Net: network}
	return client.DialContext(context.Background(), address)
}

// DialTimeout acts like Dial but takes a timeout.
func DialTimeout(network, address string, timeout time.Duration) (*ClientConn, error) {
	client := Client{Net: network, DialTimeout: timeout}
	return client.DialContext(context.Background(), address)
}

func fixNetTLS(network string) string {
	if !strings.HasSuffix(network, "-tls") {
		network += "-tls"
	}
	return network
}

// DialWithTLS connects to the address on the named network with TLS.
func DialWithTLS(network, address string, tlsConfig *tls.Config) (conn *ClientConn, err error) {
	client := Client{Net: fixNetTLS(network), TLSConfig: tlsConfig}
	return client.DialContext(context.Background(), address)
}

// DialTimeoutWithTLS acts like DialWithTLS but takes a timeout.
func DialTimeoutWithTLS(network, address string, tlsConfig *tls.Config, timeout time.Duration) (conn *ClientConn, err error) {
	client := Client{Net: fixNetTLS(network), DialTimeout: timeout, TLSConfig: tlsConfig}
	return client.DialContext(context.Background(), address)
}
