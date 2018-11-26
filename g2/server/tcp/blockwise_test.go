package coapservertcp

import (
	"fmt"
	"testing"
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
