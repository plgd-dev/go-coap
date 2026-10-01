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

Each generation has one terminal decision, serialized under the coordinator lock. The first fully validated result, including successful required separate-CON ACK handling, competes with processed last-waiter departure, exchange expiry and close. Before accepting a result, check the connection context and absolute exchange deadline: close invalidates publication, and an elapsed deadline produces an inconclusive timeout. Freeze the winning result and knowledge transition before closing the shared done signal. Later duplicates or conflicting responses cannot change either; ordinary duplicate ACK handling remains active.

Waiter cancellation takes effect on shared state when its departure is processed under the same lock. Last-waiter departure while pending invalidates the generation before canceling the exchange. If result publication wins first, the knowledge transition stands even if a caller subsequently returns its own context error. An admitted count does not claim that every admitted caller's context is still live. Each caller checks its context before returning and returns its context error if canceled; otherwise it receives the frozen shared result. Close resets knowledge, and stale-generation ingress cannot publish or remove successor ownership. This contract requires a bounded count, not retained caller contexts or polling every caller deadline.

## Shared explicit probe ownership

Empty path maps to `/.well-known/core`. Retain existing path validation; do not add percent decoding, slash collapse or dot-segment normalization. Coalescing compares the resulting path string exactly.

One pending wire probe exists per connection. Same-path callers join only while that generation is pending; different-path callers return `ErrQBlockProbeInProgress` without admission or network activity. Each caller validates its path and checks its context before joining. After a terminal decision, all new callers receive the in-progress error until wire/token/MID/permit/lease cleanup completes, including callers using the same path. Then the slot becomes empty and the next valid call starts a fresh generation; it never joins a completed result. Already admitted callers may finish consuming the frozen result after cleanup; the connection retains no retired-generation registry.

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

`DefaultClientConfig()` supplies every explicit default. A zero ClientConfig is invalid; callers start from defaults. Manager.Validate and existing pacing validation enforce timing relationships, body/retained limits, transfers/tokens and overflow. No second set of transfer timing defaults is introduced. Configuration is copied at option application/construction and immutable thereafter.

### SZX ceiling and production timing

Retain the existing shared `Config.BlockwiseSZX` ceiling, default `blockwise.SZX1024` (6), for classic and Q. `WithBlockwise(enable, szx, timeout)` sets that ceiling even when enable=false. Q selection is independent of classic enablement, but its maximum block size deliberately shares this setting. Do not add a ClientConfig or ServerConfig SZX field. Any public Q runtime validates this ceiling in [0,6] before publication, including when classic is disabled; BERT (7) is invalid for Q. Packet-budget and peer negotiation can select a smaller size before a body starts, following the existing sizing design. Both roles in a combined runtime consume the same immutable connection ceiling.

Production outbound runtime construction uses the existing real clock and automatic scheduler. Supply a concurrency-safe random jitter source returning values in [0,1), for example `math/rand/v2.Float64`. Sample once per sender body and reuse that sample for inter-set delay and derived probing wait. Retain private injectable time/jitter for deterministic tests; do not expose them as public configuration or silently use the private constant-zero jitter fallback for new public outbound wiring. This amendment does not reopen existing server-only timing reviews.

### Construction failures and resource ownership

Dial validates canonical limits, pacing, shared SZX and the configured plaintext datagram cap before opening a socket. Complete any transport/runtime-specific validation before returning a usable connection. On any failure, return nil and the initialization error and close all sockets/DTLS resources that Dial opened; preserve the primary error and join cleanup failures. Do not route Dial construction errors through Errors as well as its returned error.

Pointer-returning UDP/DTLS Client constructors retain their signatures. On invalid Q configuration, return a terminal connection with its initialization error stored. Cancel its session and synchronously finalize any partially created Q runtime, ordinary permits, endpoint attachment and internal channels, independently of reader shutdown. Do not start Run, a scheduler, or register a periodic runner. Done must already be closed when construction returns. For a supplied transport, preserve existing CloseSocket ownership: close it only if the caller selected WithCloseSocket; otherwise leave the underlying socket/DTLS connection open while the wrapper is terminal. The owned-versus-borrowed policy also applies when validation fails before wrapper construction.

Report one initialization error through the effective Errors callback after cleanup and before returning from a pointer constructor, outside all internal locks. If the callback is nil, use the existing no-op default. Returned operation errors are not reported again. Add initialization guards before ordinary request-limit queues and before body processing for Do/Get/Post/Put/Delete and DoObserve/Observe; also guard ProbeQBlock, WriteMessage, AsyncPing/Ping and Run. These executing methods return errors wrapping the stored initialization error (errors.Is must work), even for an already canceled caller context. Message creation/accessors and idempotent Close remain usable and do not promise an initialization error. Guards apply only to failed construction; successful connections retain normal queue/context errors.

Server constructors retain their existing stored-init-error/Serve error convention. Canonical/shared-role/transport validation precedes accepted worker/session publication. Reject and synchronously finalize any partially built accepted runtime; close a rejected accepted DTLS connection, or cancel an accepted UDP session without closing the server's shared socket. No OnNewConn/handler publication occurs. Normal later Close remains idempotent; do not rely on an unstarted reader to invoke runtime cleanup.

## Accepted connections and inbound/outbound coexistence

WithQBlock enables outbound selection on dialed and accepted connections. It does not enable inbound Q request handling. WithQBlockServer enables inbound handling; it does not establish remote capability or outbound enablement. Explicit probes are permitted in either case.

Both roles use one connection runtime, manager, scheduler, owned budget and endpoint member. Do not attach competing runtimes or duplicate token/MID namespaces. For a server using both options, their shared fields must match exactly: Manager, ProbingRate, NonProbingWait, MaxIntentBytes, MaxOwnedBytes, MaxMIDEntries, MaxPeers, MaxConnections and MaxEndpointMembers. Otherwise construction fails deterministically, independent of option order. This deliberately avoids implicit precedence or budget merging. Server-only Retention/MaxRecords/MaxMetadataBytes remain governed by ServerConfig; Mode/MaxProbeWaiters by ClientConfig. Derive a zero owned budget from the combined runtime floor. Shared manager limits bound both roles together.

A server configured only with WithQBlock creates the shared endpoint owner for outbound work on accepted connections, with inbound Q rejection still enabled. A server configured only with WithQBlockServer retains current server-only preparation. Capabilities remain per accepted security session, never on the shared endpoint owner.

Every DTLS connection with either Q role enables the existing Q receive safeguards before its reader starts: plaintext cap `min(MTU, MaxMessageSize)`, one overflow-detection byte, oversized-record dropping and recoverable temporary record-error handling. This includes dialed clients, supplied DTLS Client connections and accepted outbound-only connections. Enabling these safeguards does not establish peer support or inbound request enablement. UDP/DTLS use the same validated plaintext budget for owned reservation and packet selection.

## Operation selection and no replay

After the initialization guard, successful connections retain ordinary request-limit queue behavior. Select the operation when it reaches execution after those queues, using knowledge at that instant. Selection occurs once before `Message.Clone`, classic `BlockWise.Do`, body reads/seeks/copies, Q mutation, Q transfer admission or network writes. Token generation and metadata-only ordinary request setup may precede selection. Carry the selected route through preparation and response handling; do not reselect in doInternal, classic callbacks or delayed activation. Knowledge changes while an operation is queued can affect its selection; changes after selection affect later operations only. An active Q transfer is not converted or restarted.

Require selection failures perform zero body reads or seeks. Q-selected POST/PUT copying uses the existing bounded, admitted body-copy path; do not use the current unbounded `Message.Clone` body copy as a preliminary snapshot. Copy metadata separately and acquire owned capacity before body allocation/read. Preserve caller request metadata and body position according to the existing owned-copy contract. Classic-selected requests go through the existing classic pipeline; an attached Q runtime must not opportunistically prepare them.

Eligible operations are unicast GET without body and body-bearing POST/PUT, without Observe or caller-supplied classic/Q block options. Existing body limits and Request-Tag handling remain binding. DELETE, bodyless POST/PUT, GET with body, Observe, multicast, other methods and explicit block options are ineligible. Generic WriteMessage remains outside this automatic selection contract and retains its existing behavior.

Treat body-bearing as `Body()!=nil`, without probing its size during eligibility checks. An empty nonnil body remains eligible; a nil body does not. Require rejects new Observe subscriptions through both Do with an Observe option and the separate Observe/DoObserve entry points with ErrUnsupportedOperation before transmission. PreferKnown retains existing Observe handling even for a supported peer. Cancellation/cleanup of an existing subscription remains functional and is not blocked by Require; it uses ordinary cancellation handling, not Q preparation. Ping and generic WriteMessage are outside mode selection, while failed-construction guards still apply.

| Configuration / operation | Selection |
|---|---|
| No WithQBlock | existing ordinary/classic behavior |
| PreferKnown, eligible, supported | Q |
| PreferKnown, eligible, unknown/unsupported | existing ordinary/classic behavior |
| Require, eligible, unknown | `qblock.ErrCapabilityUnknown`, no body processing/write |
| Require, eligible, unsupported | `qblock.ErrPeerUnsupported`, no body processing/write |
| PreferKnown, ineligible | existing ordinary/classic behavior |
| Require, ineligible Do or new Observe/DoObserve operation | `qblock.ErrUnsupportedOperation`, no body processing/write |
| Require, eligible, supported | Q |

Classic-disabled means the same existing non-blockwise behavior, including its ordinary size errors; PreferKnown does not enable classic implicitly. Q selection is independent of classic enablement. Local preparation/admission failures after Q selection return directly, even before the first send; they do not trigger classic fallback. Failed/ambiguous Q exchanges never replay. Lost final POST/PUT responses remain uncertain application outcomes. This promises no automatic resubmission, not exactly-once execution across peers/restarts.

## Verification and bounded delivery

Implement in two coherent tasks: session knowledge/coalescing, then complete public opt-in/selection and UDP/DTLS dialed/accepted wiring. Server CON one-block replies are separate. Use RED/GREEN for behavioral changes and retained scoped ledgers. Focused normal/race, compile-only, vet and full runtime checks precede one fresh-context Astra/high review of the new implementation range.

Required tests include every outcome transition; fresh probe despite existing knowledge; Q-less result preserving supported; Bad Option revocation/restoration; same/different paths; canceled leader/follower; all-waiter cleanup; terminal result versus processed departure/expiry/close; conflicting duplicate result freeze; calls during completed cleanup; admission limits; close/replacement/stale ingress; complete mode/eligibility/classic-disabled matrix using bodies that fail on Read or Seek; explicit and convenience Observe gating plus cancellation; selection after queue admission and unchanged during Q activation; shared SZX including classic-disabled/BERT cases; immutable option copies; combined-role config mismatch/order independence; constructor primary-error wrapping/callback cardinality/borrowed-versus-owned transports/no background startup/closed Done; shared resource bounds; bounded admitted Q snapshots; production jitter wiring with deterministic private injection; UDP/DTLS accepted/dialed isolation; DTLS oversized and temporary record recovery for outbound-only/dialed roles; and lost-response POST processing with no second submission.

## Self-review

The five original boundary gaps and the six material/two smaller written-review findings are resolved by explicit policies above. Existing ExchangeLifetime and accepted-server propagation are preserved. SZX coupling is deliberate and validated; Require covers both subscription entry paths. Probe terminal ordering distinguishes processed departure from caller context expiry. Constructor error guards and synchronous ownership cleanup apply before reader startup. Selection precedes cloning/classic routing, and all DTLS Q roles share receive safeguards. Q-less success remains useful as a probe return without overstating peer absence. Resource coexistence uses rejection of mismatched shared fields rather than implicit precedence. No public capability setter, forced support, automatic probing, address-wide knowledge, generic Q writes, server CON design or Milestones 5–6 are introduced. This revision changes documents only; no behavioral implementation is claimed. Written-spec approval is required before turning this amendment into an executable task plan.
