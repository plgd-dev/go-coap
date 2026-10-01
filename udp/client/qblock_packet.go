package client

import (
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
)

// selectGETSZX reserves the standard representation metadata and the largest
// block number allowed by the body cap. Extra peer options are checked on
// receipt; they cannot be predicted by an initial GET.
func (c *qblockClient) selectGETSZX() (blockwise.SZX, error) {
	template := c.cc.AcquireMessage(c.writeContext)
	defer c.cc.ReleaseMessage(template)
	template.SetOptionBytes(message.ETag, make([]byte, 8))
	template.SetOptionUint32(message.ContentFormat, 65535)
	template.SetOptionUint32(message.Size2, c.managerConfig.Transfer.MaxBodySize)
	return c.selectSZX(template, message.QBlock2, c.managerConfig.Transfer.MaxBodySize, c.cc.blockwiseSZX, true)
}

func qblockGETSize(req *pool.Message) (uint64, error) {
	size, err := coder.DefaultCoder.Size(message.Message{Token: req.Token(), Options: req.Options()})
	return uint64(size), err
}

// qblockIncomingSize checks the decoded datagram without allocating a payload
// copy. BodySize preserves the seek position for later fragment validation.
func qblockIncomingSize(msg *pool.Message) (uint64, error) {
	overhead, err := qblockGETSize(msg)
	if err != nil {
		return 0, err
	}
	bodySize, err := msg.BodySize()
	if err != nil {
		return 0, err
	}
	if bodySize < 0 {
		return 0, qblock.ErrLimitExceeded
	}
	if bodySize == 0 {
		return overhead, nil
	}
	size, ok := qblockCheckedAdd(overhead, uint64(bodySize)+1)
	if !ok {
		return 0, qblock.ErrLimitExceeded
	}
	return size, nil
}

// selectBodySZX fixes offsets before sender registration. The template has
// all stable wire options; reserve the longest token future controls can use
// and the largest block number this body can emit, including repair packets.
func (c *qblockClient) selectBodySZX(template *pool.Message, option message.OptionID, size uint32, maximum blockwise.SZX) (blockwise.SZX, error) {
	return c.selectSZX(template, option, size, maximum, false)
}

func (c *qblockClient) selectSZX(template *pool.Message, option message.OptionID, size uint32, maximum blockwise.SZX, synthetic bool) (blockwise.SZX, error) {
	if maximum > blockwise.SZX1024 {
		return 0, blockwise.ErrInvalidSZX
	}
	for candidate := int(maximum); candidate >= 0; candidate-- {
		szx := blockwise.SZX(candidate)
		blockSize := uint64(16) << szx
		count := max(uint64(1), (uint64(size)+blockSize-1)/blockSize)
		if count > 1<<20 {
			if !synthetic {
				continue
			}
			// A GET's cap is not its representation size. Reserve the largest
			// legal NUM without requiring every resource to reach the cap.
			count = 1 << 20
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

func (c *qblockClient) oversizedIncomingQ(msg *pool.Message, wireSize uint64) bool {
	if wireSize <= uint64(c.datagramLimit) || (!msg.HasOption(message.QBlock1) && !msg.HasOption(message.QBlock2)) {
		return false
	}
	return c.ownsIncomingQ(msg.Code(), msg.Token())
}

func (c *qblockClient) ownsIncomingQ(code codes.Code, token message.Token) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if code >= codes.GET && code < 32 {
		return c.server != nil
	}
	if code < 64 {
		return false
	}
	if c.transferByToken[string(token)] != nil {
		return true
	}
	exchange := c.exchangesByOriginalToken[string(token)]
	return exchange != nil && exchange.requestCode == codes.GET
}

// oversizedRawQ recognizes private Q traffic above the session cap without
// allocating decoded options or copying payload bytes. Malformed framing keeps
// the existing session-limit error; option value semantics are checked later.
func (c *qblockClient) oversizedRawQ(data []byte) bool {
	if len(data) < 4 || data[0]>>6 != 1 {
		return false
	}
	tokenLen := int(data[0] & 15)
	if tokenLen > message.MaxTokenSize || len(data) < 4+tokenLen {
		return false
	}
	code, token := codes.Code(data[1]), message.Token(data[4:4+tokenLen])
	options := data[4+tokenLen:]
	var number uint64
	hasQ := false
	for len(options) > 0 && options[0] != 0xff {
		header := options[0]
		options = options[1:]
		delta, rest, ok := qblockRawOptionField(header>>4, options)
		if !ok {
			return false
		}
		length, rest, ok := qblockRawOptionField(header&15, rest)
		if !ok || length > uint64(len(rest)) {
			return false
		}
		number += delta
		if number > 65535 {
			return false
		}
		hasQ = hasQ || number == uint64(message.QBlock1) || number == uint64(message.QBlock2)
		options = rest[length:]
	}
	return hasQ && c.ownsIncomingQ(code, token)
}

func qblockRawOptionField(n byte, data []byte) (uint64, []byte, bool) {
	switch n {
	case 13:
		if len(data) < 1 {
			return 0, nil, false
		}
		return uint64(data[0]) + 13, data[1:], true
	case 14:
		if len(data) < 2 {
			return 0, nil, false
		}
		return uint64(data[0])<<8 + uint64(data[1]) + 269, data[2:], true
	case 15:
		return 0, nil, false
	default:
		return uint64(n), data, true
	}
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
