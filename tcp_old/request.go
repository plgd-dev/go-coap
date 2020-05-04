package tcpold

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/go-ocf/go-coap/v2/message"
	"github.com/go-ocf/go-coap/v2/message/codes"
	coapTCP "github.com/go-ocf/go-coap/v2/message/tcp"
)

var (
	RequestPool sync.Pool
)

type Request struct {
	noCopy

	//local vars
	msg             coapTCP.Message
	rawData         []byte
	valueBuffer     []byte
	origValueBuffer []byte
	rawMarshalData  []byte

	ctx      context.Context
	sequence uint64
	hijacked uint32
}

// Reset clear message for next reuse
func (r *Request) Reset() {
	r.msg.Options = r.msg.Options[:0]
	r.valueBuffer = r.origValueBuffer
}

func (r *Request) RemoveOption(opt message.OptionID) {
	r.msg.Options = r.msg.Options.Remove(opt)
}

func (r *Request) Token() []byte {
	return r.msg.Token
}

func (r *Request) SetPath(p string) {
	opts, used, err := r.msg.Options.SetPath(r.valueBuffer, p)
	r.msg.Options = opts
	if err == message.ErrTooSmall {
		r.valueBuffer = r.valueBuffer[:len(r.valueBuffer)+used]
		r.msg.Options, used, err = r.msg.Options.SetPath(r.valueBuffer, p)
	}
	r.valueBuffer = r.valueBuffer[used:]
}

func (r *Request) SetToken(token []byte) {
	r.msg.Token = token
}

func (r *Request) Code() codes.Code {
	return r.msg.Code
}

func (r *Request) SetCode(code codes.Code) {
	r.msg.Code = code
}

func (r *Request) AddQuery(query string) {
	r.AddOptionString(message.URIQuery, query)
}

func (r *Request) GetOptionUint32(id message.OptionID) (uint32, error) {
	return r.msg.Options.GetOptionUint32(id)
}

func (r *Request) ResetTotring(opt message.OptionID, value string) {
	opts, used, err := r.msg.Options.ResetTotring(r.valueBuffer, opt, value)
	r.msg.Options = opts
	if err == message.ErrTooSmall {
		r.valueBuffer = r.valueBuffer[:len(r.valueBuffer)+used]
		r.msg.Options, used, err = r.msg.Options.ResetTotring(r.valueBuffer, opt, value)
	}
	r.valueBuffer = r.valueBuffer[used:]
}

func (r *Request) AddOptionString(opt message.OptionID, value string) {
	opts, used, err := r.msg.Options.AddOptionString(r.valueBuffer, opt, value)
	r.msg.Options = opts
	if err == message.ErrTooSmall {
		r.valueBuffer = r.valueBuffer[:len(r.valueBuffer)+used]
		r.msg.Options, used, err = r.msg.Options.AddOptionString(r.valueBuffer, opt, value)
	}
	r.valueBuffer = r.valueBuffer[used:]
}

func (r *Request) AddOptionBytes(opt message.OptionID, value []byte) {
	r.msg.Options = r.msg.Options.Add(message.Option{opt, value})
}

func (r *Request) SetOptionBytes(opt message.OptionID, value []byte) {
	r.msg.Options = r.msg.Options.Set(message.Option{opt, value})
}

func (r *Request) SetOptionUint32(opt message.OptionID, value uint32) {
	opts, used, err := r.msg.Options.SetOptionUint32(r.valueBuffer, opt, value)
	r.msg.Options = opts
	if err == message.ErrTooSmall {
		r.valueBuffer = r.valueBuffer[:len(r.valueBuffer)+used]
		r.msg.Options, used, err = r.msg.Options.SetOptionUint32(r.valueBuffer, opt, value)
	}
	r.valueBuffer = r.valueBuffer[used:]
}

func (r *Request) AddOptionUint32(opt message.OptionID, value uint32) {
	opts, used, err := r.msg.Options.AddOptionUint32(r.valueBuffer, opt, value)
	r.msg.Options = opts
	if err == message.ErrTooSmall {
		r.valueBuffer = r.valueBuffer[:len(r.valueBuffer)+used]
		r.msg.Options, used, err = r.msg.Options.AddOptionUint32(r.valueBuffer, opt, value)
	}
	r.valueBuffer = r.valueBuffer[used:]
}

func (r *Request) ContentFormat() (message.MediaType, error) {
	v, err := r.GetOptionUint32(message.ContentFormat)
	return message.MediaType(v), err
}

func (r *Request) HasOption(id message.OptionID) bool {
	return r.msg.Options.HasOption(id)
}

func (r *Request) hasBlockWiseOption() bool {
	return r.msg.Options.HasOption(coapTCP.BlockWiseTransfer)
}

func (r *Request) SetContentFormat(contentFormat message.MediaType) {
	r.SetOptionUint32(message.ContentFormat, uint32(contentFormat))
}

func (r *Request) SetPayload(s io.ReadSeeker) error {
	posOff, err := s.Seek(0, 1)
	if err != nil {
		return err
	}
	endOff, err := s.Seek(0, 2)
	if err != nil {
		return err
	}
	size := int(endOff - posOff)
	_, err = s.Seek(posOff, 0)
	if err != nil {
		return err
	}
	if len(r.msg.Payload) < size {
		r.msg.Payload = r.msg.Payload[:size]
	}
	n, err := s.Read(r.msg.Payload)
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("cannot read to whole payload")
	}
	r.msg.Payload = r.msg.Payload[:n]
	return nil
}

func (r *Request) GetPayload(s io.Writer) error {
	n, err := s.Write(r.msg.Payload)
	if err != nil {
		return err
	}
	if n != len(r.msg.Payload) {
		return fmt.Errorf("cannot write whole payload")
	}
	return nil
}

func (r *Request) Copy(src *Request) {
	r.Reset()
	r.msg.Code = src.msg.Code
	if src.msg.Token != nil {
		r.msg.Token = append(r.msg.Token[:0], src.msg.Token...)
	}
	r.msg.Options = append(r.msg.Options[:0], src.msg.Options...)
	r.msg.Payload = append(r.msg.Payload[:0], r.msg.Payload...)
}

func (r *Request) UnmarshalWithHeader(hdr coapTCP.MessageHeader, data []byte) (int, error) {
	r.Reset()
	if len(r.rawData) < len(data) {
		r.rawData = make([]byte, len(data))
	}
	copy(r.rawData, data)
	r.rawData = r.rawData[:len(data)]
	return r.msg.UnmarshalWithHeader(hdr, data)
}

func (r *Request) Unmarshal(data []byte) (int, error) {
	r.Reset()
	if len(r.rawData) < len(data) {
		r.rawData = r.rawMarshalData[:len(data)]
	}
	copy(r.rawData, data)
	r.rawData = r.rawData[:len(data)]
	return r.msg.Unmarshal(data)
}

func (r *Request) Marshal() ([]byte, error) {
	size, err := r.msg.Size()
	if err != nil {
		return nil, err
	}
	if len(r.rawMarshalData) < size {
		r.rawMarshalData = r.rawMarshalData[:size]
	}
	n, err := r.msg.MarshalTo(r.rawMarshalData)
	if err != nil {
		return nil, err
	}
	r.rawMarshalData = r.rawMarshalData[:n]
	return r.rawMarshalData, nil
}

func (r *Request) Context() context.Context {
	return r.ctx
}

func (r *Request) Sequence() uint64 {
	return r.sequence
}

func (r *Request) Hijack() {
	atomic.StoreUint32(&r.hijacked, 1)
}

func (r *Request) IsHijacked() bool {
	return atomic.LoadUint32(&r.hijacked) == 1
}

// AcquireMessage returns an empty Request instance from Request pool.
//
// The returned Request instance may be passed to ReleaseMessage when it is
// no longer needed. This allows Request recycling, reduces GC pressure
// and usually improves performance.
func AcquireMessage(ctx context.Context) *Request {
	v := RequestPool.Get()
	if v == nil {
		valueBuffer := make([]byte, 256)
		return &Request{
			msg: coapTCP.Message{
				Options: make(message.Options, 0, 16),
			},
			rawData:         make([]byte, 256),
			rawMarshalData:  make([]byte, 256),
			valueBuffer:     valueBuffer,
			origValueBuffer: valueBuffer,
			ctx:             ctx,
		}
	}
	r := v.(*Request)
	r.Reset()
	r.ctx = ctx
	return r
}

// ReleaseMessage returns req acquired via AcquireMessage to Request pool.
//
// It is forbidden accessing req and/or its' members after returning
// it to Request pool.
func ReleaseMessage(req *Request) {
	req.Reset()
	RequestPool.Put(req)
}
