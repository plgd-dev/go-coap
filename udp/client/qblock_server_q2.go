package client

import (
	"bytes"
	"errors"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
)

// serverQ2Control preserves the singleton initial-GET contract.
func serverQ2Control(msg *pool.Message) (qblock.OperationKey, qblock.Block, error) {
	op, blocks, err := serverQ2Controls(msg, 1)
	if err != nil {
		return "", qblock.Block{}, err
	}
	return op, blocks[0], nil
}

func serverQ2Controls(msg *pool.Message, limit uint32) (qblock.OperationKey, []qblock.Block, error) {
	if msg.Type() != message.NonConfirmable || (msg.Code() != codes.GET && msg.Code() != codes.POST && msg.Code() != codes.PUT) || msg.HasOption(message.QBlock1) || msg.HasOption(message.Block1) || msg.HasOption(message.Block2) {
		return "", nil, errors.New("invalid q-block2 control request")
	}
	count := qblockOptionCount(msg, message.QBlock2)
	if err := qblock.ValidateOptions(msg.Options(), true); err != nil || count == 0 || uint64(count) > uint64(limit) || msg.Body() != nil {
		return "", nil, errors.New("invalid q-block2 control options")
	}
	blocks := make([]qblock.Block, 0, count)
	for _, opt := range msg.Options() {
		if opt.ID != message.QBlock2 {
			continue
		}
		value, _, err := message.DecodeUint32(opt.Value)
		if err != nil {
			return "", nil, err
		}
		block, err := qblock.DecodeBlock(value)
		if err != nil {
			return "", nil, err
		}
		if len(blocks) != 0 && (block.Number <= blocks[len(blocks)-1].Number || block.SZX != blocks[0].SZX) {
			return "", nil, errors.New("q-block2 selectors must increase with fixed size")
		}
		blocks = append(blocks, block)
	}
	opts, err := canonicalServerRequestOptions(msg.Options())
	if err != nil {
		return "", nil, err
	}
	operation, err := serverRequestKey(msg.Code(), opts)
	return operation, blocks, err
}

// A singleton set-boundary M1 is an acknowledgement. In a repair selection,
// M1 requests the tail of its set (the whole body for NUM0); overlapping
// selections yield each block once.
func serverQ2Selection(blocks []qblock.Block, meta qblock.Metadata, maxPayloads uint32) (qblock.Control, error) {
	if len(blocks) == 1 && blocks[0].More && blocks[0].Number%maxPayloads == 0 {
		if blocks[0].Number == 0 {
			return qblock.Control{}, errors.New("duplicate initial q-block2 request")
		}
		through := blocks[0].Number - 1
		return qblock.Control{Continue: &through}, nil
	}
	size := uint64(16) << meta.SZX
	count := max(uint64(1), (uint64(meta.Size)+size-1)/size)
	numbers := make([]uint32, 0, min(uint64(maxPayloads), count))
	for _, block := range blocks {
		if block.SZX != meta.SZX || uint64(block.Number) >= count {
			return qblock.Control{}, qblock.ErrInvalidRepair
		}
		end := uint64(block.Number) + 1
		if block.More {
			if block.Number == 0 {
				end = count
			} else {
				end = min((uint64(block.Number)/uint64(maxPayloads)+1)*uint64(maxPayloads), count)
			}
		}
		start := uint64(block.Number)
		if len(numbers) != 0 {
			start = max(start, uint64(numbers[len(numbers)-1])+1)
		}
		for n := start; n < end; n++ {
			if uint64(len(numbers)) >= uint64(maxPayloads) {
				return qblock.Control{}, qblock.ErrInvalidRepair
			}
			numbers = append(numbers, uint32(n))
		}
	}
	return qblock.Control{Missing: numbers}, nil
}

func (s *qblockServer) handleQ2Control(msg *pool.Message) ([]qblock.Output, bool) {
	op, blocks, err := serverQ2Controls(msg, s.client.managerConfig.Transfer.MaxPayloads)
	if err != nil {
		return nil, false
	}
	s.client.mu.Lock()
	if s.closed {
		s.client.mu.Unlock()
		return nil, false
	}
	record := s.records[op]
	if record == nil || !record.executing || record.terminal || s.byID[record.id] == nil {
		s.client.mu.Unlock()
		return nil, false
	}
	if blocks[0].SZX != record.metadata.SZX {
		s.client.mu.Unlock()
		return nil, false
	}
	control, err := serverQ2Selection(blocks, record.metadata, s.client.managerConfig.Transfer.MaxPayloads)
	if err != nil {
		s.client.mu.Unlock()
		return nil, false
	}
	token := bytes.Clone(msg.Token())
	_, owned := record.tokens[string(token)]
	if !owned {
		if err := s.client.cc.claimToken(token, tokenOwnerQBlock); err != nil {
			s.client.mu.Unlock()
			return nil, false
		}
	}
	control.Token = token
	outputs, err := s.client.manager.ControlWithToken(record.id, control, s.client.now())
	if err != nil {
		if !owned {
			s.client.cc.releaseToken(token, tokenOwnerQBlock)
		}
		s.client.mu.Unlock()
		return nil, false
	}
	progressed := false
	for _, output := range outputs {
		if output.TransferID == record.id && output.Action.Kind == qblock.SendBlock {
			progressed = true
			break
		}
	}
	s.acceptPacingFeedbackLocked(record, msg, progressed)
	if len(outputs) == 0 && control.Continue != nil && !owned {
		s.client.cc.releaseToken(token, tokenOwnerQBlock)
	} else {
		record.tokens[string(token)] = token
		record.replyToken = token
		record.queueReplyTokens(outputs, token)
	}
	s.client.mu.Unlock()
	return outputs, true
}
