package codes

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJSONUnmarshal(t *testing.T) {
	var got []Code
	want := []Code{GET, NotFound, InternalServerError, Abort}
	in := `["GET", "NotFound", "InternalServerError", "Abort"]`
	err := json.Unmarshal([]byte(in), &got)
	require.NoError(t, err)
	require.Equal(t, want, got)

	inNumeric := "["
	for i, c := range want {
		if i > 0 {
			inNumeric += ","
		}
		inNumeric += strconv.FormatUint(uint64(c), 10)
	}
	inNumeric += "]"
	err = json.Unmarshal([]byte(inNumeric), &got)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestUnmarshalJSONNoop(t *testing.T) {
	var got Code
	err := got.UnmarshalJSON([]byte("null"))
	require.NoError(t, err)
}

func TestUnmarshalJSONNilReceiver(t *testing.T) {
	var got *Code
	in := GET.String()
	err := got.UnmarshalJSON([]byte(in))
	require.Error(t, err)
}

func TestUnmarshalJSONUnknownInput(t *testing.T) {
	inputs := [][]byte{nil, []byte(""), []byte("xxx"), []byte("Code(17)"), []byte("255")}
	for _, in := range inputs {
		var got Code
		err := got.UnmarshalJSON(in)
		require.Error(t, err)
	}

	var got Code
	longStr := "This is a very long string that is longer than the max code length"
	require.True(t, len(longStr) > getMaxCodeLen())
	err := got.UnmarshalJSON([]byte(longStr))
	require.Error(t, err)
}

func TestUnmarshalJSONMarshalUnmarshal(t *testing.T) {
	for i := 0; i < _maxCode; i++ {
		var cUnMarshaled Code
		c := Code(i)

		cJSON, err := json.Marshal(c)
		require.NoError(t, err)

		err = json.Unmarshal(cJSON, &cUnMarshaled)
		require.NoError(t, err)

		require.Equal(t, c, cUnMarshaled)
	}
}

func TestCodeToString(t *testing.T) {
	var strCodes []string
	for _, val := range codeToString {
		strCodes = append(strCodes, val)
	}

	for _, str := range strCodes {
		_, err := ToCode(str)
		require.NoError(t, err)
	}
}

func FuzzUnmarshalJSON(f *testing.F) {
	f.Add([]byte("null"))
	f.Add([]byte("xxx"))
	f.Add([]byte("Code(17)"))
	f.Add([]byte("0b101010"))
	f.Add([]byte("0o52"))
	f.Add([]byte("0x2a"))
	f.Add([]byte("42"))

	f.Fuzz(func(_ *testing.T, input_data []byte) {
		var got Code
		_ = got.UnmarshalJSON(input_data)
	})
}
