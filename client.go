package coap

// A client implementation.

import (
	"crypto/tls"
	"log"
	"net"
	"strings"
	"time"
)

// A ClientConn represents a connection to a COAP server.
type ClientConn struct {
	srv          *Server
	client       *Client
	session      Session
	shutdownSync chan error
}

// A Client defines parameters for a COAP client.
type Client struct {
	Net          string        // if "tcp" or "tcp-tls" (COAP over TLS) a TCP query will be initiated, otherwise an UDP one (default is "" for UDP)
	UDPSize      uint16        // minimum receive buffer for UDP messages
	TLSConfig    *tls.Config   // TLS connection configuration
	Dialer       *net.Dialer   // a net.Dialer used to set local address, timeouts and more
	ReadTimeout  time.Duration // net.ClientConn.SetReadTimeout value for connections, defaults to 1 hour - overridden by Timeout when that value is non-zero
	WriteTimeout time.Duration // net.ClientConn.SetWriteTimeout value for connections, defaults to 1 hour - overridden by Timeout when that value is non-zero
	SyncTimeout  time.Duration // The maximum of time for synchronization go-routines, defaults to 30 seconds - overridden by Timeout when that value is non-zero if it occurs, then it call log.Fatal

	ObserverFunc HandlerFunc // for handling observation messages from server
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

func (c *Client) syncTimeout() time.Duration {
	if c.SyncTimeout != 0 {
		return c.SyncTimeout
	}
	return syncTimeout
}

// Dial connects to the address on the named network.
func (c *Client) Dial(address string) (clientConn *ClientConn, err error) {
	// create a new dialer with the appropriate timeout
	var d net.Dialer
	if c.Dialer == nil {
		d = net.Dialer{}
	} else {
		d = net.Dialer(*c.Dialer)
	}

	network := "udp"
	useTLS := false

	switch c.Net {
	case "tcp-tls":
		network = "tcp"
		useTLS = true
	case "tcp4-tls":
		network = "tcp4"
		useTLS = true
	case "tcp6-tls":
		network = "tcp6"
		useTLS = true
	default:
		if c.Net != "" {
			network = c.Net
		}
	}

	sync := make(chan bool)
	clientConn = &ClientConn{srv: &Server{Net: network, TLSConfig: c.TLSConfig, ReadTimeout: c.readTimeout(), WriteTimeout: c.writeTimeout(), UDPSize: c.UDPSize,
		NotifyStartedFunc: func() {
			timeout := c.syncTimeout()
			select {
			case sync <- true:
			case <-time.After(timeout):
				log.Fatal("Client cannot send start: Timeout")
			}
		},
		CreateSessionTCPFunc: func(connection conn, srv *Server) Session {
			return clientConn.session
		},
		CreateSessionUDPFunc: func(connection conn, srv *Server, sessionUDPData *SessionUDPData) Session {
			clientConn.session.(*sessionUDP).sessionUDPData = sessionUDPData
			return clientConn.session
		}, Handler: c.ObserverFunc},
		shutdownSync: make(chan error)}
	if useTLS {
		clientConn.srv.Conn, err = tls.DialWithDialer(&d, network, address, c.TLSConfig)
	} else {
		clientConn.srv.Conn, err = d.Dial(network, address)
	}
	if err != nil {
		return nil, err
	}
	switch clientConn.srv.Conn.(type) {
	case *net.TCPConn, *tls.Conn:
		clientConn.session = NewSessionTCP(newConnectionTCP(clientConn.srv.Conn, clientConn.srv), clientConn.srv)
	case *net.UDPConn:
		// WriteMsgUDP returns error when addr is filled in SessionUDPData for connected socket
		d := &SessionUDPData{raddr: clientConn.srv.Conn.(*net.UDPConn).RemoteAddr().(*net.UDPAddr)}
		setUDPSocketOptions(clientConn.srv.Conn.(*net.UDPConn))
		clientConn.session = NewSessionUDP(newConnectionUDP(clientConn.srv.Conn.(*net.UDPConn), clientConn.srv), clientConn.srv, d)
	}

	go func() {
		timeout := c.syncTimeout()
		err := clientConn.srv.ActivateAndServe()
		select {
		case clientConn.shutdownSync <- err:
		case <-time.After(timeout):
			log.Fatal("Client cannot send shutdown: Timeout")
		}
	}()
	select {
	case <-sync:
	case <-time.After(c.syncTimeout()):
		log.Fatal("Client cannot recv start: Timeout")
	}

	clientConn.client = c

	return clientConn, nil
}

// Exchange performs a synchronous query. It sends the message m to the address
// contained in a and waits for a reply.
//
// Exchange does not retry a failed query, nor will it fall back to TCP in
// case of truncation.
// To specify a local address or a timeout, the caller has to set the `Client.Dialer`
// attribute appropriately
func (co *ClientConn) Exchange(m Message, timeout time.Duration) (r Message, err error) {
	return co.session.Exchange(m, timeout)
}

// NewMessage Create message for request
func (co *ClientConn) NewMessage(p MessageParams) Message {
	return co.session.NewMessage(p)
}

// WriteMsg sends a message through the connection co.
func (co *ClientConn) WriteMsg(m Message, timeout time.Duration) (err error) {
	return co.session.WriteMsg(m, timeout)
}

// Close close connection
func (co *ClientConn) Close() {
	co.srv.Shutdown()
	select {
	case <-co.shutdownSync:
	case <-time.After(co.client.syncTimeout()):
		log.Fatal("Client cannot recv shutdown: Timeout")
	}
}

// Dial connects to the address on the named network.
func Dial(network, address string) (conn *ClientConn, err error) {
	client := Client{Net: network}
	return client.Dial(address)
}

// DialTimeout acts like Dial but takes a timeout.
func DialTimeout(network, address string, timeout time.Duration) (conn *ClientConn, err error) {
	client := Client{Net: network, Dialer: &net.Dialer{Timeout: timeout}}
	return client.Dial(address)
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
	return client.Dial(address)
}

// DialTimeoutWithTLS acts like DialWithTLS but takes a timeout.
func DialTimeoutWithTLS(network, address string, tlsConfig *tls.Config, timeout time.Duration) (conn *ClientConn, err error) {
	client := Client{Net: fixNetTLS(network), Dialer: &net.Dialer{Timeout: timeout}, TLSConfig: tlsConfig}
	return client.Dial(address)
}
