package mux

import (
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
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

type ErrorFunc = func(error)

// ServeCOAP calls f(w, r).
func (f HandlerFunc) ServeCOAP(w ResponseWriter, r *Message) {
	f(w, r)
}

// Router is an COAP request multiplexer. It matches the
// path name of each incoming request against a list of
// registered patterns add calls the handler for the pattern
// with same name.
// Router is also safe for concurrent access from multiple goroutines.
type Router struct {
	middlewares []MiddlewareFunc
	errors      ErrorFunc

	m              *sync.RWMutex
	defaultHandler Handler             // guarded by m
	z              map[string]muxEntry // guarded by m
}

type muxEntry struct {
	h       Handler
	pattern string
}

// NewRouter allocates and returns a new Router.
func NewRouter() *Router {
	router := &Router{
		middlewares: make([]MiddlewareFunc, 0, 2),
		errors: func(err error) {
			fmt.Println(err)
		},

		m: new(sync.RWMutex),
		z: make(map[string]muxEntry),
	}
	router.defaultHandler = HandlerFunc(func(w ResponseWriter, m *Message) {
		if err := w.SetResponse(codes.NotFound, message.TextPlain, nil); err != nil {
			router.errors(fmt.Errorf("router handler: cannot set response: %w", err))
		}
	})
	return router
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
func (r *Router) match(path string) (h Handler, pattern string) {
	r.m.RLock()
	defer r.m.RUnlock()
	var n = 0
	for k, v := range r.z {
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

// Handle adds a handler to the Router for pattern.
func (r *Router) Handle(pattern string, handler Handler) error {
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

	r.m.Lock()
	r.z[pattern] = muxEntry{h: handler, pattern: pattern}
	r.m.Unlock()
	return nil
}

// DefaultHandle set default handler to the Router
func (r *Router) DefaultHandle(handler Handler) {
	r.m.Lock()
	defer r.m.Unlock()
	r.defaultHandler = handler
}

// HandleFunc adds a handler function to the Router for pattern.
func (r *Router) HandleFunc(pattern string, handler func(w ResponseWriter, r *Message)) {
	if err := r.Handle(pattern, HandlerFunc(handler)); err != nil {
		r.errors(fmt.Errorf("cannot handle pattern(%v): %w", pattern, err))
	}
}

// DefaultHandleFunc set a default handler function to the Router.
func (r *Router) DefaultHandleFunc(handler func(w ResponseWriter, r *Message)) {
	r.DefaultHandle(HandlerFunc(handler))
}

// HandleRemove deregistrars the handler specific for pattern from the Router.
func (r *Router) HandleRemove(pattern string) error {
	switch pattern {
	case "", "/":
		pattern = "/"
	}
	r.m.Lock()
	defer r.m.Unlock()
	if _, ok := r.z[pattern]; ok {
		delete(r.z, pattern)
		return nil
	}
	return errors.New("pattern is not registered in")
}

// ServeCOAP dispatches the request to the handler whose
// pattern most closely matches the request message. If DefaultServeMux
// is used the correct thing for DS queries is done: a possible parent
// is sought.
// If no handler is found a standard NotFound message is returned
func (r *Router) ServeCOAP(w ResponseWriter, req *Message) {
	path, err := req.Options.Path()
	if err != nil {
		r.defaultHandler.ServeCOAP(w, req)
		return
	}
	h, _ := r.match(path)
	if h == nil {
		r.m.Lock()
		h = r.defaultHandler
		r.m.Unlock()
	}
	if h == nil {
		return
	}
	for i := len(r.middlewares) - 1; i >= 0; i-- {
		h = r.middlewares[i].Middleware(h)
	}
	h.ServeCOAP(w, req)
}
