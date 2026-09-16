package qblock

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// Kind identifies the Q-Block transfer direction.
type Kind uint8

const (
	Q1 Kind = iota + 1
	Q2
)

// ActionKind identifies an operation for the transfer owner to perform.
type ActionKind uint8

const (
	SendBlock ActionKind = iota + 1
	SendContinue
	RequestMissing
	Deliver
	Complete
	Duplicate
	Release
)

// Action is an owned output from a transfer state transition.
type Action struct {
	Kind    ActionKind
	Block   Block
	Payload []byte
	Numbers []uint32
	Through uint32
	Err     error
}

// TransferConfig bounds and times one Q-Block transfer.
type TransferConfig struct {
	MaxPayloads       uint32
	MaxBodySize       uint32
	NonTimeout        time.Duration
	NonReceiveTimeout time.Duration
	NonMaxRetransmit  uint32
	Lifetime          time.Duration
}

var (
	ErrExpired          = errors.New("q-block transfer expired")
	ErrRetriesExhausted = errors.New("q-block retries exhausted")
	ErrCanceled         = errors.New("q-block transfer canceled")
	ErrClosed           = errors.New("q-block transfer closed")
	ErrAlreadyStarted   = errors.New("q-block transfer already started")
	ErrInvalidRepair    = errors.New("invalid q-block repair request")
)

// DefaultTransferConfig returns protocol timing defaults and local resource limits.
func DefaultTransferConfig() TransferConfig {
	return TransferConfig{
		MaxPayloads:       10,
		MaxBodySize:       1 << 20,
		NonTimeout:        2 * time.Second,
		NonReceiveTimeout: 4 * time.Second,
		NonMaxRetransmit:  4,
		Lifetime:          247 * time.Second,
	}
}

// Validate checks transfer bounds and timing relationships.
func (c TransferConfig) Validate() error {
	if c.MaxPayloads == 0 || c.MaxPayloads > 1<<20 {
		return fmt.Errorf("max payloads must be in [1, %d]", 1<<20)
	}
	if c.MaxBodySize == 0 {
		return errors.New("max body size must be positive")
	}
	if c.NonTimeout <= 0 {
		return errors.New("NON timeout must be positive")
	}
	if c.NonReceiveTimeout <= 0 {
		return errors.New("NON receive timeout must be positive")
	}
	if c.NonMaxRetransmit > 30 {
		return errors.New("NON max retransmit must not exceed 30")
	}
	if c.Lifetime <= 0 {
		return errors.New("lifetime must be positive")
	}

	halfRoundedUp := c.NonTimeout/2 + c.NonTimeout%2
	if c.NonTimeout > time.Duration(math.MaxInt64)-time.Second-halfRoundedUp {
		return errors.New("NON receive timeout minimum overflows time.Duration")
	}
	minimumReceiveTimeout := c.NonTimeout + halfRoundedUp + time.Second
	if c.NonReceiveTimeout < minimumReceiveTimeout {
		return fmt.Errorf("NON receive timeout must be at least %v", minimumReceiveTimeout)
	}
	return nil
}
