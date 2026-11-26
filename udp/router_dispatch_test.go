package udp

import (
	"sync/atomic"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/mux"
	"github.com/stretchr/testify/require"
)

func TestRouterInvalidPathGoesThroughMiddleware(t *testing.T) {
	router := mux.NewRouter()
	var defaultCalls atomic.Int32
	router.DefaultHandleFunc(func(w mux.ResponseWriter, _ *mux.Message) {
		defaultCalls.Add(1)
		if err := w.SetResponse(codes.Content, message.TextPlain, nil); err != nil {
			t.Errorf("set default response: %v", err)
		}
	})
	router.Use(func(mux.Handler) mux.Handler {
		return mux.HandlerFunc(func(w mux.ResponseWriter, _ *mux.Message) {
			if err := w.SetResponse(codes.Unauthorized, message.TextPlain, nil); err != nil {
				t.Errorf("set middleware response: %v", err)
			}
		})
	})
	client := newTokenlessTestServer(t, router)
	for _, path := range []string{"path/segment", string([]byte{0xff, 0xfe}), "unknown"} {
		response := client.get(path)
		require.Equal(t, codes.Unauthorized, response.Code)
	}
	require.Zero(t, defaultCalls.Load())
}
