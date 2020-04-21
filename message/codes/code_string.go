package codes

import "strconv"

func (c Code) String() string {
	switch c {
	case Empty:
		return "Empty"
	case GET:
		return "GET"
	case POST:
		return "POST"
	case PUT:
		return "PUT"
	case DELETE:
		return "DELETE"
	case Created:
		return "Created"
	case Deleted:
		return "Deleted"
	case Valid:
		return "Valid"
	case Changed:
		return "Changed"
	case Content:
		return "Content"
	case BadRequest:
		return "BadRequest"
	case Unauthorized:
		return "Unauthorized"
	case BadOption:
		return "BadOption"
	case Forbidden:
		return "Forbidden"
	case NotFound:
		return "NotFound"
	case MethodNotAllowed:
		return "MethodNotAllowed"
	case NotAcceptable:
		return "NotAcceptable"
	case PreconditionFailed:
		return "PreconditionFailed"
	case RequestEntityTooLarge:
		return "RequestEntityTooLarge"
	case UnsupportedMediaType:
		return "UnsupportedMediaType"
	case InternalServerError:
		return "InternalServerError"
	case NotImplemented:
		return "NotImplemented"
	case BadGateway:
		return "BadGateway"
	case ServiceUnavailable:
		return "ServiceUnavailable"
	case GatewayTimeout:
		return "GatewayTimeout"
	case ProxyingNotSupported:
		return "ProxyingNotSupported"
	case CSM:
		return "Capabilities and Settings Messages"
	case Ping:
		return "Ping"
	case Pong:
		return "Pong"
	case Release:
		return "Release"
	case Abort:
		return "Abort"
	default:
		return "Code(" + strconv.FormatInt(int64(c), 10) + ")"
	}
}
