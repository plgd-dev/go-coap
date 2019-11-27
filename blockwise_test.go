package coap

import (
	"bytes"
	"fmt"
	"log"
	"testing"

	"github.com/go-ocf/go-coap/codes"
)

func testMarshal(t *testing.T, szx BlockWiseSzx, blockNumber uint, moreBlocksFollowing bool, expectedBlock uint32) {
	fmt.Printf("testMarshal szx=%v, num=%v more=%v\n", szx, blockNumber, moreBlocksFollowing)
	block, err := MarshalBlockOption(szx, blockNumber, moreBlocksFollowing)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if block != expectedBlock {
		t.Fatalf("unexpected value of block %v, expected %v", block, expectedBlock)
	}
}

func testUnmarshal(t *testing.T, block uint32, expectedSzx BlockWiseSzx, expectedNum uint, expectedMoreBlocksFollowing bool) {
	fmt.Printf("testUnmarshal %v\n", block)
	szx, num, more, err := UnmarshalBlockOption(block)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if szx != expectedSzx {
		t.Fatalf("unexpected szx of block %v, expected %v", szx, expectedSzx)
	}
	if num != expectedNum {
		t.Fatalf("unexpected num of block %v, expected %v", num, expectedNum)
	}
	if more != expectedMoreBlocksFollowing {
		t.Fatalf("unexpected more of block %v, expected %v", more, expectedMoreBlocksFollowing)
	}
}

func TestBlockWiseBlockMarshal(t *testing.T) {
	testMarshal(t, BlockWiseSzx16, 0, false, uint32(0))
	testMarshal(t, BlockWiseSzx16, 0, true, uint32(8))
	testMarshal(t, BlockWiseSzx32, 0, false, uint32(1))
	testMarshal(t, BlockWiseSzx32, 0, true, uint32(9))
	testMarshal(t, BlockWiseSzx64, 0, false, uint32(2))
	testMarshal(t, BlockWiseSzx64, 0, true, uint32(10))
	testMarshal(t, BlockWiseSzx128, 0, false, uint32(3))
	testMarshal(t, BlockWiseSzx128, 0, true, uint32(11))
	testMarshal(t, BlockWiseSzx256, 0, false, uint32(4))
	testMarshal(t, BlockWiseSzx256, 0, true, uint32(12))
	testMarshal(t, BlockWiseSzx512, 0, false, uint32(5))
	testMarshal(t, BlockWiseSzx512, 0, true, uint32(13))
	testMarshal(t, BlockWiseSzx1024, 0, false, uint32(6))
	testMarshal(t, BlockWiseSzx1024, 0, true, uint32(14))
	testMarshal(t, BlockWiseSzxBERT, 0, false, uint32(7))
	testMarshal(t, BlockWiseSzxBERT, 0, true, uint32(15))

	val, err := MarshalBlockOption(BlockWiseSzx16, maxBlockNumber+1, false)
	if err == nil {
		t.Fatalf("expected error, block %v", val)
	}
}

func TestBlockWiseBlockUnmarshal(t *testing.T) {
	testUnmarshal(t, uint32(0), BlockWiseSzx16, 0, false)
	testUnmarshal(t, uint32(8), BlockWiseSzx16, 0, true)
	testUnmarshal(t, uint32(1), BlockWiseSzx32, 0, false)
	testUnmarshal(t, uint32(9), BlockWiseSzx32, 0, true)
	testUnmarshal(t, uint32(2), BlockWiseSzx64, 0, false)
	testUnmarshal(t, uint32(10), BlockWiseSzx64, 0, true)
	testUnmarshal(t, uint32(3), BlockWiseSzx128, 0, false)
	testUnmarshal(t, uint32(11), BlockWiseSzx128, 0, true)
	testUnmarshal(t, uint32(4), BlockWiseSzx256, 0, false)
	testUnmarshal(t, uint32(12), BlockWiseSzx256, 0, true)
	testUnmarshal(t, uint32(5), BlockWiseSzx512, 0, false)
	testUnmarshal(t, uint32(13), BlockWiseSzx512, 0, true)
	testUnmarshal(t, uint32(6), BlockWiseSzx1024, 0, false)
	testUnmarshal(t, uint32(14), BlockWiseSzx1024, 0, true)
	testUnmarshal(t, uint32(7), BlockWiseSzxBERT, 0, false)
	testUnmarshal(t, uint32(15), BlockWiseSzxBERT, 0, true)
	szx, num, m, err := UnmarshalBlockOption(0x1000000)
	if err == nil {
		t.Fatalf("expected error, szx %v, num %v, m %v", szx, num, m)
	}
}

func TestServingUDPBlockWiseSzx16(t *testing.T) {
	testServingTCPWithMsg(t, "udp", true, BlockWiseSzx16, make([]byte, 128), simpleMsg)
}

func TestServingUDPBlockWiseSzx32(t *testing.T) {
	testServingTCPWithMsg(t, "udp", true, BlockWiseSzx32, make([]byte, 128), simpleMsg)
}

func TestServingUDPBlockWiseSzx64(t *testing.T) {
	testServingTCPWithMsg(t, "udp", true, BlockWiseSzx64, make([]byte, 128), simpleMsg)
}

func TestServingUDPBlockWiseSzx128(t *testing.T) {
	testServingTCPWithMsg(t, "udp", true, BlockWiseSzx128, make([]byte, 128), simpleMsg)
}

func TestServingUDPBlockWiseSzx256(t *testing.T) {
	testServingTCPWithMsg(t, "udp", true, BlockWiseSzx256, make([]byte, 128), simpleMsg)
}

func TestServingUDPBlockWiseSzx512(t *testing.T) {
	testServingTCPWithMsg(t, "udp", true, BlockWiseSzx512, make([]byte, 128), simpleMsg)
}

func TestServingUDPBlockWiseSzx1024(t *testing.T) {
	testServingTCPWithMsg(t, "udp", true, BlockWiseSzx1024, make([]byte, 128), simpleMsg)
}

func TestServingUDPBlockWiseSzxBERT(t *testing.T) {
	_, addr, _, err := RunLocalUDPServer("udp", ":0", true, BlockWiseSzx1024)
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}

	BlockWiseTransfer := true
	BlockWiseTransferSzx := BlockWiseSzxBERT
	c := Client{Net: "udp", BlockWiseTransfer: &BlockWiseTransfer, BlockWiseTransferSzx: &BlockWiseTransferSzx}
	_, err = c.Dial(addr)
	if err != nil {
		if err.Error() != ErrInvalidBlockWiseSzx.Error() {
			t.Fatalf("Expected error '%v', got '%v'", err, ErrInvalidBlockWiseSzx)
		}
	} else {
		t.Fatalf("Expected error '%v'", ErrInvalidBlockWiseSzx)
	}
}

func TestServingTCPBlockWiseSzx16(t *testing.T) {
	testServingTCPWithMsg(t, "tcp", true, BlockWiseSzx16, make([]byte, 128), simpleMsg)
}

func TestServingTCPBlockWiseSzx32(t *testing.T) {
	testServingTCPWithMsg(t, "tcp", true, BlockWiseSzx32, make([]byte, 128), simpleMsg)
}

func TestServingTCPBlockWiseSzx64(t *testing.T) {
	testServingTCPWithMsg(t, "tcp", true, BlockWiseSzx64, make([]byte, 128), simpleMsg)
}

func TestServingTCPBlockWiseSzx128(t *testing.T) {
	testServingTCPWithMsg(t, "tcp", true, BlockWiseSzx128, make([]byte, 128), simpleMsg)
}

func TestServingTCPBlockWiseSzx256(t *testing.T) {
	testServingTCPWithMsg(t, "tcp", true, BlockWiseSzx256, make([]byte, 128), simpleMsg)
}

func TestServingTCPBlockWiseSzx512(t *testing.T) {
	testServingTCPWithMsg(t, "tcp", true, BlockWiseSzx512, make([]byte, 128), simpleMsg)
}

func TestServingTCPBlockWiseSzx1024(t *testing.T) {
	testServingTCPWithMsg(t, "tcp", true, BlockWiseSzx1024, make([]byte, 128), simpleMsg)
}

func TestServingTCPBlockWiseSzxBERT(t *testing.T) {
	testServingTCPWithMsg(t, "tcp", true, BlockWiseSzxBERT, make([]byte, 128), simpleMsg)
}

func TestServingTCPBigMsgBlockWiseSzx1024(t *testing.T) {
	testServingTCPWithMsg(t, "tcp", true, BlockWiseSzx1024, make([]byte, 1024), simpleMsg)
}

func TestServingTCPBigMsgBlockWiseSzxBERT(t *testing.T) {
	testServingTCPWithMsg(t, "tcp", true, BlockWiseSzxBERT, make([]byte, 10*1024*1024), simpleMsg)
}

var helloWorld = []byte("Hello world")

// EchoServerUsingWrite echoes request payloads using ResponseWriter.Write
func EchoServerUsingWrite(w ResponseWriter, r *Request) {
	w.SetCode(codes.Content)
	if mt, ok := r.Msg.Option(ContentFormat).(MediaType); ok {
		w.SetContentFormat(mt)
		_, err := w.Write(r.Msg.Payload())
		if err != nil {
			log.Printf("Cannot write echo %v", err)
		}
	} else {
		w.SetContentFormat(TextPlain)
		_, err := w.Write(helloWorld)
		if err != nil {
			log.Printf("Cannot write echo %v", err)
		}
	}
}

func TestServingUDPBlockWiseUsingWrite(t *testing.T) {
	// Test that responding to blockwise requests using ResponseWrite.write
	// works correctly (as opposed to using WriteMsg directly)

	HandleFunc("/test-with-write", EchoServerUsingWrite)
	defer HandleRemove("/test-with-write")

	payload := make([]byte, 512)

	_, addr, _, err := RunLocalUDPServer("udp", ":0", true, BlockWiseSzx1024)
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}

	BlockWiseTransfer := true
	BlockWiseTransferSzx := BlockWiseSzx128
	c := &Client{
		Net:                  "udp",
		BlockWiseTransfer:    &BlockWiseTransfer,
		BlockWiseTransferSzx: &BlockWiseTransferSzx,
		MaxMessageSize:       ^uint32(0),
	}
	co, err := c.Dial(addr)
	if err != nil {
		t.Fatal("cannot dial", err)
	}

	req, err := co.NewPostRequest("/test-with-write", TextPlain, bytes.NewBuffer(payload))
	if err != nil {
		t.Fatal("cannot create request", err)
	}

	m, err := co.Exchange(req)
	if err != nil {
		t.Fatal("failed to exchange", err)
	}
	if m == nil {
		t.Fatalf("Didn't receive CoAP response")
	}

	expectedMsg := &DgramMessage{
		MessageBase: MessageBase{
			typ:     Acknowledgement,
			code:    codes.Content,
			payload: req.Payload(),
			token:   req.Token(),
		},
		messageID: req.MessageID(),
	}
	expectedMsg.SetOption(ContentFormat, req.Option(ContentFormat))

	assertEqualMessages(t, expectedMsg, m)
}

func TestServingUDPBlockWiseWithClientWithoutBlockWise(t *testing.T) {
	HandleFunc("/test-with-write", EchoServerUsingWrite)
	defer HandleRemove("/test-with-write")

	payload := make([]byte, 8)

	_, addr, _, err := RunLocalUDPServer("udp", ":0", true, BlockWiseSzx16)
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}

	BlockWiseTransfer := false
	BlockWiseTransferSzx := BlockWiseSzx128
	c := &Client{
		Net:                  "udp",
		BlockWiseTransfer:    &BlockWiseTransfer,
		BlockWiseTransferSzx: &BlockWiseTransferSzx,
		MaxMessageSize:       ^uint32(0),
	}
	co, err := c.Dial(addr)
	if err != nil {
		t.Fatal("cannot dial", err)
	}

	req, err := co.NewPostRequest("/test-with-write", TextPlain, bytes.NewBuffer(payload))
	if err != nil {
		t.Fatal("cannot create request", err)
	}

	m, err := co.Exchange(req)
	if err != nil {
		t.Fatal("failed to exchange", err)
	}
	if m == nil {
		t.Fatalf("Didn't receive CoAP response")
	}

	expectedMsg := &DgramMessage{
		MessageBase: MessageBase{
			typ:     Acknowledgement,
			code:    codes.Content,
			payload: req.Payload(),
			token:   req.Token(),
		},
		messageID: req.MessageID(),
	}

	expectedMsg.SetOption(ContentFormat, TextPlain)
	expectedMsg.SetOption(Block2, uint32(0))
	expectedMsg.SetOption(Size2, uint32(len(req.Payload())))

	assertEqualMessages(t, expectedMsg, m)

	getReq, err := co.NewGetRequest("/test-with-write")
	if err != nil {
		t.Fatal("cannot create request", err)
	}

	getResp, err := co.Exchange(getReq)
	if err != nil {
		t.Fatal("failed to exchange", err)
	}
	expectedGetMsg := DgramMessage{
		MessageBase: MessageBase{
			typ:     Acknowledgement,
			code:    codes.Content,
			payload: helloWorld,
			token:   getReq.Token(),
		},
		messageID: getReq.MessageID(),
	}

	if etag, ok := getResp.Option(ETag).([]byte); ok {
		expectedGetMsg.SetOption(ETag, etag)
	}

	expectedGetMsg.SetOption(ContentFormat, TextPlain)
	expectedGetMsg.SetOption(Block2, uint32(0))
	expectedGetMsg.SetOption(Size2, uint32(len(helloWorld)))

	assertEqualMessages(t, &expectedGetMsg, getResp)
}
