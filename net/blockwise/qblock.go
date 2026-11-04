package blockwise

// This facade preserves the original Q-Block API while the implementation
// and its transfer state live in the independent qblock package.

import (
	"io"
	"net"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise/qblock"
)

// QBlockwiseMode selects RFC 9177 use for datagram transfers.
type QBlockwiseMode = qblock.QBlockwiseMode

// QBlockwiseConfig configures RFC 9177 congestion and recovery parameters.
type QBlockwiseConfig = qblock.Config

// SetQBlockwiseConfig sets Q parameters before the first transfer.
func (b *BlockWise[C]) SetQBlockwiseConfig(config QBlockwiseConfig) error {
	return b.qblock.SetConfig(config)
}

const (
	QBlockwiseDisabled = qblock.QBlockwiseDisabled
	QBlockwiseAuto     = qblock.QBlockwiseAuto
	QBlockwiseOnly     = qblock.QBlockwiseOnly
)

// QBlockAssembly is a bounded sparse Q-Block body receiver.
type QBlockAssembly = qblock.Assembly

// QBlockSender reads replayable Q-Block payloads.
type QBlockSender = qblock.Sender

// QBlockExchange routes body responses until it is closed.
type QBlockExchange = qblock.Exchange

// QBlockTransport provides datagram sending, routing, and scheduling.
type QBlockTransport = qblock.Transport

// Q-Block errors are shared with the dedicated qblock package.
var (
	ErrInvalidMissingBlocks = qblock.ErrInvalidMissingBlocks
	ErrQBlockInvalidSize    = qblock.ErrQBlockInvalidSize
	ErrQBlockInvalidNUM     = qblock.ErrQBlockInvalidNUM
	ErrQBlockIncomplete     = qblock.ErrQBlockIncomplete
	ErrQBlockConflict       = qblock.ErrQBlockConflict
	ErrQBlockTooLarge       = qblock.ErrQBlockTooLarge
	ErrQBlockScheduleFull   = qblock.ErrQBlockScheduleFull
	ErrQBlockInvalidMode    = qblock.ErrQBlockInvalidMode
	ErrQBlockNeedsBlockwise = qblock.ErrQBlockNeedsBlockwise
	ErrQBlockDisabled       = qblock.ErrQBlockDisabled
	ErrQBlockUnsupported    = qblock.ErrQBlockUnsupported
)

// ValidateQBlockwiseConfig checks Q mode and blockwise support.
func ValidateQBlockwiseConfig(mode QBlockwiseMode, blockwiseEnabled bool) error {
	return qblock.ValidateQBlockwiseConfig(mode, blockwiseEnabled)
}

// EncodeQBlockOption encodes a datagram Q-Block option.
func EncodeQBlockOption(szx SZX, num int64, more bool) (uint32, error) {
	return qblock.EncodeQBlockOption(szx, num, more)
}

// DecodeQBlockOption decodes a datagram Q-Block option.
func DecodeQBlockOption(value uint32) (szx SZX, num int64, more bool, err error) {
	return qblock.DecodeQBlockOption(value)
}

// EncodeMissingBlocks appends an RFC 9177 unsigned CBOR sequence to dst.
func EncodeMissingBlocks(dst []byte, blocks []uint32) ([]byte, error) {
	return qblock.EncodeMissingBlocks(dst, blocks)
}

// DecodeMissingBlocks appends validated, deduplicated NUMs to dst.
func DecodeMissingBlocks(dst []uint32, data []byte) ([]uint32, error) {
	return qblock.DecodeMissingBlocks(dst, data)
}

// NewQBlockAssembly creates a sparse receiver with explicit resource bounds.
func NewQBlockAssembly(szx SZX, bodySize, maxBodySize int64, maxBlocks int) (*QBlockAssembly, error) {
	return qblock.NewAssembly(szx, bodySize, maxBodySize, maxBlocks)
}

// NewQBlockSender creates a replayable body sender.
func NewQBlockSender(body io.ReadSeeker, szx SZX, bodySize int64) (*QBlockSender, error) {
	return qblock.NewSender(body, szx, bodySize)
}

// CloseQBlockTransfers releases retained Q state when the connection closes.
func (b *BlockWise[C]) CloseQBlockTransfers() {
	b.qblock.CloseQBlockTransfers()
}

// CancelQBlockObservation drops a Q registration and its partial outgoing body.
func (b *BlockWise[C]) CancelQBlockObservation(token message.Token, remote net.Addr) {
	b.qblock.CancelQBlockObservation(token, remote)
}

// DoQBlock1 sends a request body using Q-Block1.
func (b *BlockWise[C]) DoQBlock1(r *pool.Message, szx SZX, maxBodySize int64,
	do func(*pool.Message) (*pool.Message, error),
) (*pool.Message, error) {
	return b.qblock.DoQBlock1(r, szx, maxBodySize, do)
}

// DoQBlock2 requests and assembles a Q-Block2 body.
func (b *BlockWise[C]) DoQBlock2(req *pool.Message, szx SZX,
	do func(*pool.Message) (*pool.Message, error),
) (*pool.Message, error) {
	return b.qblock.DoQBlock2(req, szx, do)
}

// ReceiveQBlock2 assembles a response body from an existing transfer route.
func (b *BlockWise[C]) ReceiveQBlock2(req *pool.Message, szx SZX, exchange QBlockExchange, initial *pool.Message) (*pool.Message, error) {
	return b.qblock.ReceiveQBlock2(req, szx, exchange, initial)
}
