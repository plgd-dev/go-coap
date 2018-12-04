package coapservertcp

import (
	"sync"

	coap "github.com/go-ocf/go-coap/g2/message"
	coapTCP "github.com/go-ocf/go-coap/g2/message/tcp"
)

var (
	RequestPool sync.Pool
)

type Request struct {
	noCopy

	//exported
	Client *ClientCommander

	//local vars
	msg             coapTCP.Message
	rawData         []byte
	valueBuffer     []byte
	origValueBuffer []byte
}

// Reset clear message for next reuse
func (r *Request) Reset() {
	r.msg.Options = r.msg.Options[:0]
	r.valueBuffer = r.origValueBuffer
}

func (r *Request) RemoveOption(opt coap.OptionID) {
	r.msg.Options = r.msg.Options.Remove(opt)
}

func (r *Request) Token() []byte {
	return r.msg.Token
}

func (r *Request) SetToken(token []byte) {
	r.msg.Token = token
}

func (r *Request) Code() coap.COAPCode {
	return r.msg.Code
}

func (r *Request) SetCode(code coap.COAPCode) {
	r.msg.Code = code
}

func (r *Request) OptionUint32(id coap.OptionID) (uint32, coap.ErrorCode) {
	return r.msg.Options.OptionUint32(id)
}

func (r *Request) SetOptionString(opt coap.OptionID, value string) {
	r.msg.Options, used, err = r.msg.Options.SetOptionSting(r.valueBuffer, opt, value)
	if err == coap.ErrorCodeTooSmall {
		r.valueBuffer = append(r.valueBuffer, used)
		r.msg.Options, used, err = r.msg.Options.SetOptionSting(r.valueBuffer, opt, value)
	}
	r.valueBuffer = r.valueBuffer[used:]
}

func (r *Request) AddOptionString(opt coap.OptionID, value string) {
	r.msg.Options, used, err = r.msg.Options.AddOptionSting(r.valueBuffer, opt, value)
	if err == coap.ErrorCodeTooSmall {
		r.valueBuffer = append(r.valueBuffer, used)
		r.msg.Options, used, err = r.msg.Options.AddOptionSting(r.valueBuffer, opt, value)
	}
	r.valueBuffer = r.valueBuffer[used:]
}

func (r *Request) AddOptionBytes(opt coap.OptionID, value []byte) {
	r.msg.Options = r.msg.Options.Add(coap.Option{opt, value})
}

func (r *Request) SetOptionBytes(opt coap.OptionID, value []byte) {
	r.msg.Options = r.msg.Options.Set(coap.Option{opt, value})
}

func (r *Request) SetOptionUint32(opt coap.OptionID, value uint32) {
	r.msg.Options, used, err = r.msg.Options.SetOptionUint32(r.valueBuffer, opt, value)
	if err == coap.ErrorCodeTooSmall {
		r.valueBuffer = append(r.valueBuffer, used)
		r.msg.Options, used, err = r.msg.Options.SetOptionUint32(r.valueBuffer, opt, value)
	}
	r.valueBuffer = r.valueBuffer[used:]
}

func (r *Request) AddOptionUint32(opt coap.OptionID, value uint32) {
	r.msg.Options, used, err = r.msg.Options.AddOptionUint32(r.valueBuffer, opt, value)
	if err == coap.ErrorCodeTooSmall {
		r.valueBuffer = append(r.valueBuffer, used)
		r.msg.Options, used, err = r.msg.Options.AddOptionUint32(r.valueBuffer, opt, value)
	}
	r.valueBuffer = r.valueBuffer[used:]
}

func (r *Request) ContentFormat() (coap.MediaType, coap.ErrorCode) {
	v, err := r.OptionUint32(coap.MediaType)
	return coap.MediaType(v), err
}

func (r *Request) SetContentFormat(contentFormat coap.MediaType) {
	r.SetOptionUint32(coap.ContentFormat, uint32(contentFormat))
}

func (r *Request) Copy(src *Request) {
	r.msg.Code = src.msg.Code
	if src.msg.Token != nil {
		r.msg.Token = append(r.msg.Token[:0], src.msg.Token)
	}
	r.msg.Options = append(r.msg.Options[:0], src.msg.Options)
	r.msg.Payload = append(r.msg.Payload[:0], r.msg.Payload)
}

// AcquireRequestMsg returns an empty Request instance from Request pool.
//
// The returned Request instance may be passed to ReleaseRequest when it is
// no longer needed. This allows Request recycling, reduces GC pressure
// and usually improves performance.
func AcquireRequest() *Request {
	v := RequestPool.Get()
	if v == nil {
		return &Request{
			msg: coapTCP.Message{
				Options: make(coap.Options, 0, 16),
			},
			rawData:         make([]byte, 256),
			valueBuffer:     make([]byte, 256),
			origValueBuffer: valueBuffer,
		}
	}
	return v.(*Request)
}

// ReleaseRequestMsg returns req acquired via AcquireRequest to Request pool.
//
// It is forbidden accessing req and/or its' members after returning
// it to Request pool.
func ReleaseRequest(req *Request) {
	req.Reset()
	RequestPool.Put(req)
}
