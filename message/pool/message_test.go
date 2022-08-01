package pool_test

import (
	"context"
	"strings"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/test/net"
	"github.com/stretchr/testify/require"
)

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
			args: args{p: "/a/b/c"},
			want: "/a/b/c",
		},
		{
			name: "Bath with duplicit slashes",
			args: args{p: "/a///b//c/"},
			want: "/a/b/c",
		},
		{
			name: "Path without first slash",
			args: args{p: "a/b/c"},
			want: "/a/b/c",
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
		size = size * 4
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
		size = size * 4
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
			args: args{p: "/a/b/c"},
			want: "/a/b/c",
		},
		{
			name: "Bath with duplicit slashes",
			args: args{p: "/a///b//c/"},
			want: "/a/b/c",
		},
		{
			name: "Path without first slash",
			args: args{p: "a/b/c"},
			want: "/a/b/c",
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
