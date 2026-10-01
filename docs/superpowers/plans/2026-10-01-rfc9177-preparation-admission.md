# Q1 preparation admission before owned reads

**Spec:** docs/superpowers/specs/2026-10-01-rfc9177-milestone3-completion-design.md
Execute inline TDD. This prerequisite bounds concurrent preparation by existing
callback/exchange slots; it does not claim complete byte accounting.

## Task 1: Claim lifetime slot before reading or snapshot copying

Files: udp/client/qblock_client.go, qblock_memory_test.go.
Add a behavior test that consumes the only callback slot then attempts Q1 with a
counting reader. RED: current prepare reads the body before rejecting admission.
GREEN: acquire callback slot before selectGETSZX/options/body/tag copying, defer
release on every preparation failure and hand ownership to the exchange only
once admitted. No double release; keep the existing idempotent slot release.
Test success, body failure and exhausted admission; focused normal/race, compile,
vet and whitespace. One fresh-context review and scoped commit. Preserve docs
and .codanna. Aggregate byte envelopes remain a separate architecture plan.
