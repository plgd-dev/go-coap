package mux_test

import (
	"log"

	"github.com/plgd-dev/go-coap/v2/mux"
)

// Middleware function, which will be called for each request
func loggingMiddleware(next mux.Handler) mux.Handler {
	return mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		log.Printf("ClientAddress %v, %v\n", w.Client().RemoteAddr(), r.String())
		next.ServeCOAP(w, r)
	})
}

func Example_authenticationMiddleware() {
	r := mux.NewRouter()
	r.HandleFunc("/", func(w mux.ResponseWriter, r *mux.Message) {
		// Do something here
	})
	r.Use(loggingMiddleware)
}
