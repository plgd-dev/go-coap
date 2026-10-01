# Milestone 3 public UDP Q-Block server

> Execute inline with executing-plans and TDD.

**Spec:** docs/superpowers/specs/2026-10-01-rfc9177-milestone3-completion-design.md
**Goal:** Approved server-only options.WithQBlockServer(qblock.ServerConfig).
Public client probing/fallback and DTLS stay Milestone4.

## Task 1: Initial GET response ownership

Extend serverQ2Control to GET, accepting initial GET only NON NUM0/M1, no body,
no classic blocks, exactly one Q2 and Request-Tag collection. Snapshot operation
options and request token into admitted server record before handler dispatch.
GET handler receives normal request/options once; reuse bounded handler snapshot,
response sizing and Q2 sender path. Subsequent GET controls target same record,
retain stable SZX and identity; malformed/unknown controls silently consumed.
Record lifecycle and leases must not depend on a receiver TransferID for initial
GET. Test initial response, repairs, duplicate suppression, reset, handler close,
metadata/body cap and SZX negotiation. RED then minimum implementation GREEN.

## Task 2: Server-only public configuration and runtime

net/qblock/server_config.go defines ServerConfig with Manager, ProbingRate,
NonProbingWait, MaxIntentBytes, Retention, MaxRecords, MaxMetadataBytes,
MaxOwnedBytes, MaxMIDEntries, MaxPeers, MaxConnections, MaxEndpointMembers;
DefaultServerConfig and Validate. No zero struct silent defaults; helper explicit.
Runtime in udp/client owns immutable config and endpoint domain. NewConn attaches
private automatic server role, disables outbound private client preparation,
validates owned initialization and rolls back errors. Runtime Close/Prune.
options.WithQBlockServer applies only UDPServerApply, config copied. udp/server
New stores validation error; Serve and NewConn/getConn reject before admission.
getOrCreateConn propagates errors and never caches failed connections. Runtime
closed after sessions during Stop; periodic maintenance prunes detached debt.
Bound active conns and ordinary/Q domain members explicitly; document allowance
for endpoint table storage, no unbounded config allocations.

RED tests config validation and option application, real loopback GET and Q1
handler delivery, disabled rejection, construction capacity and shutdown.
Run focused normal/race, compile/vet/whitespace/runtime, one fresh review and
one TDD material fix pass. Update roadmap/results and scoped commit.

## Review focus

Initial GET duplicates and cached identity; handler teardown without receiver;
config rejection before Serve; connection cache limits/rollback; server-only
boundary and no automatic outbound Q capability assumption.
