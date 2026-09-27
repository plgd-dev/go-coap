package client

import (
	"errors"
	"io"

	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
)

// qblockDatagramSize measures the encoded UDP payload without leaving a
// pooled message's body reader at a different position for the later write.
func qblockDatagramSize(msg *pool.Message) (size uint64, err error) {
	if body := msg.Body(); body != nil {
		position, seekErr := body.Seek(0, io.SeekCurrent)
		if seekErr != nil {
			return 0, seekErr
		}
		defer func() {
			if _, restoreErr := body.Seek(position, io.SeekStart); restoreErr != nil {
				size = 0
				err = errors.Join(err, restoreErr)
			}
		}()
	}
	wire, err := msg.MarshalWithEncoder(coder.DefaultCoder)
	if err != nil {
		return 0, err
	}
	return uint64(len(wire)), nil
}
