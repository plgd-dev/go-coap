//go:build plan9
// +build plan9

package net

// Check if error returned by operation on a socket failed because
// the other side has closed the connection.
func IsConnectionBrokenError(err error) bool {
	return false
}
