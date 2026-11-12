# OSCORE (RFC 8613)

OSCORE protects CoAP request and response messages end to end using
application-provisioned security contexts. It supports the four AES-CCM
algorithm suites with a 13-byte nonce described below. Contexts are
pre-established; OSCORE does not perform key establishment or identity
provisioning.

The [RFC requirements audit](oscore-rfc-audit.md) records implementation
evidence, known gaps, and unsupported features. The repository
[threat model](../../.oss-scanner/threat_model.md) describes the security
boundaries and test scope.

## Algorithm selection

Set `ContextConfig.Algorithm` to the same COSE algorithm on both peers. Its
zero value preserves the existing AES-CCM-16-64-128 default:

| Go constant | COSE ID | Key | Tag |
|---|---:|---:|---:|
| `AEADAlgorithmAESCCM16_64_128` | 10 | 128 bits | 64 bits |
| `AEADAlgorithmAESCCM16_64_256` | 11 | 256 bits | 64 bits |
| `AEADAlgorithmAESCCM16_128_128` | 30 | 128 bits | 128 bits |
| `AEADAlgorithmAESCCM16_128_256` | 31 | 256 bits | 128 bits |

For example, select `oscore.AEADAlgorithmAESCCM16_128_256` in both peers'
`ContextConfig` values. Algorithm 10 is the default. Algorithm 11 has a
64-bit authentication tag and therefore
offers less forgery resistance than the 128-bit-tag variants. Peers must use
matching algorithms; mismatches fail authentication. Other COSE AEAD suites
and AES-CCM variants that require a nonce other than 13 bytes are unsupported.

AES-256 with a 128-bit CCM tag (algorithm 31) is the strongest supported
choice against generic quantum search over the symmetric key: Grover's
algorithm is commonly estimated to reduce AES-256's effective key-search
strength to about 128 bits. This is not a claim that the complete OSCORE
deployment is post-quantum secure. OSCORE here uses HKDF-SHA-256 and
application-provisioned master secrets, not a post-quantum key-establishment
protocol; the security of provisioning and any external key exchange remains
separate.

## Context provisioning

The two peers must provision matching master secret, master salt, and optional
ID Context values. Each peer's Sender ID is the other peer's Recipient ID.
Provision these values through a trusted mechanism; do not embed production
secrets in source code or logs.

This example uses public test-only material:

```go
import (
	"github.com/plgd-dev/go-coap/v3/net/oscore"
)

func newOSCOREProcessor(senderID, recipientID string) (*oscore.Processor, error) {
	ctx, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret:          []byte("0123456789abcdef"),
		MasterSalt:            []byte("salt1234"),
		SenderID:              []byte(senderID),
		RecipientID:           []byte(recipientID),
		AcceptReplayStateLoss: true, // Use only for examples or deliberate volatile-state deployments.
	})
	if err != nil {
		return nil, err
	}
	return oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts:        []*oscore.Context{ctx},
		PlaintextPolicy: oscore.PlaintextDisabled,
	})
}
```

For this example, configure the server with
`newOSCOREProcessor("server", "client")` and the client with
`newOSCOREProcessor("client", "server")`.

## Direct `net/oscore` API

The public API separates state by responsibility:

- `Context` owns derived keys, sender sequence allocation, recipient replay
  state, algorithm, and identifiers.
- `Processor` selects configured contexts and enforces the endpoint's
  plaintext policy.
- `Exchange` binds a request to response protection and replay handling.
  Keep the returned value with its request; copies share lifecycle state.
  Read request metadata through `RequestIdentifier()`,
  `RequestPartialIVBytes()`, and `IsObserve()`.
- Transports attach `RequestInfo` to the handler context. Use
  `oscore.RequestInfoFromContext(req.Context())` to distinguish protected
  requests and inspect the inbound Recipient ID and ID Context without
  exposing key material or the Exchange.
- A transport owns CoAP retransmission, Token/MID tracking, Observe delivery,
  and Blockwise/Q-Block integration. Direct `net/oscore` calls do not provide
  those transport services.

`ContextConfig` byte slices are consumed during construction; retained
identifiers are copied. The master secret must be high-entropy provisioned
material, not a password. Go does not guarantee complete zeroization of all
temporary secret copies. `Processor.RemoveContext(recipientID, idContext)`
prevents future context selection but is not immediate key zeroization or hard
revocation: already-bound Exchanges and operations holding a pre-removal
context snapshot may complete, and the original Context remains available to
them. Removing a context does not make it safe to restart or reuse its sender
sequence numbers.
The Processor retains sender key/common-IV fingerprints after removal to
prevent nonce reuse. Therefore, after `RemoveContext`, calling `AddContext`
with a newly constructed Context that has the same sender security context
fails with `ErrInvalidContext` ("duplicate sender key and common IV") by
design. Re-adding the exact same Context instance is allowed; restarting it
with reset sender sequence state is not.

`Processor.UnprotectResponse(response, context, exchange)` accepts only the
same Context instance that created the supplied Exchange. An Exchange remains
usable for an in-flight response after that Context is removed from the
Processor.

Low-level primitives such as `SealResponseWithRequestNonce`,
`OpenResponseForExchange`, `BuildAAD`, and `EncodeInner` remain exported for
advanced use. Encryption/decryption primitives do not provide the same
Exchange binding, one-shot response, replay, and ordering guarantees as the
high-level message methods. In particular, repeated direct use of a request
nonce can reuse an AEAD nonce. Prefer the high-level `ProtectRequest`,
`ProtectResponse`, `UnprotectRequest`, and `UnprotectResponse` methods for
message processing.

### API stability

The OSCORE API is new and has not been released. Its API may therefore evolve
before stabilization without preserving compatibility with intermediate
unreleased revisions. This does not authorize changes to APIs that already
exist in the repository's `master` branch; those remain unchanged.

`Exchange` is intentionally opaque. Read its request metadata through
`RequestIdentifier()`, `RequestPartialIVBytes()`, and `IsObserve()` rather than
mutable fields. The endpoint plaintext booleans are replaced by
`ProcessorConfig.PlaintextPolicy`: use `PlaintextDisabled` instead of
`RequireOSCORE: true`, and `PlaintextAllowed` instead of
`AllowUnprotected: true`. `SenderSequenceReserver` is the current name for the
sequence-reservation interface.

With the current API, a plaintext request rejected by
`PlaintextDisabled` returns `oscore.ErrPlaintextDisabled`;
`oscore.ErrOSCORERequired` continues to identify a plaintext response where a
client transport requires an OSCORE-protected response. Match these errors
with `errors.Is`.

The following is a complete direct message-processing example. Application
errors are protected in the same way as successful responses. A second attempt
to protect an ordinary response on the same Exchange returns `oscore.ErrReplay`;
retransmission must resend the already protected message instead of encrypting
different plaintext.

```go
import (
	"errors"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/net/oscore"
)

func directOSCOREExample() error {
	masterSecret := []byte("public test-only secret")
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret:          masterSecret,
		SenderID:              []byte("client"),
		RecipientID:           []byte("server"),
		AcceptReplayStateLoss: true,
	})
	if err != nil {
		return err
	}
	serverContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret:          masterSecret,
		SenderID:              []byte("server"),
		RecipientID:           []byte("client"),
		AcceptReplayStateLoss: true,
	})
	if err != nil {
		return err
	}
	clientProcessor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts:        []*oscore.Context{clientContext},
		PlaintextPolicy: oscore.PlaintextDisabled,
	})
	if err != nil {
		return err
	}
	serverProcessor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts:        []*oscore.Context{serverContext},
		PlaintextPolicy: oscore.PlaintextDisabled,
	})
	if err != nil {
		return err
	}

	protectedRequest, exchange, err := clientProcessor.ProtectRequest(
		message.Message{Code: codes.GET, Token: message.Token{1}},
	)
	if err != nil {
		return err
	}
	request, _, serverExchange, err := serverProcessor.ProcessIncomingRequest(protectedRequest)
	if err != nil {
		return err
	}
	// Dispatch the authenticated request to the application.
	protectedResponse, err := serverContext.ProtectResponse(
		message.Message{Code: codes.NotFound, Token: request.Token, Payload: []byte("not found")},
		serverExchange,
	)
	if err != nil {
		return err
	}
	_, duplicateErr := serverContext.ProtectResponse(
		message.Message{Code: codes.NotFound, Token: request.Token, Payload: []byte("different plaintext")},
		serverExchange,
	)
	if !errors.Is(duplicateErr, oscore.ErrReplay) {
		return errors.New("duplicate response protection was not rejected")
	}
	response, err := clientProcessor.UnprotectResponse(protectedResponse, clientContext, exchange)
	if err != nil {
		return err
	}
	// The application receives the authenticated 4.04 response.
	_ = response
	return nil
}
```

The matching executable example is `ExampleContext_directAPI` in
[`net/oscore/example_test.go`](../../net/oscore/example_test.go).

For Observe and Q-Block exchanges, use a fresh Partial IV for each subsequent
notification or response with `ProtectResponseWithPartialIV`. The Context
allocates unique Partial IVs; applications producing notifications
concurrently should serialize them in intended notification order.

## Configure endpoints

Pass the processor through `options.WithOSCORE`. The option is available for
UDP, DTLS, TCP, and TLS clients and servers. For example:

```go
import (
	"log"

	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
)

processor, err := newOSCOREProcessor("client", "server")
if err != nil {
	log.Fatal(err)
}
cc, err := udp.Dial("127.0.0.1:5683", options.WithOSCORE(processor))
if err != nil {
	log.Fatal(err)
}
defer cc.Close()
```

OSCORE is opt-in. Endpoints that do not configure it retain their existing
behavior. `ProcessorConfig.PlaintextPolicy` controls incoming requests only:
its zero value, `PlaintextDisabled`, rejects plaintext requests with
`oscore.ErrPlaintextDisabled`; `PlaintextAllowed` accepts both plaintext and
OSCORE-protected requests; and `PlaintextOnly` accepts only plaintext requests,
rejecting protected requests with `oscore.ErrOSCOREDisabled`. Incoming
responses are handled according to their originating exchange, not this
policy; for example, a plaintext response to an outgoing request that requires
OSCORE is rejected with `oscore.ErrOSCORERequired`. This is an endpoint-wide
request policy. The UDP and TCP transport handlers receive the decrypted
request without its outer OSCORE option, so handlers cannot use that option to
identify protected requests. To enforce a per-resource policy on a mixed-mode
endpoint, call `Processor.ProcessIncomingRequest` directly and check the
returned context before dispatching: it is non-nil for a successfully
authenticated OSCORE request and nil for a plaintext request. Otherwise, use
separate endpoints when some resources must not accept plaintext.

Once an OSCORE option is present, malformed messages, unknown contexts,
authentication failures, and replays are rejected; they are never retried as
plaintext. Outgoing requests with multiple configured contexts require an
`OutgoingContextSelector`; without one, protection returns
`oscore.ErrAmbiguousContext`.

For an outstanding protected request, clients may receive the RFC 8613
processing errors 4.00, 4.01, or 4.02 without OSCORE protection. Such responses
are unauthenticated and may be spoofed by an on-path attacker; they can cause
denial of service but must not be treated as authenticated peer statements.
Check `oscore.IsUnprotectedProcessingError(response.Context())` before acting
on one.

Server endpoints can opt into sending these responses with
`options.WithOSCOREErrorResponses()`. It is disabled by default; when enabled,
UDP/DTLS replies only to Confirmable requests, while TCP responds on the stream.
Unknown contexts and replays map to 4.01, malformed OSCORE options to 4.02, and
other OSCORE processing failures to 4.00. Responses contain no diagnostic
payload and failed requests are never dispatched to the application handler.

## Sender sequence and receiver replay state

Partial IV values must never be reused with the same sender key. The default
in-memory sequence counter is appropriate only if that sender key will not be
reused after process restart. For durable deployments, configure a
`SenderSequenceStore` whose `Reserve` operation atomically reserves and durably
persists a unique value before returning it. Coordinate reservations across all
processes and `Context` instances that share a sender key. A reserved value
remains consumed even if protection or transmission later fails.

The file store reserves sender values in windows, so file locking prevents
reuse but does not guarantee that independently active stores transmit in
sequence order. For example, one instance may send sequence 256 before another
sends sequence 1; a receiver with the default 32-entry replay window will
reject the later low sequence as stale. Use a single active sender/store
instance for each sender context, or coordinate transmission so messages arrive
in sequence order.

Alternatively, restore `SenderSequenceNumber` to the next unused value before
constructing a context. Never restore an old or guessed value. The receiver
replay window is independent of sender sequence persistence. To preserve it
across receiver restarts, configure a `ReplayWindowStore` on `ContextConfig`.
`NewContext` requires either this store or the explicit
`AcceptReplayStateLoss: true` opt-in. The opt-in permits an in-memory replay
window only; after a restart, recorded requests may be accepted again. Use it
only when that loss is an intentional deployment choice, such as a transient
example. Production receivers should use a durable replay store or follow the
RFC recovery procedure rather than silently accepting state loss.
The store must load the state for that recipient context and persist it before
returning success; otherwise the protected request is not accepted. Use one
store per context and coordinate access if multiple receiver processes use
that context. A custom `ReplayWindowStore` that persists every complete
window advance can limit throughput. The file-backed `HighWaterFileStore`
implements the optional `ReplayWindowHighWaterStore` and instead persists one
exclusive cutoff per 256 accepted sequence advances. After restart, all
sequences below that cutoff are treated as replay, including requests that may
not have arrived before shutdown. This trades availability for fewer durable
writes. Omitting a replay store avoids that write cost but loses replay state
after restart; do not continue using that context without restoring replay
protection. Follow RFC 8613 Appendix B.1.2 recovery (for example, Echo) or
establish a fresh security context.

The `net/oscore/store` package provides `HighWaterFileStore`, which durably
reserves sender sequence windows of 256 values and atomically persists one
exclusive receiver replay cutoff per 256 sequence advances. After restart,
every sequence below the cutoff is treated as replay, including requests that
may not have arrived before shutdown; this intentionally trades availability
for fewer durable writes. Use only one active receiver `Context` for this
store. Rolling back its high-water file cannot be detected without an external
monotonic trust anchor. Sender reservations use a rollback guard and pending
transaction so startup can finish an interrupted reservation while consuming
its entire reserved window. Missing individual sender-state files, corrupt
files, and mismatched files are rejected.

```go
import (
	"github.com/plgd-dev/go-coap/v3/net/oscore"
	oscorestore "github.com/plgd-dev/go-coap/v3/net/oscore/store"
)

state, err := oscorestore.NewHighWaterFileStore("/var/lib/myapp/oscore", "peer-17")
if err != nil {
	return err
}
ctx, err := oscore.NewContext(oscore.ContextConfig{
	MasterSecret:        masterSecret,
	SenderID:            senderID,
	RecipientID:         recipientID,
	SenderSequenceStore: state,
	ReplayWindowStore:   state,
})
```

Use the same stable store ID when recreating the same local OSCORE context and
a different ID for each distinct context. The store creates files with
restrictive permissions and atomically syncs updates. On Unix it coordinates
multiple processes with file locks; on other platforms it only coordinates
within one process and cannot sync directory metadata. Keep its directory
writable only by the trusted application account. The store does not persist
master secrets or encrypt state at rest. It detects sender-state corruption,
missing individual sender-state files, and sender-state rollback, but cannot
detect rollback of the complete high-water file without an external monotonic
trust anchor. Reserved but unused sender values are intentionally skipped
after restart. Applications can implement `SenderSequenceStore` and
`ReplayWindowHighWaterStore` for a database or remote transactional store.

## Minimal forward proxy

The `net/oscore/proxy` package provides a basic `mux.Handler` and a
transport-neutral `RoundTripper` interface for one-request/one-response
forwarding. A proxy does not need the OSCORE context: it routes using the
outer authority and forwards the OSCORE option and ciphertext unchanged. The
origin server performs OSCORE verification and decryption.

On the OSCORE client, provide the full target URI only as a `Proxy-Uri` option.
`Context.ProtectRequest` follows RFC 8613 by moving the URI path and query into
the encrypted inner message and retaining only the scheme and authority in the
outer `Proxy-Uri`. Do not combine `Proxy-Uri` with separate `Uri-*` options.
For example, build a GET with an empty path and a Proxy-Uri option, then send it
with `Do`:

```go
import (
	"github.com/plgd-dev/go-coap/v3/message"
)

req, err := cc.NewGetRequest(ctx, "", message.Option{
	ID:    message.ProxyURI,
	Value: []byte("coap://origin.example/sensors/temp"),
})
if err != nil {
	return err
}
defer cc.ReleaseMessage(req)
resp, err := cc.Do(req)
if err != nil {
	return err
}
defer cc.ReleaseMessage(resp)
```

Configure the proxy with a `RoundTripper` for the target transport and an
explicit allowlist `AllowTarget` policy. A nil policy rejects all forwarding;
do not expose an unrestricted proxy, since arbitrary targets can create an
SSRF path. If hostnames are allowed, resolve them in the policy and pin the
selected IP in the `RoundTripper`; validating one DNS answer and dialing a
later answer permits DNS rebinding. `AllowPublicIPLiteralTarget` is a helper
for policies that intentionally accept only literal public IP addresses.
The round tripper receives the context, parsed target, and a copied
CoAP message whose OSCORE option and ciphertext are unchanged. It should use a
fresh connection per request unless it safely handles token collisions.
Register `Forwarder.ServeCOAP` as a mux handler, for example as the router's
default handler.

This minimal path does not implement caching, Observe subscriptions, blockwise
session management, proxy authentication, or cross-protocol HTTP forwarding.
Applications are responsible for DNS/IP allowlisting, transport security,
timeouts, and response-size limits.

## Supported scope and limitations

- HKDF-SHA-256 and AES-CCM-16-64-128, AES-CCM-16-64-256,
  AES-CCM-16-128-128, and AES-CCM-16-128-256. The default remains
  AES-CCM-16-64-128.
- UDP, DTLS, TCP, and TLS transports.
- TCP OSCORE exchanges use a 247-second response timeout by default.
  Configure `options.WithOSCOREExchangeTimeout` to extend it for slow origins;
  zero disables timeout expiry. This is a local exchange-retention policy, not
  an RFC-defined TCP lifetime.
- Classic Block1/Block2 and Observe flows. Inner Blockwise remains the
  default. UDP and DTLS also support opt-in Outer Blockwise fragmentation and
  reassembly with `options.WithOSCOREOuterBlockwise()`; reassembled payloads
  default to a 65,536-byte limit. Configure a different cap with
  `options.WithOSCOREOuterBlockwiseMaxReassembledPayloadSize(size)`; a size
  of zero selects the default. The cap applies only to messages carrying the
  OSCORE option; plaintext Blockwise transfers are unaffected. The existing
  Block1 body-size limit remains independent, and the stricter cap applies to
  Block1 uploads. Protected client requests exceeding the cap are rejected
  locally, and UDP/DTLS endpoints replace oversized protected responses with
  a protected 4.13 response. Proxies reject oversized protected responses
  locally because they cannot create a protected error response. The option
  does not automatically fall back to Inner Blockwise when an Outer
  reassembled payload would exceed the cap; this is a documented deviation
  from RFC 8613's SHOULD to use Inner Blockwise for such messages. The option
  requires Blockwise to be enabled and OSCORE processing or proxy mode.
  Observe notifications use fresh Partial IVs; stale or duplicate
  notifications are rejected.
- No-Response suppression is preserved for protected requests over UDP, DTLS,
  TCP, and TLS; the application processes each request while the suppressed
  response is not sent.
- Receiver replay-state policy is explicit: use `ReplayWindowStore` for
  durable persistence or set `AcceptReplayStateLoss` to opt into volatile
  replay state; storage durability and coordination are application
  responsibilities.
- Q-Block1 uploads and Q-Block2 downloads over OSCORE-protected UDP and DTLS.
  Q-Block options are encrypted as Class E options. Q-Block is a datagram
  protocol (RFC 9177); TCP/TLS use reliable-transport Blockwise instead.
- Pre-established unicast contexts only. Group OSCORE, automatic context
  establishment (including EDHOC), and multicast OSCORE are unsupported. The
  `net/oscore/proxy` package provides only the minimal forwarding scope above;
  the surrounding UDP/DTLS Blockwise transport handles Outer Blockwise
  fragments. A `RoundTripper` forwarding large envelopes must likewise use a
  Blockwise-enabled transport; the package is not a full-featured CoAP proxy.
- Sender sequence and optional receiver replay-state persistence are provided
  by the application; safe operation depends on the stores' atomicity and
  durability.

Do not interpret the supported feature list as a claim of complete RFC
conformance. See the [RFC audit](oscore-rfc-audit.md) for known gaps and
verification status.

## Tests and interoperability

Run the OSCORE core, file store, and transport tests with:

```sh
go test ./net/oscore/... ./udp/client ./udp ./tcp ./dtls
```

The OSCORE libcoap tests require a libcoap build with OSCORE enabled. The
dedicated CI and OSS scanner jobs build a pinned libcoap and set the required
flag so missing tools or OSCORE support fail instead of silently skipping:

```sh
GOCOAP_REQUIRE_LIBCOAP_OSCORE=1 \
LIBCOAP_QBLOCK_REQUIRED=1 \
LIBCOAP_QBLOCK_MODE=enabled \
  go test ./udp/client -run 'TestLibCoAP(Server|Client).*OSCORE' -count=1
```

Without that flag, local libcoap interoperability tests skip when the required
OSCORE-enabled binaries are not available. The interoperability credentials are
public and loopback-only; do not reuse them in applications.

The pinned libcoap server test covers a multi-block Q-Block1 upload and
single-block Q-Block2 response. Its multi-block Q-Block2 echo response is not
used because it does not interoperate: at least one response lacks OSCORE
protection, which go-coap correctly rejects. A multi-block OSCORE-protected
Q-Block2 response is covered in the reverse direction, from go-coap to the
libcoap client.
