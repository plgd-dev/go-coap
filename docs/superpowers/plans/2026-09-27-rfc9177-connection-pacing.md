# Private Q-Block Connection Pacing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enforce the approved shared probing policy for both private UDP roles without consuming retries before transmission or leaking queued operation state.

**Architecture:** Add prepared senders and explicitly committed receiver controls to the deterministic core. A private connection coordinator owns bounded pending work and a shared probing gate, driven by the existing deadline scheduler. Preserve immediate core APIs and the adapter's ordering, cancellation, and duplicate-suppression contracts.

**Tech Stack:** Go 1.25.0, existing `net/qblock` machines, `udp/client`, UDP coder, `testing`, `testify/require`, existing fake clocks and sessions; no new dependencies.

**Spec:** [Connection pacing design](../specs/2026-09-27-rfc9177-connection-pacing-design.md). Read both documents before execution.

## Progress (2026-09-28)

Tasks 1–5 are complete and committed: prepared senders (`4877126`), deferred
receiver controls (`df7918d`), probing gate and packet sizing (`525984f`),
bounded pending work (`089e3ec`), and paced private client traffic (`2775a89`).
Tasks 6–7 are complete; Task 8 remains open.
The Task 5 completion run passed
`rtk go test ./udp/client -run TestQBlock -count=1` (216 tests); a broader
core/client selection passed 285 tests and repeated paced-path race checks
passed. A full `udp/client` run is still blocked by the documented host IPv6
`invalid srcAddr type <nil>` failure in the unrelated `TestConnDeduplication`.
This is not yet a complete or publicly enabled pacing feature.

Task 6 verification passed `rtk go test ./udp/client -run TestQBlock -count=1`
and the focused server pacing tests under `-race`. The required broader
`net/qblock` + `udp/client` run passed core tests but failed in the unrelated
IPv6 `TestConnDeduplication` path (`invalid srcAddr type <nil>`), also reproduced
on the untouched original branch. This is not counted as a passing full suite.

Task 7's queued-GET cancellation regression failed before the callback fix and
passed afterward. The other new lifecycle adversaries passed without further
production changes; `rtk go test -race ./udp/client -run 'TestQBlock(Pacing|Paced)' -count=1 -timeout=180s` passed. Existing expiry selection and release paths
met those tests, so the proposed extra `expirePendingWorkLocked` helper and
`releasePacingWorkLocked` signature change were not added speculatively.

## Global Constraints

- Construction and tests remain in `udp/client`; this introduces no exported transport option or `udp/server` construction hook.
- The accounting boundary is the existing `Conn`.
- The default rate is one byte per second.
- An explicit wait has no jitter.
- Reserve work slots across both roles, capped at `ManagerConfig.MaxTransfers`; default intent-byte capacity is `ManagerConfig.MaxRetainedBytes`.
- Charge encoded CoAP datagram bytes, including header, token, options, payload marker, and payload.
- A prepared sender's lifetime starts at preparation; activation does not restart it.
- Expiry wins ties with readiness.
- Keep `actionMu` then `mu` ordering; waiting for a rate deadline holds neither lock.
- No new timer or goroutine per intent is introduced.
- Preserve immediate core behavior; new `net/qblock` methods are exported API additions requiring review.
- Q1 2.31 Continue bypasses the probing gate; supported Q2 control methods all use the individually paced control path.
- Preserve completed-request suppression after failure and retained Q2 representations after successful initial transmission.
- Packet sizing, complete memory accounting, capability discovery, public configuration, DTLS, and additional methods remain outside this plan.

## Review Focus

- A size calculation consumes a seekable body: the eventual session write must still see the original payload and cursor (Task 3).
- Canceled/reused operation names and tokens receive old feedback: only the current work generation and attempted packet can open the gate (Tasks 3, 5, 6).
- Retry count is zero, or time reaches expiry during a partial control batch: no extra repair is emitted and no late commit revives state (Tasks 2, 7).
- Large options or arithmetic overflow exceed a reservation: fail atomically before publishing work; attempted traffic must never wrap its debt into immediate readiness (Tasks 3, 4).
- A server handler finishes while an unrelated body owns the gate: settle execution immediately, retain duplicate suppression, and expire an unsent response without replay (Tasks 6, 7).

---

## Baseline, file map, and execution rules

Worktree: `/private/tmp/go-coap-qblock-foundation`, branch `feat/qblock-foundation`.
Implementation baseline: `9a7e038`; approved design commit: `cd555f3`.
The recovered, modified `2026-09-21-rfc9177-private-bidirectional-udp-adapter.md`
belongs to the user. Do not stage, rewrite, or use its historical status as the
current implementation ledger. Record progress with the checkboxes here.

| Files | Responsibility |
| --- | --- |
| `net/qblock/manager_sender.go` (new), `manager.go` | Prepare/activate registration and immediate wrapper |
| `net/qblock/receiver_control.go` (new), `receiver.go`, `manager.go` | Pending control revisions, commits, progress observation |
| `udp/client/qblock_pacing.go` (new) | Private config, checked timing arithmetic, scalar probing gate |
| `udp/client/qblock_pending.go` (new) | Shared work slots, intent storage reservations, stable selection |
| `udp/client/qblock_transmit.go` (new) | Packet sizing, charged writes, pending-work execution |
| `udp/client/qblock_client.go`, `conn.go` | Client admission, initial GET ownership, response correlation |
| `udp/client/qblock_server.go`, `qblock_server_q1.go`, `qblock_server_q2.go` | Server admission, handler settlement, reply context, feedback |
| `udp/client/qblock_scheduler.go` and coordinator scheduler methods | Deadline integration and shutdown |
| New focused `*_test.go` files named below | Core, gate, queue, integration, and lifecycle regressions |

Run shell commands through `rtk` from the worktree. Before implementation,
record `rtk git status --short` and run
`rtk go test ./net/qblock ./udp/client -count=1 -timeout=180s`; distinguish
environmental socket failures from assertion failures. Every task follows
red → green → focused regression → commit. Stage explicit task files only.
Use the suggested commit message after a clean `rtk git diff --check` and
inspection of `rtk git diff --cached --name-only`.

Tasks 1–4 establish independently tested contracts. Tasks 5–8 integrate them in
order. Do not enable partially integrated pacing as a finished feature. The
new contracts must be used by both roles before this slice is marked complete.

## Task 1: Prepare senders without emitting a burst

**Files:** Create `net/qblock/manager_sender.go`, `net/qblock/manager_sender_test.go`; modify `net/qblock/manager.go`; reuse `sender.go` without changing its immediate contract.

**Interfaces:** Preserve the existing `StartSender` signature. Produce:

```go
func (m *Manager) PrepareSender(operation OperationKey, token message.Token, kind Kind, meta Metadata, payload []byte, now time.Time, jitter float64) (TransferID, error)
func (m *Manager) ActivateSender(id TransferID, now time.Time) ([]Output, error)
```

- [x] **1. Write `TestManagerPreparedSenderReservesWithoutSending`.** Use `MaxPayloads=2`, a 48-byte Q1 body at SZX16, token `{1}`, and `time.Unix(100, 0)`. After preparation assert `require.Equal(t, uint32(1), m.Active())`, `require.Equal(t, uint64(48), m.retained)`, and `require.False(t, m.byID[id].sender.started)`. `NextDeadline` equals preparation time plus Lifetime. A Tick one second later produces no output. Activation then produces `outputNumbers(outputs) == []uint32{0, 1}` and retains the original expiry.
- [x] **2. Add transactional boundary tests.** `TestManagerPreparedSenderRollback` covers empty/duplicate operation, token collision, invalid metadata/jitter, and exhausted transfer/token/byte capacity; compare all registries and retained counters before/after. `TestManagerPreparedSenderExpiryAndControls` proves mutation-free rejection of Continue/repair before activation, including `ControlWithToken`; exact-boundary activation expires/releases; cancellation releases once; second activation returns `ErrAlreadyStarted`. Mutating caller payload/token after prepare must not change owned state.
- [x] **3. Run red:** `rtk go test ./net/qblock -run TestManagerPreparedSender -count=1`. Expect failure because the two methods do not exist.
- [x] **4. Implement the interfaces.** Move sender registration into `manager_sender.go`; construct `NewSender` without calling Start. Activate with `Sender.Start`, normal output conversion, and release handling. Implement existing `StartSender` as prepare followed immediately by activate. An activation error in this wrapper must roll back its newly prepared record; a second explicit activation must leave the existing sender intact.
- [x] **5. Run green:** `rtk go test ./net/qblock -count=1`. All existing immediate-sender tests and new preparation tests pass.
- [x] **6. Commit:** `feat(qblock): separate sender preparation from activation`.

## Task 2: Commit receiver controls after their packets are sent

**Files:** Create `net/qblock/receiver_control.go`, `net/qblock/receiver_control_test.go`, `net/qblock/manager_control_test.go`; modify `receiver.go`, `manager.go`.

**Interfaces:** Consume existing `Action`, `Fragment`, `Output`, and manager routing. Produce:

```go
type ControlIntent struct { Revision uint64; Action Action }
func NewDeferredReceiver(kind Kind, cfg TransferConfig, meta Metadata, now time.Time) (*Receiver, error)
func (r *Receiver) PendingControls() []ControlIntent
func (r *Receiver) CommitControl(revision uint64, now time.Time) []Action
func (m *Manager) StartReceiverDeferred(fragment Fragment, now time.Time) ([]Output, error)
func (m *Manager) PendingControls(id TransferID) []ControlIntent
func (m *Manager) CommitControl(id TransferID, revision uint64, now time.Time) []Output
func (m *Manager) ReceiverProgress(id TransferID) (uint64, bool)
```

`ControlIntent.Action` is exactly one SendContinue or RequestMissing action.
Each has a distinct nonzero revision; at most two intents exist per receiver.
A RequestMissing intent expands into a batch of Q2 packets, and is committed
only after its final packet. A Continue is separately committed after its own
write, even when another recovery intent remains pending. This defines the
spec's control-batch boundary without holding a sent Continue uncommitted.

- [x] **1. Write `TestDeferredReceiverRetriesBeginAtCommit`.** Use a 48-byte Q1 body, SZX16, MaxPayloads=2, Lifetime=60s, NonReceiveTimeout=4s. Receive block 0 at t0; Tick(t0+4s) creates one intent and returns no RequestMissing output. Assert `require.Zero(t, r.retries)` and NextDeadline=t0+60s. Tick(t0+20s) emits nothing. Commit at t0+20s: retries becomes 1 and next retry is t0+28s, preserving the existing `retryDelay(1)=8s`. Repeating that revision changes nothing.
- [x] **2. Pin replacement, continuation, and expiry semantics.** `TestDeferredReceiverProgressInvalidatesIntent` records old revision, accepts a new block, then proves the old commit leaves retries/deadline unchanged; malformed and duplicate fragments preserve the revision and progress counter. `TestDeferredReceiverContinueCommitsIndependently` sends Continue before a separately delayed missing report and proves it is not regenerated on the next block. `TestDeferredReceiverZeroRetriesAndLateCommit` uses NonMaxRetransmit=0 and an exact lifetime boundary; assert no recovery intent and no resurrection. `TestDeferredReceiverCompletesWithPendingControl` delivers once and removes all intents.
- [x] **3. Add manager ownership tests, then run red:** `rtk go test ./net/qblock -run 'Test(DeferredReceiver|ManagerDeferredControl)' -count=1`. Manager tests cover invalid first-fragment rollback, copied intent slices, stale/released transfer IDs, and two receivers with equal local revision values. Expect missing-API failures before implementation.
- [x] **4. Implement shared transitions.** Factor intent creation/commit out of Receiver.Receive/Tick. Deferred mode keeps controls in owned pending state and emits only non-control actions; immediate mode commits newly generated intents at the event time and returns the same actions/order as today. Increment receiver progress only on accepted nonduplicate data. Successful final delivery is also observable through Deliver output if the manager immediately removes Q2. Expiry takes precedence in commit and remains visible while retries are suspended. Keep canceled/replaced revision values unreusable for that receiver lifetime.
- [x] **5. Run green:** `rtk go test ./net/qblock -count=1`. Existing `TestReceiverImmediateGapReportConsumesRetryBudget` and deadline tests must still pass unchanged. Check that unknown/stale manager commits return no outputs and cannot affect another transfer.
- [x] **6. Commit:** `feat(qblock): support deferred receiver control commits`.

## Task 3: Implement checked probing arithmetic and the scalar gate

**Files:** Create `udp/client/qblock_pacing.go`, `qblock_pacing_test.go`, `qblock_transmit.go`, `qblock_transmit_test.go`; add the private Pacing field in `qblock_client.go` without routing traffic yet.

**Interfaces:** Add `Pacing *qblockPacingConfig` to `qblockClientConfig`. Produce:

```go
type qblockPacingConfig struct {
    ProbingRate uint64
    NonProbingWait time.Duration
    MaxIntentBytes uint64
}
type qblockProbeKey uint64
type qblockProbeKind uint8 // qblockProbeBody, qblockProbeControl
func normalizeQBlockPacingConfig(cfg *qblockPacingConfig, mc qblock.ManagerConfig) (qblockPacingConfig, error)
func qblockProbingWait(cfg qblockPacingConfig, tc qblock.TransferConfig, jitter float64) (time.Duration, error)
func qblockProbeDelay(bytes, rate uint64, cap time.Duration) time.Duration
func newQBlockProbeGate(rate uint64) *qblockProbeGate
func (g *qblockProbeGate) ready(now time.Time) bool
func (g *qblockProbeGate) admit(key qblockProbeKey, kind qblockProbeKind, wait time.Duration, now time.Time) bool
func (g *qblockProbeGate) charge(key qblockProbeKey, bytes uint64)
func (g *qblockProbeGate) settle(key qblockProbeKey, now time.Time)
func (g *qblockProbeGate) feedback(key qblockProbeKey) bool
func (g *qblockProbeGate) nextDeadline() (time.Time, bool)
func qblockDatagramSize(msg *pool.Message) (uint64, error)
```

Nil Pacing selects defaults: rate=1, wait=0 (derive), intent bytes=manager
limit. An explicit configuration must have positive rate and intent bytes;
wait=0 derives, wait<0 rejects. The gate is synchronization-free and called
under coordinator.mu. Probe keys are monotonic connection-local identities,
never tokens. Key zero cannot be admitted and is used only for an explicitly
ungated Continue write. `cap=0` means uncapped delay, not an unlimited rate.

- [x] **1. Write `TestQBlockPacingArithmetic`.** Pin these assertions: `qblockProbeDelay(1, 3, 0) == 333333334*time.Nanosecond`; `(100, 1, 0) == 100*time.Second`; `(1000, 1, 7*time.Second) == 7*time.Second`. Derived wait with default TransferConfig is 247s at jitter 0 and 248s at jitter 1; explicit 9s stays 9s for both. Reject invalid rate/bytes, nonfinite/out-of-range jitter, and derived-wait overflow. Uncapped runtime delay overflow saturates at `time.Duration(math.MaxInt64)`; it never wraps to a past deadline.
- [x] **2. Write gate ownership tests.** `TestQBlockPacingGateBodyAndControl` admits body key1, charges 100 bytes, denies key2 while active, settles at t0, then permits key2 at t0+7s with cap7s. A 100-byte control at rate1 still waits 100s. `TestQBlockPacingGateStaleFeedbackAndCancellation` proves feedback before charge is ignored, matching feedback opens once, old feedback cannot open key2, zero-byte settlement opens immediately, and partial cancellation preserves debt. A late wake grants only one new reservation; idle time creates no credit.
- [x] **3. Write `TestQBlockDatagramSizePreservesBodyCursor`.** Compare returned size with `len(msg.MarshalWithEncoder(coder.DefaultCoder))` for empty/nonempty payloads, long options, and token lengths 1 and 8; restore the fixture cursor before measuring. After sizing, assert the original seek position and `io.ReadAll` result are unchanged. Inject seek/read/encoding failures and require an error. Run red: `rtk go test ./udp/client -run 'TestQBlock(Pacing|DatagramSize)' -count=1`.
- [x] **4. Implement configuration, gate, and sizing.** Use overflow-safe integer nanosecond division rounded upward; saturate runtime debt and reject invalid derived configuration before connection initialization. Derived wait is `NonTimeout*(2^NonMaxRetransmit-1)*1.5 + 200s + sendDelay(jitter)` using the same body sample as the sender. Track open/active-body/settled states, attempted bytes, owner key, and deadline. Size with `MarshalWithEncoder(coder.DefaultCoder)` and restore the body cursor on success and error; retain no returned buffer beyond the call. Settlement samples actual completion time. Stale-key operations are no-ops.
- [x] **5. Run green:** repeat the focused command, then `rtk go test ./udp/client -run TestQBlock -count=1`. No production send path uses the new gate yet.
- [x] **6. Commit:** `feat(qblock): add checked private probing gate`.

## Task 4: Bound pending work and reserve future control capacity

**Files:** Create `udp/client/qblock_pending.go`, `qblock_pending_test.go`.

**Interfaces:** Consume Task 2 ControlIntent and Task 3 gate/config. Produce:

```go
type qblockWorkID uint64
type qblockWorkKind uint8 // qblockWorkBody, qblockWorkGET, qblockWorkControls
type qblockControlWork struct {
    Intent qblock.ControlIntent
    ReplyToken message.Token
    PacketIndex int
    ProbeKey qblockProbeKey
}
type qblockPendingWork struct {
    Kind qblockWorkKind
    Operation qblock.OperationKey
    TransferID qblock.TransferID
    Generation uint64
    Expires time.Time
    ProbeKey qblockProbeKey
    NonProbingWait time.Duration
    RequestCode codes.Code
    RequestOptions message.Options
    RequestToken message.Token
    Controls []qblockControlWork
}
func newQBlockWorkQueue(maxSlots uint32, maxBytes uint64) *qblockWorkQueue
func qblockControlCapacity(options message.Options, maxPayloads uint32) (uint64, error)
func (q *qblockWorkQueue) reserve(controlBytes uint64) (qblockWorkID, error)
func (q *qblockWorkQueue) replace(id qblockWorkID, work qblockPendingWork, retainOrder bool) error
func (q *qblockWorkQueue) next(now time.Time, gate *qblockProbeGate) (qblockWorkID, qblockPendingWork, bool)
func (q *qblockWorkQueue) nextDeadline(now time.Time, gate *qblockProbeGate) (time.Time, bool)
func (q *qblockWorkQueue) clearPending(id qblockWorkID)
func (q *qblockWorkQueue) release(id qblockWorkID)
```

Keep one slot per logical exchange across handoff, distinct from its pending
entry. Queue entries reference prepared-body IDs and never contain bodies,
pooled messages, callbacks, or transport closures. `next` returns an owned
snapshot without removing it; callers must revalidate before writing.

- [x] **1. Write `TestQBlockPendingSharedCapacityRollback`.** With maxSlots=2 and maxBytes=128, reserve 48 bytes for client and 48 for server; a third slot fails with `qblock.ErrLimitExceeded`. A replacement requiring more than the remaining shared bytes fails without changing order, entry, or counters. Release twice and assert one capacity refund. Use `qblockControlCapacity` for realistic options in separate tests; artificial 48-byte reservations test queue arithmetic only.
- [x] **2. Pin ownership and scheduling.** `TestQBlockPendingControlCapacitySurvivesFullQueue` replaces a body activation with a bounded control batch without needing a new slot/reservation. `TestQBlockPendingOrderingAndCopies` mutates source options/tokens/numbers after replace and observes unchanged snapshots; replacements retain order, separately paced next packets join the tail. `TestQBlockPendingExpiryAndUngatedContinue` proves expiry wins readiness and a Continue is eligible behind a closed gate. `TestQBlockPendingOversizedOptions` exercises checked byte summation and exact-limit success versus one-byte-over rollback. Run red: `rtk go test ./udp/client -run TestQBlockPending -count=1`.
- [x] **3. Implement the bounded store.** Use monotonically allocated IDs and enqueue sequence numbers; bounded scans are sufficient. Budget retained slice backing storage and copied bytes for options, tokens, control descriptors, and at most MaxPayloads uint32 numbers plus a Continue descriptor. Also reserve one MaxPayloads-bounded current-probe correlation list/descriptor per slot, allowing a sent report's feedback identity to coexist with a replacement intent. Include up to `message.MaxTokenSize` bytes per stored token. Count the slot's reserved control space plus any current excess; do not charge the same allocation twice. Use checked arithmetic. Fixed-size slot/map overhead is bounded by maxSlots, not claimed as complete heap accounting.
- [x] **4. Implement deadline/selection semantics.** Expired entries are returned for cleanup before sends; ungated Continue precedes gated work; otherwise select the oldest eligible entry. An active-body gate supplies no artificial past deadline. Queue deadlines still include expiry. `clearPending` retains the slot's control reserve; `release` removes both atomically. Empty queues create no timer need for settled debt.
- [x] **5. Run green:** `rtk go test ./udp/client -run 'TestQBlock(Pending|Pacing)' -count=1`.
- [x] **6. Commit:** `feat(qblock): bound private pending transmission work`.

## Task 5: Route all client transmissions through pacing

**Files:** Modify `udp/client/qblock_client.go`, `conn.go`, `qblock_transmit.go`; create `qblock_pacing_client_test.go`; adapt directly affected helpers in `qblock_client_test.go`, `qblock_client_regression_test.go`, `qblock_scheduler_test.go`.

**Interfaces:** Consume Tasks 1–4. Add `workID qblockWorkID` to qblockExchange, with a monotonic exchange generation and per-body probe key. Produce:

```go
type qblockPreparation struct { Prepared, OwnsTransmission bool }
func (c *qblockClient) prepare(req *pool.Message, fail func(error)) (qblockPreparation, error)
func (c *qblockClient) syncPacingControlsLocked(id qblock.TransferID, now time.Time) error
func (c *qblockClient) executePendingOrdered(now time.Time) []qblockCallback
func (c *qblockClient) writePacedMessage(key qblockProbeKey, msg *pool.Message) error
func (c *qblockClient) acceptPacingFeedbackLocked(key qblockProbeKey) bool
```

`executePendingOrdered` requires actionMu, acquires mu only for state, and
executes at most one gated admission per invocation plus ready ungated controls.
`writePacedMessage` checks context, sizes, then rechecks context/ownership while
charging under mu immediately before I/O. It preserves attempted debt on error.
Body activation owns a complete ordered sender burst; control packets settle
individually. The ordinary executor can write further sets without readmission
while charging the matching active body if it is still unanswered.

Keep one bounded current-probe correlation record on the coordinator, containing
the probe key, work ID, generation, transfer ID, accepted token, and relevant
set/block numbers. Populate it only for an attempted packet. It survives
clearing a transmitted queue entry while its exchange is live, using that
slot's reserved control storage. Release/cancellation discards the correlation
record but preserves scalar gate debt; later traffic cannot clear that debt by
reusing its token. Expired/replaced gate ownership also clears the record.
Token equality alone is never the feedback join: Q1 missing blocks can arrive
with fresh tokens, and body/control replies may route through another still
owned token. Validate the operation and requested block/set before matching
the current probe, using the existing role-specific routing rules.
`syncPacingControlsLocked` operates on one transfer so a storage failure can
cancel that wire operation without failing unrelated work; timer turns scan
the bounded set of live receiver IDs.

- [x] **1. Write `TestQBlockPacedInitialGETHasSingleWriter`.** Use an existing manual-mode connection and qblockTestSession; arrange a 20-byte control debt at rate1 settled at t0. Call Do(GET) in a goroutine. At t0+19s, `require.Empty(t, session.writesSnapshot())`; at t0+20s, one initial GET appears with its original request token. A matching response completes Do once. Cancel a second queued GET before readiness: no write, no handler/token/work/callback reservation remains. Readiness with no response is bounded by request context or admission time plus Transfer.Lifetime, whichever is earlier.
- [x] **2. Write client flow regressions.** `TestQBlockPacedQ1PreparationAndHandoff` verifies queued Q1 manager reservations, one jitter call per body, no first burst before admission, and the same work slot through Q1→Q2. `TestQBlockPacedQ2ControlBatch` uses missing blocks 0 and 1, verifies one fresh token per actually attempted packet and commit only after packet two. `TestQBlockPacedFeedbackRequiresProgress` sends malformed, duplicate, old-generation, and unrelated responses and asserts unchanged gate ownership; an accepted matching fragment opens it.
- [x] **3. Run red:** `rtk go test ./udp/client -run 'TestQBlockPaced(InitialGET|Q1|Q2|Feedback)' -count=1`. Existing direct initial GET writing or immediate sender/control output must fail the behavioral tests before the routing changes.
- [x] **4. Implement client admission and transmission ownership.** Initialize the normalized gate and shared queue on qblockClient. Reserve the work slot/control bytes before publishing GET or Q1 state; on later failures unwind every earlier reservation. Change doInternal to skip its direct write when OwnsTransmission is true, including GET. Copy the queued description, never retain req. Use PrepareSender and enqueue activation for Q1; use StartReceiverDeferred for all Q2 creation/handoff. Sample jitter once and pass it to both prepared sender and wait calculation. Store immutable expiry for pre-receiver GET and keep the slot through handoff.
- [x] **5. Implement pending execution and feedback.** Synchronize owned pending controls after receive, commit, Tick, and release; use ReceiverProgress before/after receive plus accepted Deliver to distinguish progress. Preserve exact set/block matching, code/metadata validation, and terminal response routing before opening debt. Allocate control tokens/MIDs only after eligibility. A stale packet snapshot must be discarded before reservation/I/O. Add pending readiness/expiry to nextDeadlineLocked and call the same pending executor from ordinary ordered turns and advanceDueWithCallbacks, so automatic mode stays functional at this task boundary. Keep old core/control paths out of the private client after migration.
- [x] **6. Run green:** the focused command, then `rtk go test ./udp/client -run TestQBlock -count=1`. Update legacy timing fixtures only where the new policy changes their premise: inject a finite high rate or valid feedback explicitly; do not add a bypass mode or replace precise deadline assertions with loose sleeps. Commit: `feat(qblock): pace private client requests and recovery controls`.

## Task 6: Share the gate and work budgets with the server role

**Files:** Modify `udp/client/qblock_server.go`, `qblock_server_q1.go`, `qblock_server_q2.go`, `qblock_transmit.go`; create `qblock_pacing_server_test.go`; adapt affected `qblock_server_test.go` helpers.

**Interfaces:** Consume the shared coordinator and Task 5 executor. Add `workID qblockWorkID` and the current body probe key to qblockServerRecord. Produce:

```go
func (s *qblockServer) prepareResponseLocked(record *qblockServerRecord, code codes.Code, options message.Options, payload []byte, now time.Time, jitter float64) error
func (s *qblockServer) acceptPacingFeedbackLocked(record *qblockServerRecord, msg *pool.Message, progressed bool) bool
```

These helpers run with coordinator.mu held. prepareResponseLocked registers a
prepared Q2 sender and activation intent without writing. Feedback is called
only after existing full identity, token ownership, SZX, code, and block-control
validation succeeds; new accepted reply context is attached to its own output.

- [x] **1. Write `TestQBlockPacedServerSharesClientGate`.** On one Conn, send an unanswered client body, complete a server upload, and observe its queued response: no extra body admission, shared work/byte counts, and one handler call. Release the first owner's gate with validated feedback or its exact deadline; assert the server response uses the incoming request token and fresh MIDs. Count subsequent Q2 sets as part of the admitted body.
- [x] **2. Pin handler settlement and controls.** `TestQBlockPacedServerSettlesBeforeResponseAdmission` proves handlerRunning is false and retainUntil is fixed while activation waits; cancel/expire it and resend the upload without increasing handlerCalls. `TestQBlockPacedServerContinueBypassesDebt` verifies prompt 2.31 behind a closed gate, while `TestQBlockPacedServerMissingControlWaitsForPermit` holds NON 4.08 and its retry commit. `TestQBlockPacedServerRepairContextAndFeedback` interleaves two accepted repair tokens and rejects wrong identity/SZX; only the corresponding probe opens and each response keeps its intended token.
- [x] **3. Run red:** `rtk go test ./udp/client -run TestQBlockPacedServer -count=1`. Expect the existing immediate finishHandler StartSender/write path and unpaced controls to violate these assertions.
- [x] **4. Implement server admission and handoff.** Reserve the shared slot/control capacity before publishing the first accepted request; rejected/malformed first fragments roll back everything. Start Q1 receivers in deferred mode. At handler completion settle execution/retention first, cancel the old wire phase, and prepare Q2 using its incoming reply token. Preserve the work slot across this transition; preparation failure releases wire resources and keeps suppression. A suppression-only record releases its work slot, while a retained repairable Q2 sender keeps it. Never allocate a fresh response token.
- [x] **5. Integrate Q1 control and Q2 body writes.** Snapshot reply tokens with each control revision, send Continue ungated, and route 4.08 through the common gate. Accepted newly received missing blocks can acknowledge the current 4.08 probe; duplicates cannot. Q2 sender Complete settles initial body debt without releasing the repair representation. Final packet detection must distinguish initial transmission from repairs; use sender/output context rather than treating every M=0 repair packet as a new body completion.
- [x] **6. Run green:** focused server tests, then `rtk go test ./net/qblock ./udp/client -count=1 -timeout=180s`. Commit: `feat(qblock): share private pacing across UDP roles`.

## Task 7: Audit scheduler ordering, cancellation, and resource release

**Files:** Modify coordinator scheduler/lifecycle methods in `qblock_client.go`, runtime in `qblock_scheduler.go` only where needed, and server lifecycle helpers; create `qblock_pacing_lifecycle_test.go`; extend existing fake-clock/barrier helpers locally.

**Interfaces:** Consume the integrated pending executor and shared gate. Preserve existing `Tick`, `nextDeadlineLocked`, `advanceDueWithCallbacks`, `abandon`, and close entry points. Add:

```go
func (c *qblockClient) expirePendingWorkLocked(now time.Time) ([]qblock.Output, []qblockCallback)
func (c *qblockClient) releasePacingWorkLocked(id qblockWorkID, now time.Time)
```

expirePendingWorkLocked returns callbacks as well as outputs because an
unsent initial GET has no manager transfer to carry its terminal result.
releasePacingWorkLocked is idempotent, removes unsent work, and settles an
active body's attempted debt without clearing another owner's gate. Returned
terminal outputs/callbacks follow the existing ordered executor/dispatcher.

- [x] **1. Write `TestQBlockPacingSchedulerExpiryWinsTie`.** Schedule a prepared sender and gate at the same absolute deadline. Wake exactly there; assert no packet and one expiry result, with operation/token/body/work reservations released. Add an old timer wake after close and assert no rearm. `TestQBlockPacingSchedulerNoBusyLoop` holds a recovery intent past its old retry deadline and asserts the timer targets gate readiness or absolute expiry, with one due turn and no speculative output batches.
- [x] **2. Write lifecycle adversaries.** `TestQBlockPacingCancelPartialBatch` cancels between two Q2 control requests and verifies one attempted token, no second MID, no retry commit, and retained scalar debt. `TestQBlockPacingCloseInterruptsBothRoles` holds a context-aware write with both roles queued: stop is visible before drain, callback runs once outside locks, and every work reservation is released. `TestQBlockPacingCallbackReentersClose` verifies no self-join. `TestQBlockPacingLateCommitAfterReuse` delivers a stale revision/result after operation name reuse and verifies the replacement is unchanged.
- [x] **3. Run red:** `rtk go test ./udp/client -run TestQBlockPacing -count=1`. Force ordering with existing fake clock, session write channels, and actionMu contention barriers. If an adversarial test already passes, record that fact and add no speculative production change for it.
- [x] **4. Complete deadline and cancellation coverage.** Expire invalid work before manager progress or admission, resample automatic time after acquiring actionMu, then execute active outputs and ready pending work. Notify on every readiness-changing mutation, including accepted feedback and canceled unsent GET. Leave debt without waiters lazy. Check generation/context again just before charge/write. Close cancels contexts and invalidates queue before waiting for output; late completions cannot recreate slots or intents. Never await a rate deadline while holding either lock.
- [x] **5. Run green and race checks:** `rtk go test ./udp/client -run 'TestQBlock(Pacing|Paced)' -count=1`, then `rtk go test -race ./udp/client -run 'TestQBlock(Pacing|Paced)' -count=1 -timeout=180s`. Require clean completion and no race reports; inspect exact terminal counts and snapshots, not just absence of a hang.
- [x] **6. Commit:** `fix(qblock): complete paced scheduler lifecycle handling`.

## Task 8: Validate paired flows and record the slice's actual completion

**Files:** Create `udp/client/qblock_pacing_trace_test.go`; extend `qblock_server_lifecycle_test.go` only for reusable copied-wire/fake-time support; update this plan, the pacing spec status, `docs/superpowers/plans/2026-09-15-rfc9177-results.md`, and the Milestone 3 status in `2026-09-15-rfc9177.md`.

**Interfaces:** Reuse `pairedQBlockSession`, `pairedQBlockSnapshot`, `newFakeQBlockClock`, and copied packets. GET server initiation remains unsupported: for GET downloads drive the existing client through a deterministic scripted peer, not a newly implemented server handler route.

- [ ] **1. Write `TestQBlockPacingPairedPOSTPUTRepair`.** Run both methods with MaxPayloads=2, SZX16, 48-byte upload/response, rate=1024 bytes/s, and explicit wait=1s. Drop an upload block and a response block, advance fake clocks to the actual next deadlines, and assert complete bodies, correct tokens, one handler execution, and no direct Tick calls in automatic mode. Add a default-rate trace with sufficient operation lifetime to observe the actual long waits rather than bypassing the gate.
- [ ] **2. Write `TestQBlockPacingScriptedGETRepair` and `TestQBlockPacingBidirectionalSilenceExpires`.** GET verifies initial-request ownership and separately paced repairs. In the silence case both connections have active client and server wire phases; suppress feedback and advance to absolute expiry. Assert bounded termination, no late bursts, zero transfer/token/work counters after lifecycle cleanup, and no handler replay. Retention records may remain until their own specified deadline; advance to that deadline before asserting their count is zero.
- [ ] **3. Run targeted red/green:** `rtk go test ./udp/client -run 'TestQBlockPacing(Paired|Scripted|Bidirectional)' -count=1 -timeout=180s`. These are integration assertions over completed components; if already green, keep the new coverage and do not manufacture a failure. Fix only demonstrated integration gaps in the owning files and rerun their focused tests.
- [ ] **4. Run final verification:** `rtk go test ./net/qblock ./udp/client -count=1 -timeout=180s`; `rtk go test -race ./net/qblock ./udp/client -run 'Test(ManagerPrepared|ManagerDeferred|DeferredReceiver|QBlock)' -count=1 -timeout=180s`; then `rtk go test ./... -count=1 -timeout=180s`. Record the actual commands/results. Investigate assertion/race failures; record environmental failures with their evidence rather than treating them as passes. Do not repeat broad tests without a new change or unresolved failure.
- [ ] **5. Review the complete diff and update evidence.** Confirm all outbound private entry points use the gate policy, immediate core API behavior is preserved, and no public UDP configuration appeared. Record the conservative simultaneous-silence limitation and the remaining packet-sizing/full-memory/public-enablement work. Mark this slice complete only after all tasks/checks pass; do not mark all Milestone 3 complete. Leave the recovered private-adapter plan untouched.
- [ ] **6. Commit:** `test(qblock): validate paced bidirectional UDP lifecycle`.

## Coverage and handoff

| Spec requirement | Owning tasks |
| --- | --- |
| Prepared registration, rollback, original lifetime | 1, 5, 6 |
| Deferred retries, revisions, inbound progress during waits | 2, 5, 7 |
| Shared debt, cap distinction, feedback and encoded bytes | 3, 5, 6 |
| Bounded slots/intent bytes and protected control capacity | 4, 5, 6 |
| Initial GET ownership and server handler settlement | 5, 6 |
| Deadline integration, fairness, cancellation, stale wake/result safety | 4, 7 |
| Paired losses, silence policy, ordinary/core regressions | 8 |

Recommended execution: inline, sequential tasks, with review after the core
contracts (Task 2), complete role integration (Task 6), and final verification.
These boundaries are closely coupled; parallel implementation of dependent
contracts would add avoidable integration work. Task-specific implementer and
reviewer agents remain an alternative if selected by the user.

Status: Tasks 1–7 implemented; Task 8 remains open. Full-suite verification
is blocked by the separately reproduced host IPv6 UDP failure.
