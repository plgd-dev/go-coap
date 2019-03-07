package coap

import (
	"reflect"
	"testing"
)

func TestNoResponse2XXCodes(t *testing.T) {
	nr := newNoResponseWriter(ResponseWriter(&responseWriter{req: &Request{}}))
	codes := nr.decodeNoResponseOption(2)
	exp := resp2XXCodes
	if !reflect.DeepEqual(exp, codes) {
		t.Fatalf("Expected\n%#v\ngot\n%#v", exp, codes)
	}
}

func TestNoResponse4XXCodes(t *testing.T) {
	nr := newNoResponseWriter(ResponseWriter(&responseWriter{req: &Request{}}))
	codes := nr.decodeNoResponseOption(8)
	exp := resp4XXCodes
	if !reflect.DeepEqual(exp, codes) {
		t.Fatalf("Expected\n%#v\ngot\n%#v", exp, codes)
	}
}

func TestNoResponse5XXCodes(t *testing.T) {
	nr := newNoResponseWriter(ResponseWriter(&responseWriter{req: &Request{}}))
	codes := nr.decodeNoResponseOption(16)
	exp := resp5XXCodes
	if !reflect.DeepEqual(exp, codes) {
		t.Fatalf("Expected\n%#v\ngot\n%#v", exp, codes)
	}
}

func TestNoResponseCombinationXXCodes(t *testing.T) {
	nr := newNoResponseWriter(ResponseWriter(&responseWriter{req: &Request{}}))
	codes := nr.decodeNoResponseOption(18)
	exp := append(resp2XXCodes, resp5XXCodes...)
	if !reflect.DeepEqual(exp, codes) {
		t.Fatalf("Expected\n%#v\ngot\n%#v", exp, codes)
	}
}

func TestNoResponseAllCodes(t *testing.T) {
	nr := newNoResponseWriter(ResponseWriter(&responseWriter{req: &Request{}}))
	codes := nr.decodeNoResponseOption(0)
	exp := []COAPCode(nil)
	if !reflect.DeepEqual(exp, codes) {
		t.Fatalf("Expected\n%#v\ngot\n%#v", exp, codes)
	}
}
