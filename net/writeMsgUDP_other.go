//go:build !windows

package net

func normalizeWriteMsgUDPResult(n int, err error, buffer []byte) (int, error) {
	return n, err
}
