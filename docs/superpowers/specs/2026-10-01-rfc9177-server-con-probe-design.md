# RFC 9177 server CON single-block replies

Status: approved for inline implementation by the user: “So let's implement it according to the standard.” Baseline feat/qblock-foundation 834270eb7bd4932bf2a3c4680bcaac0e5b93e467.

## Protocol and scope

RFC9177 §4.1 requires CON discovery for reliable critical-option rejection; §4.4 request M=0 asks for one block, independently of response M. RFC7252 §§5.2.1–5.2.2 permit piggybacked ACK or empty ACK plus separate response. Once an empty ACK is selected, all duplicate request acknowledgements stay empty and the Content is sent separately. RFC7252 §4.5 recommends same-ACK duplicate handling and permits relaxed idempotent GET handling. RFC7967 No-Response suppression remains effective.

This slice implements unicast CON GET with exactly one QBlock2, M=0, legal NUM and SZX0–6, no body/Observe/QBlock1/classic blocks. Empty token is legal; Request-Tag is optional. Clamp response SZX to the shared local ceiling while preserving the requested byte offset. Reject invalid/out-of-range block requests with Bad Request; malformed/conflicting critical Q options with Bad Option. Other CON Q transfers remain unsupported; no full CON body sender or Milestones5–6.

Current enabled server interception silently consumes CON requests because its NON control validator rejects them. Introduce a dedicated bounded single-block path before ordinary duplicate-cache lookup. Outbound-only connections retain disabled-Q rejection.

## Records and handler

Use a private table by remote request MID with exact method/token/options identity. Pending duplicates never redispatch. Frozen piggyback ACKs resend identically; after empty ACK, duplicates resend only that empty ACK. Conflicting live MID identities are dropped. The remote MID belongs to the receive namespace, not the local sender namespace.

Share MaxRecords, MaxMetadataBytes and owned server envelopes with existing NON records. Count records still owned by running callbacks even after expiry, preventing repeated admissions from accumulating callbacks. Retain callback and write leases until they finish. Capture token/options/source control after admission; callbacks execute outside all coordinator locks. Handler receives original CON metadata and connection-derived bounded context.

Read/hash at most MaxBodySize+1 with fixed streaming scratch, restore body cursor, retain only the requested block. Use supplied valid ETag or generate an 8-byte digest. Size2 is the exact complete representation size. Response M indicates bytes beyond the selected block. Preserve valid application metadata; normalize QBlock2/Size2/ETag and omit incompatible block/Observe metadata. Oversized representation/read failure/invalid handler metadata returns bounded Internal Server Error; invalid block offset returns Bad Request. If a minimum block and metadata cannot fit, return Request Entity Too Large. Application errors keep their code and bounded payload without Q metadata. Unmodified or suppressed responses send only empty ACK, never a fabricated positive response. Direct Message mutation is also subject to No-Response.

## ACK choice and separate delivery

Allow prompt handler completion to publish a piggyback ACK. A connection-owned acknowledgement deadline of min(1 second, configured ACK timeout/2) selects empty ACK for delayed work, using the existing shared scheduler. The original admission+ExchangeLifetime (247 seconds) bounds publication/duplicate retention. The 1-second delay is a local quality-of-implementation policy, not an RFC-mandated constant.

Separate Content uses original token, fresh local MID, CON type and captured source. Use existing endpoint admission for CON control traffic; retain the permit through ACK/Reset. Sample initial retransmission timeout from configured ACK timeout multiplied by 1+jitter/2 (RFC default random factor1.5). Retransmit the same response/MID with exponential backoff at most configured MaxRetransmit times; then wait the next timeout before failure. The shared Q scheduler drives retransmission and absolute expiry; no unbounded per-record timers/goroutines. ACK or Reset matching response MID terminates retransmission regardless of attached response payload; feedback ownership cannot settle another exchange. Do not install an ordinary transient async MID handler.

Serialize ACK choice, publication and retransmission decisions with the shared action gate; no writes/callbacks under client.mu. Empty ACK must be written before separate response attempt. Check current record, open connection and absolute expiry before every publication/write. Expiry/close unpublishes response MID ownership and cancels context before releasing owner leases; late handler/write completion cannot affect a successor. Report errors outside locks. Completed request suppression expires at first admission+ExchangeLifetime, never extended by duplicates.

## Acceptance and limits

Tests cover short/long/empty bodies, requested offset/SZX ceiling, supplied/generated ETag, cursor restoration, No-Response, malformed requests, admission and metadata/body/datagram limits, duplicate/conflicting identities, pending timer versus handler completion, separate ACK/Reset/retransmission exhaustion, expiry/close and held callback/write accounting. Real UDP and PSK DTLS ProbeQBlock followed by Require exchanges establish local endpoint integration, not independent libcoap interoperability.

Run focused normal/race, unfiltered net/qblock race, compile-only/vet and full runtime. Exactly one fresh Astra/high reviewer checks this new implementation range; material findings receive one TDD fix pass. Keep branch/worktree/all ledgers and preserved edits; no merge/push. Server reply completion is a bounded Milestone4 slice, not whole-milestone completion.

## Historical proposal and rulings

The initial uncommitted proposal chose piggyback-only, fixed NUM0/SZX0, generated ETag only and a discovery No-Response exception. RFC/libcoap investigation showed these were local restrictions, not standards requirements. User directed standards implementation; this revision replaces those choices and includes separate response ownership. Pinned libcoap source c63c8f7cb7f248a4992539529b9e1b691962f29a implements M0 as single_request without a continuing body sender, supplied/digest ETags, ordinary async CON responses and No-Response. Its last-ACK cache is less strict than our bounded identity table. Source inspection is not runtime interop evidence.
