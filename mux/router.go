package mux

import (
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"golang.org/x/exp/maps" // TODO: replace with standard maps package as soon as Go dependency hits 1.21
)

type ResponseWriter = interface {
	SetResponse(code codes.Code, contentFormat message.MediaType, d io.ReadSeeker, opts ...message.Option) error
	Conn() Conn
	SetMessage(m *pool.Message)
	Message() *pool.Message
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
	defaultHandler Handler          // guarded by m
	z              map[string]Route // guarded by m
}

type Route struct {
	h            Handler
	pattern      string
	regexMatcher *routeRegexp
}

func (route *Route) GetRouteRegexp() (string, error) {
	if route.regexMatcher.regexp == nil {
		return "", errors.New("mux: route does not have a regexp")
	}
	return route.regexMatcher.regexp.String(), nil
}

// NewRouter allocates and returns a new Router.
func NewRouter() *Router {
	router := &Router{
		middlewares: make([]MiddlewareFunc, 0, 2),
		errors: func(err error) {
			fmt.Println(err)
		},

		m: new(sync.RWMutex),
		z: make(map[string]Route),
	}
	router.defaultHandler = HandlerFunc(func(w ResponseWriter, _ *Message) {
		if err := w.SetResponse(codes.NotFound, message.TextPlain, nil); err != nil {
			router.errors(fmt.Errorf("router handler: cannot set response: %w", err))
		}
	})
	return router
}

// SetErrorHandler sets a custom error handler for the default mux handler set in the constructor.
func (r *Router) SetErrorHandler(h func(error)) {
	r.m.Lock()
	defer r.m.Unlock()
	r.errors = h
}

// Does path match pattern?
func pathMatch(pattern Route, path string) bool {
	return pattern.regexMatcher.regexp.MatchString(path)
}

// FilterPath checks the unfiltered input path or pattern against a blacklist and transforms them into valid paths
// of the same semantic meaning
func FilterPath(unfiltered string) string {
	if unfiltered == "" {
		return "/"
	}
	return unfiltered
}

// Find a handler on a handler map given a path string
// Most-specific (longest) pattern wins
func (r *Router) Match(path string, routeParams *RouteParams) (matchedRoute *Route, matchedPattern string) {
	path = FilterPath(path)
	r.m.RLock()
	n := 0
	for pattern, route := range r.z {
		if !pathMatch(route, path) {
			continue
		}
		if matchedRoute == nil || len(pattern) > n {
			n = len(pattern)
			r := route
			matchedRoute = &r
			matchedPattern = pattern
		}
	}
	r.m.RUnlock()

	if matchedRoute == nil {
		return
	}

	routeParams.Path = path
	if routeParams.Vars == nil {
		routeParams.Vars = make(map[string]string)
	}
	routeParams.PathTemplate = matchedPattern
	matchedRoute.regexMatcher.extractRouteParams(path, routeParams)

	return
}

// Handle adds a handler to the Router for pattern.
func (r *Router) Handle(pattern string, handler Handler) error {
	pattern = FilterPath(pattern)

	if handler == nil {
		return errors.New("nil handler")
	}

	routeRegex, err := newRouteRegexp(pattern)
	if err != nil {
		return err
	}

	r.m.Lock()
	r.z[pattern] = Route{h: handler, pattern: pattern, regexMatcher: routeRegex}
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
// This function will panic if the pattern parameter is invalid. If the APP provides 'user defined patterns' better
// use Handle(), which will return an error.
func (r *Router) HandleFunc(pattern string, handler func(w ResponseWriter, r *Message)) {
	if err := r.Handle(pattern, HandlerFunc(handler)); err != nil {
		panic(fmt.Errorf("cannot handle pattern(%v): %w", pattern, err))
	}
}

// DefaultHandleFunc set a default handler function to the Router.
func (r *Router) DefaultHandleFunc(handler func(w ResponseWriter, r *Message)) {
	r.DefaultHandle(HandlerFunc(handler))
}

// HandleRemove deregistrars the handler specific for pattern from the Router.
func (r *Router) HandleRemove(pattern string) error {
	pattern = FilterPath(pattern)
	r.m.Lock()
	defer r.m.Unlock()
	if _, ok := r.z[pattern]; ok {
		delete(r.z, pattern)
		return nil
	}
	return errors.New("pattern is not registered in")
}

// GetRoute obtains route from the pattern it has been assigned
func (r *Router) GetRoute(pattern string) *Route {
	pattern = FilterPath(pattern)
	r.m.RLock()
	defer r.m.RUnlock()
	if route, ok := r.z[pattern]; ok {
		return &route
	}
	return nil
}

func (r *Router) GetRoutes() map[string]Route {
	r.m.RLock()
	defer r.m.RUnlock()
	return maps.Clone(r.z)
}

// ServeCOAP dispatches the request to the handler whose
// pattern most closely matches the request message. If DefaultServeMux
// is used the correct thing for DS queries is done: a possible parent
// is sought.
// If no handler is found a standard NotFound message is returned
func (r *Router) ServeCOAP(w ResponseWriter, req *Message) {
	path, err := req.Options().Path()
	r.m.RLock()
	defaultHandler := r.defaultHandler
	r.m.RUnlock()
	if err != nil && !errors.Is(err, message.ErrOptionNotFound) {
		defaultHandler.ServeCOAP(w, req)
		return
	}
	var h Handler
	matchedMuxEntry, _ := r.Match(path, req.RouteParams)
	if matchedMuxEntry == nil {
		h = defaultHandler
	} else {
		h = matchedMuxEntry.h
	}
	if h == nil {
		return
	}

	for i := len(r.middlewares) - 1; i >= 0; i-- {
		h = r.middlewares[i].Middleware(h)
	}
	h.ServeCOAP(w, req)
}
