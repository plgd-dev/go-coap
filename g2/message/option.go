package message

import (
	"encoding/binary"
	"strconv"
)

const (
	max1ByteNumber = uint32(^uint8(0))
	max2ByteNumber = uint32(^uint16(0))
	max3ByteNumber = uint32(0xffffff)
)

const (
	ExtendOptionByteCode   = 13
	ExtendOptionByteAddend = 13
	ExtendOptionWordCode   = 14
	ExtendOptionWordAddend = 269
	ExtendOptionError      = 15
)

// OptionID identifies an option in a message.
type OptionID uint8

/*
   +-----+----+---+---+---+----------------+--------+--------+---------+
   | No. | C  | U | N | R | Name           | Format | Length | Default |
   +-----+----+---+---+---+----------------+--------+--------+---------+
   |   1 | x  |   |   | x | If-Match       | opaque | 0-8    | (none)  |
   |   3 | x  | x | - |   | Uri-Host       | string | 1-255  | (see    |
   |     |    |   |   |   |                |        |        | below)  |
   |   4 |    |   |   | x | ETag           | opaque | 1-8    | (none)  |
   |   5 | x  |   |   |   | If-None-Match  | empty  | 0      | (none)  |
   |   7 | x  | x | - |   | Uri-Port       | uint   | 0-2    | (see    |
   |     |    |   |   |   |                |        |        | below)  |
   |   8 |    |   |   | x | Location-Path  | string | 0-255  | (none)  |
   |  11 | x  | x | - | x | Uri-Path       | string | 0-255  | (none)  |
   |  12 |    |   |   |   | Content-Format | uint   | 0-2    | (none)  |
   |  14 |    | x | - |   | Max-Age        | uint   | 0-4    | 60      |
   |  15 | x  | x | - | x | Uri-Query      | string | 0-255  | (none)  |
   |  17 | x  |   |   |   | Accept         | uint   | 0-2    | (none)  |
   |  20 |    |   |   | x | Location-Query | string | 0-255  | (none)  |
   |  23 | x  | x | - | - | Block2         | uint   | 0-3    | (none)  |
   |  27 | x  | x | - | - | Block1         | uint   | 0-3    | (none)  |
   |  28 |    |   | x |   | Size2          | uint   | 0-4    | (none)  |
   |  35 | x  | x | - |   | Proxy-Uri      | string | 1-1034 | (none)  |
   |  39 | x  | x | - |   | Proxy-Scheme   | string | 1-255  | (none)  |
   |  60 |    |   | x |   | Size1          | uint   | 0-4    | (none)  |
   +-----+----+---+---+---+----------------+--------+--------+---------+
   C=Critical, U=Unsafe, N=NoCacheKey, R=Repeatable
*/

// Option IDs.
const (
	IfMatch       OptionID = 1
	URIHost       OptionID = 3
	ETag          OptionID = 4
	IfNoneMatch   OptionID = 5
	Observe       OptionID = 6
	URIPort       OptionID = 7
	LocationPath  OptionID = 8
	URIPath       OptionID = 11
	ContentFormat OptionID = 12
	MaxAge        OptionID = 14
	URIQuery      OptionID = 15
	Accept        OptionID = 17
	LocationQuery OptionID = 20
	Block2        OptionID = 23
	Block1        OptionID = 27
	Size2         OptionID = 28
	ProxyURI      OptionID = 35
	ProxyScheme   OptionID = 39
	Size1         OptionID = 60
)

// Option value format (RFC7252 section 3.2)
type ValueFormat uint8

const (
	ValueUnknown ValueFormat = iota
	ValueEmpty
	ValueOpaque
	ValueUint
	ValueString
)

type OptionDef struct {
	ValueFormat ValueFormat
	MinLen      int
	MaxLen      int
}

var CoapOptionDefs = map[OptionID]OptionDef{
	IfMatch:       OptionDef{ValueFormat: ValueOpaque, MinLen: 0, MaxLen: 8},
	URIHost:       OptionDef{ValueFormat: ValueString, MinLen: 1, MaxLen: 255},
	ETag:          OptionDef{ValueFormat: ValueOpaque, MinLen: 1, MaxLen: 8},
	IfNoneMatch:   OptionDef{ValueFormat: ValueEmpty, MinLen: 0, MaxLen: 0},
	Observe:       OptionDef{ValueFormat: ValueUint, MinLen: 0, MaxLen: 3},
	URIPort:       OptionDef{ValueFormat: ValueUint, MinLen: 0, MaxLen: 2},
	LocationPath:  OptionDef{ValueFormat: ValueString, MinLen: 0, MaxLen: 255},
	URIPath:       OptionDef{ValueFormat: ValueString, MinLen: 0, MaxLen: 255},
	ContentFormat: OptionDef{ValueFormat: ValueUint, MinLen: 0, MaxLen: 2},
	MaxAge:        OptionDef{ValueFormat: ValueUint, MinLen: 0, MaxLen: 4},
	URIQuery:      OptionDef{ValueFormat: ValueString, MinLen: 0, MaxLen: 255},
	Accept:        OptionDef{ValueFormat: ValueUint, MinLen: 0, MaxLen: 2},
	LocationQuery: OptionDef{ValueFormat: ValueString, MinLen: 0, MaxLen: 255},
	Block2:        OptionDef{ValueFormat: ValueUint, MinLen: 0, MaxLen: 3},
	Block1:        OptionDef{ValueFormat: ValueUint, MinLen: 0, MaxLen: 3},
	Size2:         OptionDef{ValueFormat: ValueUint, MinLen: 0, MaxLen: 4},
	ProxyURI:      OptionDef{ValueFormat: ValueString, MinLen: 1, MaxLen: 1034},
	ProxyScheme:   OptionDef{ValueFormat: ValueString, MinLen: 1, MaxLen: 255},
	Size1:         OptionDef{ValueFormat: ValueUint, MinLen: 0, MaxLen: 4},
}

// MediaType specifies the content format of a message.
type MediaType uint16

// Content formats.
const (
	TextPlain         MediaType = 0     // text/plain;charset=utf-8
	AppCoseEncrypt0   MediaType = 16    // application/cose; cose-type="cose-encrypt0" (RFC 8152)
	AppCoseMac0       MediaType = 17    // application/cose; cose-type="cose-mac0" (RFC 8152)
	AppCoseSign1      MediaType = 18    // application/cose; cose-type="cose-sign1" (RFC 8152)
	AppLinkFormat     MediaType = 40    // application/link-format
	AppXML            MediaType = 41    // application/xml
	AppOctets         MediaType = 42    // application/octet-stream
	AppExi            MediaType = 47    // application/exi
	AppJSON           MediaType = 50    // application/json
	AppJsonPatch      MediaType = 51    //application/json-patch+json (RFC6902)
	AppJsonMergePatch MediaType = 52    //application/merge-patch+json (RFC7396)
	AppCBOR           MediaType = 60    //application/cbor (RFC 7049)
	AppCWT            MediaType = 61    //application/cwt
	AppCoseEncrypt    MediaType = 96    //application/cose; cose-type="cose-encrypt" (RFC 8152)
	AppCoseMac        MediaType = 97    //application/cose; cose-type="cose-mac" (RFC 8152)
	AppCoseSign       MediaType = 98    //application/cose; cose-type="cose-sign" (RFC 8152)
	AppCoseKey        MediaType = 101   //application/cose-key (RFC 8152)
	AppCoseKeySet     MediaType = 102   //application/cose-key-set (RFC 8152)
	AppCoapGroup      MediaType = 256   //coap-group+json (RFC 7390)
	AppOcfCbor        MediaType = 10000 //application/vnd.ocf+cbor
	AppLwm2mTLV       MediaType = 11542 //application/vnd.oma.lwm2m+tlv
	AppLwm2mJSON      MediaType = 11543 //application/vnd.oma.lwm2m+json
)

func (c MediaType) String() string {
	switch c {
	case TextPlain:
		return "text/plain;charset=utf-8"
	case AppCoseEncrypt0:
		return "application/cose; cose-type=\"cose-encrypt0\" (RFC 8152)"
	case AppCoseMac0:
		return "application/cose; cose-type=\"cose-mac0\" (RFC 8152)"
	case AppCoseSign1:
		return "application/cose; cose-type=\"cose-sign1\" (RFC 8152)"
	case AppLinkFormat:
		return "application/link-format"
	case AppXML:
		return "application/xml"
	case AppOctets:
		return "application/octet-stream"
	case AppExi:
		return "application/exi"
	case AppJSON:
		return "application/json"
	case AppJsonPatch:
		return "application/json-patch+json (RFC6902)"
	case AppJsonMergePatch:
		return "application/merge-patch+json (RFC7396)"
	case AppCBOR:
		return "application/cbor (RFC 7049)"
	case AppCWT:
		return "application/cwt"
	case AppCoseEncrypt:
		return "application/cose; cose-type=\"cose-encrypt\" (RFC 8152)"
	case AppCoseMac:
		return "application/cose; cose-type=\"cose-mac\" (RFC 8152)"
	case AppCoseSign:
		return "application/cose; cose-type=\"cose-sign\" (RFC 8152)"
	case AppCoseKey:
		return "application/cose-key (RFC 8152)"
	case AppCoseKeySet:
		return "application/cose-key-set (RFC 8152)"
	case AppCoapGroup:
		return "coap-group+json (RFC 7390)"
	case AppOcfCbor:
		return "application/vnd.ocf+cbor"
	case AppLwm2mTLV:
		return "application/vnd.oma.lwm2m+tlv"
	case AppLwm2mJSON:
		return "application/vnd.oma.lwm2m+json"
	}
	return "Unknown media type: 0x" + strconv.FormatInt(int64(c), 16)
}

func extendOpt(opt int) (int, int) {
	ext := 0
	if opt >= ExtendOptionByteAddend {
		if opt >= ExtendOptionWordAddend {
			ext = opt - ExtendOptionWordAddend
			opt = ExtendOptionWordCode
		} else {
			ext = opt - ExtendOptionByteAddend
			opt = ExtendOptionByteCode
		}
	}
	return opt, ext
}

func marshalOptionHeaderExt(buf []byte, opt, ext int) (int, ErrorCode) {
	switch opt {
	case ExtendOptionByteCode:
		if buf != nil && len(buf) > 0 {
			buf[0] = byte(ext)
			return 1, OK
		}
		return 1, ErrorCodeTooSmall
	case ExtendOptionWordCode:
		if buf != nil && len(buf) > 1 {
			binary.BigEndian.PutUint16(buf, uint16(ext))
			return 2, OK
		}
		return 2, ErrorCodeTooSmall
	}
	return 0, OK
}

func marshalOptionHeader(buf []byte, delta, length int) (int, ErrorCode) {
	size := 0

	d, dx := extendOpt(delta)
	l, lx := extendOpt(length)

	if buf != nil && len(buf) > 0 {
		buf[0] = byte(d<<4) | byte(l)
		size++
	} else {
		buf = nil
		size++
	}
	var lenBuf int
	var err ErrorCode
	if buf == nil {
		lenBuf, err = marshalOptionHeaderExt(nil, d, dx)
	} else {
		lenBuf, err = marshalOptionHeaderExt(buf[size:], d, dx)
	}

	switch err {
	case OK:
	case ErrorCodeTooSmall:
		buf = nil
	default:
		return -1, err
	}
	size += lenBuf

	if buf == nil {
		lenBuf, err = marshalOptionHeaderExt(nil, l, lx)
	} else {
		lenBuf, err = marshalOptionHeaderExt(buf[size:], l, lx)
	}
	switch err {
	case OK:
	case ErrorCodeTooSmall:
		buf = nil
	default:
		return -1, err
	}
	size += lenBuf
	if buf == nil {
		return size, ErrorCodeTooSmall
	}
	return size, OK
}

type Option struct {
	ID    OptionID
	Value []byte
}

func (o Option) MarshalValue(buf []byte) (int, ErrorCode) {
	if len(buf) < len(o.Value) {
		return len(o.Value), ErrorCodeTooSmall
	}
	copy(buf, o.Value)
	return len(o.Value), OK
}

func (o *Option) UnmarshalValue(buf []byte) (int, ErrorCode) {
	o.Value = buf
	return len(buf), OK
}

func (o Option) Marshal(buf []byte, previousID OptionID) (int, ErrorCode) {
	/*
	     0   1   2   3   4   5   6   7
	   +---------------+---------------+
	   |               |               |
	   |  Option Delta | Option Length |   1 byte
	   |               |               |
	   +---------------+---------------+
	   \                               \
	   /         Option Delta          /   0-2 bytes
	   \          (extended)           \
	   +-------------------------------+
	   \                               \
	   /         Option Length         /   0-2 bytes
	   \          (extended)           \
	   +-------------------------------+
	   \                               \
	   /                               /
	   \                               \
	   /         Option Value          /   0 or more bytes
	   \                               \
	   /                               /
	   \                               \
	   +-------------------------------+
	*/
	delta := int(o.ID) - int(previousID)

	lenBuf, err := o.MarshalValue(nil)
	switch err {
	case ErrorCodeTooSmall, OK:
	default:
		return -1, err
	}

	//header marshal
	lenBuf, err = marshalOptionHeader(buf, delta, lenBuf)
	switch err {
	case OK:
	case ErrorCodeTooSmall:
		buf = nil
	default:
		return -1, err
	}
	length := lenBuf

	if buf == nil {
		lenBuf, err = o.MarshalValue(nil)
	} else {
		lenBuf, err = o.MarshalValue(buf[length:])
	}

	switch err {
	case OK:
	case ErrorCodeTooSmall:
		buf = nil
	default:
		return -1, err
	}
	length = length + lenBuf

	if buf == nil {
		return length, ErrorCodeTooSmall
	}
	return length, OK
}

func parseExtOpt(data []byte, opt int) (int, int, ErrorCode) {
	processed := 0
	switch opt {
	case ExtendOptionByteCode:
		if len(data) < 1 {
			return 0, -1, ErrorCodeOptionTruncated
		}
		opt = int(data[0]) + ExtendOptionByteAddend
		processed = 1
	case ExtendOptionWordCode:
		if len(data) < 2 {
			return 0, -1, ErrorCodeOptionTruncated
		}
		opt = int(binary.BigEndian.Uint16(data[:2])) + ExtendOptionWordAddend
		processed = 2
	}
	return processed, opt, OK
}

func (o *Option) Unmarshal(data []byte, optionDefs map[OptionID]OptionDef, OptionID OptionID) (int, ErrorCode) {
	if def, ok := optionDefs[OptionID]; ok {
		if def.ValueFormat == ValueUnknown {
			// Skip unrecognized options (RFC7252 section 5.4.1)
			return 0, OK
		}
		if len(data) < def.MinLen || len(data) > def.MaxLen {
			// Skip options with illegal value length (RFC7252 section 5.4.3)
			return 0, OK
		}
		o.ID = OptionID
		proc, err := o.UnmarshalValue(data)
		if err != OK {
			return -1, err
		}
		return proc, err
	}
	// Skip unrecognized options (should never be reached)
	return 0, OK
}
