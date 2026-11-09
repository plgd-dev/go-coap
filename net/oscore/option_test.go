package oscore

import (
	"bytes"
	"testing"
)

func TestRFC8613OSCOREOptionEncoding(t *testing.T) {
	tests := []struct {
		name string
		in   OptionValue
		want []byte
	}{
		{
			name: "empty option",
			in:   OptionValue{},
			want: []byte{},
		},
		{
			name: "partial IV zero and empty sender ID",
			in: OptionValue{
				PartialIV:   []byte{0},
				SenderIDSet: true,
			},
			want: []byte{0x09, 0x00},
		},
		{
			name: "RFC 8613 Appendix C.4 option",
			in: OptionValue{
				PartialIV:   []byte{0x14},
				SenderIDSet: true,
			},
			want: []byte{0x09, 0x14},
		},
		{
			name: "partial IV, ID context, sender ID",
			in: OptionValue{
				PartialIV:    []byte{0x14},
				IDContext:    []byte{0x37, 0xcb},
				SenderID:     []byte{0x01},
				IDContextSet: true,
				SenderIDSet:  true,
			},
			want: []byte{0x19, 0x14, 0x02, 0x37, 0xcb, 0x01},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := tt.in.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(encoded, tt.want) {
				t.Fatalf("encoded = %x, want %x", encoded, tt.want)
			}
			decoded, err := UnmarshalOption(encoded)
			if err != nil {
				t.Fatal(err)
			}
			roundTrip, err := decoded.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(roundTrip, encoded) {
				t.Fatalf("round-trip = %x, want %x", roundTrip, encoded)
			}
		})
	}
}

func TestUnmarshalOptionRejectsMalformedValues(t *testing.T) {
	tests := [][]byte{
		{0x00},
		{0x20},
		{0x06},
		{0x09},
		{0x10},
		{0x02, 0x00, 0x01},
		{0x10, 0x04, 0xaa},
		{0x11, 0x00, 0, 1, 2, 3, 4, 5, 6, 7, 8},
	}
	for _, value := range tests {
		if _, err := UnmarshalOption(value); err == nil {
			t.Errorf("UnmarshalOption(%x) succeeded", value)
		}
	}
}
