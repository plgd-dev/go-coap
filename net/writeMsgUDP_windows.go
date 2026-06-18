//go:build windows

package net

func normalizeWriteMsgUDPResult(n int, err error, buffer []byte) (int, error) {
	if err == nil && n == 0 {
		return len(buffer), nil
	}
	return n, err
}
