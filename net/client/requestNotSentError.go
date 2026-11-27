package client

import "github.com/plgd-dev/go-coap/v3/internal/requestnotsent"

// RequestNotSentError identifies a failure that occurred before transmission.
// Transport errors must not use this wrapper if the peer might have received
// any part of the request.
type RequestNotSentError = requestnotsent.Error
