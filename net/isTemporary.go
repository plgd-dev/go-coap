package net

import (
	"net"
	"strings"
)

func isTemporary(err error) bool {
	if netErr, ok := err.(net.Error); ok && (netErr.Temporary() || netErr.Timeout()) {
		return true
	}
	// https://github.com/golang/go/blob/958e212db799e609b2a8df51cdd85c9341e7a404/src/internal/poll/fd.go#L43
	if strings.Contains(err.Error(), "i/o timeout") {
		return true
	}
	return false
}
