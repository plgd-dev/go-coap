package mux

import (
	"errors"
	"io"
	"sync"

	"github.com/go-ocf/go-coap/v2/message"
	"github.com/go-ocf/go-coap/v2/message/codes"
)

type ResponseWriter = interface {
	SetResponse(code codes.Code, contentFormat message.MediaType, d io.ReadSeeker, opts ...message.Option) error
	Client() Client
}

type Handler interface {
	ServeCOAP(w ResponseWriter, r *Message)
}

// The HandlerFunc type is an adapter to allow the use of
// ordinary functions as COAP handlers.  If f is a function
// with the appropriate signature, HandlerFunc(f) is a
// Handler object that calls f.
type HandlerFunc func(w ResponseWriter, r *Message)

// ServeCOAP calls f(w, r).
func (f HandlerFunc) ServeCOAP(w ResponseWriter, r *Message) {
	f(w, r)
}

// ServeMux is an COAP request multiplexer. It matches the
// path name of each incoming request against a list of
// registered patterns add calls the handler for the pattern
// with same name.
// ServeMux is also safe for concurrent access from multiple goroutines.
type ServeMux struct {
	z              map[string]muxEntry
	m              *sync.RWMutex
	defaultHandler Handler
}

type muxEntry struct {
	h       Handler
	pattern string
}

// NewServeMux allocates and returns a new ServeMux.
func NewServeMux() *ServeMux {
	return &ServeMux{z: make(map[string]muxEntry), m: new(sync.RWMutex), defaultHandler: HandlerFunc(func(w ResponseWriter, r *Message) {
		w.SetResponse(codes.NotFound, message.TextPlain, nil)
	})}
}

// Does path match pattern?
func pathMatch(pattern, path string) bool {
	switch pattern {
	case "", "/":
		switch path {
		case "", "/":
			return true
		}
		return false
	default:
		n := len(pattern)
		if pattern[n-1] != '/' {
			return pattern == path
		}
		return len(path) >= n && path[0:n] == pattern
	}
}

// Find a handler on a handler map given a path string
// Most-specific (longest) pattern wins
func (mux *ServeMux) match(path string) (h Handler, pattern string) {
	mux.m.RLock()
	defer mux.m.RUnlock()
	var n = 0
	for k, v := range mux.z {
		if !pathMatch(k, path) {
			continue
		}
		if h == nil || len(k) > n {
			n = len(k)
			h = v.h
			pattern = v.pattern
		}
	}
	return
}

// Handle adds a handler to the ServeMux for pattern.
func (mux *ServeMux) Handle(pattern string, handler Handler) error {
	switch pattern {
	case "", "/":
		pattern = "/"
	default:
		if pattern[0] == '/' {
			pattern = pattern[1:]
		}
	}

	if handler == nil {
		return errors.New("nil handler")
	}

	mux.m.Lock()
	mux.z[pattern] = muxEntry{h: handler, pattern: pattern}
	mux.m.Unlock()
	return nil
}

// DefaultHandle set default handler to the ServeMux
func (mux *ServeMux) DefaultHandle(handler Handler) {
	mux.m.Lock()
	mux.defaultHandler = handler
	mux.m.Unlock()
}

// HandleFunc adds a handler function to the ServeMux for pattern.
func (mux *ServeMux) HandleFunc(pattern string, handler func(w ResponseWriter, r *Message)) {
	mux.Handle(pattern, HandlerFunc(handler))
}

// DefaultHandleFunc set a default handler function to the ServeMux.
func (mux *ServeMux) DefaultHandleFunc(handler func(w ResponseWriter, r *Message)) {
	mux.DefaultHandle(HandlerFunc(handler))
}

// HandleRemove deregistrars the handler specific for pattern from the ServeMux.
func (mux *ServeMux) HandleRemove(pattern string) error {
	switch pattern {
	case "", "/":
		pattern = "/"
	}
	mux.m.Lock()
	defer mux.m.Unlock()
	if _, ok := mux.z[pattern]; ok {
		delete(mux.z, pattern)
		return nil
	}
	return errors.New("pattern is not registered in")
}

// ServeCOAP dispatches the request to the handler whose
// pattern most closely matches the request message. If DefaultServeMux
// is used the correct thing for DS queries is done: a possible parent
// is sought.
// If no handler is found a standard NotFound message is returned
func (mux *ServeMux) ServeCOAP(w ResponseWriter, r *Message) {
	path, err := r.Options.Path()
	if err != nil {
		mux.defaultHandler.ServeCOAP(w, r)
		return
	}
	h, _ := mux.match(path)
	if h == nil {
		h = mux.defaultHandler
	}
	if h == nil {
		return
	}
	h.ServeCOAP(w, r)
}
