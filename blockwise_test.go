package coap

import (
	"fmt"
	"testing"
)

func testMarshal(t *testing.T, szx BlockSzx, blockNumber uint, moreBlocksFollowing bool, expectedBlock uint32) {
	fmt.Printf("testMarshal szx=%v, num=%v more=%v\n", szx, blockNumber, moreBlocksFollowing)
	block, err := MarshalBlockOption(szx, blockNumber, moreBlocksFollowing)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if block != expectedBlock {
		t.Fatalf("unexpected value of block %v, expected %v", block, expectedBlock)
	}
}

func testUnmarshal(t *testing.T, block uint32, expectedSzx BlockSzx, expectedNum uint, expectedMoreBlocksFollowing bool) {
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
	testMarshal(t, BlockSzx16, 0, false, uint32(0))
	testMarshal(t, BlockSzx16, 0, true, uint32(8))
	testMarshal(t, BlockSzx32, 0, false, uint32(1))
	testMarshal(t, BlockSzx32, 0, true, uint32(9))
	testMarshal(t, BlockSzx64, 0, false, uint32(2))
	testMarshal(t, BlockSzx64, 0, true, uint32(10))
	testMarshal(t, BlockSzx128, 0, false, uint32(3))
	testMarshal(t, BlockSzx128, 0, true, uint32(11))
	testMarshal(t, BlockSzx256, 0, false, uint32(4))
	testMarshal(t, BlockSzx256, 0, true, uint32(12))
	testMarshal(t, BlockSzx512, 0, false, uint32(5))
	testMarshal(t, BlockSzx512, 0, true, uint32(13))
	testMarshal(t, BlockSzx1024, 0, false, uint32(6))
	testMarshal(t, BlockSzx1024, 0, true, uint32(14))
	testMarshal(t, BlockSzxBERT, 0, false, uint32(7))
	testMarshal(t, BlockSzxBERT, 0, true, uint32(15))

	val, err := MarshalBlockOption(BlockSzx16, maxBlockNumber+1, false)
	if err == nil {
		t.Fatalf("expected error, block %v", val)
	}
}

func TestBlockWiseBlockUnmarshal(t *testing.T) {
	testUnmarshal(t, uint32(0), BlockSzx16, 0, false)
	testUnmarshal(t, uint32(8), BlockSzx16, 0, true)
	testUnmarshal(t, uint32(1), BlockSzx32, 0, false)
	testUnmarshal(t, uint32(9), BlockSzx32, 0, true)
	testUnmarshal(t, uint32(2), BlockSzx64, 0, false)
	testUnmarshal(t, uint32(10), BlockSzx64, 0, true)
	testUnmarshal(t, uint32(3), BlockSzx128, 0, false)
	testUnmarshal(t, uint32(11), BlockSzx128, 0, true)
	testUnmarshal(t, uint32(4), BlockSzx256, 0, false)
	testUnmarshal(t, uint32(12), BlockSzx256, 0, true)
	testUnmarshal(t, uint32(5), BlockSzx512, 0, false)
	testUnmarshal(t, uint32(13), BlockSzx512, 0, true)
	testUnmarshal(t, uint32(6), BlockSzx1024, 0, false)
	testUnmarshal(t, uint32(14), BlockSzx1024, 0, true)
	testUnmarshal(t, uint32(7), BlockSzxBERT, 0, false)
	testUnmarshal(t, uint32(15), BlockSzxBERT, 0, true)
	szx, num, m, err := UnmarshalBlockOption(0x1000000)
	if err == nil {
		t.Fatalf("expected error, szx %v, num %v, m %v", szx, num, m)
	}
}

func TestServingUDPBlockSzx16(t *testing.T) {
	testServingTCPWithMsg(t, "udp", true, BlockSzx16, make([]byte, 128), simpleMsg)
}

func TestServingUDPBlockSzx32(t *testing.T) {
	testServingTCPWithMsg(t, "udp", true, BlockSzx32, make([]byte, 128), simpleMsg)
}

func TestServingUDPBlockSzx64(t *testing.T) {
	testServingTCPWithMsg(t, "udp", true, BlockSzx64, make([]byte, 128), simpleMsg)
}

func TestServingUDPBlockSzx128(t *testing.T) {
	testServingTCPWithMsg(t, "udp", true, BlockSzx128, make([]byte, 128), simpleMsg)
}

func TestServingUDPBlockSzx256(t *testing.T) {
	testServingTCPWithMsg(t, "udp", true, BlockSzx256, make([]byte, 128), simpleMsg)
}

func TestServingUDPBlockSzx512(t *testing.T) {
	testServingTCPWithMsg(t, "udp", true, BlockSzx512, make([]byte, 128), simpleMsg)
}

func TestServingUDPBlockSzx1024(t *testing.T) {
	testServingTCPWithMsg(t, "udp", true, BlockSzx1024, make([]byte, 128), simpleMsg)
}

func TestServingUDPBlockSzxBERT(t *testing.T) {
	_, addr, _, err := RunLocalUDPServer("udp", ":0", true, BlockSzx1024)
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}

	BlockWiseTransfer := true
	BlockWiseTransferSzx := BlockSzxBERT
	c := Client{Net: "udp", BlockWiseTransfer: &BlockWiseTransfer, BlockWiseTransferSzx: &BlockWiseTransferSzx}
	_, err = c.Dial(addr)
	if err != nil {
		if err.Error() != ErrInvalidBlockSzx.Error() {
			t.Fatalf("Expected error '%v', got '%v'", err, ErrInvalidBlockSzx)
		}
	} else {
		t.Fatalf("Expected error '%v'", ErrInvalidBlockSzx)
	}
}

func TestServingTCPBlockSzx16(t *testing.T) {
	testServingTCPWithMsg(t, "tcp", true, BlockSzx16, make([]byte, 128), simpleMsg)
}

func TestServingTCPBlockSzx32(t *testing.T) {
	testServingTCPWithMsg(t, "tcp", true, BlockSzx32, make([]byte, 128), simpleMsg)
}

func TestServingTCPBlockSzx64(t *testing.T) {
	testServingTCPWithMsg(t, "tcp", true, BlockSzx64, make([]byte, 128), simpleMsg)
}

func TestServingTCPBlockSzx128(t *testing.T) {
	testServingTCPWithMsg(t, "tcp", true, BlockSzx128, make([]byte, 128), simpleMsg)
}

func TestServingTCPBlockSzx256(t *testing.T) {
	testServingTCPWithMsg(t, "tcp", true, BlockSzx256, make([]byte, 128), simpleMsg)
}

func TestServingTCPBlockSzx512(t *testing.T) {
	testServingTCPWithMsg(t, "tcp", true, BlockSzx512, make([]byte, 128), simpleMsg)
}

func TestServingTCPBlockSzx1024(t *testing.T) {
	testServingTCPWithMsg(t, "tcp", true, BlockSzx1024, make([]byte, 128), simpleMsg)
}

func TestServingTCPBlockSzxBERT(t *testing.T) {
	testServingTCPWithMsg(t, "tcp", true, BlockSzxBERT, make([]byte, 128), simpleMsg)
}

func TestServingTCPBigMsgBlockSzx1024(t *testing.T) {
	testServingTCPWithMsg(t, "tcp", true, BlockSzx1024, make([]byte, 10*1024*1024), simpleMsg)
}

func TestServingTCPBigMsgBlockSzxBERT(t *testing.T) {
	testServingTCPWithMsg(t, "tcp", true, BlockSzxBERT, make([]byte, 10*1024*1024), simpleMsg)
}
