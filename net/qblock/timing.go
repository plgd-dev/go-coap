package qblock

import (
	"errors"
	"math"
	"time"
)

func (c TransferConfig) sendDelay(jitter float64) (time.Duration, error) {
	if math.IsNaN(jitter) || math.IsInf(jitter, 0) || jitter < 0 || jitter > 1 {
		return 0, errors.New("jitter must be finite and in [0, 1]")
	}
	if c.NonTimeout <= 0 {
		return 0, errors.New("NON timeout must be positive")
	}

	delay := float64(c.NonTimeout) * (1 + jitter/2)
	if delay >= float64(math.MaxInt64) {
		return 0, errors.New("send delay overflows time.Duration")
	}
	return time.Duration(delay), nil
}

func (c TransferConfig) retryDelay(attempt uint32) time.Duration {
	delay := c.NonReceiveTimeout
	if delay >= c.Lifetime {
		return c.Lifetime
	}
	for ; attempt > 0; attempt-- {
		if delay > c.Lifetime/2 {
			return c.Lifetime
		}
		delay *= 2
	}
	return delay
}

func setEnd(number, count, maxPayloads uint32) uint32 {
	if maxPayloads == 0 || number >= count {
		return count
	}
	start := number - number%maxPayloads
	remaining := count - start
	if remaining <= maxPayloads {
		return count
	}
	return start + maxPayloads
}
