# Connected UDP acceptance repair

Execute inline with executing-plans and TDD in feat/qblock-foundation. Base
1f05289. This bounded acceptance repair supports Milestone 3's UDP gate.

## Task 1: Use real loopback destinations in socket acceptance tests

**Design basis:** Acceptance fixtures bind wildcard addresses and dial them as
if they were routable peers. On this host that causes nil read sources, routing
errors and UDP/DTLS failures. Use explicit loopback for listener fixtures that
are dialed locally; leave production transport and wildcard-listener coverage
unchanged. An explicit connected IPv6 regression already passes before changes;
there is no evidence for a transport fallback.

**Files:** local-dial test fixtures in net, udp, udp/client, dtls and tcp; explicit wildcard/multicast routing coverage remains unchanged.

- [ ] Add TestUDPConnConnectedIPv6ReadPeer using an IPv6 loopback listener and
  connected dialer, send response and assert bytes, peer address and no error.
- [ ] Run `rtk proxy go test ./net -run '^TestUDPConnConnectedIPv6ReadPeer$' -count=1 -timeout=15s`. Expected: diagnose the connected-source hypothesis; actual GREEN rejects that hypothesis. Existing wildcard-destination acceptance tests supply RED.
- [ ] Replace local-dial fixture wildcard addresses with localhost/explicit IPv4/IPv6 loopback.
- [ ] Run same test GREEN and TestConnDeduplication targeted host probe.
- [ ] Run focused normal/race Q/core checks and net targeted regression; compile
  and vet affected packages; commit scoped repair after observed passes.

One final fresh-context review for this plan range after verification. Preserve
user edits/.codanna and worktree. Full suite remains open until actual pass;
this fix is evidence for one host blocker only. No merge/push.

Ruling: initial transport hypothesis rejected. Explicit connected IPv6 test was
green before production code; speculative peer fallback removed. TestConnDeduplication
RED wildcard destination turned GREEN with loopback binding, as did DTLS TestConnGet
and net TestPacketConnReadFrom. This is a fixture portability repair, not a Q or
transport behavior change.
