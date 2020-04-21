package udp

import (
	"strconv"
)

// COAPType represents the message type.
type COAPType uint8

const (
	// Confirmable messages require acknowledgements.
	Confirmable COAPType = 0
	// NonConfirmable messages do not require acknowledgements.
	NonConfirmable COAPType = 1
	// Acknowledgement is a message indicating a response to confirmable message.
	Acknowledgement COAPType = 2
	// Reset indicates a permanent negative acknowledgement.
	Reset COAPType = 3
)

func (t COAPType) String() string {
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
