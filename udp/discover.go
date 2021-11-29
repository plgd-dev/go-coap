package udp

import (
	"context"
	"fmt"
	"net"

	"github.com/plgd-dev/go-coap/v2/udp/client"
	"github.com/plgd-dev/go-coap/v2/udp/message"
	"github.com/plgd-dev/go-coap/v2/udp/message/pool"
)

var defaultMulticastOptions = multicastOptions{
	hopLimit: 2,
}

type multicastOptions struct {
	hopLimit int
}

// A MulticastOption sets options such as hop limit, etc.
type MulticastOption interface {
	apply(*multicastOptions)
}

// Discover sends GET to multicast or unicast address and waits for responses until context timeouts or server shutdown.
// For unicast there is a difference against the Dial. The Dial is connection-oriented and it means that, if you send a request to an address, the peer must send the response from the same
// address where was request sent. For Discover it allows the client to send a response from another address where was request send.
func (s *Server) Discover(ctx context.Context, address, path string, receiverFunc func(cc *client.ClientConn, resp *pool.Message), opts ...MulticastOption) error {
	req, err := client.NewGetRequest(ctx, s.messagePool, path)
	if err != nil {
		return fmt.Errorf("cannot create discover request: %w", err)
	}
	req.SetMessageID(s.getMID())
	req.SetType(message.NonConfirmable)
	defer s.messagePool.ReleaseMessage(req)
	return s.DiscoveryRequest(req, address, receiverFunc, opts...)
}

// DiscoveryRequest sends request to multicast/unicast address and wait for responses until request timeouts or server shutdown.
// For unicast there is a difference against the Dial. The Dial is connection-oriented and it means that, if you send a request to an address, the peer must send the response from the same
// address where was request sent. For Discover it allows the client to send a response from another address where was request send.
func (s *Server) DiscoveryRequest(req *pool.Message, address string, receiverFunc func(cc *client.ClientConn, resp *pool.Message), opts ...MulticastOption) error {
	token := req.Token()
	if len(token) == 0 {
		return fmt.Errorf("invalid token")
	}
	cfg := defaultMulticastOptions
	for _, o := range opts {
		o.apply(&cfg)
	}
	c := s.conn()
	if c == nil {
		return fmt.Errorf("server doesn't serve connection")
	}
	addr, err := net.ResolveUDPAddr(c.Network(), address)
	if err != nil {
		return fmt.Errorf("cannot resolve address: %w", err)
	}

	data, err := req.Marshal()
	if err != nil {
		return fmt.Errorf("cannot marshal req: %w", err)
	}
	s.multicastRequests.Store(token.String(), req)
	defer s.multicastRequests.Delete(token.String())
	err = s.multicastHandler.Insert(token, func(w *client.ResponseWriter, r *pool.Message) {
		receiverFunc(w.ClientConn(), r)
	})
	if err != nil {
		return err
	}
	defer s.multicastHandler.Pop(token)

	if addr.IP.IsMulticast() {
		err = c.WriteMulticast(req.Context(), addr, cfg.hopLimit, data)
		if err != nil {
			return err
		}
	} else {
		err = c.WriteWithContext(req.Context(), addr, data)
		if err != nil {
			return err
		}
	}

	select {
	case <-req.Context().Done():
		return nil
	case <-s.ctx.Done():
		return fmt.Errorf("server was closed: %w", s.ctx.Err())
	}
}
