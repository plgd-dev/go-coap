package coapservertcp

import (
	"sync"

	coap "github.com/go-ocf/go-coap/g2/message"
	coapTCP "github.com/go-ocf/go-coap/g2/message/tcp"
)

var (
	requestPool sync.Pool
)

type RequestMsg struct {
	noCopy
	msg coapTCP.Message

	rawData         []byte
	valueBuffer     []byte
	origValueBuffer []byte
}

// Reset clear message for next reuse
func (r *RequestMsg) Reset() {
	r.msg.Options = r.msg.Options[:0]
	r.valueBuffer = r.origValueBuffer
}

func (r *RequestMsg) RemoveOption(opt coap.OptionID) {
	r.msg.Options = r.msg.Options.Remove(opt)
}

func (r *RequestMsg) Token() []byte {
	return r.msg.Token
}

func (r *RequestMsg) SetToken(token []byte) {
	r.msg.Token = token
}

func (r *RequestMsg) Code() coap.COAPCode {
	return r.msg.Code
}

func (r *RequestMsg) SetCode(code coap.COAPCode) {
	r.msg.Code = code
}

func (r *RequestMsg) OptionUint32(id coap.OptionID) (uint32, coap.ErrorCode) {
	return r.msg.Options.OptionUint32(id)
}

func (r *RequestMsg) SetOptionString(opt coap.OptionID, value string) {
	r.SetOptionBytes(opt, []byte(value))
}

func (r *RequestMsg) AddOptionString(opt coap.OptionID, value string) {
	r.AddOptionBytes(opt, []byte(value))
}

func (r *RequestMsg) AddOptionBytes(opt coap.OptionID, value []byte) {
	r.msg.Options = r.msg.Options.Add(coap.Option{opt, value})
}

func (r *RequestMsg) SetOptionBytes(opt coap.OptionID, value []byte) {
	r.msg.Options = r.msg.Options.Set(coap.Option{opt, value})
}

func (r *RequestMsg) SetOptionUint32(opt coap.OptionID, value uint32) {
	r.msg.Options, used, err = r.msg.Options.SetOptionUint32(r.valueBuffer, opt, value)
	if err == coap.ErrorCodeTooSmall {
		r.valueBuffer = append(r.valueBuffer, used)
		r.msg.Options, used, err = r.msg.Options.SetOptionUint32(r.valueBuffer, opt, value)
	}
	r.valueBuffer = r.valueBuffer[used:]
}

func (r *RequestMsg) AddOptionUint32(opt coap.OptionID, value uint32) {
	r.msg.Options, used, err = r.msg.Options.AddOptionUint32(r.valueBuffer, opt, value)
	if err == coap.ErrorCodeTooSmall {
		r.valueBuffer = append(r.valueBuffer, used)
		r.msg.Options, used, err = r.msg.Options.AddOptionUint32(r.valueBuffer, opt, value)
	}
	r.valueBuffer = r.valueBuffer[used:]
}

func (r *RequestMsg) ContentFormat() (coap.MediaType, coap.ErrorCode) {
	v, err := r.OptionUint32(coap.MediaType)
	return coap.MediaType(v), err
}

func (r *RequestMsg) SetContentFormat(contentFormat coap.MediaType) {
	r.SetOptionUint32(coap.ContentFormat, uint32(contentFormat))
}

func (r *RequestMsg) Copy(src *RequestMsg) {
	r.msg.Code = src.msg.Code
	if src.msg.Token != nil {
		r.msg.Token = append(r.msg.Token[:0], src.msg.Token)
	}
	r.msg.Options = append(r.msg.Options[:0], src.msg.Options)
	r.msg.Payload = append(r.msg.Payload[:0], r.msg.Payload)
}

// AcquireRequestMsg returns an empty Request instance from request pool.
//
// The returned Request instance may be passed to ReleaseRequest when it is
// no longer needed. This allows Request recycling, reduces GC pressure
// and usually improves performance.
func AcquireRequestMsg() *RequestMsg {
	v := requestPool.Get()
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

// ReleaseRequestMsg returns req acquired via AcquireRequest to request pool.
//
// It is forbidden accessing req and/or its' members after returning
// it to request pool.
func ReleaseRequestMsg(req *RequestMsg) {
	req.Reset()
	requestPool.Put(req)
}
