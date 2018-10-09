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

// NewMessage Create message for request
func (cc *ClientCommander) NewMessage(p MessageParams) Message {
	return cc.networkSession.NewMessage(p)
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

// Write sends direct a message through the connection
func (cc *ClientCommander) Write(m Message) error {
	return cc.networkSession.Write(m)
}

// Ping send a ping message and wait for a pong response
func (cc *ClientCommander) Ping(timeout time.Duration) error {
	return cc.networkSession.Ping(timeout)
}

// Get retrieve the resource identified by the request path
func (cc *ClientCommander) Get(path string) (Message, error) {
	req, err := createGetReq(cc, path)
	if err != nil {
		return nil, err
	}
	return cc.networkSession.Exchange(req)
}

func (cc *ClientCommander) putPostHelper(code COAPCode, path string, contentType MediaType, body io.Reader) (Message, error) {
	token, err := GenerateToken()
	if err != nil {
		return nil, err
	}
	req := cc.networkSession.NewMessage(MessageParams{
		Type:      Confirmable,
		Code:      POST,
		MessageID: GenerateMessageID(),
		Token:     token,
	})
	req.SetPathString(path)
	req.SetOption(ContentFormat, contentType)
	payload, err := ioutil.ReadAll(body)
	if err != nil {
		return nil, err
	}
	req.SetPayload(payload)
	return cc.networkSession.Exchange(req)
}

// Post update the resource identified by the request path
func (cc *ClientCommander) Post(path string, contentType MediaType, body io.Reader) (Message, error) {
	return cc.putPostHelper(POST, path, contentType, body)
}

// Put create the resource identified by the request path
func (cc *ClientCommander) Put(path string, contentType MediaType, body io.Reader) (Message, error) {
	return cc.putPostHelper(PUT, path, contentType, body)
}

// Delete delete the resource identified by the request path
func (cc *ClientCommander) Delete(path string) (Message, error) {
	token, err := GenerateToken()
	if err != nil {
		return nil, err
	}
	req := cc.networkSession.NewMessage(MessageParams{
		Type:      Confirmable,
		Code:      DELETE,
		MessageID: GenerateMessageID(),
		Token:     token,
	})
	req.SetPathString(path)
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
	err1 := o.client.Write(req)
	err2 := o.client.networkSession.TokenHandler().Remove(o.token)
	if err1 != nil {
		return err1
	}
	return err2
}

// Observe subscribe to severon path. After subscription and every change on path,
// server sends immediately response
func (cc *ClientCommander) Observe(path string, observeFunc func(req *Request)) (*Observation, error) {
	req, err := createGetReq(cc, path)
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
			resp, err = cc.Get(path)
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
	err = cc.networkSession.Write(req)
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
