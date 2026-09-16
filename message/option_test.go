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

func TestQBlockIdentifiers(t *testing.T) {
	require.Equal(t, 19, int(QBlock1))
	require.Equal(t, 31, int(QBlock2))
	require.Equal(t, 292, int(RequestTag))
	require.Equal(t, MediaType(272), AppMissingBlocksCBORSeq)
}

func TestQBlockOptionDefinitions(t *testing.T) {
	require.Equal(t, OptionDef{ValueFormat: ValueUint, MinLen: 0, MaxLen: 3}, CoapOptionDefs[QBlock1])
	require.Equal(t, OptionDef{ValueFormat: ValueUint, MinLen: 0, MaxLen: 3}, CoapOptionDefs[QBlock2])
	require.Equal(t, OptionDef{ValueFormat: ValueOpaque, MinLen: 0, MaxLen: 8}, CoapOptionDefs[RequestTag])
}

func TestQBlockOptionNames(t *testing.T) {
	tests := []struct {
		id   OptionID
		name string
	}{
		{id: QBlock1, name: "QBlock1"},
		{id: QBlock2, name: "QBlock2"},
		{id: RequestTag, name: "RequestTag"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.name, tt.id.String())
			got, err := ToOptionID(tt.name)
			require.NoError(t, err)
			require.Equal(t, tt.id, got)
		})
	}
}

func TestQBlockRepeatedOptionsWireRoundTrip(t *testing.T) {
	want := Options{
		{ID: QBlock2, Value: []byte{0x01}},
		{ID: QBlock2, Value: []byte{0x02, 0x03, 0x04}},
		{ID: RequestTag, Value: []byte{}},
		{ID: RequestTag, Value: []byte("tag-two")},
	}
	wire := make([]byte, 64)
	n, err := want.Marshal(wire)
	require.NoError(t, err)

	got := make(Options, 0, len(want))
	consumed, err := got.Unmarshal(wire[:n], CoapOptionDefs)
	require.NoError(t, err)
	require.Equal(t, n, consumed)
	require.Equal(t, want, got)
}

func TestQBlockMissingBlocksMediaTypeName(t *testing.T) {
	const name = "application/missing-blocks+cbor-seq"
	require.Equal(t, name, AppMissingBlocksCBORSeq.String())
	got, err := ToMediaType(name)
	require.NoError(t, err)
	require.Equal(t, AppMissingBlocksCBORSeq, got)
}
