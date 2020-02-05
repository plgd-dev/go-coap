package coap

import (
	"github.com/cenkalti/backoff/v4"
)

// Stop indicates that no more retries should be made for use in NextBackOff().
const Stop = backoff.Stop

// BackOff is a backoff policy for retrying an operation.
type BackOff = backoff.BackOff
