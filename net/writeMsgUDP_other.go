//go:build !windows

package net

func normalizeWriteMsgUDPResult(n int, err error, _ []byte) (int, error) {
	return n, err
}
