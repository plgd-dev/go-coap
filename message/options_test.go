package message

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func testFindPositionBytesOption(t *testing.T, options Options, id OptionID, prepend bool, expectedIdx int) int {
	idx := options.findPositon(id, prepend)
	if idx != expectedIdx {
		t.Fatalf("Unexpected idx %d, expected %d, ", idx, expectedIdx)
	}
	return idx
}

func TestFindPositonBytesOption(t *testing.T) {
	options := make(Options, 0, 10)
	testFindPositionBytesOption(t, options, 3, true, 0)
	testFindPositionBytesOption(t, options, 3, false, 0)
	options = append(options, Options{{ID: 1}}...)
	testFindPositionBytesOption(t, options, 0, true, 0)
	testFindPositionBytesOption(t, options, 0, false, 0)
	options = append(options, Options{{ID: 2}}...)
	options = append(options, Options{{ID: 2}}...)
	options = append(options, Options{{ID: 2}}...)
	options = append(options, Options{{ID: 2}}...)
	testFindPositionBytesOption(t, options, 2, true, 1)
	testFindPositionBytesOption(t, options, 2, false, 5)
	options = append(options, Options{{ID: 5}}...)
	testFindPositionBytesOption(t, options, 3, true, 5)
	testFindPositionBytesOption(t, options, 3, false, 5)
	options = append(options, Options{{ID: 5}}...)
	testFindPositionBytesOption(t, options, 5, true, 5)
	testFindPositionBytesOption(t, options, 5, false, 7)
}

func TestSetBytesOption(t *testing.T) {
	options := make(Options, 0, 10)
	options = options.Set(Option{ID: 0, Value: []byte("0")})

	if len(options) != 1 {
		t.Fatalf("bad size of option %d", len(options))
	}
	// options = options[:len]
	options = append(options, Options{{ID: 0, Value: []byte("1")}}...)
	options = append(options, Options{{ID: 0, Value: []byte("2")}}...)
	options = append(options, Options{{ID: 0, Value: []byte("3")}}...)
	options = options.Set(Option{ID: 0, Value: []byte("4")})
	if len(options) != 1 {
		t.Fatalf("bad size of option %d", len(options))
	}
	// options = options[:len]
	options = append(options, Options{{ID: 1, Value: []byte("5")}}...)
	options = options.Set(Option{ID: 1, Value: []byte("6")})
	if len(options) != 2 {
		t.Fatalf("bad size of option %d", len(options))
	}
	// options = options[:len]
	options = append(options, Options{{ID: 1, Value: []byte("7")}}...)
	options = append(options, Options{{ID: 1, Value: []byte("8")}}...)
	options = options.Set(Option{ID: 1, Value: []byte("9")})

	if len(options) != 2 {
		t.Fatalf("bad size of option %d", len(options))
	}
	// options = options[:len]
	options = options.Set(Option{ID: 2, Value: []byte("10")})
	if len(options) != 3 {
		t.Fatalf("bad size of option %d", len(options))
	}
	// options = options[:len]
	options = options.Set(Option{ID: 1, Value: []byte("11")})
	if len(options) != 3 {
		t.Fatalf("bad size of option %d", len(options))
	}

	v := make([]string, 1)
	n, errCode := options.ReadStrings(1, v)
	require.Equal(t, OK, errCode)
	require.Equal(t, 1, n)
	require.Equal(t, []string{"11"}, v)

	// options = options[:len]
}

func testAddBytesOption(t *testing.T, options Options, option Option, expectedIdx int) Options {
	expectedLen := len(options) + 1
	options = options.Add(option)
	if len(options) != expectedLen {
		t.Fatalf("bad size of option %d, expected %d", len(options), expectedLen)
	}
	// options = options[:len]
	if options[expectedIdx].ID != option.ID || !bytes.Equal(options[expectedIdx].Value, option.Value) {
		t.Fatalf("bad option %d:%s, expected %d:%s", options[expectedIdx].ID, options[expectedIdx].Value, option.ID, option.Value)
	}
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
	n, errCode := options.ReadBytes(0, v)
	require.Equal(t, OK, errCode)
	require.Equal(t, 2, n)
	require.Equal(t, [][]byte{[]byte{0x30}, []byte{0x31}}, v)
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
	options = testRemoveBytesOption(t, options, 2, 3)
}

func TestPathOption(t *testing.T) {
	options := make(Options, 0, 10)
	path := "a/b/c"
	buf := make([]byte, 256)
	options, bufLen, err := options.SetPath(buf, path)
	if err != OK {
		t.Fatalf("unexpected error %d", err)
	}
	if bufLen != 3 {
		t.Fatalf("unexpected length %d", bufLen)
	}

	runes := make([]rune, 32)
	bufLen, err = options.Path(runes)
	if err != OK {
		t.Fatalf("unexpected error %d", err)
	}
	if bufLen == 6 {
		t.Fatalf("unexpected length %d", bufLen)
	}
	newPath := string(runes[:bufLen])
	if newPath != path {
		t.Fatalf("unexpected value %v, expected %v", newPath, path)
	}
}

func BenchmarkPathOption(b *testing.B) {
	runes := make([]rune, 32)
	buf := make([]byte, 256)
	b.ResetTimer()
	for i := uint32(0); i < uint32(b.N); i++ {
		options := make(Options, 0, 10)
		path := "a/b/c"

		options, bufLen, err := options.SetPath(buf, path)
		if err != OK {
			b.Fatalf("unexpected error %d", err)
		}
		if bufLen != 3 {
			b.Fatalf("unexpected length %d", bufLen)
		}

		bufLen, err = options.Path(runes)
		if err != OK {
			b.Fatalf("unexpected error %d", err)
		}
		if bufLen == 6 {
			b.Fatalf("unexpected length %d", bufLen)
		}

		newPath := string(runes[:bufLen])
		if newPath != path {
			b.Fatalf("unexpected path")
		}

		v := make([]string, 3)
		n, errCode := options.ReadStrings(URIPath, v)
		if n != 3 {
			b.Fatalf("bad length")
		}
		if errCode != OK {
			b.Fatalf("unexpected code")
		}
	}
}
