# Milestone 4 DTLS Q-Block server propagation Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans inline with TDD. Retain this plan's scoped ledger.

**Goal:** Enable bounded server-side Q-Block GET and assembled POST/PUT over DTLS using the existing public server option.

**Architecture:** Extend WithQBlockServer to DTLSServerApply and construct the existing server-only runtime for accepted DTLS sessions. Keep runtime endpoint ownership server-scoped and payload/identity state session-scoped. Validate final plaintext datagram budgets before serving, bound accepted workers, and close/prune runtime state with the server.

**Tech Stack:** Go 1.25, existing Pion DTLS and Q adapter, testify, loopback PSK fixtures; no new dependencies.

**Spec:** docs/superpowers/specs/2026-09-15-rfc9177-design.md, Milestone 4 roadmap in docs/superpowers/plans/2026-09-15-rfc9177.md; server construction contracts from docs/superpowers/specs/2026-10-01-rfc9177-milestone3-completion-design.md.

## Global Constraints

- NON Q-Block1 POST/PUT and Q-Block2 GET, assembled handlers and bounded copies.
- No public outbound Q selection or automatic capability assumption. Client probing, cache and mode/fallback matrix remain separate Milestone 4 work.
- Preserve classic behavior, disabled-Q rejection and session-local transfer identity.
- No merge/push/main-checkout edits; preserve the two user-edited documents and .codanna.
- Memory limits describe adapter-owned copies and conservative bookkeeping, not RSS or DTLS-library storage.

## Review Focus

- Invalid/final transport config must fail before listener admission (Task 1).
- Session replacement must not retain application duplicate suppression (Task 2).
- Simultaneous accepted sessions must not exceed MaxConnections or leak capacity after close (Task 2).
- Oversized decrypted records must never become valid Q upload prefixes; valid retries remain usable (Task 2).
- Stop racing admission must not expose failed connections to OnNewConn; classic requests on server-only Q connections remain classic (Tasks 1–2).

### Task 1: Public DTLS server runtime

**Files:** options/qblockOptions.go; dtls/server/config.go; dtls/server/server.go; udp/client/qblock_runtime.go (comments); dtls/qblock_test.go.

**Interfaces:** Consume QBlockServerRuntime NewConn(Session,*client.Config,...client.Option)(*client.Conn,error), ValidateTransport(uint32)error, Close(), Prune(time.Time). Produce QBlockServerOpt.DTLSServerApply(*dtlsServer.Config); Config.QBlockServer *qblock.ServerConfig; private createConn returns (*client.Conn,error).

- [x] Step 1: Write TestQBlockDTLSServerTransfers table for GET, POST, PUT using real loopback PSK DTLS and raw Q datagrams. GET returns multiple Q2 blocks; Q1 arrives final-first across two blocks and handler receives exact complete bytes once, with a Q2 response. Add TestQBlockDTLSServerInvalidConfig with zero limits and final transport owned-budget rejection; Serve(nil) errors before listener access. Disabled CON Q gets BadOption and enabled ordinary GET has no Q option.
- [x] Step 2: Run `rtk proxy go test ./dtls -run TestQBlockDTLS -count=1 -timeout=30s`. Expected RED: option lacks DTLSServerApply, then transfer failure until runtime is connected.
- [x] Step 3: Add copied DTLS server config, runtime startup/transport validation, error-returning construction with cfg.MTU propagation, failed session cleanup, periodic pruning and Stop closure. Preserve existing classic construction when disabled. Runtime remains server-only.
- [x] Step 4: Run the focused command and `rtk proxy go test ./options ./dtls/... -count=1 -timeout=60s`. Expected PASS.
- [x] Step 5: Commit scoped code/tests/plan as `feat(qblock): propagate server-only Q handling to DTLS`.

### Task 2: DTLS admission, record and session boundaries

**Files:** dtls/server/server.go; dtls/server/session.go; dtls/qblock_test.go; dtls/server/qblock_test.go; roadmap/results.

**Interfaces:** Consume Task 1 runtime/config. Produce bounded active accepted-worker reservation before handshake/OnNewConn and release after worker return. Session read cap is min(MTU,MaxMessageSize) plus one overflow byte for Q-enabled sessions; oversized or Pion short-buffer records are consumed as loss without Process; disabled session behavior is preserved.

- [x] Step 1: Write TestQBlockDTLSServerConnectionLimit proving a second authenticated peer is rejected at MaxConnections=1 and a replacement is accepted after first close. Repeat identical Q upload/token/tag on a replacement session and require a fresh handler invocation. Write TestQBlockDTLSSessionOversize using datagram fake net.Conn: oversized valid-prefix record is dropped, a valid retry dispatches once, and a following ordinary datagram still works. Include short-buffer record followed by valid record and final MaxMessageSize smaller than MTU.
- [x] Step 2: Run `rtk proxy go test ./dtls/... -run TestQBlockDTLS -count=1 -timeout=30s`. Expected RED: second worker admitted and oversized record terminates/dispatches rather than retaining valid retry.
- [x] Step 3: Reserve bounded workers in Serve before goroutine/handshake; release with defer. Add Q-only session read guard with overflow byte and short-buffer loss handling. Keep failed construction closed and runtime Close idempotent.
- [x] Step 4: Run focused normal/race; `rtk proxy go test ./... -run '^$' -count=1 -timeout=180s`; `rtk proxy go vet ./...`; `rtk proxy go test ./... -count=1 -timeout=180s`; `rtk git diff --check`. Expected PASS. Update roadmap/results: DTLS server slice complete, Milestone 4 client work open.
- [x] Step 5: Commit as `fix(qblock): bound DTLS admission and receive records`.

## Completion

One fresh-context Astra/high review of 16da62f..completed plan HEAD, with this plan, approved specs and retained ledger; one TDD fix pass for material findings. Record deferred minors and every ruling. Verify preserved edits by original hashes. Retain workspace/ledger as explicitly requested. No integration action.
