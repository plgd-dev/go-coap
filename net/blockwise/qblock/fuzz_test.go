package qblock

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
)

func packQBlock2FuzzOptions(options ...[]byte) []byte {
	packed := make([]byte, 0)
	for _, option := range options {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(option)))
		packed = append(packed, length[:]...)
		packed = append(packed, option...)
	}
	return packed
}

func unpackQBlock2FuzzOptions(packed []byte) ([6][]byte, bool) {
	var options [6][]byte
	offset := 0
	for i := range options {
		if len(packed)-offset < 4 {
			return options, false
		}
		length := int(binary.BigEndian.Uint32(packed[offset : offset+4]))
		offset += 4
		if length > len(packed)-offset {
			return options, false
		}
		options[i] = packed[offset : offset+length]
		offset += length
	}
	return options, offset == len(packed)
}

func FuzzParseQBlock1(f *testing.F) {
	f.Add([]byte{0x18}, []byte{1}, []byte{0x10}, []byte{8}, uint8(codes.PUT))
	f.Add([]byte{0xff, 0xff, 0xff, 0xff}, []byte{}, []byte{}, []byte{}, uint8(codes.GET))
	f.Fuzz(func(t *testing.T, qblock, tag, size, qblock2 []byte, code uint8) {
		request := pool.NewMessage(context.Background())
		request.SetCode(codes.Code(code))
		request.AddOptionBytes(message.QBlock1, qblock)
		request.AddOptionBytes(message.RequestTag, tag)
		request.AddOptionBytes(message.Size1, size)
		request.AddOptionBytes(message.QBlock2, qblock2)

		parameters, err := parseQBlock1(request)
		if err != nil {
			return
		}
		if parameters.num < 0 || parameters.num > maxQBlockNumber || parameters.szx > qBlockMaxSZX {
			t.Fatalf("parser accepted invalid Q-Block1 parameters: %+v", parameters)
		}
	})
}

func FuzzParseQBlock2(f *testing.F) {
	f.Add(packQBlock2FuzzOptions([]byte{0x18}, nil, nil, nil, nil, nil), uint8(codes.GET))
	f.Add(packQBlock2FuzzOptions([]byte{0xff, 0xff, 0xff, 0xff}, []byte{0}, nil, nil, nil, nil), uint8(codes.PUT))
	f.Fuzz(func(t *testing.T, packed []byte, code uint8) {
		options, ok := unpackQBlock2FuzzOptions(packed)
		if !ok {
			return
		}
		request := pool.NewMessage(context.Background())
		request.SetCode(codes.Code(code))
		request.AddOptionBytes(message.QBlock2, options[0])
		request.AddOptionBytes(message.QBlock2, options[1])
		request.AddOptionBytes(message.QBlock1, options[2])
		request.AddOptionBytes(message.Block1, options[3])
		request.AddOptionBytes(message.Block2, options[4])
		request.AddOptionBytes(message.Observe, options[5])
		engine := New(newTestClient(), nil)

		szx, num, _, err := engine.parseQBlock2(request)
		if err != nil {
			return
		}
		if num < 0 || num > maxQBlockNumber || szx > qBlockMaxSZX {
			t.Fatalf("parser accepted invalid Q-Block2 parameters: szx=%d num=%d", szx, num)
		}
	})
}

func FuzzQBlock1MissingReport(f *testing.F) {
	f.Add([]byte{0, 1, 0x18, 24}, int64(32))
	f.Add([]byte{0x1a, 0, 15, 255, 255}, int64(1<<20))
	f.Add([]byte{0x80}, int64(10))
	f.Fuzz(func(t *testing.T, report []byte, blockCount int64) {
		missing, err := parseQBlock1MissingReport(report, blockCount)
		if err != nil {
			return
		}
		if len(missing) > q1MaxTrackedBlocks {
			t.Fatalf("accepted %d missing blocks, limit is %d", len(missing), q1MaxTrackedBlocks)
		}
		for i, num := range missing {
			if num < 0 || num >= blockCount || (i > 0 && num <= missing[i-1]) {
				t.Fatalf("accepted invalid missing-block report: %v", missing)
			}
		}
	})
}
