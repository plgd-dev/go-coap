# Private Q-Block client snapshot memory Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans inline, as requested by the user. Follow checkbox steps and maintain the scoped execution ledger.

**Goal:** Reserve retained outgoing request snapshots separately from pending control copies and make pending backing storage match its byte charge.

**Architecture:** Keep the existing connection work queue and its MaxIntentBytes limit. Client work slots reserve immutable exchange options/token/tag plus their existing control capacity for the exchange lifetime. Pending copies use exact-length backing arrays; no public configuration or new budget subsystem is introduced.

**Tech Stack:** Go, existing udp/client private adapter, deterministic sessions, testify.

**Spec:** ../specs/2026-09-15-rfc9177-design.md (copied ownership and aggregate limits), ../specs/2026-09-27-rfc9177-connection-pacing-design.md (work-slot reservations).

## Global Constraints

- Work only in the linked feat/qblock-foundation worktree.
- Preserve the pre-existing private adapter plan and pacing spec edits without staging them.
- No incoming pooled message is retained past its callback by the Q engine.
- Failed or ambiguous Q payload exchanges never trigger classic replay.
- No public API, endpoint-wide coordination, DTLS, or complete-memory claim.
- Keep the host-blocked pacing Task 8 full-suite checkbox open; do not repeat socket suites without a network-related reason.

## Review Focus

- Snapshot/control copies coexist: both must be covered, including GET while waiting.
- Original token and Request-Tag backing must be copied and charged.
- Failed admission must leave no work slot or token/context/callback reservation.
- Clearing pending work must preserve the snapshot reservation until exchange teardown.
- Pending numbers/options/tokens must use backing lengths matching charges.

### Task 1: Reserve immutable client snapshots

**Files:** Modify udp/client/qblock_client.go and the existing exact option clone helper in qblock_server.go; extend udp/client/qblock_memory_test.go.

**Interfaces:** Consume qblockOptionBytes, qblockCheckedAdd, qblockControlCapacity, workQueue.reserve/release. Produce `qblockClientSnapshotCapacity(options message.Options, token message.Token, tag []byte, maxPayloads uint32) (uint64, error)` in qblock_memory.go. Generalize exact retained option copy to `cloneQBlockOptions(options message.Options) message.Options` and use for request snapshots.

- [ ] Write `TestQBlockMemoryClientSnapshotReservation` for GET and POST: provide only the previous control reservation as MaxIntentBytes, expect ErrLimitExceeded, no packet, no work slot; then increase budget to cover snapshot/control and expect admission followed by full release on abandonment. Assert copied request options remain independent after caller mutation.
- [ ] Run `rtk proxy go test ./udp/client -run '^TestQBlockMemoryClientSnapshot' -count=1 -timeout=30s`. Expected: FAIL on missing snapshot reservation.
- [ ] Implement checked snapshot-plus-control reservation: exact option element/value bytes plus original token and Request-Tag. Use it at GET and Q1 work-slot admission. Snapshot options copy exactly; preserve existing rollback and handoff slot lifetime.
- [ ] Run the focused command above. Expected: PASS.
- [ ] Run `rtk proxy go test ./net/qblock ./udp/client -run '^(TestQBlock|TestManager|TestDeferred|TestDoInternalWithoutPrivateQBlockWritesOrdinaryGET|TestClassicBlock2WithoutPrivateQBlockDeliversNormalHandler|TestConnDelivers.*QBlock)' -count=1 -timeout=180s`. Expected: PASS.
- [ ] Commit only task code/tests and this plan: `fix(qblock): reserve retained client request snapshots`.

### Task 2: Match pending storage to charged bytes

**Files:** Modify udp/client/qblock_pending.go and udp/client/qblock_memory_test.go.

**Interfaces:** Consume Task 1's exact option clone; preserve qblockCloneWork, qblockWorkBytes, replacement/order APIs.

- [ ] Write `TestQBlockMemoryPendingBackingMatchesCharge`: odd-length option/token/control number arrays retain capacity equal to copied length, source mutations do not affect stored work, replacement/release returns all extra bytes.
- [ ] Run `rtk proxy go test ./udp/client -run '^TestQBlockMemoryPendingBacking' -count=1 -timeout=30s`. Expected: FAIL on append/clone spare capacity.
- [ ] Replace append-based token/value/number copies with exact make/copy arrays; keep zero-value semantics and detached snapshots. Do not charge transient executor snapshots as retained queue state.
- [ ] Run the focused command above and Task 1's whole-task selection. Expected: PASS.
- [ ] Run the whole-task selection with `-race`; run `rtk proxy go test ./... -run '^$' -count=1 -timeout=180s`, `rtk proxy go vet ./udp/client ./net/qblock`, and `rtk git diff --check`. Expected: all PASS; repository check is compile-only.
- [ ] Update roadmap/results with exact limits and remaining transient/map/metadata accounting. Commit: `fix(qblock): match pending copies to memory charges`.

## Execution and completion

Task 1 precedes Task 2 because pending copies consume its exact option clone. Existing implementation commits through b48f357 are complete and must not be repeated. Execute both tasks with the scoped ledger; perform one final review of this addendum's range and address important findings through RED/GREEN. Keep branch and worktree available; no merge or push requested.
