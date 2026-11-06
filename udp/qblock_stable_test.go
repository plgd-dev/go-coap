package udp_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/stretchr/testify/require"
)

func TestQBlockLibcoapConfirmableProgress(t *testing.T) {
	if qLibcoapMode(t) != qLibcoapEnabled {
		return
	}
	binary := qLibcoapBinary(t, "coap-server")
	addr := startQLibcoapServer(t, binary, true, blockwise.SZX16)
	cc := qDial(t, addr, false, blockwise.SZX16, options.QBlockwiseOnly, new(qWireCounts))
	start := time.Now()
	payload := bytes.Repeat([]byte{7}, 321)
	response := qDo(t, cc, codes.PUT, "/progress", payload, message.Confirmable)
	body, err := response.ReadBody()
	require.NoError(t, err)
	require.Equal(t, payload, body)
	require.Less(t, time.Since(start), 7*time.Second)
}
