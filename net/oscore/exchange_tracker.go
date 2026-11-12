package oscore

import "fmt"

// ExchangeTracker stores transport-specific exchange state by CoAP Token.
// Callers serialize access with their transport lock. The lifecycle is:
//
//	pending -> sent -> Observe notifications -> completed
//	                   \-> expired
type ExchangeTracker[T any] map[string]T

// NewExchangeTracker creates an empty token-to-exchange tracker.
func NewExchangeTracker[T any]() ExchangeTracker[T] {
	return make(ExchangeTracker[T])
}

// ExchangeTrackerKey returns the map key for a CoAP Token.
func ExchangeTrackerKey(token []byte) string {
	return string(token)
}

// UpdateResponse verifies that key still refers to expected and applies the
// common OSCORE Observe/replay checks before storing the updated exchange.
func (t ExchangeTracker[T]) UpdateResponse(
	key string,
	expected Exchange,
	option OptionValue,
	usedCancelExchange bool,
	exchangeOf func(T) Exchange,
	observeOf func(T) bool,
	responseStateOf func(*T) *ObserveResponseState,
) (T, error) {
	if exchangeOf == nil || observeOf == nil || responseStateOf == nil {
		var zero T
		return zero, fmt.Errorf("%w: incomplete exchange tracker adapters", ErrInvalidContext)
	}
	current, exists := t[key]
	if !exists || exchangeOf(current) != expected {
		var zero T
		return zero, fmt.Errorf("%w: response belongs to a replaced OSCORE exchange", ErrInvalidOSCOREMessage)
	}
	responseState := responseStateOf(&current)
	if responseState == nil {
		var zero T
		return zero, fmt.Errorf("%w: missing exchange response state", ErrInvalidContext)
	}
	if err := responseState.Update(
		option,
		observeOf(current),
		usedCancelExchange,
	); err != nil {
		var zero T
		return zero, err
	}
	t[key] = current
	return current, nil
}
