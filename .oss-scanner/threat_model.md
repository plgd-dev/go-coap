
# go-coap Security Threat Model

## 1. Project overview

go-coap is an open-source implementation of the
Constrained Application Protocol (CoAP) in Go.

It provides CoAP client and server implementations for
constrained networks and IoT environments.

Supported transports include:

- CoAP over UDP (RFC 7252)
- CoAP over TCP
- CoAP over TLS
- CoAP over DTLS

The implementation also provides functionality including:

- CoAP request multiplexing and routing
- Observe (RFC 7641)
- Block-wise transfers (RFC 7959)
- FETCH, PATCH, and iPATCH methods (RFC 8132)
- Q-Block transfers (RFC 9177), including Q-Block1 and Q-Block2
- Multicast
- NoResponse option (RFC 7967)
- Object Security for Constrained RESTful Environments (OSCORE, RFC 8613)
- DTLS-based communication

The library is intended to be embedded into applications,
IoT gateways, servers and other network-facing software.

## 2. Security objectives

The primary security objectives are:

- Correct parsing and validation of untrusted CoAP messages.
- Preventing remote code execution.
- Preventing memory exhaustion and uncontrolled resource consumption.
- Preventing denial-of-service conditions.
- Correct handling of malformed and oversized packets.
- Safe handling of concurrent network requests.
- Correct enforcement of protocol semantics.
- Secure handling of DTLS and TLS connections.
- Preventing out-of-bounds access, panics and unexpected process termination.

## 3. Threat actors and attack surface

Assume an attacker can send arbitrary network traffic to a
CoAP server exposed over UDP or TCP.

The attacker may be:

- An unauthenticated remote network client.
- A malicious or compromised IoT device.
- A malicious peer in a constrained network.
- An authenticated client attempting to abuse protocol features.

Treat all network-originated input as untrusted.

Important attack surfaces include:

### Message parsing

- CoAP header parsing.
- Token and message ID handling.
- Option parsing and option length validation.
- Payload processing.
- Invalid and truncated messages.
- Unexpected option values.
- Malformed packet handling.

### Request methods and payload formats

- Handling of GET, POST, PUT, DELETE, FETCH, PATCH, and iPATCH request codes.
- Content-Format and payload validation for body-bearing methods, especially
  FETCH, PATCH, and iPATCH.
- Preservation of request method semantics while requests are fragmented and
  reassembled by Blockwise or Q-Block transfers.
- Application handlers are responsible for validating method payload formats
  and enforcing the atomic update semantics required for PATCH and iPATCH.

### UDP and TCP transports

- Datagram processing.
- TCP framing and message boundaries.
- Connection lifecycle management.
- Concurrent request processing.
- Unexpected connection termination.
- Resource exhaustion through repeated requests.

### Block-wise transfers

- Block size validation.
- Block number and offset calculations.
- Reassembly of fragmented payloads.
- Inconsistent block sequences.
- Excessive payload allocation.
- Resource exhaustion through large or incomplete transfers.

### Q-Block transfers (RFC 9177)

- Validation of Q-Block1 and Q-Block2 options, including NUM, SZX, M,
  Request-Tag, Size1, and Size2.
- Reassembly of out-of-order, duplicate, missing, and conflicting blocks.
- Selective recovery requests and handling of repeated or malicious recovery
  traffic.
- Bounded payload bursts, transfer state, queued work, and retained response
  bodies.
- Isolation of transfer state between peers, tokens, and representations.
- Cancellation, expiration, and cleanup during concurrent transfers.

### Observe functionality

- Subscription lifecycle management.
- Notification handling.
- Cancellation and cleanup.
- Concurrent subscriptions.
- Resource exhaustion through excessive subscriptions.

### Request multiplexer

- Path matching and routing.
- Handler selection.
- Middleware execution.
- Request context handling.
- Concurrent handler execution.

### DTLS and TLS

- Secure transport configuration.
- Certificate and PSK handling.
- Handshake error handling.
- Session lifecycle management.
- Authentication and encryption configuration.

### OSCORE (RFC 8613)

- Provisioning and matching master secret, master salt, Sender ID, Recipient
  ID, and ID Context across peers.
- Derivation and selection of the supported security context, including
  unknown or ambiguous context identifiers.
- Sender Partial IV allocation, exhaustion, process restart, and duplicate
  sequence reservations across contexts or processes sharing a sender key.
- Recipient replay-window state, duplicate and out-of-order messages, and replay
  state loss after process restart.
- OSCORE option parsing, inner/outer message construction, option classes,
  external AAD, authentication-tag validation, and response-to-request binding.
- Downgrade risks when endpoints permit both plaintext and protected traffic;
  authentication and context failures must never fall back to plaintext.
- Observe notification freshness, cancellation, token/context lifetime, and
  asynchronous sends.
- Interaction with blockwise transfers, UDP retransmission/deduplication, and
  reliable-transport framing.
- Q-Block options are encrypted with OSCORE on the tested UDP/DTLS endpoint
  paths; verify this remains true across retransmission and recovery flows.
- Proxy forwarding is supported only by the minimal opaque forwarder; caching,
  Observe, and blockwise proxy sessions are not implemented. Multicast OSCORE
  and group OSCORE remain unsupported. RFC 9177 Q-Block over TCP/TLS is not
  supported; those transports use reliable-transport Blockwise.

## 4. Vulnerability classes of interest

Please prioritize findings involving:

### Critical / High

- Remote code execution.
- Authentication bypass.
- Memory corruption or unsafe memory access.
- Remote denial of service affecting server availability.
- Unbounded memory allocation controlled by remote input.
- Severe cryptographic or transport security failures.
- Request smuggling or message desynchronization leading to
  security boundary violations.
- Cross-client data exposure.

### Medium

- Resource exhaustion requiring specific conditions.
- Incorrect protocol validation with security implications.
- Information disclosure.
- Improper cleanup of subscriptions or connections.
- Race conditions affecting security-sensitive state.
- Unexpected panics triggered by remote input.

### Low / Informational

- Protocol interoperability issues without security impact.
- Non-security-related RFC compliance problems.
- Defensive coding improvements without a demonstrated exploit.

## 5. Testing guidance

Please investigate and test:

- Fuzzing opportunities in packet and option parsers.
- Malformed and truncated CoAP messages.
- Integer overflow and underflow in length and offset calculations.
- Extremely large option and payload lengths.
- Block-wise transfer boundary conditions.
- Large and fragmented FETCH, PATCH, and iPATCH requests over Blockwise.
- Q-Block1/Q-Block2 malformed options, block boundaries, duplicates,
  out-of-order delivery, selective recovery, and concurrent transfers.
- Interoperability of Blockwise and Q-Blockwise FETCH, PATCH, and iPATCH with
  libcoap.
- Q-Block resource limits, transfer expiration, and cancellation under load.
- Concurrent request and subscription handling.
- Connection and session cleanup.
- UDP amplification and resource exhaustion scenarios.
- DTLS configuration and authentication edge cases.
- OSCORE Appendix C test vectors, malformed options/messages, invalid contexts,
  modified ciphertext or AAD, replay, out-of-order notifications, concurrent
  sequence reservation, sender-sequence exhaustion, and restart behavior.
- OSCORE/plaintext policy on all transports, ensuring an OSCORE failure never
  reaches an application handler or triggers plaintext fallback.
- OSCORE interoperability with the pinned OSCORE-enabled libcoap build in both
  endpoint directions, including Observe, blockwise, invalid credentials,
  tampering, and replay.

Use existing tests and fuzz tests as a starting point.
Run the Q-Block interoperability tests against a Q-enabled libcoap build; they
exercise both client and server behavior, including UDP and DTLS.
Run OSCORE interoperability tests against an OSCORE-enabled libcoap build as
well. The `.oss-scanner/Dockerfile` builds libcoap with both Q-Block and OSCORE
enabled and makes both interoperability suites mandatory.

When possible, create a minimal reproducible proof of concept.

## 6. Scope and exclusions

The primary target is the go-coap repository itself.

Focus on vulnerabilities in code maintained by this project.

Third-party dependencies should not be reported merely because
they contain a vulnerability. Report them only when a concrete
vulnerable integration or usage in go-coap can be demonstrated.

Do not classify application-level authorization problems as
go-coap vulnerabilities when authorization is intentionally
implemented by the consuming application.

Do not report theoretical issues without a credible attack
scenario and security impact.

Do not treat normal protocol behavior or intentional
resource limits as vulnerabilities.

## 7. Reporting expectations

For every finding, please include:

- Vulnerability title.
- Affected package and source file.
- Exact affected code location.
- Severity and rationale.
- Attacker capabilities and prerequisites.
- Concrete attack scenario.
- Reproducible proof of concept where possible.
- Expected versus actual behavior.
- Suggested minimal patch.
- Regression test demonstrating the fix.

Prefer actionable findings over speculative security concerns.

Avoid duplicate findings describing the same root cause.
