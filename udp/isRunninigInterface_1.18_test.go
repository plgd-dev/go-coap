//go:build go1.18 || go1.19

package udp_test

import "net"

func isRunningInterface(net.Interface) bool {
	return true
}
