package coap

// A client implementation.

import (
	"net"
	"time"
)

// A ClientConn represents a connection to a COAP server.
type MulticastClientConn struct {
	conn   *ClientConn
	client *MulticastClient
}

// A MulticastClient defines parameters for a COAP client.
type MulticastClient struct {
	Net            string        // "udp" / "udp4" / "udp6"
	MaxMessageSize uint32        // Max message size that could be received from peer. If not set it defaults to 1152 B.
	DialTimeout    time.Duration // set Timeout for dialer
	ReadTimeout    time.Duration // net.ClientConn.SetReadTimeout value for connections, defaults to 1 hour - overridden by Timeout when that value is non-zero
	WriteTimeout   time.Duration // net.ClientConn.SetWriteTimeout value for connections, defaults to 1 hour - overridden by Timeout when that value is non-zero
	SyncTimeout    time.Duration // The maximum of time for synchronization go-routines, defaults to 30 seconds - overridden by Timeout when that value is non-zero if it occurs, then it call log.Fatal

	Handler              HandlerFunc     // default handler for handling messages from server
	NotifySessionEndFunc func(err error) // if NotifySessionEndFunc is set it is called when TCP/UDP session was ended.

	BlockWiseTransfer    *bool         // Use blockWise transfer for transfer payload (default for UDP it's enabled, for TCP it's disable)
	BlockWiseTransferSzx *BlockWiseSzx // Set maximal block size of payload that will be send in fragment

	multicastHandler *TokenHandler
}

// Dial connects to the address on the named network.
func (c *MulticastClient) dialNet(net, address string) (*ClientConn, error) {
	if c.multicastHandler == nil {
		c.multicastHandler = &TokenHandler{tokenHandlers: make(map[[MaxTokenSize]byte]HandlerFunc)}
	}
	client := &Client{
		Net:            net,
		MaxMessageSize: c.MaxMessageSize,
		DialTimeout:    c.DialTimeout,
		ReadTimeout:    c.ReadTimeout,
		WriteTimeout:   c.WriteTimeout,
		SyncTimeout:    c.SyncTimeout,
		Handler: func(w ResponseWriter, r *Request) {
			handler := c.Handler
			if handler == nil {
				handler = HandleFailed
			}
			c.multicastHandler.Handle(w, r, handler)
		},
		NotifySessionEndFunc: c.NotifySessionEndFunc,
		BlockWiseTransfer:    c.BlockWiseTransfer,
		BlockWiseTransferSzx: c.BlockWiseTransferSzx,
	}

	return client.Dial(address)
}

// Dial connects to the address on the named network.
func (c *MulticastClient) Dial(address string) (*MulticastClientConn, error) {
	var net string
	switch c.Net {
	case "udp", "udp4", "udp6":
		net = c.Net + "-mcast"
	case "":
		net = "udp-mcast"
	default:
		return nil, ErrInvalidNetParameter
	}
	conn, err := c.dialNet(net, address)
	if err != nil {
		return nil, err
	}
	return &MulticastClientConn{
		conn:   conn,
		client: c,
	}, nil
}

// LocalAddr implements the networkSession.LocalAddr method.
func (mconn *MulticastClientConn) LocalAddr() net.Addr {
	return mconn.conn.LocalAddr()
}

// RemoteAddr implements the networkSession.RemoteAddr method.
func (mconn *MulticastClientConn) RemoteAddr() net.Addr {
	return mconn.conn.RemoteAddr()
}

// NewMessage Create message for request
func (mconn *MulticastClientConn) NewMessage(p MessageParams) Message {
	return mconn.conn.NewMessage(p)
}

// NewGetRequest creates get request
func (mconn *MulticastClientConn) NewGetRequest(path string) (Message, error) {
	return mconn.conn.NewGetRequest(path)
}

// WriteMsg sends a message through the connection co.
func (mconn *MulticastClientConn) WriteMsg(m Message) error {
	return mconn.conn.WriteMsg(m)
}

// SetReadDeadline set read deadline for timeout for Exchange
func (mconn *MulticastClientConn) SetReadDeadline(timeout time.Duration) {
	mconn.conn.SetReadDeadline(timeout)
}

// SetWriteDeadline set write deadline for timeout for Exchange and Write
func (mconn *MulticastClientConn) SetWriteDeadline(timeout time.Duration) {
	mconn.conn.SetWriteDeadline(timeout)
}

// Close close connection
func (mconn *MulticastClientConn) Close() {
	mconn.conn.Close()
}

//ResponseWaiter represents subscription to resource on the server
type ResponseWaiter struct {
	token []byte
	path  string
	conn  *MulticastClientConn
}

// Cancel remove observation from server. For recreate observation use Observe.
func (r *ResponseWaiter) Cancel() error {
	return r.conn.client.multicastHandler.Remove(r.token)
}

// Publish subscribe to sever on path. After subscription and every change on path,
// server sends immediately response
func (mconn *MulticastClientConn) Publish(path string, responseHandler func(req *Request)) (*ResponseWaiter, error) {
	req, err := mconn.conn.NewGetRequest(path)
	if err != nil {
		return nil, err
	}
	r := &ResponseWaiter{
		token: req.Token(),
		path:  path,
		conn:  mconn,
	}
	err = mconn.client.multicastHandler.Add(req.Token(), func(w ResponseWriter, r *Request) {
		var err error
		switch r.Msg.Code() {
		case GET, POST, PUT, DELETE:
			//dont serve commands by multicast handler (filter own request)
			return
		}
		needGet := false
		resp := r.Msg
		if r.Msg.Option(Size2) != nil {
			if len(r.Msg.Payload()) != int(r.Msg.Option(Size2).(uint32)) {
				needGet = true
			}
		}
		if !needGet {
			if block, ok := r.Msg.Option(Block2).(uint32); ok {
				_, _, more, err := UnmarshalBlockOption(block)
				if err != nil {
					return
				}
				needGet = more
			}
		}

		if needGet {
			resp, err = r.Client.Get(path)
			if err != nil {
				return
			}
		}
		responseHandler(&Request{Msg: resp, Client: r.Client})
	})
	if err != nil {
		return nil, err
	}

	err = mconn.WriteMsg(req)
	if err != nil {
		mconn.client.multicastHandler.Remove(r.token)
		return nil, err
	}

	return r, nil
}
