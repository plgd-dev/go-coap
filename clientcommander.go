package coap

import (
	"bytes"
	"io"
	"io/ioutil"
	"net"
	"time"
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

func (cc *ClientCommander) newGetDeleteRequest(path string, code COAPCode) (Message, error) {
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

func (cc *ClientCommander) newPostPutRequest(path string, contentFormat MediaType, body io.Reader, code COAPCode) (Message, error) {
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
	return cc.newGetDeleteRequest(path, GET)
}

// NewPostRequest creates post request
func (cc *ClientCommander) NewPostRequest(path string, contentFormat MediaType, body io.Reader) (Message, error) {
	return cc.newPostPutRequest(path, contentFormat, body, POST)
}

// NewPutRequest creates put request
func (cc *ClientCommander) NewPutRequest(path string, contentFormat MediaType, body io.Reader) (Message, error) {
	return cc.newPostPutRequest(path, contentFormat, body, PUT)
}

// NewDeleteRequest creates delete request
func (cc *ClientCommander) NewDeleteRequest(path string) (Message, error) {
	return cc.newGetDeleteRequest(path, DELETE)
}

// LocalAddr implements the networkSession.LocalAddr method.
func (cc *ClientCommander) LocalAddr() net.Addr {
	return cc.networkSession.LocalAddr()
}

// RemoteAddr implements the networkSession.RemoteAddr method.
func (cc *ClientCommander) RemoteAddr() net.Addr {
	return cc.networkSession.RemoteAddr()
}

// Equal compare two ClientCommanders
func (cc *ClientCommander) Equal(cc1 *ClientCommander) bool {
	return cc.RemoteAddr().String() == cc1.RemoteAddr().String() && cc.LocalAddr().String() == cc1.LocalAddr().String()
}

// Exchange performs a synchronous query. It sends the message m to the address
// contained in a and waits for a reply.
//
// Exchange does not retry a failed query, nor will it fall back to TCP in
// case of truncation.
// To specify a local address or a timeout, the caller has to set the `Client.Dialer`
// attribute appropriately
func (cc *ClientCommander) Exchange(m Message) (Message, error) {
	return cc.networkSession.Exchange(m)
}

// WriteMsg sends direct a message through the connection
func (cc *ClientCommander) WriteMsg(m Message) error {
	return cc.networkSession.WriteMsg(m)
}

// Ping send a ping message and wait for a pong response
func (cc *ClientCommander) Ping(timeout time.Duration) error {
	return cc.networkSession.Ping(timeout)
}

// Get retrieve the resource identified by the request path
func (cc *ClientCommander) Get(path string) (Message, error) {
	req, err := cc.NewGetRequest(path)
	if err != nil {
		return nil, err
	}
	return cc.networkSession.Exchange(req)
}

// Post update the resource identified by the request path
func (cc *ClientCommander) Post(path string, contentFormat MediaType, body io.Reader) (Message, error) {
	req, err := cc.NewPostRequest(path, contentFormat, body)
	if err != nil {
		return nil, err
	}
	return cc.networkSession.Exchange(req)
}

// Put create the resource identified by the request path
func (cc *ClientCommander) Put(path string, contentFormat MediaType, body io.Reader) (Message, error) {
	req, err := cc.NewPutRequest(path, contentFormat, body)
	if err != nil {
		return nil, err
	}
	return cc.networkSession.Exchange(req)
}

// Delete delete the resource identified by the request path
func (cc *ClientCommander) Delete(path string) (Message, error) {
	req, err := cc.NewDeleteRequest(path)
	if err != nil {
		return nil, err
	}
	return cc.networkSession.Exchange(req)
}

//Observation represents subscription to resource on the server
type Observation struct {
	token     []byte
	path      string
	obsSeqNum uint32
	client    *ClientCommander
}

// Cancel remove observation from server. For recreate observation use Observe.
func (o *Observation) Cancel() error {
	req := o.client.NewMessage(MessageParams{
		Type:      NonConfirmable,
		Code:      GET,
		MessageID: GenerateMessageID(),
		Token:     o.token,
	})
	req.SetPathString(o.path)
	req.SetOption(Observe, 1)
	err1 := o.client.WriteMsg(req)
	err2 := o.client.networkSession.TokenHandler().Remove(o.token)
	if err1 != nil {
		return err1
	}
	return err2
}

// Observe subscribe to severon path. After subscription and every change on path,
// server sends immediately response
func (cc *ClientCommander) Observe(path string, observeFunc func(req *Request)) (*Observation, error) {
	req, err := cc.NewGetRequest(path)
	if err != nil {
		return nil, err
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
		token:     req.Token(),
		path:      path,
		obsSeqNum: 0,
		client:    cc,
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
			resp, err = r.Client.Get(path)
			if err != nil {
				return
			}
		}
		setObsSeqNum := func() bool {
			if r.Msg.Option(Observe) != nil {
				obsSeqNum := r.Msg.Option(Observe).(uint32)
				//obs starts with 0, after that check obsSeqNum
				if obsSeqNum != 0 && o.obsSeqNum > obsSeqNum {
					return false
				}
				o.obsSeqNum = obsSeqNum
			}
			return true
		}

		switch {
		case r.Msg.Option(ETag) != nil && resp.Option(ETag) != nil:
			//during processing observation, check if notification is still valid
			if bytes.Equal(resp.Option(ETag).([]byte), r.Msg.Option(ETag).([]byte)) {
				if setObsSeqNum() {
					observeFunc(&Request{Msg: resp, Client: r.Client})
				}
			}
		default:
			if setObsSeqNum() {
				observeFunc(&Request{Msg: resp, Client: r.Client})
			}
		}
		return
	})
	if err != nil {
		return nil, err
	}
	err = cc.WriteMsg(req)
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

// SetReadDeadline set read deadline for timeout for Exchange
func (cc *ClientCommander) SetReadDeadline(timeout time.Duration) {
	cc.networkSession.SetReadDeadline(timeout)
}

// SetWriteDeadline set write deadline for timeout for Exchange and Write
func (cc *ClientCommander) SetWriteDeadline(timeout time.Duration) {
	cc.networkSession.SetWriteDeadline(timeout)
}

// ReadDeadline get read deadline
func (cc *ClientCommander) ReadDeadline() time.Duration {
	return cc.networkSession.ReadDeadline()
}

// WriteDeadline get read writeline
func (cc *ClientCommander) WriteDeadline() time.Duration {
	return cc.networkSession.WriteDeadline()
}
