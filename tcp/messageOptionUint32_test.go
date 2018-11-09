package tcpcoap

import (
	"testing"
)

func testFindPosition(t *testing.T, options Uint32Options, id OptionID, prepend bool, expectedIdx int) int {
	idx := options.findPositon(id, prepend)
	if idx != expectedIdx {
		t.Fatalf("Unexpected idx %d, expected %d, ", idx, expectedIdx)
	}
	return idx
}

func TestFindPositonUint32Option(t *testing.T) {
	options := make(Uint32Options, 0, 10)
	testFindPosition(t, options, 3, true, 0)
	testFindPosition(t, options, 3, false, 0)
	options = append(options, Uint32Options{{ID: 1}}...)
	testFindPosition(t, options, 0, true, 0)
	testFindPosition(t, options, 0, false, 0)
	options = append(options, Uint32Options{{ID: 2}}...)
	options = append(options, Uint32Options{{ID: 2}}...)
	options = append(options, Uint32Options{{ID: 2}}...)
	options = append(options, Uint32Options{{ID: 2}}...)
	testFindPosition(t, options, 2, true, 1)
	testFindPosition(t, options, 2, false, 5)
	options = append(options, Uint32Options{{ID: 5}}...)
	testFindPosition(t, options, 3, true, 5)
	testFindPosition(t, options, 3, false, 5)
	options = append(options, Uint32Options{{ID: 5}}...)
	testFindPosition(t, options, 5, true, 5)
	testFindPosition(t, options, 5, false, 7)
}

func TestSetUint32Option(t *testing.T) {
	options := make(Uint32Options, 0, 10)
	options, err := options.Set(Uint32Option{ID: 0, Value: 0})
	if err != OK {
		t.Fatalf("cannot set option %d", err)
	}
	if len(options) != 1 {
		t.Fatalf("bad size of option %d", len(options))
	}
	options = append(options, Uint32Options{{ID: 0, Value: 1}}...)
	options = append(options, Uint32Options{{ID: 0, Value: 2}}...)
	options = append(options, Uint32Options{{ID: 0, Value: 3}}...)
	options, err = options.Set(Uint32Option{ID: 0, Value: 4})
	if err != OK {
		t.Fatalf("cannot set option %d", err)
	}
	if len(options) != 1 {
		t.Fatalf("bad size of option %d", len(options))
	}
	options = append(options, Uint32Options{{ID: 1, Value: 5}}...)
	options, err = options.Set(Uint32Option{ID: 1, Value: 6})
	if err != OK {
		t.Fatalf("cannot set option %d", err)
	}
	if len(options) != 2 {
		t.Fatalf("bad size of option %d", len(options))
	}
	options = append(options, Uint32Options{{ID: 1, Value: 7}}...)
	options = append(options, Uint32Options{{ID: 1, Value: 8}}...)
	options, err = options.Set(Uint32Option{ID: 1, Value: 9})
	if err != OK {
		t.Fatalf("cannot set option %d", err)
	}
	if len(options) != 2 {
		t.Fatalf("bad size of option %d", len(options))
	}

	options, err = options.Set(Uint32Option{ID: 2, Value: 10})
	if err != OK {
		t.Fatalf("cannot set option %d", err)
	}
	if len(options) != 3 {
		t.Fatalf("bad size of option %d", len(options))
	}

	options, err = options.Set(Uint32Option{ID: 1, Value: 11})
	if err != OK {
		t.Fatalf("cannot set option %d", err)
	}
	if len(options) != 3 {
		t.Fatalf("bad size of option %d", len(options))
	}

}

func testAddUint32Option(t *testing.T, options Uint32Options, option Uint32Option, expectedIdx int) Uint32Options {
	expectedLen := len(options) + 1
	options, err := options.Add(option)
	if err != OK {
		t.Fatalf("cannot set option %d", err)
	}
	if len(options) != expectedLen {
		t.Fatalf("bad size of option %d, expected %d", len(options), expectedLen)
	}

	if options[expectedIdx].ID != option.ID || options[expectedIdx].Value != option.Value {
		t.Fatalf("bad option %d:%d, expected %d:%d", options[expectedIdx].ID, options[expectedIdx].Value, option.ID, option.Value)
	}
	return options
}

func TestAddUint32Option(t *testing.T) {
	options := make(Uint32Options, 0, 10)
	options = testAddUint32Option(t, options, Uint32Option{ID: 0, Value: 0}, 0)

	options = testAddUint32Option(t, options, Uint32Option{ID: 0, Value: 1}, 1)

	options = testAddUint32Option(t, options, Uint32Option{ID: 3, Value: 2}, 2)

	options = testAddUint32Option(t, options, Uint32Option{ID: 3, Value: 3}, 3)

	options = testAddUint32Option(t, options, Uint32Option{ID: 1, Value: 4}, 2)

}

func testRemoveUint32Option(t *testing.T, options Uint32Options, option OptionID, expectedLen int) Uint32Options {
	options = options.Remove(option)
	if len(options) != expectedLen {
		t.Fatalf("bad size of options %d, expected %d", len(options), expectedLen)
	}

	for _, o := range options {
		if o.ID == option {
			t.Fatalf("option %d wasn't removed", option)
		}
	}
	return options
}

func TestRemoveUint32Option(t *testing.T) {
	options := make(Uint32Options, 0, 10)
	options = testAddUint32Option(t, options, Uint32Option{ID: 0, Value: 0}, 0)

	options = testAddUint32Option(t, options, Uint32Option{ID: 0, Value: 1}, 1)

	options = testAddUint32Option(t, options, Uint32Option{ID: 3, Value: 2}, 2)

	options = testAddUint32Option(t, options, Uint32Option{ID: 3, Value: 3}, 3)

	options = testAddUint32Option(t, options, Uint32Option{ID: 1, Value: 4}, 2)

	options = testRemoveUint32Option(t, options, 99, 5)

	options = testRemoveUint32Option(t, options, 0, 3)

	options = testAddUint32Option(t, options, Uint32Option{ID: 2, Value: 5}, 1)

	options = testRemoveUint32Option(t, options, 2, 3)

}
