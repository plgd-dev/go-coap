package message

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQBlockOptionDefinitions(t *testing.T) {
	for _, tt := range []struct {
		id   OptionID
		num  uint16
		name string
	}{
		{QBlock1, 19, "QBlock1"},
		{QBlock2, 31, "QBlock2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.num, uint16(tt.id))
			require.Equal(t, tt.name, tt.id.String())
			id, err := ToOptionID(tt.name)
			require.NoError(t, err)
			require.Equal(t, tt.id, id)
			require.Equal(t, ValueUint, CoapOptionDefs[tt.id].ValueFormat)
			for size := 0; size <= 3; size++ {
				require.True(t, VerifyOptLen(tt.id, size))
			}
			require.False(t, VerifyOptLen(tt.id, 4))
			require.Equal(t, OptionID(1), tt.id&1, "critical")
			require.Equal(t, OptionID(2), tt.id&2, "unsafe")
		})
	}
}

func TestMissingBlocksMediaType(t *testing.T) {
	require.Equal(t, MediaType(272), AppMissingBlocksCBORSeq)
	require.Equal(t, "application/missing-blocks+cbor-seq", AppMissingBlocksCBORSeq.String())
	mt, err := ToMediaType("application/missing-blocks+cbor-seq")
	require.NoError(t, err)
	require.Equal(t, AppMissingBlocksCBORSeq, mt)
	mt, err = MediaTypeFromNumber(272)
	require.NoError(t, err)
	require.Equal(t, AppMissingBlocksCBORSeq, mt)
}
