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
	require.Equal(t, QBlock1, OptionID(19))
	require.Equal(t, QBlock2, OptionID(31))
	require.Equal(t, RequestTag, OptionID(292))
	require.Equal(t, MediaType(272), AppMissingBlocksCBORSeq)
}

func TestQBlockDefinitions(t *testing.T) {
	for _, id := range []OptionID{QBlock1, QBlock2} {
		require.Equal(t, OptionDef{MinLen: 0, MaxLen: 3, ValueFormat: ValueUint}, CoapOptionDefs[id])
		require.True(t, VerifyOptLen(id, 0))
		require.True(t, VerifyOptLen(id, 3))
		require.False(t, VerifyOptLen(id, 4))
	}
	require.Equal(t, OptionDef{MinLen: 0, MaxLen: 8, ValueFormat: ValueOpaque}, CoapOptionDefs[RequestTag])
	require.True(t, VerifyOptLen(RequestTag, 8))
	require.False(t, VerifyOptLen(RequestTag, 9))
}

func TestQBlockNames(t *testing.T) {
	for _, id := range []OptionID{QBlock1, QBlock2, RequestTag} {
		got, err := ToOptionID(id.String())
		require.NoError(t, err)
		require.Equal(t, id, got)
	}
	const name = "application/missing-blocks+cbor-seq"
	require.Equal(t, name, AppMissingBlocksCBORSeq.String())
	got, err := ToMediaType(name)
	require.NoError(t, err)
	require.Equal(t, AppMissingBlocksCBORSeq, got)
}

func TestQBlockRepeatedOptionsRoundTrip(t *testing.T) {
	buf := make([]byte, 64)
	var opts Options
	var n int
	var err error
	opts, n, err = opts.AddUint32(buf, QBlock2, 1)
	require.NoError(t, err)
	opts, _, err = opts.AddUint32(buf[n:], QBlock2, 2)
	require.NoError(t, err)
	opts, n, err = opts.AddBytes(buf[2:], RequestTag, []byte{3})
	require.NoError(t, err)
	opts, _, err = opts.AddBytes(buf[2+n:], RequestTag, []byte{4})
	require.NoError(t, err)
	wire := make([]byte, 64)
	used, err := opts.Marshal(wire)
	require.NoError(t, err)
	decoded := make(Options, 0, 4)
	_, err = decoded.Unmarshal(wire[:used], CoapOptionDefs)
	require.NoError(t, err)
	var values []uint32
	for _, opt := range decoded {
		if opt.ID == QBlock2 {
			value, _, decodeErr := DecodeUint32(opt.Value)
			require.NoError(t, decodeErr)
			values = append(values, value)
		}
	}
	require.Equal(t, []uint32{1, 2}, values)
	tags := make([][]byte, 2)
	count, err := decoded.GetBytess(RequestTag, tags)
	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.Equal(t, [][]byte{{3}, {4}}, tags)
}
