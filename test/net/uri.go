// Helper package for tests, must not be used in production code.
package net

import (
	"regexp"
	"strings"
	"time"

	pkgRand "github.com/plgd-dev/go-coap/v3/pkg/rand"
)

var weakRng = pkgRand.NewRand(time.Now().UnixNano())

const (
	// 71 allowed letters in URL path segment
	urlLetterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ-._~!$&'()*+,;=:@"

	urlLetterIdxBits = 7                       // we need 7 bits to represent a letter index (0..70)
	urlLetterIdxMask = 1<<urlLetterIdxBits - 1 // All 1-bits, as many as letterIdxBits
)

// RandomURLString generates random URL path of length n.
func RandomURLString(n int) string {
	b := make([]byte, n)
	if n > 0 {
		b[0] = '/'
	}
	for i := 1; i < n; {
		if idx := int(weakRng.Int63() & urlLetterIdxMask); idx < len(urlLetterBytes) {
			b[i] = urlLetterBytes[idx]
			i++
		}
	}
	return string(b)
}

// RandomValidURLString generate URL path of length n where the delimiter '/' occurs
// at least every maxSegmentLen characters.
func RandomValidURLString(n, maxSegmentLen int) string {
	b := make([]byte, n)
	if n > 0 {
		b[0] = '/'
	}
	for i := 1; i < n; {
		if idx := int(weakRng.Int63() & urlLetterIdxMask); idx < len(urlLetterBytes) {
			b[i] = urlLetterBytes[idx]
			i++
		}
	}

	// ensure that at at least every maxSegmentLen-th character is '/', otherwise
	// SetPath will fail with invalid path error
	index := 0
	for {
		remainder := n - index
		if remainder < maxSegmentLen {
			break
		}
		shift := uint8(weakRng.Int63() >> 55)
		index = index + int(shift) + 1
		if index >= n {
			index = n - 1
		}
		b[index] = '/'
	}
	return string(b)
}

// NormalizeURLPath replace repeated '/' characters with a single '/' character
// and remove ending '/'.
func NormalizeURLPath(s string) string {
	if len(s) == 0 {
		return s
	}
	space := regexp.MustCompile("/+")
	return strings.TrimSuffix(space.ReplaceAllString(s, "/"), "/")
}
