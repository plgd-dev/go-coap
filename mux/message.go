package mux

import "github.com/go-ocf/go-coap/v2/message"

// Message contains message with sequence number.
type Message struct {
	*message.Message
	// SequenceNumber identifies the order of the message from a TCP connection. For UDP it is just for debugging.
	SequenceNumber uint64
}
