package client

import (
	"context"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"math"
)

// Initial GET has no receiver body; its record owns the same bounded handler
// lease as an assembled upload, then becomes an ordinary Q2 sender record.
func (s *qblockServer) handleInitialGET(msg *pool.Message) ([]qblock.Output, bool) {
	if msg.Code() != codes.GET {
		return nil, false
	}
	op, block, err := serverQ2Control(msg)
	if err != nil || block.Number != 0 || !block.More || qblockOptionCount(msg, message.RequestTag) == 0 {
		return nil, false
	}
	c := s.client
	c.mu.Lock()
	defer c.mu.Unlock()
	if s.closed {
		return nil, false
	}
	if s.records[op] != nil {
		return nil, false
	}
	if uint64(s.recordCountLocked()) >= uint64(s.config.MaxRecords) || s.nextGen == math.MaxUint64 {
		return nil, false
	}
	release, err := c.ownedBudget.acquire(c.ownedBudget.serverCost)
	if err != nil {
		return nil, false
	}
	lease := newQBlockOwnedLease(release)
	options, err := canonicalServerRequestOptions(msg.Options())
	if err != nil {
		lease.drop()
		return nil, false
	}
	charge := uint64(len(op)) + optionsSize(options)
	if charge > s.config.MaxMetadataBytes-s.metadata {
		lease.drop()
		return nil, false
	}
	capacity, err := qblockControlCapacity(options, c.managerConfig.Transfer.MaxPayloads)
	if err != nil {
		lease.drop()
		return nil, false
	}
	work, err := c.workQueue.reserve(capacity)
	if err != nil {
		lease.drop()
		return nil, false
	}
	token := cloneQBlockBytes(msg.Token())
	if err := c.cc.claimToken(token, tokenOwnerQBlock); err != nil {
		c.workQueue.release(work)
		lease.drop()
		return nil, false
	}
	s.nextGen++
	ctx, cancel := context.WithCancel(c.writeContext)
	now := c.now()
	record := &qblockServerRecord{ownedLease: lease, workID: work, operation: op, activeOperation: op, responseCeiling: &block, metadata: qblock.Metadata{SZX: block.SZX}, options: options, tokens: map[string]message.Token{string(token): token}, replyToken: token, code: codes.GET, charged: charge, generation: s.nextGen, writeContext: ctx, cancelWrite: cancel, writeExpires: now.Add(c.managerConfig.Transfer.Lifetime)}
	record.captureRequest(msg)
	s.records[op] = record
	s.metadata += charge
	return []qblock.Output{{Operation: op, Action: qblock.Action{Kind: qblock.Deliver}}}, true
}
