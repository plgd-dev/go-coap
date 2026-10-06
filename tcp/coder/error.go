package coder

import "errors"

var (
	ErrMessageTruncated      = errors.New("message is truncated")
	ErrMessageInvalidVersion = errors.New("message has invalid version")
	ErrMessageTooLong        = errors.New("message length exceeds supported maximum")
)
