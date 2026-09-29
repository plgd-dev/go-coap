# RFC 9177 Transfer Machines Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build socket-independent Q-Block1 and Q-Block2 transfer machines that assemble one application body, recover bounded loss, and terminate predictably.

**Architecture:** Extend `net/qblock` with an engine driven by owned packet snapshots, explicit time, send results, and cancellation. The engine emits packet, delivery, completion, and release actions; it does no I/O and never holds pooled messages. The UDP/DTLS connection adapter belongs to milestone 3.

**Tech Stack:** Go 1.25, `message`, `net/blockwise`, the milestone 1 `net/qblock` types, standard library time and random source injection, testify tests.

**Spec:** [RFC 9177 implementation design](../specs/2026-09-15-rfc9177-design.md). This plan expands milestone 2 of [the delivery roadmap](2026-09-15-rfc9177.md).

## Global Constraints

- Support unicast NON Q-Block1 POST/PUT and Q-Block2 responses to GET/POST/PUT, including combined upload/download.
- Preserve the existing classic `BlockWise` path and do not add public `WithQBlock` configuration in this milestone.
- No incoming pooled message, option value, token, or payload may be retained after its callback; the engine only sees owned `Packet` values.
- Engine methods are serialized by their caller. They never call an application handler, socket, or timer callback.
- Failed or ambiguous Q payload exchanges terminate without classic replay.
- Defaults: MaxPayloads 10, NonTimeout 2s, NonReceiveTimeout 4s, NonMaxRetransmit 4. Use one jitter sample per body and fixed payload-set boundaries.
- Bound active transfers, retained bytes, tokens, completed-operation records, repair report size, and partial-operation lifetime. Account for contiguous assembly before allocating it.

## Review Focus

1. A late Q1 block with a new MID but the same operation identity must not invoke the application again; Task 2 tests the retained outcome.
2. A late Continue for set 0–9 must not advance a sender waiting on set 20; Task 3 tests exact set correlation.
3. A changed ETag must not append to a partial Q2 representation; Task 5 tests replacement and byte release.
4. A successful application write followed by a lost final response must not cause a second write or classic replay; Tasks 2 and 3 test uncertain completion.
5. Oversized or malformed missing-block reports must not allocate unbounded memory or kill a valid transfer; Tasks 4 and 6 test ignored reports and pacing.

## File map and interfaces

| File | Responsibility |
| --- | --- |
| `net/qblock/config.go` | Internal limits and timing validation; no public option wiring |
| `net/qblock/identity.go` | Canonical operation key and representation key |
| `net/qblock/engine.go` | Owned packets, action API, transfer registry, budget ledger |
| `net/qblock/transfer.go` | Common transfer state, deadlines, terminal cleanup |
| `net/qblock/qblock1.go` | Q1 receive/send transitions |
| `net/qblock/qblock2.go` | Q2 send/receive transitions |
| `net/qblock/timing.go` | Set pacing, backoff, retry and probing budget |
| Corresponding `*_test.go` files | Fake-time transition tests and ownership tests |

Fix these API types in Task 1, before implementing transitions:

```go
type TransferID uint64
type SendID uint64
type OperationKey string
type RepresentationKey string
func MakeOperationKey(packet Packet) (OperationKey, error)
func MakeRepresentationKey(operation OperationKey, etag []byte, present bool) RepresentationKey

type Config struct {
    SZX blockwise.SZX
    MaxPayloads uint32
    MaxBodyBytes uint32
    MaxAggregateBytes uint64
    MaxTransfers int
    MaxTokens int
    MaxCompleted int
    MaxReportBytes int
    NonTimeout time.Duration
    NonReceiveTimeout time.Duration
    NonMaxRetransmit int
    PartialTTL time.Duration
}
func DefaultConfig() Config

// Packet values returned by NewPacket own every byte slice, including each option value.
type Packet struct {
    Type message.Type
    Code codes.Code
    MessageID int32
    Token []byte
    Options message.Options
    Payload []byte
}
func NewPacket(m message.Message) Packet

type ActionKind uint8 // ActionSend, ActionDeliverRequest, ActionCompleteResponse, ActionTerminal
type Action struct {
    Kind ActionKind
    Transfer TransferID
    Send SendID
    Packet Packet
    Body []byte
    Err error
}

type Engine struct { /* private registry and budgets */ }
func NewEngine(cfg Config, jitter func() float64) (*Engine, error)
func (e *Engine) StartQ1(request Packet, body []byte, now time.Time) (TransferID, []Action, error)
func (e *Engine) StartQ2(response Packet, body []byte, operation OperationKey, now time.Time) (TransferID, []Action, error)
func (e *Engine) Receive(packet Packet, now time.Time) []Action
func (e *Engine) Sent(id SendID, sendErr error, now time.Time) []Action
func (e *Engine) Cancel(id TransferID, now time.Time) []Action
func (e *Engine) Advance(now time.Time) []Action
func (e *Engine) NextDeadline() (time.Time, bool)
```

`StartQ1` is the client upload entry; `StartQ2` is the server response entry. `Receive` also creates server Q1 receiver and client Q2 receiver transfers from validated packets. The adapter reports a send result for every `Send` action, even synchronous failures. Send actions are ordered but do not claim a datagram was delivered. `Terminal` is emitted once per transfer; it says complete, canceled, timed out, or uncertain after payload transmission. A terminal outcome owns no retained bytes or tokens. A Q1 terminal response that carries Q2 starts/links a Q2 receiver before completing the overall exchange.

`Config` must name `SZX`, `MaxPayloads`, `MaxBodyBytes`, `MaxAggregateBytes`, `MaxTransfers`, `MaxTokens`, `MaxCompleted`, `MaxReportBytes`, `NonTimeout`, `NonReceiveTimeout`, `NonMaxRetransmit`, and `PartialTTL`. `DefaultConfig()` returns SZX512, 10 payloads, a 1 MiB body cap, an 8 MiB aggregate cap, 32 active transfers, 64 tokens, 128 completed records, a 512-byte report cap, 2s NonTimeout, 4s NonReceiveTimeout, 4 retransmits, and 247s PartialTTL. `NewEngine` rejects zero/invalid explicit caps, SZX > 6, or `NonReceiveTimeout < NonTimeout`. Reject a start before consuming any budget if its body exceeds caps. Engine ownership is per connection/security session, so operation keys need no global peer identifier.

## State transitions

| Machine | State | Input | Required action and next state |
| --- | --- | --- | --- |
| Q1 receiver | absent | first valid block | Reserve budget, create sparse `Body`, record operation key, enter collecting |
| Q1 receiver | collecting | valid new/duplicate block | Add or ignore duplicate; report missing indices at set boundary; enter delivered only after exact assembly |
| Q1 receiver | delivered | new MID with same operation key | Return retained terminal outcome; never deliver application body again |
| Q1 sender | ready | start | Send first fixed set of at most MaxPayloads; enter waiting for matching Continue |
| Q1 sender | waiting | matching Continue | Repair holes in current set, then advance exactly one set; stale Continue changes nothing |
| Q1 sender | waiting | terminal response | Complete once, linking Q2 response if present; retain no payload after terminal action |
| Q2 sender | retained | initial send or repair request | Send requested bounded blocks, deduplicate overlapping repairs, preserve response identity |
| Q2 receiver | collecting | Q2 block with same operation/ETag | Add block; issue bounded missing requests; deliver one assembled response when complete |
| Q2 receiver | collecting | changed ETag | Release old partial body and start a new representation within the same aggregate budget |
| Any active | active | cancellation, exhausted retry, expiry, send failure | Emit one terminal result and release bytes/tokens/deadlines |

All transitions above take explicit `now`; tests use a deterministic jitter function. If an action send fails, the engine receives `Sent(id, err, now)` and terminates or retries only when that failure is known safe under the protocol. A timeout after payload transmission is an uncertain result, never a fallback request.

### Task 1: ownership, identities, and budgets

**Files:** Create `config.go`, `identity.go`, `engine.go`, `transfer.go`, `engine_test.go`, `identity_test.go`.

**Interfaces:** Produce the public types and methods above. `OperationKey` is a comparable internal representation of the request code plus length-prefixed operation options and the full ordered Request-Tag collection; `RepresentationKey` adds ETag presence and bytes. The engine copies these inputs. `Packet` options are cloned in order, preserving repeated tags and Q2 options.

- [ ] Write `TestPacketOwnership`, `TestOperationIdentity`, and `TestEngineBudgetAdmission`: mutate source slices after snapshot; distinguish absent/present ETag and one/two Request-Tags; reject a second transfer before any send when aggregate bytes or token cap is exhausted.
- [ ] Run `go test ./net/qblock -run 'PacketOwnership|OperationIdentity|EngineBudget' -count=1`; expect missing types/APIs.
- [ ] Implement only ownership, canonical keys, validated config, ID generation, budget reservation/release, and terminal-once bookkeeping. Include contiguous assembly allocation in the reservation before calling `Body.Assemble`.
- [ ] Run focused tests and `go test -race ./net/qblock -count=1`; require pass.
- [ ] Commit `feat(qblock): define owned engine events and bounded identities`.

### Task 2: Q1 receiver and completed outcome

**Files:** Create `qblock1.go`, `qblock1_test.go`; extend `engine.go` registry.

**Interfaces:** `Receive(Packet, now)` accepts validated Q1 fragments and returns `DeliverRequest` with an owned whole body once. Add `CompleteRequest(id TransferID, response Packet, now time.Time) []Action` to retain the application's outcome and serve late duplicate Q1 blocks without reinvoking it.

- [ ] Write `TestQ1ReceiveOutOfOrder`, `TestQ1DuplicateNewMID`, and `TestQ1OutcomeExpiry`: final block first, repeated block same/new MID, completed body delivered once, late duplicate gets retained outcome, record expires at PartialTTL.
- [ ] Run `go test ./net/qblock -run 'Q1Receive|Q1Duplicate|Q1Outcome' -count=1`; expect failures.
- [ ] Implement sparse intake using `Body`, operation identity from Task 1, set-boundary missing reports via `EncodeMissing`, and bounded completed-operation records. Reject size/SZX/identity mismatches before storing bytes.
- [ ] Run focused tests plus `go test -race ./net/qblock -count=1`; require pass.
- [ ] Commit `feat(qblock): receive Q-Block1 with one application delivery`.

### Task 3: Q1 sender and set correlation

**Files:** Extend `qblock1.go`, `qblock1_test.go`, `transfer.go`.

**Interfaces:** `StartQ1`, `Receive`, and `Sent` emit ordered `Send` actions. `SendID` correlates each result. Fixed set indices are `[k*MaxPayloads, min((k+1)*MaxPayloads, blockCount))` and never shift when repairs occur.

- [ ] Write `TestQ1TwentyOneBlocks`, `TestQ1StaleContinue`, `TestQ1RepairAcrossSets`, and `TestQ1UncertainFinal`: expect sets 0–9, 10–19, 20; stale 0–9 Continue cannot advance set 20; lost final response terminates uncertain without replay.
- [ ] Run `go test ./net/qblock -run 'Q1TwentyOne|Q1Stale|Q1Repair|Q1Uncertain' -count=1`; expect failures.
- [ ] Implement ordered NON sends, fresh logical send IDs, strict Continue correlation, repair priority within the current fixed set, and one terminal action. Do not assign network MIDs here; the adapter does that in milestone 3.
- [ ] Run focused tests plus `go test -race ./net/qblock -count=1`; require pass.
- [ ] Commit `feat(qblock): send fixed Q-Block1 payload sets`.

### Task 4: Q2 retained sender

**Files:** Create `qblock2.go`, `qblock2_test.go`.

**Interfaces:** `StartQ2` owns the complete response bytes and an immutable representation key. `Receive` recognizes bounded Q2 repair requests, including repeated Q2 options, without changing the retained representation.

- [ ] Write `TestQ2InitialSet`, `TestQ2OverlappingRepair`, `TestQ2InvalidMissingReport`, and `TestQ2Continuation`: literal expected block-number sequences; overlapping requests produce one send per requested block; malformed CBOR and out-of-range indices cause no allocation or terminal failure.
- [ ] Run `go test ./net/qblock -run 'Q2Initial|Q2Overlapping|Q2Invalid|Q2Continuation' -count=1`; expect failures.
- [ ] Implement retained response block selection with `EncodeBlock` and `DecodeMissing`; cap each repair action batch by MaxPayloads and MaxReportBytes. Keep ETag and content metadata identical on every emitted block.
- [ ] Run focused tests plus `go test -race ./net/qblock -count=1`; require pass.
- [ ] Commit `feat(qblock): retain Q-Block2 responses for bounded repair`.

### Task 5: Q2 receiver and combined exchange

**Files:** Extend `qblock2.go`, `qblock2_test.go`, `engine.go`.

**Interfaces:** `Receive` recognizes multiple Q2 responses for one operation and emits one `CompleteResponse` with the assembled owned body. A Q1 terminal response containing Q2 links to this receiver using the same operation identity.

- [ ] Write `TestQ2OutOfOrder`, `TestQ2ETagReplacement`, `TestQ2NoETagIdentity`, and `TestQ1ThenQ2`: verify one complete response; changed ETag drops old partial bytes; omitted ETag is distinct from a present one-byte ETag; combined POST upload/response completes once.
- [ ] Run `go test ./net/qblock -run 'Q2OutOfOrder|Q2ETag|Q2NoETag|Q1ThenQ2' -count=1`; expect failures.
- [ ] Implement receiver state using `Body`, representation keys, missing requests, and reservation changes before replacing a body. Terminal Q1 and Q2 actions must not each complete the same operation separately.
- [ ] Run focused tests plus `go test -race ./net/qblock -count=1`; require pass.
- [ ] Commit `feat(qblock): assemble Q-Block2 and combined exchanges`.

### Task 6: deadlines, backoff, and cleanup

**Files:** Create `timing.go`, `timing_test.go`; extend `engine.go` and transfer tests.

**Interfaces:** `Advance(now)` processes all due deadlines and returns actions; `NextDeadline()` returns the earliest remaining one. `Cancel` and terminal send failure release every byte, token, completed record, and deadline once.

- [ ] Write fake-time `TestInterSetJitterOnce`, `TestReceiveBackoff`, `TestLostControlTermination`, `TestProbeRateBudget`, `TestPartialExpiry`, and `TestSendFailureCleanup`. Include a 21-block trace, lost first/middle/final data, lost repair controls, and a concurrent-transfer budget check.
- [ ] Run `go test ./net/qblock -run 'Jitter|Backoff|LostControl|ProbeRate|PartialExpiry|SendFailure' -count=1`; expect failures.
- [ ] Implement RFC 9177 §7.2 timing: one sampled inter-set delay per body, NonTimeout and NonReceiveTimeout defaults and constraints, retry limit 4, receive backoff, stale control filtering, partial expiry, and probing-rate accounting. `Advance` emits actions only; no callbacks under locks.
- [ ] Run focused tests, `go test -race ./net/qblock -count=1`, and `go test ./net/qblock ./net/blockwise -count=1`; require pass.
- [ ] Commit `feat(qblock): bound Q-Block transfer timing and cleanup`.

## Milestone 2 handoff

- [ ] Run all `net/qblock` tests and fuzz seeds; format changed files and run configured golangci-lint v2 against changed code.
- [ ] Inspect a whole-branch diff for pool ownership, byte/token releases, and state-machine transitions not pinned by the named tests.
- [ ] Preserve the milestone 1 disabled request gate. Do not activate it or advertise Q transfers before milestone 3 adapter and milestone 4 capability work.
- [ ] Record any platform-wide socket-test failures separately from the socket-free engine suite.
