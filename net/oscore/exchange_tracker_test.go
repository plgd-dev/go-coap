package oscore

import (
	"errors"
	"testing"
)

type testTrackedExchange struct {
	exchange Exchange
}

func TestExchangeTrackerMatch(t *testing.T) {
	tracker := NewExchangeTracker[testTrackedExchange]()
	exchange := Exchange{responseState: &exchangeResponseState{}}
	tracker["token"] = testTrackedExchange{exchange: exchange}
	if _, err := tracker.Match("token", exchange, func(tracked testTrackedExchange) Exchange {
		return tracked.exchange
	}); err != nil {
		t.Fatalf("match exchange: %v", err)
	}
}

func TestExchangeTrackerMatchRejectsReplacedExchange(t *testing.T) {
	tracker := NewExchangeTracker[testTrackedExchange]()
	tracker["token"] = testTrackedExchange{exchange: Exchange{responseState: &exchangeResponseState{}}}
	_, err := tracker.Match(
		"token",
		Exchange{},
		func(exchange testTrackedExchange) Exchange { return exchange.exchange },
	)
	if !errors.Is(err, ErrInvalidOSCOREMessage) {
		t.Fatalf("replaced exchange error = %v, want ErrInvalidOSCOREMessage", err)
	}
}
