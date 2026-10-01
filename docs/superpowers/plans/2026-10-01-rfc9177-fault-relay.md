# RFC 9177 deterministic fault relay

Authorized for autonomous inline execution by the resume request. Baseline407e5ab45f0bac54f39c4835420ad772d2d4b8c4.

## Design and boundary

First bounded Milestone5 slice: a socket-free scriptable datagram relay in internal/test/qblocklink. No endpoint integration or protocol runtime changes. Callers serialize events (including fake-time deliveries); no wall clock, goroutines or sockets. Later UDP and decrypted DTLS adapters consume outputs with bounded contexts. This does not establish independent interoperability or full Milestone5 acceptance.

Raw header/option parsing classifies malformed, ordinary, Q1, Q2, Continue, missing-block response, empty ACK and Reset. Preserve malformed option values rather than use the production decoder. Direction and kind identify an occurrence counter; occurrence is one-based, rules specify a single occurrence, unmatched packets pass. Reject duplicate selectors and invalid directions/actions/limits at construction. Continue/missing controls take precedence over Q data. Structurally malformed or illegal Q lengths/SZX classify malformed; repeated Q2 requests remain Q2. Script copies are immutable.

Actions: pass/drop/hold/duplicate. Hold returns no output; Release IDs in caller-specified order implements reorder and bypasses rules/counters. Reject unknown/duplicate release IDs atomically. Each input and release appends a chronological event, including exact detached wire bytes and original input ID on release. Duplicate emits two independently owned copies. Trace returns detached snapshots. MaxEvents/MaxBytes bound retained trace bytes, including release events; rejects do not consume IDs/counters/holds. Held storage references its original trace entry. The finite relay is never silently truncated. No packet mutation or random decisions.

## Files and tasks

### Task1: relay contract, raw classification and scripted delivery

Create internal/test/qblocklink/link.go, link_test.go and README.md. New API: New(rules []Rule, limits Limits) (*Link,error), Process(Direction,[]byte) ([]Packet,error), Release(ids ...uint64) ([]Packet,error), Trace() []Event. Types Direction, Kind, Action, Rule, Limits, Packet and Event described in package docs. Link is caller-serialized.

- [ ] Write literal-wire tests for direction/occurrence isolation, drop/duplicate, hold/reorder, raw controls/malformed Q, caller/output/trace mutation isolation, constructor validation, atomic failed release and finite limits. Tests catch wrong packet selection, corruption and silent loss of fault evidence.
- [ ] Supply compilable API stub and run normal test: expected behavioral assertion failures (not missing symbols). Retain red.log.
- [ ] Implement minimal relay; run normal/race count1; expected exit0. Retain green.log/race.log.
- [ ] Commit implementation/tests/plan/README after diff check.

### Task2: roadmap reconciliation, review and final evidence

- [ ] Append current M4 acceptance boundary to roadmap/results; retain historical entries. M4 named deliverables/acceptance covered through407e5ab with two deferred test minors, historical full runtime bounded to1cf57d3 and no whole-branch approval. Link latest evidence and new relay slice.
- [ ] One fresh Astra/high reviewer evaluates only407e5ab..new HEAD plus uncommitted current documentation against this contract. Review focus: independent classification, atomic limit/release failure, occurrence isolation, immutable storage, trace reproducibility and scope claims. Not final whole-branch acceptance. Address material findings in one TDD pass; retain minors, no rereview.
- [ ] Final normal/race relay; compile-only ./...; vet ./...; diff check; retain logs with commands/exits. Commit scoped final docs/fixes. No full runtime solely for breadth.

## Rulings

User authorization overrides design confirmation gates. Keep existing linked worktree, all sibling/new ignored ledgers/logs and both preserved user edits; no force-add, merge or push. Review only this new bounded range; final whole-branch acceptance remains open. Socket-free slice requires no host-loopback probe; prior host evidence remains retained.
