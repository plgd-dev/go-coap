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
support. A Q-less successful response returns `false, nil` but does not prove
that Q is unsupported, so it leaves current knowledge unchanged. A validated
Bad Option is conclusive and records the session as unsupported; a later
positive probe restores supported. Reset, malformed or invalid responses,
transport errors and timeout are inconclusive and preserve existing
knowledge. Cancellation returns an error for that caller and does not itself
clear session knowledge. Closing a connection clears its knowledge. New UDP
or DTLS connections start unknown and must be probed independently.

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

The benchmark measures a Q-Block2 GET after explicit discovery: an 8 KiB
one-set response and a 256 KiB multi-set response, each with no loss and with
one dropped response block. These two timed runs completed each case and
verified the complete body hash. Throughput is body bytes divided by elapsed
wall time; capability discovery and fixture setup are outside the timed
interval.

| Response | Loss | Run 1 transfer time | Run 1 MiB/s | Run 2 transfer time | Run 2 MiB/s | Dropped Q2 blocks |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| 8 KiB | none | 0.927083 ms | 8.426969 | 2.337583 ms | 3.342127 | 0 / 0 |
| 8 KiB | response NUM 3 | 1.154987208 s | 0.006764 | 1.154801791 s | 0.006765 | 1 / 1 |
| 256 KiB | none | 42.271541 ms | 5.914144 | 39.344167 ms | 6.354182 | 0 / 0 |
| 256 KiB | response NUM 3 | 231.419708 ms | 1.080288 | 253.520417 ms | 0.986114 | 1 / 1 |

The sampled Q accounting high-water values were:

| Response and loss | Client manager B (runs 1 / 2) | Server manager B (runs 1 / 2) | Client adapter reserved B | Server adapter reserved B |
| --- | ---: | ---: | ---: | ---: |
| 8 KiB, none | 16,504 / 0 | 8,192 / 8,192 | 190,381,962 | 204,844,496 |
| 8 KiB, response NUM 3 | 16,504 / 16,504 | 8,192 / 8,192 | 190,381,962 | 204,844,496 |
| 256 KiB, none | 524,528 / 524,528 | 262,144 / 262,144 | 190,889,866 | 206,876,112 |
| 256 KiB, response NUM 3 | 524,528 / 524,528 | 262,144 / 262,144 | 190,889,866 | 206,876,112 |

Manager values are read from `net/qblock.Manager.retained` at the benchmark's
adapter event sampling points. The short one-set 8 KiB client observation
varied from 0 to 16,504 bytes across the two timing runs; the separate race run
also observed 16,504 bytes. These are sampled high-water values and may miss
transient state within one adapter event. Adapter values are ownership
reservations, including the conservative precharged budget floor, not live
heap or RSS. The race-detector run is correctness evidence only and is
excluded from the timing table. These loopback measurements describe this
host and configuration; they do not predict a universal Q-Block speedup.

Reproduce the measurement with:

```sh
GOCACHE=/private/tmp/go-coap-con-build go test ./udp/client \
  -run '^$' -bench '^BenchmarkQBlockMilestone6WireMatrix$' \
  -benchtime=1x -count=1 -v
```

Exact settings, body hashes, repeat runs and evidence paths are recorded in
`docs/superpowers/plans/2026-09-15-rfc9177-results.md` and the retained
`.superpowers/sdd/2026-10-01-rfc9177-milestone6/` ledger.
