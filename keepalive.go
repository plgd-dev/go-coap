package coap

import (
	"context"
	"fmt"
	"time"
)

type keepAliveSession struct {
	networkSession
}

type RetryFunc = func() (when time.Time, err error)
type RetryFuncFactory = func() RetryFunc

// KeepAlive config
type KeepAlive struct {
	// Enable watch connection
	Enable bool
	// Interval between two success pings
	Interval time.Duration
	// WaitForPong how long it will waits for pong response.
	WaitForPong time.Duration
	// NewRetryPolicy creates retry policy for the connection when ping fails.
	NewRetryPolicy RetryFuncFactory
}

const minDuration = time.Millisecond * 50

// MakeKeepAlive creates a policy that detects dropped connections within the connTimeout limit
func MakeKeepAlive(connTimeout time.Duration) (KeepAlive, error) {
	duration := connTimeout / 4
	if duration < minDuration {
		return KeepAlive{}, fmt.Errorf("connTimeout %v it too small: must be greater than %v", connTimeout, minDuration*4)
	}
	return KeepAlive{
		Enable:      true,
		Interval:    duration,
		WaitForPong: duration,
		NewRetryPolicy: func() RetryFunc {
			now := time.Now()
			// try 2 times to send ping after fails
			return func() (time.Time, error) {
				c := time.Now()
				if c.Before(now.Add(duration * 2)) {
					return c.Add(duration), nil
				}
				return time.Time{}, ErrKeepAliveDeadlineExceeded
			}
		},
	}, nil
}

// MustMakeKeepAlive must creates a keepalive policy.
func MustMakeKeepAlive(connTimeout time.Duration) KeepAlive {
	k, err := MakeKeepAlive(connTimeout)
	if err != nil {
		panic(err)
	}
	return k
}

func validateKeepAlive(cfg KeepAlive) error {
	if !cfg.Enable {
		return nil
	}
	if cfg.Interval <= 0 {
		return fmt.Errorf("invalid Interval")
	}
	if cfg.WaitForPong <= 0 {
		return fmt.Errorf("invalid WaitForPong")
	}
	if cfg.NewRetryPolicy == nil {
		return fmt.Errorf("invalid NewRetryPolicy")
	}
	return nil
}

func newKeepAliveSession(s networkSession, srv *Server) *keepAliveSession {
	newS := keepAliveSession{
		networkSession: s,
	}

	ping := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), srv.KeepAlive.WaitForPong)
		defer cancel()
		return newS.PingWithContext(ctx)
	}

	go func(s *keepAliveSession) {
		ticker := time.NewTicker(srv.KeepAlive.Interval)
		defer ticker.Stop()
	PING_LOOP:
		for {
			select {
			case <-ticker.C:
				if err := ping(); err != nil {
					if err != context.DeadlineExceeded {
						s.closeWithError(err)
						return
					}
					retryPolicy := srv.KeepAlive.NewRetryPolicy()
					for {
						when, err := retryPolicy()
						if err != nil {
							s.closeWithError(err)
							return
						}
						select {
						case <-s.Done():
							return
						case <-time.After(time.Until(when)):
							err := ping()
							if err == context.DeadlineExceeded {
								continue
							}
							if err == nil {
								goto PING_LOOP
							}
							s.closeWithError(err)
							return
						}
					}
				}
			case <-s.Done():
				return
			}
		}
	}(&newS)
	return &newS
}
