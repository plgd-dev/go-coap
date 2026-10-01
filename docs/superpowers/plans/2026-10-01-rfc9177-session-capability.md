# Milestone 4 session capability revised boundary and bounded plan

> **For agentic workers:** Use superpowers:executing-plans inline after the revised written amendment is approved and this outline has been expanded into executable task briefs. Behavioral changes require RED/GREEN. This document does not approve the amendment or begin implementation.

**Goal:** Add session capability caching/coalescing together with a coherent public client opt-in, without payload discovery or classic replay.

**Spec:** Proposed `docs/superpowers/specs/2026-10-01-rfc9177-capability-selection-design.md`; existing protocol/pacing/memory constraints in `docs/superpowers/specs/2026-09-15-rfc9177-design.md`; Milestone 4 roadmap in `docs/superpowers/plans/2026-09-15-rfc9177.md`.

**Baseline:** `feat/qblock-foundation`, `246c6420c61d499e72bc8737085d7e19f543759e`, linked worktree `/private/tmp/go-coap-qblock-foundation`.

**Revision baseline:** `029be8da6b006bedc1cc3974877a422f9946b869`. The original stop/review below is historical; the revised amendment proposes concrete resolutions and awaits written-spec approval. All implementation tasks remain unchecked.

## Verified implementation boundary

The public UDP/DTLS `Conn.ProbeQBlock(ctx, path)` performs an uncached single-block CON GET. Its busy slot rejects overlapping calls with `ErrQBlockProbeInProgress`. The caller owns the exchange context; connection closure also cancels it. Positive and conclusive negative wire outcomes are distinct from inconclusive errors. Existing review fixes remain completed and are not reopened.

`udp/client.Config` has no public Q client configuration. `options.WithQBlockServer` configures accepted server connections only. The private `qblockClientConfig` provides manager, pacing, endpoint, memory and scheduling inputs. Private preparation selects Q directly for eligible GET/POST/PUT; server-only preparation is disabled. Dialed UDP/DTLS clients do not attach the Q payload runtime. Server dispatch and response construction currently use the NON body path; answering a CON one-block discovery request needs a separate bounded lifecycle design.

## Original design boundary (historical)

1. **Cache observation versus explicit refresh.** Does a subsequent `ProbeQBlock` return cached knowledge or always perform the explicitly requested wire probe? Can supported become unsupported after a later conclusive response? Does an inconclusive refresh preserve earlier knowledge? The spec requires session lifetime but does not resolve refresh semantics. Permanently caching a negative from an application-selected resource can prevent later discovery through a capable resource.
2. **Coalescing identity and path conflict.** Same-session calls can supply different safe paths. Sharing the first caller's exchange silently ignores the second caller's resource choice; rejecting, serializing or separating different paths changes the public contract. Define normalization and conflict behavior before changing the existing busy error.
3. **Cancellation and bounded shared ownership.** Define whether a leader's cancellation leaves other callers alive, when the wire exchange is canceled after all waiters leave, its independent deadline, and admission bounds for waiters. A shared exchange cannot simply retain the first caller's context. The spec requires cancellation isolation but does not specify these lifecycle/admission choices.
4. **Public opt-in interaction.** Define whether session knowledge exists without `WithQBlock`, whether explicit probes on server-only connections can establish outbound knowledge without enabling outbound transfers, and the exact canonical config/defaults/validation/constructor error surface. Exposing `WithQBlock` before implementing its promised selection and dialed runtime would produce a partial public API. Existing server resource limits do not define a canonical outbound config.
5. **Selection boundary.** Specify Require behavior for conclusive unsupported peers and ineligible operations (Observe, DELETE, explicit block options, multicast, generic writes), and how classic-disabled configuration interacts with PreferKnown. The approved unknown/supported matrix does not resolve these cases; private preparation currently declines several operations. Selection must occur before body reads/copies and before sending bytes.

These decisions affect externally observable behavior and resource ownership. They require an approved design amendment; they are not inferred from server enablement or settled as implementation rulings.

## Revised design contract and review coverage

The amendment proposes fresh explicit wire probes, supported/unsupported transitions only from validated Q-aware Content/Bad Option, unchanged knowledge on Q-less success or inconclusive errors, and same-path-only sharing. It retains ExchangeLifetime and accepted UDP/DTLS propagation. The following revisions resolve the subsequent written-spec review:

| Finding | Revised contract | Owning task |
|---|---|---|
| Undefined Q SZX configuration | Retain shared BlockwiseSZX, default 6; WithBlockwise sets it even when disabled; validate 0–6 for every Q runtime | 2 |
| Observe entry-point inconsistency | Require rejects new subscriptions through Do and Observe/DoObserve; PreferKnown and existing-subscription cancellation remain ordinary | 2 |
| Failed-constructor ownership/errors | Dial returns primary init error and closes owned resources; pointer Client finalizes synchronously, preserves borrowed transport unless CloseSocket, closes Done, guards executing methods and reports once | 2 |
| Probe terminal races | One frozen decision under coordinator lock; processed last departure competes with completion; expiry/close checked before publication; completed/cleaning slots reject new joins | 1 |
| Clone/classic routing before selection | Sample after ordinary queues, before Clone/BlockWise.Do/body access; retain route; Q body snapshots use bounded admitted copying | 2 |
| Dialed/outbound-only DTLS ingress | Apply existing plaintext cap, overflow detection and temporary record recovery to every DTLS Q role before reading | 2 |
| Constant-zero production jitter | Wire real automatic time and concurrency-safe random per-body jitter; preserve deterministic private injection | 2 |
| Queue snapshot timing | Sample knowledge after ordinary queue admission; later changes cannot reroute selected/delayed work | 2 |

Only the revised spec defines these choices. This outline maps them to implementation ownership; it does not silently grant spec approval or claim a task passed.

## Global constraints

- Work only in this branch/worktree; no merge or push.
- Preserve the two supplied user edits and unrelated `.codanna/` files; never stage them.
- Prefix shell commands with `rtk`.
- Capability belongs only to the current connection/security session; replacement starts unknown.
- Never discover through application payloads, replay failed/ambiguous Q through classic, or switch an active CON body to NON.
- No Milestones 5–6 or repeated completed probe/Milestone 3 review.
- Retain this plan's ledger in `.superpowers/sdd/2026-10-01-rfc9177-session-capability/`.

## Bounded execution outline after design approval

This is a reviewable scope outline, not an executable task brief: exact signatures and expected conflict outcomes must be filled from the approved amendment.

### Task 1: Session capability state and shared explicit probe

**Files:** `udp/client/qblock_capability.go` (new), `udp/client/qblock_probe.go`, `udp/client/conn.go`, `udp/client/qblock_memory.go`; new same-package capability/lifecycle tests.

**Interfaces:** Consume the reviewed wire primitive and connection context. Produce the approved session state and shared-probe lifecycle; no payload selection yet.

- [ ] RED: outcome table, fresh probe with existing knowledge, Q-less preserving supported, Bad Option revocation/positive restoration; verify current uncached behavior fails new knowledge assertions.
- [ ] RED: same-path sharing, different-path error, independent leader/follower cancellation, last departure and default 64 waiter bound; verify current busy rejection fails sharing.
- [ ] RED: result-versus-processed-departure, deadline/close before publication, conflicting duplicate freeze, new call during completed cleanup, successor after teardown and stale generation; use deterministic barriers, not sleep-based timing.
- [ ] Implement only the approved state/coalescing contract; run focused normal/race tests and inspect cleanup of tokens, MIDs, permits and owned leases.
- [ ] GREEN: normal/race checks for the new capability lifecycle plus existing probe regressions; no re-review of the completed primitive. Record exact command/results and commit scoped work.

### Task 2: Public opt-in with complete selection and dialed wiring

**Files:** canonical config/errors in `net/qblock`, aliases/options in `options/qblockOptions.go`, `udp/client/config.go`, `udp/client/conn.go`, `udp/client/qblock_runtime.go`, `udp/client/qblock_client.go`, `udp/client/qblock_packet.go`, `net/client/client.go`, `net/client/limitParallelRequests/limitParallelRequests.go`, `udp/client.go`, `dtls/client.go`, UDP/DTLS server config/construction/session files, focused transport/config tests. These paths identify existing integration boundaries; the executable plan must settle exact guard/runtime interfaces before execution.

**Interfaces:** Consume Task 1 state and approved canonical limits/mode contract. Produce a working opt-in on UDP/DTLS dialed and accepted connections, one combined runtime where both roles are enabled, and explicit shared-field mismatch rejection independent of option order.

- [ ] RED/GREEN canonical defaults/limits, immutable options, shared SZX 0–6 with classic enabled/disabled/BERT, combined-role matching/mismatch in both option orders, and one shared manager/scheduler/endpoint/budget.
- [ ] RED/GREEN Dial early validation/no socket on invalid config and rollback after opening; pointer Client borrowed/owned socket cleanup, synchronous closed Done, no reader/scheduler/periodic startup, once-only callback and errors.Is(initErr) before request queues, including canceled contexts. Cover Do/convenience calls, Observe, Probe, WriteMessage, Ping and Run.
- [ ] RED/GREEN mode/eligibility matrix with zero-Read/zero-Seek bodies on Require rejection; check selection precedes Clone and BlockWise.Do, uses knowledge after queue admission and remains fixed through delayed activation. Bound/admit Q body copies before access; preserve caller position/options.
- [ ] RED/GREEN Require Observe rejection through both subscription APIs and ordinary cancellation of existing observations; PreferKnown keeps Observe ordinary even after positive discovery.
- [ ] RED/GREEN UDP/DTLS dialed/accepted peer-session isolation and no outbound knowledge from inbound enablement; DTLS overflow/temp-error recovery for dialed, supplied-client, outbound-only and combined roles before reader start. Verify production random-jitter wiring using private deterministic injection for timing assertions.
- [ ] RED/GREEN timeout/lost response after POST processing: handler runs once for this exchange and no automatic classic resubmission. Do not claim exactly-once execution across restarts.
- [ ] Run `rtk proxy go test ./udp/... ./dtls/... ./options ./net/qblock -run 'TestQBlock' -count=1 -timeout=180s`, its focused UDP/DTLS/options race equivalent, unfiltered `net/qblock` race, compile-only `./...`, vet and full runtime `./...`; expect exit 0 and no race reports. Retain logs and exact failure evidence if host-blocked; update roadmap/results as slice completion only.

Before execution, writing-plans expands each RED/GREEN item into exact test names/assertions, signatures, expected commands and commits. This revision is a bounded scope outline under a proposed spec, not that executable plan.

## Review focus

- Path-dependent negative evidence must not silently acquire broader meaning than the approved cache policy.
- Caller cancellation must not poison another waiter's exchange or leak ownership.
- Close/replacement must invalidate knowledge and prevent stale completion publication.
- Public options must actually govern dialed behavior and fail before payload transmission where required.
- Server enablement must not establish outbound peer support; failures after Q submission must never cause replay.

The owning task's regression matrix must cover constructor shutdown without reader startup, borrowed sockets, both Observe APIs, SZX coupling, terminal probe ordering, unbounded preselection clones, queued/delayed selection and DTLS role-dependent receive setup. Run one fresh-context Astra/high review of the eventual new implementation range, with one TDD pass for material findings. The historical boundary review does not count as that future implementation review.

## Historical stop and current revision status

The approved design supplies safety constraints and state names, but none of the five decisions above has a complete implementable contract. The tasks deliberately remain unchecked and no API/type names are invented beyond the existing proposed surface. This run stops at the requested reviewable design boundary. A separate server CON reply slice also lacks approved handler/admission/retention semantics and is not substituted to bypass that boundary. No production code or tests change; runtime verification of unchanged code is not a completion claim for this slice.

One fresh-context Astra/high review confirmed that stopping is justified; no Critical or Important findings. Two documentation minors are retained in the scoped ledger: distinguish shared deadline ownership from the existing ExchangeLifetime upper bound, and explicitly retain accepted-server propagation while narrowing its unresolved coexistence/precedence contracts. Neither changes the current stop or constitutes architecture approval.

The revised amendment now resolves those two documentation minors and maps all six material/two smaller written-review findings to tasks above. Historical review/stop evidence remains intact. Current status: revised proposed design and bounded outline ready for written review; implementation not started, Milestone 4 incomplete. No production or behavioral test files changed; no new runtime result is claimed.
