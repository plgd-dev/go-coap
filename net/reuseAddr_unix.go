// +build !windows

package net

import "syscall"

func SocketReuseAddr(descriptor uintptr) {
	syscall.SetsockoptInt(int(descriptor), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
}
