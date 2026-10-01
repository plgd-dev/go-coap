# Paired automatic scheduler acceptance

> Execute inline with executing-plans and TDD.

**Spec:** approved connection pacing design and M3 completion addendum.
**Goal:** Remove reproduced fake-clock event-pump race and prove M3 runtime gate.

Current diagnostic evidence: paired trace advances stale far-future timer while
worker/handler has not yet published near-term response-set deadline. Same failure
reproduces on baseline; clocks can jump hundreds of seconds and expire valid work.
Keep production behavior unchanged unless a production defect is proven.

## Task 1: Synchronize test clock progression with executor work

Inspect automatic scheduler ownership and fake clock. Introduce test-only event
barrier/state inspection that waits for worker and handler/executor settlement
before consuming a published timer deadline. Never use arbitrary sleeps/longer
Eventually or count passes as a fix. Keep existing packet loss/repair/token/MID
assertions. RED repeat50/100 original trace; deterministic test/barrier regression
then minimum event-pump change GREEN repeated normal/race. Use one shared time
base so advancing one side cannot skip the other's work; fake clock must handle
all timers when multiple members exist. No test helper fields in production.

## Task 2: Final acceptance

Focused normal/race, repository compile/vet/whitespace and full runtime suite.
One fresh whole-branch Astra review, one RED/GREEN material fix pass, scoped
ledger/report and commits. Mark M3 complete only after concrete roadmap criteria
and runtime gate satisfied. Milestones4–6 remain open; preserve branch/worktree.
