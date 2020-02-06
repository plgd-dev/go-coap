package coap

import (
	"context"
	"fmt"
	"time"
)

type keepAliveSession struct {
	networkSession
}

// KeepAlive config
type KeepAlive struct {
	// Enable watch connection
	Enable bool
	// Interval between two success pings
	Interval time.Duration
	// WaitForPong how long it will waits for pong response.
	WaitForPong time.Duration
	// NewRetryPolicy creates retry policy for the connection when ping fails.
	NewRetryPolicy func() BackOff
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

	retryPolicy := srv.KeepAlive.NewRetryPolicy()
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
					for {
						nextCall := retryPolicy.NextBackOff()
						if nextCall == Stop {
							s.closeWithError(ErrKeepAliveDeadlineExceeded)
							return
						}
						select {
						case <-s.Done():
							return
						case <-time.After(nextCall):
							err := ping()
							if err == context.DeadlineExceeded {
								continue
							}
							if err == nil {
								retryPolicy.Reset()
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
