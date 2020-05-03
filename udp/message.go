package udp

import (
	"bytes"
	"context"
	"io"
	"sync"
	"sync/atomic"

	"github.com/go-ocf/go-coap/v2/message"
	"github.com/go-ocf/go-coap/v2/message/codes"
	coapUDP "github.com/go-ocf/go-coap/v2/message/udp"
)

var (
	RequestPool sync.Pool
)

type Message struct {
	noCopy

	//local vars
	msg             coapUDP.Message
	rawData         []byte
	valueBuffer     []byte
	origValueBuffer []byte
	rawMarshalData  []byte

	payload  io.ReadSeeker
	ctx      context.Context
	sequence uint64
	hijacked uint32
	wantSend bool
}

// Reset clear message for next reuse
func (r *Message) Reset() {
	r.msg.Token = nil
	r.msg.Code = codes.Empty
	r.msg.Options = r.msg.Options[:0]
	r.valueBuffer = r.origValueBuffer
	r.rawData = r.rawData[:0]
	r.rawMarshalData = r.rawMarshalData[:0]
	r.payload = nil
	r.wantSend = false
}

func (r *Message) Remove(opt message.OptionID) {
	r.msg.Options = r.msg.Options.Remove(opt)
}

func (r *Message) Token() message.Token {
	if r.msg.Token == nil {
		return nil
	}
	token := make(message.Token, 0, 8)
	token = append(token, r.msg.Token...)
	return token
}

func (r *Message) SetToken(token message.Token) {
	if token == nil {
		r.msg.Token = nil
		return
	}
	r.msg.Token = append(r.msg.Token[:0], token...)
}

func (r *Message) SetOptions(in message.Options) {
	used, opts, err := r.msg.Options.SetOptions(r.valueBuffer, in)
	if err == message.ErrTooSmall {
		r.valueBuffer = append(r.valueBuffer, make([]byte, used)...)
		used, opts, err = r.msg.Options.SetOptions(r.valueBuffer, in)
	}
	r.msg.Options = opts
	r.valueBuffer = r.valueBuffer[used:]
	if len(in) > 0 {
		r.wantSend = true
	}
}

func (r *Message) Options() message.Options {
	return r.msg.Options
}

func (r *Message) SetPath(p string) {
	opts, used, err := r.msg.Options.SetPath(r.valueBuffer, p)

	if err == message.ErrTooSmall {
		r.valueBuffer = append(r.valueBuffer, make([]byte, used)...)
		opts, used, err = r.msg.Options.SetPath(r.valueBuffer, p)
	}
	r.msg.Options = opts
	r.valueBuffer = r.valueBuffer[used:]
	r.wantSend = true
}

func (r *Message) SetMessageID(mid uint16) {
	r.msg.MessageID = mid
}

func (r *Message) MessageID() uint16 {
	return r.msg.MessageID
}

func (r *Message) SetType(typ coapUDP.Type) {
	r.msg.Type = typ
	r.wantSend = true
}

func (r *Message) Type() coapUDP.Type {
	return r.msg.Type
}

func (r *Message) Code() codes.Code {
	return r.msg.Code
}

func (r *Message) SetCode(code codes.Code) {
	r.msg.Code = code
	r.wantSend = true
}

func (r *Message) SetETag(value []byte) {
	r.SetOptionBytes(message.ETag, value)
}

func (r *Message) GetETag() ([]byte, error) {
	return r.GetOptionBytes(message.ETag)
}

func (r *Message) AddQuery(query string) {
	r.AddOptionString(message.URIQuery, query)
}

func (r *Message) GetOptionUint32(id message.OptionID) (uint32, error) {
	return r.msg.Options.GetOptionUint32(id)
}

func (r *Message) SetOptionString(opt message.OptionID, value string) {
	opts, used, err := r.msg.Options.SetOptionString(r.valueBuffer, opt, value)
	if err == message.ErrTooSmall {
		r.valueBuffer = append(r.valueBuffer, make([]byte, used)...)
		opts, used, err = r.msg.Options.SetOptionString(r.valueBuffer, opt, value)
	}
	r.msg.Options = opts
	r.valueBuffer = r.valueBuffer[used:]
	r.wantSend = true
}

func (r *Message) AddOptionString(opt message.OptionID, value string) {
	opts, used, err := r.msg.Options.AddOptionString(r.valueBuffer, opt, value)
	if err == message.ErrTooSmall {
		r.valueBuffer = append(r.valueBuffer, make([]byte, used)...)
		opts, used, err = r.msg.Options.AddOptionString(r.valueBuffer, opt, value)
	}
	r.msg.Options = opts
	r.valueBuffer = r.valueBuffer[used:]
	r.wantSend = true
}

func (r *Message) AddOptionBytes(opt message.OptionID, value []byte) {
	r.msg.Options = r.msg.Options.Add(message.Option{opt, value})
	r.wantSend = true
}

func (r *Message) SetOptionBytes(opt message.OptionID, value []byte) {
	r.msg.Options = r.msg.Options.Set(message.Option{opt, value})
	r.wantSend = true
}

func (r *Message) GetOptionBytes(id message.OptionID) ([]byte, error) {
	return r.msg.Options.GetOptionBytes(id)
}

func (r *Message) SetOptionUint32(opt message.OptionID, value uint32) {
	opts, used, err := r.msg.Options.SetOptionUint32(r.valueBuffer, opt, value)
	if err == message.ErrTooSmall {
		r.valueBuffer = append(r.valueBuffer, make([]byte, used)...)
		opts, used, err = r.msg.Options.SetOptionUint32(r.valueBuffer, opt, value)
	}
	r.msg.Options = opts
	r.valueBuffer = r.valueBuffer[used:]
	r.wantSend = true
}

func (r *Message) AddOptionUint32(opt message.OptionID, value uint32) {
	opts, used, err := r.msg.Options.AddOptionUint32(r.valueBuffer, opt, value)
	if err == message.ErrTooSmall {
		r.valueBuffer = append(r.valueBuffer, make([]byte, used)...)
		opts, used, err = r.msg.Options.AddOptionUint32(r.valueBuffer, opt, value)
	}
	r.msg.Options = opts
	r.valueBuffer = r.valueBuffer[used:]
	r.wantSend = true
}

func (r *Message) ContentFormat() (message.MediaType, error) {
	v, err := r.GetOptionUint32(message.ContentFormat)
	return message.MediaType(v), err
}

func (r *Message) HasOption(id message.OptionID) bool {
	return r.msg.Options.HasOption(id)
}

func (r *Message) SetContentFormat(contentFormat message.MediaType) {
	r.SetOptionUint32(message.ContentFormat, uint32(contentFormat))
}

func (r *Message) SetObserve(observe uint32) {
	r.SetOptionUint32(message.Observe, observe)
}

func (r *Message) Observe() (uint32, error) {
	return r.GetOptionUint32(message.Observe)
}

func (r *Message) ETag() ([]byte, error) {
	return r.GetOptionBytes(message.ETag)
}

func (r *Message) PayloadSize() (int64, error) {
	if r.payload == nil {
		return 0, nil
	}
	orig, err := r.payload.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	_, err = r.payload.Seek(0, io.SeekStart)
	if err != nil {
		return 0, err
	}
	size, err := r.payload.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	_, err = r.payload.Seek(orig, io.SeekStart)
	if err != nil {
		return 0, err
	}
	return size, nil
}

func (r *Message) SetPayload(s io.ReadSeeker) {
	r.payload = s
	r.wantSend = true
}

func (r *Message) Payload() io.ReadSeeker {
	return r.payload
}

func (r *Message) Copy(src *Message) {
	r.Reset()
	r.msg.Code = src.msg.Code
	if src.msg.Token != nil {
		r.msg.Token = append(r.msg.Token[:0], src.msg.Token...)
	}
	r.msg.Options = append(r.msg.Options[:0], src.msg.Options...)
	r.msg.Payload = append(r.msg.Payload[:0], r.msg.Payload...)
	if len(r.msg.Payload) > 0 {
		r.payload = bytes.NewReader(r.msg.Payload)
	}
}

func (r *Message) Unmarshal(data []byte) (int, error) {
	r.Reset()
	if len(r.rawData) < len(data) {
		r.rawData = append(r.rawData, make([]byte, len(data)-len(r.rawData))...)
	}
	copy(r.rawData, data)
	r.rawData = r.rawData[:len(data)]
	n, err := r.msg.Unmarshal(r.rawData)
	if err != nil {
		return n, err
	}
	if len(r.msg.Payload) > 0 {
		r.payload = bytes.NewReader(r.msg.Payload)
	}
	return n, err
}

func (r *Message) Marshal() ([]byte, error) {
	if r.payload != nil {
		size, err := r.PayloadSize()
		if err != nil {
			return nil, err
		}
		_, err = r.payload.Seek(0, io.SeekStart)
		if err != nil {
			return nil, err
		}
		if int64(len(r.msg.Payload)) < size {
			r.msg.Payload = make([]byte, size)
		}
		n, err := io.ReadFull(r.payload, r.msg.Payload)
		if err != nil {
			if err == io.ErrUnexpectedEOF && int64(n) == size {
				err = nil
			}
		}
		if err != nil {
			return nil, err
		}
		r.msg.Payload = r.msg.Payload[:n]
	} else {
		r.msg.Payload = r.msg.Payload[:0]
	}
	size, err := r.msg.Size()
	if err != nil {
		return nil, err
	}
	if len(r.rawMarshalData) < size {
		r.rawMarshalData = append(r.rawMarshalData, make([]byte, size-len(r.rawMarshalData))...)
	}
	n, err := r.msg.MarshalTo(r.rawMarshalData)
	if err != nil {
		return nil, err
	}
	r.rawMarshalData = r.rawMarshalData[:n]
	return r.rawMarshalData, nil
}

func (r *Message) Context() context.Context {
	return r.ctx
}

func (r *Message) Sequence() uint64 {
	return r.sequence
}

func (r *Message) Hijack() {
	atomic.StoreUint32(&r.hijacked, 1)
}

func (r *Message) IsHijacked() bool {
	return atomic.LoadUint32(&r.hijacked) == 1
}

func (r *Message) WantToSend() bool {
	return r.wantSend
}

// AcquireRequest returns an empty Message instance from Message pool.
//
// The returned Message instance may be passed to ReleaseRequest when it is
// no longer needed. This allows Message recycling, reduces GC pressure
// and usually improves performance.
func AcquireRequest(ctx context.Context) *Message {
	v := RequestPool.Get()
	if v == nil {
		valueBuffer := make([]byte, 256)
		return &Message{
			msg: coapUDP.Message{
				Options: make(message.Options, 0, 16),
			},
			rawData:         make([]byte, 256),
			rawMarshalData:  make([]byte, 256),
			valueBuffer:     valueBuffer,
			origValueBuffer: valueBuffer,
			ctx:             ctx,
		}
	}
	r := v.(*Message)
	r.Reset()
	r.ctx = ctx
	return r
}

// ReleaseRequest returns req acquired via AcquireRequest to Message pool.
//
// It is forbidden accessing req and/or its' members after returning
// it to Message pool.
func ReleaseRequest(req *Message) {
	req.Reset()
	RequestPool.Put(req)
}
