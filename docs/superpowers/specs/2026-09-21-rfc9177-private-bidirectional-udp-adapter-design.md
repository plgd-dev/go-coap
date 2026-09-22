# Private bidirectional UDP Q-Block adapter design

Status: implemented private slice (2026-09-22). This follows the completed
private UDP client Q-Block1/Q-Block2 adapter and implements/tests the server
role inside `udp/client`; it does not wire that role into `udp/server`, enable
a public Q-Block API, or claim full RFC 9177 support.

## Purpose and boundary

Extend a privately configured `udp/client.Conn` so its server role can receive
a NON Q-Block1 POST/PUT, dispatch one assembled request to the configured
handler, send a Q-Block2 response body, and answer later Q-Block2 Continue or
repair requests. The result uses the existing `net/qblock` sender and receiver
machines and keeps the ordinary handler body-oriented.

The slice is deliberately private, selected only by unexported construction
options used by same-package `udp/client` tests. `udp/server` wiring is
deferred with public configuration. This adds neither `options.WithQBlock`,
capability discovery, DTLS propagation, Observe support, generic
`WriteMessage` interception, nor application-visible Q-Block configuration.
Classic Block1/Block2 behavior and the disabled Q-Block rejection path remain
unchanged outside the explicitly enabled test path.

Success means a complete Q1 upload invokes the application handler once, the
handler's response can be served as Q2, and all failures or connection closure
release active state without allowing a malformed/conflicting first fragment
to leave any operation, token, or retained-byte state behind.

## Architecture

Each connection has one private Q-Block coordinator and two role adapters:

```
incoming Q1 request ──> server receiver ──> assembled handler dispatch
                                               │
                                               v
Q2 controls <───────── server sender <── copied handler response

outgoing client requests/responses <──> existing private client role
```

The existing private `qblockClient` becomes the per-connection coordinator. It
owns exactly one `qblock.Manager`, one serialization mutex, and
connection-wide registries for Q-Block transfer IDs, tokens, MIDs, owned
messages, and aggregate retained bytes. Its current client maps remain the
client role; a private server-role record/map namespace hangs from the same
coordinator because its request and response lifecycles differ from outgoing
client exchanges.

The manager is deliberately synchronization-free. The coordinator calls it
only while its mutex is held. It snapshots outputs and releases that mutex
before writing packets, invoking a handler, invoking a failure callback, or
releasing a pooled message. Packet-send results re-enter the coordinator to
finish or roll back state. This preserves the current no-I/O-under-lock rule
and gives both roles one limit and deadline domain.

All bytes retained beyond an inbound callback are copied: request tags,
identity fields, options, payload blocks, response body, and representation
metadata. A `pool.Message` is never retained. Response construction happens
from copied snapshots before its writer/message lifecycle ends.

## Shared coordination and ownership

The coordinator owns a single `ManagerConfig`, so `MaxTransfers`, `MaxTokens`,
and `MaxRetainedBytes` apply across client and server roles, not once per role.
It is the sole Q-Block user of `Conn`'s collision-checked token reservations
and its MID ownership map. An outgoing request or a Q2 control request claims
a new token; the server never allocates a fresh token for a response to a Q1
block.

The private server role maintains these logical records:

- Active Q1 receiver by validated request identity and by its request token.
- Completed-Q1 duplicate record by the same identity, with execution outcome,
  bounded expiry, and enough response metadata to suppress re-dispatch.
- Active/retained Q2 sender by representation identity, request identity, and
  all tokens/MIDs that can address it.
- A bounded mapping from a valid Q2 control request identity to the retained
  sender; a control token is bound only after this lookup succeeds.

The coordinator releases a MID after its response/write ownership ends. A
manager token binding and the connection's token reservation are released
together. Manager `Release` output is not by itself permission to discard a
completed-request duplicate record or a retained Q2 representation: those
records have their own bounded lifetimes.

## Routing and wire rules

Q processing occurs after ordinary duplicate-MID response-cache lookup but
before classic Blockwise or application dispatch. The private client role
retains first chance at owned responses; the server role then handles inbound
Q1 requests and Q2 controls. Unowned/non-Q traffic follows the existing
routing path.

Q1 first fragments are valid only for supported NON POST/PUT request shapes.
They must contain valid Q options, exactly the required Q1 metadata including
Request-Tag and Size1, a consistent request identity, valid block/offset/body
metadata, and no classic/Q option mix. Later fragments must resolve to that
identity and have unchanged fixed metadata. Invalid NON Q requests are
silently ignored under the existing disabled/error policy; no malformed
request reaches the application handler.

The Q2 sender uses the appropriate incoming Q1 request token for the response
exchange, as required by RFC 9177 section 6. It allocates a fresh MID for each
NON response. It does not allocate a response token. Its representation body
is retained after `Sender.Complete`, because a peer can legitimately issue
later Continue or repair controls within the configured retention lifetime.

A Q2 Continue or repair control request has a fresh request token. It cannot
be found by token alone. The server first validates and canonicalizes the
control's original-request identity (method, request operation options,
complete Request-Tag collection, and applicable representation identity),
locates the retained sender, then atomically claims and binds that new token.
Conflicting, ambiguous, stale, or unknown controls make no change to the
sender, token registry, or retained representation.

## Transactional Q1 admission

First-fragment admission is a strict prepare/commit sequence:

1. Decode and copy only the fields needed to validate the fragment and derive
   its canonical request identity.
2. Validate option multiplicity, request method/type, Request-Tag/Size1,
   metadata, block range, payload length, and aggregate limits.
3. Reject an identity that is active with conflicting metadata or whose
   completed record conflicts with its original request fingerprint.
4. Call `Manager.StartReceiver`; it validates the first block before publishing
   a manager operation, token binding, or retained-byte reservation.
5. Only after that succeeds, publish coordinator maps and any token/MID
   indexes, then execute returned actions outside the mutex.

Any error before step 5 leaves no newly created coordinator record, manager
operation, manager token binding, connection token reservation, or retained
payload bytes. Existing ordinary token handlers and unrelated transfers remain
untouched. A conflicting fragment for an existing transfer does not create a
second operation; it follows the normal terminal/cancellation policy only for
the resolved existing transfer.

## Handler dispatch and duplicate suppression

When the Q1 receiver emits its one assembled delivery action, the coordinator
builds a fresh owned request snapshot and dispatches it once through the
server's configured handler. The handler cannot observe a Q-Block fragment or
the original pooled request. Its response is copied before response-writer
resources are released and is validated against connection and Q-Block limits
before starting the Q2 sender.

The completed-request record is published before handler dispatch, marking the
identity as executing. It transitions atomically to completed success or
completed failure. A duplicate Q1 block/transfer for that identity never
invokes the handler again. If sending the response fails, the record remains
for its bounded duplicate-suppression lifetime even though active wire state
and transient MIDs are released. The record may provide the retained response
where applicable, but its minimum requirement is to prevent a second
application invocation.

Completed records and retained Q2 responses expire according to a bounded
per-connection lifetime compatible with the existing response-cache/exchange
lifetime policy. Expiry and connection close remove the record, all remaining
token/MID bindings, and retained bytes exactly once. This is bounded duplicate
suppression, not durable exactly-once execution.

## Failure, timer, and shutdown behavior

Every action write is followed by a result event. Token/MID allocation,
message encoding, and socket write failures cancel only the affected active
transfer and release its transient resources. They do not delete a completed
duplicate-suppression record. A handler error or an unusable response produces
the defined terminal outcome once and leaves no active transfer.

`Conn.CheckExpirations` continues to call the private coordinator tick. It
advances the shared manager once, expires bounded completed/representation
records, and executes resulting writes outside its mutex. This is sufficient
for the private adapter's deterministic timeout and cleanup tests; it is not
the future one-deadline scheduler, pacing, probing-rate, or MTU packet-sizing
work. Those remain a separate phase before public enablement.

Connection close cancels all active client and server transfers through the
same coordinator, drains token and MID ownership, drops retained snapshots and
completion records, and reports each waiting client operation at most once.
It never calls application code under the coordinator lock.

## Testing

Use same-package fake-session tests with controlled time, token/MID generators,
and an inspectable handler. Tests must not depend on UDP socket timing.

- Valid multi-fragment Q1 POST and PUT assemble exactly once and expose a
  normal complete request to the handler.
- Valid handler responses become Q2 NON fragments using the incoming Q1 token
  and fresh MIDs; later Continue and repair requests use fresh control tokens.
- A malformed, conflicting, token-colliding, over-limit, or write-failing first
  Q1 fragment leaves manager active count, manager retained bytes, coordinator
  maps, token reservations, and retained payload bytes unchanged.
- A duplicate upload with a different MID is not conflated with response-cache
  duplicate handling and never invokes the handler twice, including after an
  initial response-write failure.
- Q2 controls with a fresh token locate the correct retained representation by
  validated identity, bind only after success, and cannot cross-address another
  response with the same token, tag, or ETag component.
- Timer expiry, Reset, send failure, handler failure, cancellation, and
  connection close release active state exactly once. Retained completed/Q2
  records survive only for their intended bounded lifetime.
- Existing client Q1/Q2, disabled-Q, classic Blockwise, ordinary `Do`, and
  Observe tests remain unchanged and pass with the shared coordinator.

## Deferred work

After this adapter is accepted, complete the shared deadline scheduler,
pacing/probing-rate accounting, packet/MTU sizing, and full connection-memory
accounting. Only then add public configuration, explicit capability probing,
and UDP/DTLS propagation. Interoperability, fault injection, and release
documentation remain later milestones.

Real `udp/server` construction, server-initiated GET Q-Block2, public
configuration, DTLS propagation, complete scheduler/pacing/packet sizing, and
interoperability are explicitly outside this implemented private slice.
