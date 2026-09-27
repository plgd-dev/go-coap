# Private Q-Block connection deadline scheduler

Status: private scheduler implemented and final Task 3 ownership/cancellation
audit completed, 2026-09-27. Automatic paired POST/PUT loss-and-repair traces,
queued-delivery rollback, close-callback shutdown, and post-gate clock sampling
are covered by deterministic tests. Final verification: `go test
./net/qblock ./udp/client -count=1` passed (423 tests), and `go test -race
./udp/client -run TestQBlock -count=5` passed (940 test executions). An earlier
loopback-dependent failure did not reproduce in the final full-package run.

Predecessor: [private bidirectional UDP adapter](2026-09-21-rfc9177-private-bidirectional-udp-adapter-design.md).
Roadmap: [RFC 9177 implementation plan](../plans/2026-09-15-rfc9177.md), Milestone 3.
Source baseline: `b680461` on `feat/qblock-foundation`.

## Outcome and scope

A privately enabled `udp/client.Conn` should advance both Q-Block roles when
their next deadline arrives, without depending on an external periodic call
to `CheckExpirations`. One connection owns one deadline timer, one manager,
and one serialized path for protocol progress and packet output. Completed
server request records must expire even after the manager has no transfers.

Implementing this design completes the scheduling part of Milestone 3.
Construction remains
private in `udp/client`, with tests in the same package. Public configuration,
`udp/server` construction, capability discovery, DTLS integration, server GET
initiation, aggregate traffic pacing, probing-rate enforcement, packet sizing,
and complete memory accounting remain separate work. Existing transfer-machine
delays and retry rules are reused; waking them on time is not a claim that
connection-wide congestion control has been implemented.

Success is observable progress at the earliest eligible deadline, no duplicate
bursts from competing timer sources, bounded scheduler resources, and shutdown
that prevents new work and interrupts writes before waiting for output.

## Existing behavior and prerequisites

`Manager.NextDeadline` already finds the earliest transfer deadline.
`Manager.Tick` advances due transfers in stable transfer-ID order and each
sender advances at most one set per call. These remain deterministic,
synchronization-free core operations.

`qblockClient.Tick` currently changes manager state under `mu`, then calls
`drive` after unlocking. `drive` serializes output through `actionMu` and runs
callbacks after releasing it. Consequently, serialization of writes alone
does not establish ordering between state transitions and their writes:
another event can advance a sender while an earlier burst is still pending.
An autonomous timer must not amplify this gap. The Sender contract expressly
requires execution of a burst before the next progress event.

Completed server records have deadlines outside the manager. The scheduler
must combine both sources. It must also resolve this current lifecycle gap:
manager expiry can mark a record terminal while its handler is running, but
`finishHandler` returns early on `record.terminal` before clearing
`handlerRunning`. Such a record remains pinned indefinitely. The existing
blocked-handler test closes the connection before allowing the handler to
return, so it does not establish normal post-expiry unpinning.

These are prerequisites within this slice, with failing regressions before
implementation. The predecessor's completion ledger is not evidence that
these additional cases already work.

## Approaches considered

1. **One resettable timer per enabled connection — recommended.** It consumes
   the existing manager deadline plus server retention deadlines and fits the
   connection's ownership and shutdown boundaries. Cost: a small private
   runtime and explicit notifications after state changes.
2. **Keep periodic polling.** This needs fewer new lifecycle hooks, but progress
   remains dependent on the caller's polling cadence and idle connections
   keep being visited. It does not satisfy this slice's scheduling outcome.
3. **A shared timer heap across connections.** It could reduce runtime cost at
   large connection counts, but introduces cross-connection ownership and
   fairness before those requirements have been established. Defer it until
   measurements justify the change.

## Deadline ownership

The coordinator computes the minimum of:

- `manager.NextDeadline()` for active client and server transfer machines;
- server request retention deadlines eligible for removal.

Use a private `nextDeadlineLocked() (time.Time, bool)` on the coordinator and
a private server helper. Both require `mu`. The boolean distinguishes no
deadline from a legitimate timestamp; do not use timestamp zero as an
independent state flag. Keep the bounded map scans already used by the manager
instead of introducing a second transfer index.

Ready deliveries and running handlers retain their record slot. They do not
contribute a record-removal deadline until the callback has settled. Their
manager transfer can still expire and release its body/token resources.

When a matching handler finishes, clear the running flag even if its wire
transfer expired or was canceled while it ran. Discard a result for a canceled
wire phase, but settle its duplicate-suppression record and arm retention from
that settlement time. Connection close removes the record immediately and a
late result must not recreate it.

For ordinary handler completion, establish `retainUntil = completionTime +
Retention` once. A later Q2 release or failed write must not repeatedly extend
it. A retained Q2 sender has its own manager lifetime; the existing requirement
`Retention >= Transfer.Lifetime` covers a Q2 sender started at the same
completion time. At a shared boundary, release active transfer ownership before
removing the suppression record. Delayed handler completion starts retention
when execution settles, not at first-fragment admission.

All expiry comparisons use `!now.Before(deadline)`. A late wake advances the
machines once at the current time, without replaying every elapsed timer
interval. A no-progress event must not leave an eligible deadline permanently
in the past and spin the runtime.

## Clock and construction

Introduce a private clock interface supplying `Now` and a stoppable/resettable
timer with a receive channel. Production time and timer creation come from
the same clock. A deterministic fake owns both for tests. Replacing `Now`
alone while a real timer is running is prohibited. The timer abstraction must
specify safe stop/reset behavior and model stale delivery in its fake; do not
rely on competing goroutines draining or resetting the same timer.

Private construction has explicit automatic and manual scheduling modes.
Automatic mode is the normal private runtime. Migrate existing tests with an
injected `Now` function to explicit manual mode; scheduler tests inject the
complete fake clock. Reject contradictory configuration rather than silently
mixing clock domains. No exported transport option is added.

Start the runtime only after the manager, both role adapters, receive hooks,
and close hook are initialized. Invalid private configuration and connections
without Q support create no scheduler goroutines or timer. Initialize once,
not once per transfer or role. An idle runtime parks with the timer stopped.

## Wake and execution flow

Use a capacity-one notification channel as a hint to recompute the deadline.
It carries no outputs, pooled messages, or timestamp snapshots. Coalescing
notifications is safe because the authoritative state is under `mu`.

Every accepted mutation that can change a deadline requests recomputation:
start, receive, Continue/repair, Q1-to-Q2 handoff, cancellation, Reset, write
failure, handler settlement, manager Release, and suppression-record removal.
Rollback/no-op paths must not publish a new deadline or reserve a token.

The runtime consists of a timer owner and one due-work worker. Only the timer
owner operates the timer. At most one due-work turn can be queued or running;
further wakes coalesce until that turn completes. This prevents a blocked write
from creating goroutines or accumulating speculative sender bursts.

On notification or timer delivery, the owner recomputes the earliest deadline
from current state. An early or stale timer delivery is only a wake hint. If
work is due and no turn is outstanding, notify the worker. While it is busy,
stop the timer and retain a recomputation hint. On completion, recompute and
arm the next deadline. No timer reset receives a negative duration.

The worker acquires the common progress/execution gate, checks that the
connection is open, then samples time again. Under `mu`, it validates the
current deadline and calls `Manager.Tick` once if needed. It snapshots action
ownership, unlocks `mu`, executes the outputs in order, and settles resulting
releases and eligible record expiry before reporting completion to the timer
owner. If work was canceled while waiting for the gate, it does nothing.

This adds bounded runtime state: one timer, two workers, and capacity-one
notification/work/completion signals per enabled connection. No per-transfer
timer and no unbounded timer-event queue are introduced. Protocol output-copy
accounting remains part of the later memory-accounting work.

## Progress ordering, cancellation, and callbacks

Use the existing `actionMu` as the common progress/execution gate. Refactor
ordinary Q-Block start, receive, control, and timer events so the state
transition and execution of its burst occur in the same gate turn. Lock order
for progress is `actionMu` then `mu`; never wait for `actionMu` while holding
`mu`. This requires an executor entry point for callers that already own the
gate, avoiding recursive acquisition through `drive`.

Cancellation and close must bypass the gate to invalidate state and cancel
write contexts under `mu`. Their cleanup/callback execution follows outside
that lock. Every queued send and delivery rechecks transfer ID, operation,
record generation where applicable, and cancellation state before starting.
Keep immutable reply context attached to the actual output batch; block number
alone is not a unique identity for successive repair outputs.

Do not hold `mu` during packet writes, callbacks, or pooled-message release.
Do not invoke application callbacks under `actionMu`. The scheduler also must
not run user code on its timer owner or due-work worker: a callback can block
or call a connection API, including Close.

Use bounded callback dispatch for timer-originated terminal notifications.
Reserve a private callback slot when admitting a logical client exchange and
hold it until its callback returns; cap these slots at the configured manager
transfer limit. Server handlers retain their already-bounded record slot.
Callback dispatch has no unbounded queue or goroutine-per-wake path. A blocked
callback can consume its slot and cause new admissions to fail locally, but
cannot block timers or shutdown. Slot release after callback return must be
idempotent even after close. Ordinary request-owned callback execution may
remain on the request caller's path, subject to the same lock rules. Rejected
admissions roll back their slot, and terminal exchanges with no callback release
it immediately. A callback slot belongs to the logical exchange across Q1/Q2
handoff, not to each transfer phase.

Packet writes remain serialized. A transport write that blocks can delay other
packet progress; this scheduler does not claim to solve that fairness problem.
All private writes need a coordinator-owned cancelable context, combined with
request cancellation where applicable. Cancellation/close must interrupt a
context-aware blocked write before waiting for executor quiescence. A custom
Session that ignores context cancellation cannot be promised bounded drain.

## Legacy Tick and CheckExpirations

Keep the private `Tick(now)` entry point for deterministic manual-mode tests.
It calls the same ordered advancement operation as automatic scheduling.
Reject using it as a second automatic driver: in automatic mode it requests
recomputation and time is sampled from the configured clock.

`Conn.CheckExpirations` continues its existing cache, classic blockwise,
inactivity, and retransmission work. In automatic Q mode it only notifies the
Q scheduler; in manual mode it forwards the supplied time as today. Tests must
cover a periodic tick racing a scheduled wake at the same boundary, proving
one manager advancement and one resulting burst.

## Shutdown

Closing the coordinator marks it closed under `mu`, rejects admissions,
invalidates outstanding work, cancels its write context, and signals scheduler
stop before waiting on the execution gate. Timer shutdown is idempotent and
does not require acquiring the output gate or invoking a callback.

Separate the scheduler's stopped signal from output-drained completion. The
timer owner can exit while an already-started write unwinds; neither runtime
worker waits for itself, and callbacks do not run on either worker. Final
cleanup settles each waiting client at most once, clears both role indexes,
and releases records/tokens/MIDs. Repeated close and a late handler result do
not rearm a timer, write a packet, or republish a record.

A timer-triggered write failure must use the same cancellation/cleanup path as
an inbound-triggered failure. It must not synchronously join a runtime worker
from that same worker's stack. Tests join runtime workers explicitly; public
connection Close keeps its existing non-waiting lifecycle contract.

## Deterministic acceptance tests

Use the real manager and adapter with a fake clock/timer and copied fake-session
packets. Clock advancement and worker barriers determine progress; wall-clock
timeouts are only deadlock guards.

1. Disabled/invalid configuration starts no runtime; enabled idle state keeps
   no active timer. First admission arms exactly the earliest deadline.
2. Earlier/later deadlines after receive, Continue, repair, cancellation,
   handoff, and release rearm correctly, including concurrent notification
   immediately before parking. No wake is lost.
3. Two roles with equal deadlines advance in manager transfer-ID order. Early,
   stale, repeated, and late timer notifications neither duplicate bursts nor
   produce catch-up floods.
4. A terminal server record expires when manager Active is zero. A blocked
   handler stays pinned; after transfer expiry and eventual handler return,
   its result is discarded and the record expires at its settlement boundary.
5. Q2 lifetime expiry and record retention sharing a timestamp release all
   indexes/counters once. Suppression remains until its intended deadline
   after failed sends, without deadline extension on duplicate cleanup.
6. Hold a write while a control and a timer become due. Sender progress does
   not run ahead of the earlier burst. Subsequent output uses its own accepted
   reply context. Cancellation discards unsent actions.
7. Timer and periodic CheckExpirations race at one boundary. They produce one
   advancement. Manual-mode tests create no real timers.
8. Close during timer reset, gate contention, a context-aware blocked write,
   and a blocked callback stops/rejects work without deadlock. Late results,
   repeated close, and old timer deliveries cannot restart the connection.
9. A completion callback re-enters Do/Close; no lock or self-join deadlock.
   Block callbacks to fill their bounded slots; admission fails without
   operation/token/body leaks and resumes when capacity becomes available.
10. Paired POST/PUT loss-and-repair traces complete using scheduler wakes,
    without direct Tick calls. Close one connection with both of its roles
    active and verify runtime and ownership cleanup.

Preserve existing core, ordinary Do/Observe, classic blockwise, and disabled-Q
regressions. Run focused scheduler and paired traces normally and under the
race detector, followed by affected package checks. Use permission-approved
socket checks where available; record actual environmental failures separately
from deterministic assertions. No performance or interoperability claim follows
from fake-session traces.

## Implementation-plan handoff

After review, expand into ordered TDD tasks: lifecycle/deadline prerequisites;
private clock and mode contracts; progress/execution ordering and cancelable
writes; bounded callback ownership; timer runtime and mutation notifications;
connection integration and paired traces. Each task needs its own runnable
acceptance tests before its dependent task begins.

Expected files are `udp/client/qblock_scheduler.go` and its tests, plus focused
changes to `qblock_client.go`, `qblock_server*.go`, and `conn.go`. The initial
design needs no new public `net/qblock` API or production dependency. Review
the ordering and callback-admission changes explicitly in the implementation
plan because they are the main cost of safely introducing autonomous progress.
