package oscore

import (
	"errors"
	"testing"
)

type testTrackedExchange struct {
	exchange Exchange
	observe  bool
	response ObserveResponseState
}

func TestExchangeTrackerUpdateResponse(t *testing.T) {
	tracker := NewExchangeTracker[testTrackedExchange]()
	tracker["token"] = testTrackedExchange{observe: true}

	update := func(partialIV []byte) error {
		_, err := tracker.UpdateResponse(
			"token",
			Exchange{},
			OptionValue{PartialIV: partialIV},
			false,
			func(exchange testTrackedExchange) Exchange { return exchange.exchange },
			func(exchange testTrackedExchange) bool { return exchange.observe },
			func(exchange *testTrackedExchange) *ObserveResponseState {
				return &exchange.response
			},
		)
		return err
	}

	if err := update([]byte{1}); err != nil {
		t.Fatalf("first Observe response: %v", err)
	}
	if err := update([]byte{1}); !errors.Is(err, ErrReplay) {
		t.Fatalf("duplicate Observe sequence error = %v, want ErrReplay", err)
	}
	if got := tracker["token"].response.Sequence; got != 1 {
		t.Fatalf("stored Observe sequence = %d, want 1", got)
	}
}

func TestExchangeTrackerUpdateResponseRejectsReplacedExchange(t *testing.T) {
	tracker := NewExchangeTracker[testTrackedExchange]()
	tracker["token"] = testTrackedExchange{
		exchange: Exchange{responseState: &exchangeResponseState{}},
	}
	_, err := tracker.UpdateResponse(
		"token",
		Exchange{},
		OptionValue{},
		false,
		func(exchange testTrackedExchange) Exchange { return exchange.exchange },
		func(exchange testTrackedExchange) bool { return exchange.observe },
		func(exchange *testTrackedExchange) *ObserveResponseState {
			return &exchange.response
		},
	)
	if !errors.Is(err, ErrInvalidOSCOREMessage) {
		t.Fatalf("replaced exchange error = %v, want ErrInvalidOSCOREMessage", err)
	}
}
