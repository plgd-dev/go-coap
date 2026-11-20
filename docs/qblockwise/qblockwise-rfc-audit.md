# RFC 9177 implementation audit

This is a source-and-test audit of the experimental direct-unicast UDP/DTLS
implementation against [RFC 9177](https://www.rfc-editor.org/rfc/rfc9177.html).
It is not a conformance certificate. Related requirements are grouped below;
an implemented row does not imply exhaustive fault or timing coverage.

Status meanings:

- **Implemented**: a source path implements the rule, with related test evidence.
- **Partial/gap**: a known deviation or incomplete behavior within the supported scope.
- **Unverified**: insufficient focused evidence; not a claim that the rule is met.
- **Outside scope**: not an advertised capability; not automatically a violation
  of requirements for the supported direct endpoint role.

## Requirements and evidence

Links below are relative to this document.

| RFC section and normative requirement | Status | Implementation and test evidence / qualification |
|---|---|---|
| 3.2, 7: Q guards supplement, not replace, base congestion control. | Implemented; load coverage incomplete | [Transport](../../udp/client/qblock.go) retains normal CON/NSTART handling and NON probing gates; [transport tests](../../udp/client/qblock_internal_test.go). No claim of measured congestion fairness under load. |
| 4.1: support both Q1 and Q2 or neither; client indicates Q2 capability. | Implemented | [Client sender](../../net/blockwise/qblock/client.go), [server routing](../../net/blockwise/qblock/engine.go), [selection tests](../../udp/qblock_engine_test.go). Disabled remains the default. |
| 4.1: process or reject critical Q options; discover support with CON. | Implemented | [Critical-option handling](../../net/client/criticalOptions.go), [connection selection](../../udp/client/conn.go), [critical-option tests](../../net/client/qblock_critical_test.go), [native fallback matrix](../../udp/qblock_libcoap_test.go). Discovery is separate from application writes. |
| 4.1: Q2 repeats only for recovery; reject illegal repeated Q1/Q2 and Q/legacy mixtures. | Implemented | [Q1 validation](../../net/blockwise/qblock/receive.go), [Q2 request validation](../../net/blockwise/qblock/send.go), [Q2 response validation](../../net/blockwise/qblock/client_receive.go); [wire negatives](../../udp/qblock_semantics_test.go), [response family tests](../../net/blockwise/qblock/rfc_test.go). |
| 4.1: unsafe-to-forward and OSCORE class E/U handling. | Partial | UDP/DTLS endpoint-to-endpoint Q-Block with OSCORE is implemented: Q-Block options use the default Class E handling and are encrypted. [OSCORE message tests](../../net/oscore/protect_test.go), [UDP integration tests](../../udp/oscore_test.go), [DTLS integration test](../../dtls/oscore_test.go), and [pinned-libcoap tests](../../udp/client/libcoap_oscore_test.go) cover the protected paths. Proxy forwarding remains unsupported; direct processing does not permit forwarding Q options. |
| 4.2: NUM/M/SZX layout; absent versus zero; UDP SZX limits. | Implemented | [Option definitions](../../message/option.go), [codec](../../net/blockwise/qblock/codec.go), [wire tests](../../udp/coder/qblock_test.go), [message tests](../../message/qblock_test.go). BERT/reliable Q is not implemented. |
| 4.3: stable Request-Tag per body, unique for different bodies. | Implemented with caller responsibility | [Sender](../../net/blockwise/qblock/client.go) generates a tag when absent and preserves it on retransmissions. Caller-supplied tags must not be reused for different concurrent bodies. [Raw-wire tests](../../udp/qblock_semantics_test.go) cover tag semantics, including empty tags. |
| 4.3: initial and retransmitted payloads in increasing NUM order. | Implemented, including reports spanning sets | [Sender](../../net/blockwise/qblock/client.go) recovers every validated NUM in the reported order; [unit regression](../../net/blockwise/qblock/client_final_test.go) checks multiple sets and rejects out-of-range reports before sending. [Configured recovery tests](../../udp/qblock_config_test.go) cover normal transfer behavior. |
| 4.3: final/Continue/4.08 responses use a received exchange token. | Implemented | [Q1 receiver](../../net/blockwise/qblock/receive.go), [token routing](../../udp/client/qblock.go), [token lifecycle tests](../../udp/client/qblock_internal_test.go). |
| 4.3: FETCH Observe response token remains the notification token. | Implemented for body-bearing FETCH Observe | Q-Block selection now permits FETCH Observe, sends the registration body as Q1 with Q2 capability, preserves the NUM=0 registration token through upload completion, and routes the initial/future Q2 representations and cancellation lifecycle. [End-to-end regression](../../udp/qblock_observe_test.go) checks the FETCH body, initial and updated multiblock representations, and cancellation. Exhaustive method/interop coverage remains open. |
| 4.3: Continue MUST NOT be generated for every received Q1 request; SHOULD only acknowledge complete NON sets, SHOULD NOT be generated for CON. | Implemented for CON; broader timing coverage remains unverified | Incomplete CON payloads receive only the transport's empty ACK; the Go sender advances across acknowledged sets and awaits the final Q1 response. NON complete-set Continue and missing-block reports are unchanged. [Receiver](../../net/blockwise/qblock/receive.go), [sender](../../net/blockwise/qblock/client.go), [raw-wire regression](../../udp/qblock_semantics_test.go), [Go/libcoap progress test](../../udp/qblock_stable_test.go), [native client/server matrix](../../udp/qblock_libcoap_test.go), and [fault tests](../../udp/qblock_loss_test.go) cover dropped/duplicated empty ACKs and final Q2 blocks, Q1 retry exhaustion, Q2 response reordering/retry exhaustion, exact body delivery, and single application dispatch. Broader NSTART congestion and delayed-stale-packet scenarios remain unverified. |
| 4.3: missing Request-Tag/Size1 returns 4.00; unsupported CON Q returns 4.02. | Implemented | [Receiver validation](../../net/blockwise/qblock/receive.go), [wire-negative tests](../../udp/qblock_semantics_test.go), [disabled critical-option tests](../../net/client/qblock_critical_test.go). |
| 4.3, 5: CBOR 4.08 selects retransmission with identical NUM/SZX/M/Tag/Size1; non-CBOR 4.08 is ordinary incomplete handling. | Implemented | [Sender](../../net/blockwise/qblock/client.go) treats CBOR missing-block reports selectively and returns non-CBOR 4.08 responses to the caller with the original exchange token; [client regression](../../net/blockwise/qblock/client_final_test.go) checks the status, token, body, and that no selective retransmission is sent. Full native interoperability coverage remains open. |
| 4.3: duplicate payloads do not redeliver the body; bounded partial retention and token cleanup. | Implemented | [Receiver](../../net/blockwise/qblock/receive.go), [assembly tests](../../net/blockwise/qblock/transfer_test.go), [duplicate delivery tests](../../udp/qblock_semantics_test.go), [budget tests](../../net/blockwise/qblock/engine_test.go). Exhaustion coverage remains incomplete for Q1 end-to-end. |
| 4.4: M/NUM request ranges and deduplication of overlapping Q2 requests. | Implemented | [Q2 request expansion](../../net/blockwise/qblock/send.go), `TestQBlock2RequestedSets` in [engine tests](../../net/blockwise/qblock/engine_test.go), [configured boundaries](../../net/blockwise/qblock/config_test.go). |
| 4.4: all body blocks have the same ETag; different representations have different ETags. | Implemented with application responsibility | [Response cache](../../net/blockwise/qblock/send.go) preserves an application ETag or generates one from the body. Applications must not reuse an explicit ETag for different data. [Observe replacement tests](../../net/blockwise/qblock/observe_test.go). Hash-generated ETags do not provide mathematical collision freedom. |
| 4.4: recovery requests contain increasing, duplicate-free Q2 values; invalid order SHOULD get 4.00. | Implemented | [Receiver requests](../../net/blockwise/qblock/client_receive.go), [server validation](../../net/blockwise/qblock/send.go), [wire negatives](../../udp/qblock_semantics_test.go). |
| 4.4: ignore duplicate Q2 payloads with the same ETag. | Implemented | [Assembly](../../net/blockwise/qblock/transfer.go), [receiver](../../net/blockwise/qblock/client_receive.go), [assembly tests](../../net/blockwise/qblock/transfer_test.go). Conflicting data at an already received NUM is treated as invalid input, not a second delivery. |
| 4.4: client SHOULD retain partial data for min(NON_PARTIAL_TIMEOUT, Max-Age), release tokens, omit ETag from missing requests. | Implemented for positive Max-Age; intentional zero-age SHOULD deviation | [Receiver](../../net/blockwise/qblock/client_receive.go) uses the smaller lifetime for positive Max-Age and removes ETag from control requests. Max-Age=0 retains assembly for NON_PARTIAL_TIMEOUT so an immediately stale representation can still complete; it does not become fresh cached data. [Lifecycle regressions](../../net/blockwise/qblock/observe_lifecycle_test.go) cover zero-age assembly and terminal responses. Short positive age and expiry-during-recovery tests remain open. |
| 4.4, 8: cache a response for recovery; changed data uses latest ETag/Size2, invalidates the old partial body. | Implemented; broader expiry coverage open | [Response cache](../../net/blockwise/qblock/send.go), [Observe replacement](../../net/blockwise/qblock/observe_test.go). Server retention is min(NON_PARTIAL_TIMEOUT, max(NON_RECEIVE_TIMEOUT, Max-Age)), including Max-Age=0, without increasing the advertised freshness. [Retention tests](../../net/blockwise/qblock/retention_test.go) cover zero, short, longer and capped ages. Section 8 leaves cache lifetime implementation-specific. |
| 4.4: CON piggyback ACK then CON payloads; failed transmission SHOULD terminate body transfer. | Implemented path; exhaustive failure timing unverified | [Scheduler](../../net/blockwise/qblock/schedule.go), [native matrix](../../udp/qblock_libcoap_test.go), [stable recovery regression](../../udp/qblock_stable_test.go). ACK payloads are interpreted as the CON family by [receiver type tests](../../net/blockwise/qblock/rfc_test.go). |
| 4.5: consistent Observe value on all original payloads; omit Observe from Q2 missing requests and responses. | Implemented for GET and body-bearing FETCH Observe | [Observe engine](../../net/blockwise/qblock/observe.go), [receiver](../../net/blockwise/qblock/client_receive.go), [scheduler](../../net/blockwise/qblock/schedule.go), [GET and FETCH lifecycle tests](../../udp/qblock_observe_test.go). |
| 4.6: exact Size1/Size2 on every applicable payload, invariant within a body. | Implemented | [Q1 receiver](../../net/blockwise/qblock/receive.go), [Q2 receiver](../../net/blockwise/qblock/client_receive.go), [assembly tests](../../net/blockwise/qblock/transfer_test.go), [wire negatives](../../udp/qblock_semantics_test.go). |
| 4.7: Q1 request body and Q2 response body together. | Implemented | [Completion path](../../net/blockwise/qblock/receive.go), PUT echo in [native matrix](../../udp/qblock_libcoap_test.go) and [configured transfers](../../udp/qblock_config_test.go). |
| 4.8: server MUST ignore multicast Q2 requests. | Implemented | [Transport](../../udp/client/qblock.go), `TestQBlockMulticastRequestsAreIgnored` in [transport tests](../../udp/client/qblock_internal_test.go). Existing legacy multicast is unchanged. |
| 5: missing-block payload is a CBOR unsigned-integer sequence, no array wrapper, ascending and duplicate-free on emission. | Implemented | [Codec](../../net/blockwise/qblock/codec.go), [codec tests and fuzzers](../../net/blockwise/qblock/codec_test.go). Decoder accepts duplicate entries once as required. Malformed/descending reports produce an explicit error rather than being silently dropped (SHOULD). |
| 5: a 4.08 report MUST fit a single datagram by limiting the number of missing blocks. | Implemented for Q1 and Q2 client control requests | [Q1 reports](../../net/blockwise/qblock/receive.go) use the configured datagram budget, including the response token, options, payload marker, MTU, DTLS reserve, and OSCORE reserve, and send the largest ascending CBOR prefix that fits. Subsequent reports advance as holes are filled; an unrepresentable first item fails explicitly. [Q1 MTU regression](../../udp/qblock_mtu_test.go) checks a 256-block set, high NUM, long URI-Path/eight-byte token, packet size, and eventual upload completion. [Q2 request sizing](../../net/blockwise/qblock/client_receive.go) validates each repeated-option candidate against the actual transport budget and splits oversized lists across requests; [integration coverage](../../udp/qblock_loss_test.go) drops 95 of 100 response blocks and verifies request datagram bounds and eventual completion. OSCORE expansion and DTLS reserves are included in transport validation. |
| 6: per-request tokens, received-token association, retire tokens and Reset stale responses. | Implemented paths; exhaustive late traffic unverified | [Routes](../../udp/client/qblock.go), [token tests](../../udp/client/qblock_internal_test.go), [Observe Reset tests](../../udp/qblock_observe_reset_test.go). Add late aliases across cancellation/expiry/re-registration and non-Observe Reset packet assertions. |
| 7: all body blocks either CON or NON. | Implemented | Q1 validates the stored mode; Q2 validates each representation's logical type, treating piggyback ACK as CON. [Negative/positive response type tests](../../net/blockwise/qblock/rfc_test.go). |
| 7.2: configurable MAX_PAYLOADS, default 10, equal at both peers. | Implemented; peer coordination required | [Config](../../net/blockwise/qblock/config.go), [option propagation/startup tests](../../options/qblock_options_test.go), [UDP/DTLS set-size tests](../../udp/qblock_config_test.go). No protocol negotiation exists for the value. |
| 7.2: per-body randomized set delay, receive guard of at least one second, exponential retry wait, derived partial/probing lifetimes. | Implemented | [Config and formula tests](../../net/blockwise/qblock/config_test.go), [sender](../../net/blockwise/qblock/client.go), [scheduler](../../net/blockwise/qblock/schedule.go), [configured probing cap](../../udp/client/qblock_internal_test.go). Integer duration arithmetic rounds fractional nanoseconds. |
| 7.2: pace each NON set, prioritize missing sets, Continue can permit immediate progress. | Implemented paths; fault matrix incomplete | [Scheduler](../../net/blockwise/qblock/schedule.go), [paced burst tests](../../net/blockwise/qblock/engine_test.go), [configured selective recovery](../../udp/qblock_config_test.go), [seeded loss](../../udp/qblock_seeded_loss_test.go). |
| 7.2: retry exhaustion SHOULD discard stale body and tokens. | Implemented; broader cleanup evidence remains | [Receiver](../../net/blockwise/qblock/client_receive.go), [Q1 timer](../../net/blockwise/qblock/receive.go), [deterministic Q2 retry test](../../net/blockwise/qblock/config_test.go), [end-to-end Q1/Q2 exhaustion tests](../../udp/qblock_loss_test.go), [budget cleanup tests](../../net/blockwise/qblock/observe_test.go). |
| 8-9: Q-aware proxy cache, forwarding, Request-Tag replacement, HTTP mapping. | Outside scope | No proxy/HTTP Q implementation is advertised. Needs its own cache-key, body replacement, and bidirectional interoperability tests before enabling. |
| 11: NoSec is NOT RECOMMENDED. | Documented recommendation, not a transport ban | [Guide](qblockwise.md) recommends authenticated DTLS; plain UDP is opt-in through Q mode. DTLS native tests use a public loopback-only test PSK. |

## Prioritized remaining work

### P1: close supported-scope protocol gaps

1. **CON Q1 broader fault timing.** Extend the relay matrix to cover NSTART
   congestion, packet reordering between independent uploads, and delayed stale
   packets; assert exact body delivery and no duplicate application dispatch.

### P2: complete normative edge-case evidence

- Run the native libcoap method matrix in the Q-enabled CI environment for
  POST/FETCH/PATCH/iPATCH; local validation needs the pinned Q-enabled binaries.
- Test external non-CBOR 4.08 behavior with native peers; verify ascending
  retransmission, stale tokens, retry exhaustion, and cleanup.
- Resolve/test Max-Age zero freshness versus server retransmission retention;
  cover short age, default age, expiry during recovery, ETag and Observe
  replacement, and application-supplied tag/ETag contracts.
- Add exact timing/count assertions for Q1 retry exhaustion, missing final
  payloads/Continue/final responses, and complete reverse-path blackholes.
  Keep deterministic timer tests separate from noisy wall-clock tests.

### P3: release-quality performance and reliability

- Soak asymmetric/burst loss, duplication, reordering, cancellation and repeated
  notifications with both pinned libcoap versions, Q enabled and disabled.
- Benchmark 1/16/128 concurrent transfers, cold/concurrent discovery, scheduler
  goroutines, peak/steady-state retained heap, and legacy-disabled fast-path
  regressions against explicit statistical thresholds.
- Preserve bounded per-peer/process budgets. The current 16 MiB/65,536-block body
  limits and MAX_PAYLOADS cap are deliberate resource policies, not failures of
  wire-option range support. Application delivery still needs body-sized storage;
  this is not a streaming API.

Review regressions additionally cover combined per-engine quotas (32 MiB and
128 transfer/observation slots, below the process-wide 256 MiB/8,192 limits),
atomic reservation rollback and cleanup, empty-body missing lists, rejection of
NUM at the end of a nonempty body, terminal Observe delivery/cleanup,
single-payload CON sets without recovery, and Q1 timer overflow guards.
[Budget tests](../../net/blockwise/qblock/budget_test.go) exercise isolation and
cleanup. [Scheduler tests](../../udp/qblock_scheduler_test.go) withhold a CON ACK
while an unrelated NON body progresses; scheduled CON sends use bounded
asynchronous ACK waits without changing NSTART.
[MTU tests](../../udp/qblock_mtu_test.go) assert actual UDP/DTLS Q response sizes
against the configured server budget. Download benchmarks rotate warmed peers
outside timed batches so retained response caches cannot exhaust the quotas as
the benchmark runner increases its iteration count.
[Terminal Observe integration](../../udp/qblock_observe_terminal_test.go) keeps
a notification body incomplete, asserts exactly-once terminal 2.05/4.02/4.04
delivery and observation cancellation, recovers lost terminal-body payloads,
and reuses the registration token to verify route cleanup. Terminal bodies stay
on Q-Block2; the server retires the registration but retains the bounded response
cache for recovery. [Generation regressions](../../net/blockwise/qblock/observe_generation_test.go)
also cancel and re-register during notification snapshot reads: cache installation
must still match the exact registration generation. An absent Observe option on
the registration token in an unblocked response or Q-Block2 NUM=0 terminates
reception; later payloads can omit Observe even on that token. Omission on a recovery token
does not itself terminate a notification or turn 4.02 into a terminal response.

Additional [receiver regressions](../../net/blockwise/qblock/review_regression_test.go)
cover request monitors consuming upload bodies, effective default/custom Size1
limits in 4.13 responses, and recovery after an Observe replacement changes
between NON and CON (including piggyback ACK normalization).
[Observation ownership tests](../../net/observation/ownership_test.go) verify that
callback release/reuse cannot change whether a notification terminates the
observation.

### Separate feature scope

Proxy, tokenless Q, reliable-transport Q/BERT, TCP/TLS Q-Block, and explicit
derived-timer overrides need independent designs and acceptance tests. Their
absence must not be confused with a failure of ordinary legacy/TCP behavior,
but they must not be advertised as supported RFC 9177 surfaces.

## Reproducible validation

Focused configuration/formula, malformed-family/type, startup, recovery and
transport tests:

```sh
CGO_ENABLED=1 go test -race ./net/blockwise/... ./options ./udp/client \
  -count=1 -timeout=3m
CGO_ENABLED=1 go test -race ./udp -run '^TestQBlockConfigured' \
  -count=1 -timeout=3m
```

[Configured interoperability tests](../../udp/qblock_config_test.go) also exercise
both native roles over UDP/DTLS with altered timers and MAX_PAYLOADS=10, proving
actual Q traffic rather than legacy fallback. To include them in the complete
required Q-enabled native matrix:

```sh
LIBCOAP_QBLOCK_REQUIRED=1 LIBCOAP_QBLOCK_MODE=enabled CGO_ENABLED=1 \
  go test -race ./udp -run '^TestQBlockLibcoap' -count=1 -timeout=15m
```

Use the pinned references from the [CI workflow](../../.github/workflows/test-qblock.yml).
The configured-timer case does not apply to a Q-disabled binary; the separate
disabled-reference matrix checks fallback and Q-only rejection. Successful
interoperability, even with no skips, does not close the gaps above.
