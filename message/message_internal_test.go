package message

import (
	"testing"

	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/stretchr/testify/require"
)

func TestMessageIsPing(t *testing.T) {
	tests := []struct {
		name    string
		message *Message
		isTCP   bool
		want    bool
	}{
		{
			name: "Ping message (TCP)",
			message: &Message{
				Code:    codes.Ping,
				Type:    Confirmable,
				Token:   nil,
				Options: nil,
				Payload: nil,
			},
			isTCP: true,
			want:  true,
		},
		{
			name: "Ping message (UDP)",
			message: &Message{
				Code:    codes.Empty,
				Type:    Confirmable,
				Token:   nil,
				Options: nil,
				Payload: nil,
			},
			isTCP: false,
			want:  true,
		},
		{
			name: "Non-ping message (TCP)",
			message: &Message{
				Code:    codes.GET,
				Type:    Confirmable,
				Token:   []byte{1, 2, 3},
				Options: []Option{{ID: 1, Value: []byte{4, 5, 6}}},
				Payload: []byte{7, 8, 9},
			},
			isTCP: true,
			want:  false,
		},
		{
			name: "Non-ping message (UDP)",
			message: &Message{
				Code:    codes.GET,
				Type:    Confirmable,
				Token:   []byte{1, 2, 3},
				Options: []Option{{ID: 1, Value: []byte{4, 5, 6}}},
				Payload: []byte{7, 8, 9},
			},
			isTCP: false,
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.message.IsPing(tt.isTCP)
			require.Equal(t, tt.want, got)
		})
	}
}
