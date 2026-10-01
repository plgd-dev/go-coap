# Pinned RFC 9177 independent-peer checks

Run these local/manual socket tests from the repository root:

```sh
rtk proxy bash tests/interop/qblock/run.sh
rtk proxy bash tests/interop/qblock/run-dtls.sh
```

The peer source must be a clean checkout of libcoap commit
`c63c8f7cb7f248a4992539529b9e1b691962f29a` (reports version 4.3.5).
The scripts check the exact commit and source cleanliness, build out of tree,
and never fetch, install, or change the peer source. Set `LIBCOAP_SOURCE`,
`LIBCOAP_BUILD`, or `LIBCOAP_DTLS_BUILD` to change the default scratch paths.
Dependencies are Go, Python 3, CMake, a C compiler, and RTK. DTLS additionally
needs OpenSSL; `OPENSSL_ROOT_DIR` defaults to Homebrew's `openssl@3`.
Both scripts record exact CMake configure flags in their source and retain
`configure.log`/`build.log` in the build directory. The UDP build disables
DTLS/TCP/WebSockets/OSCORE and enables Q-Block and the examples, with static
Debug linking. DTLS uses the same flags except `ENABLE_DTLS=ON` and
`DTLS_BACKEND=openssl`.

UDP runs GET, POST, and PUT in both directions with 1500-byte bodies and
64-byte blocks, spanning three payload sets. POST/PUT upload and echo response
exercise Q-Block1 followed by Q-Block2. Go uses the public APIs with
`qblock.Require` and an explicit `ProbeQBlock` before payload traffic.
The libcoap client uses `-L 7 -N -b 64`: libcoap block handling, assembled body,
and explicit Q-Block selection, followed by its own CON capability probe.
Its default empty token is intentionally preserved.

The Go client talks to `libcoap-server.c`, an application handler linked with
the unchanged pinned libcoap library. It supplies the real resource Size2
through `coap_add_option` and uses `coap_add_data_large_response`; libcoap owns
all protocol decoding, Q-Block assembly, scheduling, recovery, and encoding.
The shipped `coap-server` example omits RFC 9177-required Size2 on its
single-block CON capability replies, including `/example_data` and
`/.well-known/core`. Go correctly rejects those replies. This fixture makes
the application metadata explicit without rewriting packets or replacing the
independent protocol engine. The reverse direction uses the shipped
`coap-client` executable against Go's public server APIs.

Each run writes a new evidence directory (`QBLOCK_INTEROP_OUTPUT` overrides
the default). Each case includes both exact command arrays, peer logs,
response bytes, and `wire.jsonl` with monotonic timestamps, direction, raw
datagram hex, and the relay forwarding decision. The relay forwards byte for
byte. `wire.py` independently decodes RFC 7252 headers/options without Go's
decoder, separates capability ACK from NON payload, rejects classic
Block1/Block2, checks stable Request-Tag/ETag/Size/SZX/content format and
response-token correlation, verifies complete fragment coverage, and
reconstructs upload and response bytes with SHA-256. Run it separately with:

```sh
rtk proxy python3 tests/interop/qblock/wire.py /path/to/case/wire.jsonl --method GET
rtk proxy python3 -m unittest discover -s tests/interop/qblock -p test_wire.py
```

Each UDP direction also runs GET with the first server-to-client NON Q2 block
3 dropped. The decision predicate and occurrence are fixed, logged, and
repeatable. Success requires a subsequent explicit Q2 block3 recovery request,
delivery of the lost block, and exact independently reconstructed body.
Dropped packets are excluded from the delivered-body audit while retained in
the raw trace. The script continues after individual case failures and exits
nonzero if any case fails; `results.json` preserves the full matrix.

DTLS PSK is a separate six-case matrix with local test identity
`qblock-interop` and key `qblock-interop-local-key`, using
`TLS_PSK_WITH_AES_128_GCM_SHA256`. The OpenSSL 3.6.3 peer ClientHello did not
offer Pion's initial CCM8-only cipher, so that fixture configuration failed
handshake; GCM is shared by both peers. DTLS evidence includes encrypted raw
datagrams and independent libcoap plaintext diagnostics plus exact application
bodies. It does **not** claim independent plaintext raw-wire auditing or loss
injection for DTLS. UDP is the stronger wire-audited recovery evidence.
