package blockwise

import "encoding/base64"

func TokenToStr(token []byte) string {
	return base64.StdEncoding.EncodeToString(token)
}
