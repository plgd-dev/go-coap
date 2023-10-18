//go:build !go1.18 && !go1.19

package udp_test

import "net"

func isRunningInterface(i net.Interface) bool {
	return i.Flags&net.FlagRunning == net.FlagRunning
}
