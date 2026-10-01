package client

import (
	"errors"
	"math"
	"math/bits"
	"time"

	"github.com/plgd-dev/go-coap/v3/net/qblock"
)

type qblockPacingConfig struct {
	ProbingRate    uint64
	NonProbingWait time.Duration
	MaxIntentBytes uint64
}

func normalizeQBlockPacingConfig(cfg *qblockPacingConfig, mc qblock.ManagerConfig) (qblockPacingConfig, error) {
	if cfg == nil {
		cfg = &qblockPacingConfig{ProbingRate: 1, MaxIntentBytes: mc.MaxRetainedBytes}
	}
	if cfg.ProbingRate == 0 || cfg.MaxIntentBytes == 0 || cfg.NonProbingWait < 0 {
		return qblockPacingConfig{}, errors.New("invalid q-block pacing configuration")
	}
	result := *cfg
	if _, err := qblockProbingWait(result, mc.Transfer, 1); err != nil {
		return qblockPacingConfig{}, err
	}
	return result, nil
}

// qblockProbingWait computes the local RFC 9177 wait cap for one sender's
// already-sampled jitter. An explicit wait bypasses the derived formula.
func qblockProbingWait(cfg qblockPacingConfig, tc qblock.TransferConfig, jitter float64) (time.Duration, error) {
	if err := tc.Validate(); err != nil {
		return 0, err
	}
	if math.IsNaN(jitter) || math.IsInf(jitter, 0) || jitter < 0 || jitter > 1 {
		return 0, errors.New("invalid q-block pacing jitter")
	}
	if cfg.NonProbingWait < 0 {
		return 0, errors.New("negative q-block probing wait")
	}
	if cfg.NonProbingWait > 0 {
		return cfg.NonProbingWait, nil
	}
	sendDelay := float64(tc.NonTimeout) * (1 + jitter/2)
	if sendDelay >= float64(math.MaxInt64) {
		return 0, errors.New("q-block send delay overflows")
	}
	retries := (uint64(1) << tc.NonMaxRetransmit) - 1
	derived := float64(tc.NonTimeout)*float64(retries)*1.5 + float64(200*time.Second) + float64(time.Duration(sendDelay))
	if derived >= float64(math.MaxInt64) {
		return 0, errors.New("q-block probing wait overflows")
	}
	return time.Duration(derived), nil
}

// qblockProbeDelay rounds bytes/rate upward to nanoseconds. Runtime overflow
// saturates so an unanswered peer cannot become immediately eligible.
func qblockProbeDelay(bytes, rate uint64, cap time.Duration) time.Duration {
	if bytes == 0 {
		return 0
	}
	delay := time.Duration(math.MaxInt64)
	if rate != 0 {
		hi, lo := bits.Mul64(bytes, uint64(time.Second))
		if hi < rate {
			whole, rem := bits.Div64(hi, lo, rate)
			if rem > 0 && whole < math.MaxUint64 {
				whole++
			}
			if whole <= math.MaxInt64 {
				delay = time.Duration(whole)
			}
		}
	}
	if cap > 0 && delay > cap {
		return cap
	}
	return delay
}

type qblockProbeKey uint64
type qblockProbeKind uint8

const (
	qblockProbeBody qblockProbeKind = iota + 1
	qblockProbeControl
)

type qblockProbeState uint8

const (
	qblockProbeOpen qblockProbeState = iota
	qblockProbeActive
	qblockProbeWaiting
)

// qblockProbeGate is owned by the connection coordinator and called under
// its mutex. It retains only one current unanswered probing unit.
type qblockProbeGate struct {
	rate     uint64
	state    qblockProbeState
	key      qblockProbeKey
	kind     qblockProbeKind
	wait     time.Duration
	bytes    uint64
	deadline time.Time
}

func newQBlockProbeGate(rate uint64) *qblockProbeGate {
	return &qblockProbeGate{rate: rate}
}

func (g *qblockProbeGate) reset() {
	g.state = qblockProbeOpen
	g.key = 0
	g.kind = 0
	g.wait = 0
	g.bytes = 0
	g.deadline = time.Time{}
}

func (g *qblockProbeGate) ready(now time.Time) bool {
	if g.state == qblockProbeWaiting && !now.Before(g.deadline) {
		g.reset()
	}
	return g.state == qblockProbeOpen
}

func (g *qblockProbeGate) admit(key qblockProbeKey, kind qblockProbeKind, wait time.Duration, now time.Time) bool {
	if key == 0 || (kind != qblockProbeBody && kind != qblockProbeControl) || !g.ready(now) {
		return false
	}
	g.state, g.key, g.kind, g.wait = qblockProbeActive, key, kind, wait
	return true
}

func (g *qblockProbeGate) charge(key qblockProbeKey, bytes uint64) {
	if g.state != qblockProbeActive || g.key != key {
		return
	}
	if bytes > math.MaxUint64-g.bytes {
		g.bytes = math.MaxUint64
		return
	}
	g.bytes += bytes
}

func (g *qblockProbeGate) settle(key qblockProbeKey, now time.Time) {
	if g.state != qblockProbeActive || g.key != key {
		return
	}
	if g.bytes == 0 {
		g.reset()
		return
	}
	cap := time.Duration(0)
	if g.kind == qblockProbeBody {
		cap = g.wait
	}
	g.deadline = now.Add(qblockProbeDelay(g.bytes, g.rate, cap))
	g.state = qblockProbeWaiting
}

func (g *qblockProbeGate) feedback(key qblockProbeKey) bool {
	if key == 0 || g.key != key || g.bytes == 0 || g.state == qblockProbeOpen {
		return false
	}
	g.reset()
	return true
}

func (g *qblockProbeGate) nextDeadline() (time.Time, bool) {
	if g.state != qblockProbeWaiting {
		return time.Time{}, false
	}
	return g.deadline, true
}

// qblockCongestionGate allows a standalone connection or shared endpoint owner.
type qblockCongestionGate interface {
	ready(time.Time) bool
	admit(qblockProbeKey, qblockProbeKind, time.Duration, time.Time) bool
	settle(qblockProbeKey, time.Time)
	feedback(qblockProbeKey) bool
	nextDeadline() (time.Time, bool)
	owns(qblockProbeKey) bool
	ownsActive(qblockProbeKey) bool
	kindOf(qblockProbeKey) qblockProbeKind
	beginAttempt(qblockProbeKey, uint64) bool
	endAttempt(qblockProbeKey, time.Time)
}

func (g *qblockProbeGate) owns(key qblockProbeKey) bool {
	return g.key == key && g.state != qblockProbeOpen
}
func (g *qblockProbeGate) ownsActive(key qblockProbeKey) bool {
	return g.key == key && g.state == qblockProbeActive
}
func (g *qblockProbeGate) kindOf(key qblockProbeKey) qblockProbeKind {
	if g.owns(key) {
		return g.kind
	}
	return 0
}
func (g *qblockProbeGate) beginAttempt(key qblockProbeKey, bytes uint64) bool {
	if !g.ownsActive(key) {
		return false
	}
	g.charge(key, bytes)
	return true
}
func (g *qblockProbeGate) endAttempt(qblockProbeKey, time.Time) {}
func (c *qblockClient) gate() qblockCongestionGate {
	if c.endpoint != nil {
		return c.endpoint
	}
	return c.probeGate
}

func (c *qblockClient) withdrawPending(key qblockProbeKey) {
	if c.endpoint != nil {
		c.endpoint.withdraw(key)
	}
}
