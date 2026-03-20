package client

import (
	"net"
	"testing"

	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/stretchr/testify/require"
)

func TestSetControlInformationNil(t *testing.T) {
	var cc Conn
	addr := net.ParseIP("192.0.2.1").To4()
	cc.localAddr.Store(&addr)
	cc.interfaceIndex.Store(7)

	cc.setControlInformation(nil)

	require.Equal(t, int64(0), cc.interfaceIndex.Load())
	require.Nil(t, cc.localAddr.Load())
}

func TestSetControlInformationUnicastCopiesAddress(t *testing.T) {
	var cc Conn
	dst := net.ParseIP("2001:db8::1")
	cm := &coapNet.ControlMessage{Dst: append(net.IP(nil), dst...), IfIndex: 9}

	cc.setControlInformation(cm)

	require.Equal(t, int64(9), cc.interfaceIndex.Load())
	stored := cc.localAddr.Load()
	require.NotNil(t, stored)
	expected := append(net.IP(nil), dst...)
	require.True(t, (*stored).Equal(expected))

	cm.Dst[0] ^= 0xff
	require.True(t, (*stored).Equal(expected))
}

func TestSetControlInformationMulticastClearsAddress(t *testing.T) {
	var cc Conn
	addr := net.ParseIP("192.0.2.1").To4()
	cc.localAddr.Store(&addr)

	cc.setControlInformation(&coapNet.ControlMessage{Dst: net.ParseIP("ff02::1"), IfIndex: 11})

	require.Equal(t, int64(11), cc.interfaceIndex.Load())
	require.Nil(t, cc.localAddr.Load())
}
