package coap

// A client implementation.

import (
	"context"
	"net"
	"strings"
	"time"

	"github.com/go-ocf/go-coap/codes"
)

// A ClientConn represents a connection to a COAP server.
type MulticastClientConn struct {
	conn   *ClientConn
	client *MulticastClient
}

// A MulticastClient defines parameters for a COAP client.
type MulticastClient struct {
	Net            string        // udp4" / "udp6" / "udp" - don't use udp on apple, because multicast ipv6->ipv4 is not supported.
	MaxMessageSize uint32        // Max message size that could be received from peer. If not set it defaults to 1152 B.
	DialTimeout    time.Duration // set Timeout for dialer
	ReadTimeout    time.Duration // net.ClientConn.SetReadTimeout value for connections, defaults to 1 hour - overridden by Timeout when that value is non-zero
	WriteTimeout   time.Duration // net.ClientConn.SetWriteTimeout value for connections, defaults to 1 hour - overridden by Timeout when that value is non-zero
	HeartBeat      time.Duration // The maximum of time for synchronization go-routines, defaults to 30 seconds - overridden by Timeout when that value is non-zero if it occurs, then it call log.Fatal

	Handler              HandlerFunc     // default handler for handling messages from server
	NotifySessionEndFunc func(err error) // if NotifySessionEndFunc is set it is called when TCP/UDP session was ended.

	BlockWiseTransfer    *bool         // Use blockWise transfer for transfer payload (default for UDP it's enabled, for TCP it's disable)
	BlockWiseTransferSzx *BlockWiseSzx // Set maximal block size of payload that will be send in fragment

	MulticastHopLimit int //sets the hop limit field value for future outgoing multicast packets. default is 2.

	Errors func(err error) // Report errors

	multicastHandler *TokenHandler
}

// Dial connects to the address on the named network.
func (c *MulticastClient) dialNet(ctx context.Context, net, address string) (*ClientConn, error) {
	if c.multicastHandler == nil {
		c.multicastHandler = &TokenHandler{tokenHandlers: make(map[[MaxTokenSize]byte]HandlerFunc)}
	}
	client := &Client{
		Net:            net,
		MaxMessageSize: c.MaxMessageSize,
		DialTimeout:    c.DialTimeout,
		ReadTimeout:    c.ReadTimeout,
		WriteTimeout:   c.WriteTimeout,
		HeartBeat:      c.HeartBeat,
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
		MulticastHopLimit:    c.MulticastHopLimit,
		Errors:               c.Errors,
	}

	return client.DialWithContext(ctx, address)
}

func (c *MulticastClient) Dial(address string) (*MulticastClientConn, error) {
	return c.DialWithContext(context.Background(), address)
}

// DialWithContext connects with context to the address on the named network.
func (c *MulticastClient) DialWithContext(ctx context.Context, address string) (*MulticastClientConn, error) {
	var net string
	switch c.Net {
	case "udp", "udp4", "udp6":
		net = c.Net + "-mcast"
	case "":
		if strings.Contains(address, "[") {
			net = "udp6-mcast"
		} else {
			net = "udp4-mcast"
		}

	default:
		return nil, ErrInvalidNetParameter
	}
	conn, err := c.dialNet(ctx, net, address)
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
	return mconn.WriteMsgWithContext(context.Background(), m)
}

// WriteContextMsg sends a message with context through the connection co.
func (mconn *MulticastClientConn) WriteMsgWithContext(ctx context.Context, m Message) error {
	return mconn.conn.WriteMsgWithContext(ctx, m)
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

// Publish subscribes to sever on path. After subscription and every change on path,
// server sends immediately response
func (mconn *MulticastClientConn) Publish(path string, responseHandler func(req *Request)) (*ResponseWaiter, error) {
	return mconn.PublishWithContext(context.Background(), path, responseHandler)
}

// PublishContext subscribes with context to sever on path. After subscription and every change on path,
// server sends immediately response
func (mconn *MulticastClientConn) PublishWithContext(ctx context.Context, path string, responseHandler func(req *Request)) (*ResponseWaiter, error) {
	req, err := mconn.NewGetRequest(path)
	if err != nil {
		return nil, err
	}
	return mconn.PublishMsgWithContext(ctx, req, responseHandler)
}

// PublishMsg subscribes to sever with GET message. After subscription and every change on path,
// server sends immediately response
func (mconn *MulticastClientConn) PublishMsg(req Message, responseHandler func(req *Request)) (*ResponseWaiter, error) {
	return mconn.PublishMsgWithContext(context.Background(), req, responseHandler)
}

// PublishMsgWithContext subscribes with context to sever with GET message. After subscription and every change on path,
// server sends immediately response
func (mconn *MulticastClientConn) PublishMsgWithContext(ctx context.Context, req Message, responseHandler func(req *Request)) (*ResponseWaiter, error) {
	if req.Code() != codes.GET || req.PathString() == "" {
		return nil, ErrInvalidRequest
	}

	path := req.PathString()

	r := &ResponseWaiter{
		token: req.Token(),
		path:  path,
		conn:  mconn,
	}
	err := mconn.client.multicastHandler.Add(req.Token(), func(w ResponseWriter, r *Request) {
		var err error
		switch r.Msg.Code() {
		case codes.GET, codes.POST, codes.PUT, codes.DELETE:
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
			resp, err = r.Client.GetWithContext(ctx, path)
			if err != nil {
				return
			}
		}
		responseHandler(&Request{Msg: resp, Client: r.Client, Ctx: ctx, Sequence: r.Client.Sequence()})
	})
	if err != nil {
		return nil, err
	}

	err = mconn.WriteMsgWithContext(ctx, req)
	if err != nil {
		mconn.client.multicastHandler.Remove(r.token)
		return nil, err
	}

	return r, nil
}
