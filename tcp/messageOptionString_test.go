package tcpcoap

import (
	"fmt"
	"testing"
	"unicode/utf8"
)

func testFindPositionStringOption(t *testing.T, options StringOptions, id OptionID, prepend bool, expectedIdx int) int {
	idx := options.findPositon(id, prepend)
	if idx != expectedIdx {
		t.Fatalf("Unexpected idx %d, expected %d, ", idx, expectedIdx)
	}
	return idx
}

func TestFindPositonStringOption(t *testing.T) {
	options := make(StringOptions, 0, 10)
	testFindPositionStringOption(t, options, 3, true, 0)
	testFindPositionStringOption(t, options, 3, false, 0)
	options = append(options, StringOptions{{ID: 1}}...)
	testFindPositionStringOption(t, options, 0, true, 0)
	testFindPositionStringOption(t, options, 0, false, 0)
	options = append(options, StringOptions{{ID: 2}}...)
	options = append(options, StringOptions{{ID: 2}}...)
	options = append(options, StringOptions{{ID: 2}}...)
	options = append(options, StringOptions{{ID: 2}}...)
	testFindPositionStringOption(t, options, 2, true, 1)
	testFindPositionStringOption(t, options, 2, false, 5)
	options = append(options, StringOptions{{ID: 5}}...)
	testFindPositionStringOption(t, options, 3, true, 5)
	testFindPositionStringOption(t, options, 3, false, 5)
	options = append(options, StringOptions{{ID: 5}}...)
	testFindPositionStringOption(t, options, 5, true, 5)
	testFindPositionStringOption(t, options, 5, false, 7)
}

func TestSetStringOption(t *testing.T) {
	options := make(StringOptions, 0, 10)
	options, err := options.Set(StringOption{ID: 0, Value: "0"})
	if err != OK {
		t.Fatalf("cannot set option %d", err)
	}
	if len(options) != 1 {
		t.Fatalf("bad size of option %d", len(options))
	}
	// options = options[:len]
	options = append(options, StringOptions{{ID: 0, Value: "1"}}...)
	options = append(options, StringOptions{{ID: 0, Value: "2"}}...)
	options = append(options, StringOptions{{ID: 0, Value: "3"}}...)
	options, err = options.Set(StringOption{ID: 0, Value: "4"})
	if err != OK {
		t.Fatalf("cannot set option %d", err)
	}
	if len(options) != 1 {
		t.Fatalf("bad size of option %d", len(options))
	}
	// options = options[:len]
	options = append(options, StringOptions{{ID: 1, Value: "5"}}...)
	options, err = options.Set(StringOption{ID: 1, Value: "6"})
	if err != OK {
		t.Fatalf("cannot set option %d", err)
	}
	if len(options) != 2 {
		t.Fatalf("bad size of option %d", len(options))
	}
	// options = options[:len]
	options = append(options, StringOptions{{ID: 1, Value: "7"}}...)
	options = append(options, StringOptions{{ID: 1, Value: "8"}}...)
	options, err = options.Set(StringOption{ID: 1, Value: "9"})
	if err != OK {
		t.Fatalf("cannot set option %d", err)
	}
	if len(options) != 2 {
		t.Fatalf("bad size of option %d", len(options))
	}
	// options = options[:len]
	options, err = options.Set(StringOption{ID: 2, Value: "10"})
	if err != OK {
		t.Fatalf("cannot set option %d", err)
	}
	if len(options) != 3 {
		t.Fatalf("bad size of option %d", len(options))
	}
	// options = options[:len]
	options, err = options.Set(StringOption{ID: 1, Value: "11"})
	if err != OK {
		t.Fatalf("cannot set option %d", err)
	}
	if len(options) != 3 {
		t.Fatalf("bad size of option %d", len(options))
	}
	// options = options[:len]
}

func testAddStringOption(t *testing.T, options StringOptions, option StringOption, expectedIdx int) StringOptions {
	expectedLen := len(options) + 1
	options, err := options.Add(option)
	if err != OK {
		t.Fatalf("cannot set option %d", err)
	}
	if len(options) != expectedLen {
		t.Fatalf("bad size of option %d, expected %d", len(options), expectedLen)
	}
	// options = options[:len]
	if options[expectedIdx].ID != option.ID || options[expectedIdx].Value != option.Value {
		t.Fatalf("bad option %d:%s, expected %d:%s", options[expectedIdx].ID, options[expectedIdx].Value, option.ID, option.Value)
	}
	return options
}

func TestAddStringOption(t *testing.T) {
	options := make(StringOptions, 0, 10)
	options = testAddStringOption(t, options, StringOption{ID: 0, Value: "0"}, 0)
	options = testAddStringOption(t, options, StringOption{ID: 0, Value: "1"}, 1)
	options = testAddStringOption(t, options, StringOption{ID: 3, Value: "2"}, 2)
	options = testAddStringOption(t, options, StringOption{ID: 3, Value: "3"}, 3)
	options = testAddStringOption(t, options, StringOption{ID: 1, Value: "4"}, 2)
}

func testRemoveStringOption(t *testing.T, options StringOptions, option OptionID, expectedLen int) StringOptions {
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

func TestRemoveStringOption(t *testing.T) {
	options := make(StringOptions, 0, 10)
	options = testAddStringOption(t, options, StringOption{ID: 0, Value: "0"}, 0)
	options = testAddStringOption(t, options, StringOption{ID: 0, Value: "1"}, 1)
	options = testAddStringOption(t, options, StringOption{ID: 3, Value: "2"}, 2)
	options = testAddStringOption(t, options, StringOption{ID: 3, Value: "3"}, 3)
	options = testAddStringOption(t, options, StringOption{ID: 1, Value: "4"}, 2)

	options = testRemoveStringOption(t, options, 99, 5)
	options = testRemoveStringOption(t, options, 0, 3)
	options = testAddStringOption(t, options, StringOption{ID: 2, Value: "5"}, 1)
	options = testRemoveStringOption(t, options, 2, 3)
}

func testSetPath(t *testing.T, options StringOptions, path string, expectedLen int) StringOptions {
	options, err := options.SetPath(path)
	if err != OK {
		t.Fatalf("unexpected error: %d", err)
	}
	if len(options) != expectedLen {
		t.Fatalf("unexpected length %d, expected %d", err, expectedLen)
	}
	return options
}

func TestSetPath(t *testing.T) {
	options := make(StringOptions, 0, 10)
	options = testSetPath(t, options, "", 0)
	options = testSetPath(t, options, "/a/b/c/d/e", 5)
	options = testSetPath(t, options, "f/g/h", 3)
	options, err := options.SetPath("i/j/k/l/m/n/o/p/r/s/t")
	if err == OK {
		t.Fatalf("expected error: %d", err)
	}
}

func TestRune(t *testing.T) {
	s := "abc日"
	r := []rune(s)
	buf := make([]byte, 1024)
	written := 0
	for _, rune := range r {
		written += utf8.EncodeRune(buf[written:], rune)
	}
	buf = buf[:written]
	fmt.Printf("%v\n", len(buf))

	decodeS := string(buf)
	if s == decodeS {
		fmt.Printf("OK\n")
	}

	/*
		fmt.Printf("%v\n", r)
		fmt.Printf("%U\n", r)
		// Output:
		// [97 98 99 26085]
		// [U+0061 U+0062 U+0063 U+65E5]

			fmt.Printf("r[0] = %p\n", &r[0])
			for idx, r := range s {
				if idx == 0 {
					fmt.Printf("s[0] = %p\n", &r)
				}
			}
	*/
}
