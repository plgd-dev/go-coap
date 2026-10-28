package udp

import (
	"bytes"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/mux"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

// rawUDPClient sends hand-crafted messages, because the go-coap client always uses tokens.
type rawUDPClient struct {
	t    *testing.T
	conn net.Conn
	mid  int32
}

func newTokenlessTestServer(t *testing.T, m *mux.Router) *rawUDPClient {
	l, err := coapNet.NewListenUDP("udp", "127.0.0.1:0")
	require.NoError(t, err)
	s := NewServer(options.WithMux(m))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = s.Serve(l)
	}()
	conn, err := net.Dial("udp", l.LocalAddr().String())
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = conn.Close()
		s.Stop()
		wg.Wait()
		_ = l.Close()
	})
	return &rawUDPClient{t: t, conn: conn}
}

func (c *rawUDPClient) get(path string, opts ...message.Option) message.Message {
	return c.do(codes.GET, path, nil, opts...)
}

func (c *rawUDPClient) do(code codes.Code, path string, payload []byte, opts ...message.Option) message.Message {
	c.mid++
	options := append(message.Options{{ID: message.URIPath, Value: []byte(path)}}, opts...)
	// options must be encoded in ascending order
	slices.SortStableFunc(options, func(a, b message.Option) int { return int(a.ID) - int(b.ID) })
	msg := message.Message{
		Code:      code,
		Type:      message.Confirmable,
		MessageID: c.mid,
		Options:   options,
		Payload:   payload,
	}
	buf := make([]byte, 2048)
	n, err := coder.DefaultCoder.Encode(msg, buf)
	require.NoError(c.t, err)
	_, err = c.conn.Write(buf[:n])
	require.NoError(c.t, err)
	return c.receive()
}

func (c *rawUDPClient) receive() message.Message {
	buf := make([]byte, 2048)
	require.NoError(c.t, c.conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	n, err := c.conn.Read(buf)
	require.NoError(c.t, err)
	msg := message.Message{Options: make(message.Options, 0, 16)}
	_, err = coder.DefaultCoder.Decode(buf[:n], &msg)
	require.NoError(c.t, err)
	msg.Payload = append([]byte(nil), msg.Payload...)
	if msg.Type == message.Confirmable {
		ack := make([]byte, 16)
		n, err = coder.DefaultCoder.Encode(message.Message{Type: message.Acknowledgement, MessageID: msg.MessageID}, ack)
		require.NoError(c.t, err)
		_, err = c.conn.Write(ack[:n])
		require.NoError(c.t, err)
	}
	return msg
}

func blockOption(t *testing.T, id message.OptionID, num int64, more bool) message.Option {
	block, err := blockwise.EncodeBlockOption(blockwise.SZX1024, num, more)
	require.NoError(t, err)
	buf := make([]byte, 4)
	n, err := message.EncodeUint32(buf, block)
	require.NoError(t, err)
	return message.Option{ID: id, Value: buf[:n]}
}

func block2Option(t *testing.T, num int64) message.Option {
	return blockOption(t, message.Block2, num, false)
}

// upload sends payload in Block1 blocks with tokenless requests (RFC 7959 §2.5) and returns the final response.
func (c *rawUDPClient) upload(code codes.Code, path string, payload []byte) message.Message {
	const blockSize = 1024
	for num := 0; ; num++ {
		start := num * blockSize
		end := min(start+blockSize, len(payload))
		more := end < len(payload)
		resp := c.do(code, path, payload[start:end], blockOption(c.t, message.Block1, int64(num), more))
		require.Empty(c.t, resp.Token)
		// the server echoes Block1 in the intermediate and in the final response (RFC 7959 §2.3)
		block, err := resp.Options.GetUint32(message.Block1)
		require.NoError(c.t, err)
		_, gotNum, gotMore, err := blockwise.DecodeBlockOption(block)
		require.NoError(c.t, err)
		require.Equal(c.t, int64(num), gotNum)
		require.Equal(c.t, more, gotMore)
		if !more {
			return resp
		}
		require.Equal(c.t, codes.Continue, resp.Code)
	}
}

// fetchRemainingBlocks retrieves the rest of a Block2 representation with tokenless GET requests (RFC 7959 §2.4, §2.6).
func (c *rawUDPClient) fetchRemainingBlocks(path string, first message.Message) []byte {
	return c.fetchRemainingBlocksWithCode(codes.GET, path, first)
}

func (c *rawUDPClient) fetchRemainingBlocksWithCode(code codes.Code, path string, first message.Message) []byte {
	body := append([]byte(nil), first.Payload...)
	resp := first
	for {
		require.Empty(c.t, resp.Token)
		block, err := resp.Options.GetUint32(message.Block2)
		require.NoError(c.t, err)
		_, num, more, err := blockwise.DecodeBlockOption(block)
		require.NoError(c.t, err)
		if !more {
			return body
		}
		resp = c.do(code, path, nil, block2Option(c.t, num+1))
		require.Equal(c.t, first.Code, resp.Code)
		body = append(body, resp.Payload...)
	}
}

func TestServerTokenlessGetBlockwiseDifferentResources(t *testing.T) {
	m := mux.NewRouter()
	payloads := map[string][]byte{}
	for _, path := range []string{"a", "b"} {
		payload := []byte(path + bigPayload10KiB[:2999])
		payloads[path] = payload
		require.NoError(t, m.Handle("/"+path, mux.HandlerFunc(func(w mux.ResponseWriter, _ *mux.Message) {
			require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader(payload)))
		})))
	}
	c := newTokenlessTestServer(t, m)

	// Start /a, then continue /b: the cached /a response must not be served for /b.
	first := c.get("a")
	require.Equal(t, payloads["a"][:1024], first.Payload)
	resp := c.get("b", block2Option(t, 1))
	require.Equal(t, codes.Content, resp.Code)
	require.Equal(t, payloads["b"][1024:2048], resp.Payload)

	require.Equal(t, payloads["a"], c.fetchRemainingBlocks("a", first))
}

func TestServerTokenlessObserveBlockwise(t *testing.T) {
	registration := []byte(bigPayload10KiB[:3000])
	notification := []byte(bigPayload10KiB[100:3100])
	var current atomic.Pointer[[]byte]
	current.Store(&registration)
	m := mux.NewRouter()
	require.NoError(t, m.Handle("/obs", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		obs, err := r.Options().Observe()
		if err != nil || obs != 0 {
			require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader(*current.Load())))
			return
		}
		require.Empty(t, r.Token())
		cc := w.Conn()
		go func() {
			time.Sleep(100 * time.Millisecond)
			current.Store(&notification)
			msg := cc.AcquireMessage(cc.Context())
			defer cc.ReleaseMessage(msg)
			msg.SetCode(codes.Content)
			msg.SetContentFormat(message.TextPlain)
			msg.SetObserve(3)
			msg.SetBody(bytes.NewReader(notification))
			_ = cc.WriteMessage(msg)
		}()
		require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader(registration), message.Option{ID: message.Observe, Value: []byte{2}}))
	})))
	c := newTokenlessTestServer(t, m)

	resp := c.get("obs", message.Option{ID: message.Observe})
	require.Equal(t, codes.Content, resp.Code)
	_, err := resp.Options.Observe()
	require.NoError(t, err)
	require.Equal(t, registration, c.fetchRemainingBlocks("obs", resp))

	notif := c.receive()
	require.Equal(t, codes.Content, notif.Code)
	obs, err := notif.Options.Observe()
	require.NoError(t, err)
	require.Equal(t, uint32(3), obs)
	require.Equal(t, notification, c.fetchRemainingBlocks("obs", notif))
}

func TestServerTokenlessPutBlockwise(t *testing.T) {
	request := []byte(bigPayload10KiB[:3000])
	var received atomic.Pointer[[]byte]
	m := mux.NewRouter()
	require.NoError(t, m.Handle("/put", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		require.Equal(t, codes.PUT, r.Code())
		require.Empty(t, r.Token())
		body, err := r.ReadBody()
		require.NoError(t, err)
		received.Store(&body)
		require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, nil))
	})))
	c := newTokenlessTestServer(t, m)

	resp := c.upload(codes.PUT, "put", request)
	require.Equal(t, codes.Changed, resp.Code)
	require.NotNil(t, received.Load())
	require.Equal(t, request, *received.Load())
}

// TestServerTokenlessPostBlockwise uploads a big request with Block1 and fetches a big response with Block2 (RFC 7959 §3.3).
func TestServerTokenlessPostBlockwise(t *testing.T) {
	request := []byte(bigPayload10KiB[:3000])
	response := []byte(bigPayload10KiB[200:3700])
	var received atomic.Pointer[[]byte]
	var calls atomic.Int32
	m := mux.NewRouter()
	require.NoError(t, m.Handle("/post", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		require.Equal(t, codes.POST, r.Code())
		require.Empty(t, r.Token())
		calls.Add(1)
		body, err := r.ReadBody()
		require.NoError(t, err)
		received.Store(&body)
		require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader(response)))
	})))
	c := newTokenlessTestServer(t, m)

	first := c.upload(codes.POST, "post", request)
	require.Equal(t, codes.Changed, first.Code)
	require.NotNil(t, received.Load())
	require.Equal(t, request, *received.Load())
	require.Equal(t, response[:1024], first.Payload)

	require.Equal(t, response, c.fetchRemainingBlocksWithCode(codes.POST, "post", first))
	// Block2 continuations are served from the cached response, the handler is called only once
	require.Equal(t, int32(1), calls.Load())
}
