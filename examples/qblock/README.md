# Q-Block examples

These examples exercise go-coap's opt-in RFC 9177 Q-Block support on unicast
UDP. The client explicitly probes the peer before sending an application
request, then selects either `PreferKnown` or `Require`. The server receives a
complete assembled POST/PUT body in its handler and returns one complete
application response.

Start the server:

```sh
go run ./examples/qblock/server -addr 127.0.0.1:5688 -response-bytes 32768
```

Run a required Q-Block GET and an echoed Q-Block POST:

```sh
go run ./examples/qblock/client -addr 127.0.0.1:5688 -method get -mode require -probe=true -response-bytes 32768
go run ./examples/qblock/client -addr 127.0.0.1:5688 -method post -mode require -probe=true -body-bytes 8192
```

The client also accepts `put`. It reads the response body, checks its content,
and releases the caller-owned pooled response with `ReleaseMessage`. Press
Ctrl-C or set `-timeout` to cancel the operation context. Cancellation stops
the local wait and transfer; it cannot establish whether a peer already
processed a POST/PUT.

## Discovery and selection

`ProbeQBlock` performs an explicit, bounded CON GET capability probe. A
successful probe records Q support for that connection only; it does not
enable Q by itself. `WithQBlock` enables outbound selection. The probe uses a
single requested Q-Block2 block and validates the response's QBlock2, ETag and
Size2 metadata. A generic successful response without Q-Block2 does not prove
support. Reset, malformed responses, timeout and cancellation leave capability
unknown. New UDP or DTLS connections start unknown and must be probed
independently.

`PreferKnown` is the default. It uses Q only after the current connection has
positive probe knowledge; otherwise the request follows the existing ordinary
or classic path. It does not probe implicitly. `Require` also needs an
explicit successful probe and returns an error before reading or transmitting
the request body if capability is unknown or unsupported. To see that behavior,
run `-mode require -probe=false`; no application payload is sent.

The probe target defaults to the requested resource path. Applications may
choose a stable resource that returns a valid Q-aware representation. Some
shipped libcoap examples omit mandatory Size2 on their probe response and are
rejected by go-coap's strict validation. The pinned interoperability fixture
adds that application metadata through public libcoap APIs; this does not
claim compatibility with the unmodified shipped example.

## Supported subset and limits

Q data is sent in NON messages. The current outbound selection supports
bodyless unicast GET and body-bearing unicast POST/PUT on UDP and DTLS. The
server accepts Q-Block GET and assembled POST/PUT requests. Capability belongs
to one connection/security session and is cleared when that session closes.

Defaults come from `qblock.DefaultClientConfig` and
`qblock.DefaultServerConfig`:

| Limit | Default |
| --- | ---: |
| Maximum body size per transfer | 1 MiB |
| Maximum retained bytes in a connection manager | 16 MiB |
| Active transfers per manager | 64 |
| Reserved transfer tokens per manager | 512 |
| Q payloads per pacing set | 10 |
| Transfer lifetime | 247 seconds |
| Server completed-request retention | 247 seconds |
| Server request records per connection | 64 |
| Server retained metadata per connection | 64 KiB |
| Server endpoint peers / connections / members | 1024 / 1024 / 4096 |

`MaxOwnedBytes` is a separate adapter reservation limit. Zero derives its
conservative reservation floor from the selected transport and configuration;
it does not mean unlimited memory. Manager retained-byte limits count sender
body copies and receiver payload, assembly and sparse-index capacity. These
accounting limits exclude caller-owned handler allocations and preexisting
message-pool capacity.

An incomplete body is not delivered to the application. Transfer state expires
at its configured deadline and releases its reservation; server duplicate
suppression is bounded by the configured retention period. A Q exchange that
fails or expires is not replayed through classic blockwise. If a POST/PUT was
processed but its terminal response was lost, the application outcome is
uncertain. Use application-level idempotency when retrying such operations.

Observe, FETCH/PATCH/iPATCH, multicast, TCP/TLS Q transfers, BERT and OSCORE
remain deferred. These examples do not enable those features.

## Local performance characterization

The release ledger records four loopback runs of a Q-Block2 GET: an 8 KiB
one-set response and a 256 KiB multi-set response, each with no loss and with
one dropped response block. The benchmark explicitly probes, requires support,
verifies the complete body and records transfer time, hashes and Q-owned byte
accounting. The byte figures describe manager retained-byte reservations and
adapter ownership reservations. They are not process heap or RSS measurements.
Loopback results characterize this host and configuration; they do not predict
a universal Q-Block speedup.

Reproduce the measurement with:

```sh
GOCACHE=/private/tmp/go-coap-con-build go test ./udp/client \
  -run '^$' -bench '^BenchmarkQBlockMilestone6WireMatrix$' \
  -benchtime=1x -count=1 -v
```

Exact settings, repeat runs and measured values are recorded in
`docs/superpowers/plans/2026-09-15-rfc9177-results.md` and the retained
`.superpowers/sdd/2026-10-01-rfc9177-milestone6/` ledger.
