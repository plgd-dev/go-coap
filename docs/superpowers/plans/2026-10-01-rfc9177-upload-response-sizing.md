# Q1 upload / Q2 response sizing

**Spec:** docs/superpowers/specs/2026-10-01-rfc9177-milestone3-completion-design.md
**Execution:** inline executing-plans, TDD. Base after acceptance fixture repair.
Preserve user-owned docs and .codanna. No public/client/DTLS wiring in this plan.

## Task 1: Advertise an independent response ceiling

Files: udp/client/qblock_client.go, qblock_packet_test.go.
Add a test proving every Q1 upload advertises NUM0/M1 response SZX computed from
receiver MTU; use long upload options so upload SZX differs. Run RED. Compute
response ceiling before admission, store on exchange and include encoded Q2
advertisement in upload SZX template and each newQ1Request. Run GREEN and existing
packet tests; verify complete encoded datagrams remain inside MTU.
Expected: `go test ./udp/client -run '^TestQBlockPacket' -count=1` passes.

## Task 2: Consume the ceiling independently on the server

Files: qblock_server.go, qblock_server_q1.go, sizing tests.
Test combined Q1/Q2 delivery through the private server, malformed/repeated hints,
changed hints and distinct response/upload SZX. Run RED. Route Q1 before Q2
controls; validate NUM0/M1 single hint and freeze it independently of canonical
upload identity. Select response SZX <= hint and local send limit. Run GREEN.
Expected: packet/server focused tests pass with one dispatch and fixed offsets.

## Task 3: Reject offset-changing feedback before state changes

Files: qblock_client.go, sizing tests.
Test oversized advertised Q2 handoff ignored with Q1/debt retained and later valid
handoff accepted; changed Continue SZX must fail before emitting a next batch.
Run RED then add pre-transition validation. Run GREEN. Run focused normal/race
Q tests, compile-only repository check, vet and whitespace; commit scoped changes.
Expected: all focused gates pass. Runtime gate is separate and reported accurately.

One final fresh-context Astra/high review for this plan range; one TDD fix pass
for material findings. Keep scoped ledger with every ruling and verification.
