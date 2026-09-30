package client

import (
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
)

// selectBodySZX fixes offsets before sender registration. The template has
// all stable wire options; reserve the longest token future controls can use
// and the largest block number this body can emit, including repair packets.
func (c *qblockClient) selectBodySZX(template *pool.Message, option message.OptionID, size uint32, maximum blockwise.SZX) (blockwise.SZX, error) {
	if maximum > blockwise.SZX1024 {
		return 0, blockwise.ErrInvalidSZX
	}
	for candidate := int(maximum); candidate >= 0; candidate-- {
		szx := blockwise.SZX(candidate)
		blockSize := uint64(16) << szx
		count := max(uint64(1), (uint64(size)+blockSize-1)/blockSize)
		if count > 1<<20 {
			continue
		}
		value, err := qblock.EncodeBlock(qblock.Block{Number: uint32(count - 1), More: true, SZX: szx})
		if err != nil {
			return 0, err
		}
		template.SetOptionUint32(option, value)
		wireSize, err := coder.DefaultCoder.Size(message.Message{
			Token: make(message.Token, message.MaxTokenSize), Options: template.Options(),
			Payload: make([]byte, int(min(uint64(size), blockSize))),
		})
		if err != nil {
			return 0, err
		}
		if uint64(wireSize) <= uint64(c.datagramLimit) {
			return szx, nil
		}
	}
	return 0, qblock.ErrLimitExceeded
}

func (c *qblockClient) writeQBlockMessage(msg *pool.Message) error {
	if err := c.writeContext.Err(); err != nil {
		return err
	}
	if err := msg.Context().Err(); err != nil {
		return err
	}
	size, err := qblockDatagramSize(msg)
	if err != nil {
		return err
	}
	if size > uint64(c.datagramLimit) {
		return qblock.ErrLimitExceeded
	}
	return c.cc.session.WriteMessage(msg)
}
