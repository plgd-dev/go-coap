package qblock

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// This trace wires normalized actions, not UDP packets: token ownership,
// wire metadata and peer-wide congestion budgets belong to the manager.
func TestTransferReorderLossAndLostRecovery(t *testing.T) {
	for _, kind := range []Kind{Q1, Q2} {
		t.Run(map[Kind]string{Q1: "upload", Q2: "download"}[kind], func(t *testing.T) {
			cfg := DefaultTransferConfig()
			start := time.Unix(100, 0)
			meta := Metadata{Size: 21 * 16, Identity: []byte("body")}
			payload := make([]byte, meta.Size)
			for i := range payload {
				payload[i] = byte(i % 251)
			}
			sender, err := NewSender(kind, cfg, meta, payload, start, 0)
			require.NoError(t, err)
			receiver, err := NewReceiver(kind, cfg, meta, start)
			require.NoError(t, err)
			actions, err := sender.Start(start)
			require.NoError(t, err)
			delivered := 0
			senderCompleted := 0
			droppedBlock := false
			droppedRecovery := false
			var queue []Action
			enqueue := func(actions []Action) {
				// Reverse data packets in each returned burst; keep outcome notifications.
				for i := len(actions) - 1; i >= 0; i-- {
					if actions[i].Kind == SendBlock {
						queue = append(queue, actions[i])
					}
				}
				for _, a := range actions {
					if a.Kind == Complete {
						require.NoError(t, a.Err)
						senderCompleted++
					}
				}
			}
			enqueue(actions)
			var lastBlock Action
			for second := 0; second < 120 && delivered == 0; second++ {
				now := start.Add(time.Duration(second) * time.Second)
				enqueue(sender.Tick(now))
				receiverActions := receiver.Tick(now)
				for len(queue) > 0 || len(receiverActions) > 0 {
					if len(receiverActions) == 0 {
						packet := queue[0]
						queue = queue[1:]
						if packet.Block.Number == 2 && !droppedBlock {
							droppedBlock = true
							continue
						}
						lastBlock = packet
						receiverActions, err = receiver.Receive(meta, packet.Block, packet.Payload, now)
						require.NoError(t, err)
						// Mutating the input after intake must not affect eventual assembly.
						for i := range packet.Payload {
							packet.Payload[i] = 255
						}
					}
					current := receiverActions
					receiverActions = nil
					for _, a := range current {
						switch a.Kind {
						case SendContinue:
						// Lose Continue messages too: sender must still advance on its own timer.
						case RequestMissing:
							if !droppedRecovery {
								droppedRecovery = true
								continue
							}
							repair, e := sender.Repair(a.Numbers, now)
							require.NoError(t, e)
							enqueue(repair)
						case Deliver:
							delivered++
							require.True(t, bytes.Equal(payload, a.Payload))
							if kind == Q1 {
								enqueue(sender.Finish(nil))
							}
						case Complete:
							require.NoError(t, a.Err)
						}
					}
				}
			}
			require.True(t, droppedBlock)
			require.True(t, droppedRecovery)
			require.Equal(t, 1, delivered)
			require.Equal(t, 1, senderCompleted)
			if kind == Q1 {
				a, e := receiver.Receive(meta, lastBlock.Block, make([]byte, len(lastBlock.Payload)), start.Add(121*time.Second))
				require.NoError(t, e)
				require.Len(t, a, 1)
				require.Equal(t, Duplicate, a[0].Kind)
			} else {
				actions = sender.Tick(start.Add(cfg.Lifetime))
				require.Len(t, actions, 1)
				require.Equal(t, Release, actions[0].Kind)
			}
		})
	}
}
