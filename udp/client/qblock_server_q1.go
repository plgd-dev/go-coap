package client

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
)

func (s *qblockServer) handleQ1(msg *pool.Message) bool {
	// Q-Block requests owned by the enabled private role are always consumed,
	// including malformed input, so the generic disabled-Q path stays silent.
	if msg.Type() != message.NonConfirmable || (msg.Code() != codes.POST && msg.Code() != codes.PUT) {
		return true
	}
	fragment, options, err := serverQ1Fragment(msg)
	if err != nil {
		return true
	}
	operation, err := serverRequestKey(msg.Code(), options)
	if err != nil {
		return true
	}
	fragment.Operation = operation

	s.client.mu.Lock()
	if record := s.records[operation]; record != nil {
		outputs := s.receiveLocked(record, fragment)
		s.client.mu.Unlock()
		s.client.drive(outputs)
		return true
	}
	if uint64(len(s.records)) >= uint64(s.config.MaxRecords) {
		s.client.mu.Unlock()
		return true
	}
	charge := uint64(len(operation)) + optionsSize(options)
	if charge > s.config.MaxMetadataBytes-s.metadata {
		s.client.mu.Unlock()
		return true
	}
	if err := s.client.cc.claimToken(fragment.Token, tokenOwnerQBlock); err != nil {
		s.client.mu.Unlock()
		return true
	}
	outputs, err := s.client.manager.StartReceiver(fragment, s.client.now())
	if err != nil {
		s.client.cc.releaseToken(fragment.Token, tokenOwnerQBlock)
		s.client.mu.Unlock()
		return true
	}
	id, ok := s.client.manager.TransferID(operation)
	if !ok {
		// StartReceiver must retain Q1 after any Deliver. Treat a violated
		// manager invariant as a rejected admission without publishing a record.
		s.client.cc.releaseToken(fragment.Token, tokenOwnerQBlock)
		s.client.mu.Unlock()
		return true
	}
	record := &qblockServerRecord{
		id: id, operation: operation, metadata: fragment.Metadata, options: options,
		tokens: map[string]message.Token{string(fragment.Token): bytes.Clone(fragment.Token)}, replyToken: bytes.Clone(fragment.Token), charged: charge,
	}
	s.records[operation] = record
	s.byID[id] = record
	s.metadata += charge
	s.client.mu.Unlock()
	s.client.drive(outputs)
	return true
}

func (s *qblockServer) receiveLocked(record *qblockServerRecord, fragment qblock.Fragment) []qblock.Output {
	if fragment.Metadata.Size != record.metadata.Size || fragment.Metadata.SZX != record.metadata.SZX || fragment.Metadata.HasContentFormat != record.metadata.HasContentFormat || fragment.Metadata.ContentFormat != record.metadata.ContentFormat || !bytes.Equal(fragment.Metadata.Identity, record.metadata.Identity) {
		return s.client.manager.Cancel(record.id, errors.New("q-block request metadata changed"))
	}
	key := string(fragment.Token)
	if _, ok := record.tokens[key]; !ok {
		if err := s.client.cc.claimToken(fragment.Token, tokenOwnerQBlock); err != nil {
			return nil
		}
		if err := s.client.manager.BindToken(record.id, fragment.Token); err != nil {
			s.client.cc.releaseToken(fragment.Token, tokenOwnerQBlock)
			return nil
		}
		record.tokens[key] = bytes.Clone(fragment.Token)
	}
	outputs, err := s.client.manager.Receive(fragment, s.client.now())
	if err != nil {
		return s.client.manager.Cancel(record.id, err)
	}
	record.replyToken = bytes.Clone(fragment.Token)
	return outputs
}

func serverQ1Fragment(msg *pool.Message) (qblock.Fragment, message.Options, error) {
	if msg.HasOption(message.QBlock2) || msg.HasOption(message.Block1) || msg.HasOption(message.Block2) {
		return qblock.Fragment{}, nil, errors.New("mixed q-block request options")
	}
	if err := qblock.ValidateOptions(msg.Options(), true); err != nil {
		return qblock.Fragment{}, nil, err
	}
	if qblockOptionCount(msg, message.QBlock1) != 1 || qblockOptionCount(msg, message.Size1) != 1 || qblockOptionCount(msg, message.RequestTag) == 0 {
		return qblock.Fragment{}, nil, errors.New("q-block request requires QBlock1, Size1, and Request-Tag")
	}
	value, err := msg.GetOptionUint32(message.QBlock1)
	if err != nil {
		return qblock.Fragment{}, nil, err
	}
	block, err := qblock.DecodeBlock(value)
	if err != nil {
		return qblock.Fragment{}, nil, err
	}
	size, err := msg.GetOptionUint32(message.Size1)
	if err != nil {
		return qblock.Fragment{}, nil, err
	}
	payload := []byte(nil)
	if body := msg.Body(); body != nil {
		payload, err = io.ReadAll(body)
		if err != nil {
			return qblock.Fragment{}, nil, err
		}
	}
	metadata := qblock.Metadata{Size: size, SZX: block.SZX, Identity: []byte("request")}
	if msg.HasOption(message.ContentFormat) {
		format, err := msg.ContentFormat()
		if err != nil {
			return qblock.Fragment{}, nil, err
		}
		metadata.HasContentFormat = true
		metadata.ContentFormat = format
	}
	body, err := qblock.NewBody(metadata, ^uint32(0))
	if err != nil {
		return qblock.Fragment{}, nil, err
	}
	if _, err = body.Add(metadata, block, payload); err != nil {
		return qblock.Fragment{}, nil, err
	}
	options, err := canonicalServerRequestOptions(msg.Options())
	if err != nil {
		return qblock.Fragment{}, nil, err
	}
	return qblock.Fragment{Token: bytes.Clone(msg.Token()), Kind: qblock.Q1, Metadata: metadata, Block: block, Payload: payload}, options, nil
}

func canonicalServerRequestOptions(opts message.Options) (message.Options, error) {
	copy, err := opts.Clone()
	if err != nil {
		return nil, err
	}
	for _, id := range []message.OptionID{message.QBlock1, message.QBlock2, message.Size1, message.Size2, message.Block1, message.Block2, message.ETag} {
		copy = copy.Remove(id)
	}
	return copy, nil
}

func serverRequestKey(code codes.Code, opts message.Options) (qblock.OperationKey, error) {
	encoded := make([]byte, 0, int(optionsSize(opts))+2)
	encoded = append(encoded, byte(code))
	for _, option := range opts {
		if len(option.Value) > 0xffff {
			return "", errors.New("request option is too large")
		}
		var header [4]byte
		binary.BigEndian.PutUint16(header[:2], uint16(option.ID))
		binary.BigEndian.PutUint16(header[2:], uint16(len(option.Value)))
		encoded = append(encoded, header[:]...)
		encoded = append(encoded, option.Value...)
	}
	return qblock.NewOperationKey([]byte("server-q1"), encoded)
}

func optionsSize(opts message.Options) uint64 {
	var size uint64
	for _, option := range opts {
		size += uint64(4 + len(option.Value))
	}
	return size
}
