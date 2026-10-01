# Bounded sparse receiver indexing

**Spec:** docs/superpowers/specs/2026-10-01-rfc9177-milestone3-completion-design.md
**Execution:** inline TDD. This slice closes sparse-map overhead only; aggregate
executor/preparation/callback/metadata accounting remains a subsequent plan.

## Task 1: Reserve indexed sparse storage before receiver admission

Replace Body's map with a fixed directory of lazy fragment pages. Each page
covers 64 blocks, owns one contiguous payload backing and a uint64 presence mask.
Directory contains exactly ceil(blockCount/64) pointers. Page allocation is lazy:
a final fragment in a large announced body allocates only its final page, never
the full body. Complete/duplicate/missing/assembly behavior remains identical.
Worst-case storage includes full payload, full assembly, directory pointer
backing, page structs, and detached identity bytes. Compute using uint64 checked
bounds after metadata validation; Manager.StartReceiver reserves it before first
intake. Explicitly account using unsafe.Sizeof application layouts, not runtime
map bucket/RSS estimates. Empty body presence is tracked without payload bytes.

Files: net/qblock/body.go, receiver.go, receiver_control.go, manager.go and tests.
Write admission regression with retained limit equal to old 2*body size; RED must
show acceptance despite missing indexing capacity. Then implement indexed pages
and reservation. Update exact boundary tests to use the new charge, including
one-byte-below rejection, release/reuse, duplicate and final sparse fragment.
Expected: go test ./net/qblock -count=1 passes and sparse payload does not allocate
announced body at first fragment. Run focused adapter tests to catch retained
limit fixture assumptions; normal/race, compile and vet plus whitespace.

One final fresh-context review of this plan range; commit only scoped verified
files and accurate roadmap/results. Preserve user edits and .codanna. No public
configuration or endpoint design implementation in this plan.
