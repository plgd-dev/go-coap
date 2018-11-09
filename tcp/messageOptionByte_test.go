package tcpcoap

import (
	"bytes"
	"testing"
)

func testFindPositionBytesOption(t *testing.T, options BytesOptions, id OptionID, prepend bool, expectedIdx int) int {
	idx := options.findPositon(id, prepend)
	if idx != expectedIdx {
		t.Fatalf("Unexpected idx %d, expected %d, ", idx, expectedIdx)
	}
	return idx
}

func TestFindPositonBytesOption(t *testing.T) {
	options := make(BytesOptions, 0, 10)
	testFindPositionBytesOption(t, options, 3, true, 0)
	testFindPositionBytesOption(t, options, 3, false, 0)
	options = append(options, BytesOptions{{ID: 1}}...)
	testFindPositionBytesOption(t, options, 0, true, 0)
	testFindPositionBytesOption(t, options, 0, false, 0)
	options = append(options, BytesOptions{{ID: 2}}...)
	options = append(options, BytesOptions{{ID: 2}}...)
	options = append(options, BytesOptions{{ID: 2}}...)
	options = append(options, BytesOptions{{ID: 2}}...)
	testFindPositionBytesOption(t, options, 2, true, 1)
	testFindPositionBytesOption(t, options, 2, false, 5)
	options = append(options, BytesOptions{{ID: 5}}...)
	testFindPositionBytesOption(t, options, 3, true, 5)
	testFindPositionBytesOption(t, options, 3, false, 5)
	options = append(options, BytesOptions{{ID: 5}}...)
	testFindPositionBytesOption(t, options, 5, true, 5)
	testFindPositionBytesOption(t, options, 5, false, 7)
}

func TestSetBytesOption(t *testing.T) {
	options := make(BytesOptions, 0, 10)
	options, err := options.Set(BytesOption{ID: 0, Value: []byte("0")})
	if err != OK {
		t.Fatalf("cannot set option %d", err)
	}
	if len(options) != 1 {
		t.Fatalf("bad size of option %d", len(options))
	}
	// options = options[:len]
	options = append(options, BytesOptions{{ID: 0, Value: []byte("1")}}...)
	options = append(options, BytesOptions{{ID: 0, Value: []byte("2")}}...)
	options = append(options, BytesOptions{{ID: 0, Value: []byte("3")}}...)
	options, err = options.Set(BytesOption{ID: 0, Value: []byte("4")})
	if err != OK {
		t.Fatalf("cannot set option %d", err)
	}
	if len(options) != 1 {
		t.Fatalf("bad size of option %d", len(options))
	}
	// options = options[:len]
	options = append(options, BytesOptions{{ID: 1, Value: []byte("5")}}...)
	options, err = options.Set(BytesOption{ID: 1, Value: []byte("6")})
	if err != OK {
		t.Fatalf("cannot set option %d", err)
	}
	if len(options) != 2 {
		t.Fatalf("bad size of option %d", len(options))
	}
	// options = options[:len]
	options = append(options, BytesOptions{{ID: 1, Value: []byte("7")}}...)
	options = append(options, BytesOptions{{ID: 1, Value: []byte("8")}}...)
	options, err = options.Set(BytesOption{ID: 1, Value: []byte("9")})
	if err != OK {
		t.Fatalf("cannot set option %d", err)
	}
	if len(options) != 2 {
		t.Fatalf("bad size of option %d", len(options))
	}
	// options = options[:len]
	options, err = options.Set(BytesOption{ID: 2, Value: []byte("10")})
	if err != OK {
		t.Fatalf("cannot set option %d", err)
	}
	if len(options) != 3 {
		t.Fatalf("bad size of option %d", len(options))
	}
	// options = options[:len]
	options, err = options.Set(BytesOption{ID: 1, Value: []byte("11")})
	if err != OK {
		t.Fatalf("cannot set option %d", err)
	}
	if len(options) != 3 {
		t.Fatalf("bad size of option %d", len(options))
	}
	// options = options[:len]
}

func testAddBytesOption(t *testing.T, options BytesOptions, option BytesOption, expectedIdx int) BytesOptions {
	expectedLen := len(options) + 1
	options, err := options.Add(option)
	if err != OK {
		t.Fatalf("cannot set option %d", err)
	}
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
	options := make(BytesOptions, 0, 10)
	options = testAddBytesOption(t, options, BytesOption{ID: 0, Value: []byte("0")}, 0)
	options = testAddBytesOption(t, options, BytesOption{ID: 0, Value: []byte("1")}, 1)
	options = testAddBytesOption(t, options, BytesOption{ID: 3, Value: []byte("2")}, 2)
	options = testAddBytesOption(t, options, BytesOption{ID: 3, Value: []byte("3")}, 3)
	options = testAddBytesOption(t, options, BytesOption{ID: 1, Value: []byte("4")}, 2)
}

func testRemoveBytesOption(t *testing.T, options BytesOptions, option OptionID, expectedLen int) BytesOptions {
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
	options := make(BytesOptions, 0, 10)
	options = testAddBytesOption(t, options, BytesOption{ID: 0, Value: []byte("0")}, 0)
	options = testAddBytesOption(t, options, BytesOption{ID: 0, Value: []byte("1")}, 1)
	options = testAddBytesOption(t, options, BytesOption{ID: 3, Value: []byte("2")}, 2)
	options = testAddBytesOption(t, options, BytesOption{ID: 3, Value: []byte("3")}, 3)
	options = testAddBytesOption(t, options, BytesOption{ID: 1, Value: []byte("4")}, 2)

	options = testRemoveBytesOption(t, options, 99, 5)
	options = testRemoveBytesOption(t, options, 0, 3)
	options = testAddBytesOption(t, options, BytesOption{ID: 2, Value: []byte("5")}, 1)
	options = testRemoveBytesOption(t, options, 2, 3)
}
