# RFC 9177 server CON single-block replies Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans inline with TDD. Steps use checkbox syntax.

**Goal:** Answer single-block CON QBlock2 GETs with standards-compliant piggyback/separate replies.

**Architecture:** A private server record table owns exact request identity and bounded frozen replies. It shares the runtime action gate, scheduler, endpoint admission and owned budget; it never creates a body sender.

**Tech Stack:** Go, existing UDP/DTLS clients, qblock scheduler and endpoint admission.

**Spec:** docs/superpowers/specs/2026-10-01-rfc9177-server-con-probe-design.md

## Global Constraints

Keep feat/qblock-foundation/worktree/ledgers; no main checkout, merge/push. Never stage the two preserved user docs or .codanna. ExchangeLifetime247s from admission; shared MaxRecords/MaxMetadataBytes/MaxBodySize/MTU. CON single-block GET only; no full CON Q sender. Existing session capability/selection and NON transfers stay intact. Exactly one fresh Astra/high review of baseline834270e through the implementation HEAD, one material TDD fix pass.

## Review Focus

- Timer completion race must choose exactly one ACK form.
- Expired running handlers must retain admitted capacity without publishing late replies.
- ACK/Reset must release only its owned local MID and endpoint permit.
- Handler cursor and supplied ETag must survive single-block extraction.
- Suppressed/directly mutated responses must never leak Content.

### Task 1: bounded single-block response and duplicate identity

**Files:** Create udp/client/qblock_server_con.go and udp/client/qblock_server_con_test.go; modify udp/client/conn.go, qblock_server.go, qblock_server_q1.go, qblock_server_get.go, qblock_memory.go.

**Interfaces:** Produces (*qblockServer).handleCONRequest(*pool.Message) bool, (*qblockServer).nextCONDeadlineLocked() (time.Time,bool), (*qblockServer).expireCONLocked(time.Time), (*qblockServer).closeCONLocked(), (*qblockServer).recordCountLocked() int. qblockServerCONRecord owns request MID/token/options/control/context/cancel/lease/expiry/frozen ACK and callback state. All Locked methods require client.mu. handleCONRequest runs handler outside actionMu/client.mu. Consumes qblockOwnedLease, qblockDatagramSize and canonical metadata validation.

- [ ] Write TestQBlockServerCONSingleBlock: body lengths0/4/40 → exactly one ACK Content; QBlock2 literals0/0/8, Size2 exact, payload≤16, valid ETag; no manager sender. TestQBlockServerCONOffsetAndETag: NUM1/SZX0 extracts bytes16–31 and preserves supplied ETag/cursor. TestQBlockServerCONDuplicates: same MID/identity gets same ACK with one handler call; changed token/path dropped. TestQBlockServerCONLimits: MaxRecords shared with NON, oversized body/metadata/datagram bounded error and no positive Q result. TestQBlockServerCONNoResponse: suppressed SetResponse and direct mutation yield tokenless empty ACK.
- [ ] RED: `rtk proxy go test ./udp/client -run '^TestQBlockServerCON' -count=1 -timeout=60s` → FAIL missing CON replies, not compile errors.
- [ ] Implement interfaces, bounded streaming extraction, exact snapshot and admission/expiry/close; response interception precedes ordinary cache. Preserve local source metadata and response cursor.
- [ ] GREEN same command → PASS; `rtk proxy go test ./udp/client -run 'TestQBlock(Server|Owned|Memory)' -count=1 -timeout=90s` → PASS.
- [ ] Commit only task files/spec/plan: `rtk proxy git commit -m 'feat(qblock): answer bounded CON single-block requests'` after exact-path git add and diff --check.

### Task 2: delayed ACK and separate CON lifecycle

**Files:** Modify udp/client/qblock_server_con.go, qblock_server_con_test.go, qblock_client.go, qblock_memory.go, conn.go; create udp/client/qblock_server_con_lifecycle_test.go.

**Interfaces:** Produces (*qblockServer).handleCONFeedback(*pool.Message) bool and (*qblockServer).dueCON(time.Time) []qblockCallback. Server table conByMID maps local response MID to record; reserveMIDLocked accounts for it. Separate record owns permit, response wire, responseMID, retry deadline/interval/count, sent/completed state. ACK delay and retry deadlines participate in nextRecordDeadlineLocked; due callbacks run through existing scheduled dispatcher. Feedback routing before generic special-message handling.

- [ ] Write TestQBlockServerCONDelayedSeparate: blocked handler, advance ACK deadline → empty ACK; release handler → CON Content with new MID/original token; duplicate request → empty ACK only. TestQBlockServerCONRetransmit: deterministic jitter0, timer intervals2/4/8 seconds, same frozen bytes/MID; ACK stops retries/releases permit. TestQBlockServerCONResetAndExhaustion: Reset terminates; MaxRetransmit exhausted waits final timeout then terminates. TestQBlockServerCONExpiryAndClose: blocked handler/write retains budget until return; no late reply; bounded admissions; closure clears local MID and permit. TestQBlockServerCONDeadlineRace: completion versus timer cannot emit both piggyback and separate forms.
- [ ] RED: `rtk proxy go test ./udp/client -run '^TestQBlockServerCON(Delayed|Retransmit|Reset|Expiry|Deadline)' -count=1 -timeout=60s` → FAIL missing empty ACK/separate tracking.
- [ ] Implement shared deadline callbacks and serialized ACK decision, separate endpoint admission/fresh MID/exponential retries, feedback/expiry/close ownership. Snapshot before writes, retain callback/write lease, errors outside locks.
- [ ] GREEN same command → PASS; `rtk proxy go test -race ./udp/client -run 'TestQBlockServerCON|TestQBlockScheduler|TestQBlockServer' -count=1 -timeout=120s` → PASS.
- [ ] Commit exact task files: `rtk proxy git commit -m 'feat(qblock): deliver delayed single-block CON responses reliably'`.

### Task 3: public UDP/DTLS integration and verification

**Files:** Create udp/qblock_con_probe_test.go, dtls/qblock_con_probe_test.go; update docs/superpowers/plans/2026-09-15-rfc9177-results.md and roadmap2026-09-15-rfc9177.md.

**Interfaces:** Existing udp.Dial/dtls.Dial, options.WithQBlockServer/WithQBlock, Conn.ProbeQBlock/Do. No new public API.

- [ ] Write TestQBlockServerCONProbeUDP and TestQBlockServerCONProbeDTLS with real loopback endpoints: explicit probe to application resource establishes support; Require GET and body POST finish once; probe sends no continuation. Delayed handler variant succeeds through separate response. Use127.0.0.1/PSK and deterministic synchronization, not localhost DNS.
- [ ] RED/GREEN: integration should first expose any missing transport behavior; if already green document characterization of task1/2 integration (no production change required). `rtk proxy go test ./udp ./dtls -run 'TestQBlockServerCONProbe' -count=1 -timeout=120s` → PASS.
- [ ] Run focused normal/race UDP/client/DTLS/options; `rtk proxy go test -race ./net/qblock -count=1`; `rtk proxy go test ./... -run '^$'`; `rtk proxy go vet ./...`; `rtk proxy go test ./... -count=1 -timeout=180s`. Retain exact logs and host blockers; loopback escalation is authorized by task, no routine confirmation.
- [ ] Record bounded completion, remaining M4 coverage and interop limits; commit only tests/results/roadmap.
- [ ] One fresh-context reviewer gpt-6-astra/high fork none checks834270e..HEAD against spec/plan/ledger. Re-grade and ledger findings; one RED/GREEN material fix pass, defer minors; final tests before scoped fix commit. No second review.

## Self-review

Coverage maps spec sections to tasks1–3. Names/signatures above align with existing client/server types. Review focus each has a named test. No public API partial exposure, invented full-CON body behavior or historical review reused. User authorized implementation inline without routine confirmation; proceed after self-review. Retain all ledgers despite skill cleanup default.
