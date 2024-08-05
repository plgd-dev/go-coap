package blockwise

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/dsnet/golib/memfile"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testmessage struct {
	code     codes.Code
	ctx      context.Context
	token    message.Token
	options  message.Options
	payload  io.ReadSeeker
	sequence uint64
}

func toPoolMessage(m *testmessage) *pool.Message {
	msg := pool.NewMessage(m.ctx)
	msg.SetCode(m.code)
	msg.SetToken(m.token)
	msg.ResetOptionsTo(m.options)
	msg.SetBody(m.payload)
	msg.SetSequence(m.sequence)
	return msg
}

func fromPoolMessage(m *pool.Message) *testmessage {
	opts := m.Options()
	if len(opts) == 0 {
		opts = nil
	}
	return &testmessage{
		code:     m.Code(),
		ctx:      m.Context(),
		token:    m.Token(),
		options:  opts,
		payload:  m.Body(),
		sequence: m.Sequence(),
	}
}

type testClient struct {
	p *pool.Pool
}

func (c *testClient) RemoteAddr() net.Addr {
	return &net.IPAddr{IP: net.IPv4(127, 0, 0, 1)}
}

func newTestClient() *testClient {
	return &testClient{
		p: pool.New(100, 1024),
	}
}

func (c *testClient) AcquireMessage(ctx context.Context) *pool.Message {
	return c.p.AcquireMessage(ctx)
}

func (c *testClient) ReleaseMessage(m *pool.Message) {
	c.p.ReleaseMessage(m)
}

func makeDo[C Client](t *testing.T, sender, receiver *BlockWise[C], senderMaxSZX SZX, senderMaxMessageSize uint32, receiverMaxSZX SZX, receiverMaxMessageSize uint32, next func(*responsewriter.ResponseWriter[C], *pool.Message)) func(*pool.Message) (*pool.Message, error) {
	return func(req *pool.Message) (*pool.Message, error) {
		c := make(chan *pool.Message)
		go func() {
			var roReq *pool.Message
			roReq = req
			for {
				var resp *pool.Message
				receiverResp := responsewriter.New(receiver.cc.AcquireMessage(roReq.Context()), receiver.cc)
				receiver.Handle(receiverResp, roReq, senderMaxSZX, senderMaxMessageSize, next)
				t.Logf("receiver.Handle - receiverResp %v senderResp.Message: %v\n", receiverResp.Message(), roReq)
				senderResp := responsewriter.New(sender.cc.AcquireMessage(roReq.Context()), sender.cc)
				sender.Handle(senderResp, receiverResp.Message(), receiverMaxSZX, receiverMaxMessageSize, func(_ *responsewriter.ResponseWriter[C], r *pool.Message) {
					resp = r
				})
				t.Logf("sender.Handle - senderResp %v receiverResp.Message: %v\n", senderResp.Message(), receiverResp.Message())
				if resp != nil {
					c <- resp
					return
				}
				roReq = senderResp.Message()
			}
		}()
		resp := <-c
		return resp, nil
	}
}

func TestBlockWiseDo(t *testing.T) {
	sender := New(newTestClient(), time.Second*3600, func(err error) { t.Log(err) }, nil)
	receiver := New(newTestClient(), time.Second*3600, func(err error) { t.Log(err) }, nil)
	type args struct {
		r              *testmessage
		szx            SZX
		maxMessageSize int64
		do             func(req *pool.Message) (*pool.Message, error)
	}
	tests := []struct {
		name    string
		args    args
		want    *testmessage
		wantErr bool
	}{
		{
			name: "SZX16-SZX16",
			args: args{
				r: &testmessage{
					ctx:     context.Background(),
					token:   []byte{2},
					options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
					code:    codes.POST,
					payload: bytes.NewReader(make([]byte, 128)),
				},
				szx:            SZX16,
				maxMessageSize: SZX16.Size(),
				do: makeDo(t, sender, receiver, SZX16, uint32(SZX16.Size()), SZX16, uint32(SZX16.Size()), func(w *responsewriter.ResponseWriter[*testClient], r *pool.Message) {
					require.Equal(t, &testmessage{
						ctx:     context.Background(),
						token:   []byte{2},
						options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
						code:    codes.POST,
						payload: memfile.New(make([]byte, 128)),
					}, fromPoolMessage(r))
					w.SetMessage(
						toPoolMessage(&testmessage{
							ctx:     context.Background(),
							token:   r.Token(),
							code:    codes.Changed,
							payload: bytes.NewReader(make([]byte, 17)),
						}),
					)
				}),
			},
			want: &testmessage{
				ctx:     context.Background(),
				token:   []byte{2},
				code:    codes.Changed,
				payload: memfile.New(make([]byte, 17)),
			},
		},
		{
			name: "SZX16-SZX1024",
			args: args{
				r: &testmessage{
					ctx:     context.Background(),
					token:   []byte{2},
					options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
					code:    codes.POST,
					payload: bytes.NewReader(make([]byte, 128)),
				},
				szx:            SZX16,
				maxMessageSize: SZX16.Size(),
				do: makeDo(t, sender, receiver, SZX16, uint32(SZX16.Size()), SZX1024, uint32(SZX1024.Size()), func(w *responsewriter.ResponseWriter[*testClient], r *pool.Message) {
					require.Equal(t, &testmessage{
						ctx:     context.Background(),
						token:   []byte{2},
						options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
						code:    codes.POST,
						payload: memfile.New(make([]byte, 128)),
					}, fromPoolMessage(r))
					w.SetMessage(
						toPoolMessage(&testmessage{
							ctx:     context.Background(),
							token:   r.Token(),
							code:    codes.Changed,
							payload: bytes.NewReader(make([]byte, 17)),
						}),
					)
				}),
			},
			want: &testmessage{
				ctx:     context.Background(),
				token:   []byte{2},
				code:    codes.Changed,
				payload: memfile.New(make([]byte, 17)),
			},
		},
		{
			name: "SZXBERT-SZXBERT",
			args: args{
				r: &testmessage{
					ctx:     context.Background(),
					token:   []byte{'B', 'E', 'R', 'T'},
					options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
					code:    codes.POST,
					payload: bytes.NewReader(make([]byte, 11111)),
				},
				szx:            SZXBERT,
				maxMessageSize: SZXBERT.Size() * 2,
				do: makeDo(t, sender, receiver, SZXBERT, uint32(SZXBERT.Size()*2), SZXBERT, uint32(SZXBERT.Size()*2), func(w *responsewriter.ResponseWriter[*testClient], r *pool.Message) {
					require.Equal(t, &testmessage{
						ctx:     context.Background(),
						token:   []byte{'B', 'E', 'R', 'T'},
						options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
						code:    codes.POST,
						payload: memfile.New(make([]byte, 11111)),
					}, fromPoolMessage(r))
					w.SetMessage(
						toPoolMessage(&testmessage{
							ctx:     context.Background(),
							token:   r.Token(),
							code:    codes.Changed,
							payload: bytes.NewReader(make([]byte, 22222)),
						}),
					)
				}),
			},
			want: &testmessage{
				ctx:     context.Background(),
				token:   []byte{'B', 'E', 'R', 'T'},
				code:    codes.Changed,
				payload: memfile.New(make([]byte, 22222)),
			},
		},

		{
			name: "PUT-SZX16-SZX16",
			args: args{
				r: &testmessage{
					ctx:     context.Background(),
					token:   []byte{2},
					options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
					code:    codes.PUT,
					payload: bytes.NewReader(make([]byte, 128)),
				},
				szx:            SZX16,
				maxMessageSize: SZX16.Size(),
				do: makeDo(t, sender, receiver, SZX16, uint32(SZX16.Size()), SZX16, uint32(SZX16.Size()), func(w *responsewriter.ResponseWriter[*testClient], r *pool.Message) {
					require.Equal(t, &testmessage{
						ctx:     context.Background(),
						token:   []byte{2},
						options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
						code:    codes.PUT,
						payload: memfile.New(make([]byte, 128)),
					}, fromPoolMessage(r))
					w.SetMessage(
						toPoolMessage(&testmessage{
							ctx:     context.Background(),
							token:   r.Token(),
							code:    codes.Created,
							payload: bytes.NewReader(make([]byte, 17)),
						}),
					)
				}),
			},
			want: &testmessage{
				ctx:     context.Background(),
				token:   []byte{2},
				code:    codes.Created,
				payload: memfile.New(make([]byte, 17)),
			},
		},
		{
			name: "GET-SZX16-SZX16",
			args: args{
				r: &testmessage{
					ctx:     context.Background(),
					token:   []byte{2},
					options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
					code:    codes.GET,
				},
				szx:            SZX16,
				maxMessageSize: SZX16.Size(),
				do: makeDo(t, sender, receiver, SZX16, uint32(SZX16.Size()), SZX16, uint32(SZX16.Size()), func(w *responsewriter.ResponseWriter[*testClient], r *pool.Message) {
					require.Equal(t, &testmessage{
						ctx:     context.Background(),
						token:   []byte{2},
						options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
						code:    codes.GET,
					}, fromPoolMessage(r))
					w.SetMessage(
						toPoolMessage(&testmessage{
							ctx:     context.Background(),
							token:   r.Token(),
							code:    codes.Content,
							payload: bytes.NewReader(make([]byte, 399)),
						}),
					)
				}),
			},
			want: &testmessage{
				ctx:     context.Background(),
				token:   []byte{2},
				code:    codes.Content,
				payload: memfile.New(make([]byte, 399)),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sender.Do(toPoolMessage(tt.args.r), tt.args.szx, uint32(tt.args.maxMessageSize), tt.args.do)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			receivingMessagesCache := sender.receivingMessagesCache.LoadAndDeleteAll()
			require.Empty(t, receivingMessagesCache)
			sendingMessagesCache := sender.sendingMessagesCache.LoadAndDeleteAll()
			require.Empty(t, sendingMessagesCache)
			require.NoError(t, err)
			assert.Equal(t, tt.want, fromPoolMessage(got))
		})
	}
}

/*
func TestBlockWiseParallel(t *testing.T) {
	sender := New(newTestClient(), time.Second*3600, func(err error) { t.Log(err) }, nil)
	receiver := New(newTestClient(), time.Second*3600, func(err error) { t.Log(err) }, nil)
	type args struct {
		r              *testmessage
		szx            SZX
		maxMessageSize int64
		do             func(req *pool.Message) (*pool.Message, error)
	}
	tests := []struct {
		name    string
		args    args
		want    *testmessage
		wantErr bool
	}{
		{
			name: "SZX16-SZX1024",
			args: args{
				r: &testmessage{
					ctx:     context.Background(),
					token:   []byte{2},
					options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
					code:    codes.POST,
					payload: bytes.NewReader(make([]byte, 20000)),
				},
				szx:            SZX16,
				maxMessageSize: SZX16.Size(),
				do: makeDo(t, sender, receiver, SZX16, uint32(SZX16.Size()), SZX1024, uint32(SZX1024.Size()), func(w *responsewriter.ResponseWriter[*testClient], r *pool.Message) {
					require.Equal(t, &testmessage{
						ctx:     context.Background(),
						token:   []byte{2},
						options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
						code:    codes.POST,
						payload: memfile.New(make([]byte, 20000)),
					}, fromPoolMessage(r))
					w.SetMessage(
						toPoolMessage(&testmessage{
							ctx:     context.Background(),
							token:   r.Token(),
							code:    codes.Changed,
							payload: bytes.NewReader(make([]byte, 30000)),
						}),
					)
				}),
			},
			want: &testmessage{
				ctx:     context.Background(),
				token:   []byte{2},
				code:    codes.Changed,
				payload: memfile.New(make([]byte, 30000)),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var wg sync.WaitGroup
			defer wg.Wait()
			r := toPoolMessage(tt.args.r)
			bodysize, err := r.BodySize()
			require.NoError(t, err)
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					req := &testmessage{
						ctx:     r.Context(),
						token:   r.Token(),
						options: r.Options(),
						code:    r.Code(),
						payload: bytes.NewReader(make([]byte, bodysize)),
					}
					got, err := sender.Do(toPoolMessage(req), tt.args.szx, uint32(tt.args.maxMessageSize), tt.args.do)
					if tt.wantErr {
						require.Error(t, err)
						return
					}
					require.NoError(t, err)
					assert.Equal(t, tt.want, fromPoolMessage(got))
				}()
			}
		})
	}
}
*/

func TestEncodeBlockOption(t *testing.T) {
	type args struct {
		szx                 SZX
		blockNumber         int64
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
				require.Error(t, err)
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
		wantBlockNumber         int64
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
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantSzx, gotSzx)
			assert.Equal(t, tt.wantBlockNumber, gotBlockNumber)
			assert.Equal(t, tt.wantMoreBlocksFollowing, gotMoreBlocksFollowing)
		})
	}
}

func makeWriteReq[C Client](sender, receiver *BlockWise[C], senderMaxSZX SZX, senderMaxMessageSize uint32, receiverMaxSZX SZX, receiverMaxMessageSize uint32, next func(*responsewriter.ResponseWriter[C], *pool.Message)) func(*pool.Message) error {
	return func(req *pool.Message) error {
		c := make(chan bool, 1)
		go func() {
			var roReq *pool.Message
			roReq = req
			for {
				receiverResp := responsewriter.New(receiver.cc.AcquireMessage(roReq.Context()), receiver.cc)
				receiver.Handle(receiverResp, roReq, senderMaxSZX, senderMaxMessageSize, func(w *responsewriter.ResponseWriter[C], r *pool.Message) {
					defer close(c)
					next(w, r)
				})
				senderResp := responsewriter.New(sender.cc.AcquireMessage(roReq.Context()), sender.cc)
				orig := senderResp.Message()
				sender.Handle(senderResp, receiverResp.Message(), receiverMaxSZX, receiverMaxMessageSize, func(*responsewriter.ResponseWriter[C], *pool.Message) {
				})
				if orig == senderResp.Message() {
					select {
					case <-c:
						return
					default:
						close(c)
					}
				}
				select {
				case <-c:
					return
				default:
				}
				roReq = senderResp.Message()
			}
		}()
		<-c
		return nil
	}
}

func TestBlockWiseWriteTestMessage(t *testing.T) {
	sender := New(newTestClient(), time.Second*3600, func(err error) { t.Log(err) }, nil)
	receiver := New(newTestClient(), time.Second*3600, func(err error) { t.Log(err) }, nil)
	type args struct {
		r                *testmessage
		szx              SZX
		maxMessageSize   int64
		writetestmessage func(req *pool.Message) error
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "SZX16-SZX16",
			args: args{
				r: &testmessage{
					ctx:     context.Background(),
					token:   []byte{1},
					options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
					code:    codes.Content,
					payload: bytes.NewReader(make([]byte, 128)),
				},
				szx:            SZX16,
				maxMessageSize: SZX16.Size(),
				writetestmessage: makeWriteReq(sender, receiver, SZX16, uint32(SZX16.Size()), SZX16, uint32(SZX16.Size()), func(_ *responsewriter.ResponseWriter[*testClient], r *pool.Message) {
					require.Failf(t, "Unexpected msg", "Received unexpected message: %+v", r)
				}),
			},
		},
		{
			name: "SZX16-SZX1024",
			args: args{
				r: &testmessage{
					ctx:     context.Background(),
					token:   []byte{2},
					options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
					code:    codes.POST,
					payload: bytes.NewReader(make([]byte, 128)),
				},
				szx:            SZX16,
				maxMessageSize: SZX16.Size(),
				writetestmessage: makeWriteReq(sender, receiver, SZX16, uint32(SZX16.Size()), SZX1024, uint32(SZX1024.Size()), func(_ *responsewriter.ResponseWriter[*testClient], r *pool.Message) {
					require.Equal(t, &testmessage{
						ctx:     context.Background(),
						token:   []byte{2},
						options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
						code:    codes.POST,
						payload: memfile.New(make([]byte, 128)),
					}, fromPoolMessage(r))
				}),
			},
		},
		{
			name: "SZXBERT-SZXBERT",
			args: args{
				r: &testmessage{
					ctx:     context.Background(),
					token:   []byte{'B', 'E', 'R', 'T'},
					options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
					code:    codes.POST,
					payload: bytes.NewReader(make([]byte, 11111)),
				},
				szx:            SZXBERT,
				maxMessageSize: SZXBERT.Size() * 2,
				writetestmessage: makeWriteReq(sender, receiver, SZXBERT, uint32(SZXBERT.Size()*2), SZXBERT, uint32(SZXBERT.Size()*5), func(_ *responsewriter.ResponseWriter[*testClient], r *pool.Message) {
					require.Equal(t, &testmessage{
						ctx:     context.Background(),
						token:   []byte{'B', 'E', 'R', 'T'},
						options: message.Options{message.Option{ID: message.URIPath, Value: []byte("abc")}},
						code:    codes.POST,
						payload: memfile.New(make([]byte, 11111)),
					}, fromPoolMessage(r))
				}),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := sender.WriteMessage(toPoolMessage(tt.args.r), tt.args.szx, uint32(tt.args.maxMessageSize), tt.args.writetestmessage)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}
