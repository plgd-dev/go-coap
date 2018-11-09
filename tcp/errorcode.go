package tcpcoap

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
	ErrorCodeUint32OptionsTooSmall        = ErrorCode(-8)
	ErrorCodeStringOptionsTooSmall        = ErrorCode(-9)
	ErrorCodeBytesOptionsTooSmall         = ErrorCode(-10)
)
