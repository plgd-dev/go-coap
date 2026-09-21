# RFC 9177 Operation Manager Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a deterministic, bounded manager that correlates Q-Block transfer machines by operation and token, without introducing UDP socket integration.

**Architecture:** `net/qblock.Manager` owns operation records, token ownership, aggregate retained-byte accounting, and the earliest deadline. It accepts validated, normalized fragments and control events and wraps the existing sender/receiver actions in `Output` records. `udp/client` remains responsible for decoding options, establishing an operation key, allocating packet tokens/MIDs, serializing manager calls, writing actions, and returning send failures through `Cancel`.

**Tech Stack:** Go 1.25.0, `net/qblock`, existing `message.Token` only at the manager boundary, standard-library synchronization-free collections. No socket, goroutine, wall-clock, or new dependency.

**Spec:** [RFC 9177 implementation design](../specs/2026-09-15-rfc9177-design.md), [transfer-machines plan](2026-09-15-rfc9177-transfers.md), and [RFC 9177 §§4–7](https://www.rfc-editor.org/rfc/rfc9177.html#section-4).

## Global constraints

- This manager is a deterministic core. Its caller serializes every method call; `Manager` has no mutex and makes no network calls.
- A `Fragment` or `Control` has already passed `ValidateOptions`, semantic required-field checks, and operation-key construction in the future adapter. It never retains a pool message or a caller-owned token/payload/identity byte slice.
- Every `Start*`, `Receive`, `Control`, `Tick`, and `Cancel` returns owned `Output` values; all contained bytes are copies.
- Token ownership is exact, per connection/security context. A token can bind to only one active transfer. Invalid, unknown, expired, and cross-operation events return errors without creating or changing state.
- A body has one fixed identity and metadata. Q1 uses an adapter-provided request key that includes the complete Request-Tag collection and operation options; Q2 uses an initiating-operation key plus ETag. `OperationKey` values must be copied and compared byte-for-byte.
- Aggregate limits cover active operations, bound tokens, and sparse retained body/source bytes. The manager reserves `Metadata.Size` before creating a transfer and releases it exactly once; an emitted assembled delivery payload is produced from that reservation and is not additionally reserved. It does not claim to budget adapter-owned wire messages.
- A completed Q1 receive operation retains only its bounded completed record and tokens needed to recognise duplicate payloads until its lifetime; application response replay remains the UDP adapter’s responsibility. A Q2 receive transfer releases immediately after `Deliver`, as the Part A receiver specifies.
- A sender’s completion and all close paths remove operation records, tokens, and reserved bytes. Late packets cannot recreate a released operation because creation requires explicit `StartReceiver` from a validated first fragment.
- Peer-wide PROBING_RATE and packet MTU sizing stay in the UDP adapter until its packet writer is available. The manager does cap requested missing numbers at `MaxPayloads` and preserves the action ordering returned by a transfer machine.

---

## Public types and file map

| File | Responsibility |
| --- | --- |
| `net/qblock/manager.go` | manager configuration, registries, start/receive/control/tick/cancel APIs |
| `net/qblock/identity.go` | immutable `OperationKey`, canonical byte-part encoding for adapter-built identities |
| `net/qblock/manager_test.go` | limits, routing, deadline, cleanup, output ownership |
| `net/qblock/identity_test.go` | key copying and unambiguous part encoding |
| `net/qblock/manager_trace_test.go` | sender/receiver/control integration with a fake clock |
| `docs/superpowers/specs/2026-09-15-rfc9177-design.md` | mark Part B manager scope as implemented only after Task 4 passes |
| `docs/superpowers/plans/2026-09-15-rfc9177-results.md` | record manager checks and remaining adapter work |

```go
type OperationKey string
func NewOperationKey(parts ...[]byte) (OperationKey, error)

type ManagerConfig struct {
    Transfer TransferConfig
    MaxTransfers uint32
    MaxTokens uint32
    MaxRetainedBytes uint64
}
func DefaultManagerConfig() ManagerConfig
func (c ManagerConfig) Validate() error

type TransferID uint64
type Role uint8
const (Send Role = iota + 1; Receive)
type Output struct {
    TransferID TransferID
    Operation OperationKey
    Action Action
}
type Fragment struct {
    Operation OperationKey
    Token message.Token
    Kind Kind
    Metadata Metadata
    Block Block
    Payload []byte
}
type Control struct {
    Token message.Token
    Continue *uint32
    Missing []uint32
    Finish error
}

type Manager struct { /* private registries and accounting */ }
func NewManager(cfg ManagerConfig) (*Manager, error)
func (m *Manager) StartSender(operation OperationKey, token message.Token,
    kind Kind, meta Metadata, payload []byte, now time.Time, jitter float64) ([]Output, error)
func (m *Manager) StartReceiver(fragment Fragment, now time.Time) ([]Output, error)
func (m *Manager) Receive(fragment Fragment, now time.Time) ([]Output, error)
func (m *Manager) Control(control Control, now time.Time) ([]Output, error)
func (m *Manager) ControlWithToken(id TransferID, control Control, now time.Time) ([]Output, error)
func (m *Manager) BindToken(id TransferID, token message.Token) error
func (m *Manager) Tick(now time.Time) []Output
func (m *Manager) Cancel(id TransferID, err error) []Output
func (m *Manager) NextDeadline() (time.Time, bool)
func (m *Manager) Active() uint32
```

`NewOperationKey` returns an error for zero parts, an empty part, more than 32 parts, or a combined encoded length above 512 bytes. It encodes each part as a two-byte big-endian length followed by bytes. The exact opaque result is only a map key; it is never parsed by the manager.

`Control` permits exactly one of `Continue`, nonempty `Missing`, or `Finish`; malformed combinations are rejected before registry lookup. `Missing` is normalized by the manager: values must be in ascending order before it calls `Sender.Repair`, since an unordered report is protocol-invalid. Manager action wrapping copies `Payload`, `Numbers`, `Operation`, and `Block` by value.

`ControlWithToken` is the explicit-ID variant for adapters that first validate
control identity outside the manager and then receive a fresh packet token. It
evaluates the sender transition and token binding atomically: rejection leaves
sender state, token ownership, and retained bytes unchanged. Reusing a token
already owned by that transfer is allowed; another transfer's token and a
token-cap violation are rejected. A no-op Continue does not create a binding,
an accepted queued Repair may, and an expiry release never retains the fresh
token. `Control` and `BindToken` retain their existing semantics.

## Task 1: canonical operation identity and manager configuration

**Files:** Create `net/qblock/identity.go`, `net/qblock/identity_test.go`; modify `net/qblock/transfer.go` only if shared error definitions must move there; create `net/qblock/manager.go`, `net/qblock/manager_test.go` with configuration-only tests.

**Interfaces:** Produce `OperationKey`, `NewOperationKey`, `ManagerConfig`, `DefaultManagerConfig`, `Validate`, and `NewManager`. Add exported sentinel errors: `ErrUnknownTransfer`, `ErrTokenInUse`, `ErrOperationInUse`, `ErrOperationNotFound`, `ErrLimitExceeded`, and `ErrInvalidControl`.

- [ ] Write failing identity tests showing `{[]byte("ab"), []byte("c")}` differs from `{[]byte("a"), []byte("bc")}`, mutating source slices cannot change a key, and invalid empty/oversized part lists fail.
- [ ] Write failing config tests for defaults: transfer defaults, 64 transfers, 512 tokens, and 16 MiB retained bytes. Reject zero limits, `MaxTokens < MaxTransfers`, invalid `TransferConfig`, and a body-size limit larger than aggregate retained bytes.
- [ ] Run `GOCACHE=/tmp/go-coap-qblock-cache go test ./net/qblock -run 'OperationKey|ManagerConfig' -count=1`; expect undefined APIs.
- [ ] Implement the canonical length-prefix encoder and config validation. Keep all error values stable sentinels wrapped with context only where needed.
- [ ] Run focused tests plus `go vet ./net/qblock` and `git diff --check`.
- [ ] Commit: `feat(qblock): add operation identity and manager limits`.

## Task 2: sender records, token registry, and output ownership

**Files:** Modify `net/qblock/manager.go`, `net/qblock/manager_test.go`.

**Interfaces:** Implement `StartSender`, `BindToken`, `Control`, `Cancel`, `Active`, and private `emit`/`remove` helpers. A sender operation is keyed by `OperationKey`; one initial nonempty token binds atomically with sender creation. `StartSender` calls `Sender.Start(now)` and returns its actions wrapped as outputs.

- [ ] Write a failing test that starts a 21-block Q1 sender under key A/token A; assert its first ten `SendBlock` outputs carry one transfer ID and copied source bytes. Bind a second token, then mutate the caller’s token/payload slices and prove the manager mapping and repair output are unchanged.
- [ ] Test that duplicate operation keys and duplicate tokens fail atomically with `ErrOperationInUse` and `ErrTokenInUse`; an unknown/cross-operation control returns `ErrUnknownTransfer`; neither changes `Active`, token count, bytes, or earliest deadline.
- [ ] Test `Control{Token: A, Continue: ptr(9)}` advances only sender A; an ordered `Missing:[2,7]` produces repair outputs; unordered, duplicate, empty, and mixed control fields return `ErrInvalidControl` before calling Sender.
- [ ] Test StartSender reserves `meta.Size`, refuses a second operation exceeding `MaxRetainedBytes`, and releases bytes/tokens/operation after `Cancel` and terminal `Control.Finish`. Call Cancel twice and verify no double-release or output.
- [ ] Run the focused manager tests red, implement record tables (`byOperation`, `byToken`, `byID`) and output copying, then rerun green.
- [ ] Commit: `feat(qblock): manage sender operations and token ownership`.

## Task 3: receiver records and duplicate lifecycle

**Files:** Modify `net/qblock/manager.go`, `net/qblock/manager_test.go`.

**Interfaces:** Implement `StartReceiver` and `Receive`. `StartReceiver` validates an unbound nonempty fragment token and creates a receiver from its complete adapter-provided metadata. It reserves `Metadata.Size` before creation, binds the fragment token, immediately passes it to `Receiver.Receive`, and rolls back every registry/accounting update if any step fails. `Receive` requires exact operation, kind, and token ownership before calling `Receiver.Receive`.

- [ ] Write a failing Q1 test that receives final block first, starts under key A/token A, binds token B for a later fragment, and delivers exactly one owned body after all fragments. Assert a duplicate block on B yields `Duplicate` with its manager transfer ID and keeps the Q1 record active.
- [ ] Test Q2 delivery emits `Deliver` then `Release`, and manager removes the operation/token/byte reservation before returning. A late Q2 fragment using the old token returns `ErrUnknownTransfer` and cannot recreate the body.
- [ ] Test cross-operation token injection, key mismatch, malformed fragment metadata, and byte/transfer/token limit pressure leave every registry unchanged. Verify an incomplete Q1 receiver reaches retry exhaustion through `Tick` and releases state.
- [ ] Test manager copies received payload and emitted delivery payload. Mutate input after `Receive`, then complete/reassemble and compare the original bytes.
- [ ] Run receiver-manager tests red, implement transaction-style start/rollback and release processing, then rerun green.
- [ ] Commit: `feat(qblock): manage receiving operation lifecycle`.

## Task 4: global deadlines, action ordering, and trace

**Files:** Modify `net/qblock/manager.go`, `net/qblock/manager_trace_test.go`, `net/qblock/manager_test.go`; update design/results documents.

**Interfaces:** Implement `Tick` and `NextDeadline`. `NextDeadline` returns the minimum nonzero deadline among active transfer records. `Tick` visits records in increasing `TransferID` order, evaluates only transfers due at `now`, preserves each transfer’s action order, and removes records immediately after a `Release` action. A record that emits `Complete` without `Release` remains active. `Cancel` removes only after processing its Release output.

- [ ] Write a failing test with two senders having different jitter-derived due times and one receiver recovery deadline; assert NextDeadline chooses the earliest. Tick at each timestamp advances only due transfers and orders outputs by TransferID then transfer action order.
- [ ] Write a deterministic Q1 trace through manager actions: start sender, bind each emitted packet token, drop one data block and first missing-control event, call Tick, feed ordered missing control using a bound token, finish successfully, then prove every token/key/byte reservation is gone. Repeat an inbound Q2 receive trace and prove Deliver/Release action order.
- [ ] Test action release cannot delete a newly created operation with the same key: old records are removed before the caller can reuse their key, and stale token controls fail.
- [ ] Implement stable ID sorting, deadline selection, and removal only through `Release`. Update the design status and results with exact checks; do not claim UDP/DTLS integration.
- [ ] Run `go test -race ./net/qblock -count=1`, focused UDP Q rejection tests, existing missing-decoder fuzz for 20 seconds, `golangci-lint run ./net/qblock/...`, and `git diff --check`.
- [ ] Commit: `feat(qblock): coordinate transfer operations and deadlines`.

## Coverage check

| Requirement | Task |
| --- | --- |
| Exact Request-Tag/ETag-operation identity boundary | 1 |
| Bounded active operations, tokens, and retained bytes | 1–3 |
| Multiple tokens, ownership and stale controls | 2–4 |
| Q1 terminal outcome and duplicate recognition | 2–3 |
| Q2 delivery/release lifecycle | 3–4 |
| Global earliest deadline and deterministic action ordering | 4 |
| Shared probing rate, MTU report sizing, raw option parsing, UDP packet writing | Explicitly deferred to adapter milestone |
