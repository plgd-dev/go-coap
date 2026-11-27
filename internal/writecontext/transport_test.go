package writecontext_test

import (
	"context"
	"net"
	"testing"
	"time"

	dtlsServer "github.com/plgd-dev/go-coap/v3/dtls/server"
	"github.com/plgd-dev/go-coap/v3/internal/writecontext"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	tcpClient "github.com/plgd-dev/go-coap/v3/tcp/client"
	"github.com/stretchr/testify/require"
)

type startedConn struct {
	net.Conn
	started chan struct{}
}

func (c *startedConn) Write(data []byte) (int, error) {
	close(c.started)
	return c.Conn.Write(data)
}

func TestStreamSessionWriteCauses(t *testing.T) {
	const (
		dtlsTransport   = "dtls"
		requestCancel   = "request cancel"
		requestDeadline = "request deadline"
		inheritedClose  = "session close inherited by request"
		serverTimeout   = "server timeout"
		sessionClose    = "session close"
	)
	for _, transport := range []string{"tcp", dtlsTransport} {
		for _, cause := range []string{requestDeadline, serverTimeout, requestCancel, sessionClose, inheritedClose} {
			t.Run(transport+"/"+cause, func(t *testing.T) {
				raw, peer := net.Pipe()
				defer peer.Close()
				started := make(chan struct{})
				conn := coapNet.NewConn(&startedConn{Conn: raw, started: started})
				defer conn.Close()
				var write func(*pool.Message) error
				var closeSession func() error
				var sessionCtx context.Context
				switch transport {
				case "tcp":
					session := tcpClient.NewSession(context.Background(), conn, 1024, nil, true, false, nil, nil, 1024, pool.New(0, 0))
					write, closeSession = session.WriteMessage, session.Close
					sessionCtx = session.Context()
				case dtlsTransport:
					session := dtlsServer.NewSession(context.Background(), conn, 1024, 1024, false)
					write, closeSession = session.WriteMessage, session.Close
					sessionCtx = session.Context()
				}
				defer func() { require.NoError(t, closeSession()) }()
				parent := context.Background()
				if cause == inheritedClose {
					parent = sessionCtx
				}
				reqCtx, cancelReq := context.WithCancel(parent)
				defer cancelReq()
				if cause == requestDeadline {
					var cancel context.CancelFunc
					reqCtx, cancel = context.WithTimeout(reqCtx, 200*time.Millisecond)
					defer cancel()
				}
				if cause == serverTimeout {
					var cancel func()
					reqCtx, cancel = writecontext.New(reqCtx, context.Background(), 200*time.Millisecond)
					defer cancel()
				}
				req := pool.NewMessage(reqCtx)
				req.SetCode(codes.GET)
				if transport == dtlsTransport {
					req.SetType(message.Confirmable)
					req.SetMessageID(42)
				}
				done := make(chan error, 1)
				go func() { done <- write(req) }()
				select {
				case <-started:
				case <-time.After(2 * time.Second):
					t.Fatal("write did not start")
				}
				switch cause {
				case requestCancel:
					cancelReq()
				case sessionClose, inheritedClose:
					require.NoError(t, closeSession())
				}
				select {
				case err := <-done:
					switch cause {
					case requestDeadline:
						require.ErrorIs(t, err, context.DeadlineExceeded)
						require.NotErrorIs(t, err, coapNet.ErrWriteTimeout)
					case serverTimeout:
						require.ErrorIs(t, err, context.DeadlineExceeded)
						require.ErrorIs(t, err, coapNet.ErrWriteTimeout)
					case requestCancel:
						require.ErrorIs(t, err, context.Canceled)
					case sessionClose, inheritedClose:
						require.ErrorIs(t, err, net.ErrClosed)
						require.ErrorIs(t, err, context.Canceled)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("write did not stop")
				}
			})
		}
	}
}
