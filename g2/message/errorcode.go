package message

// Error errors type of coap
type ErrorCode int

const (
	OK                                    = ErrorCode(0)
	ErrorCodeTooSmall                     = ErrorCode(-1)
	ErrorCodeInvalidOptionHeaderExt       = ErrorCode(-2)
	ErrorCodeInvalidTokenLen              = ErrorCode(-3)
	ErrorCodeInvalidValueLength           = ErrorCode(-4)
	ErrorCodeShortRead                    = ErrorCode(-5)
	ErrorCodeOptionTruncated              = ErrorCode(-6)
	ErrorCodeOptionUnexpectedExtendMarker = ErrorCode(-7)
	ErrorCodeBytesOptionsTooSmall         = ErrorCode(-10)
	ErrorCodeInvalidEncoding              = ErrorCode(-11)
	ErrorCodeOptionNotFound               = ErrorCode(-12)
	ErrorCodeOptionDuplicate              = ErrorCode(-13)
)
