package pool_test

import (
	"math/rand"
	"regexp"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/pool"
	"github.com/stretchr/testify/require"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

const (
	// 71 allowed letters in URL path segment
	urlLetterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ-._~!$&'()*+,;=:@"

	urlLetterIdxBits = 7                       // we need 7 bits to represent a letter index (0..70)
	urlLetterIdxMask = 1<<urlLetterIdxBits - 1 // All 1-bits, as many as letterIdxBits
)

var maxURIPathLen = message.CoapOptionDefs[message.URIPath].MaxLen

func randomURLString(n int) string {
	b := make([]byte, n)
	if n > 0 {
		b[0] = '/'
	}
	for i := 1; i < n; {
		if idx := int(rand.Int63() & urlLetterIdxMask); idx < len(urlLetterBytes) {
			b[i] = urlLetterBytes[idx]
			i++
		}
	}
	return string(b)
}

func randomValidURLString(n int) string {
	b := make([]byte, n)
	if n > 0 {
		b[0] = '/'
	}
	for i := 1; i < n; {
		if idx := int(rand.Int63() & urlLetterIdxMask); idx < len(urlLetterBytes) {
			b[i] = urlLetterBytes[idx]
			i++
		}
	}

	// ensure that at at least every maxURIPathLen-th character is '/', otherwise
	// SetPath will fail with invalid path error
	index := 0
	for {
		remainder := n - index
		if remainder < int(maxURIPathLen) {
			break
		}
		shift := uint8(rand.Int63() >> 55)
		index = index + int(shift)
		b[index] = '/'
	}
	return string(b)
}

func normalizePath(s string) string {
	space := regexp.MustCompile("/+")
	return space.ReplaceAllString(s, "/")
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
			msg := pool.NewMessage()
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

// URL is split by "/" into URI-Path options, however the maximal length
// of an URI-Path option is 255, so if a path segment is longer than the
// maximal length an error should be returned.
func TestMessageSetPathOptionLength(t *testing.T) {
	size := 4
	// try strings of length [4, 16, .., 65536]
	for i := 0; i < 8; i++ {
		msg := pool.NewMessage()
		inPath := randomURLString(size)
		wantErr := size-1 > int(maxURIPathLen) // -1 for the starting '/'
		err := msg.SetPath(inPath)
		if wantErr {
			require.Error(t, err)
			continue
		}
		outPath, err := msg.Path()
		require.NoError(t, err)
		require.Equal(t, normalizePath(inPath), outPath)
		size = size * 4
	}
}

func TestMessageSetPathValidLength(t *testing.T) {
	size := 4
	// try strings of length [4, 16, .., 65536]
	for i := 0; i < 8; i++ {
		msg := pool.NewMessage()
		inPath := randomValidURLString(size)
		err := msg.SetPath(inPath)
		require.NoError(t, err)
		outPath, err := msg.Path()
		require.NoError(t, err)
		require.Equal(t, normalizePath(inPath), outPath)
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
			msg := pool.NewMessage()
			msg.MustSetPath(tt.args.p)
			path, err := msg.Path()
			require.NoError(t, err)
			require.Equal(t, tt.want, path)
		})
	}
}
