# Private Q-Block GET size negotiation Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans inline with TDD and one final fresh-context review of this plan's range.

**Goal:** Advertise a locally fitting Q2 block size for private GET and reject an unacceptable first response before committing receiver offsets.

**Spec:** ../specs/2026-09-15-rfc9177-design.md (Body storage: decide SZX before payloads and handle negotiation explicitly); ../specs/2026-09-27-rfc9177-connection-pacing-design.md (prepared GET ownership, validated feedback).

**Architecture:** Reuse selectBodySZX with a synthetic response template containing maximum token/ETag, Content-Format and Size2, sized for MaxBodySize and later block-option growth. Save the advertised ceiling on the GET exchange. An initial response may choose a smaller SZX; a larger SZX or an actual datagram over the local limit is ignored like other invalid first fragments, without receiver admission or feedback. Stable metadata validation keeps subsequent offsets fixed. Unknown response options cannot be predicted, so the actual first-packet guard is necessary.

**Constraints:** Private udp/client only. Worktree feat/qblock-foundation at base 4ae63b9. Preserve both user-edited documents and .codanna. No public API, GET server initiation, Q1 receive renegotiation, Q1-to-Q2 response size advertisement, endpoint coordination, or complete memory claim. Keep the full-suite gate open; targeted host probe already failed with invalid srcAddr type <nil>. No broad rerun without new network evidence.

## Review Focus

- Synthetic response sizing includes maximum option growth through MaxBodySize, independently of GET request options.
- Failed GET preparation publishes no work/callback/context resources and writes nothing.
- Invalid initial responses neither create a receiver nor clear probing debt; later valid responses remain acceptable.
- A peer's smaller first SZX is accepted and used for subsequent controls; later SZX changes remain rejected.
- Actual first datagram size catches unanticipated response options; the sizing estimate is not a promise for all metadata.

## Task 1: Select GET advertisement before admission

**Files:** udp/client/qblock_packet.go, qblock_client.go, qblock_packet_test.go.

**Interfaces:** Add `func (c *qblockClient) selectGETSZX() (blockwise.SZX, error)`; add `getSZX blockwise.SZX` on qblockExchange. Consume selectBodySZX, datagramLimit, managerConfig.Transfer.MaxBodySize. Preserve prepare signature and queue ownership.

- [x] Add TestQBlockPacketGETSelectsSZXBeforeAdmission: MTU 68 advertises SZX32, 50 advertises SZX16, 49 rejects; separately oversized request options reject before publishing queue state. Assert no writes/resources on failure and transmitted Q2 values on success.
- [x] Run `rtk proxy go test ./udp/client -run '^TestQBlockPacketGETSelects' -count=1 -timeout=30s`. Expected: FAIL on oversized advertisement or admitted impossible request.
- [x] Implement response-template selection and actual initial request datagram validation before exchange resources. Store selected SZX on GET exchange; leave Q1 behavior unchanged.
- [x] Run the RED command. Expected: PASS.
- [x] Run `rtk proxy go test ./net/qblock ./udp/client -run '^(TestQBlock|TestManager|TestDeferred|TestDoInternalWithoutPrivateQBlockWritesOrdinaryGET|TestClassicBlock2WithoutPrivateQBlockDeliversNormalHandler|TestConnDelivers.*QBlock)' -count=1 -timeout=180s`. Expected: PASS.
- [x] Commit scoped implementation/tests and this plan: `feat(qblock): size private GET advertisements before admission`.

## Task 2: Validate first GET response negotiation

**Files:** udp/client/qblock_client.go, qblock_packet_test.go, roadmap/results.

**Interfaces:** Consume Task 1 getSZX; preserve invalid-first-fragment ignore policy and existing follow-on metadata validation.

- [ ] Add TestQBlockPacketGETRejectsUnacceptableFirstResponse for SZX above advertised ceiling and oversized options; assert zero manager receiver bytes/transfers, pending exchange intact and unchanged probing debt, then valid smaller response admits a receiver. Include equal/smaller first SZX and later changed SZX behavior using existing validation coverage.
- [ ] Run `rtk proxy go test ./udp/client -run '^TestQBlockPacketGETRejects' -count=1 -timeout=30s`. Expected: FAIL on unwanted receiver admission/feedback.
- [ ] Check actual first response size before payload snapshot and check decoded SZX before StartReceiverDeferred. Ignore unacceptable first packets without clearing work or feedback.
- [ ] Run focused command and Task 1 whole-task selection. Expected: PASS.
- [ ] Run whole-task selection with -race; `rtk proxy go test ./... -run '^$' -count=1 -timeout=180s`; `rtk proxy go vet ./udp/client ./net/qblock`; `rtk git diff --check`. Expected: PASS, repository command is compile-only.
- [ ] Update roadmap/results with exact scope, RED/GREEN and host probe evidence. Commit scoped files: `fix(qblock): enforce initial GET response size negotiation`.

## Completion

Pre-flight: Task 2 consumes Task 1's saved advertised ceiling; no shared-interface conflict. Self-review: this plan covers only GET first-response negotiation under the approved design; remaining incoming/Q1/handoff sizing is deliberately deferred. User explicitly authorized planning and inline execution without routine confirmation. Keep the scoped ledger for continuity. Review base 4ae63b9 through final HEAD once with gpt-6-astra/high, fix important findings in one TDD pass, preserve worktree/branch and do not merge or push.
