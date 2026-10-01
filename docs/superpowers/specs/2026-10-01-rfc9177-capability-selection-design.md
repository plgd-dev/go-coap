# Milestone 4 capability and client-selection design amendment

Status: proposed written amendment for user review. No implementation is authorized by this document alone.

## Intent and current boundary

Complete explicit session discovery and public opt-in selection for unicast UDP/DTLS GET and body-bearing POST/PUT. Preserve application-facing complete bodies and the existing classic path. Never discover through application payloads, replay a failed or ambiguous Q submission through classic, or switch an active CON body to NON.

The foundation, Milestones 1–3, bounded UDP/DTLS server construction and explicit `Conn.ProbeQBlock(ctx, path)` primitive are implemented. Session knowledge, coalescing, public outbound opt-in and dialed Q wiring are not. Server CON single-block probe replies remain a separate bounded slice. Historical milestone-specific limitations in older specs describe those slices, not current project status.

This amendment supersedes the capability/selection proposals in `2026-09-15-rfc9177-design.md` after approval. Existing protocol, pacing, memory and endpoint designs remain binding.

## RFC and libcoap grounding

RFC 9177 §4.1 requires both Q options together and CON discovery for reliable rejection. It leaves application selection policy outside its scope. Section 4.4 permits a one-block request with M=0.

Reference: libcoap commit `c63c8f7cb7f248a4992539529b9e1b691962f29a`, `src/coap_block.c:coap_block_test_q_block`, `src/coap_net.c` probe dispatch/result handling, and `man/coap_block.txt.in`. Its probe is CON GET `/.well-known/core`, QBlock2 NUM0/M0/SZX0. TRY_Q_BLOCK automatically queues the initiating application request behind discovery; session flags retain the result. FORCE_Q_BLOCK assumes support. These are precedents, not our API contract. Go-coap retains explicit discovery, stricter response validation and inconclusive Reset/error outcomes. No force option or automatic probe is introduced. Source inspection is not runtime interoperability evidence.

## Session knowledge and probe outcomes

Maintain private session knowledge: unknown, supported, unsupported. New connections start unknown. No address-keyed capability cache or knowledge inheritance exists. Both configured and unconfigured connections may explicitly probe; successful probing alone does not enable outbound Q.

The existing `(bool, error)` probe return remains. Every non-overlapping valid call initiates a fresh wire probe, even when knowledge exists. Knowledge is used for operation selection, never to suppress an explicit probe.

| Validated outcome | Return | Knowledge transition |
|---|---|---|
| Valid Q-aware Content response | true, nil | any → supported |
| Conclusive Bad Option | false, nil | any → unsupported |
| Successful response omitting QBlock2 | false, nil | unchanged |
| Reset, malformed response, timeout, transport failure | false, error | unchanged |
| Caller cancellation | false, caller context error | no transition from cancellation |

A Q-less success does not prove session-wide absence, particularly for an application-selected resource. Unsupported means the latest conclusive critical-option rejection, not permanent absence. A later explicit positive probe restores supported. Bad Option remains a conservative rejection decision even when another option may have caused it. No TTL is added; explicit refresh and close are the only discovery transitions. Ordinary application responses neither establish nor revoke knowledge in this slice.

One completed shared probe publishes its validated transition before notifying callers, provided the session is open, its generation is current and it still has at least one admitted waiter. Canceling one caller does not prevent publication for others. Close resets knowledge and invalidates publication. Last-waiter departure invalidates the generation before cancellation; late ingress cannot change knowledge or remove successor ownership.

## Shared explicit probe ownership

Empty path maps to `/.well-known/core`. Retain existing path validation; do not add percent decoding, slash collapse or dot-segment normalization. Coalescing compares the resulting path string exactly.

One wire probe exists per connection. Same-path callers join; different-path callers return `ErrQBlockProbeInProgress` without admission or network activity. Each caller validates its path and checks its context before joining. A caller arriving during canceled-probe cleanup receives the same in-progress error until cleanup completes.

The exchange context derives from the connection, not the first caller. Start its ExchangeLifetime deadline when the first waiter is admitted; admission/NSTART waits count against that bound. Caller deadlines only bound that caller's wait. Last-waiter departure cancels the exchange; cleanup keeps the slot occupied until wire/token/MID/permit/lease teardown finishes. No I/O or caller callback runs under the session-state lock.

At most `MaxProbeWaiters` callers are admitted, including the first. Excess callers return `qblock.ErrLimitExceeded`; they do not queue. Default is 64, maximum 65536; zero is invalid in explicit config. Without public config, use the same default. Retain one shared result/done signal and a bounded waiter count, not a channel/map allocation per caller owned by the connection. Do not retain caller contexts after departure. Charge the shared exchange and bounded coordinator storage to the existing owned-budget model when a Q runtime exists; unconfigured connections retain fixed bounded coordinator storage and the existing datagram limit.

## Public opt-in and resource configuration

Canonical `qblock.ClientConfig`, aliased as `options.QBlockConfig`; canonical `qblock.Mode` with `PreferKnown` and `Require`, aliased in `options`. `options.WithQBlock(config)` implements UDPClientApply, UDPServerApply and DTLSServerApply only. No TCP/TLS option support.

ClientConfig fields:

- `Mode`: PreferKnown by default; only PreferKnown/Require valid.
- `Manager`: existing `qblock.ManagerConfig`, default `DefaultManagerConfig()`.
- `ProbingRate`, `NonProbingWait`, `MaxIntentBytes`: existing pacing semantics; defaults 1 byte/second, 0 (derive), and 16 MiB.
- `MaxOwnedBytes`: default 0, meaning derive the adapter reservation floor; nonzero must meet the transport/runtime-specific floor, without silently increasing the caller's limit.
- `MaxMIDEntries`: default 65536; valid 1–65536.
- `MaxProbeWaiters`: default 64; valid 1–65536.
- `MaxPeers`, `MaxConnections`, `MaxEndpointMembers`: server endpoint admission fields, defaults 1024/1024/4096; existing ServerConfig range/relationship validation applies. Dialed clients use one standalone endpoint member; these fields do not create process-global sharing.

`DefaultClientConfig()` supplies every explicit default. A zero ClientConfig is invalid; callers start from defaults. Manager.Validate and existing pacing validation enforce SZX, timing relationships, body/retained limits, transfers/tokens and overflow. No second set of transfer timing defaults is introduced. Configuration is copied at option application/construction and immutable thereafter.

Dial validates config before opening a socket; transport-specific owned floors are validated before connection publication. Existing pointer-returning Client constructors retain their signatures: invalid Q config creates a closed connection whose operations return its initialization error, reports the error through the configured Errors callback once, and releases construction-owned resources. Server constructors retain their existing stored-init-error/Serve error convention; no accepted session is published on failure.

## Accepted connections and inbound/outbound coexistence

WithQBlock enables outbound selection on dialed and accepted connections. It does not enable inbound Q request handling. WithQBlockServer enables inbound handling; it does not establish remote capability or outbound enablement. Explicit probes are permitted in either case.

Both roles use one connection runtime, manager, scheduler, owned budget and endpoint member. Do not attach competing runtimes or duplicate token/MID namespaces. For a server using both options, their shared fields must match exactly: Manager, ProbingRate, NonProbingWait, MaxIntentBytes, MaxOwnedBytes, MaxMIDEntries, MaxPeers, MaxConnections and MaxEndpointMembers. Otherwise construction fails deterministically, independent of option order. This deliberately avoids implicit precedence or budget merging. Server-only Retention/MaxRecords/MaxMetadataBytes remain governed by ServerConfig; Mode/MaxProbeWaiters by ClientConfig. Derive a zero owned budget from the combined runtime floor. Shared manager limits bound both roles together.

A server configured only with WithQBlock creates the shared endpoint owner for outbound work on accepted connections, with inbound Q rejection still enabled. A server configured only with WithQBlockServer retains current server-only preparation. Capabilities remain per accepted security session, never on the shared endpoint owner.

## Operation selection and no replay

Selection occurs once before body reads, seek/copy, Q mutation, transfer admission or network writes. Token generation/ordinary request setup may precede selection. Knowledge changes after selection affect subsequent operations only; an active Q transfer is not converted or restarted.

Eligible operations are unicast GET without body and body-bearing POST/PUT, without Observe or caller-supplied classic/Q block options. Existing body limits and Request-Tag handling remain binding. DELETE, bodyless POST/PUT, GET with body, Observe, multicast, other methods and explicit block options are ineligible. Generic WriteMessage remains outside this automatic selection contract and retains its existing behavior.

| Configuration / operation | Selection |
|---|---|
| No WithQBlock | existing ordinary/classic behavior |
| PreferKnown, eligible, supported | Q |
| PreferKnown, eligible, unknown/unsupported | existing ordinary/classic behavior |
| Require, eligible, unknown | `qblock.ErrCapabilityUnknown`, no body processing/write |
| Require, eligible, unsupported | `qblock.ErrPeerUnsupported`, no body processing/write |
| PreferKnown, ineligible | existing ordinary/classic behavior |
| Require, ineligible Do operation | `qblock.ErrUnsupportedOperation`, no body processing/write |
| Require, eligible, supported | Q |

Classic-disabled means the same existing non-blockwise behavior, including its ordinary size errors; PreferKnown does not enable classic implicitly. Q selection is independent of classic enablement. Local preparation/admission failures after Q selection return directly, even before the first send; they do not trigger classic fallback. Failed/ambiguous Q exchanges never replay. Lost final POST/PUT responses remain uncertain application outcomes. This promises no automatic resubmission, not exactly-once execution across peers/restarts.

## Verification and bounded delivery

Implement in two coherent tasks: session knowledge/coalescing, then complete public opt-in/selection and UDP/DTLS dialed/accepted wiring. Server CON one-block replies are separate. Use RED/GREEN for behavioral changes and retained scoped ledgers. Focused normal/race, compile-only, vet and full runtime checks precede one fresh-context Astra/high review of the new implementation range.

Required tests include every outcome transition; fresh probe despite existing knowledge; Q-less result preserving supported; Bad Option revocation/restoration; same/different paths; canceled leader/follower; all-waiter cleanup; admission limits; close/replacement/stale ingress; complete mode/eligibility/classic-disabled matrix using bodies that fail if read; immutable option copies; combined-role config mismatch/order independence; shared resource bounds; UDP/DTLS accepted/dialed isolation; and lost-response POST processing with no second submission.

## Self-review

The five boundary-plan gaps are resolved by explicit policies above. Existing ExchangeLifetime and accepted-server propagation are preserved. Q-less success remains useful as a probe return without overstating peer absence. Resource coexistence uses rejection of mismatched shared fields rather than implicit precedence. No public capability setter, forced support, automatic probing, address-wide knowledge, generic writes, server CON design or Milestones 5–6 are introduced. Written-spec approval is required before turning this amendment into an executable plan.
