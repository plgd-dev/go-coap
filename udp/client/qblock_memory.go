package client

import (
	"io"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
)

func cloneQBlockBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	result := make([]byte, len(value))
	copy(result, value)
	return result
}

// readQBlockBody copies at most the limit and one overflow-detection byte.
// Failed reads never return a partial body for publication or transmission.
func readQBlockBody(reader io.Reader, limit uint32) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if uint64(len(payload)) > uint64(limit) {
		return nil, qblock.ErrLimitExceeded
	}
	return payload, nil
}

func qblockClientSnapshotCapacity(options message.Options, token message.Token, tag []byte, maxPayloads uint32) (uint64, error) {
	capacity, err := qblockControlCapacity(options, maxPayloads)
	if err != nil {
		return 0, err
	}
	snapshot, err := qblockOptionBytes(options)
	if err != nil {
		return 0, err
	}
	for _, part := range []uint64{snapshot, uint64(len(token)), uint64(len(tag))} {
		var ok bool
		capacity, ok = qblockCheckedAdd(capacity, part)
		if !ok {
			return 0, qblock.ErrLimitExceeded
		}
	}
	return capacity, nil
}
