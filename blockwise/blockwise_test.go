package blockwise

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/dsnet/golib/memfile"
	"github.com/go-ocf/go-coap/v2/message"
	"github.com/go-ocf/go-coap/v2/message/codes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type responseWriter struct {
	resp Message
}

func (r *responseWriter) Message() Message {
	return r.resp
}

func (r *responseWriter) SetMessage(resp Message) {
	r.resp = resp
}

func newResponseWriter(r Message) *responseWriter {
	return &responseWriter{
		resp: r,
	}
}

type request struct {
	code    codes.Code
	ctx     context.Context
	token   message.Token
	options message.Options
	payload io.ReadSeeker
}

func (r *request) Context() context.Context {
	return r.ctx
}

func (r *request) SetCode(c codes.Code) {
	r.code = c
}

func (r *request) Code() codes.Code {
	return r.code
}

func (r *request) SetToken(token message.Token) {
	r.token = token
}

func (r *request) Token() message.Token {
	return r.token
}

func (r *request) SetOptionUint32(id message.OptionID, value uint32) {
	opts, _, err := r.options.SetUint32(make([]byte, 4), id, value)
	r.options = opts
	if err != nil {
		panic(err)
	}
}

func (r *request) GetOptionUint32(id message.OptionID) (uint32, error) {
	return r.options.GetUint32(id)
}

func (r *request) SetOptionBytes(id message.OptionID, value []byte) {
	opts, _, err := r.options.SetBytes(make([]byte, len(value)), id, value)
	r.options = opts
	if err != nil {
		panic(err)
	}
}

func (r *request) GetOptionBytes(id message.OptionID) ([]byte, error) {
	return r.options.GetBytes(id)
}

func (r *request) Remove(id message.OptionID) {
	r.options = r.options.Remove(id)
	if len(r.options) == 0 {
		r.options = nil
	}
}

func (r *request) ResetOptionsTo(in message.Options) {
	r.options = r.options[:0]
	for _, o := range in {
		v := make([]byte, len(o.Value))
		copy(v, o.Value)
		r.options = r.options.Add(message.Option{
			ID:    o.ID,
			Value: v,
		})
	}
}

func (r *request) Options() message.Options {
	return r.options
}

func (r *request) SetBody(p io.ReadSeeker) {
	r.payload = p
}

func (r *request) Body() io.ReadSeeker {
	return r.payload
}

func (r *request) BodySize() (int64, error) {
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

func acquireMessage(ctx context.Context) Message {
	return &request{
		ctx: ctx,
	}
}

func releaseMessage(r Message) {
	req := r.(*request)
	req.options = nil
	req.token = nil
	req.payload = nil
	req.code = 0
	req.ctx = nil
}

func makeDo(t *testing.T, sender, receiver *BlockWise, senderMaxSZX SZX, senderMaxMessageSize int, receiverMaxSZX SZX, receiverMaxMessageSize int, next func(ResponseWriter, Message)) func(Message) (Message, error) {
	return func(req Message) (Message, error) {
		c := make(chan Message)
		go func() {
			var roReq Message
			roReq = req
			for {
				var resp Message
				receiverResp := newResponseWriter(acquireMessage(roReq.Context()))
				receiver.Handle(receiverResp, roReq, senderMaxSZX, senderMaxMessageSize, next)
				senderResp := newResponseWriter(acquireMessage(roReq.Context()))
				sender.Handle(senderResp, receiverResp.Message(), receiverMaxSZX, receiverMaxMessageSize, func(w ResponseWriter, r Message) {
					resp = r
				})
				if resp != nil {
					c <- resp
					return
				}
				roReq = senderResp.Message()
			}
		}()
		select {
		case resp := <-c:
			return resp, nil
		}
	}
}

func TestBlockWise_Do(t *testing.T) {
	sender := NewBlockWise(acquireMessage, releaseMessage, time.Second*10, func(err error) { t.Log(err) }, true, nil)
	receiver := NewBlockWise(acquireMessage, releaseMessage, time.Second*10, func(err error) { t.Log(err) }, true, nil)
	type args struct {
		r              Message
		szx            SZX
		maxMessageSize int
		do             func(req Message) (Message, error)
	}
	tests := []struct {
		name    string
		args    args
		want    Message
		wantErr bool
	}{
		{
			name: "SZX16-SZX16",
			args: args{
				r: &request{
					ctx:     context.Background(),
					token:   []byte{2},
					options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
					code:    codes.POST,
					payload: bytes.NewReader(make([]byte, 128)),
				},
				szx:            SZX16,
				maxMessageSize: SZX16.Size(),
				do: makeDo(t, sender, receiver, SZX16, SZX16.Size(), SZX16, SZX16.Size(), func(w ResponseWriter, r Message) {
					require.Equal(t, &request{
						ctx:     context.Background(),
						token:   []byte{2},
						options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
						code:    codes.POST,
						payload: memfile.New(make([]byte, 128))}, r)
					w.SetMessage(
						&request{
							ctx:     context.Background(),
							token:   r.Token(),
							code:    codes.Content,
							payload: bytes.NewReader(make([]byte, 17)),
						},
					)
				}),
			},
			want: &request{
				ctx:     context.Background(),
				token:   []byte{2},
				code:    codes.Content,
				payload: memfile.New(make([]byte, 17)),
			},
		},
		{
			name: "SZX16-SZX1024",
			args: args{
				r: &request{
					ctx:     context.Background(),
					token:   []byte{2},
					options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
					code:    codes.POST,
					payload: bytes.NewReader(make([]byte, 128)),
				},
				szx:            SZX16,
				maxMessageSize: SZX16.Size(),
				do: makeDo(t, sender, receiver, SZX16, SZX16.Size(), SZX1024, SZX1024.Size(), func(w ResponseWriter, r Message) {
					require.Equal(t, &request{
						ctx:     context.Background(),
						token:   []byte{2},
						options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
						code:    codes.POST,
						payload: memfile.New(make([]byte, 128))}, r)
					w.SetMessage(
						&request{
							ctx:     context.Background(),
							token:   r.Token(),
							code:    codes.Content,
							payload: bytes.NewReader(make([]byte, 17)),
						},
					)
				}),
			},
			want: &request{
				ctx:     context.Background(),
				token:   []byte{2},
				code:    codes.Content,
				payload: memfile.New(make([]byte, 17)),
			},
		},
		{
			name: "SZXBERT-SZXBERT",
			args: args{
				r: &request{
					ctx:     context.Background(),
					token:   []byte{'B', 'E', 'R', 'T'},
					options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
					code:    codes.POST,
					payload: bytes.NewReader(make([]byte, 11111)),
				},
				szx:            SZXBERT,
				maxMessageSize: SZXBERT.Size() * 2,
				do: makeDo(t, sender, receiver, SZXBERT, SZXBERT.Size()*2, SZXBERT, SZXBERT.Size()*5, func(w ResponseWriter, r Message) {
					require.Equal(t, &request{
						ctx:     context.Background(),
						token:   []byte{'B', 'E', 'R', 'T'},
						options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
						code:    codes.POST,
						payload: memfile.New(make([]byte, 11111))}, r)
					w.SetMessage(
						&request{
							ctx:     context.Background(),
							token:   r.Token(),
							code:    codes.Content,
							payload: bytes.NewReader(make([]byte, 22222)),
						},
					)
				}),
			},
			want: &request{
				ctx:     context.Background(),
				token:   []byte{'B', 'E', 'R', 'T'},
				code:    codes.Content,
				payload: memfile.New(make([]byte, 22222)),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sender.Do(tt.args.r, tt.args.szx, tt.args.maxMessageSize, tt.args.do)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestEncodeBlockOption(t *testing.T) {
	type args struct {
		szx                 SZX
		blockNumber         int
		moreBlocksFollowing bool
	}
	tests := []struct {
		name    string
		args    args
		want    uint32
		wantErr bool
	}{
		{name: "SZX16", args: args{szx: SZX16, blockNumber: 0, moreBlocksFollowing: false}, want: uint32(0)},
		{name: "SZX16", args: args{szx: SZX16, blockNumber: 0, moreBlocksFollowing: true}, want: uint32(8)},
		{name: "SZX32", args: args{szx: SZX32, blockNumber: 0, moreBlocksFollowing: false}, want: uint32(1)},
		{name: "SZX32", args: args{szx: SZX32, blockNumber: 0, moreBlocksFollowing: true}, want: uint32(9)},
		{name: "SZX64", args: args{szx: SZX64, blockNumber: 0, moreBlocksFollowing: false}, want: uint32(2)},
		{name: "SZX64", args: args{szx: SZX64, blockNumber: 0, moreBlocksFollowing: true}, want: uint32(10)},
		{name: "SZX128", args: args{szx: SZX128, blockNumber: 0, moreBlocksFollowing: false}, want: uint32(3)},
		{name: "SZX128", args: args{szx: SZX128, blockNumber: 0, moreBlocksFollowing: true}, want: uint32(11)},
		{name: "SZX256", args: args{szx: SZX256, blockNumber: 0, moreBlocksFollowing: false}, want: uint32(4)},
		{name: "SZX256", args: args{szx: SZX256, blockNumber: 0, moreBlocksFollowing: true}, want: uint32(12)},
		{name: "SZX512", args: args{szx: SZX512, blockNumber: 0, moreBlocksFollowing: false}, want: uint32(5)},
		{name: "SZX512", args: args{szx: SZX512, blockNumber: 0, moreBlocksFollowing: true}, want: uint32(13)},
		{name: "SZX1024", args: args{szx: SZX1024, blockNumber: 0, moreBlocksFollowing: false}, want: uint32(6)},
		{name: "SZX1024", args: args{szx: SZX1024, blockNumber: 0, moreBlocksFollowing: true}, want: uint32(14)},
		{name: "SZXBERT", args: args{szx: SZXBERT, blockNumber: 0, moreBlocksFollowing: false}, want: uint32(7)},
		{name: "SZXBERT", args: args{szx: SZXBERT, blockNumber: 0, moreBlocksFollowing: true}, want: uint32(15)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := EncodeBlockOption(tt.args.szx, tt.args.blockNumber, tt.args.moreBlocksFollowing)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDecodeBlockOption(t *testing.T) {
	type args struct {
		blockVal uint32
	}
	tests := []struct {
		name                    string
		args                    args
		wantSzx                 SZX
		wantBlockNumber         int
		wantMoreBlocksFollowing bool
		wantErr                 bool
	}{
		{name: "SZX16", args: args{blockVal: uint32(0)}, wantSzx: SZX16, wantBlockNumber: 0, wantMoreBlocksFollowing: false},
		{name: "SZX16", args: args{blockVal: uint32(8)}, wantSzx: SZX16, wantBlockNumber: 0, wantMoreBlocksFollowing: true},
		{name: "SZX32", args: args{blockVal: uint32(1)}, wantSzx: SZX32, wantBlockNumber: 0, wantMoreBlocksFollowing: false},
		{name: "SZX32", args: args{blockVal: uint32(9)}, wantSzx: SZX32, wantBlockNumber: 0, wantMoreBlocksFollowing: true},
		{name: "SZX64", args: args{blockVal: uint32(2)}, wantSzx: SZX64, wantBlockNumber: 0, wantMoreBlocksFollowing: false},
		{name: "SZX64", args: args{blockVal: uint32(10)}, wantSzx: SZX64, wantBlockNumber: 0, wantMoreBlocksFollowing: true},
		{name: "SZX128", args: args{blockVal: uint32(3)}, wantSzx: SZX128, wantBlockNumber: 0, wantMoreBlocksFollowing: false},
		{name: "SZX128", args: args{blockVal: uint32(11)}, wantSzx: SZX128, wantBlockNumber: 0, wantMoreBlocksFollowing: true},
		{name: "SZX256", args: args{blockVal: uint32(4)}, wantSzx: SZX256, wantBlockNumber: 0, wantMoreBlocksFollowing: false},
		{name: "SZX256", args: args{blockVal: uint32(12)}, wantSzx: SZX256, wantBlockNumber: 0, wantMoreBlocksFollowing: true},
		{name: "SZX512", args: args{blockVal: uint32(5)}, wantSzx: SZX512, wantBlockNumber: 0, wantMoreBlocksFollowing: false},
		{name: "SZX512", args: args{blockVal: uint32(13)}, wantSzx: SZX512, wantBlockNumber: 0, wantMoreBlocksFollowing: true},
		{name: "SZX1024", args: args{blockVal: uint32(6)}, wantSzx: SZX1024, wantBlockNumber: 0, wantMoreBlocksFollowing: false},
		{name: "SZX1024", args: args{blockVal: uint32(14)}, wantSzx: SZX1024, wantBlockNumber: 0, wantMoreBlocksFollowing: true},
		{name: "SZXBERT", args: args{blockVal: uint32(7)}, wantSzx: SZXBERT, wantBlockNumber: 0, wantMoreBlocksFollowing: false},
		{name: "SZXBERT", args: args{blockVal: uint32(15)}, wantSzx: SZXBERT, wantBlockNumber: 0, wantMoreBlocksFollowing: true},
		{name: "error", args: args{blockVal: 0x1000000}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSzx, gotBlockNumber, gotMoreBlocksFollowing, err := DecodeBlockOption(tt.args.blockVal)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantSzx, gotSzx)
			assert.Equal(t, tt.wantBlockNumber, gotBlockNumber)
			assert.Equal(t, tt.wantMoreBlocksFollowing, gotMoreBlocksFollowing)
		})
	}
}

func makeWriteReq(t *testing.T, sender, receiver *BlockWise, senderMaxSZX SZX, senderMaxMessageSize int, receiverMaxSZX SZX, receiverMaxMessageSize int, next func(ResponseWriter, Message)) func(Message) error {
	return func(req Message) error {
		c := make(chan bool, 1)
		go func() {
			var roReq Message
			roReq = req
			for {
				receiverResp := newResponseWriter(acquireMessage(roReq.Context()))
				receiver.Handle(receiverResp, roReq, senderMaxSZX, senderMaxMessageSize, func(w ResponseWriter, r Message) {
					defer close(c)
					next(w, r)
				})
				senderResp := newResponseWriter(acquireMessage(roReq.Context()))
				sender.Handle(senderResp, receiverResp.Message(), receiverMaxSZX, receiverMaxMessageSize, func(w ResponseWriter, r Message) {
				})
				select {
				case <-c:
					return
				default:
				}
				roReq = senderResp.Message()
			}
		}()
		select {
		case <-c:
			return nil
		}
	}
}

func TestBlockWise_WriteRequest(t *testing.T) {
	sender := NewBlockWise(acquireMessage, releaseMessage, time.Second*10, func(err error) { t.Log(err) }, true, nil)
	receiver := NewBlockWise(acquireMessage, releaseMessage, time.Second*10, func(err error) { t.Log(err) }, true, nil)
	type args struct {
		r              Message
		szx            SZX
		maxMessageSize int
		writeRequest   func(req Message) error
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{

		{
			name: "SZX16-SZX16",
			args: args{
				r: &request{
					ctx:     context.Background(),
					token:   []byte{1},
					options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
					code:    codes.Content,
					payload: bytes.NewReader(make([]byte, 128)),
				},
				szx:            SZX16,
				maxMessageSize: SZX16.Size(),
				writeRequest: makeWriteReq(t, sender, receiver, SZX16, SZX16.Size(), SZX16, SZX16.Size(), func(w ResponseWriter, r Message) {
					require.Equal(t, &request{
						ctx:     context.Background(),
						token:   []byte{1},
						options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
						code:    codes.Content,
						payload: memfile.New(make([]byte, 128))}, r)
				}),
			},
		},
		{
			name: "SZX16-SZX1024",
			args: args{
				r: &request{
					ctx:     context.Background(),
					token:   []byte{2},
					options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
					code:    codes.POST,
					payload: bytes.NewReader(make([]byte, 128)),
				},
				szx:            SZX16,
				maxMessageSize: SZX16.Size(),
				writeRequest: makeWriteReq(t, sender, receiver, SZX16, SZX16.Size(), SZX1024, SZX1024.Size(), func(w ResponseWriter, r Message) {
					require.Equal(t, &request{
						ctx:     context.Background(),
						token:   []byte{2},
						options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
						code:    codes.POST,
						payload: memfile.New(make([]byte, 128))}, r)
				}),
			},
		},
		{
			name: "SZXBERT-SZXBERT",
			args: args{
				r: &request{
					ctx:     context.Background(),
					token:   []byte{'B', 'E', 'R', 'T'},
					options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
					code:    codes.POST,
					payload: bytes.NewReader(make([]byte, 11111)),
				},
				szx:            SZXBERT,
				maxMessageSize: SZXBERT.Size() * 2,
				writeRequest: makeWriteReq(t, sender, receiver, SZXBERT, SZXBERT.Size()*2, SZXBERT, SZXBERT.Size()*5, func(w ResponseWriter, r Message) {
					require.Equal(t, &request{
						ctx:     context.Background(),
						token:   []byte{'B', 'E', 'R', 'T'},
						options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
						code:    codes.POST,
						payload: memfile.New(make([]byte, 11111))}, r)
				}),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := sender.WriteRequest(tt.args.r, tt.args.szx, tt.args.maxMessageSize, tt.args.writeRequest)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}
