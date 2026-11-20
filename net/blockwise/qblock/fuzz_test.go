package qblock

import (
	"context"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
)

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
	f.Add([]byte{0x18}, []byte{}, []byte{}, []byte{}, []byte{}, []byte{}, uint8(codes.GET))
	f.Add([]byte{0xff, 0xff, 0xff, 0xff}, []byte{0}, []byte{}, []byte{}, []byte{}, []byte{}, uint8(codes.PUT))
	f.Fuzz(func(t *testing.T, qblock, qblock2, qblock1, block1, block2, observe []byte, code uint8) {
		request := pool.NewMessage(context.Background())
		request.SetCode(codes.Code(code))
		request.AddOptionBytes(message.QBlock2, qblock)
		request.AddOptionBytes(message.QBlock2, qblock2)
		request.AddOptionBytes(message.QBlock1, qblock1)
		request.AddOptionBytes(message.Block1, block1)
		request.AddOptionBytes(message.Block2, block2)
		request.AddOptionBytes(message.Observe, observe)
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
