package client

import (
	"io"

	"github.com/plgd-dev/go-coap/v3/net/qblock"
)

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
