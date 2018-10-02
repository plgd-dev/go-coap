package coap

// Error errors type of coap
type Error string

func (e Error) Error() string { return string(e) }

// ErrShortRead To construct Message we need to read more data from connection
const ErrShortRead = Error("short read")

// ErrTimeout Timeout occurs during waiting for response Message
const ErrTimeout = Error("timeout")

// ErrConnectionClosed Connection closed
const ErrConnectionClosed = Error("connection closed")

// ErrTokenAlreadyExist Token in request is not unique for session
const ErrTokenAlreadyExist = Error("token is not unique for session")

// ErrTokenNotExist Token in request is not exist
const ErrTokenNotExist = Error("token is not exist")

// ErrInvalidTokenLen invalid token length in Message
const ErrInvalidTokenLen = Error("invalid token length")

// ErrOptionTooLong option is too long  in Message
const ErrOptionTooLong = Error("option is too long")

// ErrOptionGapTooLarge option gap too large in Message
const ErrOptionGapTooLarge = Error("option gap too large")

// ErrOptionTruncated option is truncated
const ErrOptionTruncated = Error("option is truncated")

// ErrOptionUnexpectedExtendMarker unexpected extended option marker
const ErrOptionUnexpectedExtendMarker = Error("unexpected extended option marker")

// ErrMessageTruncated message is truncated
const ErrMessageTruncated = Error("message is truncated")

// ErrMessageInvalidVersion invalid version of Message
const ErrMessageInvalidVersion = Error("invalid version of Message")

// ErrServerAlreadyStarted server already started
const ErrServerAlreadyStarted = Error("server already started")

// ErrInvalidServerNetParameter invalid .Net parameter
const ErrInvalidNetParameter = Error("invalid .Net parameter")

// ErrInvalidServerConnParameter invalid Server.Conn parameter
const ErrInvalidServerConnParameter = Error("invalid Server.Conn parameter")

// ErrInvalidServerListenerParameter invalid Server.Listener parameter
const ErrInvalidServerListenerParameter = Error("invalid Server.Listener parameter")

// ErrServerNotStarted server not started
const ErrServerNotStarted = Error("server not started")

// ErrMsgTooLarge message it too large, for processing
const ErrMsgTooLarge = Error("message it too large, for processing")

// ErrInvalidResponse invalid response received for certain token
const ErrInvalidResponse = Error("invalid response")

// ErrNotSupported invalid response received for certain token
const ErrNotSupported = Error("not supported")

const ErrBlockNumberExceedLimit = Error("block number exceed limit 1,048,576")

const ErrBlockInvalidSize = Error("block has invalid size")

const ErrInvalidOptionBlock2 = Error("message has invalid value of Block2")

const ErrInvalidOptionBlock1 = Error("message has invalid value of Block1")

const ErrInvalidReponseCode = Error("response code has invalid value")

const ErrInvalidPayloadSize = Error("invalid payload size")

const ErrInvalidBlockSzx = Error("invalid block-wise transfer szx")

const ErrRequestEntityIncomplete = Error("payload comes in bad order")

const ErrInvalidRequest = Error("invalid request")
