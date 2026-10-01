# RFC 9177 Milestone 5 Remaining Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete supported NON fault coverage, real UDP/PSK-DTLS relay checks, and pinned bidirectional independent-peer interoperability evidence.

**Architecture:** Extend the existing independently classified `qblocklink` and paired fake-clock harness. Keep timing assertions in fake time and transport checks bounded by contexts. Audit libcoap captures using an independent raw-wire parser; two-go-coap-endpoint results establish integration, not independent interoperability.

**Tech Stack:** Go 1.25, testify, UDP, Pion PSK-DTLS, existing `qblocklink`, Python standard library, libcoap `c63c8f7cb7f248a4992539529b9e1b691962f29a`.

**Spec:** `docs/superpowers/specs/2026-09-15-rfc9177-design.md`, Milestone 5 in `docs/superpowers/plans/2026-09-15-rfc9177.md`, RFC 9177 sections 4.4/7/10 and Figures 2–15. Baseline `bb3cf29`; latest results supersede stale historical statuses.

## Global Constraints

- NON Q-Block1 POST/PUT and Q-Block2 GET, client and server; include Q-Block2 response bodies to POST/PUT.
- Failed or ambiguous Q payload exchanges never trigger classic replay.
- Observe, FETCH/PATCH/iPATCH, multicast, TCP/TLS, BERT and OSCORE remain deferred. Figure adaptations remove Observe and replace FETCH with POST/PUT explicitly.
- Preserve `feat/qblock-foundation`, both user-edited documents, `.codanna/`, and every existing ledger. No merge/push; stage only task-owned files.
- No new production dependency. Compatibility fixes require behavioral RED before GREEN.
- Reuse existing timing/lifecycle/budget tests. A new test is required only when an actual behavior or ingress boundary lacks coverage.
- Finite detached traces include script/configuration and remain available on failure. Tests use temporary directories by default; `QBLOCK_TRACE_DIR` explicitly persists artifacts.

## Review Focus

- Repeated Q2 selectors and M1 suffix overlap must not amplify responses or mutate ownership on rejection (Task 1).
- Entire initial-data loss and one-way silence terminate without application replay; sender/receiver storage releases at bounded fake deadlines (Task 2).
- Hold/release trace order must agree with actual deliveries, and duplicate final requests do not reinvoke handlers (Task 2).
- DTLS classification occurs on authenticated plaintext at the accepted-connection seam; resources close on failures as well as success (Task 3).
- Independent acceptance requires exact bodies and Q/NON wire evidence, pinned build/version/commands, and failure on classic payload fallback (Task 4).

---

### Task 1: Supported Q2 repair compatibility and empty peer tokens

**Files:** `udp/client/qblock_server_q2.go`, `qblock_server_get.go`, `qblock_server_q2_selection_test.go`, the stale-Continue fixture in `qblock_server_test.go`, `udp/client/conn.go`, `net/qblock/manager.go`, `manager_sender.go`, `manager_empty_q2_test.go`.

**Interfaces:** `serverQ2Control` preserves singleton initial GET; `serverQ2Controls(msg, limit)` parses increasing fixed-SZX selectors; `serverQ2Selection(blocks, metadata, maxPayloads)` produces one bounded `qblock.Control` for atomic `Manager.ControlWithToken` acceptance. Singleton boundary M1 remains Continue; non-boundary M1 repairs its set tail. Repeated selections merge overlap once; NUM0/M1 denotes the whole body. Requests whose expansion exceeds the existing MaxPayloads repair capacity or includes unsent blocks reject atomically.

- [x] `TestQBlockServerQ2Selectors`, `...CrossSets`, `...RejectAtomically`: suffix 2–9/MAX_PAYLOADS10, repeated M0, overlap, mixed-selector cross-set pacing, stable payload/ETag/Size2/token, malformed order/SZX/bounds, expansion cap and unchanged ownership/deadline after rejection.
- [x] Observe behavioral RED, implement normalization, correct prior NUM1/M1 stale-Continue fixture to actual boundary NUM10/M1. Self-review adds RED/GREEN for whole-body NUM0 expansion exceeding capacity.
- [x] Independent libcoap discovery found valid empty-token NON GET rejected. `TestManagerEmptyQ2*`, `TestQBlockServerQ2EmptyToken` and `TestQBlockEmptyTokenReservationAndFreshAllocation` cover retained response, repair, collision/reuse/caps, handler once and wrong-owner release. Allow empty token only for Q2 sender ownership; locally generated exchange/control tokens remain nonempty and Q1 receiver rules stay unchanged.
- [x] Focused normal/race and unfiltered core normal pass; exact logs `q2-*-red.log`, `q2-final-normal-after-review.log`, `q2-final-race-after-review.log` retained in the new ledger. Parent includes the final source in the combined acceptance/review/commit gate.

### Task 2: Deterministic fault gaps and the RFC coverage matrix

**Files:** `udp/client/qblock_pacing_trace_test.go`, `qblock_milestone5_fault_test.go`, `internal/test/qblocklink/link_test.go`; coverage artifact `.superpowers/sdd/2026-10-01-rfc9177-milestone5-remaining/coverage-matrix.md`.

**Interfaces:** `runQBlockPacingPairedGeometry` consumes method/pacing/lifetime/relay/body-size/reorder/MaxPayloads; reuse `pairedQBlockSession`, `newFakeQBlockClock`, `advancePairedQBlockClock` and `Link.Process/Release/Trace`. Distinct bytes per block detect incorrect offsets/assembly. Existing no-loss GET/upload/combined tests and exact retry/backoff tests remain the evidence for their established behavior.

- [x] Add `TestQBlockMilestone5CrossSetAndReorder`: POST/PUT five-block loss across sets and actual hold/release reordered delivery; complete bodies and handler once.
- [x] Add `TestQBlockMilestone5DefaultSetGeometry`: 13 blocks, MAX_PAYLOADS10, actual lost block numbers 1/9/10, Q2 response loss and successful combined recovery. This pins literal RFC set geometry alongside scaled cases.
- [x] Add `TestQBlockMilestone5AllInitialLoss`: all five initial Q1 datagrams dropped, sender expires exactly once with no replay or retained ownership. No receiver receives these packets; do not describe this as two-endpoint handler execution evidence.
- [x] Add `TestQBlockMilestone5AsymmetricResponseLoss`: accepted upload, response loss, retained duplicate suppression and eventual server release. Existing receiver retry-exhaustion tests supply exact backoff/limit evidence.
- [x] Add `TestQBlockMilestone5MalformedControlWire` and `...RepresentationWire`: raw decode preserves malformed CBOR/Q lengths and changed ETag; original exchange fails once and releases without mixing bodies.
- [x] Cover the relay parser's valid two-byte extension fixture. Preserve prior recovery-control loss and duplicate-delivery mutation checks, and complete trace experiment metadata.
- [x] Run combined deterministic normal/race and reconcile coverage. `TestQBlockMilestone5LostRepairResponse` drops initial Q2 block0 and its first repair (Q2 occurrences1/4), observes at least two repair requests and completes; final controller race passes. This closes the exact second-loss detail in Figures9/15.

### Task 3: Real public UDP and authenticated PSK-DTLS faults

**Files:** `internal/test/qblocktransport/{conn.go,conn_test.go,udp.go,workflow.go}`, `udp/qblock_relay_test.go`, `dtls/qblock_relay_test.go`.

**Interfaces:** `Controller` serializes the relay and arms fault scripts after public probing; detached snapshots distinguish probe and fault events. UDP forwarding uses real front/back udp4 sockets. DTLS `PlaintextConn` wraps the accepted Pion connection through the existing listener seam, preserving `HandshakeContext`; faults act after decryption on Read and before encryption on Write. This is a real authenticated session with plaintext fault injection, not a second terminating DTLS bridge.

- [x] `TestQBlockUDPCombinedFaults` and `TestQBlockDTLSCombinedFaults`, each GET/POST/PUT, exercise explicit discovery, Require payloads, data loss/duplication and repair through public operation paths. Assert full bodies, handler once, actual script effects and repair controls.
- [x] Configure SZX16, MTU128, MaxPayloads2, NonTimeout100ms, NonReceiveTimeout1150ms, Lifetime30s, ProbingRate65536 and NonProbingWait10ms. Bound operation to12s and cleanup to2s; register cleanup as each resource is acquired.
- [x] Verify negotiated PSK cipher, detached trace storage and worker shutdown. Socket normal/race plus four helper tests pass; logs and traces remain in `.superpowers/sdd/2026-10-01-rfc9177-milestone5-socket/`.

### Task 4: Pinned bidirectional independent peer

**Files:** `tests/interop/qblock/{run.sh,run-dtls.sh,README.md,harness.py,wire.py,test_wire.py,libcoap-server.c}`, `fixture/main.go`.

**Interfaces:** Go fixture client/server use public APIs and explicit Probe before Require payload. Python harness manages bounded subprocesses and UDP capture/fault forwarding. Independent `wire.py` parses CoAP types/options/blocks and audits Request-Tag, ETag/Size, token correlation, body hash and method/status. `run.sh` validates the exact source/binaries and fails on absent prerequisites or unsuccessful cases.

- [x] Build pinned source out-of-tree and preserve actual flags/compiler/version. Intended CMake options: Q_BLOCK/EXAMPLES ON; DOCS/TESTS/DTLS/TCP/WS/OSCORE OFF; static Debug. Record accepted option names from the actual cache rather than assuming spellings.
- [x] Run parser literal fixtures and negative audit tests that reject corrupted/classic traces.
- [x] Go client and libcoap client both exercise GET/POST/PUT with1500-byte bodies,24blocks at64-byte SZX, including combined echo uploads/responses. Both directions add a UDP GET drop of Q2 block3 followed by explicit repair. Preserve actual application resource names and commands in the final artifacts.
- [x] Require expected body/hash, codes/methods, explicit Q discovery, NON Q payloads, Q1 Request-Tag/Size1, Q2 stable ETag/Size2, exact process exits and wire artifact per scenario. Include deterministic block loss/recovery in each direction and preserve failures that reveal compatibility defects.
- [x] Final independent matrix has UDP8 PASS with independent raw plaintext audit, and PSK-DTLS6 PASS using OpenSSL/Pion AES128-GCM-SHA256 with body equality, encrypted captures and independent libcoap plaintext diagnostics. No independent raw plaintext audit or injected-loss claim applies to DTLS. Both shipped libcoap discovery and `/example_data` omit required Size2 on capability replies; `libcoap-server.c` supplies correct application metadata using public APIs while unchanged pinned libcoap owns protocol behavior. This is independent engine evidence, not shipped-example compatibility. Exact evidence is `.superpowers/sdd/2026-10-01-rfc9177-milestone5-interop/RESULTS.md` and `evidence/{udp-final,dtls-final}/results.json`.

### Task 5: Combined acceptance, final review and commits

**Files:** This plan, coverage matrix, roadmap and results; new retained ledger.

- [x] Reconcile each matrix row to actual test/trace and bounded status. Original Observe/FETCH figures remain deferred; supported NON adaptations are explicit.
- [x] Controller reports current affected-package race, compile-only/vet, fresh full runtime `integrated-full.log` and final independent script/audit pass. Full runtime preceded the final test-only repaired-response-loss addition; `controller-final-race.log` validates that addition. Preserve this exact evidence boundary.
- [x] Obtain final whole-branch Astra/high review under user model routing. Address material findings with focused regressions, rerun affected gates and retain disposition.
- [x] Mark Milestone 5 complete only after the supported matrix, real UDP/PSK-DTLS faults, independent UDP directions and final race gate pass. Any blocked required gate remains explicit.
- [x] Commit only reviewed task-owned files and verify user-edit hashes and all preserved artifacts. Milestone 6 release examples/documentation/performance remains separate. No merge/push.

Final Milestone5 acceptance: one fresh whole-branch Astra/high review ofc864548 against origin/master merge-base defc9c6 found0Critical,2Important and2Minor. Both Important findings are fixed in one regression pass, commit2f46bf3: ordinary NON application4xx/5xx responses preserve bounded status/body and complete GET/POST/PUT without replay; enabled Q1 missing Request-Tag/Size1 returns ordinary4.00 before admission. GET ownership cleanup, duplicate suppression, MTU/No-Response, held-write close/expiry and temporary MID ownership are covered. Invalid Q-bearing response rejection remains intact. No rereview or post-fix reviewer approval is claimed.

Fresh final production full runtime, affected-package race, unfiltered relay-oracle race, repository compile/vet and diff checks pass (postfix-*.log in remaining ledger). This full run includes the final LostRepairResponse test and production fixes. Final pinned-peer reruns at2f46bf3705e4707162c48ca3f02085e52eeaf5ac pass UDP8/8 and PSK-DTLS6/6. Commit6c59a78 records exact selected source provenance and exact UDP response-status gates;9Python tests pass. Both runs preserve432input source snapshots, dependencies/toolchain, scoped dirty source diff and binary hashes; source fingerprint eb786bff3e5d0bfd09953f4a3cc56620aa474999ba67be224979c62bb56f1a9c is unchanged through build/run. Earlier successful and failed evidence is retained.

Milestone5 is complete for the supported NON scope and documented figure adaptations. Source-provenance Minor addressed; audit Minor partially addressed by exact UDP statuses, with stronger DTLS diagnostic predicates deferred. DTLS evidence remains encrypted capture plus independent diagnostics; custom libcoap application metadata and shipped-example limitations remain as above. Prior deferred supplied-DTLS fixture cleanup and exhaustive race ACK fields remain. Historical trace metadata limitations are retained; new paired artifacts contain configuration/fake timing. Milestone6 release examples/documentation/performance remains open; release acceptance is separate from this bounded controller acceptance. Branch/worktree/index ownership and all ledgers remain retained; no merge/push.
