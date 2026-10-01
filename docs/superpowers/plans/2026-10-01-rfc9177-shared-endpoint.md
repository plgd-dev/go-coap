# Shared Q endpoint congestion domain

> Execute inline with superpowers:executing-plans and TDD.

**Goal:** Share existing RFC9177 Q probing ownership across server connections
to the same normalized UDP peer, retaining debt across detach/reconnect.
**Spec:** docs/superpowers/specs/2026-10-01-rfc9177-milestone3-completion-design.md
**Base:** e512d16. Ordinary traffic and public construction follow separate plans.

## Task 1: Bounded endpoint state and ownership

Create udp/client/qblock_endpoint.go and same-package tests. Domain owns mutex,
clock/rate, bounded peer/member/FIFO candidate maps; member IDs distinguish
colliding connection probe keys. Normalize UDP IPv4 mapped addresses and IPv6
zones; reject invalid/multicast. No connection locks or callbacks under domain
lock; nonblocking wake channels may be signaled after unlock and never closed.
One current queued candidate per member; unchanged candidate preserves FIFO ticket.
Implement attach, admission, owns/kind snapshots, begin/end attempted writes,
settle, feedback, withdraw, detach, prune and close. Attempt completion must
settle debt from last completed write; cancellation/feedback while in flight
cannot admit a new owner until all attempts finish. Detached debt cannot be
evicted before expiry. Checked IDs/capacities and table-exhaustion rejection.

RED tests: colliding keys, FIFO, independent peers, mapped/zone normalization,
detach/reconnect debt, stale feedback/completion, close during active write,
zero-write cancellation, feedback wakeups and capacity exhaustion. Run tests
expect failure before implementation, implement, run expect PASS.

## Task 2: Q adapter integration

Keep local probeGate for existing standalone private fixtures; introduce a
private gate interface implemented by local gate and shared member, immutable
owns/kind queries replace direct production field reads. Work selection registers
oldest shared candidate even while busy; deadline eligibility must respect FIFO.
Scheduler listens to member wake channel. Admission/charge failure prevents write;
EndAttempt after every session write. Close detaches without clearing debt.
Configure domain membership via private connection construction option; rollback
on init error. Add two-connection adapter tests and reconnect/active-write tests.

Run focused normal/race, compile/vet/whitespace/full runtime. One fresh Astra
review of this completed range, one RED/GREEN material fix pass, ledger minors.
Update roadmap/results and commit scoped verified changes; preserve user docs.

## Review focus

FIFO starvation/open-gate spinning; stale completion after reconnect; active
write/close feedback ordering; detached table growth; scheduler lock inversion.
