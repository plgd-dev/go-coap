package coap

// A client implementation.

import (
	"bytes"
	"crypto/tls"
	"net"
	"strings"
	"time"
)

const (
	tcpIdleTimeout time.Duration = 8 * time.Second
)

// A Conn represents a connection to a COAP server.
type Conn struct {
	net.Conn        // a net.Conn holding the connection
	UDPSize  uint16 // minimum receive buffer for UDP messages
	TCPbuf   []byte
	client   *Client
}

// A Client defines parameters for a COAP client.
type Client struct {
	Net       string      // if "tcp" or "tcp-tls" (COAP over TLS) a TCP query will be initiated, otherwise an UDP one (default is "" for UDP)
	UDPSize   uint16      // minimum receive buffer for UDP messages
	TLSConfig *tls.Config // TLS connection configuration
	Dialer    *net.Dialer // a net.Dialer used to set local address, timeouts and more
	// Timeout is a cumulative timeout for dial, write and read, defaults to 0 (disabled) - overrides DialTimeout, ReadTimeout,
	// WriteTimeout when non-zero. Can be overridden with net.Dialer.Timeout (see Client.ExchangeWithDialer and
	// Client.Dialer) or context.Context.Deadline (see the deprecated ExchangeContext)
	Timeout      time.Duration
	DialTimeout  time.Duration // net.DialTimeout, defaults to 2 seconds, or net.Dialer.Timeout if expiring earlier - overridden by Timeout when that value is non-zero
	ReadTimeout  time.Duration // net.Conn.SetReadTimeout value for connections, defaults to 2 seconds - overridden by Timeout when that value is non-zero
	WriteTimeout time.Duration // net.Conn.SetWriteTimeout value for connections, defaults to 2 seconds - overridden by Timeout when that value is non-zero
}

func (c *Client) dialTimeout() time.Duration {
	if c.Timeout != 0 {
		return c.Timeout
	}
	if c.DialTimeout != 0 {
		return c.DialTimeout
	}
	return coapTimeout
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

// Dial connects to the address on the named network.
func (c *Client) Dial(address string) (conn *Conn, err error) {
	// create a new dialer with the appropriate timeout
	var d net.Dialer
	if c.Dialer == nil {
		d = net.Dialer{}
	} else {
		d = net.Dialer(*c.Dialer)
	}
	d.Timeout = c.getTimeoutForRequest(c.writeTimeout())

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

	conn = new(Conn)
	if useTLS {
		conn.Conn, err = tls.DialWithDialer(&d, network, address, c.TLSConfig)
	} else {
		conn.Conn, err = d.Dial(network, address)
	}
	if err != nil {
		return nil, err
	}
	conn.client = c
	return conn, nil
}

// Exchange performs a synchronous query. It sends the message m to the address
// contained in a and waits for a reply. Basic use pattern with a *coap.Client:
//
//	c := new(coap.Client)
//	in, rtt, err := c.Exchange(message, "127.0.0.1:53")
//
// Exchange does not retry a failed query, nor will it fall back to TCP in
// case of truncation.
// To specify a local address or a timeout, the caller has to set the `Client.Dialer`
// attribute appropriately
func (co *Conn) Exchange(m Message) (r Message, rtt time.Duration, err error) {
	t := time.Now()
	// write with the appropriate write timeout

	if err = co.WriteMsg(m, co.client.writeTimeout()); err != nil {
		return nil, 0, err
	}

	r, err = co.ReadMsg(co.client.readTimeout())
	rtt = time.Since(t)
	return r, rtt, err
}

// NewMessage Create message for request
func (co *Conn) NewMessage(p MessageParams) Message {
	switch co.Conn.(type) {
	case *net.TCPConn, *tls.Conn:
		return NewTcpMessage(p)
	}
	return NewDgramMessage(p)
}

// ReadMsg reads a message from the connection co.
func (co *Conn) ReadMsg(timeout time.Duration) (Message, error) {
	var m []byte
	var err error

	co.SetReadDeadline(time.Now().Add(timeout))
	for {
		m, err = co.Read()
		if err == ErrShortRead {
			continue
		} else if err != nil {
			return nil, err
		}
		break
	}
	switch co.Conn.(type) {
	case *net.TCPConn, *tls.Conn:
		resp, _, err := PullTcp(m)
		return resp, err
	}
	// UDP connection
	return ParseDgramMessage(m)
}

func tcpReadBuf(conn *Conn) ([]byte, error) {
	r := bytes.NewReader(conn.TCPbuf)
	mti, err := readTcpMsgInfo(r)
	if err != nil {
		return nil, ErrShortRead
	}
	if len(conn.TCPbuf) < mti.totLen {
		return nil, ErrShortRead
	}
	buf := conn.TCPbuf[:mti.totLen]
	conn.TCPbuf = conn.TCPbuf[mti.totLen:]
	return buf, nil
}

// tcpRead calls TCPConn.Read enough times to fill allocated buffer.
func tcpRead(conn *Conn) ([]byte, error) {
	m := make([]byte, maxPktLen)
	n, err := conn.Conn.Read(m)
	if err != nil {
		return nil, err
	}
	m = m[:n]
	conn.TCPbuf = append(conn.TCPbuf, m...)
	return tcpReadBuf(conn)
}

// Read implements the net.Conn read method.
func (co *Conn) Read() ([]byte, error) {
	switch co.Conn.(type) {
	case *net.TCPConn, *tls.Conn:
		m, err := tcpReadBuf(co)
		if err == nil {
			return m, nil
		}
		return tcpRead(co)
	}
	// UDP connection
	m := make([]byte, maxPktLen)
	n, err := co.Conn.Read(m)
	if err != nil {
		return nil, err
	}
	return m[:n], err
}

// WriteMsg sends a message through the connection co.
func (co *Conn) WriteMsg(m Message, timeout time.Duration) (err error) {
	var data []byte
	data, err = m.MarshalBinary()
	if err != nil {
		return err
	}
	co.SetWriteDeadline(time.Now().Add(timeout))
	_, err = co.Write(data)
	return err
}

// Write implements the net.Conn Write method.
func (co *Conn) Write(p []byte) (n int, err error) {
	return co.Conn.Write(p)
}

// Return the appropriate timeout for a specific request
func (c *Client) getTimeoutForRequest(timeout time.Duration) time.Duration {
	var requestTimeout time.Duration
	if c.Timeout != 0 {
		requestTimeout = c.Timeout
	} else {
		requestTimeout = timeout
	}
	// net.Dialer.Timeout has priority if smaller than the timeouts computed so
	// far
	if c.Dialer != nil && c.Dialer.Timeout != 0 && c.Dialer.Timeout < requestTimeout {
		requestTimeout = c.Dialer.Timeout
	}
	return requestTimeout
}

// Dial connects to the address on the named network.
func Dial(network, address string) (conn *Conn, err error) {
	client := Client{Net: network}
	return client.Dial(address)
}

// DialTimeout acts like Dial but takes a timeout.
func DialTimeout(network, address string, timeout time.Duration) (conn *Conn, err error) {
	client := Client{Net: network, Dialer: &net.Dialer{Timeout: timeout}}
	return client.Dial(address)
}

// DialWithTLS connects to the address on the named network with TLS.
func DialWithTLS(network, address string, tlsConfig *tls.Config) (conn *Conn, err error) {
	if !strings.HasSuffix(network, "-tls") {
		network += "-tls"
	}
	client := Client{Net: network, TLSConfig: tlsConfig}
	return client.Dial(address)
}

// DialTimeoutWithTLS acts like DialWithTLS but takes a timeout.
func DialTimeoutWithTLS(network, address string, tlsConfig *tls.Config, timeout time.Duration) (conn *Conn, err error) {
	if !strings.HasSuffix(network, "-tls") {
		network += "-tls"
	}
	client := Client{Net: network, Dialer: &net.Dialer{Timeout: timeout}, TLSConfig: tlsConfig}
	return client.Dial(address)
}
