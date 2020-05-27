package message

import (
	"testing"
)

func TestMediaType_String(t *testing.T) {
	for i := 0; i < 12000; i++ {
		func(string) {
		}(MediaType(i).String())
	}
}

func TestOptionID_String(t *testing.T) {
	for i := 0; i < 12000; i++ {
		func(string) {
		}(OptionID(i).String())
	}
}
