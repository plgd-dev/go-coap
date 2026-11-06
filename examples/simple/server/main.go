package main

import (
	"bytes"
	"encoding/json"
	"log"
	"sync"

	coap "github.com/plgd-dev/go-coap/v3"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/mux"
)

type object struct {
	sync.Mutex
	values []string
}

func loggingMiddleware(next mux.Handler) mux.Handler {
	return mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		log.Printf("ClientAddress %v, %v\n", w.Conn().RemoteAddr(), r.String())
		next.ServeCOAP(w, r)
	})
}

func (o *object) handle(w mux.ResponseWriter, r *mux.Message) {
	switch r.Code() {
	case codes.GET:
		o.respondJSON(w, codes.Content, map[string][]string{"value": o.currentValues()})
	case codes.FETCH:
		var query struct {
			Field string `json:"field"`
		}
		if !o.readJSONRequest(w, r, message.AppJSON, &query) {
			return
		}
		if query.Field != "value" {
			o.respond(w, codes.BadRequest, message.TextPlain, "unknown field")
			return
		}
		o.respondJSON(w, codes.Content, map[string][]string{"value": o.currentValues()})
	case codes.PATCH:
		var patch []struct {
			Op    string `json:"op"`
			Path  string `json:"path"`
			Value string `json:"value"`
		}
		if !o.readJSONRequest(w, r, message.AppJSONPatch, &patch) {
			return
		}
		if len(patch) == 0 {
			o.respond(w, codes.BadRequest, message.TextPlain, "empty JSON Patch document")
			return
		}
		for _, operation := range patch {
			if operation.Op != "add" || operation.Path != "/value/-" {
				o.respond(w, codes.BadRequest, message.TextPlain, "only add operations to /value/- are supported")
				return
			}
		}
		o.Lock()
		for _, operation := range patch {
			o.values = append(o.values, operation.Value)
		}
		values := append([]string(nil), o.values...)
		o.Unlock()
		o.respondJSON(w, codes.Changed, map[string][]string{"value": values})
	case codes.IPATCH:
		var patch struct {
			Value *[]string `json:"value"`
		}
		if !o.readJSONRequest(w, r, message.AppJSONMergePatch, &patch) {
			return
		}
		if patch.Value == nil {
			o.respond(w, codes.BadRequest, message.TextPlain, "merge patch must include value")
			return
		}
		o.Lock()
		o.values = append(o.values[:0], (*patch.Value)...)
		values := append([]string(nil), o.values...)
		o.Unlock()
		o.respondJSON(w, codes.Changed, map[string][]string{"value": values})
	default:
		o.respond(w, codes.MethodNotAllowed, message.TextPlain, "method not allowed")
	}
}

func (o *object) currentValues() []string {
	o.Lock()
	defer o.Unlock()
	return append([]string(nil), o.values...)
}

func (o *object) readJSONRequest(w mux.ResponseWriter, r *mux.Message, expectedFormat message.MediaType, target any) bool {
	contentFormat, err := r.ContentFormat()
	if err != nil || contentFormat != expectedFormat {
		o.respond(w, codes.UnsupportedMediaType, message.TextPlain, expectedFormat.String()+" required")
		return false
	}
	body, err := r.ReadBody()
	if err != nil {
		log.Printf("cannot read request body: %v", err)
		o.respond(w, codes.BadRequest, message.TextPlain, "cannot read request body")
		return false
	}
	if err := json.Unmarshal(body, target); err != nil {
		o.respond(w, codes.BadRequest, message.TextPlain, "invalid JSON request")
		return false
	}
	return true
}

func (o *object) respond(w mux.ResponseWriter, code codes.Code, format message.MediaType, body string) {
	if err := w.SetResponse(code, format, bytes.NewReader([]byte(body))); err != nil {
		log.Printf("cannot set response: %v", err)
	}
}

func (o *object) respondJSON(w mux.ResponseWriter, code codes.Code, body any) {
	encoded, err := json.Marshal(body)
	if err != nil {
		log.Printf("cannot encode JSON response: %v", err)
		return
	}
	o.respond(w, code, message.AppJSON, string(encoded))
}

func handleA(w mux.ResponseWriter, _ *mux.Message) {
	err := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("hello world")))
	if err != nil {
		log.Printf("cannot set response: %v", err)
	}
}

func handleB(w mux.ResponseWriter, r *mux.Message) {
	customResp := w.Conn().AcquireMessage(r.Context())
	defer w.Conn().ReleaseMessage(customResp)
	customResp.SetCode(codes.Content)
	customResp.SetToken(r.Token())
	customResp.SetContentFormat(message.TextPlain)
	customResp.SetBody(bytes.NewReader([]byte("B hello world")))
	err := w.Conn().WriteMessage(customResp)
	if err != nil {
		log.Printf("cannot set response: %v", err)
	}
}

func main() {
	r := mux.NewRouter()
	r.Use(loggingMiddleware)
	r.Handle("/a", mux.HandlerFunc(handleA))
	r.Handle("/b", mux.HandlerFunc(handleB))
	r.Handle("/object", mux.HandlerFunc((&object{values: []string{"hello"}}).handle))

	log.Fatal(coap.ListenAndServe("udp", ":5685", r))
}
