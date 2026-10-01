# Ordinary UDP traffic in the endpoint congestion domain

> Execute inline with executing-plans and TDD.

**Spec:** docs/superpowers/specs/2026-10-01-rfc9177-milestone3-completion-design.md
**Goal:** Q and ordinary traffic to the same peer share one unanswered owner.

## Task 1: Bounded context-cancelable ordinary permits

Create qblock_ordinary.go and tests. Each ordinary unit attaches a bounded domain
member with stable wake channel and unique identity. Borrow caller message while
waiting; do not clone/register timer until admitted. Call reader TryToReplaceLoop
before blocking; wake/deadline/context/connection cancellation select. Bound permit
maps through domain member cap, retain exact token/MID correlation until feedback,
expiry or teardown. Ordinary NON settles after write but remains correlated until
debt expires; CON retains active permit across retransmits. ACK/Reset and immediate
responses bypass admission; Q writes use their existing gate directly.

RED tests: Q blocks ordinary initial send without cloned MID registration;
context cancellation cleans waiter/member; ordinary NON blocks Q, matching
response releases and wrong token does not; close during write retains debt.
Implement minimum, run focused normal/race expect PASS.

## Task 2: Send, retransmit and feedback hooks

Integrate writeMessage/writeMessageAsync/AsyncPing before prepareWriteMessage.
Store permit on midElement; cleanup idempotently settles/detaches. Retransmits
charge same permit and never reacquire. Validated ACK/Reset only release matching
MID; parsed exact-token response releases before classic blockwise handling.
Ordinary response routing must not consume MID ownership on unrelated request.
Send failures, cancellation and timeout clean ownership. Disabled domain preserves
existing behavior. Tests real write attempts, same owner across retransmit,
invalid MID feedback and handler outbound reentrancy.

Run focused normal/race, compile/vet/whitespace/runtime suite; one fresh Astra
review and one RED/GREEN fix pass. Update roadmap/results, scoped commit.

## Review focus

Admission before clone/timer; retransmit/ACK concurrency; response before first
write returns; cancellation cleanup and debt; classic blockwise feedback timing.
