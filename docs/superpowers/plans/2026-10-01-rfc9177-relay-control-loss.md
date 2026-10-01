# RFC9177 relay recovery-control loss

Autonomous continuation baseline1729da3b10bb5857b5f1e4b1576318fba52a748e. Bounded Milestone5 test-only slice, existing socket-free paired fake-clock dispatcher. Test POST and PUT with existing second Q1 drop/first Q2 response drop/second Q2 duplication plus either first server Missing report drop or first client Q2 repair request drop. Require complete48-byte upload/response, handler once, actual deliveries per relay event, a dropped selected control and subsequent same-kind/direction control retry. No public socket/DTLS/interop claims.

- [ ] Add four recovery-control cases using existing relay helper, preserve prior tests. Retain self-contained trace output and optional QBLOCK_TRACE_DIR per scenario, exact script. Characterization can begin GREEN; no production RED→GREEN claim.
- [ ] Mutation removing control-loss rule must fail its evidence assertions; restore and run scoped normal/race, compile/vet/diff.
- [ ] One fresh Astra/high review of new range, one material fix pass, no rereview; commit tests/plan then completion roadmap/results. Keep worktree/branch/user docs/.codanna/all ledgers, no merge/push.

Review focus: control selection, retry evidence after actual loss, body/handler outcomes, failure artifacts/cleanup, fake-clock scope and bounded claims.
