# RFC9177 relay-driven combined transfer faults

Authorized autonomous inline continuation; baseline a9b93bae2d58d1841a10b1d200d4f5c665862fa5. Bounded test-only Milestone5 slice. Reuse the established paired POST/PUT fake-clock harness; inject encoded datagrams through qblocklink, decode delivered bytes into endpoint receive handling. No socket/DTLS or independent interop claim.

## Contract

Each method uploads48bytes and receives48bytes at SZX0, MaxPayloads2. Drop second client Q1, drop first server Q2, duplicate second server Q2. Script classification is independent; encoding uses real coder. Drive only idle/published fake deadlines with existing guard, bounded100event turns. Assert upload/response equality, one handler call, existing token/fresh-MID properties, exactly specified faults in chronological trace and repair traffic. Save JSON traces with scenario method/config/time boundary in ignored ledger. Existing non-relay cases stay unchanged. No behavioral production fix unless regression demonstrates one.

## Tasks

- [x] Add optional relay path to existing paired helper; create TestQBlockRelayCombinedFaults POST/PUT with scripted rules and trace assertions. New encoder/deliver helper snapshots actual message wire; decoded inputs use existing receive dispatch, not public socket Process. Characterization may already be GREEN; no production RED→GREEN claim.
- [x] Verify fault assertions catch bypass of relay (temporary mutation RED, restore GREEN). Focus normal/race plus relay oracle; compile-only/vet/diff. Retain failed attempts.
- [x] One fresh Astra/high review only new slice. One test-only fix pass for material evidence findings, defer minors; no rereview. Append roadmap/results, commit scoped files. Keep branch/worktree/user edits/all ledgers; no merge/push.

Review focus: actual fault delivery and independent expected trace, handler/body outcomes, fake-clock races, failure cleanup, bounded evidence and claims.

## Final disposition

One fresh Astra/high reviewer: no Critical, two Important (normal tests depend on ignored trace directory; duplicate-only bypass untested). One test-only pass makes default output temporary, optional QBLOCK_TRACE_DIR explicitly persists evidence and creates its directory; checks actual endpoint deliveries per input ID. Reviewer original fresh-cwd RED and duplicate-only bypass GREEN retained; fixed fresh-cwd binary GREEN, duplicate-only suppression RED, restored scoped normal/race GREEN. No production defect or behavioral production fix claim; no rereview.

Deferred Minor: artifact lacks pacing/fake-clock start/end metadata. Prior relay two-byte extension, supplied-DTLS cleanup and deadline-race ACK details remain deferred. Final compile/vet/diff pass. Retain all artifacts; no merge/push.
