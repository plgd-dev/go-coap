package message

import "errors"

var (
	ErrTooSmall                     = errors.New("too small buffer")
	ErrInvalidOptionHeaderExt       = errors.New("invalid option header ext")
	ErrInvalidTokenLen              = errors.New("invalid token length")
	ErrInvalidValueLength           = errors.New("invalid value length")
	ErrShortRead                    = errors.New("invalid shor read")
	ErrOptionTruncated              = errors.New("option truncated")
	ErrOptionUnexpectedExtendMarker = errors.New("option unexpected extend marker")
	ErrBytesOptionsTooSmall         = errors.New("bytes options too small")
	ErrInvalidEncoding              = errors.New("invalid encoding")
	ErrOptionNotFound               = errors.New("option not found")
	ErrOptionDuplicate              = errors.New("duplicated option")
)
