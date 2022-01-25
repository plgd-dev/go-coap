package message

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMediaTypeString(t *testing.T) {
	for i := 0; i < 12000; i++ {
		func(mt int, s string) {
			if v, err := ToMediaType(s); err == nil {
				require.Equal(t, MediaType(mt), v)
			}
		}(i, MediaType(i).String())
	}
}

func TestOptionIDString(t *testing.T) {
	for i := 0; i < 12000; i++ {
		func(oid int, s string) {
			if v, err := ToOptionID(s); err == nil {
				require.Equal(t, OptionID(oid), v)
			}
		}(i, OptionID(i).String())
	}
}
