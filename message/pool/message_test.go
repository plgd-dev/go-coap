package pool_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/tcp/coder"
	"github.com/plgd-dev/go-coap/v3/test/net"
	"github.com/stretchr/testify/require"
)

const testPathABC = "/a/b/c"

func TestSetupRFC8132Requests(t *testing.T) {
	tests := []struct {
		name  string
		code  codes.Code
		setup func(*pool.Message, string, message.Token, io.ReadSeeker) error
	}{
		{"FETCH", codes.FETCH, func(m *pool.Message, path string, token message.Token, body io.ReadSeeker) error {
			return m.SetupFetch(path, token, message.AppOctets, body)
		}},
		{"PATCH", codes.PATCH, func(m *pool.Message, path string, token message.Token, body io.ReadSeeker) error {
			return m.SetupPatch(path, token, message.AppOctets, body)
		}},
		{"iPATCH", codes.IPATCH, func(m *pool.Message, path string, token message.Token, body io.ReadSeeker) error {
			return m.SetupIPatch(path, token, message.AppOctets, body)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := pool.NewMessage(context.Background())
			token := message.Token{1, 2}
			require.NoError(t, tt.setup(req, "/resource", token, bytes.NewReader([]byte("payload"))))
			require.Equal(t, tt.code, req.Code())
			require.Equal(t, token, req.Token())
			path, err := req.Path()
			require.NoError(t, err)
			require.Equal(t, "/resource", path)
			format, err := req.ContentFormat()
			require.NoError(t, err)
			require.Equal(t, message.AppOctets, format)
			body, err := req.ReadBody()
			require.NoError(t, err)
			require.Equal(t, []byte("payload"), body)

			invalidPath := pool.NewMessage(context.Background())
			require.Error(t, tt.setup(invalidPath, strings.Repeat("x", 256), token, bytes.NewReader([]byte("payload"))))
		})
	}

	fetch := pool.NewMessage(context.Background())
	require.NoError(t, fetch.SetupFetch("/resource", message.Token{1}, message.AppOctets, nil))
	require.False(t, fetch.HasOption(message.ContentFormat), "empty FETCH requests do not need Content-Format")

	for _, setup := range []func() error{
		func() error { return fetch.SetupFetch("/resource", message.Token{1}, message.AppOctets, nil) },
		func() error { return fetch.SetupPatch("/resource", message.Token{1}, message.AppOctets, nil) },
		func() error { return fetch.SetupIPatch("/resource", message.Token{1}, message.AppOctets, nil) },
	} {
		require.NoError(t, setup())
		require.False(t, fetch.HasOption(message.ContentFormat), "empty requests do not need Content-Format")
	}
}

func TestPoolAcquireClearsHijack(t *testing.T) {
	p := pool.New(1, 2048)
	for i := 0; i < 64; i++ {
		msg := p.AcquireMessage(context.Background())
		require.False(t, msg.IsHijacked(), "a recycled message must not retain its previous owner's hijack")
		msg.Hijack()
		p.ReleaseMessage(msg)
	}
}

func TestPoolReleaseMessageIfNotHijacked(t *testing.T) {
	p := pool.New(1, 2048)
	for i := 0; i < 64; i++ {
		msg := p.AcquireMessage(context.Background())
		generation := msg.Generation()
		msg.Hijack()
		p.ReleaseMessageIfNotHijacked(msg, generation)
		require.True(t, msg.IsHijacked())
		p.ReleaseMessage(msg)

		next := p.AcquireMessage(context.Background())
		next.SetMessageID(42)
		p.ReleaseMessageIfNotHijacked(msg, generation)
		require.Equal(t, int32(42), next.MessageID(), "stale cleanup must not reset a reused acquisition")
		p.ReleaseMessageIfNotHijacked(next, next.Generation())
	}
}

func TestPoolConcurrentReleaseClaimsMessageOnce(t *testing.T) {
	p := pool.New(2, 2048)
	for i := 0; i < 64; i++ {
		msg := p.AcquireMessage(context.Background())
		generation := msg.Generation()
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			p.ReleaseMessage(msg)
		}()
		go func() {
			defer wg.Done()
			<-start
			p.ReleaseMessageIfNotHijacked(msg, generation)
		}()
		close(start)
		wg.Wait()

		first := p.AcquireMessage(context.Background())
		second := p.AcquireMessage(context.Background())
		require.NotSame(t, first, second, "release must not publish the same message twice")
		first.SetMessageID(42)
		second.SetMessageID(43)
		p.ReleaseMessageIfNotHijacked(msg, generation)
		require.Equal(t, int32(42), first.MessageID())
		require.Equal(t, int32(43), second.MessageID())
		p.ReleaseMessage(first)
		p.ReleaseMessage(second)
	}
}

func TestMessageSetPath(t *testing.T) {
	type args struct {
		p string
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
		want    string
	}{
		{
			name:    "Empty",
			args:    args{p: ""},
			wantErr: true,
		},
		{
			name:    "Empty (slash)",
			args:    args{p: "/"},
			wantErr: true,
		},
		{
			name:    "Empty (multiple slashes)",
			args:    args{p: "//////////"},
			wantErr: true,
		},
		{
			name: "Basic path",
			args: args{p: testPathABC},
			want: testPathABC,
		},
		{
			name: "Bath with duplicit slashes",
			args: args{p: "/a///b//c/"},
			want: testPathABC,
		},
		{
			name: "Path without first slash",
			args: args{p: "a/b/c"},
			want: testPathABC,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := pool.NewMessage(context.Background())
			err := msg.SetPath(tt.args.p)
			require.NoError(t, err)
			path, err := msg.Path()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, path)
		})
	}
}

var maxURIPathLen = int(message.CoapOptionDefs[message.URIPath].MaxLen)

// URL is split by "/" into URI-Path options, however the maximal length
// of an URI-Path option is 255, so if a path segment is longer than the
// maximal length an error should be returned.
func TestMessageSetPathOptionLength(t *testing.T) {
	size := 4
	// try strings of length [4, 16, .., 65536]
	for i := 0; i < 8; i++ {
		msg := pool.NewMessage(context.Background())
		inPath := net.RandomURLString(size)
		wantErr := size-1 > maxURIPathLen // -1 for the starting '/'
		err := msg.SetPath(inPath)
		if wantErr {
			require.Error(t, err)
			continue
		}
		outPath, err := msg.Path()
		require.NoError(t, err)
		require.Equal(t, net.NormalizeURLPath(inPath), outPath)
		size *= 4
	}
}

func TestMessageSetPathValidLength(t *testing.T) {
	size := 4
	// try strings of length [4, 16, .., 65536]
	for i := 0; i < 8; i++ {
		msg := pool.NewMessage(context.Background())
		inPath := net.RandomValidURLString(size, maxURIPathLen)
		err := msg.SetPath(inPath)
		require.NoError(t, err)
		outPath, err := msg.Path()
		require.NoError(t, err)
		require.Equal(t, net.NormalizeURLPath(inPath), outPath)
		size *= 4
	}
}

func TestMessageMustSetPath(t *testing.T) {
	type args struct {
		p string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "Basic path",
			args: args{p: testPathABC},
			want: testPathABC,
		},
		{
			name: "Bath with duplicit slashes",
			args: args{p: "/a///b//c/"},
			want: testPathABC,
		},
		{
			name: "Path without first slash",
			args: args{p: "a/b/c"},
			want: testPathABC,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := pool.NewMessage(context.Background())
			msg.MustSetPath(tt.args.p)
			path, err := msg.Path()
			require.NoError(t, err)
			require.Equal(t, tt.want, path)
		})
	}
}

func TestMessageAddQuery(t *testing.T) {
	type args struct {
		queries []string
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name:    "Empty query",
			wantErr: true,
		},
		{
			name: "Single query",
			args: args{
				queries: []string{"a"},
			},
		},
		{
			name: "Multiple queries",
			args: args{
				queries: []string{"ab", "cdef", "ghijklmn"},
			},
		},
		{
			name: "Long query",
			args: args{
				queries: []string{strings.Repeat("q", 4096)},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := pool.NewMessage(context.Background())
			for _, q := range tt.args.queries {
				msg.AddQuery(q)
			}
			queries, err := msg.Queries()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.args.queries, queries)
		})
	}
}

func TestMessageAddETags(t *testing.T) {
	msg := pool.NewMessage(context.Background())
	err := msg.AddETag([]byte{})
	require.Error(t, err)

	opts := make([][]byte, 2)
	opts[0] = []byte{13, 37}
	opts[1] = []byte{14, 42}
	err = msg.AddETag(opts[0])
	require.NoError(t, err)
	err = msg.AddETag(opts[1])
	require.NoError(t, err)

	maxETagPathLen := int(message.CoapOptionDefs[message.ETag].MaxLen)
	err = msg.AddETag([]byte(strings.Repeat("a", maxETagPathLen+1)))
	require.Error(t, err)

	buf := make([][]byte, 0)
	n, err := msg.ETags(buf)
	require.Error(t, err)
	buf = make([][]byte, n)
	n, err = msg.ETags(buf)
	require.NoError(t, err)
	require.Equal(t, len(opts), n)
	for i, v := range opts {
		t.Logf("verifying index: %v", i)
		require.Equal(t, v, buf[i])
	}
}

func TestMessageSetETag(t *testing.T) {
	maxETagPathLen := int(message.CoapOptionDefs[message.ETag].MaxLen)

	type args struct {
		value []byte
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name:    "Empty ETag",
			wantErr: true,
		},
		{
			name: "Basic ETag",
			args: args{
				value: []byte{13, 37},
			},
		},
		{
			name: "Too long ETag",
			args: args{
				value: []byte(strings.Repeat("a", maxETagPathLen+1)),
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := pool.NewMessage(context.Background())
			err := msg.SetETag(tt.args.value)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			value, err := msg.ETag()
			require.NoError(t, err)
			require.Equal(t, tt.args.value, value)
		})
	}
}

func TestMessageETag(t *testing.T) {
	msg := pool.NewMessage(context.Background())
	require.False(t, msg.HasOption(message.ETag))
	_, err := msg.ETag()
	require.Error(t, err)

	etag := []byte{13, 37}
	err = msg.SetETag(etag)
	require.NoError(t, err)
	value, err := msg.ETag()
	require.NoError(t, err)
	require.Equal(t, etag, value)

	msg.Remove(message.ETag)
	require.False(t, msg.HasOption(message.ETag))
	_, err = msg.ETag()
	require.Error(t, err)

	maxETagPathLen := int(message.CoapOptionDefs[message.ETag].MaxLen)
	etag = make([]byte, 0)
	for i := 1; i <= maxETagPathLen; i++ {
		etag = append(etag, byte(i))
		err = msg.SetETag(etag)
		require.NoError(t, err)
	}
	value, err = msg.ETag()
	require.NoError(t, err)
	require.Equal(t, etag, value)
}

func TestMessageEcho(t *testing.T) {
	msg := pool.NewMessage(context.Background())
	value, err := msg.Echo()
	require.ErrorIs(t, err, message.ErrOptionNotFound)
	require.Nil(t, value)

	for size := 1; size <= 40; size++ {
		echo := bytes.Repeat([]byte{byte(size)}, size)
		require.NoError(t, msg.SetEcho(echo))
		echo[0] = 99
		value, err = msg.Echo()
		require.NoError(t, err)
		require.Equal(t, bytes.Repeat([]byte{byte(size)}, size), value)
		require.Len(t, msg.Options(), 1)
	}

	for _, invalid := range [][]byte{nil, {}, make([]byte, 41)} {
		require.ErrorIs(t, msg.SetEcho(invalid), message.ErrInvalidValueLength)
		value, err = msg.Echo()
		require.NoError(t, err)
		require.Equal(t, bytes.Repeat([]byte{40}, 40), value)
	}

	msg.Remove(message.Echo)
	require.False(t, msg.HasOption(message.Echo))
	_, err = msg.Echo()
	require.ErrorIs(t, err, message.ErrOptionNotFound)
}

func TestMessageRequestTags(t *testing.T) {
	msg := pool.NewMessage(context.Background())
	_, err := msg.RequestTag()
	require.ErrorIs(t, err, message.ErrOptionNotFound)

	first := []byte{1, 2}
	require.NoError(t, msg.AddRequestTag(first))
	first[0] = 9
	require.NoError(t, msg.AddRequestTag([]byte{3}))
	value, err := msg.RequestTag()
	require.NoError(t, err)
	require.Equal(t, []byte{1, 2}, value)

	n, err := msg.RequestTags(nil)
	require.Error(t, err)
	require.Equal(t, 2, n)
	values := make([][]byte, n)
	n, err = msg.RequestTags(values)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.Equal(t, [][]byte{{1, 2}, {3}}, values)

	require.ErrorIs(t, msg.AddRequestTag(make([]byte, 9)), message.ErrInvalidValueLength)
	require.ErrorIs(t, msg.SetRequestTag(make([]byte, 9)), message.ErrInvalidValueLength)
	n, err = msg.RequestTags(values)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.Equal(t, [][]byte{{1, 2}, {3}}, values)

	for size := 0; size <= 8; size++ {
		tag := bytes.Repeat([]byte{byte(size)}, size)
		require.NoError(t, msg.SetRequestTag(tag))
		if size > 0 {
			tag[0] = 99
		}
		n, err = msg.RequestTags(values)
		require.NoError(t, err)
		require.Equal(t, 1, n)
		require.Equal(t, bytes.Repeat([]byte{byte(size)}, size), values[0])
		require.True(t, msg.HasOption(message.RequestTag))
	}

	require.NoError(t, msg.SetRequestTag(nil))
	require.True(t, msg.HasOption(message.RequestTag))
	value, err = msg.RequestTag()
	require.NoError(t, err)
	require.Empty(t, value)
	require.NoError(t, msg.AddRequestTag(nil))
	n, err = msg.RequestTags(values)
	require.NoError(t, err)
	require.Equal(t, 2, n)

	msg.Remove(message.RequestTag)
	require.False(t, msg.HasOption(message.RequestTag))
	_, err = msg.RequestTag()
	require.ErrorIs(t, err, message.ErrOptionNotFound)
}

type malFuncSeeker struct{}

func (m malFuncSeeker) Read([]byte) (int, error) {
	return 0, nil
}

func (m malFuncSeeker) Seek(int64, int) (int64, error) {
	return 0, errors.New("seek error")
}

type malFuncReader struct{}

func (m malFuncReader) Read([]byte) (int, error) {
	return 0, errors.New("read error")
}

func (m malFuncReader) Seek(int64, int) (int64, error) {
	return 0, nil
}

func TestMessageClone(t *testing.T) {
	original := pool.NewMessage(context.Background())
	original.SetMessageID(1)
	original.SetType(message.Confirmable)
	err := original.SetPath("/test")
	require.NoError(t, err)
	original.AddQuery("q1")
	original.AddQuery("q2")
	original.SetBody(bytes.NewReader([]byte("test body")))

	cloned := pool.NewMessage(original.Context())
	err = original.Clone(cloned)
	require.NoError(t, err)

	require.Equal(t, original.MessageID(), cloned.MessageID())
	require.Equal(t, original.Type(), cloned.Type())
	originalPath, err := original.Path()
	require.NoError(t, err)
	clonedPath, err := cloned.Path()
	require.NoError(t, err)
	require.Equal(t, originalPath, clonedPath)
	originalQueries, err := original.Queries()
	require.NoError(t, err)
	clonedQueries, err := cloned.Queries()
	require.NoError(t, err)
	require.Equal(t, originalQueries, clonedQueries)
	originalBody, err := original.ReadBody()
	require.NoError(t, err)
	clonedBody, err := cloned.ReadBody()
	require.NoError(t, err)
	require.Equal(t, originalBody, clonedBody)

	original.SetBody(malFuncSeeker{})
	err = original.Clone(cloned)
	require.Error(t, err)
	require.Equal(t, "seek error", err.Error())

	original.SetBody(malFuncReader{})
	err = original.Clone(cloned)
	require.Error(t, err)
	require.Contains(t, err.Error(), "read error")
}

func TestUnmarshalMessageWithMultipleOptions(t *testing.T) {
	tests := []struct {
		numOptions int
	}{
		{numOptions: 0},
		{numOptions: 8},
		{numOptions: 16},
		{numOptions: 32},
		{numOptions: 64},
		{numOptions: 1023},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("num-options-%v", tt.numOptions), func(t *testing.T) {
			req := pool.NewMessage(context.Background())
			for i := 0; i < 64; i++ {
				req.AddOptionUint32(message.URIQuery, uint32(i))
			}
			data, err := req.MarshalWithEncoder(coder.DefaultCoder)
			require.NoError(t, err)
			msg := pool.NewMessage(context.Background())
			n, err := msg.UnmarshalWithDecoder(coder.DefaultCoder, data)
			require.NoError(t, err)
			require.Len(t, data, n)
			require.Equal(t, req.Options(), msg.Options())
		})
	}
}

func TestUnmarshalMessageWithZeroOptionCapacity(t *testing.T) {
	req := pool.NewMessage(context.Background())
	req.AddOptionString(message.URIPath, "resource")
	data, err := req.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)

	msg := pool.NewMessage(context.Background())
	msg.SetMessage(message.Message{})
	n, err := msg.UnmarshalWithDecoder(coder.DefaultCoder, data)
	require.NoError(t, err)
	require.Len(t, data, n)
	require.Equal(t, req.Options(), msg.Options())
}
