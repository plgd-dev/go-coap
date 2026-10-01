# Private Q-Block incoming datagram limits Implementation Plan

> Execute inline with superpowers:executing-plans and TDD; one final fresh-context review of this range.

**Goal:** Ignore oversized incoming private Q packets before fragment copying, engine mutation, or pacing feedback, across existing client/server phases.

**Spec:** ../specs/2026-09-15-rfc9177-design.md (wire validation before fragment acceptance, ownership); ../specs/2026-09-27-rfc9177-connection-pacing-design.md (validated feedback and complete datagram accounting).

**Architecture:** Generalize the initial-GET raw-length guard to cover private server Q requests and Q-option responses correlated with pending GETs or owned client transfers. Conn.Process checks original wire length after decoding and before routing; decoded size guards cover direct injected messages. Oversized packets are ignored, preserving existing operations and debt. Invalid packets do not negotiate a new SZX or trigger classic replay.

**Constraints:** Base ef426db on linked feat/qblock-foundation. Preserve two existing document edits and .codanna. Private udp/client only; no exported API, DTLS, public server configuration, SZX renegotiation, or complete memory claim. Full-suite gate remains open; existing host probe is current evidence from this ongoing continuation and no network change warrants another broad run.

## Review Focus

- Oversized follow-on Q2 leaves receiver progress, tokens, work, and debt unchanged; valid retry still completes.
- Oversized Q1-to-Q2 first response leaves the Q1 sender live until a valid handoff.
- Server Q1 and Q2 controls are ignored before accepting tokens or reinvoking handlers.
- Original raw length includes discarded malformed options; direct injected packets use decoded size without body reads.
- Ordinary traffic and disabled private server requests retain existing behavior; size rejection produces no response.

## Task 1: Guard decoded client and server packets

**Files:** udp/client/qblock_client.go, qblock_server.go, qblock_packet_test.go.

**Interfaces:** Reuse qblockIncomingSize and datagramLimit. No new engine/API contracts. Place validation before client Q-option routing and server action execution.

- [x] Add TestQBlockPacketIncomingClientLimits (follow-on GET and Q1-to-Q2 handoff): oversized option-bearing response leaves transfer identity/progress and probing state unchanged, then valid retry completes/changes phase.
- [x] Add TestQBlockPacketIncomingServerLimits: oversized initial and subsequent Q1 blocks create no state/progress; oversized Q2 control binds no token and writes nothing; valid retry works and handler count stays one.
- [x] Run `rtk proxy go test ./udp/client -run '^TestQBlockPacketIncoming' -count=1 -timeout=30s`. Expected: FAIL on state mutation by oversized packets.
- [x] Add decoded complete-size guard only to private Q-option paths before payload parsing/action execution; ignore errors/over-limit packets without teardown.
- [x] Run the RED command. Expected: PASS.
- [x] Run whole-task selection below. Expected: PASS. Commit scoped code/tests and this plan: `fix(qblock): reject oversized private incoming Q packets`.

## Task 2: Preserve original raw datagram limits across all private roles

**Files:** udp/client/qblock_packet.go, conn.go, qblock_packet_test.go, roadmap/results.

**Interfaces:** Replace oversizedInitialGET with `func (c *qblockClient) oversizedIncomingQ(msg *pool.Message, wireSize uint64) bool`; correlate client responses under mu, recognize private server Q requests. Consume Task 1 decoded guards.

- [x] Add TestQBlockPacketIncomingRawLimits: Process decodes oversized malformed Max-Age options that would otherwise be discarded, covering owned client Q2 and private server Q1/Q2; ignored packets do not reach monitor/admission, valid packets still do. Verify unowned ordinary traffic is unchanged.
- [x] Run `rtk proxy go test ./udp/client -run '^TestQBlockPacketIncomingRaw' -count=1 -timeout=30s`. Expected: FAIL on oversized raw packet reaching monitor.
- [x] Generalize raw guard by role/ownership; preserve original first-GET behavior and server-disabled behavior.
- [x] Run RED command plus whole-task normal and race selection. Expected: PASS.
- [x] Run `rtk proxy go test ./... -run '^$' -count=1 -timeout=180s`, `rtk proxy go vet ./udp/client ./net/qblock`, `rtk git diff --check`. Expected: PASS (repository check compile-only).
- [x] Update roadmap/results; commit scoped verified files: `fix(qblock): enforce raw incoming limits across private roles`.

## Verification and completion

Whole-task selection: `rtk proxy go test ./net/qblock ./udp/client -run '^(TestQBlock|TestManager|TestDeferred|TestDoInternalWithoutPrivateQBlockWritesOrdinaryGET|TestClassicBlock2WithoutPrivateQBlockDeliversNormalHandler|TestConnDelivers.*QBlock)' -count=1 -timeout=180s`; race uses the same command with -race.

Pre-flight: Task 2 consumes Task 1's decoded rejection policy; raw validation precedes decoder-discard consequences and direct calls remain covered. No conflicting interfaces. Self-review: this is packet acceptance enforcement only; Q1 receive negotiation and Q1-to-Q2 advertised ceiling remain deferred. User authorized bounded planning/inline execution without routine confirmation. Maintain scoped ledger, review ef426db..final HEAD once with Astra/high, fix Important/Critical in one TDD pass. Keep branch/worktree, no merge/push.

Verification qualification: whole focused normal/race selections passed, but repeated normal paired POST/PUT traces intermittently fail the existing readiness assertion. The same failure reproduced on an untouched ef426db archive. No speculative scheduler/test correction retained; this remains an open acceptance issue. Task completion means this scoped packet-limit behavior, not broad readiness.

## Final review fix pass

Fresh-context Astra/high review found one Important session-limit ordering gap.
RED proved the generic Process size error preempted ignore policy and would
close real sessions. GREEN fix scans raw header/options without decoded copies
before that error, then ignores correlated private Q traffic; ordinary oversized
traffic retains its error. Extended lengths and malformed reserved option
framing are covered. Final focused normal/race, repository compile-only, vet
and whitespace checks passed. No deferred Minor findings; acceptance blockers
remain as qualified above.
