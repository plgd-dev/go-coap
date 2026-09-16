package qblock

import (
	"math"
	"testing"
	"time"
)

func TestDefaultTransferConfig(t *testing.T) {
	cfg := DefaultTransferConfig()
	if cfg.MaxPayloads != 10 {
		t.Fatalf("MaxPayloads = %d, want 10", cfg.MaxPayloads)
	}
	if cfg.MaxBodySize != 1<<20 {
		t.Fatalf("MaxBodySize = %d, want %d", cfg.MaxBodySize, 1<<20)
	}
	if cfg.NonTimeout != 2*time.Second {
		t.Fatalf("NonTimeout = %v, want 2s", cfg.NonTimeout)
	}
	if cfg.NonReceiveTimeout != 4*time.Second {
		t.Fatalf("NonReceiveTimeout = %v, want 4s", cfg.NonReceiveTimeout)
	}
	if cfg.NonMaxRetransmit != 4 {
		t.Fatalf("NonMaxRetransmit = %d, want 4", cfg.NonMaxRetransmit)
	}
	if cfg.Lifetime != 247*time.Second {
		t.Fatalf("Lifetime = %v, want 247s", cfg.Lifetime)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestTransferConfigRejectsInvalidValues(t *testing.T) {
	valid := DefaultTransferConfig()
	tests := []struct {
		name string
		edit func(*TransferConfig)
	}{
		{name: "zero max payloads", edit: func(c *TransferConfig) { c.MaxPayloads = 0 }},
		{name: "too many max payloads", edit: func(c *TransferConfig) { c.MaxPayloads = 1<<20 + 1 }},
		{name: "zero max body size", edit: func(c *TransferConfig) { c.MaxBodySize = 0 }},
		{name: "zero non timeout", edit: func(c *TransferConfig) { c.NonTimeout = 0 }},
		{name: "negative non timeout", edit: func(c *TransferConfig) { c.NonTimeout = -time.Nanosecond }},
		{name: "zero receive timeout", edit: func(c *TransferConfig) { c.NonReceiveTimeout = 0 }},
		{name: "negative receive timeout", edit: func(c *TransferConfig) { c.NonReceiveTimeout = -time.Nanosecond }},
		{name: "receive timeout below minimum", edit: func(c *TransferConfig) { c.NonReceiveTimeout = 4*time.Second - time.Nanosecond }},
		{name: "receive timeout half nanosecond boundary", edit: func(c *TransferConfig) {
			c.NonTimeout = time.Nanosecond
			c.NonReceiveTimeout = time.Second + time.Nanosecond
		}},
		{name: "receive timeout arithmetic overflow", edit: func(c *TransferConfig) {
			c.NonTimeout = time.Duration(math.MaxInt64)
			c.NonReceiveTimeout = time.Duration(math.MaxInt64)
		}},
		{name: "too many retransmits", edit: func(c *TransferConfig) { c.NonMaxRetransmit = 31 }},
		{name: "zero lifetime", edit: func(c *TransferConfig) { c.Lifetime = 0 }},
		{name: "negative lifetime", edit: func(c *TransferConfig) { c.Lifetime = -time.Nanosecond }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.edit(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate() = nil, want error")
			}
		})
	}
}

func TestTransferConfigAcceptsReceiveTimeoutMinimum(t *testing.T) {
	cfg := DefaultTransferConfig()
	cfg.NonTimeout = time.Nanosecond
	cfg.NonReceiveTimeout = time.Second + 2*time.Nanosecond
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestTimingSendDelay(t *testing.T) {
	cfg := DefaultTransferConfig()
	for _, tt := range []struct {
		name   string
		jitter float64
		want   time.Duration
	}{
		{name: "minimum", jitter: 0, want: 2 * time.Second},
		{name: "middle", jitter: 0.5, want: 2500 * time.Millisecond},
		{name: "maximum", jitter: 1, want: 3 * time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cfg.sendDelay(tt.jitter)
			if err != nil {
				t.Fatalf("sendDelay(%v) = %v", tt.jitter, err)
			}
			if got != tt.want {
				t.Fatalf("sendDelay(%v) = %v, want %v", tt.jitter, got, tt.want)
			}
		})
	}
}

func TestTimingSendDelayRejectsInvalidJitterAndOverflow(t *testing.T) {
	tests := []struct {
		name   string
		cfg    TransferConfig
		jitter float64
	}{
		{name: "negative", cfg: DefaultTransferConfig(), jitter: -math.SmallestNonzeroFloat64},
		{name: "above one", cfg: DefaultTransferConfig(), jitter: math.Nextafter(1, 2)},
		{name: "nan", cfg: DefaultTransferConfig(), jitter: math.NaN()},
		{name: "positive infinity", cfg: DefaultTransferConfig(), jitter: math.Inf(1)},
		{name: "negative infinity", cfg: DefaultTransferConfig(), jitter: math.Inf(-1)},
		{name: "duration overflow", cfg: TransferConfig{NonTimeout: time.Duration(math.MaxInt64)}, jitter: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.cfg.sendDelay(tt.jitter); err == nil {
				t.Fatalf("sendDelay(%v) = nil error, want error", tt.jitter)
			}
		})
	}
}

func TestTimingRetryDelaySaturatesAtLifetime(t *testing.T) {
	cfg := DefaultTransferConfig()
	for _, tt := range []struct {
		attempt uint32
		want    time.Duration
	}{
		{attempt: 0, want: 4 * time.Second},
		{attempt: 1, want: 8 * time.Second},
		{attempt: 5, want: 128 * time.Second},
		{attempt: 6, want: 247 * time.Second},
		{attempt: math.MaxUint32, want: 247 * time.Second},
	} {
		if got := cfg.retryDelay(tt.attempt); got != tt.want {
			t.Errorf("retryDelay(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}

	overflow := TransferConfig{NonReceiveTimeout: time.Duration(math.MaxInt64/2 + 1), Lifetime: time.Duration(math.MaxInt64)}
	if got := overflow.retryDelay(1); got != overflow.Lifetime {
		t.Fatalf("overflow retryDelay(1) = %v, want %v", got, overflow.Lifetime)
	}
}

func TestTimingSetEndUsesFixedBoundaries(t *testing.T) {
	for _, tt := range []struct {
		number uint32
		want   uint32
	}{
		{number: 0, want: 10},
		{number: 9, want: 10},
		{number: 10, want: 20},
		{number: 20, want: 21},
	} {
		if got := setEnd(tt.number, 21, 10); got != tt.want {
			t.Errorf("setEnd(%d, 21, 10) = %d, want %d", tt.number, got, tt.want)
		}
	}
}

func TestTimingSetEndDoesNotOverflowUint32(t *testing.T) {
	if got := setEnd(math.MaxUint32-1, math.MaxUint32, 10); got != math.MaxUint32 {
		t.Fatalf("setEnd near uint32 limit = %d, want %d", got, uint32(math.MaxUint32))
	}
}
