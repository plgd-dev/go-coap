package coap

import (
	"testing"
)

func TestServingIPv4MCastBlockSzx16(t *testing.T) {
	testServingMCast(t, "udp4-mcast", "225.0.1.187:11111", true, BlockSzx16, 1033)
}

func TestServingIPv4MCastBlockSzx32(t *testing.T) {
	testServingMCast(t, "udp4-mcast", "225.0.1.187:11111", true, BlockSzx32, 1033)
}

func TestServingIPv4MCastBlockSzx64(t *testing.T) {
	testServingMCast(t, "udp4-mcast", "225.0.1.187:11111", true, BlockSzx64, 1033)
}

func TestServingIPv4MCastBlockSzx128(t *testing.T) {
	testServingMCast(t, "udp4-mcast", "225.0.1.187:11111", true, BlockSzx128, 1033)
}

func TestServingIPv4MCastBlockSzx256(t *testing.T) {
	testServingMCast(t, "udp4-mcast", "225.0.1.187:11111", true, BlockSzx256, 1033)
}

func TestServingIPv4MCastBlockSzx512(t *testing.T) {
	testServingMCast(t, "udp4-mcast", "225.0.1.187:11111", true, BlockSzx512, 1033)
}

func TestServingIPv4MCastBlockSzx1024(t *testing.T) {
	testServingMCast(t, "udp4-mcast", "225.0.1.187:11111", true, BlockSzx1024, 1033)
}
