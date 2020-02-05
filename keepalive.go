package coap

import (
	"time"
	"context"
)

type keepAliveSession struct {
	networkSession
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
							s.closeWithError(err)
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