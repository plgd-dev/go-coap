package message

import (
	"testing"
)

func TestMediaTypeString(t *testing.T) {
	for i := 0; i < 12000; i++ {
		func(string) {
		}(MediaType(i).String())
	}
}

func TestOptionIDString(t *testing.T) {
	for i := 0; i < 12000; i++ {
		func(string) {
		}(OptionID(i).String())
	}
}
