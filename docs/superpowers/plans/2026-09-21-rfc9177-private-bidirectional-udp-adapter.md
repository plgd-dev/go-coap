# Private bidirectional UDP Q-Block adapter Implementation Plan

> For agentic workers: REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

Goal: Add a test-only private server role to udp/client.Conn that receives Q-Block1 requests, dispatches complete bodies once, and retains Q-Block2 responses for controls and repair.

Architecture: The existing private qblockClient remains the per-connection coordinator and continues to own its one qblock.Manager, mutex, action executor, and connection token/MID registries. A private qblockServer role shares those resources while keeping its Q1 receive, completed-request, and Q2 representation records separate from outgoing client exchanges. It is constructed only by unexported udp/client options in same-package tests; no udp/server wiring or exported configuration is added.

Tech Stack: Go 1.25, udp/client, net/qblock.Manager, pooled CoAP messages, testify/require, deterministic fake Session.

Spec: docs/superpowers/specs/2026-09-21-rfc9177-private-bidirectional-udp-adapter-design.md

## Global Constraints

- Keep the adapter private to same-package udp/client tests. Do not modify udp/server, DTLS, options, or exported API.
- Keep one qblock.Manager per Conn; MaxTransfers, MaxTokens, and MaxRetainedBytes cover both private client and private server records.
- Call the manager only while qblockClient.mu is held. Do not write packets, invoke handlers/callbacks, or retain a pooled message while holding that mutex.
- Copy every retained token, Request-Tag, option list, payload, metadata field, and response representation. Never retain a pool.Message.
- Q1 accepts only NON POST/PUT. Validate raw Q options before state publication and reject mixed classic/Q options.
- A failed first Q1 fragment must leave no manager operation, manager token, manager retained bytes, server map entry, connection token reservation, or copied payload bytes.
- Q2 responses reuse the selected inbound Q1 request token and allocate a fresh MID for every NON message. They never allocate response tokens.
- Q2 controls have fresh inbound tokens. Locate their retained representation from validated request identity before Manager.BindToken.
- Keep completed-request records and Q2 senders until their bounded lifetime expires, even after a response write fails. Remove active wire resources promptly.
- Conn.CheckExpirations may tick and expire the private role; pacing, a one-deadline scheduler, probing-rate accounting, packet sizing, public enablement, and udp/server wiring remain deferred.

## Review Focus

- A malformed first fragment with a token already used by an unrelated private client transfer leaves that client transfer, its manager reservation, and its token route untouched. Task 2.
- An out-of-order valid first Q1 block can create one receiver, while a later block with changed Size1, SZX, method, request options, or Request-Tag cancels only that receiver. Task 2.
- A duplicate complete upload with a different MID never runs the application handler twice, even when the original response write failed. Task 3.
- A Q2 control with a fresh token cannot select a retained response by token collision, partial Request-Tag match, or matching ETag alone. Task 4.
- Expiry, Reset, encoding failure, and connection close release all active manager/token/MID state once while a completed duplicate record survives until its configured expiry. Task 5.

---

## File map

| File | Responsibility |
| --- | --- |
| udp/client/conn.go | private construction, request routing before the disabled-Q gate, periodic tick, close hookup |
| udp/client/qblock_client.go | shared manager/action coordinator; preserve and extend client control identity |
| udp/client/qblock_server.go | private server role, request canonicalization, Q1 receipt, completed records, response sender, Q2 control routing |
| udp/client/qblock_server_test.go | fake-session server-role tests, rollback, dispatch, Q2 controls, expiry and close |
| udp/client/qblock_client_test.go | regression for Request-Tag retention on Q1-to-Q2 client controls and shared manager limits |
| docs/superpowers/specs/2026-09-21-rfc9177-private-bidirectional-udp-adapter-design.md | record implementation status and the private construction boundary |
| docs/superpowers/plans/2026-09-15-rfc9177-results.md | record focused validation and deferred public/server wiring |

### Task 1: private server-role construction and shared coordinator

Files:
- Modify: udp/client/conn.go
- Modify: udp/client/qblock_client.go
- Create: udp/client/qblock_server.go
- Create: udp/client/qblock_server_test.go
- Modify: udp/client/qblock_client_test.go

Interfaces:

    type qblockServerConfig struct {
        Retention time.Duration
    }

    type qblockServer struct {
        owner                *qblockClient
        retention            time.Duration
        activeByOperation    map[qblock.OperationKey]*qblockServerTransfer
        activeByToken        map[string]*qblockServerTransfer
        completedByOperation map[qblock.OperationKey]*qblockCompletedRequest
        senderByOperation    map[qblock.OperationKey]*qblockServerSender
    }

    func withQBlockServer(cfg qblockServerConfig) Option
    func newQBlockServer(owner *qblockClient, cfg qblockServerConfig) *qblockServer
    func (c *qblockClient) active() uint32
    func (c *qblockClient) close()
    func (c *qblockClient) Tick(now time.Time)
    type qblockClock struct { now time.Time }
    func (c *qblockClock) Now() time.Time
    func newQBlockServerTestConn(t *testing.T, cfg qblock.ManagerConfig) (*Conn, *qblockTestSession, *qblockClock)
    func newQBlockServerHandlerConn(t *testing.T, handler HandlerFunc) (*Conn, *qblockClock, *qblockTestSession)
    func q1Request(t *testing.T, cc *Conn, token message.Token, number uint32, more bool, size uint32, tag, body []byte) *pool.Message
    func q2ControlRequest(t *testing.T, cc *Conn, token message.Token, code codes.Code, tag []byte, number uint32, more bool) *pool.Message
    func requireQBlockServerEmpty(t *testing.T, s *qblockServer)

Add qblockServer *qblockServer to qblockClient. Add an unexported
qblockServerConfig *qblockServerConfig field to ConnOptions. withQBlockServer
stores a copied config. NewConnWithOpts constructs a server role only after it
has constructed a nonnil qblockClient. A default connection, or a connection
with only withQBlockClient, has no server role.

- [ ] Step 1: Write failing construction and shared-limit tests

    func TestQBlockServerRoleIsPrivateAndDisabledByDefault(t *testing.T) {
        cc := newTestConn(t)
        require.Nil(t, cc.qblockClient)

        cc, _, _ = newQBlockTestConn(t)
        require.NotNil(t, cc.qblockClient)
        require.Nil(t, cc.qblockClient.qblockServer)
    }

func TestQBlockServerSharesCoordinatorManager(t *testing.T) {
    cc, _, _ := newQBlockServerTestConn(t, qblock.DefaultManagerConfig())
    require.NotNil(t, cc.qblockClient.qblockServer)
    require.Same(t, cc.qblockClient, cc.qblockClient.qblockServer.owner)
    require.Same(t, cc.qblockClient.manager, cc.qblockClient.qblockServer.owner.manager)
}

- [ ] Step 2: Run the focused test to verify it fails

Run: GOCACHE=/tmp/go-coap-qblock-cache go test ./udp/client -run 'QBlockServerRoleIsPrivate|QBlockServerSharesCoordinator' -count=1

Expected: FAIL because withQBlockServer, qblockServer, and handleServerRequest do not exist.

- [ ] Step 3: Implement private construction and ownership

    func withQBlockServer(cfg qblockServerConfig) Option {
        return func(opts *ConnOptions) {
            copied := cfg
            opts.qblockServerConfig = &copied
        }
    }

    func newQBlockServer(owner *qblockClient, cfg qblockServerConfig) *qblockServer {
        if cfg.Retention <= 0 {
            cfg.Retention = owner.managerConfig.Transfer.Lifetime
        }
        return &qblockServer{
            owner: owner, retention: cfg.Retention,
            activeByOperation: make(map[qblock.OperationKey]*qblockServerTransfer),
            activeByToken: make(map[string]*qblockServerTransfer),
            completedByOperation: make(map[qblock.OperationKey]*qblockCompletedRequest),
            senderByOperation: make(map[qblock.OperationKey]*qblockServerSender),
        }
    }

Store ManagerConfig in qblockClient.managerConfig at construction so the
private server derives a retention default without inventing another limit
domain. Do not create a second manager. The initial handleServerRequest returns
false; Task 2 owns request behavior.

- [ ] Step 4: Run focused construction and existing client tests

Run: GOCACHE=/tmp/go-coap-qblock-cache go test ./udp/client -run 'QBlockServerRoleIsPrivate|QBlockServerSharesCoordinator|QBlockClientPrepares' -count=1

Expected: PASS.

- [ ] Step 5: Commit the private construction seam

    git add udp/client/conn.go udp/client/qblock_client.go udp/client/qblock_server.go udp/client/qblock_server_test.go udp/client/qblock_client_test.go
    git commit -m "feat(qblock): add private server role construction"

### Task 2: transactional Q1 admission and route precedence

Files:
- Modify: udp/client/conn.go
- Modify: udp/client/qblock_client.go
- Modify: udp/client/qblock_server.go
- Modify: udp/client/qblock_server_test.go

Interfaces:

    type qblockServerTransfer struct {
        id            qblock.TransferID
        operation     qblock.OperationKey
        code          codes.Code
        options       message.Options
        requestTag    []byte
        metadata      qblock.Metadata
        responseToken message.Token
    }

    func (c *qblockClient) handleServerRequest(
        w *responsewriter.ResponseWriter[*Conn], msg *pool.Message,
    ) bool
    func serverQ1Fragment(msg *pool.Message) (qblock.Fragment, qblockServerTransfer, error)
    func serverQ1Operation(code codes.Code, opts message.Options, tags [][]byte) (qblock.OperationKey, error)
    func canonicalServerRequestOptions(opts message.Options) (message.Options, error)

canonicalServerRequestOptions clones options, removes transfer fields QBlock1,
QBlock2, Size1, Size2, ETag, Block1, and Block2, then marshals the remaining
ordered options into an owned byte slice. serverQ1Operation calls
qblock.NewOperationKey with the literal server-q1, request code, marshalled
options, and every Request-Tag as an explicit part. It never uses packet token
as the request identity.

- [ ] Step 1: Write failing rollback and route-order tests

    func TestQBlockServerInvalidFirstFragmentRollsBackEverything(t *testing.T) {
        for _, mutate := range []func(*pool.Message){
            removeRequestTag, removeSize1, duplicateQBlock1,
            addClassicBlock1, changeQ1PayloadLength,
        } {
            t.Run(testName(mutate), func(t *testing.T) {
                cc, _, _ := newQBlockServerTestConn(t, qblock.DefaultManagerConfig())
                msg := q1Request(t, cc, message.Token{1}, 0, true, 32, []byte("tag-a"), bytes.Repeat([]byte("a"), 16))
                mutate(msg)

                require.True(t, cc.qblockClient.handleServerRequest(testWriter(t, cc), msg))
                requireQBlockServerEmpty(t, cc.qblockClient.qblockServer)
                require.Zero(t, cc.qblockClient.active())
                require.Zero(t, qblockClientManagerTokenCountForTest(cc.qblockClient.manager))
                require.Zero(t, qblockClientManagerRetainedBytesForTest(cc.qblockClient.manager))
            })
        }
    }

    func TestConnRoutesPrivateServerQ1BeforeDisabledGate(t *testing.T) {
        cc, _, _ := newQBlockServerTestConn(t, qblock.DefaultManagerConfig())
        msg := q1Request(t, cc, message.Token{1}, 0, true, 32, []byte("tag-a"), bytes.Repeat([]byte("a"), 16))
        cc.ProcessReceivedMessageWithHandler(msg, cc.handleReq)
        require.Equal(t, uint32(1), cc.qblockClient.active())
    }

- [ ] Step 2: Run the focused tests to verify they fail

Run: GOCACHE=/tmp/go-coap-qblock-cache go test ./udp/client -run 'QBlockServerInvalidFirst|ConnRoutesPrivateServerQ1' -count=1

Expected: FAIL because the disabled-Q gate consumes every inbound Q request.

- [ ] Step 3: Validate before publishing manager or adapter state

In Conn.handleReq, after response-cache lookup and w.Message().SetModified(false),
call cc.qblockClient.handleServerRequest(w, req) before handleDisabledQBlock.
Return when it reports that it consumed a Q request. Non-Q requests still reach
the existing disabled gate and ordinary route.

Implement first-fragment admission as this locked sequence:

    fragment, record, err := serverQ1Fragment(msg)
    if err != nil {
        return true
    }
    if existing := s.activeByOperation[fragment.Operation]; existing != nil {
        return s.receiveExistingQ1Locked(existing, fragment, record.responseToken)
    }
    if s.completedByOperation[fragment.Operation] != nil {
        return true
    }
    outputs, err := c.manager.StartReceiver(fragment, c.now())
    if err != nil {
        return true
    }
    record.id = outputs[0].TransferID
    if id, ok := c.manager.TransferID(fragment.Operation); ok {
        record.id = id
        s.activeByOperation[record.operation] = &record
        s.activeByToken[string(fragment.Token)] = &record
    }
    s.execute(w, &record, outputs)

Do not mutate activeByOperation, activeByToken, a token reservation, or copied
payload storage until StartReceiver succeeds. For an existing operation, compare
method, canonical options, complete tag list, Size1, SZX, Content-Format
presence/value, and request identity before binding a fresh inbound token with
Manager.BindToken. A mismatch cancels only the existing transfer through the
shared manager.

- [ ] Step 4: Add the conflict and unrelated-token regression

    func TestQBlockServerConflictingFragmentCancelsOnlyResolvedReceiver(t *testing.T) {
        cc, _, _ := newQBlockServerTestConn(t, qblock.DefaultManagerConfig())
        first := q1Request(t, cc, message.Token{1}, 0, true, 32, []byte("tag-a"), bytes.Repeat([]byte("a"), 16))
        require.True(t, cc.qblockClient.handleServerRequest(testWriter(t, cc), first))

        conflicting := q1Request(t, cc, message.Token{2}, 1, false, 48, []byte("tag-a"), bytes.Repeat([]byte("b"), 16))
        require.True(t, cc.qblockClient.handleServerRequest(testWriter(t, cc), conflicting))
        requireQBlockServerEmpty(t, cc.qblockClient.qblockServer)
        require.Zero(t, cc.qblockClient.active())
    }

Run: GOCACHE=/tmp/go-coap-qblock-cache go test ./udp/client -run 'QBlockServerInvalidFirst|ConnRoutesPrivateServerQ1|QBlockServerConflicting' -count=1

Expected: PASS.

- [ ] Step 5: Commit transactional admission

    git add udp/client/conn.go udp/client/qblock_client.go udp/client/qblock_server.go udp/client/qblock_server_test.go
    git commit -m "feat(qblock): admit private server q1 transactionally"

### Task 3: assembled dispatch and bounded duplicate suppression

Files:
- Modify: udp/client/qblock_server.go
- Modify: udp/client/qblock_server_test.go

Interfaces:

    type qblockCompletedRequest struct {
        operation qblock.OperationKey
        expires   time.Time
        state     qblockRequestState
        response  *qblockServerSender
    }

    type qblockRequestState uint8
    const (
        qblockRequestExecuting qblockRequestState = iota + 1
        qblockRequestCompleted
        qblockRequestFailed
    )

    func (s *qblockServer) execute(
        w *responsewriter.ResponseWriter[*Conn], fallback *qblockServerTransfer,
        outputs []qblock.Output,
    ) []qblock.Output
    func (s *qblockServer) dispatch(
        w *responsewriter.ResponseWriter[*Conn], transfer *qblockServerTransfer,
        payload []byte,
    ) []qblock.Output
    func (s *qblockServer) releaseReceiverLocked(id qblock.TransferID)

A Deliver output publishes the completed record as qblockRequestExecuting before
calling the ordinary application path. Build a new pooled message with copied
request code/options, the delivery token, and a cloned bytes.Reader body. Call
cc.handle(w, assembled), then release the assembled request. Never invoke the
handler while qblockClient.mu is held. Clear the response writer modified bit
after its copied response has been handed to Task 4, so processResponse cannot
send an ordinary response.

- [ ] Step 1: Write failing complete-body and duplicate tests

    func TestQBlockServerDispatchesAssembledRequestOnce(t *testing.T) {
        calls := 0
        cc, _, session := newQBlockServerHandlerConn(t, func(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
            calls++
            body, err := io.ReadAll(r.Body())
            require.NoError(t, err)
            require.Equal(t, []byte("abcdefghijklmnopqrst"), body)
            require.Equal(t, codes.POST, r.Code())
            require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader([]byte("ok"))))
        })
        cc.ProcessReceivedMessageWithHandler(q1Request(t, cc, message.Token{1}, 0, true, 20, []byte("tag-a"), []byte("abcdefghijklmnop")), cc.handleReq)
        cc.ProcessReceivedMessageWithHandler(q1Request(t, cc, message.Token{2}, 1, false, 20, []byte("tag-a"), []byte("qrst")), cc.handleReq)

        require.Equal(t, 1, calls)
        require.NotEmpty(t, session.writesSnapshot())
    }

    func TestQBlockServerDuplicateCompletedUploadDoesNotRedispatch(t *testing.T) {
        calls := atomic.Int32{}
        cc, _, _ := newQBlockServerHandlerConn(t, func(*responsewriter.ResponseWriter[*Conn], *pool.Message) {
            calls.Add(1)
        })
        complete := q1Request(t, cc, message.Token{1}, 0, false, 4, []byte("tag-a"), []byte("body"))
        cc.ProcessReceivedMessageWithHandler(complete, cc.handleReq)
        duplicate := q1Request(t, cc, message.Token{9}, 0, false, 4, []byte("tag-a"), []byte("body"))
        cc.ProcessReceivedMessageWithHandler(duplicate, cc.handleReq)
        require.Equal(t, int32(1), calls.Load())
    }

- [ ] Step 2: Run the focused tests to verify they fail

Run: GOCACHE=/tmp/go-coap-qblock-cache go test ./udp/client -run 'QBlockServerDispatches|QBlockServerDuplicateCompleted' -count=1

Expected: FAIL because delivery outputs are not dispatched to an application handler.

- [ ] Step 3: Implement delivery publication and dispatch

When Manager.StartReceiver or Manager.Receive returns outputs, retain the adapter
transfer record until execute consumes its Deliver and Release outputs. The
fallback record passed from first-fragment admission handles a complete
single-fragment body, whose manager record was released before TransferID can
be queried.
releaseReceiverLocked removes only active-Q1 maps; it does not remove a
completed record.

    func (s *qblockServer) dispatch(w *responsewriter.ResponseWriter[*Conn], tr *qblockServerTransfer, body []byte) []qblock.Output {
        completed := &qblockCompletedRequest{
            operation: tr.operation, expires: s.owner.now().Add(s.retention),
            state: qblockRequestExecuting,
        }
        s.owner.mu.Lock()
        s.completedByOperation[tr.operation] = completed
        s.owner.mu.Unlock()

        request := s.owner.cc.AcquireMessage(s.owner.cc.Context())
        request.SetCode(tr.code)
        request.SetToken(bytes.Clone(tr.responseToken))
        request.ResetOptionsTo(tr.options)
        request.SetBody(bytes.NewReader(bytes.Clone(body)))
        s.owner.cc.handle(w, request)
        s.owner.cc.ReleaseMessage(request)
        return s.startResponse(w, completed, tr)
    }

Keep dispatch outside the coordinator mutex. If the handler leaves the writer
unmodified, mark the record completed with no sender and return no output. If
the handler response cannot be copied or converted, mark the record failed,
remove active receiver resources, and retain the failed record until expiry.

- [ ] Step 4: Add the failed-response-write duplicate test

    func TestQBlockServerFailedResponseWriteKeepsDuplicateRecord(t *testing.T) {
        calls := atomic.Int32{}
        cc, _, session := newQBlockServerHandlerConn(t, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
            calls.Add(1)
            require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader([]byte("response"))))
        })
        session.writeErr = errors.New("write failed")
        complete := q1Request(t, cc, message.Token{1}, 0, false, 4, []byte("tag-a"), []byte("body"))
        cc.ProcessReceivedMessageWithHandler(complete, cc.handleReq)
        cc.ProcessReceivedMessageWithHandler(q1Request(t, cc, message.Token{2}, 0, false, 4, []byte("tag-a"), []byte("body")), cc.handleReq)

        require.Equal(t, int32(1), calls.Load())
        require.Len(t, cc.qblockClient.qblockServer.completedByOperation, 1)
        require.Zero(t, cc.qblockClient.active())
    }

Run: GOCACHE=/tmp/go-coap-qblock-cache go test ./udp/client -run 'QBlockServerDispatches|QBlockServerDuplicateCompleted|QBlockServerFailedResponse' -count=1

Expected: PASS.

- [ ] Step 5: Commit assembled dispatch

    git add udp/client/qblock_server.go udp/client/qblock_server_test.go
    git commit -m "feat(qblock): dispatch assembled private q1 requests once"

### Task 4: retained Q2 response and identity-validated controls

Files:
- Modify: udp/client/qblock_client.go
- Modify: udp/client/qblock_client_test.go
- Modify: udp/client/qblock_server.go
- Modify: udp/client/qblock_server_test.go

Interfaces:

    type qblockServerSender struct {
        id               qblock.TransferID
        operation        qblock.OperationKey
        requestOperation qblock.OperationKey
        metadata         qblock.Metadata
        code             codes.Code
        options          message.Options
        expires          time.Time
    }

    func (s *qblockServer) startResponse(
        w *responsewriter.ResponseWriter[*Conn],
        completed *qblockCompletedRequest,
        request *qblockServerTransfer,
    ) []qblock.Output
    func (s *qblockServer) handleQ2Control(
        w *responsewriter.ResponseWriter[*Conn], msg *pool.Message,
    ) bool
    func qblockServerResponseETag(code codes.Code, opts message.Options, body []byte) ([]byte, error)
    func (s *qblockServer) writeQ2Response(
        sender *qblockServerSender, token message.Token, action qblock.Action,
    ) error

qblockServerResponseETag accepts exactly one existing nonempty ETag of at most
eight bytes. When the handler did not set an ETag, calculate sha256 of the
response code, canonical response options, and body, then use the first eight
bytes. Copy that ETag into response options and use it as Q2 metadata identity.

- [ ] Step 1: Write failing Q2 send and client Request-Tag tests

    func TestQBlockServerQ2ResponsesReuseDeliveryTokenAndFreshMIDs(t *testing.T) {
        cc, _, session := newQBlockServerHandlerConn(t, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
            require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader(bytes.Repeat([]byte("r"), 32))))
        })
        deliveryToken := message.Token{7}
        cc.ProcessReceivedMessageWithHandler(q1Request(t, cc, deliveryToken, 0, false, 4, []byte("tag-a"), []byte("body")), cc.handleReq)

        writes := session.writesSnapshot()
        require.Len(t, writes, 2)
        require.Equal(t, deliveryToken, writes[0].token)
        require.Equal(t, deliveryToken, writes[1].token)
        require.NotEqual(t, writes[0].mid, writes[1].mid)
        require.Equal(t, message.NonConfirmable, writes[0].typ)
        require.True(t, writes[0].options.HasOption(message.QBlock2))
        require.True(t, writes[0].options.HasOption(message.ETag))
    }

Extend TestQBlockClientHandoffQ2ControlsPreserveRequestMethodAndOptions after
it snapshots upload and control. Add this assertion for every POST/PUT and
Continue/missing subtest:

    requestTag := mustOptionBytes(t, upload.options, message.RequestTag)
    require.Equal(t, requestTag, mustOptionBytes(t, control.options, message.RequestTag))

- [ ] Step 2: Run the focused tests to verify they fail

Run: GOCACHE=/tmp/go-coap-qblock-cache go test ./udp/client -run 'QBlockServerQ2Responses|QBlockClientQ1ToQ2Controls' -count=1

Expected: FAIL because no server Q2 sender exists and handoff controls omit the saved Request-Tag.

- [ ] Step 3: Start and retain a Q2 sender

Copy response code/options/body out of the writer before calling
Manager.StartSender. Remove Q1/Q2, Size1/Size2, Block1/Block2, and Request-Tag
from copied response options before adding exactly one ETag, Size2, and QBlock2
to each transmitted response. Start the sender with its response operation key,
the selected delivery token, qblock.Q2, and the owned response body. Store
qblockServerSender in senderByOperation before executing outputs.

For each SendBlock, use the token associated with the input that caused that
output: the delivery Q1 token for initial blocks, or the incoming Q2 control
token for a Continue/repair. Construct a fresh NON message and MID; write it
outside qblockClient.mu. A Q2 Complete leaves the sender and body retained.
Only a sender Release, expiry, cancellation, or connection close removes it.

In newControlRequest, when the transfer resulted from Q1-to-Q2 handoff, add
the copied transfer.requestTag as Request-Tag before writing every Q2 Continue
or repair request. Retain the existing GET-based Q2 behavior.

- [ ] Step 4: Implement validated fresh-token control binding

A Q2 control must be a NON request with exactly one QBlock2 option, a complete
Request-Tag collection, and canonical method/options equal to the retained
sender request identity. Locate completedByOperation first, resolve its sender,
then bind its fresh packet token.

    func (s *qblockServer) handleQ2Control(w *responsewriter.ResponseWriter[*Conn], msg *pool.Message) bool {
        control, requestOperation, err := serverQ2Control(msg)
        if err != nil {
            return true
        }
        s.owner.mu.Lock()
        completed := s.completedByOperation[requestOperation]
        if completed == nil || completed.response == nil || s.owner.now().After(completed.expires) {
            s.owner.mu.Unlock()
            return true
        }
        sender := completed.response
        if err := s.owner.manager.BindToken(sender.id, msg.Token()); err != nil {
            s.owner.mu.Unlock()
            return true
        }
        outputs, err := s.owner.manager.Control(control, s.owner.now())
        s.owner.mu.Unlock()
        if err != nil {
            return true
        }
        s.executeQ2Outputs(sender, message.Token(bytes.Clone(msg.Token())), outputs)
        return true
    }

Check that the new token is not already mapped by this server role before
BindToken. If validation, lookup, or binding fails, leave the sender, manager,
and retained representation unchanged.

- [ ] Step 5: Add fresh-token correlation tests

    func TestQBlockServerQ2ControlBindsOnlyAfterIdentityLookup(t *testing.T) {
        cc, _, session := startRetainedQ2ServerResponse(t)
        before := qblockClientManagerTokenCountForTest(cc.qblockClient.manager)

        unknown := q2ControlRequest(t, cc, message.Token{9}, codes.POST, []byte("wrong-tag"), 1, true)
        cc.ProcessReceivedMessageWithHandler(unknown, cc.handleReq)
        require.Equal(t, before, qblockClientManagerTokenCountForTest(cc.qblockClient.manager))

        valid := q2ControlRequest(t, cc, message.Token{10}, codes.POST, []byte("tag-a"), 1, true)
        cc.ProcessReceivedMessageWithHandler(valid, cc.handleReq)
        require.Greater(t, qblockClientManagerTokenCountForTest(cc.qblockClient.manager), before)
        require.Contains(t, tokenStrings(session.writesSnapshot()), string(message.Token{10}))
    }

Run: GOCACHE=/tmp/go-coap-qblock-cache go test ./udp/client -run 'QBlockServerQ2Responses|QBlockClientQ1ToQ2Controls|QBlockServerQ2Control' -count=1

Expected: PASS.

- [ ] Step 6: Commit retained Q2 service

    git add udp/client/qblock_client.go udp/client/qblock_client_test.go udp/client/qblock_server.go udp/client/qblock_server_test.go
    git commit -m "feat(qblock): serve private q2 responses and controls"

### Task 5: expiry, teardown, and regression evidence

Files:
- Modify: udp/client/qblock_client.go
- Modify: udp/client/qblock_server.go
- Modify: udp/client/qblock_server_test.go
- Modify: docs/superpowers/specs/2026-09-21-rfc9177-private-bidirectional-udp-adapter-design.md
- Modify: docs/superpowers/plans/2026-09-15-rfc9177-results.md

Interfaces:

    func (s *qblockServer) Tick(now time.Time) []qblock.Output
    func (s *qblockServer) closeLocked() []qblock.Output
    func (s *qblockServer) releaseSenderLocked(id qblock.TransferID)
    func requireQBlockServerEmpty(t *testing.T, s *qblockServer)

Call manager.Tick(now) once from qblockClient.Tick, then let client and server
roles consume only outputs belonging to their own transfer maps. Expire
completed records and retained senders whose expiry is at or before now. Close
cancels all active server receivers/senders through the shared manager,
releases their maps, and clears completed records. It must leave existing
client close behavior intact.

- [ ] Step 1: Write failing expiry and close tests

    func TestQBlockServerExpiryReleasesActiveStateAndThenDuplicateRecord(t *testing.T) {
        now := time.Unix(100, 0)
        cc, _, _ := newQBlockServerHandlerConnAt(t, now, func(*responsewriter.ResponseWriter[*Conn], *pool.Message) {})
        cc.ProcessReceivedMessageWithHandler(q1Request(t, cc, message.Token{1}, 0, false, 4, []byte("tag-a"), []byte("body")), cc.handleReq)

        cc.qblockClient.Tick(now.Add(time.Second))
        require.Len(t, cc.qblockClient.qblockServer.completedByOperation, 1)

        cc.qblockClient.Tick(now.Add(qblock.DefaultTransferConfig().Lifetime))
        requireQBlockServerEmpty(t, cc.qblockClient.qblockServer)
        require.Zero(t, cc.qblockClient.active())
    }

    func TestQBlockServerCloseReleasesServerAndClientRecords(t *testing.T) {
        cc, _, _ := newQBlockServerTestConn(t, qblock.DefaultManagerConfig())
        cc.ProcessReceivedMessageWithHandler(q1Request(t, cc, message.Token{1}, 0, true, 32, []byte("tag-a"), bytes.Repeat([]byte("a"), 16)), cc.handleReq)
        cc.qblockClient.close()

        requireQBlockServerEmpty(t, cc.qblockClient.qblockServer)
        requireQBlockClientEmpty(t, cc)
        require.Zero(t, qblockClientManagerTokenCountForTest(cc.qblockClient.manager))
        require.Zero(t, qblockClientManagerRetainedBytesForTest(cc.qblockClient.manager))
    }

- [ ] Step 2: Run the focused lifecycle tests to verify they fail

Run: GOCACHE=/tmp/go-coap-qblock-cache go test ./udp/client -run 'QBlockServerExpiry|QBlockServerClose' -count=1

Expected: FAIL because current tick and close paths know only client-role maps.

- [ ] Step 3: Implement idempotent lifecycle cleanup

For every server-role transfer, remove adapter maps only after its matching
manager Release output or explicit cancellation. For Q2 Complete, keep the
sender record and its manager transfer. For response write failure, call
manager.Cancel(sender.id, err), remove transient token/MID routes, and keep
the completed request record until expiry. For a receiver error, remove active
maps and keep a failed completed record only after application dispatch began.

    func (s *qblockServer) closeLocked() []qblock.Output {
        var outputs []qblock.Output
        for _, tr := range s.activeByOperation {
            outputs = append(outputs, s.owner.manager.Cancel(tr.id, qblock.ErrClosed)...)
        }
        for _, sender := range s.senderByOperation {
            outputs = append(outputs, s.owner.manager.Cancel(sender.id, qblock.ErrClosed)...)
        }
        return outputs
    }

After executing close outputs, clear active, sender, and completed maps.
Repeated close and repeated tick calls must be harmless.

- [ ] Step 4: Run focused and package regression tests

Run: GOCACHE=/tmp/go-coap-qblock-cache go test ./udp/client -run 'QBlockServer|QBlockClient|DisabledQBlock|TestDo|TestConn' -count=1

Expected: PASS.

Run: GOCACHE=/tmp/go-coap-qblock-cache go test -race ./udp/client ./net/qblock -count=1

Expected: PASS.

Run: git diff --check

Expected: no output.

- [ ] Step 5: Record scope and validation

Change the design status to implemented only after Step 4 passes. Add the
focused command results, race result, and the deferred boundaries of
private-only construction, no udp/server wiring, no public option, no DTLS, and
no scheduler/pacing work to the results document.

- [ ] Step 6: Commit lifecycle completion

    git add udp/client/qblock_client.go udp/client/qblock_server.go udp/client/qblock_server_test.go docs/superpowers/specs/2026-09-21-rfc9177-private-bidirectional-udp-adapter-design.md docs/superpowers/plans/2026-09-15-rfc9177-results.md
    git commit -m "feat(qblock): complete private server adapter lifecycle"
