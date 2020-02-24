package coap

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"reflect"
	"strconv"
	"strings"

	"github.com/go-ocf/go-coap/codes"
)

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

var typeNames = [256]string{
	Confirmable:     "Confirmable",
	NonConfirmable:  "NonConfirmable",
	Acknowledgement: "Acknowledgement",
	Reset:           "Reset",
}

const (
	max1ByteNumber = uint32(^uint8(0))
	max2ByteNumber = uint32(^uint16(0))
	max3ByteNumber = uint32(0xffffff)
)

func init() {
	for i := range typeNames {
		if typeNames[i] == "" {
			typeNames[i] = fmt.Sprintf("Unknown (0x%x)", i)
		}
	}
}

func (t COAPType) String() string {
	return typeNames[t]
}

// OptionID identifies an option in a message.
type OptionID uint16

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
	NoResponse    OptionID = 258
)

// Option value format (RFC7252 section 3.2)
type valueFormat uint8

const (
	valueUnknown valueFormat = iota
	valueEmpty
	valueOpaque
	valueUint
	valueString
)

type optionDef struct {
	valueFormat valueFormat
	minLen      int
	maxLen      int
}

var coapOptionDefs = map[OptionID]optionDef{
	IfMatch:       optionDef{valueFormat: valueOpaque, minLen: 0, maxLen: 8},
	URIHost:       optionDef{valueFormat: valueString, minLen: 1, maxLen: 255},
	ETag:          optionDef{valueFormat: valueOpaque, minLen: 1, maxLen: 8},
	IfNoneMatch:   optionDef{valueFormat: valueEmpty, minLen: 0, maxLen: 0},
	Observe:       optionDef{valueFormat: valueUint, minLen: 0, maxLen: 3},
	URIPort:       optionDef{valueFormat: valueUint, minLen: 0, maxLen: 2},
	LocationPath:  optionDef{valueFormat: valueString, minLen: 0, maxLen: 255},
	URIPath:       optionDef{valueFormat: valueString, minLen: 0, maxLen: 255},
	ContentFormat: optionDef{valueFormat: valueUint, minLen: 0, maxLen: 2},
	MaxAge:        optionDef{valueFormat: valueUint, minLen: 0, maxLen: 4},
	URIQuery:      optionDef{valueFormat: valueString, minLen: 0, maxLen: 255},
	Accept:        optionDef{valueFormat: valueUint, minLen: 0, maxLen: 2},
	LocationQuery: optionDef{valueFormat: valueString, minLen: 0, maxLen: 255},
	Block2:        optionDef{valueFormat: valueUint, minLen: 0, maxLen: 3},
	Block1:        optionDef{valueFormat: valueUint, minLen: 0, maxLen: 3},
	Size2:         optionDef{valueFormat: valueUint, minLen: 0, maxLen: 4},
	ProxyURI:      optionDef{valueFormat: valueString, minLen: 1, maxLen: 1034},
	ProxyScheme:   optionDef{valueFormat: valueString, minLen: 1, maxLen: 255},
	Size1:         optionDef{valueFormat: valueUint, minLen: 0, maxLen: 4},
	NoResponse:    optionDef{valueFormat: valueUint, minLen: 0, maxLen: 1},
}

func (o OptionID) String() string {
	switch o {
	case IfMatch:
		return "IfMatch"
	case URIHost:
		return "URIHost"
	case ETag:
		return "ETag"
	case IfNoneMatch:
		return "IfNoneMatch"
	case Observe:
		return "Observe"
	case URIPort:
		return "URIPort"
	case LocationPath:
		return "LocationPath"
	case URIPath:
		return "URIPath"
	case ContentFormat:
		return "ContentFormat"
	case MaxAge:
		return "MaxAge"
	case URIQuery:
		return "URIQuery"
	case Accept:
		return "Accept"
	case LocationQuery:
		return "LocationQuery"
	case Block2:
		return "Block2"
	case Block1:
		return "Block1"
	case Size2:
		return "Size2"
	case ProxyURI:
		return "ProxyURI"
	case ProxyScheme:
		return "ProxyScheme"
	case Size1:
		return "Size1"
	case NoResponse:
		return "NoResponse"
	default:
		return "Option(" + strconv.FormatInt(int64(o), 10) + ")"
	}
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
	AppSenmlJSON      MediaType = 110   //application/senml+json
	AppSenmlCbor      MediaType = 112   //application/senml+cbor
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
		return "application/cose; cose-type=\"cose-encrypt0\"" // (RFC 8152)
	case AppCoseMac0:
		return "application/cose; cose-type=\"cose-mac0\"" // (RFC 8152)
	case AppCoseSign1:
		return "application/cose; cose-type=\"cose-sign1\"" // (RFC 8152)
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
		return "application/json-patch+json" // (RFC6902)
	case AppJsonMergePatch:
		return "application/merge-patch+json" // (RFC7396)
	case AppCBOR:
		return "application/cbor" // (RFC 7049)
	case AppCWT:
		return "application/cwt"
	case AppCoseEncrypt:
		return "application/cose; cose-type=\"cose-encrypt\"" // (RFC 8152)
	case AppCoseMac:
		return "application/cose; cose-type=\"cose-mac\"" // (RFC 8152)
	case AppCoseSign:
		return "application/cose; cose-type=\"cose-sign\"" // (RFC 8152)
	case AppCoseKey:
		return "application/cose-key" // (RFC 8152)
	case AppCoseKeySet:
		return "application/cose-key-set" // (RFC 8152)
	case AppCoapGroup:
		return "coap-group+json" // (RFC 7390)
	case AppSenmlJSON:
		return "application/senml+json"
	case AppSenmlCbor:
		return "application/senml+cbor"
	case AppOcfCbor:
		return "application/vnd.ocf+cbor"
	case AppLwm2mTLV:
		return "application/vnd.oma.lwm2m+tlv"
	case AppLwm2mJSON:
		return "application/vnd.oma.lwm2m+json"
	}
	return "Unknown media type: 0x" + strconv.FormatInt(int64(c), 16)
}

type option struct {
	ID    OptionID
	Value interface{}
}

func encodeInt(buf io.Writer, v uint32) error {
	switch {
	case v == 0:
	case v <= max1ByteNumber:
		buf.Write([]byte{byte(v)})
	case v <= max2ByteNumber:
		return binary.Write(buf, binary.BigEndian, uint16(v))
	case v <= max3ByteNumber:
		rv := []byte{0, 0, 0, 0}
		binary.BigEndian.PutUint32(rv, uint32(v))
		_, err := buf.Write(rv[1:])
		return err
	default:
		return binary.Write(buf, binary.BigEndian, uint32(v))
	}
	return nil
}

func lengthInt(v uint32) int {
	switch {
	case v == 0:
		return 0
	case v <= max1ByteNumber:
		return 1
	case v <= max2ByteNumber:
		return 2
	case v <= max3ByteNumber:
		return 3
	default:
		return 4
	}
}

func decodeInt(b []byte) uint32 {
	tmp := []byte{0, 0, 0, 0}
	copy(tmp[4-len(b):], b)
	return binary.BigEndian.Uint32(tmp)
}

func (o option) writeData(buf io.Writer) error {
	var v uint32

	switch i := o.Value.(type) {
	case string:
		_, err := buf.Write([]byte(i))
		return err
	case []byte:
		_, err := buf.Write(i)
		return err
	case MediaType:
		v = uint32(i)
	case int:
		v = uint32(i)
	case int32:
		v = uint32(i)
	case uint:
		v = uint32(i)
	case uint32:
		v = i
	default:
		return fmt.Errorf("invalid type for option %x: %T (%v)",
			o.ID, o.Value, o.Value)
	}

	return encodeInt(buf, v)
}

func (o option) toBytesLength() (int, error) {
	var v uint32

	switch i := o.Value.(type) {
	case string:
		return len(i), nil
	case []byte:
		return len(i), nil
	case MediaType:
		v = uint32(i)
	case int:
		v = uint32(i)
	case int32:
		v = uint32(i)
	case uint:
		v = uint32(i)
	case uint32:
		v = i
	default:
		return 0, fmt.Errorf("invalid type for option %x: %T (%v)",
			o.ID, o.Value, o.Value)
	}

	return lengthInt(v), nil
}

func parseOptionValue(optionDefs map[OptionID]optionDef, optionID OptionID, valueBuf []byte) interface{} {
	if def, ok := optionDefs[optionID]; ok {
		if def.valueFormat == valueUnknown {
			// Skip unrecognized options (RFC7252 section 5.4.1)
			return nil
		}
		if len(valueBuf) < def.minLen || len(valueBuf) > def.maxLen {
			// Skip options with illegal value length (RFC7252 section 5.4.3)
			return nil
		}
		switch def.valueFormat {
		case valueUint:
			intValue := decodeInt(valueBuf)
			if optionID == ContentFormat || optionID == Accept {
				return MediaType(intValue)
			}
			return intValue
		case valueString:
			return string(valueBuf)
		case valueOpaque, valueEmpty:
			return valueBuf
		}
	}
	// unKnown options
	return valueBuf
}

type options []option

func (o options) Len() int {
	return len(o)
}

func (o options) Less(i, j int) bool {
	if o[i].ID == o[j].ID {
		return i < j
	}
	return o[i].ID < o[j].ID
}

func (o options) Swap(i, j int) {
	o[i], o[j] = o[j], o[i]
}

func (o options) Remove(oid OptionID) options {
	idx := 0
	for i := 0; i < len(o); i++ {
		if o[i].ID != oid {
			o[idx] = o[i]
			idx++
		}
	}
	return o[:idx]
}

// Message represents the COAP message
type Message interface {
	Type() COAPType
	Code() codes.Code
	MessageID() uint16
	Token() []byte
	Payload() []byte
	AllOptions() options

	IsConfirmable() bool
	Options(o OptionID) []interface{}
	Option(o OptionID) interface{}
	optionStrings(o OptionID) []string
	Path() []string
	PathString() string
	SetPathString(s string)
	SetPath(s []string)
	SetType(typ COAPType)
	Query() []string
	QueryString() string
	SetQueryString(string)
	SetQuery([]string)
	SetURIQuery(s string)
	SetObserve(b uint32)
	SetPayload(p []byte)
	RemoveOption(opID OptionID)
	AddOption(opID OptionID, val interface{})
	SetOption(opID OptionID, val interface{})
	MarshalBinary(buf io.Writer) error
	UnmarshalBinary(data []byte) error
	SetToken(t []byte)
	SetMessageID(messageID uint16)
	ToBytesLength() (int, error)
	SetCode(code codes.Code)
}

// MessageParams params to create COAP message
type MessageParams struct {
	Type      COAPType
	Code      codes.Code
	MessageID uint16
	Token     []byte
	Payload   []byte
}

// MessageBase is a CoAP message.
type MessageBase struct {
	typ  COAPType
	code codes.Code

	token, payload []byte

	opts options
}

func (m *MessageBase) Type() COAPType {
	return m.typ
}

func (m *MessageBase) SetType(typ COAPType) {
	m.typ = typ
}

func (m *MessageBase) Code() codes.Code {
	return m.code
}

func (m *MessageBase) Token() []byte {
	return m.token
}

func (m *MessageBase) Payload() []byte {
	return m.payload
}

func (m *MessageBase) AllOptions() options {
	return m.opts
}

// IsConfirmable returns true if this message is confirmable.
func (m *MessageBase) IsConfirmable() bool {
	return m.typ == Confirmable
}

// Options gets all the values for the given option.
func (m *MessageBase) Options(o OptionID) []interface{} {
	var rv []interface{}

	for _, v := range m.opts {
		if o == v.ID {
			rv = append(rv, v.Value)
		}
	}

	return rv
}

// Option gets the first value for the given option ID.
func (m *MessageBase) Option(o OptionID) interface{} {
	for _, v := range m.opts {
		if o == v.ID {
			return v.Value
		}
	}
	return nil
}

func (m *MessageBase) optionStrings(o OptionID) []string {
	var rv []string
	for _, o := range m.Options(o) {
		rv = append(rv, o.(string))
	}
	return rv
}

// Path gets the Path set on this message if any.
func (m *MessageBase) Path() []string {
	return m.optionStrings(URIPath)
}

// PathString gets a path as a / separated string.
func (m *MessageBase) PathString() string {
	return strings.Join(m.Path(), "/")
}

// SetPathString sets a path by a / separated string.
func (m *MessageBase) SetPathString(s string) {
	switch s {
	case "", "/":
		//root path is not set as option
		return
	default:
		if s[0] == '/' {
			s = s[1:]
		}
		m.SetPath(strings.Split(s, "/"))
	}
}

// SetPath updates or adds a URIPath attribute on this message.
func (m *MessageBase) SetPath(s []string) {
	m.SetOption(URIPath, s)
}

// Query gets the Query set on this message if any.
func (m *MessageBase) Query() []string {
	return m.optionStrings(URIQuery)
}

// QueryString gets a path as an ampersand separated string.
func (m *MessageBase) QueryString() string {
	return strings.Join(m.Query(), "&")
}

// SetQueryString sets a query by an ampersand separated string.
func (m *MessageBase) SetQueryString(s string) {
	m.SetQuery(strings.Split(s, "&"))
}

// SetQuery updates or adds a URIQuery attribute on this message.
func (m *MessageBase) SetQuery(s []string) {
	m.SetOption(URIQuery, s)
}

// Set URIQuery attibute to the message
func (m *MessageBase) SetURIQuery(s string) {
	m.AddOption(URIQuery, s)
}

// Set Observer attribute to the message
func (m *MessageBase) SetObserve(b uint32) {
	m.SetOption(Observe, b)
}

// SetPayload
func (m *MessageBase) SetPayload(p []byte) {
	m.payload = p
}

// SetToken
func (m *MessageBase) SetToken(p []byte) {
	m.token = p
}

// RemoveOption removes all references to an option
func (m *MessageBase) RemoveOption(opID OptionID) {
	m.opts = m.opts.Remove(opID)
}

// AddOption adds an option.
func (m *MessageBase) AddOption(opID OptionID, val interface{}) {
	iv := reflect.ValueOf(val)
	if (iv.Kind() == reflect.Slice || iv.Kind() == reflect.Array) &&
		iv.Type().Elem().Kind() == reflect.String {
		for i := 0; i < iv.Len(); i++ {
			m.opts = append(m.opts, option{opID, iv.Index(i).Interface()})
		}
		return
	}
	m.opts = append(m.opts, option{opID, val})
}

// SetOption sets an option, discarding any previous value
func (m *MessageBase) SetOption(opID OptionID, val interface{}) {
	m.RemoveOption(opID)
	m.AddOption(opID, val)
}

// SetCode sets the coap code on the message
func (m *MessageBase) SetCode(code codes.Code) {
	m.code = code
}

const (
	extoptByteCode   = 13
	extoptByteAddend = 13
	extoptWordCode   = 14
	extoptWordAddend = 269
	extoptError      = 15
)

func writeOpt(o option, buf io.Writer, delta int) {
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

	   See parseExtOption(), extendOption()
	   and writeOptionHeader() below for implementation details
	*/

	writeOptHeader := func(delta, length int) {
		d, dx := extendOpt(delta)
		l, lx := extendOpt(length)

		buf.Write([]byte{byte(d<<4) | byte(l)})

		writeExt := func(opt, ext int) {
			switch opt {
			case extoptByteCode:
				buf.Write([]byte{byte(ext)})
			case extoptWordCode:
				binary.Write(buf, binary.BigEndian, uint16(ext))
			}
		}

		writeExt(d, dx)
		writeExt(l, lx)
	}

	len, err := o.toBytesLength()
	if err != nil {
		log.Fatal(err)
	} else {
		writeOptHeader(delta, len)
		o.writeData(buf)
	}
}

func writeOpts(buf io.Writer, opts options) {
	prev := 0
	for _, o := range opts {
		writeOpt(o, buf, int(o.ID)-prev)
		prev = int(o.ID)
	}
}

func extendOpt(opt int) (int, int) {
	ext := 0
	if opt >= extoptByteAddend {
		if opt >= extoptWordAddend {
			ext = opt - extoptWordAddend
			opt = extoptWordCode
		} else {
			ext = opt - extoptByteAddend
			opt = extoptByteCode
		}
	}
	return opt, ext
}

func lengthOptHeaderExt(opt, ext int) int {
	switch opt {
	case extoptByteCode:
		return 1
	case extoptWordCode:
		return 2
	}
	return 0
}

func lengthOptHeader(delta, length int) int {
	d, dx := extendOpt(delta)
	l, lx := extendOpt(length)

	//buf.Write([]byte{byte(d<<4) | byte(l)})
	res := 1

	res = res + lengthOptHeaderExt(d, dx)
	res = res + lengthOptHeaderExt(l, lx)
	return res
}

func lengthOpt(o option, delta int) int {
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

	   See parseExtOption(), extendOption()
	   and writeOptionHeader() below for implementation details
	*/

	res, err := o.toBytesLength()
	if err != nil {
		log.Fatal(err)
	} else {
		res = res + lengthOptHeader(delta, res)
	}
	return res
}

func bytesLengthOpts(opts options) int {
	length := 0
	prev := 0
	for _, o := range opts {
		length = length + lengthOpt(o, int(o.ID)-prev)
		prev = int(o.ID)
	}
	return length
}

// parseBody extracts the options and payload from a byte slice.  The supplied
// byte slice contains everything following the message header (everything
// after the token).
func parseBody(optionDefs map[OptionID]optionDef, data []byte) (options, []byte, error) {
	prev := 0

	parseExtOpt := func(opt int) (int, error) {
		switch opt {
		case extoptByteCode:
			if len(data) < 1 {
				return -1, ErrOptionTruncated
			}
			opt = int(data[0]) + extoptByteAddend
			data = data[1:]
		case extoptWordCode:
			if len(data) < 2 {
				return -1, ErrOptionTruncated
			}
			opt = int(binary.BigEndian.Uint16(data[:2])) + extoptWordAddend
			data = data[2:]
		}
		return opt, nil
	}

	var opts options

	for len(data) > 0 {
		if data[0] == 0xff {
			data = data[1:]
			break
		}

		delta := int(data[0] >> 4)
		length := int(data[0] & 0x0f)

		if delta == extoptError || length == extoptError {
			return nil, nil, ErrOptionUnexpectedExtendMarker
		}

		data = data[1:]

		delta, err := parseExtOpt(delta)
		if err != nil {
			return nil, nil, err
		}
		length, err = parseExtOpt(length)
		if err != nil {
			return nil, nil, err
		}

		if len(data) < length {
			return nil, nil, ErrMessageTruncated
		}

		oid := OptionID(prev + delta)
		opval := parseOptionValue(optionDefs, oid, data[:length])
		data = data[length:]
		prev = int(oid)

		if opval != nil {
			opt := option{ID: oid, Value: opval}
			opts = append(opts, opt)
		}
	}

	return opts, data, nil
}
