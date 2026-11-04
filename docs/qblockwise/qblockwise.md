# Experimental QBlockwise transfers

Q-Blockwise implements datagram transfers from
[RFC 9177](https://www.rfc-editor.org/rfc/rfc9177.html). The engine, codecs, and
transfer tests live in [`net/blockwise/qblock`](../../net/blockwise/qblock).
The parent `blockwise` package keeps its existing Q-Block APIs as compatibility
aliases and forwarding methods; UDP/DTLS application configuration is unchanged.

## Configuration

Existing applications continue to use RFC 7959 blockwise transfers by default.
Q-Block selection is opt-in on UDP/DTLS clients and servers:

```go
options.WithQBlockwise(options.QBlockwiseAuto)
```

| Mode | Behavior |
|---|---|
| `QBlockwiseDisabled` | Default: disable Q-Block and preserve RFC 7959 Block1/Block2 handling. |
| `QBlockwiseAuto` | Discover peer support before selecting Q-Block; fall back to legacy blockwise if the peer rejects Q-Block. |
| `QBlockwiseOnly` | Require Q-Block support; fail rather than fall back. |

Auto discovery uses a separate confirmable request and does not replay a
side-effecting application request. Keep the existing `WithBlockwise` option for
enabling blockwise handling, choosing the maximum block size, and legacy timeouts.
Q-Block requires blockwise handling to be enabled.

Automatic protocol selection does not change the application's CON/NON choice
or increase NSTART. Q-Block's main benefit is sending sets of NON blocks with
selective recovery, not bypassing congestion control. TCP/TLS BERT and multicast
behavior remain unchanged.

Enabling Q-Block permits transfers on both plain UDP and DTLS; no additional
permission option is required for UDP. Prefer authenticated DTLS in production:
RFC 9177 does not recommend Q-Block in NoSec mode.

### Congestion and recovery parameters

Use the same option on UDP/DTLS clients and servers. For example, with imports
`github.com/plgd-dev/go-coap/v3/net/blockwise/qblock` and
`github.com/plgd-dev/go-coap/v3/options`:

```go
parameters := qblock.DefaultConfig()
parameters.MaxPayloads = 16
parameters.NonTimeout = 3 * time.Second
parameters.NonReceiveTimeout = 6 * time.Second

cc, err := udp.Dial(addr,
    options.WithQBlockwise(options.QBlockwiseAuto),
    options.WithQBlockwiseConfig(parameters),
)
```

| Field | RFC default | Accepted configuration |
|---|---|---|
| `MaxPayloads` | 10 | 1 through 256; must match on both peers. |
| `NonTimeout` | 2 s | Positive, at most 1 hour. |
| `NonReceiveTimeout` | 4 s | At least `1.5*NonTimeout + 1 s`; exponential retry backoff must not overflow `time.Duration`. |
| `NonMaxRetransmit` | 4 | 0 through 20; zero disables missing-payload retries. |
| `MaxLatency` | 100 s | Positive, at most 24 hours; used in derived lifetimes. |

These bounds are local resource/overflow policies, not RFC-specified maxima.
The entire zero `qblock.Config{}` selects defaults. Other configurations are
complete values, not partial overrides: start from `qblock.DefaultConfig()`.
Validation runs even when Q is disabled. `Dial` and server `Serve` return errors
wrapping `qblock.ErrInvalidConfig` before opening/using the connection or listener.
The existing `Client` constructors cannot return an error: they notify the
configured error handler and retain the invalid-configuration error for requests.

The engine samples one `NON_TIMEOUT_RANDOM` delay in
`[NonTimeout, 1.5*NonTimeout)` per transmitted NON body and reuses it between sets.
`ACK_RANDOM_FACTOR` remains 1.5. Derived timers follow RFC 9177 Section 7.2:

```text
NON_PARTIAL_TIMEOUT = NonTimeout * (2^NonMaxRetransmit - 1) * 1.5
                      + 2*MaxLatency + NonTimeout
NON_PROBING_WAIT    = NON_PARTIAL_TIMEOUT - NonTimeout + NON_TIMEOUT_RANDOM
Time-to-Wait       = NonReceiveTimeout * 2^(Re-Request-Count - 1)
```

Default partial retention is 247 s, subject to positive Max-Age for Q2; default probing
wait is 247-248 s. `Config.NonPartialTimeout()` and `Config.NonProbingWait(delay)`
expose these calculations for a resolved configuration. Explicit overrides for
the derived lifetimes and jitter factor are not exposed.
Max-Age=0 marks the representation immediately stale for caching, but does not
abort an ongoing multi-block assembly: it retains the partial body for
`NonPartialTimeout`. Positive Max-Age limits retention to the smaller of that
age and `NonPartialTimeout`. This deliberately separates transfer completion
from cache freshness and differs from Section 4.4's recommended zero-age
partial-retention limit.

`WithQBlockwiseConfig` does not enable Q, alter legacy blockwise timeouts, change
the selected CON/NON type, or tune CON retransmission/NSTART parameters.
There is no in-band MAX_PAYLOADS negotiation: Auto detects Q support, not these
parameters. Keep MAX_PAYLOADS=10 with a default-configured libcoap peer.
Shorter timers are useful for controlled tests, but are not generally appropriate
for an Internet deployment.

Direct engine users can call `Engine.SetConfig` or the compatibility facade's
`BlockWise.SetQBlockwiseConfig`. These setters are setup-only, before concurrent
transfers; an invalid update leaves the previous configuration unchanged.

## Run the client and server

The [server](../../examples/qblockwise/server/main.go) and
[client](../../examples/qblockwise/client/main.go) both enable `QBlockwiseAuto`
and use 1024-byte blocks. Run these commands from the repository root in
separate terminals:

```sh
go run ./examples/qblockwise/server
```

```sh
go run ./examples/qblockwise/client
```

The server listens on `127.0.0.1:5688` and exposes `/data`:

- GET returns a deterministic 64 KiB binary body.
- PUT echoes the complete uploaded body with a 2.04 Changed response.
- Other methods receive 4.05 Method Not Allowed.

The client uses NON requests to download the body, upload it back, and verify
that the echoed bytes match exactly. It logs the transferred sizes and received
Q-Block message counts. Both endpoints retain Auto's legacy fallback behavior.
Stop the server with Ctrl+C.

To use another address, pass the same `-addr` value to both commands:

```sh
go run ./examples/qblockwise/server -addr 127.0.0.1:5690
go run ./examples/qblockwise/client -addr 127.0.0.1:5690
```

The example uses plain loopback UDP for convenience, not production security.

## Bodies, Observe, and limits

Application handlers receive complete assembled bodies. This necessarily
retains body-sized storage; it is not a streaming API. Sender bodies must remain
valid and replayable until the operation completes, including retransmissions.
Peers must coordinate nondefault MAX_PAYLOADS values; RFC 9177 does not define
in-band negotiation for that parameter.

Q-Block GET Observe supports registration, notifications, missing-payload recovery,
representation replacement, and cancellation on UDP and DTLS. Original
notification payloads carry Observe; recovery traffic does not re-register.
FETCH and other non-GET Observe requests are unsupported by the Q selection
path: Only returns `ErrQBlockUnsupported`, while Auto uses legacy handling.

Experimental limits are 16 MiB and 65,536 blocks per body, with separate
per-peer and process-wide body, transfer, and token budgets. A connection has
a combined 32 MiB retained-body budget and 128 transfer/observation slots across
both directions; process-wide limits are 256 MiB and 8,192 slots. Failed
reservations roll back both counters. Q transfer state
uses its own recovery/retention lifetime, not the legacy `WithBlockwise`
timeout. Congestion and recovery defaults can be customized as described above.
Proxy and tokenless Q operations are unsupported.
Reliable-transport Q and OSCORE integration are outside this implementation.

## Why this remains experimental

Configurable parameters and successful libcoap transfers are not proof of full
RFC conformance. The [RFC requirements audit](qblockwise-rfc-audit.md) distinguishes
implemented behavior, gaps, unverified clauses, and intentionally unsupported
surfaces. In particular:

- Q1 currently sends Continue responses to individual CON blocks. This conflicts
  with Section 4.3's prohibition on a response for every block; replacing it
  requires separating empty ACK progress from application responses and testing
  both libcoap references.
- Recovery lists are bounded by MAX_PAYLOADS but are not automatically shortened
  to the remaining datagram budget. Large sets, high NUMs, long options, or a
  small MTU can fail recovery rather than produce a fitting report/request.
- Q2 server retention has a `NonReceiveTimeout` minimum even for Max-Age=0
  and is capped by `NonPartialTimeout`; freshness
  and retransmission retention are separate policies. Zero-age assembly and
  terminal Observe cleanup have regression tests, but broader freshness and
  expiry-during-recovery coverage remain release work.
- FETCH Observe support, exhaustive method coverage, prolonged asymmetric loss, timer
  exhaustion, concurrent discovery/scheduler scaling, retained-heap profiling,
  and unchanged-legacy-path performance thresholds remain release gates.
- Proxy, tokenless Q, reliable-transport Q, and OSCORE are outside the current
  scope, not capabilities implied by Auto.

The audit includes the maintained
[follow-up checklist and acceptance criteria](qblockwise-rfc-audit.md#prioritized-remaining-work).
Local body/resource limits are policy, not
in themselves RFC violations. Do not treat successful legacy fallback as evidence
of Q-Block interoperability.

## libcoap interoperability tests

The [Q-Block CI workflow](../../.github/workflows/test-qblock.yml) builds pinned
libcoap references with Q enabled and disabled, including the OpenSSL DTLS
backend. It runs both endpoint roles, CON/NON, upload/download, UDP/DTLS-PSK,
and block sizes 16 through 1024, plus Observe and selective-loss recovery.
Required CI fails if tools are missing or interoperability tests skip.

To run against a Q-enabled libcoap client and server on `PATH`:

```sh
LIBCOAP_QBLOCK_REQUIRED=1 LIBCOAP_QBLOCK_MODE=enabled \
  go test ./udp -run '^TestQBlockLibcoap' -count=1 -timeout=15m
```

Use `LIBCOAP_QBLOCK_MODE=disabled` against a libcoap build with Q disabled
to test automatic legacy fallback and Q-only rejection. Without the required
environment variable, local interoperability tests skip when binaries are
unavailable. The test PSK is public, used only for loopback interoperability,
and must not be used by applications.
