package qblock

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

type asyncTestTransport struct {
	*qTestTransport
	done func(error)
}

func (c *asyncTestTransport) SendQBlockAsync(msg *pool.Message, done func(error)) error {
	if err := c.SendQBlock(msg); err != nil {
		return err
	}
	c.done = done
	return nil
}

func TestQBlockConfirmableSchedulerWaitsOutsideWorker(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "ack", true: "failure"}[failed], func(t *testing.T) {
			cc := &asyncTestTransport{qTestTransport: &qTestTransport{
				testClient: newTestClient(), jobs: make(map[string]qBlockTestJob),
			}}
			var reported error
			engine := New(cc, func(err error) { reported = err })
			defer engine.CloseQBlockTransfers()
			req := qTestRequest(t, "body", struct {
				num  int64
				more bool
			}{0, true})
			req.SetType(message.Confirmable)
			writer := responsewriter.New(cc.AcquireMessage(req.Context()), cc)
			defer cc.ReleaseMessage(writer.Message())
			require.NoError(t, engine.handleQBlock2(writer, req, SZX16, nil,
				func(w *responsewriter.ResponseWriter[*asyncTestTransport], _ *pool.Message) {
					require.NoError(t, w.SetResponse(codes.Content, message.AppOctets, bytes.NewReader(make([]byte, 64))))
				}))
			require.Len(t, cc.jobs, 1)
			for key, job := range cc.jobs {
				delete(cc.jobs, key)
				require.NoError(t, job.run())
			}
			require.Len(t, cc.sent, 1, "only one CON may await its ACK for this body")
			require.NotNil(t, cc.done)
			require.Empty(t, cc.jobs, "the shared worker is free while the ACK is pending")
			if failed {
				sendErr := errors.New("ACK timeout")
				cc.done(sendErr)
				require.ErrorIs(t, reported, sendErr)
				require.Empty(t, cc.jobs)
			} else {
				cc.done(nil)
				require.NoError(t, reported)
				require.Len(t, cc.jobs, 1)
				for _, job := range cc.jobs {
					require.WithinDuration(t, time.Now(), job.deadline, time.Second)
				}
			}
		})
	}
}
