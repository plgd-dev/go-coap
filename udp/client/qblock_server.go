package client

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
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
	id         qblock.TransferID
	operation  qblock.OperationKey
	metadata   qblock.Metadata
	options    message.Options
	tokens     map[string]message.Token
	replyToken message.Token
	payload    []byte
	ready      bool
	charged    uint64
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
	if c.server == nil || !msg.HasOption(message.QBlock1) || msg.Code() < 1 || msg.Code() >= 32 {
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
	if record == nil || record.operation != output.Operation {
		c.mu.Unlock()
		return nil
	}
	switch output.Action.Kind {
	case qblock.Deliver:
		record.payload = bytes.Clone(output.Action.Payload)
		record.ready = true
		c.mu.Unlock()
		return nil
	case qblock.Release:
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
	}
	c.mu.Unlock()
	return nil
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
