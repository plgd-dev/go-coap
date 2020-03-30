package net

import "syscall"

func SocketReuseAddr(descriptor uintptr) {
	syscall.SetsockoptInt(syscall.Handle(descriptor), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
}
