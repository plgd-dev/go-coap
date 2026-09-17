# RFC 9177 private Q-Block2 download adapter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an internally enabled UDP client path that completes a NON Q-Block2 GET response through the existing `Do` response lifecycle.

**Architecture:** A private `qblockReceiver` owns `net/qblock.Manager`, initial request templates, transfer response metadata, and an action executor. `Conn` prepares eligible initial GETs and gives Q2 responses first chance at dispatch; normal token and classic blockwise behavior remains the fallback. The manager is called under the adapter mutex and all writes, handler calls, and caller error notifications occur afterward.

**Tech Stack:** Go 1.25, `udp/client`, existing pooled CoAP messages, `net/qblock.Manager`, `testify/require`, fake `Session`; no new dependencies and no socket test requirement.

**Spec:** [private Q-Block2 download adapter design](../specs/2026-09-16-rfc9177-q2-download-adapter-design.md)

## Global constraints

- Add no exported Q-Block option, `options` configuration, capability cache, server support, Q1 behavior, Observe support, or generic `WriteMessage` interception.
- Enable the slice only through an unexported `withQBlockReceiver(qblockReceiverConfig)` option used by same-package tests; `NewConnWithOpts` defaults to no Q2 receiver.
- Q2 uses only GET without Observe or request body and uses NON for the initial, Continue, and repair messages.
- Initial Q2 GET is one encoded QBlock2 option `Block{Number:0, More:true, SZX:cc.blockwiseSZX}`. Continue encodes `{Number:Through+1, More:true}`. Repairs use exactly the ascending manager numbers with `More:false`.
- Require response code `codes.Content`, exactly one valid QBlock2 option, a nonempty ETag, Size2, and stable response metadata on every fragment. Never retain a pooled message or caller-owned byte slice.
- A malformed/conflicting first fragment creates no manager operation, manager token binding, manager retained-byte reservation, or adapter transfer record. Do not remove the already-installed ordinary token handler.
- Serialize all manager/map transitions with the adapter mutex. Do not write, invoke a handler, or call an error notifier while holding it.
- Treat Q2 output `Complete.Err`, a control write failure, context cancellation, and connection close as terminal cleanup that wakes the original `doInternal` caller.

---

## File map

| File | Responsibility |
| --- | --- |
| `udp/client/conn.go` | construct the private receiver, prepare eligible initial requests, call Q2 before existing response routing, tick and close it, retain configured token generator |
| `udp/client/qblock_receiver.go` | private configuration, copied request/response metadata, mutex-protected manager adapter, semantic fragment conversion, action execution, and cleanup |
| `udp/client/qblock.go` | keep existing disabled server-side request rejection; add only shared Q option validation helpers if they are not receiver-specific |
| `udp/client/qblock_receiver_test.go` | fake session plus deterministic private Q2 receiver/connection tests |
| `udp/client/qblock_internal_test.go` | retain disabled-mode response-drop coverage; move no behavior into public test package |
| `docs/superpowers/specs/2026-09-16-rfc9177-q2-download-adapter-design.md` | update status and exact validation evidence after all tasks pass |
| `docs/superpowers/plans/2026-09-15-rfc9177-results.md` | append the private adapter checks and explicit remaining public-scope limitations |

## Task 1: private construction and initial GET preparation

**Files:** Modify `udp/client/conn.go`; create `udp/client/qblock_receiver.go`; create `udp/client/qblock_receiver_test.go`.

**Interfaces:**

```go
type qblockReceiverConfig struct {
	Manager qblock.ManagerConfig
	Now func() time.Time
}

type qblockReceiver struct { /* private mutex, manager, pending, transfers, conn */ }

func withQBlockReceiver(cfg qblockReceiverConfig) Option
func newQBlockReceiver(cc *Conn, cfg qblockReceiverConfig) *qblockReceiver
func (r *qblockReceiver) prepare(req *pool.Message, fail func(error)) (bool, error)
func (r *qblockReceiver) abandon(token message.Token, err error)
```

`qblockReceiver` also owns `transferByToken map[string]qblock.TransferID`, populated for the initial token after `StartReceiver` and for every successful fresh `Manager.BindToken`; remove every index entry on `Release`. `ConnOptions` has `createQBlockReceiver func(*Conn) *qblockReceiver`; `Conn` has `qblockReceiver *qblockReceiver` and `getToken func() message.Token`. `NewConnWithOpts` assigns `cfg.GetToken` to `getToken`, constructs its private receiver after `Conn` fields are initialized, and registers an `AddOnClose` cleanup callback.

- [ ] **Step 1: Write failing construction and preparation tests**

```go
func TestQBlockReceiverIsPrivateAndIdleByDefault(t *testing.T) {
	cc := newTestConn(t)
	require.Nil(t, cc.qblockReceiver)
}

func TestQBlockPrepareInitialGET(t *testing.T) {
	cc, clock, session := newQBlockTestConn(t)
	req := newGET(t, cc, "/temperature")
	errCh := make(chan error, 1)
	prepared, err := cc.qblockReceiver.prepare(req, func(err error) { errCh <- err })
	require.NoError(t, err)
	require.True(t, prepared)
	block, err := qblock.DecodeBlock(mustOptionUint32(t, req, message.QBlock2))
	require.NoError(t, err)
	require.Equal(t, qblock.Block{Number: 0, More: true, SZX: cc.blockwiseSZX}, block)
	require.Equal(t, message.NonConfirmable, req.Type())
	require.Zero(t, cc.qblockReceiver.active())
	require.Empty(t, session.writes)
	require.Empty(t, errCh)
	_ = clock
}

func TestQBlockPrepareLeavesUnsupportedRequestShapesOrdinary(t *testing.T) {
	cc, _, _ := newQBlockTestConn(t)
	for _, mutate := range []func(*pool.Message){withObserve, withBody, withExistingQBlock} {
		req := newGET(t, cc, "/temperature")
		mutate(req)
		prepared, err := cc.qblockReceiver.prepare(req, func(error) {})
		require.NoError(t, err)
		require.False(t, prepared)
		require.Zero(t, cc.qblockReceiver.active())
	}
}
```

- [ ] **Step 2: Run the focused test to verify it fails**

Run: `GOCACHE=/tmp/go-coap-qblock-cache go test ./udp/client -run 'QBlockReceiverIsPrivate|QBlockPrepare' -count=1`

Expected: FAIL because `qblockReceiver`, `withQBlockReceiver`, and the test constructor do not exist.

- [ ] **Step 3: Implement the minimal private construction path**

```go
type ConnOptions struct {
	createBlockWise      func(*Conn) *blockwise.BlockWise[*Conn]
	createQBlockReceiver func(*Conn) *qblockReceiver
	// existing fields
}

func withQBlockReceiver(cfg qblockReceiverConfig) Option {
	return func(opts *ConnOptions) {
		opts.createQBlockReceiver = func(cc *Conn) *qblockReceiver {
			return newQBlockReceiver(cc, cfg)
		}
	}
}

func (r *qblockReceiver) prepare(req *pool.Message, fail func(error)) (bool, error) {
	if req.Code() != codes.GET || req.HasOption(message.Observe) || req.Body() != nil || req.HasOption(message.QBlock1) || req.HasOption(message.QBlock2) {
		return false, nil
	}
	value, err := qblock.EncodeBlock(qblock.Block{Number: 0, More: true, SZX: r.cc.blockwiseSZX})
	if err != nil { return false, err }
	req.SetType(message.NonConfirmable)
	req.SetOptionUint32(message.QBlock2, value)
	r.pending[string(req.Token())] = newPendingTemplate(req, fail)
	return true, nil
}
```

Copy request options and token into the pending template. Do not retain `req` itself. If `NewManager` rejects the private configuration, retain that `initErr` in the receiver and return it from `prepare`; `NewConnWithOpts` cannot itself return an error. Tests use `DefaultManagerConfig` with a fixed `Now`. Keep default connection construction nil and unchanged.

- [ ] **Step 4: Connect preparation to `doInternal` and cleanup**

Create `qblockErrChan := make(chan error, 1)` alongside `respChan`. If the private receiver is nonnil, call `prepare(req, sendOnce(qblockErrChan))` before `writeMessage`; only prepare eligible GETs, leaving other ordinary requests untouched. Extend the select with `case err := <-qblockErrChan: return nil, err`. Extend the existing deferred token-handler deletion to call `abandon(token, context.Canceled)` after removing the handler. If `writeMessage` fails, the deferred abandonment removes its pending template.

- [ ] **Step 5: Run focused tests to verify they pass**

Run: `GOCACHE=/tmp/go-coap-qblock-cache go test ./udp/client -run 'QBlockReceiverIsPrivate|QBlockPrepare' -count=1`

Expected: PASS. Also run `GOCACHE=/tmp/go-coap-qblock-cache go test ./udp/client -run 'TestDo|TestConn' -count=1` to prove ordinary request setup still passes.

- [ ] **Step 6: Commit the first vertical boundary**

```bash
git add udp/client/conn.go udp/client/qblock_receiver.go udp/client/qblock_receiver_test.go
git commit -m "feat(qblock): prepare private q2 get requests"
```

## Task 2: first-fragment validation and transactional receiver start

**Files:** Modify `udp/client/qblock_receiver.go`, `udp/client/conn.go`, `udp/client/qblock_receiver_test.go`.

**Interfaces:**

```go
func (r *qblockReceiver) handle(w *responsewriter.ResponseWriter[*Conn], msg *pool.Message) bool
func (r *qblockReceiver) startLocked(msg *pool.Message, pending *qblockPending, now time.Time) ([]qblock.Output, error)
func fragmentFromQ2(msg *pool.Message, operation qblock.OperationKey, meta *qblock.Metadata) (qblock.Fragment, qblock.Metadata, error)
func q2OperationKey(token message.Token, etag []byte) (qblock.OperationKey, error)
```

`handle` returns false only when the message lacks QBlock2. It returns true for every Q2 message, including an invalid or unknown one, so classic routing never consumes it.

- [ ] **Step 1: Write failing first-fragment and rollback tests**

```go
func TestQBlockFirstFragmentStartsReceiverWithoutDelivering(t *testing.T) {
	cc, clock, _ := newQBlockTestConn(t)
	original := startQ2GET(t, cc, "/temperature")
	msg := q2Response(t, original, 0, true, blockwise.SZX64, 128, []byte("etag-a"), []byte("first"))
	require.True(t, cc.qblockReceiver.handle(testWriter(t, cc), msg))
	require.Equal(t, uint32(1), cc.qblockReceiver.active())
	require.Equal(t, original, cc.qblockReceiver.originalTokenForTest())
	require.Empty(t, cc.qblockReceiver.deliveriesForTest())
	_ = clock
}

func TestQBlockInvalidFirstFragmentRollsBack(t *testing.T) {
	for _, mutate := range []func(*pool.Message){removeETag, removeSize2, duplicateQBlock2, invalidQBlock2, conflictingFirstMetadata} {
		t.Run(testName(mutate), func(t *testing.T) {
			cc, _, session := newQBlockTestConn(t)
			original := startQ2GET(t, cc, "/temperature")
			msg := q2Response(t, original, 0, true, blockwise.SZX64, 128, []byte("etag-a"), []byte("first"))
			mutate(msg)
			require.True(t, cc.qblockReceiver.handle(testWriter(t, cc), msg))
			require.Zero(t, cc.qblockReceiver.active())
			require.Zero(t, cc.qblockReceiver.retainedForTest())
			require.Empty(t, cc.qblockReceiver.transfersForTest())
			_, ordinaryHandlerStillRegistered := cc.tokenHandlerContainer.Load(original.Hash())
			require.True(t, ordinaryHandlerStillRegistered)
			require.Empty(t, session.writes)
		})
	}
}
```

Use a valid first response with a block payload length compatible with its number/SZX and Size2. Cover conflicting first input with a duplicate ETag, mixed QBlock1/QBlock2 options, and a token that the private adapter indexes to another transfer; none may call `StartReceiver`. Import `net/blockwise` for its `SZX64` and `SZX16` constants. Add a direct `Manager.Active`/retained assertion via private test-only accessors; never make these exported production methods.

- [ ] **Step 2: Run the rollback tests to verify they fail**

Run: `GOCACHE=/tmp/go-coap-qblock-cache go test ./udp/client -run 'QBlockFirstFragment|QBlockInvalidFirstFragment' -count=1`

Expected: FAIL because Q2 responses still route through the ordinary token handler and no semantic validator exists.

- [ ] **Step 3: Implement semantic conversion before state publication**

```go
func fragmentFromQ2(msg *pool.Message, operation qblock.OperationKey, prior *qblock.Metadata) (qblock.Fragment, qblock.Metadata, error) {
	if msg.Code() != codes.Content { return qblock.Fragment{}, qblock.Metadata{}, errInvalidQBlockResponse }
	if err := qblock.ValidateOptions(msg.Options(), false); err != nil { return qblock.Fragment{}, qblock.Metadata{}, err }
	if countOption(msg.Options(), message.QBlock2) != 1 { return qblock.Fragment{}, qblock.Metadata{}, errInvalidQBlockResponse }
	block, err := qblock.DecodeBlock(mustOptionUint32(msg, message.QBlock2))
	if err != nil { return qblock.Fragment{}, qblock.Metadata{}, err }
	etag, err := msg.ETag(); if err != nil || len(etag) == 0 { return qblock.Fragment{}, qblock.Metadata{}, errMissingQBlockETag }
	size, err := msg.GetOptionUint32(message.Size2); if err != nil { return qblock.Fragment{}, qblock.Metadata{}, errMissingQBlockSize2 }
	payload := []byte(nil)
	if body := msg.Body(); body != nil { payload, err = io.ReadAll(body); if err != nil { return qblock.Fragment{}, qblock.Metadata{}, err } }
	meta := qblock.Metadata{Size: size, SZX: block.SZX, Identity: bytes.Clone(etag)}
	// Preserve optional Content-Format presence, then compare all fixed metadata to prior.
	if prior != nil && !sameMetadata(*prior, meta) { return qblock.Fragment{}, qblock.Metadata{}, errQBlockMetadataConflict }
	return qblock.Fragment{Operation: operation, Token: msg.Token(), Kind: qblock.Q2, Metadata: meta, Block: block, Payload: payload}, meta, nil
}
```

Use error-returning reads and copies. Do not use `mustOptionUint32` in production. Validate exactly one ETag as well as one QBlock2; reject QBlock1/mixed options through `ValidateOptions`. Ensure `qblock.NewBody` performs the final offset/size/payload validation before manager state commits.

- [ ] **Step 4: Intercept Q2 before old response routing and start atomically**

At the beginning of `Conn.handle`, after the separate-message check, call `cc.qblockReceiver.handle(w, m)` when nonnil and return when it consumes. In `startLocked`, look up the copied pending record by incoming token, build `q2OperationKey(pending.originalToken, etag)`, call `Manager.StartReceiver`, then insert the transfer template and its initial `transferByToken` entry only after that call succeeds. For later packets, resolve `transferByToken` first, use its saved operation and metadata for conversion, then call `Manager.Receive`. On a first-start error, leave `pending`, normal token handler, manager state, transfer map, and token index unchanged. On a follow-on conversion or manager error, cancel the resolved transfer. Execute copied outputs only after releasing the mutex.

- [ ] **Step 5: Run rollback, disabled-mode, and ordinary response tests**

Run: `GOCACHE=/tmp/go-coap-qblock-cache go test ./udp/client -run 'QBlockFirstFragment|QBlockInvalidFirstFragment|QBlockResponseDropped|DisabledQBlock' -count=1`

Expected: PASS. The disabled public behavior remains unchanged because the private receiver is nil there.

- [ ] **Step 6: Commit transactional Q2 receiver start**

```bash
git add udp/client/conn.go udp/client/qblock_receiver.go udp/client/qblock_receiver_test.go
git commit -m "feat(qblock): start q2 receivers transactionally"
```

## Task 3: action executor, complete response delivery, and control packets

**Files:** Modify `udp/client/qblock_receiver.go`, `udp/client/qblock_receiver_test.go`.

**Interfaces:**

```go
func (r *qblockReceiver) execute(outputs []qblock.Output)
func (r *qblockReceiver) writeControl(id qblock.TransferID, action qblock.Action) error
func (r *qblockReceiver) newControlRequest(record *qblockTransfer, token message.Token, action qblock.Action) (*pool.Message, error)
func (r *qblockReceiver) deliver(id qblock.TransferID, action qblock.Action)
func (r *qblockReceiver) fail(id qblock.TransferID, err error)
```

`qblockTransfer` owns copied original response token, canonical GET request options, saved ordinary response options, response code, and fail callback. It contains no pooled `Message`. `newControlRequest` clones only allowed original request options, removes `QBlock2`, `ETag`, `Observe`, and `Size2`, clears payload, adds fresh QBlock2 option values, type NON, fresh MID, and the newly reserved token.

- [ ] **Step 1: Write failing delivery and control-packet tests**

```go
func TestQBlockDeliversCompleteBodyThroughOriginalToken(t *testing.T) {
	cc, _, _ := newQBlockTestConn(t)
	original, response := startDoQ2GET(t, cc, "/temperature")
	for _, fragment := range []*pool.Message{
		q2Response(t, original, 0, true, blockwise.SZX16, 20, []byte("etag-a"), []byte("0123456789abcdef")),
		q2Response(t, original, 1, false, blockwise.SZX16, 20, []byte("etag-a"), []byte("ghij")),
	} { cc.handle(testWriter(t, cc), fragment) }
	got := <-response
	body, _ := io.ReadAll(got.Body())
	require.Equal(t, []byte("0123456789abcdefghij"), body)
	require.Equal(t, original, got.Token())
	require.False(t, got.HasOption(message.QBlock2))
	require.False(t, got.HasOption(message.Size2))
	require.Zero(t, cc.qblockReceiver.active())
}

func TestQBlockContinueAndRepairUseFreshTokensAndCorrectOptions(t *testing.T) {
	cc, clock, session := newQBlockTestConn(t)
	original := startQ2GET(t, cc, "/temperature")
	cc.handle(testWriter(t, cc), q2Response(t, original, 0, true, blockwise.SZX16, 176, []byte("etag-a"), bytes.Repeat([]byte{'a'}, 16)))
	cc.handle(testWriter(t, cc), q2Response(t, original, 9, true, blockwise.SZX16, 176, []byte("etag-a"), bytes.Repeat([]byte{'b'}, 16)))
	requireQ2Request(t, session.popWrite(), 10, true)
	clock.Advance(4 * time.Second)
	cc.CheckExpirations(clock.Now())
	requireQ2Repair(t, session.popWrite(), []uint32{1, 2, 3, 4, 5, 6, 7, 8})
}
```

The delivery test must set a Content-Format and ETag then assert those ordinary metadata options are present on the synthetic response. The control test must prove new tokens differ from the original and each other, M is set only for continuation, repair numbers are strictly ascending, and repair omits ETag and Observe.

- [ ] **Step 2: Run action tests to verify they fail**

Run: `GOCACHE=/tmp/go-coap-qblock-cache go test ./udp/client -run 'QBlockDelivers|QBlockContinueAndRepair' -count=1`

Expected: FAIL because outputs are not converted to writes or synthetic normal responses.

- [ ] **Step 3: Implement action execution without lock-held I/O**

For every `SendContinue` or `RequestMissing`, allocate a token by looping `cc.getToken()`, rejecting an empty value, any `tokenHandlerContainer` hit, and any failed `Manager.BindToken(id, token)` result; bound this loop to 32 attempts and fail the transfer with a stable allocation error. Build the control packet, write with `cc.session.WriteMessage`, and release it with `cc.ReleaseMessage`. On construction or write failure, call `Manager.Cancel(id, err)` under the mutex and execute its resulting `Complete`/`Release` outputs after unlock.

For `Deliver`, acquire a pooled message, set `codes.Content`, saved original token, saved copied response options excluding QBlock2/Size2, and `bytes.NewReader(action.Payload)`. Then atomically `LoadAndDelete` the original token handler and invoke it; if absent, release the synthetic message. On `Complete.Err`, delete the handler and invoke the stored one-shot error callback. On `Release`, delete the private transfer map entry. Ignore `Duplicate` after verifying no delivery occurs.

- [ ] **Step 4: Add failure-path tests and implement cleanup**

```go
func TestQBlockControlWriteFailureReleasesEverything(t *testing.T) {
	cc, clock, session := newQBlockTestConn(t)
	session.writeErr = errors.New("network down")
	original, result := startDoQ2GET(t, cc, "/temperature")
	startIncompleteQ2Set(t, cc, original)
	clock.Advance(4 * time.Second)
	cc.CheckExpirations(clock.Now())
	require.ErrorIs(t, <-result, session.writeErr)
	require.Zero(t, cc.qblockReceiver.active())
	require.Zero(t, cc.qblockReceiver.retainedForTest())
	_, ordinaryHandlerStillRegistered := cc.tokenHandlerContainer.Load(original.Hash())
	require.False(t, ordinaryHandlerStillRegistered)
}
```

Implement `fail` so it removes the normal original-token handler before notifying the result channel. Add an assertion that control packet send failure leaves no active manager tokens, pending record, transfer record, or retained manager bytes.

- [ ] **Step 5: Run focused action tests to verify they pass**

Run: `GOCACHE=/tmp/go-coap-qblock-cache go test ./udp/client -run 'QBlockDelivers|QBlockContinueAndRepair|QBlockControlWriteFailure' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit delivery and control writes**

```bash
git add udp/client/qblock_receiver.go udp/client/qblock_receiver_test.go
git commit -m "feat(qblock): execute private q2 receiver actions"
```

## Task 4: deadlines, cancellation, shutdown, and regression validation

**Files:** Modify `udp/client/conn.go`, `udp/client/qblock_receiver.go`, `udp/client/qblock_receiver_test.go`, `docs/superpowers/specs/2026-09-16-rfc9177-q2-download-adapter-design.md`, `docs/superpowers/plans/2026-09-15-rfc9177-results.md`.

**Interfaces:**

```go
func (r *qblockReceiver) Tick(now time.Time)
func (r *qblockReceiver) close(err error)
func (r *qblockReceiver) abandon(token message.Token, err error)
```

`Conn.CheckExpirations(now)` calls `qblockReceiver.Tick(now)` immediately after the existing cache/blockwise expiration work. `NewConnWithOpts` registers `qblockReceiver.close` using `session.AddOnClose` only when private Q2 is enabled.

- [ ] **Step 1: Write failing expiration, cancellation, and shutdown tests**

```go
func TestQBlockExpiryFailsOriginalCallAndReleasesState(t *testing.T) {
	cc, clock, _ := newQBlockTestConn(t)
	original, result := startDoQ2GET(t, cc, "/temperature")
	startIncompleteQ2Set(t, cc, original)
	for range qblock.DefaultTransferConfig().NonMaxRetransmit + 1 {
		clock.Advance(4 * time.Second)
		cc.CheckExpirations(clock.Now())
	}
	require.ErrorIs(t, <-result, qblock.ErrRetriesExhausted)
	require.Zero(t, cc.qblockReceiver.active())
}

func TestQBlockContextCancellationAndCloseReleaseState(t *testing.T) {
	cc, _, session := newQBlockTestConn(t)
	original, result := startDoQ2GET(t, cc, "/temperature")
	startIncompleteQ2Set(t, cc, original)
	cancelOriginalRequest(t, cc, original)
	require.ErrorIs(t, <-result, context.Canceled)
	require.Zero(t, cc.qblockReceiver.active())
	original, result = startDoQ2GET(t, cc, "/humidity")
	startIncompleteQ2Set(t, cc, original)
	session.close()
	require.Error(t, <-result)
	require.Zero(t, cc.qblockReceiver.active())
}
```

- [ ] **Step 2: Run lifecycle tests to verify they fail**

Run: `GOCACHE=/tmp/go-coap-qblock-cache go test ./udp/client -run 'QBlockExpiry|QBlockContextCancellationAndClose' -count=1`

Expected: FAIL because manager deadlines and connection lifecycle are not yet wired.

- [ ] **Step 3: Implement lifecycle integration**

```go
func (cc *Conn) CheckExpirations(now time.Time) {
	// existing checks
	if cc.qblockReceiver != nil { cc.qblockReceiver.Tick(now) }
}

func (r *qblockReceiver) Tick(now time.Time) {
	r.mu.Lock()
	outputs := r.manager.Tick(now)
	r.mu.Unlock()
	r.execute(outputs)
}
```

`abandon` removes a pending template or calls `Manager.Cancel` for its active transfer, then executes the outputs. `close` snapshots all pending and active IDs under its mutex, cancels each manager operation with `qblock.ErrClosed`, clears pending, unlocks, and executes each result once. It must tolerate `doInternal` deferred cleanup racing the close callback; the mutex and handler `LoadAndDelete` give a single completion.

- [ ] **Step 4: Add malformed follow-on conflict and ordinary-regression tests**

```go
func TestQBlockConflictingFollowOnFailsAndDoesNotLeak(t *testing.T) {
	cc, _, _ := newQBlockTestConn(t)
	original, result := startDoQ2GET(t, cc, "/temperature")
	cc.handle(testWriter(t, cc), q2Response(t, original, 0, true, blockwise.SZX16, 32, []byte("etag-a"), bytes.Repeat([]byte{'a'}, 16)))
	cc.handle(testWriter(t, cc), q2Response(t, original, 1, false, blockwise.SZX16, 32, []byte("etag-b"), bytes.Repeat([]byte{'b'}, 16)))
	require.ErrorIs(t, <-result, errQBlockMetadataConflict)
	require.Zero(t, cc.qblockReceiver.active())
}
```

Add ordinary GET and existing classic Block2 regression cases using a nil private receiver, and assert their request type/options and handler delivery are unchanged.

- [ ] **Step 5: Run required verification**

Run:

```bash
GOCACHE=/tmp/go-coap-qblock-cache go test ./udp/client -count=1
GOCACHE=/tmp/go-coap-qblock-cache go test -race ./udp/client -count=1
GOCACHE=/tmp/go-coap-qblock-cache go test ./net/qblock -count=1
gofmt -d udp/client/conn.go udp/client/qblock.go udp/client/qblock_receiver.go udp/client/qblock_receiver_test.go
git diff --check
```

Expected: all focused tests and race tests pass; formatter and diff checks have no output. If a full repository test cannot bind UDP sockets in the sandbox, record the exact failure and do not claim it passed.

- [ ] **Step 6: Update evidence and commit**

Change the design status to implemented private vertical slice only. Append focused/race test commands and results to `docs/superpowers/plans/2026-09-15-rfc9177-results.md`, explicitly retaining the deferred public option, capability, Q1/server, Observe, multicast, DTLS, and interoperability work.

```bash
git add udp/client/conn.go udp/client/qblock_receiver.go udp/client/qblock_receiver_test.go docs/superpowers/specs/2026-09-16-rfc9177-q2-download-adapter-design.md docs/superpowers/plans/2026-09-15-rfc9177-results.md
git commit -m "feat(qblock): run private q2 download lifecycle"
```

## Coverage check

| Requirement | Task |
| --- | --- |
| No public configuration and unchanged default client | 1, 4 |
| Q2 initial request and strict first-fragment rollback | 1, 2 |
| ETag/Size2/option semantic checks | 2 |
| Manager receiver routing and no pooled-message retention | 2 |
| Continue, repair ordering, fresh token allocation | 3 |
| Ordinary `Do` response delivery and write failure cleanup | 3 |
| Timers, cancellation, close, follow-on conflict cleanup | 4 |
| Disabled-mode and ordinary client regression coverage | 2, 4 |
