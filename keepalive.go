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
// while attempting to make 3 pings during that period.
func MakeKeepAlive(connTimeout time.Duration) (KeepAlive, error) {
	// 3 times ping-pong
	duration := connTimeout / 6
	if duration < minDuration {
		return KeepAlive{}, fmt.Errorf("connTimeout %v it too small: must be greater than %v", connTimeout, minDuration*6)
	}
	return KeepAlive{
		Enable:      true,
		Interval:    duration,
		WaitForPong: duration,
		NewRetryPolicy: func() RetryFunc {
			// The first failure is detected after 2*duration:
			// 1 since the previous ping, plus 1 for the next ping-pong to timeout
			start := time.Now()
			attempt := time.Duration(1)
			return func() (time.Time, error) {
				attempt++
				// Try to send ping and wait for pong 2 more times
				if time.Since(start) <= 2 * 2 * duration {
					return start.Add(attempt * duration), nil
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
