# Private Q-Block connection deadline scheduler Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a privately enabled `udp/client.Conn` advance client and server Q-Block transfers at their earliest deadlines without an external `CheckExpirations` poll.

**Architecture:** One private coordinator already owned by `qblockClient` continues to own the shared `qblock.Manager` and server records. A private clock supplies both time and its resettable timer; a timer-owner goroutine schedules at most one due-work turn, while the existing action gate serializes mutation and packet output. Server record retention remains an independent deadline source after manager transfer ownership ends.

**Tech Stack:** Go 1.25, `udp/client`, `net/qblock`, Go contexts and synchronization primitives, `testify/require`.

**Spec:** [2026-09-22-rfc9177-connection-deadline-scheduler-design.md](../specs/2026-09-22-rfc9177-connection-deadline-scheduler-design.md)

## Global Constraints

- Keep all construction and symbols private to `udp/client`; add no public transport option or `net/qblock` API.
- Reuse `Manager.NextDeadline` and `Manager.Tick`; do not add a second transfer heap or per-transfer timers.
- A connection has one manager across Q1 and Q2 roles, one timer, two scheduler workers, capacity-one notification/work/completion channels, and at most one fixed bounded callback dispatcher.
- Normal progress lock order is `actionMu` then `mu`; cancellation and close never wait for `actionMu` while holding `mu`.
- Never hold `mu` during packet I/O, user callbacks, or pooled-message release; callbacks never run on scheduler workers or under `actionMu`.
- Automatic scheduling uses one coherent clock/timer source. Tests injecting only a clock function use explicit manual mode and create no real timer.
- Retention is at least `ManagerConfig.Transfer.Lifetime`; all deadline comparisons use `!now.Before(deadline)`.
- Existing uncommitted changes to `docs/superpowers/plans/2026-09-21-rfc9177-private-bidirectional-udp-adapter.md` are outside this plan and must not be staged.

## File Map

| File | Responsibility |
| --- | --- |
| `udp/client/qblock_client.go` | Coordinator state, ordered output execution, cancellation, legacy `Tick`, and close integration. |
| `udp/client/qblock_server.go` | Server-record deadline projection, terminal record settlement, handler lifecycle, and output validation. |
| `udp/client/qblock_server_q1.go` / `qblock_server_q2.go` | Notify the coordinator after accepted server-side mutations. |
| `udp/client/qblock_scheduler.go` | Private clock/timer contracts, scheduler mode validation, timer owner, due worker, and deadline recomputation. |
| `udp/client/qblock_scheduler_test.go` | Fake clock/timer and deterministic scheduler lifecycle, deadline, shutdown, and race regressions. |
| `udp/client/qblock_client_test.go` | Client ordering, cancellation, callback-capacity, and manual-mode regression coverage. |
| `udp/client/qblock_server_test.go` | Server retention/expiry and output-ownership regressions. |
| `udp/client/qblock_server_lifecycle_test.go` | Paired POST/PUT loss-and-repair traces driven only by the private scheduler. |
| `udp/client/conn.go` | Start the scheduler only after the connection’s Q-Block roles, receive hook, and close hook are installed; route legacy expiration checks by mode. |

## Review Focus

- A handler that returns after its Q1 transfer expired must clear `handlerRunning`, settle duplicate suppression once, and eventually release its record; Task 1 pins this.
- A timer/control event arriving while the preceding packet write is blocked must not advance a sender ahead of that burst; Task 3 pins this.
- A stale timer delivery, an immediate notification before the owner parks, and a periodic tick at the same boundary must produce one advancement rather than a lost wake or duplicate burst; Tasks 5 and 6 pin these.
- Close or cancellation during a context-aware blocked write must stop new output without deadlocking on the progress gate; Task 3 and Task 6 pin this.
- Blocked completion callbacks must consume a bounded logical-exchange slot, reject cleanly at capacity, and never run on a scheduler worker; Task 4 pins this.

---

### Task 1: Settle server records and expose the combined deadline

**Files:**
- Modify: `udp/client/qblock_server.go: qblockServerRecord, deactivateLocked, expireRecordsLocked, finishHandler, releaseLocked`
- Modify: `udp/client/qblock_client.go: Tick`
- Test: `udp/client/qblock_server_test.go`

**Interfaces:**
- Consumes: `(*qblock.Manager).NextDeadline() (time.Time, bool)` and `qblockClient.now() time.Time`.
- Produces: `func (c *qblockClient) nextDeadlineLocked() (time.Time, bool)` and `func (s *qblockServer) nextRecordDeadlineLocked() (time.Time, bool)`; both require `c.mu`.
- Produces: `func (s *qblockServer) settleHandlerLocked(record *qblockServerRecord, now time.Time)` which clears `handlerRunning` and establishes retention exactly once.

- [ ] **Step 1: Write failing retention and deadline tests**

```go
func TestQBlockServerExpiredHandlerSettlesAndExpires(t *testing.T) {
	returned := make(chan struct{})
	h := newServerHarness(t, shortLifetimeManager(), qblockServerConfig{Retention: time.Second},
		func(_ *responsewriter.ResponseWriter[*Conn], _ *pool.Message) { <-returned })
	h.ingest(h.q1(t, 1, 0, false, 4, "body"))
	h.advance(time.Second) // Q1 expires while the handler is blocked.
	close(returned)
	require.Eventually(t, func() bool { return !recordRunning(h.cc) }, time.Second, time.Millisecond)
	h.advance(time.Second)
	require.Zero(t, h.snapshot().records)
}

func TestQBlockNextDeadlineIncludesTerminalServerRetention(t *testing.T) {
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{Retention: time.Second}, nil)
	record := installTerminalRecord(t, h, h.now.Add(time.Second))
	h.cc.qblockClient.mu.Lock()
	deadline, ok := h.cc.qblockClient.nextDeadlineLocked()
	h.cc.qblockClient.mu.Unlock()
	require.True(t, ok)
	require.Equal(t, record.expires, deadline)
}
```

- [ ] **Step 2: Run the focused tests and verify they fail**

Run: `rtk go test ./udp/client -run 'TestQBlockServer(ExpiredHandlerSettlesAndExpires|NextDeadlineIncludesTerminalServerRetention)' -count=1`

Expected: FAIL because no combined deadline helper exists and a late handler leaves its record marked running.

- [ ] **Step 3: Implement terminal settlement and deadline projection**

```go
func (s *qblockServer) nextRecordDeadlineLocked() (time.Time, bool) {
	var next time.Time
	for _, record := range s.records {
		if !record.terminal || record.handlerRunning || record.expires.IsZero() {
			continue
		}
		if next.IsZero() || record.expires.Before(next) { next = record.expires }
	}
	return next, !next.IsZero()
}

func (c *qblockClient) nextDeadlineLocked() (time.Time, bool) {
	deadline, ok := c.manager.NextDeadline()
	if c.server != nil {
		if recordDeadline, recordOK := c.server.nextRecordDeadlineLocked(); recordOK && (!ok || recordDeadline.Before(deadline)) {
			deadline, ok = recordDeadline, true
		}
	}
	return deadline, ok
}
```

Make `finishHandler` find the matching generation, clear `handlerRunning` even when the wire phase is terminal, and call `settleHandlerLocked` with `c.now()`. `settleHandlerLocked` must only set `record.expires` when it is zero. Make `expireRecordsLocked` remove a terminal, non-running record on `!now.Before(record.expires)`; when Q2 and retention share a deadline, cancel/release the active manager transfer before removing the record.

- [ ] **Step 4: Run focused and package regression tests**

Run: `rtk go test ./udp/client -run 'TestQBlockServer' -count=1`

Expected: PASS; duplicate suppression survives failed Q2 output until its original retention deadline and a late handler cannot pin a record.

- [ ] **Step 5: Commit the lifecycle prerequisite**

```bash
git add udp/client/qblock_server.go udp/client/qblock_client.go udp/client/qblock_server_test.go
git commit -m "fix(qblock): settle expired server handlers"
```

### Task 2: Add a coherent private clock and scheduling modes

**Files:**
- Create: `udp/client/qblock_scheduler.go`
- Create: `udp/client/qblock_scheduler_test.go`
- Modify: `udp/client/qblock_client.go: qblockClientConfig, qblockClient, newQBlockClient`
- Modify: `udp/client/qblock_client_test.go` and `udp/client/qblock_server_test.go`

**Interfaces:**
- Consumes: Task 1’s `nextDeadlineLocked`.
- Produces:

```go
type qblockClock interface {
	Now() time.Time
	NewTimer() qblockTimer
}
type qblockTimer interface {
	C() <-chan time.Time
	Reset(time.Duration)
	Stop() bool
}
type qblockScheduleMode uint8
const (
	qblockScheduleManual qblockScheduleMode = iota
	qblockScheduleAutomatic
)
```

- Produces `qblockClientConfig{Clock qblockClock, ScheduleMode qblockScheduleMode}`. Manual mode accepts an injected `Now func() time.Time` only for backwards-compatible deterministic tests; automatic mode requires `Clock` and rejects `Now`.

- [ ] **Step 1: Write failing configuration and fake-clock tests**

```go
func TestQBlockManualModeDoesNotCreateTimer(t *testing.T) {
	clock := newFakeQBlockClock(time.Unix(100, 0))
	cc := newPrivateQBlockConn(t, qblockClientConfig{Clock: clock, ScheduleMode: qblockScheduleManual})
	require.Zero(t, clock.newTimers)
	cc.qblockClient.Tick(clock.Now())
	require.Zero(t, clock.newTimers)
}

func TestQBlockAutomaticModeRejectsMixedClockConfiguration(t *testing.T) {
	client := newQBlockClient(newTestConn(t), qblockClientConfig{
		Clock: newFakeQBlockClock(time.Unix(100, 0)), Now: time.Now, ScheduleMode: qblockScheduleAutomatic,
	})
	require.Error(t, client.initErr)
}
```

- [ ] **Step 2: Run the focused tests and verify they fail**

Run: `rtk go test ./udp/client -run 'TestQBlock(ManualModeDoesNotCreateTimer|AutomaticModeRejectsMixedClockConfiguration)' -count=1`

Expected: FAIL because the configuration has no coherent timer contract or scheduling mode.

- [ ] **Step 3: Implement the clock contract and deterministic fake**

```go
type realQBlockClock struct{}
func (realQBlockClock) Now() time.Time { return time.Now() }
func (realQBlockClock) NewTimer() qblockTimer { return &realQBlockTimer{timer: time.NewTimer(time.Hour)} }

func (c *qblockClient) now() time.Time { return c.clock.Now() }
```

Give `realQBlockTimer` a stopped initial timer. In `qblock_scheduler_test.go`, implement `fakeQBlockClock.Advance(time.Duration)` and a fake timer whose `Reset`, `Stop`, and explicit stale delivery are controlled by the test. Validate mode/configuration in `newQBlockClient`: automatic uses exactly a `Clock`; manual uses `Clock` when supplied or a `Now`-only adapter that has no timer; mixed sources set `initErr`. Migrate every existing test that supplies `Now` to `qblockScheduleManual`.

- [ ] **Step 4: Run clock and existing private-adapter tests**

Run: `rtk go test ./udp/client -run 'TestQBlock(ManualMode|AutomaticMode|Client|Server)' -count=1`

Expected: PASS; no existing test creates wall-clock scheduler state accidentally.

- [ ] **Step 5: Commit the private time contract**

```bash
git add udp/client/qblock_scheduler.go udp/client/qblock_scheduler_test.go udp/client/qblock_client.go udp/client/qblock_client_test.go udp/client/qblock_server_test.go
git commit -m "feat(qblock): add private scheduler clock modes"
```

### Task 3: Make progress atomic with output and make writes cancelable

**Files:**
- Modify: `udp/client/qblock_client.go: prepare, prepareQ1, handle, Tick, abandon, drive, executeOrdered, writeQ1Block, writeQ2Control`
- Modify: `udp/client/qblock_server.go: executeServerOutput, writeServerQ1Control, writeServerQ2Block, finishHandler`
- Test: `udp/client/qblock_client_test.go`
- Test: `udp/client/qblock_server_test.go`

**Interfaces:**
- Consumes: the existing `actionMu`, `qblock.Output`, and per-exchange request context.
- Produces `func (c *qblockClient) progress(outputs []qblock.Output) []func()` for callers that already hold `actionMu`, and `func (c *qblockClient) runProgress(mutate func() []qblock.Output)` for the `actionMu` then `mu` mutation-and-execute turn.
- Produces `func (c *qblockClient) writeContextLocked(transfer *qblockTransfer) context.Context`; server writes use a coordinator-owned context canceled by close/reset as well.

- [ ] **Step 1: Write failing ordering and cancellation tests**

```go
func TestQBlockBlockedBurstPrecedesControlAndDueTick(t *testing.T) {
	cc, session := newBlockingQBlockConn(t)
	transfer, _ := startQ1TransferForTest(t, cc, bytes.Repeat([]byte("x"), 64))
	<-session.firstWriteStarted
	cc.qblockClient.handle(q1ContinueFor(t, cc, transfer))
	cc.qblockClient.Tick(time.Now().Add(time.Hour))
	require.Len(t, session.writesSnapshot(), 0, "later progress must wait for first burst")
	close(session.releaseFirstWrite)
	requireQ1Burst(t, session.writesFor(transfer), codes.POST, []uint32{0, 1}, nil, nil)
}

func TestQBlockCloseCancelsContextAwareBlockedServerWrite(t *testing.T) {
	h := newContextBlockingServerHarness(t)
	h.ingest(h.q1(t, 1, 0, false, 4, "body"))
	<-h.session.contextWriteStarted
	h.session.closeForTest()
	require.Eventually(t, func() bool { return h.session.contextWriteCanceled() }, time.Second, time.Millisecond)
}
```

- [ ] **Step 2: Run the focused tests and verify they fail**

Run: `rtk go test ./udp/client -run 'TestQBlock(BlockedBurstPrecedesControlAndDueTick|CloseCancelsContextAwareBlockedServerWrite)' -count=1`

Expected: FAIL because mutation completes before a previously emitted burst writes, and server writes use the connection context directly.

- [ ] **Step 3: Refactor all ordinary progress paths through one gate turn**

```go
func (c *qblockClient) runProgress(mutate func() []qblock.Output) {
	c.actionMu.Lock()
	c.mu.Lock()
	outputs := mutate()
	c.mu.Unlock()
	callbacks := c.progress(outputs)
	c.actionMu.Unlock()
	for _, callback := range callbacks { callback() }
}

func (c *qblockClient) progress(outputs []qblock.Output) []func() {
	return c.executeOrdered(outputs) // caller owns actionMu
}
```

Convert start, receive, control, handoff, manual tick, and write-failure cancellation to `runProgress`; avoid calling `drive` recursively while the gate is held. Snapshot all packet fields, request/reply token, operation, transfer ID, record generation, and a coordinator-owned cancellation context under `mu`; revalidate ownership immediately before writing. `close`, `abandon`, and Reset cancel contexts and invalidate indexes under `mu` without acquiring `actionMu`, then execute only safe cleanup outside it.

- [ ] **Step 4: Run focused ordering plus adapter regressions**

Run: `rtk go test ./udp/client -run 'TestQBlock(Client|Server|BlockedBurstPrecedesControlAndDueTick|CloseCancelsContextAwareBlockedServerWrite)' -count=1`

Expected: PASS; no callback runs under the gate, canceled queued outputs do not write, and an ignored custom-session context is the only permitted non-bounded drain case.

- [ ] **Step 5: Commit ordered progress and cancellation**

```bash
git add udp/client/qblock_client.go udp/client/qblock_server.go udp/client/qblock_client_test.go udp/client/qblock_server_test.go
git commit -m "fix(qblock): serialize progress with cancelable writes"
```

### Task 4: Bound terminal callback ownership

**Files:**
- Modify: `udp/client/qblock_client.go: qblockClient, qblockExchange, prepare, finishExchangeLocked, prepareFailure, prepareTerminalResponse, close`
- Test: `udp/client/qblock_client_test.go`

**Interfaces:**
- Consumes: Task 3’s callback-after-gate execution rule and `ManagerConfig.MaxTransfers`.
- Produces a private `qblockCallbackDispatcher` with a fixed worker and queue capacity equal to `ManagerConfig.MaxTransfers`; it accepts `submit(func()) bool`, runs no callback under the gate or on a scheduler worker, and stops without waiting for a blocked callback.
- Produces a private `qblockCallbackSlots` with `tryAcquire() bool` and idempotent `release()` closure.
- Produces `callbackRelease func()` on `qblockExchange`, reserved once for the logical exchange and retained across Q1-to-Q2 handoff.

- [ ] **Step 1: Write failing callback-capacity and re-entrancy tests**

```go
func TestQBlockBlockedTerminalCallbacksBoundAdmission(t *testing.T) {
	cc := newPrivateQBlockClientConnWithMaxTransfers(t, 1)
	entered, release := make(chan struct{}), make(chan struct{})
	startTerminalExchange(t, cc, func(error) { close(entered); <-release })
	<-entered
	_, err := startTerminalExchange(t, cc, func(error) {})
	require.ErrorIs(t, err, qblock.ErrCapacity)
	requireNoQBlockLeak(t, cc, 1)
	close(release)
	require.Eventually(t, func() bool { return startTerminalExchangeOK(cc) }, time.Second, time.Millisecond)
}

func TestQBlockTerminalCallbackCanReenterDoAndClose(t *testing.T) {
	cc := newPrivateQBlockClientConn(t)
	startTerminalExchange(t, cc, func(error) { _, _ = cc.Do(newGET(t)); _ = cc.Close() })
	deliverTerminalResponse(t, cc)
	assertNoDeadlock(t)
}
```

- [ ] **Step 2: Run the focused tests and verify they fail**

Run: `rtk go test ./udp/client -run 'TestQBlock(TerminalCallbackCanReenterDoAndClose|BlockedTerminalCallbacksBoundAdmission)' -count=1`

Expected: FAIL because callbacks have no bounded logical-exchange reservation.

- [ ] **Step 3: Reserve and release a logical callback slot**

```go
func (s *qblockCallbackSlots) tryAcquire() (func(), bool) {
	if s.used >= s.limit { return nil, false }
	s.used++
	var once sync.Once
	return func() { once.Do(func() { s.used-- }) }, true
}
```

Protect slot accounting with `c.mu`. Start one dispatcher only for automatic scheduling; it owns a buffered `chan func()` of `MaxTransfers` capacity and exits when its stop channel closes, without waiting for an already-blocked callback. Acquire a client slot before publishing any client exchange maps or token reservation; on every rejected start, release the acquired slot before returning. Wrap terminal callbacks so their slot release occurs after user code returns. Timer-originated terminal callbacks must be submitted to this dispatcher; if close has stopped it, release the slot without invoking user code. Request-owned callbacks may execute on their caller path after the gate has been released. Terminal exchanges without a callback release immediately. Preserve the same closure while `handoffQ1ToQ2Locked` changes transfer phase. Do not use this client callback pool for server handlers: their bounded server record occupies the corresponding slot.

- [ ] **Step 4: Run callback and lifecycle regressions**

Run: `rtk go test ./udp/client -run 'TestQBlock(.*Callback|Client|ServerLifecycle)' -count=1`

Expected: PASS; blocked callbacks limit new logical client admissions without stalling completion, timer ownership, or close.

- [ ] **Step 5: Commit bounded callback accounting**

```bash
git add udp/client/qblock_client.go udp/client/qblock_client_test.go
git commit -m "fix(qblock): bound terminal callback ownership"
```

### Task 5: Implement the private timer owner and due-work worker

**Files:**
- Modify: `udp/client/qblock_scheduler.go`
- Modify: `udp/client/qblock_scheduler_test.go`
- Modify: `udp/client/qblock_client.go: qblockClient, Tick, close`

**Interfaces:**
- Consumes: Tasks 1–4’s `nextDeadlineLocked`, `runProgress`, coherent clock, cancelable contexts, and bounded callbacks.
- Produces `func (c *qblockClient) startScheduler()` and `func (c *qblockClient) stopScheduler()`.
- Produces `func (c *qblockClient) notifyDeadlineChanged()` as a non-blocking capacity-one hint and `func (c *qblockClient) advanceDue(now time.Time)` as the shared manual/automatic advancement operation.

- [ ] **Step 1: Write failing timer lifecycle and deadline tests**

```go
func TestQBlockSchedulerArmsEarliestDeadlineAndParksIdle(t *testing.T) {
	clock := newFakeQBlockClock(time.Unix(100, 0))
	cc := newAutomaticQBlockConn(t, clock)
	require.False(t, clock.ActiveTimer())
	startQ1TransferForTest(t, cc, bytes.Repeat([]byte("x"), 64))
	require.Equal(t, earliestDeadline(t, cc), clock.Deadline())
	completeAllQBlockTransfers(t, cc)
	require.Eventually(t, func() bool { return !clock.ActiveTimer() }, time.Second, time.Millisecond)
}

func TestQBlockSchedulerIgnoresStaleAndLateWakeWithoutDuplicateBurst(t *testing.T) {
	clock := newFakeQBlockClock(time.Unix(100, 0))
	cc, session := newAutomaticQBlockConnWithSession(t, clock)
	startQ1TransferForTest(t, cc, bytes.Repeat([]byte("x"), 64))
	clock.DeliverStale()
	clock.Advance(10 * time.Second)
	waitSchedulerIdle(t, cc)
	requireBurstNumbersOnce(t, session.writesSnapshot())
}
```

- [ ] **Step 2: Run the focused tests and verify they fail**

Run: `rtk go test ./udp/client -run 'TestQBlockScheduler(ArmsEarliestDeadlineAndParksIdle|IgnoresStaleAndLateWakeWithoutDuplicateBurst)' -count=1`

Expected: FAIL because automatic mode has no runtime or deadline notification path.

- [ ] **Step 3: Implement bounded owner/worker coordination**

```go
type qblockScheduler struct {
	clock qblockClock
	timer qblockTimer
	notify chan struct{}
	due chan struct{}
	done chan struct{}
	stop chan struct{}
	stopped chan struct{}
}

func (c *qblockClient) notifyDeadlineChanged() {
	select { case c.scheduler.notify <- struct{}{}: default: }
}
```

The owner is the only goroutine that calls timer `Stop` or `Reset`. On a notification, timer delivery, or worker completion it locks `mu`, recomputes `nextDeadlineLocked`, and either stops the timer, resets it with `max(0, deadline-clock.Now())`, or sends one capacity-one due signal. The due worker acquires `actionMu`, checks open state, samples `clock.Now`, and under `mu` calls `manager.Tick(now)` once only when the authoritative deadline is due; it also expires eligible records. It unlocks `mu`, executes outputs, releases `actionMu`, submits terminal callbacks to Task 4’s fixed dispatcher without executing them itself, then signals `done`. Do not emit a second due turn while one is queued or running.

- [ ] **Step 4: Run deterministic scheduler tests**

Run: `rtk go test ./udp/client -run 'TestQBlockScheduler' -count=1`

Expected: PASS; one enabled connection uses one idle-stoppable timer and two bounded workers, without catch-up floods or lost recomputation hints.

- [ ] **Step 5: Commit the scheduler runtime**

```bash
git add udp/client/qblock_scheduler.go udp/client/qblock_scheduler_test.go udp/client/qblock_client.go
git commit -m "feat(qblock): schedule private connection deadlines"
```

### Task 6: Integrate lifecycle notifications, legacy ticks, and shutdown

**Files:**
- Modify: `udp/client/qblock_client.go: every accepted mutation, Tick, abandon, close`
- Modify: `udp/client/qblock_server.go`, `udp/client/qblock_server_q1.go`, `udp/client/qblock_server_q2.go`
- Modify: `udp/client/conn.go: NewConnWithOpts, CheckExpirations`
- Modify: `udp/client/qblock_scheduler_test.go`
- Modify: `udp/client/qblock_client_test.go`

**Interfaces:**
- Consumes: Task 5’s `startScheduler`, `stopScheduler`, `notifyDeadlineChanged`, and `advanceDue`.
- Produces `func (c *qblockClient) automaticScheduling() bool` and `func (c *qblockClient) schedulerStopped() <-chan struct{}` for same-package tests.

- [ ] **Step 1: Write failing notification, manual-tick, and close-race tests**

```go
func TestQBlockSchedulerRecomputesAfterReceiveCancelHandoffAndRelease(t *testing.T) {
	clock := newFakeQBlockClock(time.Unix(100, 0))
	cc := newAutomaticQBlockConn(t, clock)
	transfer, _ := startQ1TransferForTest(t, cc, bytes.Repeat([]byte("x"), 64))
	require.Equal(t, earliestDeadline(t, cc), clock.Deadline())
	cc.qblockClient.abandon(transfer.initialToken, qblock.ErrCanceled)
	require.Eventually(t, func() bool { return !clock.ActiveTimer() }, time.Second, time.Millisecond)
}

func TestQBlockAutomaticTickDoesNotDuplicateScheduledAdvance(t *testing.T) {
	clock := newFakeQBlockClock(time.Unix(100, 0))
	cc, session := newAutomaticQBlockConnWithSession(t, clock)
	startQ1TransferForTest(t, cc, bytes.Repeat([]byte("x"), 64))
	clock.AdvanceTo(clock.Deadline())
	cc.CheckExpirations(clock.Now())
	waitSchedulerIdle(t, cc)
	requireBurstNumbersOnce(t, session.writesSnapshot())
}

func TestQBlockCloseStopsSchedulerBeforeBlockedWriteDrains(t *testing.T) {
	clock := newFakeQBlockClock(time.Unix(100, 0))
	cc, session := newAutomaticBlockingQBlockConn(t, clock)
	startQ1TransferForTest(t, cc, bytes.Repeat([]byte("x"), 64))
	<-session.contextWriteStarted
	cc.qblockClient.close()
	<-cc.qblockClient.schedulerStopped()
	require.True(t, session.contextWriteCanceled())
}
```

- [ ] **Step 2: Run the focused tests and verify they fail**

Run: `rtk go test ./udp/client -run 'TestQBlock(SchedulerRecomputesAfterReceiveCancelHandoffAndRelease|AutomaticTickDoesNotDuplicateScheduledAdvance|CloseStopsSchedulerBeforeBlockedWriteDrains)' -count=1`

Expected: FAIL because not every mutation signals the owner, legacy tick competes with automatic progression, and shutdown lacks a separate stopped signal.

- [ ] **Step 3: Connect all mutation and connection lifecycle boundaries**

```go
func (c *qblockClient) Tick(now time.Time) {
	if c.automaticScheduling() { c.notifyDeadlineChanged(); return }
	c.advanceDue(now)
}
```

Call `notifyDeadlineChanged` after and only after an accepted deadline-changing mutation: Q1/Q2 start, receive, Continue/repair acceptance, handoff, Reset, cancellation, output write failure, handler settlement, manager release, and record removal. Start the scheduler in `NewConnWithOpts` only after creating the manager/server, registering `qblockClient.close`, and creating `receivedMessageReader`; do not start it for invalid configuration or manual mode. On close, mark closed and cancel write contexts under `mu`, signal stop before waiting for no gate, and remove server/client ownership exactly once. `CheckExpirations` keeps classic behavior; it invokes manual advancement only in manual mode and otherwise merely notifies. Test joins the stopped signal but does not change public `Conn.Close` into a waiting API.

- [ ] **Step 4: Run focused lifecycle and race checks**

Run: `rtk go test ./udp/client -run 'TestQBlock' -count=1`

Run: `rtk go test -race ./udp/client -run 'TestQBlock(Scheduler|Close|Blocked|AutomaticTick)' -count=1`

Expected: PASS; close is idempotent, old timer deliveries and late handler completions cannot rearm work, and timer/periodic calls have one driver.

- [ ] **Step 5: Commit lifecycle integration**

```bash
git add udp/client/qblock_client.go udp/client/qblock_server.go udp/client/qblock_server_q1.go udp/client/qblock_server_q2.go udp/client/conn.go udp/client/qblock_scheduler_test.go udp/client/qblock_client_test.go
git commit -m "feat(qblock): integrate private scheduler lifecycle"
```

### Task 7: Prove paired bidirectional scheduler traces and preserve regressions

**Files:**
- Modify: `udp/client/qblock_server_lifecycle_test.go`
- Modify: `udp/client/qblock_scheduler_test.go`
- Modify: `docs/superpowers/specs/2026-09-22-rfc9177-connection-deadline-scheduler-design.md: status`

**Interfaces:**
- Consumes: automatic-mode constructors and fake-clock barriers from Tasks 2, 5, and 6.
- Produces no production interface; it records implementation evidence and closes this scheduling slice’s deterministic acceptance coverage.

- [ ] **Step 1: Write the failing no-manual-tick paired traces**

```go
func TestQBlockPrivatePairedRolesSchedulerTrace(t *testing.T) {
	clock := newFakeQBlockClock(time.Unix(100, 0))
	client, server, clientWire, serverWire := newScheduledPairedQBlockConns(t, clock)
	startPairedUpload(t, client, codes.POST, bytes.Repeat([]byte("p"), 80))
	dropFirstQ2Block(t, serverWire)
	drainScheduledPairedQBlock(t, clock, client, server, clientWire, serverWire)
	requireCompletedExactlyOnce(t, client, server)
}

func TestQBlockSchedulerCloseWithBothRolesActive(t *testing.T) {
	clock := newFakeQBlockClock(time.Unix(100, 0))
	cc := newConnWithActiveClientAndServerQBlockRoles(t, clock)
	cc.qblockClient.close()
	<-cc.qblockClient.schedulerStopped()
	requireNoQBlockLeak(t, cc, 0)
}
```

- [ ] **Step 2: Run the focused tests and verify they fail**

Run: `rtk go test ./udp/client -run 'TestQBlock(PrivatePairedRolesSchedulerTrace|SchedulerCloseWithBothRolesActive)' -count=1`

Expected: FAIL until the paired fixture advances only its fake clock and no longer calls `Tick` or `CheckExpirations` to drive Q-Block progress.

- [ ] **Step 3: Convert the trace fixture to automatic fake-clock scheduling**

```go
func drainScheduledPairedQBlock(t *testing.T, clock *fakeQBlockClock, a, b *Conn, aw, bw *pairedQBlockSession) {
	for step := 0; step != 128; step++ {
		deliverAvailablePackets(t, a, b, aw, bw)
		if clock.AdvanceToNextTimer() { waitSchedulersIdle(t, a, b); continue }
		if noPendingPacketsOrTimers(a, b, aw, bw) { return }
	}
	t.Fatal("paired Q-Block scheduler trace did not quiesce")
}
```

Run the POST and PUT loss-and-repair variants, then close a connection while it owns a client transfer and a server record. Update the design status from “draft for review” to “implemented” only after the checks in Step 4 pass; leave the stated out-of-scope public configuration, DTLS, pacing, packet-sizing, and memory-accounting work untouched.

- [ ] **Step 4: Run the full deterministic verification set**

Run: `rtk go test ./net/qblock ./udp/client -count=1`

Run: `rtk go test -race ./udp/client -run 'TestQBlock' -count=1`

Expected: PASS. If socket-based package checks are available with permission, run them separately and record an environmental bind restriction distinctly from test assertions.

- [ ] **Step 5: Commit trace evidence and documentation status**

```bash
git add udp/client/qblock_server_lifecycle_test.go udp/client/qblock_scheduler_test.go docs/superpowers/specs/2026-09-22-rfc9177-connection-deadline-scheduler-design.md
git commit -m "test(qblock): cover scheduled bidirectional traces"
```

## Plan Self-Review

- **Spec coverage:** Tasks 1–2 establish combined deadlines, retention semantics, and coherent time sources. Tasks 3–4 establish the ordering, cancellation, callback, and bounded-ownership prerequisites. Tasks 5–6 supply the bounded runtime, mutation notifications, manual/automatic tick separation, and close behavior. Task 7 covers paired roles and closes the design evidence. Public configuration, `udp/server`, DTLS, capability discovery, server-initiated GET, aggregate pacing/probing, packet sizing, and full memory accounting remain explicitly out of scope.
- **Placeholder scan:** The plan has no deferred implementation markers or “test the above” steps; each task names concrete tests, interfaces, commands, and commit contents.
- **Type consistency:** `qblockClock`, `qblockTimer`, `qblockScheduleMode`, `nextDeadlineLocked`, `runProgress`, `notifyDeadlineChanged`, `advanceDue`, `startScheduler`, `stopScheduler`, and `schedulerStopped` are introduced before dependent tasks consume them.
- **Review Focus:** Task 1 owns late-handler settlement; Task 3 owns write ordering/cancellation; Tasks 5–6 own stale wake and manual/automatic-driver coverage; Task 4 owns bounded callbacks.
