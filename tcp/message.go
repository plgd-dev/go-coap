package tcpcoap

import "fmt"

// COAPType represents the message type.
type COAPType uint8

// MaxTokenSize maximum of token size that can be used in message
const MaxTokenSize = 8

const (
	// Confirmable messages require acknowledgements.
	Confirmable COAPType = 0
	// NonConfirmable messages do not require acknowledgements.
	NonConfirmable COAPType = 1
	// Acknowledgement is a message indicating a response to confirmable message.
	Acknowledgement COAPType = 2
	// Reset indicates a permanent negative acknowledgement.
	Reset COAPType = 3
)

// COAPCode is the type used for both request and response codes.
type COAPCode uint8

// Request Codes
const (
	GET    COAPCode = 1
	POST   COAPCode = 2
	PUT    COAPCode = 3
	DELETE COAPCode = 4
)

// Response Codes
const (
	Empty                   COAPCode = 0
	Created                 COAPCode = 65
	Deleted                 COAPCode = 66
	Valid                   COAPCode = 67
	Changed                 COAPCode = 68
	Content                 COAPCode = 69
	Continue                COAPCode = 95
	BadRequest              COAPCode = 128
	Unauthorized            COAPCode = 129
	BadOption               COAPCode = 130
	Forbidden               COAPCode = 131
	NotFound                COAPCode = 132
	MethodNotAllowed        COAPCode = 133
	NotAcceptable           COAPCode = 134
	RequestEntityIncomplete COAPCode = 136
	PreconditionFailed      COAPCode = 140
	RequestEntityTooLarge   COAPCode = 141
	UnsupportedMediaType    COAPCode = 143
	InternalServerError     COAPCode = 160
	NotImplemented          COAPCode = 161
	BadGateway              COAPCode = 162
	ServiceUnavailable      COAPCode = 163
	GatewayTimeout          COAPCode = 164
	ProxyingNotSupported    COAPCode = 165
)

//Signaling Codes for TCP
const (
	CSM     COAPCode = 225
	Ping    COAPCode = 226
	Pong    COAPCode = 227
	Release COAPCode = 228
	Abort   COAPCode = 229
)

var codeNames = [256]string{
	GET:                   "GET",
	POST:                  "POST",
	PUT:                   "PUT",
	DELETE:                "DELETE",
	Created:               "Created",
	Deleted:               "Deleted",
	Valid:                 "Valid",
	Changed:               "Changed",
	Content:               "Content",
	BadRequest:            "BadRequest",
	Unauthorized:          "Unauthorized",
	BadOption:             "BadOption",
	Forbidden:             "Forbidden",
	NotFound:              "NotFound",
	MethodNotAllowed:      "MethodNotAllowed",
	NotAcceptable:         "NotAcceptable",
	PreconditionFailed:    "PreconditionFailed",
	RequestEntityTooLarge: "RequestEntityTooLarge",
	UnsupportedMediaType:  "UnsupportedMediaType",
	InternalServerError:   "InternalServerError",
	NotImplemented:        "NotImplemented",
	BadGateway:            "BadGateway",
	ServiceUnavailable:    "ServiceUnavailable",
	GatewayTimeout:        "GatewayTimeout",
	ProxyingNotSupported:  "ProxyingNotSupported",
	CSM:                   "Capabilities and Settings Messages",
	Ping:                  "Ping",
	Pong:                  "Pong",
	Release:               "Release",
	Abort:                 "Abort",
}

func init() {
	for i := range codeNames {
		if codeNames[i] == "" {
			codeNames[i] = fmt.Sprintf("Unknown (0x%x)", i)
		}
	}
}

func (c COAPCode) String() string {
	return codeNames[c]
}
