package client

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
)

type qblockServerConfig struct {
	Retention        time.Duration
	MaxRecords       uint32
	MaxMetadataBytes uint64
}

type qblockServer struct {
	client   *qblockClient
	handler  HandlerFunc
	config   qblockServerConfig
	records  map[qblock.OperationKey]*qblockServerRecord
	byID     map[qblock.TransferID]*qblockServerRecord
	metadata uint64
}

type qblockServerRecord struct {
	id              qblock.TransferID
	operation       qblock.OperationKey
	metadata        qblock.Metadata
	options         message.Options
	tokens          map[string]message.Token
	replyToken      message.Token
	payload         []byte
	ready           bool
	executing       bool
	code            codes.Code
	responseCode    codes.Code
	responseOptions message.Options
	pendingReplies  map[qblockServerReplyKey][]message.Token
	charged         uint64
}

// qblockServerReplyKey identifies a concrete Q2 payload action.  A control
// can be accepted before its output gets the serialized action turn; retain
// its reply token with that action so a later control cannot retoken it.
type qblockServerReplyKey struct {
	number uint32
	more   bool
}

func replyKey(action qblock.Action) qblockServerReplyKey {
	return qblockServerReplyKey{number: action.Block.Number, more: action.Block.More}
}

func (r *qblockServerRecord) queueReplyTokens(outputs []qblock.Output, token message.Token) {
	for _, output := range outputs {
		if output.Action.Kind != qblock.SendBlock {
			continue
		}
		if r.pendingReplies == nil {
			r.pendingReplies = make(map[qblockServerReplyKey][]message.Token)
		}
		key := replyKey(output.Action)
		r.pendingReplies[key] = append(r.pendingReplies[key], bytes.Clone(token))
	}
}

func (r *qblockServerRecord) takeReplyToken(action qblock.Action) message.Token {
	key := replyKey(action)
	if pending := r.pendingReplies[key]; len(pending) > 0 {
		token := pending[0]
		if len(pending) == 1 {
			delete(r.pendingReplies, key)
		} else {
			r.pendingReplies[key] = pending[1:]
		}
		return bytes.Clone(token)
	}
	return bytes.Clone(r.replyToken)
}

func withQBlockServer(cfg qblockServerConfig) Option {
	return func(opts *ConnOptions) {
		copy := cfg
		opts.qblockServerConfig = &copy
	}
}

func newQBlockServer(client *qblockClient, handler HandlerFunc, cfg qblockServerConfig) *qblockServer {
	if client == nil || client.manager == nil {
		return nil
	}
	if cfg.Retention == 0 {
		cfg.Retention = client.managerConfigLifetime()
	}
	if cfg.MaxRecords == 0 {
		cfg.MaxRecords = client.managerConfigTransfers()
	}
	if cfg.MaxMetadataBytes == 0 {
		cfg.MaxMetadataBytes = 64 << 10
	}
	if cfg.Retention < 0 || cfg.Retention < client.managerConfigLifetime() {
		return nil
	}
	return &qblockServer{
		client: client, handler: handler, config: cfg,
		records: make(map[qblock.OperationKey]*qblockServerRecord),
		byID:    make(map[qblock.TransferID]*qblockServerRecord),
	}
}

func (c *qblockClient) managerConfigLifetime() time.Duration {
	return c.managerConfig.Transfer.Lifetime
}
func (c *qblockClient) managerConfigTransfers() uint32 { return c.managerConfig.MaxTransfers }

func (s *qblockServer) ownsOutput(output qblock.Output) bool {
	s.client.mu.Lock()
	defer s.client.mu.Unlock()
	return s.byID[output.TransferID] != nil
}

func (c *qblockClient) handleServerRequest(msg *pool.Message) bool {
	if c.server == nil || msg.Code() < 1 || msg.Code() >= 32 {
		return false
	}
	if msg.HasOption(message.QBlock2) {
		return c.server.handleQ2Control(msg)
	}
	if !msg.HasOption(message.QBlock1) {
		return false
	}
	return c.server.handleQ1(msg)
}

func (c *qblockClient) executeServerOutput(output qblock.Output) []func() {
	c.mu.Lock()
	if c.server == nil {
		c.mu.Unlock()
		return nil
	}
	record := c.server.byID[output.TransferID]
	if record == nil {
		c.mu.Unlock()
		return nil
	}
	switch output.Action.Kind {
	case qblock.Deliver:
		record.payload = bytes.Clone(output.Action.Payload)
		record.ready = true
		if record.executing || c.server.handler == nil {
			c.mu.Unlock()
			return nil
		}
		record.executing = true
		payload := bytes.Clone(record.payload)
		options, _ := record.options.Clone()
		code := record.code
		operation := record.operation
		c.mu.Unlock()
		return []func(){func() {
			req := c.cc.AcquireMessage(c.cc.Context())
			resp := c.cc.AcquireMessage(c.cc.Context())
			defer c.cc.ReleaseMessage(req)
			defer c.cc.ReleaseMessage(resp)
			req.SetCode(code)
			req.ResetOptionsTo(options)
			req.SetBody(bytes.NewReader(payload))
			writer := responsewriter.New(resp, c.cc, options...)
			c.server.handler(writer, req)
			body := []byte(nil)
			if reader := writer.Message().Body(); reader != nil {
				body, _ = io.ReadAll(reader)
			}
			responseOptions, _ := writer.Message().Options().Clone()
			c.server.finishHandler(operation, writer.Message().IsModified(), writer.Message().Code(), responseOptions, body)
		}}
	case qblock.Release:
		if record.executing {
			delete(c.server.byID, record.id)
			for key, token := range record.tokens {
				c.cc.releaseToken(token, tokenOwnerQBlock)
				delete(record.tokens, key)
			}
			c.mu.Unlock()
			return nil
		}
		c.server.releaseLocked(record)
		c.mu.Unlock()
		return nil
	case qblock.SendContinue, qblock.RequestMissing:
		token := bytes.Clone(record.replyToken)
		szx := record.metadata.SZX
		c.mu.Unlock()
		if err := c.writeServerQ1Control(token, szx, output.Action); err != nil {
			c.mu.Lock()
			outputs := c.manager.Cancel(output.TransferID, err)
			c.mu.Unlock()
			return c.executeOrdered(outputs)
		}
		return nil
	case qblock.SendBlock:
		token := record.takeReplyToken(output.Action)
		code := record.responseCode
		options, _ := record.responseOptions.Clone()
		szx := record.metadata.SZX
		size := record.metadata.Size
		etag := bytes.Clone(record.metadata.Identity)
		c.mu.Unlock()
		if err := c.writeServerQ2Block(token, code, options, szx, size, etag, output.Action); err != nil {
			c.mu.Lock()
			outputs := c.manager.Cancel(output.TransferID, err)
			c.mu.Unlock()
			return c.executeOrdered(outputs)
		}
		return nil
	}
	c.mu.Unlock()
	return nil
}

func (s *qblockServer) finishHandler(operation qblock.OperationKey, modified bool, code codes.Code, options message.Options, payload []byte) {
	c := s.client
	c.mu.Lock()
	record := s.records[operation]
	if record == nil || !record.executing {
		c.mu.Unlock()
		return
	}
	q1id := record.id
	_ = c.manager.Cancel(q1id, nil)
	delete(s.byID, q1id)
	if !modified {
		c.mu.Unlock()
		return
	}
	etag := sha256.Sum256(append([]byte{byte(code)}, payload...))
	meta := qblock.Metadata{Size: uint32(len(payload)), SZX: record.metadata.SZX, Identity: etag[:8], HasContentFormat: true, ContentFormat: message.TextPlain}
	q2op, err := qblock.NewOperationKey([]byte("server-q2"), []byte(operation), meta.Identity)
	if err == nil {
		var outputs []qblock.Output
		outputs, err = c.manager.StartSender(q2op, record.replyToken, qblock.Q2, meta, payload, c.now(), c.jitter())
		if err == nil {
			id, ok := c.manager.TransferID(q2op)
			if !ok {
				c.mu.Unlock()
				return
			}
			record.id, record.metadata, record.responseCode, record.responseOptions = id, meta, code, options
			s.byID[record.id] = record
			c.mu.Unlock()
			c.drive(outputs)
			return
		}
	}
	// Keep a bounded duplicate record even when no representation can be sent.
	record.executing = false
	c.mu.Unlock()
}

func (c *qblockClient) writeServerQ2Block(token message.Token, code codes.Code, options message.Options, szx blockwise.SZX, size uint32, etag []byte, action qblock.Action) error {
	msg := c.cc.AcquireMessage(c.cc.Context())
	defer c.cc.ReleaseMessage(msg)
	msg.SetType(message.NonConfirmable)
	msg.SetToken(token)
	msg.SetMessageID(c.cc.GetMessageID())
	msg.SetCode(code)
	msg.ResetOptionsTo(options)
	value, err := qblock.EncodeBlock(qblock.Block{Number: action.Block.Number, More: action.Block.More, SZX: szx})
	if err != nil {
		return err
	}
	msg.SetOptionUint32(message.QBlock2, value)
	msg.SetOptionUint32(message.Size2, size)
	msg.SetOptionBytes(message.ETag, etag)
	msg.SetBody(bytes.NewReader(action.Payload))
	return c.cc.session.WriteMessage(msg)
}

func (c *qblockClient) writeServerQ1Control(token message.Token, szx blockwise.SZX, action qblock.Action) error {
	if len(token) == 0 {
		return qblock.ErrUnknownTransfer
	}
	msg := c.cc.AcquireMessage(c.cc.Context())
	defer c.cc.ReleaseMessage(msg)
	msg.SetType(message.NonConfirmable)
	msg.SetToken(token)
	msg.SetMessageID(c.cc.GetMessageID())
	switch action.Kind {
	case qblock.SendContinue:
		value, err := qblock.EncodeBlock(qblock.Block{Number: action.Through, More: true, SZX: szx})
		if err != nil {
			return err
		}
		msg.SetCode(codes.Continue)
		msg.SetOptionUint32(message.QBlock1, value)
	case qblock.RequestMissing:
		payload, _, err := qblock.EncodeMissing(action.Numbers, int(c.cc.session.MaxMessageSize()))
		if err != nil {
			return fmt.Errorf("encode q-block missing report: %w", err)
		}
		msg.SetCode(codes.RequestEntityIncomplete)
		msg.SetContentFormat(message.AppMissingBlocksCBORSeq)
		msg.SetBody(bytes.NewReader(payload))
	default:
		return errors.New("unsupported server q-block output")
	}
	return c.cc.session.WriteMessage(msg)
}

func (s *qblockServer) releaseLocked(record *qblockServerRecord) {
	delete(s.byID, record.id)
	delete(s.records, record.operation)
	for _, token := range record.tokens {
		s.client.cc.releaseToken(token, tokenOwnerQBlock)
	}
	if s.metadata >= record.charged {
		s.metadata -= record.charged
	}
}

var errQBlockServerConfig = errors.New("invalid private q-block server configuration")
