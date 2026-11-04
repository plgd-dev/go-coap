package qblock

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

func TestQBlockAssemblyOutOfOrderAndRecovery(t *testing.T) {
	body := []byte("abcdefghijklmnopqrstuvwxyz")
	assembly, err := NewAssembly(SZX16, int64(len(body)), 64, 4)
	require.NoError(t, err)

	complete, duplicate, err := assembly.Add(1, false, body[16:])
	require.NoError(t, err)
	require.False(t, complete)
	require.False(t, duplicate)
	require.Equal(t, []uint32{0}, assembly.Missing())

	complete, duplicate, err = assembly.Add(1, false, body[16:])
	require.NoError(t, err)
	require.False(t, complete)
	require.True(t, duplicate)

	complete, duplicate, err = assembly.Add(0, true, body[:16])
	require.NoError(t, err)
	require.True(t, complete)
	require.False(t, duplicate)
	require.Empty(t, assembly.Missing())

	reader, err := assembly.ReadSeeker()
	require.NoError(t, err)
	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, body, got)
}

func TestQBlockAssemblyBoundsAndValidation(t *testing.T) {
	_, err := NewAssembly(SZX16, 65, 64, 8)
	require.ErrorIs(t, err, ErrQBlockTooLarge)
	_, err = NewAssembly(SZXBERT, 1, 64, 8)
	require.ErrorIs(t, err, ErrQBlockInvalidSize)

	assembly, err := NewAssembly(SZX16, 17, 32, 2)
	require.NoError(t, err)
	_, _, err = assembly.Add(1, false, []byte{1})
	require.NoError(t, err)
	_, _, err = assembly.Add(0, true, bytes.Repeat([]byte{1}, 15))
	require.ErrorIs(t, err, ErrQBlockInvalidSize)
	_, _, err = assembly.Add(qBlockMaxNUM+1, false, nil)
	require.ErrorIs(t, err, ErrQBlockInvalidNUM)
	_, err = assembly.ReadSeeker()
	require.ErrorIs(t, err, ErrQBlockIncomplete)
}

func TestQBlockAssemblyEmptyBody(t *testing.T) {
	assembly, err := NewAssembly(SZX16, 0, 16, 1)
	require.NoError(t, err)
	require.False(t, assembly.Complete())
	require.Equal(t, []uint32{0}, assembly.Missing())
	require.Equal(t, []uint32{0}, assembly.MissingSet())
	num, missing := assembly.FirstMissing()
	require.Zero(t, num)
	require.True(t, missing)

	_, _, err = assembly.Add(1, false, nil)
	require.ErrorIs(t, err, ErrQBlockInvalidNUM)
	complete, duplicate, err := assembly.Add(0, false, nil)
	require.NoError(t, err)
	require.True(t, complete)
	require.False(t, duplicate)
	require.Empty(t, assembly.Missing())
	require.Empty(t, assembly.MissingSet())
	_, missing = assembly.FirstMissing()
	require.False(t, missing)
	reader, err := assembly.ReadSeeker()
	require.NoError(t, err)
	body, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Empty(t, body)

	complete, duplicate, err = assembly.Add(0, false, nil)
	require.NoError(t, err)
	require.True(t, complete)
	require.True(t, duplicate)
	require.Empty(t, assembly.Missing())
}

func TestQBlockAssemblyRejectsNUMBeyondBody(t *testing.T) {
	for _, szx := range []SZX{SZX16, SZX1024} {
		for _, size := range []int64{szx.Size(), 2 * szx.Size(), 2*szx.Size() + 1} {
			assembly, err := NewAssembly(szx, size, size, 3)
			require.NoError(t, err)
			count := (size-1)/szx.Size() + 1
			for _, num := range []int64{count, count + 1} {
				_, _, err = assembly.Add(num, false, nil)
				require.ErrorIs(t, err, ErrQBlockInvalidNUM)
				require.Equal(t, int64(-1), assembly.finalBlock)
			}
			for num := int64(0); num < count; num++ {
				payload := make([]byte, min(szx.Size(), size-num*szx.Size()))
				complete, duplicate, err := assembly.Add(num, num < count-1, payload)
				require.NoError(t, err)
				require.Equal(t, num == count-1, complete)
				require.False(t, duplicate)
			}
			require.True(t, assembly.Complete())
			require.Empty(t, assembly.Missing())
		}
	}
}

func TestQBlockAssemblyConflictingDuplicate(t *testing.T) {
	assembly, err := NewAssembly(SZX16, 16, 16, 1)
	require.NoError(t, err)
	_, _, err = assembly.Add(0, false, bytes.Repeat([]byte{1}, 16))
	require.NoError(t, err)
	_, _, err = assembly.Add(0, false, bytes.Repeat([]byte{2}, 16))
	require.NoError(t, err)
	require.True(t, assembly.Complete())
	body, err := assembly.ReadSeeker()
	require.NoError(t, err)
	payload, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Equal(t, bytes.Repeat([]byte{1}, 16), payload)
}

func TestQBlockSenderRandomAccess(t *testing.T) {
	body := bytes.NewReader([]byte("abcdefghijklmnopqrstuvwxyz"))
	sender, err := NewSender(body, SZX16, 26)
	require.NoError(t, err)

	payload, more, err := sender.Block(1)
	require.NoError(t, err)
	require.False(t, more)
	require.Equal(t, []byte("qrstuvwxyz"), payload)
	value, err := sender.BlockOption(1)
	require.NoError(t, err)
	szx, num, more, err := DecodeQBlockOption(value)
	require.NoError(t, err)
	require.Equal(t, SZX16, szx)
	require.Equal(t, int64(1), num)
	require.False(t, more)

	payload, more, err = sender.Block(0)
	require.NoError(t, err)
	require.True(t, more)
	require.Equal(t, []byte("abcdefghijklmnop"), payload)

	blocks, err := sender.Blocks([]uint32{1, 0, 1}, 2)
	require.NoError(t, err)
	require.Equal(t, [][]byte{[]byte("abcdefghijklmnop"), []byte("qrstuvwxyz")}, blocks)
	_, err = sender.Blocks([]uint32{0, 1, 2}, 2)
	require.ErrorIs(t, err, ErrQBlockTooLarge)
	_, _, err = sender.Block(2)
	require.ErrorIs(t, err, ErrQBlockInvalidNUM)
}

func TestQBlockAssemblyConsumesEncodedOptions(t *testing.T) {
	sender, err := NewSender(bytes.NewReader([]byte("abcdefghijklmnopqrstuvwxyz")), SZX16, 26)
	require.NoError(t, err)
	assembly, err := NewAssembly(SZX16, 26, 32, 2)
	require.NoError(t, err)

	for _, num := range []int64{1, 0} {
		payload, _, err := sender.Block(num)
		require.NoError(t, err)
		option, err := sender.BlockOption(num)
		require.NoError(t, err)
		complete, _, err := assembly.AddOption(option, payload)
		require.NoError(t, err)
		if num == 0 {
			require.True(t, complete)
		}
	}
}

func TestQBlockRequestOptionsSurviveDatagramCodec(t *testing.T) {
	request := pool.NewMessage(context.Background())
	request.SetCode(codes.PUT)
	request.SetType(message.Confirmable)
	request.SetMessageID(1)
	require.NoError(t, request.SetPath("/q"))
	request.SetOptionUint32(message.QBlock1, 0x38)
	request.SetOptionUint32(message.Size1, 4099)
	require.NoError(t, request.SetRequestTag([]byte("stableTG")))
	request.SetBody(bytes.NewReader([]byte("payload")))
	wire, err := request.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)

	received := pool.NewMessage(context.Background())
	_, err = received.UnmarshalWithDecoder(coder.DefaultCoder, wire)
	require.NoError(t, err)
	require.True(t, received.HasOption(message.QBlock1))
	require.True(t, received.HasOption(message.Size1))
	require.True(t, received.HasOption(message.RequestTag))
	require.Equal(t, "/q", mustRequestPath(t, received))
}

func mustRequestPath(t *testing.T, request *pool.Message) string {
	t.Helper()
	path, err := request.Path()
	require.NoError(t, err)
	return path
}

func TestQBlockSenderRejectsShortBody(t *testing.T) {
	sender, err := NewSender(bytes.NewReader([]byte("short")), SZX16, 16)
	require.NoError(t, err)
	_, _, err = sender.Block(0)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}
