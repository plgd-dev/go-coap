# RFC 9177 Milestone 4 acceptance gaps

This plan closes the explicitly deferred acceptance coverage after the session-capability and server-CON implementation reviews. It does not add protocol behavior unless a new regression test demonstrates a defect.

## Scope

- Exercise public outbound Q selection on an accepted UDP and DTLS server connection: explicit CON discovery, Require GET, and Require POST against a real peer.
- Cover supplied DTLS constructor failure with borrowed and owned transports, synchronous closed `Done`, once-only initialization reporting, no periodic startup, the executing-method initialization guards, and preservation of runtime `Errors` callbacks. Verify invalid UDP/DTLS dial configuration is rejected before the dialer creates a transport.
- Close the server-CON review minors: synchronize request admission before racing handler completion with the ACK deadline; verify an expired running handler retains record admission; verify owned accounting while a CON write is held; and prove ACK/Reset releases only its response-owned endpoint permit.

## Files and responsibilities

- `udp/qblock_probe_test.go`, `dtls/qblock_test.go`: constructor validation, initialization guards, error callback and transport ownership.
- `udp/qblock_con_probe_test.go`, `dtls/qblock_con_probe_test.go`: real accepted-connection discovery and outbound GET/POST.
- `udp/client/qblock_server_con_lifecycle_test.go` and its fake-session helper: deterministic CON admission, deadline and permit lifecycle assertions.
- Existing capability and server-CON SDD ledgers, their scoped verification logs, and the 2026-09-15 roadmap/results: record exact evidence and remaining milestone boundaries.

## Tasks

### 1. Public accepted outbound and constructor acceptance

- Add real UDP and PSK-DTLS server-accepted connection flows. A raw peer sends a plain trigger request; the accepted server connection then probes and completes a Q GET plus body-bearing POST in Require mode. Assert one probe, Q options on GET/POST, complete GET body, and one POST handling outcome.
- Add supplied DTLS Client failure cases for borrowed and `WithCloseSocket` transports. Assert the callback fires once, `Done` is closed before return, periodic startup is absent, every executing method returns the stored initialization error (including when its context is canceled), and the transport follows its ownership option.
- Add successful supplied-transport runtime-error callback coverage and UDP/DTLS dial validation checks proving invalid configuration fails before the dialer opens a socket.
- Run focused normal and race tests for UDP/DTLS/client/options and retain the exact logs in the capability ledger directory.
- Commit only the tests, plan, roadmap/results entry, and capability ledger/logs.

### 2. Server-CON lifecycle acceptance

- Change the deadline race fixture to wait for handler admission before advancing the fake clock, then synchronize timer processing and handler release. Assert exactly one valid ACK form.
- With `MaxRecords=1`, expire a blocked handler and verify a second request is rejected until the handler releases its retained record.
- Hold a CON write, advance the fake clock beyond expiry, and assert owned bytes remain charged until the write returns and expiry cleanup completes.
- For ACK and Reset feedback, establish two separately owned response permits and prove feedback for one response releases only that record's MID/permit; the other response remains admitted and retransmittable.
- Run focused normal/race CON tests and retain logs in the server-CON ledger directory.
- Commit only the lifecycle tests and server-CON ledger/logs.

## Completion evidence

Review the final diff against this scope, retain all previous ledgers and historical failed attempts, and record exact commands, exit codes, HEADs, preserved-edit hashes/status, findings/rulings, deferred minors, and remaining Milestone 5–6/interoperability/release work. Do not merge or push.
