package coap

import (
	"net"
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

func TestServingIPv6MCastByClient(t *testing.T) {
	testServingMCast(t, "udp6-mcast", "[ff03::158]:11111", false, BlockWiseSzx16, 1033)
}

func TestServingIPv4AllInterfacesMCastByClient(t *testing.T) {
	ifis, err := net.Interfaces()
	if err != nil {
		t.Fatalf("unable to get interfaces: %v", err)
	}
	testServingMCastWithIfaces(t, "udp4-mcast", "225.0.1.187:11111", false, BlockWiseSzx16, 1033, ifis)
}

func TestServingIPv6AllInterfacesMCastByClient(t *testing.T) {
	ifis, err := net.Interfaces()
	if err != nil {
		t.Fatalf("unable to get interfaces: %v", err)
	}
	testServingMCastWithIfaces(t, "udp6-mcast", "[ff03::158]:5683", false, BlockWiseSzx16, 1033, ifis)
}
