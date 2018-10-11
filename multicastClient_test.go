package coap

import (
	"testing"
)

func TestServingIPv4MCastBlockWiseSzx16(t *testing.T) {
	testServingMCast(t, "udp4-mcast", "225.0.1.187:11111", true, BlockWiseSzx16, 1033)
}

func TestServingIPv4MCastBlockWiseSzx32(t *testing.T) {
	testServingMCast(t, "udp4-mcast", "225.0.1.187:11111", true, BlockWiseSzx32, 1033)
}

func TestServingIPv4MCastBlockWiseSzx64(t *testing.T) {
	testServingMCast(t, "udp4-mcast", "225.0.1.187:11111", true, BlockWiseSzx64, 1033)
}

func TestServingIPv4MCastBlockWiseSzx128(t *testing.T) {
	testServingMCast(t, "udp4-mcast", "225.0.1.187:11111", true, BlockWiseSzx128, 1033)
}

func TestServingIPv4MCastBlockWiseSzx256(t *testing.T) {
	testServingMCast(t, "udp4-mcast", "225.0.1.187:11111", true, BlockWiseSzx256, 1033)
}

func TestServingIPv4MCastBlockWiseSzx512(t *testing.T) {
	testServingMCast(t, "udp4-mcast", "225.0.1.187:11111", true, BlockWiseSzx512, 1033)
}

func TestServingIPv4MCastBlockWiseSzx1024(t *testing.T) {
	testServingMCast(t, "udp4-mcast", "225.0.1.187:11111", true, BlockWiseSzx1024, 1033)
}
