# Milestone 4 session capability design boundary and bounded plan

> **For agentic workers:** Use superpowers:executing-plans inline after the architecture decisions below are approved. Behavioral changes require RED/GREEN. This document does not approve those decisions itself.

**Goal:** Add session capability caching/coalescing together with a coherent public client opt-in, without payload discovery or classic replay.

**Spec:** `docs/superpowers/specs/2026-09-15-rfc9177-design.md`, Capability and fallback; Milestone 4 in `docs/superpowers/plans/2026-09-15-rfc9177.md`.

**Baseline:** `feat/qblock-foundation`, `246c6420c61d499e72bc8737085d7e19f543759e`, linked worktree `/private/tmp/go-coap-qblock-foundation`.

## Verified implementation boundary

The public UDP/DTLS `Conn.ProbeQBlock(ctx, path)` performs an uncached single-block CON GET. Its busy slot rejects overlapping calls with `ErrQBlockProbeInProgress`. The caller owns the exchange context; connection closure also cancels it. Positive and conclusive negative wire outcomes are distinct from inconclusive errors. Existing review fixes remain completed and are not reopened.

`udp/client.Config` has no public Q client configuration. `options.WithQBlockServer` configures accepted server connections only. The private `qblockClientConfig` provides manager, pacing, endpoint, memory and scheduling inputs. Private preparation selects Q directly for eligible GET/POST/PUT; server-only preparation is disabled. Dialed UDP/DTLS clients do not attach the Q payload runtime. Server dispatch and response construction currently use the NON body path; answering a CON one-block discovery request needs a separate bounded lifecycle design.

## Essential decisions absent from the approved design

1. **Cache observation versus explicit refresh.** Does a subsequent `ProbeQBlock` return cached knowledge or always perform the explicitly requested wire probe? Can supported become unsupported after a later conclusive response? Does an inconclusive refresh preserve earlier knowledge? The spec requires session lifetime but does not resolve refresh semantics. Permanently caching a negative from an application-selected resource can prevent later discovery through a capable resource.
2. **Coalescing identity and path conflict.** Same-session calls can supply different safe paths. Sharing the first caller's exchange silently ignores the second caller's resource choice; rejecting, serializing or separating different paths changes the public contract. Define normalization and conflict behavior before changing the existing busy error.
3. **Cancellation and bounded shared ownership.** Define whether a leader's cancellation leaves other callers alive, when the wire exchange is canceled after all waiters leave, its independent deadline, and admission bounds for waiters. A shared exchange cannot simply retain the first caller's context. The spec requires cancellation isolation but does not specify these lifecycle/admission choices.
4. **Public opt-in interaction.** Define whether session knowledge exists without `WithQBlock`, whether explicit probes on server-only connections can establish outbound knowledge without enabling outbound transfers, and the exact canonical config/defaults/validation/constructor error surface. Exposing `WithQBlock` before implementing its promised selection and dialed runtime would produce a partial public API. Existing server resource limits do not define a canonical outbound config.
5. **Selection boundary.** Specify Require behavior for conclusive unsupported peers and ineligible operations (Observe, DELETE, explicit block options, multicast, generic writes), and how classic-disabled configuration interacts with PreferKnown. The approved unknown/supported matrix does not resolve these cases; private preparation currently declines several operations. Selection must occur before body reads/copies and before sending bytes.

These decisions affect externally observable behavior and resource ownership. They require an approved design amendment; they are not inferred from server enablement or settled as implementation rulings.

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

**Files:** `udp/client/qblock_capability.go` (new), `udp/client/qblock_probe.go`, `udp/client/conn.go`; new same-package capability/lifecycle tests.

**Interfaces:** Consume the reviewed wire primitive and connection context. Produce the approved session state and shared-probe lifecycle; no payload selection yet.

- [ ] Pin positive/negative/inconclusive caching and refresh rules with failing tests.
- [ ] Pin same-path/different-path callers, independently canceled leader/follower, all-waiter cancellation, bounded admission, close and replacement with failing tests.
- [ ] Implement only the approved state/coalescing contract; run focused normal/race tests and inspect cleanup of tokens, MIDs, permits and owned leases.

### Task 2: Public opt-in with complete selection and dialed wiring

**Files:** canonical config in `net/qblock`, aliases/options in `options/qblockOptions.go`, `udp/client/config.go`, `udp/client/conn.go`, `udp/client.go`, `dtls/client.go`, transport/config tests.

**Interfaces:** Consume Task 1 state and the approved canonical limits/mode contract. Produce a working opt-in on UDP/DTLS dialed clients; accepted-server propagation only as specified by the amendment.

- [ ] RED/GREEN the approved config/default/error matrix and selection before any body read or wire write.
- [ ] RED/GREEN disabled, unknown, supported and unsupported modes, ineligible operations and classic-disabled cases.
- [ ] RED/GREEN timeout/lost response after POST processing: exactly one application submission, no classic replay.
- [ ] Verify focused normal/race, compile-only, vet and full runtime; update roadmap/results as slice completion only.

## Review focus

- Path-dependent negative evidence must not silently acquire broader meaning than the approved cache policy.
- Caller cancellation must not poison another waiter's exchange or leak ownership.
- Close/replacement must invalidate knowledge and prevent stale completion publication.
- Public options must actually govern dialed behavior and fail before payload transmission where required.
- Server enablement must not establish outbound peer support; failures after Q submission must never cause replay.

## Self-review and current stop

The approved design supplies safety constraints and state names, but none of the five decisions above has a complete implementable contract. The tasks deliberately remain unchecked and no API/type names are invented beyond the existing proposed surface. This run stops at the requested reviewable design boundary. A separate server CON reply slice also lacks approved handler/admission/retention semantics and is not substituted to bypass that boundary. No production code or tests change; runtime verification of unchanged code is not a completion claim for this slice.

One fresh-context Astra/high review confirmed that stopping is justified; no Critical or Important findings. Two documentation minors are retained in the scoped ledger: distinguish shared deadline ownership from the existing ExchangeLifetime upper bound, and explicitly retain accepted-server propagation while narrowing its unresolved coexistence/precedence contracts. Neither changes the current stop or constitutes architecture approval.
