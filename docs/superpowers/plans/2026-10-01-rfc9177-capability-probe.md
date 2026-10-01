# Milestone 4 explicit Q-Block capability probe Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans inline with TDD. Retain this plan's scoped ledger.

**Goal:** Provide an explicit, bounded client capability probe over the shared UDP/DTLS Conn without enabling outbound Q payload transfers.

**Architecture:** Conn.ProbeQBlock(ctx context.Context, path string) (bool, error) sends a safe CON GET requesting only block zero. A single bounded active probe owns its token/MID and intercepts only correlated responses before the disabled-Q gate and NON body engine. Reuse ordinary endpoint admission, NSTART and CON retransmissions; release all ownership at cancellation, closure or completion.

**Tech Stack:** Go 1.25, existing UDP/DTLS Conn, pooled messages, testify; no new dependencies.

**Spec:** docs/superpowers/specs/2026-09-15-rfc9177-design.md, capability/fallback section; RFC 9177 sections 4.1, 4.4, 4.6, and 7.1.

## Evidence and scope ruling

libcoap reference commit `c63c8f7cb7f248a4992539529b9e1b691962f29a`:
[coap_block_test_q_block](https://github.com/obgm/libcoap/blob/c63c8f7cb7f248a4992539529b9e1b691962f29a/src/coap_block.c#L2694)
sends CON GET /.well-known/core with QBlock2 NUM=0/M=0/SZX=0. RFC 9177 section 4.4 says unset request M asks for only that block; M=1/NUM=0 asks for the entire body. libcoap's probe response dispatch in
[coap_net.c](https://github.com/obgm/libcoap/blob/c63c8f7cb7f248a4992539529b9e1b691962f29a/src/coap_net.c#L4599)
detects QBlock2 and ends discovery without assembling the resource. A response M=1 is positive evidence, not a request to fetch more data. Source inspection is not runtime interoperability evidence.

This plan implements the wire probe only. It stores no capability cache, performs no coalescing or ordinary-request selection, and adds no general client Q configuration. A concurrent explicit probe returns ErrQBlockProbeInProgress. The call itself is explicit opt-in, usable on dialed UDP/DTLS clients and accepted connections. Server-only enablement does not establish outbound capability. The existing NON-only Q server still needs its own bounded CON single-block response slice; this plan does not claim that it can answer probes yet.

## Global Constraints

- Preserve feat/qblock-foundation and the linked worktree; no merge or push. Preserve both user-edited documents and .codanna.
- Empty path means /.well-known/core; a supplied absolute resource path replaces it. No payload, Observe, classic Block options or automatic follow-up requests.
- Request QBlock2 is exactly zero (NUM0/M0/SZX0); a reply is at most 16 payload bytes. Response M may be 0 or 1. No body engine or allocation based on announced Size2.
- Positive evidence requires Content, exactly one valid QBlock2 NUM0/SZX0, one valid ETag, and one Size2 consistent with payload length/M. ACK responses match MID and full token; separate CON/NON responses match the full token. Valid empty ACK merely stops retransmission; valid Reset is inconclusive failure.
- Bad Option or a Q-less 2.xx reply returns false,nil. Other errors, malformed Q replies, Reset, deadline/cancel and connection closure return false,error. No result is cached.
- One active probe per Conn, request and retransmission snapshot bounded by min(MTU, MaxMessageSize). Caller deadline may shorten the ExchangeLifetime upper bound. Join connection cancellation before NSTART/endpoint waits.
- ACK separate CON responses without starting body reception. Never send application payloads or replay anything through classic blockwise.

## Review Focus

- Wrong MID/token ACKs and malformed empty ACK/Reset must not stop retransmission or release endpoint ownership (Task 1).
- Separate response after empty ACK, or before lost ACK, must complete once and be acknowledged when CON (Tasks 1–2).
- Cancellation/close while waiting for NSTART or endpoint admission must release the single probe slot and all reservations (Tasks 1–2).
- Valid response with M=1 and very large Size2 must cause no further requests, receiver allocation or application delivery (Tasks 1–2).
- Token/MID collision and configured datagram overflow must fail without replacing other requests or sending a truncated probe (Tasks 1–2).

### Task 1: Explicit client probe and strict ingress ownership

**Files:** Create udp/client/qblock_probe.go, udp/client/qblock_probe_test.go. Modify udp/client/conn.go.

**Interfaces:** Produce Conn.ProbeQBlock(context.Context,string)(bool,error), ErrQBlockProbeInProgress, private qblockCapabilityProbe, and Conn.handleQBlockProbe(*pool.Message,[]byte)bool. Consume claimToken/releaseToken, acquireOutstandingInteraction, acquireOrdinary/writeOrdinary, midElement and CheckExpirations. Probe ingress consumes/releases only its own packets before normal response dispatch; the original datagram permits validation without discarded options.

- [x] Step 1: Write TestQBlockCapabilityProbeWireAndResponse using real Conn.Process and literal encoded packet expectations: CON GET, /.well-known/core, QBlock2 zero, empty payload, no classic blocks. Positive M0 and M1 with ETag/Size2 must return true and no manager transfer. Add table cases for BadOption, Q-less success, malformed/duplicate/mixed options, incorrect block, ETag/Size2 inconsistency, Reset and timeout; unknown/error cases must not become positive.
- [x] Step 2: Run `rtk proxy go test ./udp/client -run TestQBlockCapabilityProbe -count=1 -timeout=30s`. Expected RED: ProbeQBlock missing, then assertions fail until implemented.
- [x] Step 3: Implement the explicit bounded probe, token/MID reservations, normal CON retransmission snapshot and Process ingress interception. Validate full token/MID before feedback. Empty ACK stops retransmission only; separate responses complete the single-block probe. Keep the slot until deferred cleanup and release snapshots exactly once.
- [x] Step 4: Run the focused command; add strict correlation and separate-response regression cases, observe RED for uncovered behavior and GREEN after each fix. Run focused race. Expected PASS.
- [x] Step 5: Commit scoped code/tests/plan as `feat(qblock): add explicit single-block CON capability probe`.

### Task 2: Lifecycle, transport and scope acceptance

**Files:** udp/client/qblock_probe_test.go; udp/client/qblock_probe_lifecycle_test.go; udp/qblock_probe_test.go; dtls/qblock_test.go; roadmap/results and foundation design capability section.

**Interfaces:** Consume Task 1 ProbeQBlock. Public calls use existing UDP/DTLS construction without Q payload runtime. Test peers return independently constructed responses; no libcoap execution/interop claim.

- [x] Step 1: Add TestQBlockCapabilityProbeLifecycle covering occupied slot, cancellation followed by another successful attempt, session close during NSTART/endpoint waits, request token/MID collisions, send failure, retransmission expiry and request size rejection. Pin state cleanup and preserve unrelated ownership. TestQBlockCapabilityProbeOwnedBudget must reject before sending when the existing Q owned budget is full and release the envelope after completion; retain the envelope through concurrent ingress. Observe RED for any missing behavior and implement minimal fixes.
- [x] Step 2: Add real UDP and PSK DTLS probe tests with a small raw response handler returning QBlock2 zero/eight, ETag and Size2. The large representation M1 case returns one 16-byte block, and the client emits no follow-up. Exercise classic enabled/disabled construction and path override. Validate request fields independently in the peer handler. Run focused normal/race, expected PASS.
- [x] Step 3: Run `rtk proxy go test ./udp/client ./udp ./dtls -run 'TestQBlockCapabilityProbe' -count=1 -timeout=60s`, focused Q race across UDP/DTLS, unfiltered net/qblock race, compile-only ./..., vet ./..., full runtime ./... count1 timeout180 and git diff --check. Expected PASS.
- [x] Step 4: Update roadmap/results and capability design with the RFC/libcoap-derived wire contract and completed probe-only scope. Cache/coalescing, public client config/modes, no-replay selection matrix, dialed Q payload wiring and server CON single-block response remain open. Commit as `test(qblock): verify explicit probe transport and lifecycle boundaries`.

## Completion

One fresh-context Astra/high review of 2cc5001..completed plan HEAD. Regrade findings, one TDD fix pass for material findings, ledger deferred minors and rulings. Retain workspace/ledger and recheck preserved SHA1s. No completed Milestone 3 re-review or unrelated cleanup.

Review completed once for `2cc5001..f88c812`: four Important findings, all accepted
and reproduced in the single fix pass. Strict raw control/metadata validation,
ordinary duplicate ACK caching, idempotent acknowledgment-time NSTART release,
and identity-checked MID removal address them. The fixed owned-memory floor
covers empty ACK cache storage beyond probe completion. No new minors were
raised; previous deferred minors remain retained. Final verification and the
fix commit are recorded in the scoped ledger.

## Self-review

The wire choice is grounded in RFC 9177 and pinned libcoap source, correcting the prior unimplemented M1/small-only proposal. Task 1 owns the probe API/ingress; Task 2 consumes it for lifecycle and real transport checks. Review focus maps to tests. Deferred capability caching and selection are explicitly outside this independently usable probe primitive; no incomplete config is exposed.
