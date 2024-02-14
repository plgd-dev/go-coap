package message

import (
	"strings"
	"testing"

	"github.com/plgd-dev/go-coap/v3/test/net"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testFindPositionBytesOption(t *testing.T, options Options, id OptionID, prepend bool, expectedIdx int) int {
	prepIdx, idx := options.findPosition(id)
	if prepend {
		assert.Equal(t, expectedIdx, prepIdx)
	} else {
		assert.Equal(t, expectedIdx, idx)
	}
	return idx
}

func TestFindPositonBytesOption1(t *testing.T) {
	options := make(Options, 0, 10)
	options = append(options, Options{{ID: 11, Value: []byte{97, 98, 99}}}...)
	options, _, _ = options.SetUint32(make([]byte, 4), 60, 128)
	options, _, _ = options.SetUint32(make([]byte, 4), 27, 8)
	_, _, err := options.Find(27)
	require.NoError(t, err)
}

func TestFindObserve(t *testing.T) {
	options := make(Options, 0, 10)
	options = append(options, Options{
		{ID: ETag, Value: []byte{96, 136, 190, 171, 5, 166, 238, 88}},
		{ID: ContentFormat, Value: []byte{}},
		{ID: Block2, Value: []byte{4, 8}},
		{ID: Size2, Value: []byte{19, 136}},
	}...)
	_, _, err := options.Find(Observe)
	require.Error(t, err)
}

func TestETAG(t *testing.T) {
	opts := Options{
		{
			ID:    ETag,
			Value: []byte{238, 32, 201, 23, 231, 160, 183, 145},
		},
		{
			ID:    ContentFormat,
			Value: []byte{},
		},
		{
			ID:    Block2,
			Value: []byte{0x0e},
		},
		{
			ID:    Size2,
			Value: []byte{0x14, 0xd2},
		},
	}
	buf := make([]byte, 1024)
	newOpts := make(Options, 0, len(opts))
	newOpts, n, err := newOpts.ResetOptionsTo(buf, opts)
	require.NoError(t, err)
	require.Equal(t, opts, newOpts)
	require.Equal(t, 11, n)
	buf = buf[n:]

	opts, n, err = newOpts.SetUint32(buf, Size2, uint32(5330))
	require.NoError(t, err)
	require.Equal(t, opts, newOpts)
	require.Equal(t, 2, n)
	buf = buf[n:]

	opts, n, err = newOpts.SetUint32(buf, Block2, uint32(8))
	require.NoError(t, err)
	require.Equal(t, opts, newOpts)
	require.Equal(t, 1, n)

	etag, err := newOpts.GetBytes(ETag)
	require.NoError(t, err)
	require.Equal(t, opts[0].Value, etag)
}

func TestGetPathBufferSize(t *testing.T) {
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
			name: "Empty",
			args: args{p: ""},
		},
		{
			name: "Empty (slash)",
			args: args{p: "/"},
		},
		{
			name: "Empty (multiple slashes)",
			args: args{p: "//////////"},
		},
		{
			name:    "Invalid string",
			args:    args{p: strings.Repeat("a", maxPathValue+1)},
			wantErr: true,
		},
		{
			name: "Basic path",
			args: args{p: "/a/b/c"},
			want: "/a/b/c",
		},
		{
			name: "Bath with duplicit slashes",
			args: args{p: "/aaa///bbbbbbbbbbbbbb//ccccc/"},
			want: "/aaaa/bbbbbbbbbbbbbb/ccccc",
		},
		{
			name: "Path without first slash",
			args: args{p: "aaaaaaaaaaaaa/bbbbb/ccccccccccccccccccccccccccccccccc/"},
			want: "/aaaaaaaaaaaaa/bbbbb/ccccccccccccccccccccccccccccccccc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			size, err := GetPathBufferSize(tt.args.p)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			options := make(Options, 0, 10)
			_, used, err := options.SetPath(make([]byte, len(tt.args.p)), tt.args.p)
			require.NoError(t, err)
			require.Equal(t, used, size)
		})
	}
}

func TestGetPathBufferSizeLongPaths(t *testing.T) {
	maxURIPathLen := int(CoapOptionDefs[URIPath].MaxLen)

	for i := 8; i < 16; i++ {
		options := make(Options, 0, 10)
		path := net.RandomValidURLString(i<<i, maxURIPathLen)
		size, err := GetPathBufferSize(path)
		require.NoError(t, err)
		_, used, err := options.SetPath(make([]byte, len(path)), path)
		require.NoError(t, err)
		require.Equal(t, used, size)
	}
}

func TestSetPath(t *testing.T) {
	options := make(Options, 0, 10)
	options, _, err := options.SetPath(make([]byte, 32), "/light/2")
	require.NoError(t, err)
	require.Equal(t, Options{
		{ID: URIPath, Value: []byte("light")},
		{ID: URIPath, Value: []byte("2")},
	}, options)

	marshaled := make([]byte, 128)
	n, err := options.Marshal(marshaled)
	require.NoError(t, err)
	marshaled = marshaled[:n]
	uoptions := make(Options, 0, 10)
	un, err := uoptions.Unmarshal(marshaled, CoapOptionDefs)
	require.NoError(t, err)
	require.Equal(t, n, un)
	require.Equal(t, options, uoptions)
}

func TestLocationPath(t *testing.T) {
	options := Options{
		{ID: LocationPath, Value: []byte("foo")},
		{ID: LocationPath, Value: []byte("bar")},
	}
	path, err := options.LocationPath()
	require.NoError(t, err)
	require.Equal(t, "/foo/bar", path)
}

func TestSetLocationPath(t *testing.T) {
	options := make(Options, 0, 10)
	options, _, err := options.SetLocationPath(make([]byte, 32), "/light/2")
	require.NoError(t, err)
	require.Equal(t, Options{
		{ID: LocationPath, Value: []byte("light")},
		{ID: LocationPath, Value: []byte("2")},
	}, options)

	marshaled := make([]byte, 128)
	n, err := options.Marshal(marshaled)
	require.NoError(t, err)
	marshaled = marshaled[:n]
	uoptions := make(Options, 0, 10)
	un, err := uoptions.Unmarshal(marshaled, CoapOptionDefs)
	require.NoError(t, err)
	require.Equal(t, n, un)
	require.Equal(t, options, uoptions)
}

func TestFindPositonBytesOption(t *testing.T) {
	options := make(Options, 0, 10)
	testFindPositionBytesOption(t, options, 3, true, -1)
	testFindPositionBytesOption(t, options, 3, false, 0)
	options = append(options, Options{{ID: 1}}...)
	testFindPositionBytesOption(t, options, 0, true, -1)
	testFindPositionBytesOption(t, options, 0, false, 0)
	options = append(options, Options{{ID: 2}}...)
	options = append(options, Options{{ID: 2}}...)
	options = append(options, Options{{ID: 2}}...)
	options = append(options, Options{{ID: 2}}...)
	testFindPositionBytesOption(t, options, 2, true, 0)
	testFindPositionBytesOption(t, options, 2, false, -1)

	options = append(options, Options{{ID: 5}}...)
	testFindPositionBytesOption(t, options, 3, true, 4)
	testFindPositionBytesOption(t, options, 3, false, 5)
	options = append(options, Options{{ID: 5}}...)
	testFindPositionBytesOption(t, options, 5, true, 4)
	testFindPositionBytesOption(t, options, 5, false, -1)

	options = append(options, Options{{ID: 27}}...)
	options = append(options, Options{{ID: 60}}...)
	testFindPositionBytesOption(t, options, 27, false, 8)
	testFindPositionBytesOption(t, options, 60, false, -1)
}

func TestSetBytesOption(t *testing.T) {
	options := make(Options, 0, 10)
	options = options.Set(Option{ID: 0, Value: []byte("0")})
	require.Len(t, options, 1)

	// options = options[:len]
	options = append(options, Options{{ID: 0, Value: []byte("1")}}...)
	options = append(options, Options{{ID: 0, Value: []byte("2")}}...)
	options = append(options, Options{{ID: 0, Value: []byte("3")}}...)
	options = options.Set(Option{ID: 0, Value: []byte("4")})
	require.Len(t, options, 1)

	// options = options[:len]
	options = append(options, Options{{ID: 1, Value: []byte("5")}}...)
	options = options.Set(Option{ID: 1, Value: []byte("6")})
	require.Len(t, options, 2)

	// options = options[:len]
	options = append(options, Options{{ID: 1, Value: []byte("7")}}...)
	options = append(options, Options{{ID: 1, Value: []byte("8")}}...)
	options = options.Set(Option{ID: 1, Value: []byte("9")})
	require.Len(t, options, 2)
	// options = options[:len]
	options = options.Set(Option{ID: 2, Value: []byte("10")})
	require.Len(t, options, 3)
	// options = options[:len]
	options = options.Set(Option{ID: 1, Value: []byte("11")})
	require.Len(t, options, 3)

	v := make([]string, 2)
	n, err := options.GetStrings(1, v)
	require.Equal(t, nil, err)
	require.Equal(t, 1, n)
	require.Equal(t, []string{"11"}, v[:n])

	// options = options[:len]
}

func testAddBytesOption(t *testing.T, options Options, option Option, expectedIdx int) Options {
	expectedLen := len(options) + 1
	options = options.Add(option)
	require.Len(t, options, expectedLen)
	require.Equal(t, option, options[expectedIdx])
	return options
}

func TestAddBytesOption(t *testing.T) {
	options := make(Options, 0, 10)
	options = testAddBytesOption(t, options, Option{ID: 0, Value: []byte("0")}, 0)
	options = testAddBytesOption(t, options, Option{ID: 0, Value: []byte("1")}, 1)
	options = testAddBytesOption(t, options, Option{ID: 3, Value: []byte("2")}, 2)
	options = testAddBytesOption(t, options, Option{ID: 3, Value: []byte("3")}, 3)
	options = testAddBytesOption(t, options, Option{ID: 1, Value: []byte("4")}, 2)
	v := make([][]byte, 2)
	n, err := options.GetBytess(0, v)
	require.Equal(t, nil, err)
	require.Equal(t, 2, n)
	require.Equal(t, [][]byte{{0x30}, {0x31}}, v)
}

func testRemoveBytesOption(t *testing.T, options Options, option OptionID, expectedLen int) Options {
	options = options.Remove(option)
	if len(options) != expectedLen {
		t.Fatalf("bad size of options %d, expected %d", len(options), expectedLen)
	}
	// options = options[:len]
	for _, o := range options {
		if o.ID == option {
			t.Fatalf("option %d wasn't removed", option)
		}
	}
	return options
}

func TestRemoveBytesOption(t *testing.T) {
	options := make(Options, 0, 10)
	options = testAddBytesOption(t, options, Option{ID: 0, Value: []byte("0")}, 0)
	options = testAddBytesOption(t, options, Option{ID: 0, Value: []byte("1")}, 1)
	options = testAddBytesOption(t, options, Option{ID: 3, Value: []byte("2")}, 2)
	options = testAddBytesOption(t, options, Option{ID: 3, Value: []byte("3")}, 3)
	options = testAddBytesOption(t, options, Option{ID: 1, Value: []byte("4")}, 2)

	options = testRemoveBytesOption(t, options, 99, 5)
	options = testRemoveBytesOption(t, options, 0, 3)
	options = testAddBytesOption(t, options, Option{ID: 2, Value: []byte("5")}, 1)
	testRemoveBytesOption(t, options, 2, 3)
}

func TestPathOption(t *testing.T) {
	options := make(Options, 0, 10)
	path := "/a/b/c"
	buf := make([]byte, 256)
	options, bufLen, err := options.SetPath(buf, path)
	if err != nil {
		t.Fatalf("unexpected error %d", err)
	}
	if bufLen != 3 {
		t.Fatalf("unexpected length %d", bufLen)
	}

	newPath, err := options.Path()
	if err != nil {
		t.Fatalf("unexpected error %d", err)
	}
	if newPath != path {
		t.Fatalf("unexpected value %v, expected %v", newPath, path)
	}
}

func TestQueryOption(t *testing.T) {
	v := "if=oic.if.baseline"
	buf := make([]byte, len(v))
	var opts Options
	opts, _, err := opts.AddString(buf, URIQuery, v)
	require.NoError(t, err)
	require.True(t, opts.HasOption(URIQuery))
}

func FuzzUnmarshalData(f *testing.F) {
	f.Fuzz(func(_ *testing.T, input_data []byte) {
		uoptions := make(Options, 0, 10)
		_, _ = uoptions.Unmarshal(input_data, CoapOptionDefs)
	})
}
