# Aggregate adapter-owned memory reservations

**Spec:** docs/superpowers/specs/2026-10-01-rfc9177-milestone3-completion-design.md
Architecture assessment: conservative lifetime reservations over existing bounded
manager/pending/server reservoirs. Cap adapter-owned copies and explicit bounded
bookkeeping allowances, excluding arbitrary application allocations and shared
message-pool retained capacity; these cannot be limited by the adapter.

## Task 1: Predictable reads and metadata preflight

Use an exact limit+1 read buffer (no io.ReadAll geometric spare capacity), preserve
read error/overflow behavior. Preflight original GET/Q1 option bytes before any
clone: <= O where O=D*(sizeof Option+1), D private datagram cap. Q2 response
snapshot exact clones. Tests oversized input rejected before read, read cap exact.

## Task 2: Total MID cardinality

Add private config MaxMIDEntries default65536, validate <=65536. Bound total
Q-attributable MID sets/indexes across client and server before writes, collision
cannot overwrite other owner, decrement on release/deactivate. Test limit churn,
collision rejection, ownership after teardown. Bound original request snapshots
and retained server tokens separately from active manager token limit.

## Task 3: Aggregate reservation and leases

Add MaxOwnedBytes config, zero derives worst count-limit reservation. Define named
checked formulas from M=MaxBody,N=MaxTransfers,T=MaxTokens,P=MaxPayloads,L=retained,
I=intent,R=server records,H=metadata,D=datagram,O=D*(sizeof Option+1).
Reserve floor L+I+2H plus static executor envelope and explicit high-water map,
channel and record bookkeeping. map allowance=256+2*entries*(key+value+referenced
bytes+64); this is a conservative application bookkeeping model, not exact Go
bucket/RSS accounting. Executor=2L+4I+8O+16D+4A*(sizeof Action+Output+callback)+
16*N*P*sizeof uint32, A=N*(min(P,body blocks)+3). Client lease=2*(M+1)+4O+named
exchange/context bookkeeping; server lease=4*(M+1)+8O+named record/handler overhead.
Thread-safe idempotent/refcount leases admitted before copying. Client preparation
and exchange/callback retain ownership across Q1/Q2 and cancellation. Server
candidate/record/handler refs cover first fragment, retained suppression and
blocked invocation after close. Closing never zeros live handler/callback charge.

## Task 4: Lifecycle tests and audit

RED/GREEN exact cap second preparation before body read, blocked handler/terminal
callback after manager release/close, discard once, Q1/Q2 handoff no free window,
rollback exact counter, count/token/MID churn. Document formula inventory and
excluded caller/application storage. Focused normal/race, compile/vet/whitespace,
runtime suite and one fresh-context final review; one TDD fix pass. Scope only
aggregate owned-memory gate; endpoint coordination/public server next plans.

Every formula must use checked uint64 arithmetic before allocation. Preserve user
edits/.codanna, bounded scoped ledger and verified commits. No push/merge.
