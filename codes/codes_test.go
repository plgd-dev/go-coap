package codes

import (
	"encoding/json"
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
}

func TestUnmarshalJSON_NilReceiver(t *testing.T) {
	var got *Code
	in := GET.String()
	err := got.UnmarshalJSON([]byte(in))
	require.Error(t, err)
}

func TestUnmarshalJSON_UnknownInput(t *testing.T) {
	var got Code
	for _, in := range [][]byte{[]byte(""), []byte("xxx"), []byte("Code(17)"), nil} {
		err := got.UnmarshalJSON([]byte(in))
		require.Error(t, err)
	}
}

func TestUnmarshalJSON_MarshalUnmarshal(t *testing.T) {
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
