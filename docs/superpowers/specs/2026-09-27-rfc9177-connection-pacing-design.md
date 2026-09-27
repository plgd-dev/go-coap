# Private Q-Block connection pacing and probing

Status: proposed design for written review, 2026-09-27. The scope and shared
coordinator architecture have been approved in conversation; implementation has
not started.

Predecessor: [connection deadline scheduler](2026-09-22-rfc9177-connection-deadline-scheduler-design.md).
Roadmap: [RFC 9177 implementation plan](../plans/2026-09-15-rfc9177.md), Milestone 3.
Source baseline: `9a7e038` on `feat/qblock-foundation`.

## Outcome and scope

Extend the private bidirectional UDP adapter with one connection-owned pacing
coordinator. Client requests, server response bodies, and recovery controls use
shared admission and accounting. Delayed work is bounded, cancelable, and driven
by the existing clock and deadline scheduler. An operation must not consume its
retry budget while its recovery request is waiting to be transmitted.

Construction and tests remain in `udp/client`; this introduces no exported
transport option or `udp/server` construction hook. Packet/MTU selection,
complete connection-memory accounting, public configuration, capability
discovery, DTLS, and additional request methods remain later slices. Existing
private POST/PUT uploads, their responses, and client GET downloads define the
supported traffic. This slice does not add FETCH support.

The accounting boundary is the existing `Conn`. Combining several connections
to the same endpoint, and coordinating Q traffic with ordinary CoAP traffic,
must be resolved before claiming endpoint-wide congestion-control compliance
at public enablement. Completing this design alone does not finish Milestone 3.

## Protocol basis and implementation policy

[RFC 9177 section 7.2](https://www.rfc-editor.org/rfc/rfc9177.html#section-7.2)
applies PROBING_RATE to an unanswered body as a unit. Its inter-body wait is
capped by NON_PROBING_WAIT. NON 4.08 responses and NON GET/FETCH requests with
Q-Block2 are individually subject to the probing rate. Block-set delays and
the Continue exception also remain applicable to body transmission and repair.
[RFC 7252 sections 4.7–4.8](https://www.rfc-editor.org/rfc/rfc7252.html#section-4.7)
define the unanswered-peer rate and its default of one byte per second.

The following admission algorithm is a conservative local policy, not an
algorithm prescribed by either RFC. It deliberately favors bounded state and
deterministic behavior over maximum concurrency with a silent peer.

Use one shared probing gate with three states: open, transmitting an unanswered
body, or waiting until a deadline. An open gate admits the oldest eligible new
body or rate-limited control packet. Admission reserves the gate atomically
before any write. An already-admitted body's subsequent sets use the sender's
existing timing; they do not repeatedly enter this gate.

For an unanswered body, accumulate the encoded bytes attempted for its initial
transmission. Hold admission of another probing unit until valid feedback
arrives or that transmission ends. Without feedback, settle the gate at the
last write's completion time with a delay of
`min(ceil(bytes / ProbingRate), NonProbingWait)`. Cancellation or failure after
partial transmission settles the same way using attempted bytes. Cancellation
before any write releases the reservation immediately.

For an individual rate-limited control, settle the gate after its write with
`ceil(encodedBytes / ProbingRate)`. The body-specific wait cap does not shorten
this control deadline. Q2 recovery currently expands a missing-block action
into several requests; each resulting datagram needs its own admission.
As a conservative local policy, all supported Q2 recovery/continuation request
methods use this control path, including POST/PUT.

Q1 2.31 Continue responses do not acquire the probing gate. They remain
serialized with packet output and can run while probing work is waiting, so
this policy does not delay the normal acknowledgment of a received set.

A matching, accepted peer response opens the gate early. Feedback must refer
to the current gate owner and follow at least one attempted write: an accepted
Q1 Continue, repair report, or terminal response; a validated Q2 continuation
or repair request for the retained sender; a matching initial GET response;
or newly received blocks named by the pending 4.08/Q2 recovery control.
Rejected, duplicate/no-progress, stale,
or unrelated messages do not release another operation's debt. Correlation
includes the operation generation and relevant block/set, not just a token.
Feedback marks that body answered for this transmission, so its remaining sets
do not recreate an unanswered-body reservation. Later repair traffic remains
subject to the existing sender timing and control validation.

When a gate owner finishes, its scalar debt can outlive the manager operation.
Releasing operation storage must not reset the gate. A late response cannot
clear a newer owner's reservation. No unused credit accumulates during idle
time, and a late wake does not admit a burst of overdue probing units.

This policy can delay recovery for one operation behind an unrelated silent
outbound body. Absolute operation deadlines still win, so queued operations
can expire without transmission, including during simultaneous bidirectional
loss. This is an explicit throughput/liveness tradeoff of the initial policy;
the tests must show bounded termination and cleanup for that case. A future
policy permitting additional concurrent probes needs separate congestion and
accounting justification.

## Byte accounting and private parameters

Charge encoded CoAP datagram bytes, including header, token, options, payload
marker, and payload. Reuse the UDP encoder's size calculation; do not estimate
from Size1/Size2 alone. This measures UDP payload bytes and makes no claim to
include IP, UDP, or future DTLS overhead. Choosing an SZX that fits a packet is
still the later packet-sizing task.

Build at most the packet being considered for transmission, compute its size,
and release temporary pooled storage before parking. Queue entries contain
owned intent data or references to retained manager state, not pooled messages
or encoded copies of an entire body. Commit attempted byte cost immediately
before calling the session writer. A pre-write validation/encoding failure
charges nothing; a write error retains the attempted charge because delivery
may be uncertain. Never retry an ambiguous write automatically.

Add private pacing configuration with a positive integer byte rate, optional
explicit positive NON_PROBING_WAIT, and a bounded intent-storage byte limit.
The default rate is one byte per second. When the wait is omitted, derive it
from the transfer settings using the RFC formula, ACK_RANDOM_FACTOR 1.5 and
MAX_LATENCY 100 seconds. Reuse the sender's single jitter sample for this
calculation; do not sample a second body delay. An explicit wait has no jitter.
Validate overflow at the maximum permitted sample. Rate division rounds up,
and duration/deadline arithmetic must not wrap. There is no unlimited-rate
sentinel; tests needing short waits supply explicit valid parameters.

## Prepared bodies and delayed controls

The manager currently starts a sender and emits its first burst in one call.
Split registration from activation through an additive, transport-free core
contract. Preparing a sender validates and reserves its operation, initial
token, transfer slot, and retained body bytes atomically, but emits no packet.
Activating it once emits the first set under the normal progress/execution
gate. Preserve `StartSender` as the immediate prepare-and-activate convenience
operation for existing core users.

A prepared sender contributes only its absolute expiry to manager deadlines.
Its lifetime starts at preparation; activation does not restart it. Continue
or repair for an unactivated sender cannot advance it or add token ownership.
Canceled or expired preparation releases the normal manager reservations.
This avoids a second, unaccounted queue of response/request bodies.

Receiver control generation also needs an explicit deferred-transmission
contract. In that mode, a generated control is an intent with a monotonic
revision. Creating an intent does not consume a retry or start the next retry
timer. The adapter acknowledges successful completion of the current control
batch with its revision and actual completion time. Only that acknowledgment
commits the associated continuation boundary or recovery attempt and arms the
next retry. Repeated or obsolete acknowledgments are harmless no-ops.

Preserve existing immediate receiver behavior for existing core callers via
the synchronous convenience path. The private adapter opts into deferred
controls explicitly. Both paths share the same validation and transition
logic. The core accepts explicit times and results; it owns no socket, clock,
goroutine, or rate policy. These additions to `net/qblock` are Go API additions
and must be reviewed as such, even though the transport adapter stays private.

While an intent is pending, suppress only its send-dependent retry deadline.
Continue accepting valid inbound fragments, terminal events, cancellation,
and absolute expiry. New progress invalidates stale intent revisions and
recomputes the needed control; duplicates and malformed fragments do not.
Completion discards remaining controls before delivery/release. Never freeze
the entire manager or queue unbounded inbound events behind a delayed send.

A recovery batch may require multiple Q2 datagrams. Track a bounded cursor;
commit the retry only after the batch's final successful write. Release the
execution gate between separately paced datagrams. Incoming progress can
invalidate the remainder in between. A partial-batch write failure cancels
the operation through the existing failure path. Already sent request tokens
remain owned until normal operation cleanup; unsent packets have no new token
or MID reservation.

## Bounded coordinator ownership

Reserve one private outbound-work slot for each admitted logical client
exchange or live server wire exchange, shared across both roles and capped at
`ManagerConfig.MaxTransfers`. A slot follows Q1/Q2 phase handoff. All transfer
records still obey the manager's independent transfer/token/body limits. A
client GET reserves its slot before a response has created a receiver. A
settled server record that retains only duplicate suppression releases its
work slot while keeping its separately bounded record slot.

Each work slot holds at most one pending intent batch: body activation,
initial GET, or receiver control. Its control list is bounded by MaxPayloads
plus a continuation entry. Replacing a stale batch reuses the slot. Count all
newly retained copied intent bytes, including request options, against one
shared private queue byte budget, defaulting to the manager's retained-byte
limit. Manager-owned body bytes are referenced and remain charged there.
This bounds the storage introduced here; it does not finish accounting for
all pre-existing adapter copies, messages, or handler allocations.

Reserve slot and byte capacity before publishing work. On any admission error,
roll back new operation, token, MID, callback-slot, intent-byte, and body state.
Control storage is reserved for an admitted exchange's bounded control needs
so that filling the activation queue cannot displace an existing receiver's
recovery intent. Derive that reservation from its copied request options,
the maximum token length, and the MaxPayloads-bounded control list; reject
admission if the shared byte budget cannot cover it. If a later required
intent exceeds its reserved byte capacity,
fail that wire operation locally with a limit error and release its resources.
No overload path may execute a completed server request's handler again.

Each intent records operation/transfer identity, exchange or server generation,
control revision, and immutable reply context. Allocate fresh outgoing request
tokens when their packets become writable, except the initial token already
reserved by prepared-sender admission. Server responses continue to use the
appropriate accepted incoming request token; every emitted NON gets a fresh
MID. A later repair request cannot overwrite another pending reply's context.

## Connection paths and scheduling

Route the initial private GET through the coordinator too. Today `prepare`
only changes the request and `Conn.doInternal` writes it directly; leaving
that path would bypass pacing. Give the private preparation result an explicit
meaning that the adapter owns transmission, and let `doInternal` wait for its
normal response/error channels. Retain a copied request description across a
delay, never the caller's pooled message. Ordinary request paths retain their
existing behavior.

For server responses, settle application execution and duplicate retention at
handler completion, then prepare the Q2 sender and enqueue activation. A delay
does not keep the handler marked running or extend retention. Failed/expired
activation preserves the completed-request suppression record. Successful Q2
completion continues to retain the representation for repairs.

The scheduler's next deadline becomes the minimum of effective manager
deadlines, eligible server retention deadlines, pending-work expiry, and the
gate deadline when work is waiting. An active unanswered body is advanced by
its normal sender deadline. A deferred receiver contributes its absolute
expiry, not an already elapsed retry deadline. Settled debt without waiting
work can be expired lazily on the next admission.

Each due turn first handles expiry and invalidation, then advances due active
transfers and ungated Q1 Continue responses, then admits eligible pending work.
An ungated pending Continue contributes an immediately eligible deadline even
when the probing gate is closed. Use stable enqueue order across
roles; an invalidated control replacement retains its owner's queue position,
while the next separately paced datagram joins the tail. Every candidate
rechecks ownership, revision, context, expiry, and gate eligibility under the
same serialized turn. Expiry wins ties with readiness. Late timer events do
not replay missed sends or reserve future tokens.

Keep `actionMu` then `mu` ordering. Mutate under `mu`, release it for I/O, and
finish the actual burst before another sender progress event. Waiting for a
rate deadline holds neither lock. Rate-limited controls return to the queue;
an admitted sender burst stays a single ordered execution turn. Notifications
recompute authoritative state after preparation, feedback, intent replacement,
write settlement, cancellation, and release.

The timer owner and due worker remain the only scheduler runtime. No new timer
or goroutine per intent is introduced. Manual Tick drives the same operations
with the injected clock. Callbacks remain outside locks and outside the
scheduler workers, using the existing bounded dispatch rules.

## Cancellation and failure

Cancellation and close invalidate intents and cancel write contexts before
waiting for output quiescence. Expiration remains absolute while work waits.
Every failure releases each slot, byte reservation, and pooled packet once.
Callbacks report a terminal result at most once, including failure of an
initial GET that never reached the wire.

Per-operation cancellation preserves any settled probing debt for attempted
traffic; it cannot be used to obtain a fresh burst immediately. Connection
close discards the coordinator and all pending work. Late writes, timer hints,
handler results, and control acknowledgments cannot republish an intent.
Completed server duplicate suppression and retained Q2 representation
lifetimes keep their existing semantics.

## Deterministic acceptance tests

Use fake time, the real manager, and copied fake-session packets. Tests assert
packet times and state ownership; wall-clock timeouts only detect deadlocks.

1. A queued sender emits nothing before activation, counts against shared
   manager limits, and expires without activation. Duplicate activation and
   malformed admission cannot leak operations, tokens, or retained bytes.
2. Client and server bodies compete for the same gate. A silent first body
   delays the second through the exact byte-rate/cap boundary. Feedback from
   the owner releases it; unrelated, rejected, and obsolete feedback does not.
3. Body sets retain their existing spacing and Continue behavior. Rate waiting
   never spaces every block of an already admitted body at the probing rate.
4. Initial GET, Q2 continuation/recovery datagrams, and NON 4.08 traverse the
   shared control path. A multi-packet missing report needs separate permits;
   its rate deadline is not shortened by the body wait cap.
5. Delay a recovery request beyond several old retry intervals. No retry is
   consumed until its batch completes. Inbound progress replaces it, full
   delivery removes it, and absolute expiry still releases everything.
6. Cancel between Q2 control packets. Unsent packets own no token/MID, sent
   tokens are cleaned up once, and a stale completion cannot change a new
   generation. Exercise immutable server reply context across repairs.
7. Compare byte charges with encoded datagram lengths, including options and
   empty payloads. Check rounding, derivation/jitter, large values, overflow,
   pre-write errors, ambiguous write failures, and partial-body cancellation.
8. Fill shared work and intent-byte capacity with both roles. Verify atomic
   rollback, reserved control capacity, release after cancellation, and that
   suppression prevents handler replay after a queued response fails.
9. Expiry and rate readiness coincide; expiry wins. Early, repeated, stale,
   and late wakes neither spin nor create catch-up bursts. No work is lost
   when feedback opens the gate while the timer owner is recomputing it.
10. Block a write, queue work for both roles, then cancel/close. Check prompt
    context cancellation, bounded scheduler state, one terminal callback, and
    no deadlock or post-close writes, including reentrant callbacks.
11. Paired GET and POST/PUT loss-and-repair traces complete with automatic
    scheduling when feedback arrives within lifetime. Simultaneous silent
    bidirectional transfers demonstrate the documented bounded expiry policy.
12. Existing immediate core APIs, disabled-Q behavior, ordinary Do/Observe,
    classic blockwise, scheduler ownership, and duplicate suppression regressions
    continue to pass. Run the affected tests under the race detector.

## Implementation-plan handoff

After written review, turn the design into ordered TDD tasks: prepared-sender
registration; deferred receiver-control commits; private gate arithmetic and
feedback ownership; bounded intent admission and all outbound-path integration;
scheduler/close integration; paired traces and regression checks. Core contract
changes precede adapter queueing because emitting actions now and delaying
their writes with the current retry behavior is unsafe.

Expected code is a focused private `udp/client/qblock_pacing.go` with tests,
additive deferred contracts in `net/qblock`, and targeted changes to the client,
server, scheduler, and `Conn.doInternal`. Packet sizing, complete memory
accounting, and public enablement each retain their own subsequent review.
