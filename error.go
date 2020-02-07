package coap

import (
	"context"
	"fmt"

	"github.com/go-ocf/go-coap/net"
)

// Error errors type of coap
type Error string

func (e Error) Error() string { return string(e) }

// ErrShortRead To construct Message we need to read more data from connection
const ErrShortRead = Error("short read")

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

// ErrInvalidNetParameter invalid .Net parameter
const ErrInvalidNetParameter = Error("invalid .Net parameter")

// ErrInvalidMaxMesssageSizeParameter invalid .MaxMessageSize parameter
const ErrInvalidMaxMesssageSizeParameter = Error("invalid .MaxMessageSize parameter")

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

// ErrBlockNumberExceedLimit block number exceed limit 1,048,576
const ErrBlockNumberExceedLimit = Error("block number exceed limit 1,048,576")

// ErrBlockInvalidSize block has invalid size
const ErrBlockInvalidSize = Error("block has invalid size")

// ErrInvalidOptionBlock2 message has invalid value of Block2
const ErrInvalidOptionBlock2 = Error("message has invalid value of Block2")

// ErrInvalidOptionBlock1 message has invalid value of Block1
const ErrInvalidOptionBlock1 = Error("message has invalid value of Block1")

// ErrInvalidReponseCode response code has invalid value
const ErrInvalidReponseCode = Error("response code has invalid value")

// ErrInvalidPayloadSize invalid payload size
const ErrInvalidPayloadSize = Error("invalid payload size")

// ErrInvalidBlockWiseSzx invalid block-wise transfer szx
const ErrInvalidBlockWiseSzx = Error("invalid block-wise transfer szx")

// ErrRequestEntityIncomplete payload comes in bad order
const ErrRequestEntityIncomplete = Error("payload comes in bad order")

// ErrInvalidRequest invalid requests
const ErrInvalidRequest = Error("invalid request")

// ErrContentFormatNotSet content format is not set
const ErrContentFormatNotSet = Error("content format is not set")

//ErrInvalidPayload invalid payload
const ErrInvalidPayload = Error("invalid payload")

//ErrUnexpectedReponseCode unexpected response code occurs
const ErrUnexpectedReponseCode = Error("unexpected response code")

// ErrMessageNotInterested message is not of interest to the client
const ErrMessageNotInterested = Error("message not to be sent due to disinterest")

// ErrMaxMessageSizeLimitExceeded message size is bigger than maximum message size limit
const ErrMaxMessageSizeLimitExceeded = Error("maximum message size limit exceeded")

// ErrServerClosed Server closed
const ErrServerClosed = net.ErrServerClosed

// ErrKeepAliveDeadlineExceeded occurs during waiting for pong response
var ErrKeepAliveDeadlineExceeded = fmt.Errorf("keepalive: %w", context.DeadlineExceeded)
