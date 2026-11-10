package oscore

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
)

func TestRFC8613ExternalAAD(t *testing.T) {
	tests := []struct {
		name      string
		kid       []byte
		partialIV []byte
		want      []byte
	}{
		{
			name:      "RFC 8613 Appendix C.4",
			kid:       []byte{},
			partialIV: mustHex(t, "14"),
			want:      mustHex(t, "8368456e63727970743040488501810a40411440"),
		},
		{
			name:      "RFC 8613 Section 5.4 example",
			kid:       mustHex(t, "00"),
			partialIV: mustHex(t, "25"),
			want:      mustHex(t, "8368456e63727970743040498501810a4100412540"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildAAD(tt.kid, tt.partialIV, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tt.want) {
				t.Fatalf("AAD = %x, want %x", got, tt.want)
			}
		})
	}
}

func TestMatchesObserveRegistrationIgnoresOnlyObserveAndETag(t *testing.T) {
	registration := message.Options{
		{ID: message.Observe, Value: []byte{0}},
		{ID: message.ETag, Value: []byte("first")},
		{ID: message.URIPath, Value: []byte("resource")},
	}
	cancellation := message.Options{
		{ID: message.Observe, Value: []byte{1}},
		{ID: message.ETag, Value: []byte("second")},
		{ID: message.URIPath, Value: []byte("resource")},
	}
	if !MatchesObserveRegistration(registration, cancellation) {
		t.Fatal("cancellation with matching options did not match registration")
	}
	cancellation[2].Value = []byte("different")
	if MatchesObserveRegistration(registration, cancellation) {
		t.Fatal("cancellation with a different Uri-Path matched registration")
	}
}

func TestBuildAADWithAlgorithm(t *testing.T) {
	defaultAAD, err := BuildAAD(nil, []byte{1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	strongAAD, err := BuildAADWithAlgorithm(AEADAlgorithmAESCCM16_128_256, nil, []byte{1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(defaultAAD, strongAAD) {
		t.Fatal("AAD does not bind the selected algorithm")
	}
	if _, err := BuildAADWithAlgorithm(AEADAlgorithm(999), nil, []byte{1}, nil); !errors.Is(err, ErrUnsupportedAlgorithm) {
		t.Fatalf("unsupported algorithm error = %v", err)
	}
}

func TestRFC8613InnerMessageEncoding(t *testing.T) {
	options := message.Options{
		{ID: message.URIPath, Value: []byte("v1")},
	}
	encoded, err := EncodeInner(codes.GET, options, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := mustHex(t, "01b27631"); !bytes.Equal(encoded, want) {
		t.Fatalf("inner message = %x, want %x", encoded, want)
	}

	decoded, err := DecodeInner(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Code != codes.GET || len(decoded.Options) != 1 || decoded.Options[0].ID != message.URIPath ||
		!bytes.Equal(decoded.Options[0].Value, []byte("v1")) || len(decoded.Payload) != 0 {
		t.Fatalf("decoded inner message = %#v", decoded)
	}
}

func TestInnerMessagePayloadAndOptions(t *testing.T) {
	options := message.Options{
		{ID: message.URIPath, Value: []byte("blob")},
		{ID: message.ContentFormat, Value: []byte{byte(message.AppOctets)}},
	}
	payload := []byte{0x00, 0xff, 0x41}
	encoded, err := EncodeInner(codes.POST, options, payload)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeInner(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Code != codes.POST || !bytes.Equal(decoded.Payload, payload) {
		t.Fatalf("decoded code/payload = %v/%x", decoded.Code, decoded.Payload)
	}
	if len(decoded.Options) != len(options) {
		t.Fatalf("decoded options = %v, want %v", decoded.Options, options)
	}
	for i := range options {
		if decoded.Options[i].ID != options[i].ID || !bytes.Equal(decoded.Options[i].Value, options[i].Value) {
			t.Fatalf("decoded option[%d] = %#v, want %#v", i, decoded.Options[i], options[i])
		}
	}
}

func TestSplitOptionsUsesRFC8613Classes(t *testing.T) {
	options := message.Options{
		{ID: message.URIHost, Value: []byte("example.com")},
		{ID: message.URIPath, Value: []byte("sensors")},
		{ID: message.ContentFormat, Value: []byte{byte(message.AppJSON)}},
		{ID: message.ProxyScheme, Value: []byte("coap")},
		{ID: 99, Value: []byte("future-option")},
	}
	split, err := SplitOptions(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(split.Outer) != 2 || split.Outer[0].ID != message.URIHost || split.Outer[1].ID != message.ProxyScheme {
		t.Fatalf("outer options = %#v", split.Outer)
	}
	if len(split.Inner) != 3 || split.Inner[0].ID != message.URIPath ||
		split.Inner[1].ID != message.ContentFormat || split.Inner[2].ID != 99 {
		t.Fatalf("inner options = %#v", split.Inner)
	}
	for _, id := range []message.OptionID{message.Observe, message.Block1, message.Block2, message.Size1, message.Size2, message.NoResponse, 99} {
		if got := ClassifyOption(id); got != ClassE {
			t.Errorf("ClassifyOption(%d) = %v, want ClassE default", id, got)
		}
	}
	for _, id := range []message.OptionID{message.URIHost, message.URIPort, message.OSCORE, message.ProxyURI, message.ProxyScheme} {
		if got := ClassifyOption(id); got != ClassU {
			t.Errorf("ClassifyOption(%d) = %v, want ClassU", id, got)
		}
	}
}

func TestSplitOptionsRejectsNestedOSCOREAndUnsortedOptions(t *testing.T) {
	tests := []struct {
		name    string
		options message.Options
		wantErr error
	}{
		{
			name: "nested OSCORE",
			options: message.Options{
				{ID: message.OSCORE, Value: []byte{0}},
			},
			wantErr: ErrNestedOSCOREOption,
		},
		{
			name: "unsorted options",
			options: message.Options{
				{ID: message.URIPath, Value: []byte("x")},
				{ID: message.URIHost, Value: []byte("example.com")},
			},
			wantErr: ErrInvalidInnerMessage,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := SplitOptions(tt.options); err == nil || !strings.Contains(err.Error(), tt.wantErr.Error()) {
				t.Fatalf("SplitOptions error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestDecodeInnerRejectsOuterOptionsAndEmptyPayloadMarker(t *testing.T) {
	tests := [][]byte{
		{},
		{byte(codes.GET), 0x31, 'x'}, // Uri-Host is Class U.
		{byte(codes.GET), 0xff},
	}
	for _, input := range tests {
		if _, err := DecodeInner(input); err == nil {
			t.Errorf("DecodeInner(%x) succeeded", input)
		}
	}
}

func TestBuildAADRejectsNonIntegrityProtectedOptions(t *testing.T) {
	_, err := BuildAAD(nil, nil, message.Options{{ID: message.URIPath, Value: []byte("private")}})
	if err == nil {
		t.Fatal("expected non-Class-I option to be rejected")
	}
	if _, err := BuildAAD(nil, []byte{0, 1}, nil); err == nil {
		t.Fatal("expected non-canonical Partial IV to be rejected")
	}
}
