package mux_test

import (
	"context"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/mux"
	"github.com/stretchr/testify/require"
)

func TestRouterDispatchMiddlewareForInvalidPaths(t *testing.T) {
	const defaultHandler = "default"
	tests := []struct {
		name        string
		path        [][]byte
		wantHandler string
	}{
		{name: "slash in segment", path: [][]byte{[]byte("route/child")}, wantHandler: defaultHandler},
		{name: "invalid UTF-8", path: [][]byte{{0xff, 0xfe}}, wantHandler: defaultHandler},
		{name: "valid matching route", path: [][]byte{[]byte("route"), []byte("child")}, wantHandler: "route"},
		{name: "valid missing route", path: [][]byte{[]byte("unknown")}, wantHandler: defaultHandler},
		{name: "empty path", wantHandler: "root"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := mux.NewRouter()
			var calls []string
			r.DefaultHandleFunc(func(mux.ResponseWriter, *mux.Message) { calls = append(calls, defaultHandler) })
			r.HandleFunc("/route/child", func(mux.ResponseWriter, *mux.Message) { calls = append(calls, "route") })
			r.HandleFunc("/", func(mux.ResponseWriter, *mux.Message) { calls = append(calls, "root") })
			for _, name := range []string{"first", "second"} {
				r.Use(func(next mux.Handler) mux.Handler {
					return mux.HandlerFunc(func(w mux.ResponseWriter, req *mux.Message) {
						calls = append(calls, name+" before")
						next.ServeCOAP(w, req)
						calls = append(calls, name+" after")
					})
				})
			}
			msg := pool.NewMessage(context.Background())
			for _, segment := range tt.path {
				msg.AddOptionBytes(message.URIPath, segment)
			}
			req := &mux.Message{Message: msg, RouteParams: new(mux.RouteParams)}
			r.ServeCOAP(nil, req)
			require.Equal(t, []string{
				"first before", "second before", tt.wantHandler, "second after", "first after",
			}, calls)
			if tt.wantHandler == defaultHandler {
				require.Empty(t, req.RouteParams.PathTemplate)
			}
		})
	}
}

func TestRouterInvalidPathMiddlewareCanReject(t *testing.T) {
	r := mux.NewRouter()
	r.DefaultHandleFunc(func(mux.ResponseWriter, *mux.Message) { t.Error("default handler should not run") })
	var called bool
	r.Use(func(mux.Handler) mux.Handler {
		return mux.HandlerFunc(func(mux.ResponseWriter, *mux.Message) { called = true })
	})
	msg := pool.NewMessage(context.Background())
	msg.AddOptionBytes(message.URIPath, []byte("invalid/path"))
	r.ServeCOAP(nil, &mux.Message{Message: msg, RouteParams: new(mux.RouteParams)})
	require.True(t, called)
}

func TestRouterInvalidPathWithNilDefaultHandler(t *testing.T) {
	r := mux.NewRouter()
	r.DefaultHandle(nil)
	msg := pool.NewMessage(context.Background())
	msg.AddOptionBytes(message.URIPath, []byte("invalid/path"))
	require.NotPanics(t, func() {
		r.ServeCOAP(nil, &mux.Message{Message: msg, RouteParams: new(mux.RouteParams)})
	})
}
