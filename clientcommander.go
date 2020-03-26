package coap

import (
	"bytes"
	"context"
	"crypto/x509"
	"io"
	"io/ioutil"
	"net"
	"time"

	"github.com/go-ocf/go-coap/codes"
)

// ClientCommander provides commands Get,Post,Put,Delete,Observe
// For compare use ClientCommander.Equal
type ClientCommander struct {
	networkSession networkSession
}

// NewMessage creates message for request
func (cc *ClientCommander) NewMessage(p MessageParams) Message {
	return cc.networkSession.NewMessage(p)
}

func (cc *ClientCommander) newGetDeleteRequest(path string, code codes.Code) (Message, error) {
	token, err := GenerateToken()
	if err != nil {
		return nil, err
	}
	req := cc.NewMessage(MessageParams{
		Type:      Confirmable,
		Code:      code,
		MessageID: GenerateMessageID(),
		Token:     token,
	})
	req.SetPathString(path)
	return req, nil
}

func (cc *ClientCommander) newPostPutRequest(path string, contentFormat MediaType, body io.Reader, code codes.Code) (Message, error) {
	token, err := GenerateToken()
	if err != nil {
		return nil, err
	}
	req := cc.networkSession.NewMessage(MessageParams{
		Type:      Confirmable,
		Code:      code,
		MessageID: GenerateMessageID(),
		Token:     token,
	})
	req.SetPathString(path)
	req.SetOption(ContentFormat, contentFormat)
	payload, err := ioutil.ReadAll(body)
	if err != nil {
		return nil, err
	}
	req.SetPayload(payload)
	return req, nil
}

// NewGetRequest creates get request
func (cc *ClientCommander) NewGetRequest(path string) (Message, error) {
	return cc.newGetDeleteRequest(path, codes.GET)
}

// NewPostRequest creates post request
func (cc *ClientCommander) NewPostRequest(path string, contentFormat MediaType, body io.Reader) (Message, error) {
	return cc.newPostPutRequest(path, contentFormat, body, codes.POST)
}

// NewPutRequest creates put request
func (cc *ClientCommander) NewPutRequest(path string, contentFormat MediaType, body io.Reader) (Message, error) {
	return cc.newPostPutRequest(path, contentFormat, body, codes.PUT)
}

// NewDeleteRequest creates delete request
func (cc *ClientCommander) NewDeleteRequest(path string) (Message, error) {
	return cc.newGetDeleteRequest(path, codes.DELETE)
}

// LocalAddr implements the networkSession.LocalAddr method.
func (cc *ClientCommander) LocalAddr() net.Addr {
	return cc.networkSession.LocalAddr()
}

// RemoteAddr implements the networkSession.RemoteAddr method.
func (cc *ClientCommander) RemoteAddr() net.Addr {
	return cc.networkSession.RemoteAddr()
}

// PeerCertificates implements the networkSession.PeerCertificates method.
func (cc *ClientCommander) PeerCertificates() []*x509.Certificate {
	return cc.networkSession.PeerCertificates()
}

// Equal compare two ClientCommanders
func (cc *ClientCommander) Equal(cc1 *ClientCommander) bool {
	return cc.RemoteAddr().String() == cc1.RemoteAddr().String() && cc.LocalAddr().String() == cc1.LocalAddr().String()
}

// Exchange same as ExchangeContext without context
func (cc *ClientCommander) Exchange(m Message) (Message, error) {
	return cc.ExchangeWithContext(context.Background(), m)
}

// ExchangeContext performs a synchronous query with context. It sends the message m to the address
// contained in a and waits for a reply.
//
// ExchangeContext does not retry a failed query, nor will it fall back to TCP in
// case of truncation.
func (cc *ClientCommander) ExchangeWithContext(ctx context.Context, m Message) (Message, error) {
	return cc.networkSession.ExchangeWithContext(ctx, m)
}

// WriteMsg sends  direct a message through the connection
func (cc *ClientCommander) WriteMsg(m Message) error {
	return cc.WriteMsgWithContext(context.Background(), m)
}

// WriteContextMsg sends with context direct a message through the connection
func (cc *ClientCommander) WriteMsgWithContext(ctx context.Context, m Message) error {
	return cc.networkSession.WriteMsgWithContext(ctx, m)
}

// Ping send a ping message and wait for a pong response
func (cc *ClientCommander) Ping(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return cc.networkSession.PingWithContext(ctx)
}

// PingContext send with context a ping message and wait for a pong response
func (cc *ClientCommander) PingWithContext(ctx context.Context) error {
	return cc.networkSession.PingWithContext(ctx)
}

// Get retrieves the resource identified by the request path
func (cc *ClientCommander) Get(path string) (Message, error) {
	return cc.GetWithContext(context.Background(), path)
}

// GetContext retrieves with context the resource identified by the request path
func (cc *ClientCommander) GetWithContext(ctx context.Context, path string) (Message, error) {
	req, err := cc.NewGetRequest(path)
	if err != nil {
		return nil, err
	}
	return cc.networkSession.ExchangeWithContext(ctx, req)
}

// Post updates the resource identified by the request path
func (cc *ClientCommander) Post(path string, contentFormat MediaType, body io.Reader) (Message, error) {
	return cc.PostWithContext(context.Background(), path, contentFormat, body)
}

// PostContext updates with context the resource identified by the request path
func (cc *ClientCommander) PostWithContext(ctx context.Context, path string, contentFormat MediaType, body io.Reader) (Message, error) {
	req, err := cc.NewPostRequest(path, contentFormat, body)
	if err != nil {
		return nil, err
	}
	return cc.networkSession.ExchangeWithContext(ctx, req)
}

// Put creates the resource identified by the request path
func (cc *ClientCommander) Put(path string, contentFormat MediaType, body io.Reader) (Message, error) {
	return cc.PutWithContext(context.Background(), path, contentFormat, body)
}

// PutContext creates with context the resource identified by the request path
func (cc *ClientCommander) PutWithContext(ctx context.Context, path string, contentFormat MediaType, body io.Reader) (Message, error) {
	req, err := cc.NewPutRequest(path, contentFormat, body)
	if err != nil {
		return nil, err
	}
	return cc.networkSession.ExchangeWithContext(ctx, req)
}

// Delete deletes the resource identified by the request path
func (cc *ClientCommander) Delete(path string) (Message, error) {
	return cc.DeleteWithContext(context.Background(), path)
}

// DeleteContext deletes with context the resource identified by the request path
func (cc *ClientCommander) DeleteWithContext(ctx context.Context, path string) (Message, error) {
	req, err := cc.NewDeleteRequest(path)
	if err != nil {
		return nil, err
	}
	return cc.networkSession.ExchangeWithContext(ctx, req)
}

//Observation represents subscription to resource on the server
type Observation struct {
	token       []byte
	path        string
	obsSequence uint32
	client      *ClientCommander
}

func (o *Observation) Cancel() error {
	return o.CancelWithContext(context.Background())
}

// CancelContext remove observation from server. For recreate observation use Observe.
func (o *Observation) CancelWithContext(ctx context.Context) error {
	req := o.client.NewMessage(MessageParams{
		Type:      NonConfirmable,
		Code:      codes.GET,
		MessageID: GenerateMessageID(),
		Token:     o.token,
	})
	req.SetPathString(o.path)
	req.SetOption(Observe, 1)
	err1 := o.client.WriteMsgWithContext(ctx, req)
	err2 := o.client.networkSession.TokenHandler().Remove(o.token)
	if err1 != nil {
		return err1
	}
	return err2
}

func (cc *ClientCommander) Observe(path string, observeFunc func(req *Request)) (*Observation, error) {
	return cc.ObserveWithContext(context.Background(), path, observeFunc)
}

// ObserveContext subscribe to severon path. After subscription and every change on path,
// server sends immediately response
func (cc *ClientCommander) ObserveWithContext(
	ctx context.Context,
	path string,
	observeFunc func(req *Request),
	options ...func(Message),
) (*Observation, error) {
	req, err := cc.NewGetRequest(path)
	if err != nil {
		return nil, err
	}
	for _, option := range options {
		option(req)
	}

	req.SetOption(Observe, 0)
	/*
		IoTivity doesn't support Block2 in first request for GET
		block, err := MarshalBlockOption(cc.networkSession.blockWiseSzx(), 0, false)
		if err != nil {
			return nil, err
		}
		req.SetOption(Block2, block)
	*/
	o := &Observation{
		token:       req.Token(),
		path:        path,
		obsSequence: 0,
		client:      cc,
	}
	err = cc.networkSession.TokenHandler().Add(req.Token(), func(w ResponseWriter, r *Request) {
		var err error
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
		setObsSequence := func() bool {
			if r.Msg.Option(Observe) != nil {
				obsSequence := r.Msg.Option(Observe).(uint32)
				//obs starts with 0, after that check obsSequence
				if obsSequence != 0 && o.obsSequence > obsSequence {
					return false
				}
				o.obsSequence = obsSequence
			}
			return true
		}

		switch {
		case r.Msg.Option(ETag) != nil && resp.Option(ETag) != nil:
			//during processing observation, check if notification is still valid
			if bytes.Equal(resp.Option(ETag).([]byte), r.Msg.Option(ETag).([]byte)) {
				if setObsSequence() {
					observeFunc(&Request{Msg: resp, Client: r.Client, Ctx: r.Ctx, Sequence: r.Sequence})
				}
			}
		default:
			if setObsSequence() {
				observeFunc(&Request{Msg: resp, Client: r.Client, Ctx: r.Ctx, Sequence: r.Sequence})
			}
		}
		return
	})
	if err != nil {
		return nil, err
	}
	err = cc.WriteMsgWithContext(ctx, req)
	if err != nil {
		cc.networkSession.TokenHandler().Remove(o.token)
		return nil, err
	}

	return o, nil
}

// Close close connection
func (cc *ClientCommander) Close() error {
	return cc.networkSession.Close()
}

// Sequence discontinuously unique growing number for connection.
func (cc *ClientCommander) Sequence() uint64 {
	return cc.networkSession.Sequence()
}
