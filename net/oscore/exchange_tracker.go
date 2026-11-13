package oscore

import "fmt"

// ExchangeTracker stores transport-specific exchange state by CoAP Token.
// Callers serialize access with their transport lock.
type ExchangeTracker[T any] map[string]T

// NewExchangeTracker creates an empty token-to-exchange tracker.
func NewExchangeTracker[T any]() ExchangeTracker[T] {
	return make(ExchangeTracker[T])
}

// ExchangeTrackerKey returns the map key for a CoAP Token.
func ExchangeTrackerKey(token []byte) string {
	return string(token)
}

// Match verifies that key still refers to the expected OSCORE exchange.
func (t ExchangeTracker[T]) Match(
	key string,
	expected Exchange,
	exchangeOf func(T) Exchange,
) (T, error) {
	if exchangeOf == nil {
		var zero T
		return zero, fmt.Errorf("%w: missing exchange tracker adapter", ErrInvalidContext)
	}
	current, exists := t[key]
	if !exists || exchangeOf(current) != expected {
		var zero T
		return zero, fmt.Errorf("%w: response belongs to a replaced OSCORE exchange", ErrInvalidOSCOREMessage)
	}
	return current, nil
}
