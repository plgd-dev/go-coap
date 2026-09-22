package client

import (
	"bytes"
	"errors"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
)

func serverQ2Control(msg *pool.Message) (qblock.OperationKey, qblock.Block, error) {
	if msg.Type() != message.NonConfirmable || (msg.Code() != codes.POST && msg.Code() != codes.PUT) || msg.HasOption(message.QBlock1) || msg.HasOption(message.Block1) || msg.HasOption(message.Block2) {
		return "", qblock.Block{}, errors.New("invalid q-block2 control request")
	}
	if err := qblock.ValidateOptions(msg.Options(), true); err != nil || qblockOptionCount(msg, message.QBlock2) != 1 || msg.Body() != nil {
		return "", qblock.Block{}, errors.New("invalid q-block2 control options")
	}
	value, err := msg.GetOptionUint32(message.QBlock2)
	if err != nil {
		return "", qblock.Block{}, err
	}
	block, err := qblock.DecodeBlock(value)
	if err != nil {
		return "", qblock.Block{}, err
	}
	opts, err := canonicalServerRequestOptions(msg.Options())
	if err != nil {
		return "", qblock.Block{}, err
	}
	operation, err := serverRequestKey(msg.Code(), opts)
	return operation, block, err
}

func (s *qblockServer) handleQ2Control(msg *pool.Message) bool {
	op, block, err := serverQ2Control(msg)
	if err != nil {
		return true
	}
	s.client.mu.Lock()
	record := s.records[op]
	if record == nil || !record.executing || s.byID[record.id] == nil {
		s.client.mu.Unlock()
		return true
	}
	if block.SZX != record.metadata.SZX {
		s.client.mu.Unlock()
		return true
	}
	token := bytes.Clone(msg.Token())
	_, owned := record.tokens[string(token)]
	if !owned {
		if err := s.client.cc.claimToken(token, tokenOwnerQBlock); err != nil {
			s.client.mu.Unlock()
			return true
		}
	}
	control := qblock.Control{Token: token}
	if block.More {
		if block.Number == 0 {
			if !owned {
				s.client.cc.releaseToken(token, tokenOwnerQBlock)
			}
			s.client.mu.Unlock()
			return true
		}
		through := block.Number - 1
		control.Continue = &through
	} else {
		control.Missing = []uint32{block.Number}
	}
	outputs, err := s.client.manager.ControlWithToken(record.id, control, s.client.now())
	if err != nil {
		if !owned {
			s.client.cc.releaseToken(token, tokenOwnerQBlock)
		}
		s.client.mu.Unlock()
		return true
	}
	if len(outputs) == 0 && control.Continue != nil && !owned {
		s.client.cc.releaseToken(token, tokenOwnerQBlock)
	} else {
		record.tokens[string(token)] = token
		record.replyToken = token
		record.queueReplyTokens(outputs, token)
	}
	s.client.mu.Unlock()
	s.client.drive(outputs)
	return true
}
