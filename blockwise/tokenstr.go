package blockwise

import "encoding/base64"

// TokenToStr converts token to string representation.
func TokenToStr(token []byte) string {
	return base64.StdEncoding.EncodeToString(token)
}
