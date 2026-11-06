package qblock

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

// Config contains RFC 9177 Section 7.2 congestion and recovery parameters.
// The zero value selects DefaultConfig. For customization, start with
// DefaultConfig and change individual fields. Both peers must use MaxPayloads.
type Config struct {
	MaxPayloads       int64
	NonTimeout        time.Duration
	NonReceiveTimeout time.Duration
	NonMaxRetransmit  int
	MaxLatency        time.Duration
}

// ErrInvalidConfig identifies invalid or overflowing Q-Block parameters.
var ErrInvalidConfig = errors.New("invalid Q-Block configuration")

// DefaultConfig returns the RFC 9177 defaults, independent of legacy CON tuning.
func DefaultConfig() Config {
	return Config{
		MaxPayloads: 10, NonTimeout: 2 * time.Second,
		NonReceiveTimeout: 4 * time.Second, NonMaxRetransmit: 4,
		MaxLatency: 100 * time.Second,
	}
}

// Resolve applies zero-value defaults and validates the complete configuration.
// MaxPayloads is capped at 256 to bound burst queues and recovery metadata.
func (c Config) Resolve() (Config, error) {
	if c == (Config{}) {
		c = DefaultConfig()
	}
	switch {
	case c.MaxPayloads < 1 || c.MaxPayloads > 256:
		return c, fmt.Errorf("%w: MaxPayloads must be between 1 and 256", ErrInvalidConfig)
	case c.NonTimeout <= 0 || c.NonTimeout > time.Hour:
		return c, fmt.Errorf("%w: NonTimeout must be positive and at most one hour", ErrInvalidConfig)
	case c.NonMaxRetransmit < 0 || c.NonMaxRetransmit > 20:
		return c, fmt.Errorf("%w: NonMaxRetransmit must be between 0 and 20", ErrInvalidConfig)
	case c.MaxLatency <= 0 || c.MaxLatency > 24*time.Hour:
		return c, fmt.Errorf("%w: MaxLatency must be positive and at most 24 hours", ErrInvalidConfig)
	}
	if c.NonReceiveTimeout < c.NonTimeout+c.NonTimeout/2+time.Second ||
		c.NonReceiveTimeout > (time.Duration(1<<63-1)>>c.NonMaxRetransmit)-time.Second {
		return c, fmt.Errorf("%w: NonReceiveTimeout must exceed 1.5*NonTimeout by at least one second and allow retry backoff", ErrInvalidConfig)
	}
	return c, nil
}

// NonPartialTimeout derives the partial-body lifetime using RFC 9177 Section 7.2.
// The receiver must use a resolved configuration.
func (c Config) NonPartialTimeout() time.Duration {
	span := c.NonTimeout * time.Duration((1<<c.NonMaxRetransmit)-1)
	return span + span/2 + 2*c.MaxLatency + c.NonTimeout
}

// NonProbingWait derives the probing cap using a body's randomized set delay.
func (c Config) NonProbingWait(delay time.Duration) time.Duration {
	return c.NonPartialTimeout() - c.NonTimeout + delay
}

func (c Config) nonDelay() time.Duration {
	// #nosec G404 -- congestion jitter is not security-sensitive randomness.
	return c.NonTimeout + time.Duration(rand.Int64N(max(int64(1), int64(c.NonTimeout/2))))
}

func (c Config) setLast(num, blockCount int64) int64 {
	return min((num/c.MaxPayloads+1)*c.MaxPayloads, blockCount) - 1
}
