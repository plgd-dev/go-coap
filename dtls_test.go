package coap

import "testing"

func TestServingDTLS(t *testing.T) {
	testServingTCPWithMsg(t, "udp-dtls", false, BlockWiseSzx16, make([]byte, 128), simpleMsg)
}

func TestServingDTLSBlockWiseSzx16(t *testing.T) {
	testServingTCPWithMsg(t, "udp-dtls", true, BlockWiseSzx16, make([]byte, 128), simpleMsg)
}

func TestServingDTLSBlockWiseSzx32(t *testing.T) {
	testServingTCPWithMsg(t, "udp-dtls", true, BlockWiseSzx32, make([]byte, 128), simpleMsg)
}

func TestServingDTLSBlockWiseSzx64(t *testing.T) {
	testServingTCPWithMsg(t, "udp-dtls", true, BlockWiseSzx64, make([]byte, 128), simpleMsg)
}

func TestServingDTLSBlockWiseSzx128(t *testing.T) {
	testServingTCPWithMsg(t, "udp-dtls", true, BlockWiseSzx128, make([]byte, 128), simpleMsg)
}

func TestServingDTLSBlockWiseSzx256(t *testing.T) {
	testServingTCPWithMsg(t, "udp-dtls", true, BlockWiseSzx256, make([]byte, 128), simpleMsg)
}

func TestServingDTLSBlockWiseSzx512(t *testing.T) {
	testServingTCPWithMsg(t, "udp-dtls", true, BlockWiseSzx512, make([]byte, 128), simpleMsg)
}

func TestServingDTLSBlockWiseSzx1024(t *testing.T) {
	testServingTCPWithMsg(t, "udp-dtls", true, BlockWiseSzx1024, make([]byte, 128), simpleMsg)
}
