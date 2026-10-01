# Q-Block owned-copy boundaries

**Spec:** docs/superpowers/specs/2026-10-01-rfc9177-milestone3-completion-design.md
Inline TDD. Prerequisite for aggregate envelopes: remove unbounded or redundant
adapter copies; byte envelopes are not claimed complete by this plan.

## Task 1: Bound owned terminal/control and handler snapshots

Test an owned Q1 terminal without Q options above private datagram cap: RED
currently completes and clones it; GREEN ignore before processing/copying, retaining
upload for a valid response. Bound q1ControlFromResponse missing body reads by the
wire cap and missing count by MaxPayloads in adapter use; preserve parser helpers
for independent tests. Validate handler option backing bytes against server
metadata budget BEFORE cloning the response snapshot. Test oversized handler
options: never retain/transmit, suppression remains, one dispatch.

## Task 2: Remove copy-only sizing and hash temporaries

Test qblockDatagramSize with counting reader: expected encoded size, cursor
preserved, zero Read calls (RED currently Marshal reads/copies). Use coder.Size
plus BodySize through qblockIncomingSize. Hash response code+body incrementally,
avoid append whole payload. Canonical server options use exact clones and
sizeof Option-aware accounting. Tests assert exact backing and metadata limits.

Files: udp/client/qblock_client.go,qblock.go,qblock_server.go,
qblock_server_q1.go,qblock_transmit.go,qblock_memory_test.go.
Focused normal/race, compile/vet/whitespace and required runtime suite.
One fresh-context review for this range, scoped verified commit. Preserve user
edits/.codanna; aggregate envelopes/endpoint/public wiring remain open.
