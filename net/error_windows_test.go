//go:build windows

package net

import (
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestIsMessageSizeError(t *testing.T) {
	require.False(t, IsMessageSizeError(nil))
	require.False(t, IsMessageSizeError(syscall.WSAECONNRESET))
	require.True(t, IsMessageSizeError(windows.WSAEMSGSIZE))
	err := &net.OpError{
		Op:  "read",
		Net: "udp",
		Err: os.NewSyscallError("wsarecvfrom", windows.WSAEMSGSIZE),
	}
	require.True(t, IsMessageSizeError(fmt.Errorf("cannot read from udp connection: %w", err)))
}
