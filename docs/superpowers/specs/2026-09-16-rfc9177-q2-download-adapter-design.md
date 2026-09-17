# Private RFC 9177 Q-Block2 download adapter design

Status: implemented private vertical slice only. This document deliberately defers public options/capability handling, Q-Block1, server processing, Observe, multicast, DTLS-specific work, generic `WriteMessage` support, and interoperability validation.

## Goal

Wire the completed `net/qblock.Manager` receiver into `udp/client` so an internally enabled UDP connection can retrieve a NON Q-Block2 GET representation and return one complete ordinary response through the existing `Do` path. A caller using the public API sees no changed configuration surface. The private hook exists only for same-package tests and future integration work.

## RFC boundary

The adapter implements the Q-Block2 request and response rules from [RFC 9177 §4.4](https://www.rfc-editor.org/rfc/rfc9177.html#section-4.4):

- The initial GET advertises Q-Block2 with `NUM=0`, `M=1`, and the selected SZX, meaning request the full body.
- Each response carries exactly one Q-Block2 option and the same ETag and Size2 as the body. The adapter requires a 2.05 Content code, a nonempty ETag, and Size2 on every response. This is stricter than a general CoAP response because RFC 9177 requires Size2 on all Q-Block2 payloads and fixes the representation identity with the ETag.
- A manager `SendContinue{Through:n}` sends a fresh NON GET containing Q-Block2 `NUM=n+1`, `M=1`. `n+1` is a `MaxPayloads` set boundary.
- A `RequestMissing{Numbers}` action sends one fresh NON GET for each missing number in strictly increasing order. Each packet carries one Q-Block2 option with `M=0`. It copies the original request route and request options but removes Q-Block2, ETag, Observe, and payload before adding that packet's repair option. It does not place ETag on a repair request, as RFC 9177 specifies.
- Q2 response duplicates are silently accepted by the manager without redelivery. A changed ETag or Size2 is a conflicting fragment and terminates the transfer.

Every Q2 packet in this slice is NON. The request mutator sets the initial GET to NON, and every continuation or repair is also NON. This matches the receiver recovery model and keeps the existing confirmable MID retransmission machinery out of the transfer.

## Private connection boundary

`ConnOptions` gains an unexported `createQBlockReceiver func(*Conn) *qblockReceiver`, configured only by the unexported `withQBlockReceiver(qblockReceiverConfig)` option in `udp/client`. The production default is nil. No exported `WithQBlock`, no `options` package configuration, and no capability cache are added.

When enabled, `doInternal` recognizes a GET without Observe as eligible. Before writing it, the receiver prepares an initial Q2 request: it rejects an existing Q-Block option or request body, appends encoded Q-Block2 `0/M`, selects `cc.blockwiseSZX`, sets the type to NON, and records a small pending template keyed by the caller's already allocated token. Ordinary requests take their existing path unchanged. A pending template retains copied request options and the original response token but does not create a manager transfer, bind a Q2 token, or reserve body bytes.

When classic BlockWise wraps an eligible request, the Q2-mutated wire request is a private clone. The request retained by classic BlockWise therefore has no QBlock2 advertisement, so a peer that falls back to a multi-fragment classic Block2 response receives ordinary classic follow-ups without mixed Q options.

The normal token handler remains installed while the Q2 transfer is active. This preserves the existing `Do` response ownership and means a malformed first fragment cannot destroy the caller's pending request. Q2 intercepts an incoming response before classic blockwise/token routing. A non-Q response falls through to the existing handler unchanged.

## Adapter state and synchronization

`qblockReceiver` is a private, mutex-protected owner around one `qblock.Manager`. It contains:

- `pending`: initial-request templates keyed by the original request token; these become an active transfer only after the first response passes all semantic checks and `Manager.StartReceiver` accepts it.
- `transfers`: metadata required to serialize future requests and synthesize the final ordinary response, keyed by `qblock.TransferID`.
- `transferByToken`: copied initial and fresh-control tokens mapped to their transfer ID. It supplies the operation key and fixed metadata for later Q2 fragments before `Manager.Receive` verifies the same token independently.
- `manager`: configured with copied limits and driven with an injected clock in tests; production uses `time.Now`.
- the connection's configured `GetToken` function, used to allocate fresh control tokens. Allocation verifies that the token is neither a normal client token nor a manager-owned Q2 token before it is bound.

The mutex covers manager calls and the two adapter maps. It never covers packet writes, delivery through a token handler, or error callbacks. A state change yields copied manager outputs under the mutex; the executor consumes those outputs after unlocking. This serializes `handle` and `CheckExpirations`, as the manager requires.

The first-fragment path is transactional:

1. Decode and validate Q options, code, ETag, Size2, SZX, offset/payload bounds, fixed response metadata, and the matching pending request.
2. Build an operation key from copied original-token bytes and ETag bytes. The manager is scoped to one connection, and each original token is unique while active, so this pair is sufficient for the private slice.
3. Call `Manager.StartReceiver` before inserting `transfers` or binding any fresh control token.
4. Only after it succeeds, insert its private transfer template and execute its outputs.

Thus malformed first Q2 fragments leave manager active count, manager token registry, manager retained-byte accounting, and adapter transfer records unchanged. A fragment whose token is already indexed to a different transfer is a conflict and is rejected before any state change. The original ordinary token handler remains pending until its context or connection closes. A conflict after a transfer has started cancels that transfer and returns the protocol error to the original `Do` caller.

## Dispatch, actions, and lifecycle

`Conn.handle` calls the Q2 receiver before `BlockWise.Handle` and before `tokenHandlerContainer.LoadAndDelete`. A matching Q2 fragment is consumed even if invalid; it never reaches an application handler or classic blockwise implementation. A valid first fragment calls `StartReceiver`; later packets call `Receive` using the exact manager-owned token and operation. Unknown Q2 tokens are dropped.

The action executor maps manager output to wire I/O:

- `SendContinue` and `RequestMissing` allocate a fresh token, bind it through `Manager.BindToken`, construct a pooled NON GET from the copied request template, write it through `Session.WriteMessage`, then release the pooled packet. A write failure cancels the transfer and fails its original `Do` call.
- `Deliver` constructs a pooled ordinary 2.05 response with the original token, saved response options (ETag and Content-Format retained, Q-Block2 and Size2 omitted), and the assembled body. It atomically removes and invokes the original token handler, which hijacks the synthetic message exactly as a normal `doInternal` response does.
- `Complete` with an error removes the original token handler and sends that error to the waiting `doInternal` call. `Release` removes private transfer metadata. `Duplicate` has no wire effect.

`doInternal` gains a private Q2 error channel in its select. Its deferred cleanup asks the receiver to abandon the original token: before the first accepted fragment this removes only the pending template; after start it calls `Manager.Cancel` and releases every manager token and reserved byte. `Conn.CheckExpirations` calls `qblockReceiver.Tick(now)` and executes due recovery actions. The session close callback cancels all private pending/active records and wakes their callers. No pooled inbound message, caller request, option value, payload, or token slice is retained without copying.

## Tests and acceptance boundary

Use a fake `Session` that copies writes and a fixed clock; no socket is needed for the adapter tests. Tests must cover initial option mutation, ordinary fallback, full multi-block delivery, continuations, ascending repair packets, duplicate suppression, manager timer recovery, control-token allocation, write failure, request cancellation, and connection close.

The rollback tests are mandatory: malformed QBlock2 length/duplicate option, missing ETag, missing or inconsistent Size2, invalid first block, and a conflicting first fragment must leave `Manager.Active()==0`, no manager-owned tokens or retained bytes, no private transfer record, and no packet written. They must also prove the ordinary original token handler still remains registered until normal cancellation. Follow-on conflicting fragments must terminate the active Q2 operation without leaking state.

Run focused `udp/client` tests, `go test -race ./udp/client`, `go test ./net/qblock`, formatter and diff checks. Socket integration tests remain optional evidence because the sandbox does not permit UDP loopback binds. This slice is not release-ready RFC 9177 support; public opt-in, capability handling, Q1/server work, and interoperability remain future milestones.
