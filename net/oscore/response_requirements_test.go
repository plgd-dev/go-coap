package oscore

import (
	"testing"
	"time"
)

func TestResponseRequirements(t *testing.T) {
	requirements := NewResponseRequirements()
	now := time.Now()
	active := Exchange{responseState: &exchangeResponseState{}}
	expired := Exchange{responseState: &exchangeResponseState{started: true}}

	requirements.Add("token", active, time.Time{})
	requirements.Add("token", expired, now.Add(-time.Second))
	if !requirements.Has("token", now) {
		t.Fatal("active requirement was not retained")
	}
	if !requirements.HasToken("token") {
		t.Fatal("active token requirement was removed")
	}
	if _, exists := requirements["token"][expired]; exists {
		t.Fatal("expired requirement was not removed")
	}

	requirements.RemoveMatching("token", func(exchange Exchange) bool {
		return exchange == active
	})
	if requirements.HasToken("token") {
		t.Fatal("removed requirement still exists")
	}
}
