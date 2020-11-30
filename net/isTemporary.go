package net

import (
	"net"
	"strings"
	"time"
)

// https://github.com/golang/go/blob/958e212db799e609b2a8df51cdd85c9341e7a404/src/internal/poll/fd.go#L43
const ioTimeout = "i/o timeout"

func isTemporary(err error, deadline time.Time) bool {
	if deadline.After(time.Now()) {
		// when connection is closed during TLS handshake, it returns i/o timeout
		// so we need to validate if timeout real occurs by set deadline otherwise infinite loop occurs.
		return false
	}

	if netErr, ok := err.(net.Error); ok && (netErr.Temporary() || netErr.Timeout()) {
		return true
	}

	if strings.Contains(err.Error(), ioTimeout) {
		return true
	}
	return false
}
