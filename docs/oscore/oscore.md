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
`ContextConfig` values. Algorithm 10 remains the default for source and wire
compatibility. Algorithm 11 has a 64-bit authentication tag and therefore
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
		MasterSecret: []byte("0123456789abcdef"),
		MasterSalt:   []byte("salt1234"),
		SenderID:     []byte(senderID),
		RecipientID:  []byte(recipientID),
	})
	if err != nil {
		return nil, err
	}
	return oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts:      []*oscore.Context{ctx},
		RequireOSCORE: true,
	})
}
```

For this example, configure the server with
`newOSCOREProcessor("server", "client")` and the client with
`newOSCOREProcessor("client", "server")`.

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
behavior. For incoming requests, `RequireOSCORE: true` rejects plaintext. To
deliberately allow protected and plaintext requests on the same endpoint, set
`AllowUnprotected: true` and leave `RequireOSCORE` false. This setting is an
endpoint-wide policy; use separate endpoints or application policy when some
resources must not accept plaintext.

Once an OSCORE option is present, malformed messages, unknown contexts,
authentication failures, and replays are rejected; they are never retried as
plaintext. Outgoing requests with multiple configured contexts require an
`OutgoingContextSelector`; without one, protection returns
`oscore.ErrAmbiguousContext`.

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
The store must load the state for that recipient context and atomically persist
each new state before returning success from `Save`; otherwise the protected
request is not accepted. Use one store per context and coordinate access if
multiple receiver processes can use that context. Persisting a replay-window
advance requires durable storage on every accepted protected message and can
limit throughput. Omitting the replay store avoids that write cost, but a
receiver restart then loses its replay state. After such a restart, do not
continue using the same context without restoring replay protection; use the
RFC 8613 Appendix B.1.2 recovery approach (for example, Echo) or establish a
fresh security context.

The `net/oscore/store` package provides a file-backed implementation of both
interfaces. Its sender sequence store durably reserves windows of 256 values
and serves them from memory; unused values are skipped when the store is
recreated. Its replay store still durably records each accepted window advance:

```go
import (
	"github.com/plgd-dev/go-coap/v3/net/oscore"
	oscorestore "github.com/plgd-dev/go-coap/v3/net/oscore/store"
)

state, err := oscorestore.NewFileStore("/var/lib/myapp/oscore", "peer-17")
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
a different ID for each distinct context. The file store creates state with
restrictive permissions and atomically syncs updates. On Unix it coordinates
multiple processes with file locks; on other platforms it only coordinates
within one process and cannot sync directory metadata. Keep its directory
writable only by the trusted application account. The file store does not
persist master secrets or encrypt state at rest. Applications can instead
implement `SenderSequenceStore` and `ReplayWindowStore` for a database or
remote transactional store.

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
SSRF path. The round tripper receives the context, parsed target, and a copied
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
  AES-CCM-16-64-128; IoTivity Lite currently interoperates with algorithm 10.
- UDP, DTLS, TCP, and TLS transports.
- Classic Block1/Block2 and Observe flows. Observe notifications use fresh
  Partial IVs; stale or duplicate notifications are rejected.
- No-Response suppression is preserved for protected requests over UDP, DTLS,
  TCP, and TLS; the application processes each request while the suppressed
  response is not sent.
- Optional durable receiver replay-state persistence through
  `ReplayWindowStore`; storage durability and coordination are application
  responsibilities.
- Q-Block1 uploads and Q-Block2 downloads over OSCORE-protected UDP and DTLS.
  Q-Block options are encrypted as Class E options. Q-Block is a datagram
  protocol (RFC 9177); TCP/TLS use reliable-transport Blockwise instead.
- Pre-established unicast contexts only. Group OSCORE, automatic context
  establishment (including EDHOC), and multicast OSCORE are unsupported. The
  `net/oscore/proxy` package provides only the minimal forwarding scope above;
  it is not a full-featured CoAP proxy server.
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
