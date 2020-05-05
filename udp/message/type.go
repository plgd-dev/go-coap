package udp

import (
	"strconv"
)

// Type represents the message type.
type Type uint8

const (
	// Confirmable messages require acknowledgements.
	Confirmable Type = 0
	// NonConfirmable messages do not require acknowledgements.
	NonConfirmable Type = 1
	// Acknowledgement is a message indicating a response to confirmable message.
	Acknowledgement Type = 2
	// Reset indicates a permanent negative acknowledgement.
	Reset Type = 3
)

func (t Type) String() string {
	switch t {
	case Confirmable:
		return "Confirmable"
	case NonConfirmable:
		return "NonConfirmable"
	case Acknowledgement:
		return "Acknowledgement"
	case Reset:
		return "reset"
	}
	return "Type(" + strconv.FormatInt(int64(t), 10) + ")"
}
