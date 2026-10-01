package qblocktransport

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/internal/test/qblocklink"
	"github.com/stretchr/testify/require"
)

// This catches swallowing drops, aliasing queued duplicates, or returning
// success for a short underlying datagram write.
func TestPlaintextConnFaults(t *testing.T) {
	c := NewController()
	require.NoError(t, c.Arm([]qblocklink.Rule{
		{Direction: qblocklink.ClientToServer, Kind: qblocklink.Q1, Occurrence: 1, Action: qblocklink.Drop},
		{Direction: qblocklink.ClientToServer, Kind: qblocklink.Q1, Occurrence: 2, Action: qblocklink.Duplicate},
		{Direction: qblocklink.ServerToClient, Kind: qblocklink.Q2, Occurrence: 1, Action: qblocklink.Drop},
		{Direction: qblocklink.ServerToClient, Kind: qblocklink.Q2, Occurrence: 2, Action: qblocklink.Duplicate},
	}))
	base := &datagramConn{reads: [][]byte{{0x50, 2, 0, 1, 0xd0, 6}, {0x50, 2, 0, 2, 0xd0, 6}}}
	w := NewPlaintextConn(base, c)
	buf := make([]byte, 100)
	n, err := w.Read(buf)
	require.NoError(t, err)
	require.Equal(t, []byte{0x50, 2, 0, 2, 0xd0, 6}, buf[:n])
	buf[0] = 0
	n, err = w.Read(buf)
	require.NoError(t, err)
	require.Equal(t, []byte{0x50, 2, 0, 2, 0xd0, 6}, buf[:n])
	q2 := []byte{0x50, 69, 0, 3, 0xd0, 18}
	n, err = w.Write(q2)
	require.NoError(t, err)
	require.Equal(t, len(q2), n)
	require.Empty(t, base.writes)
	n, err = w.Write(q2)
	require.NoError(t, err)
	require.Equal(t, len(q2), n)
	require.Equal(t, [][]byte{q2, q2}, base.writes)
	require.Len(t, c.Snapshot().Faults, 4)
	require.Equal(t, []Forward{{"faults", qblocklink.ClientToServer, 2, 2}, {"faults", qblocklink.ServerToClient, 4, 2}}, c.Snapshot().Forwarded)
	AssertForwarded(t, c.Snapshot())
	base.short = true
	_, err = w.Write(q2)
	require.ErrorIs(t, err, io.ErrShortWrite)
	require.Equal(t, []Forward{{"faults", qblocklink.ClientToServer, 2, 2}, {"faults", qblocklink.ServerToClient, 4, 2}}, c.Snapshot().Forwarded, "failed short write must not count as delivery")
}

func TestPlaintextConnPreservesHandshake(t *testing.T) {
	base := &datagramConn{}
	w := NewPlaintextConn(base, NewController())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, w.HandshakeContext(ctx), context.Canceled)
	require.Equal(t, 1, base.handshakes)
}

// Concurrent reads, writes and detached snapshots must not race Link state.
func TestPlaintextConnConcurrentTrace(t *testing.T) {
	base := &datagramConn{}
	for i := 0; i < 64; i++ {
		base.reads = append(base.reads, []byte{0x50, 2, 0, byte(i), 0xd0, 6})
	}
	c := NewController()
	w := NewPlaintextConn(base, c)
	var wg sync.WaitGroup
	for j := 0; j < 3; j++ {
		wg.Add(1)
		go func(j int) {
			defer wg.Done()
			for i := 0; i < 64; i++ {
				switch j {
				case 0:
					_, err := w.Read(make([]byte, 100))
					require.NoError(t, err)
				case 1:
					_, err := w.Write([]byte{0x50, 69, 0, byte(i), 0xd0, 18})
					require.NoError(t, err)
				default:
					snapshot := c.Snapshot()
					if len(snapshot.Probe) > 0 {
						snapshot.Probe[0].Wire[0] = 0
					}
				}
			}
		}(j)
	}
	wg.Wait()
	require.Len(t, c.Snapshot().Probe, 128)
	AssertForwarded(t, c.Snapshot())
	for _, event := range c.Snapshot().Probe {
		require.Equal(t, byte(0x50), event.Wire[0])
	}
}

type datagramConn struct {
	reads, writes [][]byte
	short         bool
	handshakes    int
}

func (c *datagramConn) Read(b []byte) (int, error) {
	if len(c.reads) == 0 {
		return 0, io.EOF
	}
	w := c.reads[0]
	c.reads = c.reads[1:]
	return copy(b, w), nil
}
func (c *datagramConn) Write(b []byte) (int, error) {
	c.writes = append(c.writes, bytes.Clone(b))
	if c.short {
		return len(b) - 1, nil
	}
	return len(b), nil
}
func (c *datagramConn) HandshakeContext(ctx context.Context) error { c.handshakes++; return ctx.Err() }
func (*datagramConn) Close() error                                 { return nil }
func (*datagramConn) LocalAddr() net.Addr                          { return &net.UDPAddr{} }
func (*datagramConn) RemoteAddr() net.Addr                         { return &net.UDPAddr{} }
func (*datagramConn) SetDeadline(time.Time) error                  { return nil }
func (*datagramConn) SetReadDeadline(time.Time) error              { return nil }
func (*datagramConn) SetWriteDeadline(time.Time) error             { return nil }
