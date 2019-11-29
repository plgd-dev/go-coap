package coap

import (
	"context"

	"github.com/go-ocf/go-coap/codes"
)

var (
	resp2XXCodes = []codes.Code{codes.Created, codes.Deleted, codes.Valid, codes.Changed, codes.Content}
	resp4XXCodes = []codes.Code{codes.BadRequest, codes.Unauthorized, codes.BadOption, codes.Forbidden, codes.NotFound, codes.MethodNotAllowed, codes.NotAcceptable, codes.PreconditionFailed, codes.RequestEntityTooLarge, codes.UnsupportedMediaType}
	resp5XXCodes = []codes.Code{codes.InternalServerError, codes.NotImplemented, codes.BadGateway, codes.ServiceUnavailable, codes.GatewayTimeout, codes.ProxyingNotSupported}
)

func isSet(n uint32, pos uint32) bool {
	val := n & (1 << pos)
	return (val > 0)
}

func powerOfTwo(exponent uint32) uint32 {
	if exponent != 0 {
		return (2 * powerOfTwo(exponent-1))
	}
	return 1
}

func (w *noResponseWriter) decodeNoResponseOption(v uint32) []codes.Code {
	var codes []codes.Code
	if v == 0 {
		// No suppresed code
		return codes
	}

	var i uint32
	// Max bit value:4; ref:table_2_rfc7967
	for i = 0; i <= 4; i++ {
		if isSet(v, i) {
			index := powerOfTwo(i)
			codes = append(codes, w.noResponseValueMap[index]...)
		}
	}
	return codes
}

type noResponseWriter struct {
	ResponseWriter
	noResponseValueMap map[uint32][]codes.Code
}

func newNoResponseWriter(w ResponseWriter) *noResponseWriter {
	return &noResponseWriter{
		ResponseWriter: w,
		noResponseValueMap: map[uint32][]codes.Code{
			2:  resp2XXCodes,
			8:  resp4XXCodes,
			16: resp5XXCodes,
		},
	}
}

func (w *noResponseWriter) Write(p []byte) (n int, err error) {
	return w.WriteWithContext(context.Background(), p)
}

func (w *noResponseWriter) WriteWithContext(ctx context.Context, p []byte) (n int, err error) {
	l, resp := prepareReponse(w, w.ResponseWriter.getReq().Msg.Code(), w.ResponseWriter.getCode(), w.ResponseWriter.getContentFormat(), p)
	err = w.WriteMsgWithContext(ctx, resp)
	return l, err
}

func (w *noResponseWriter) SetCode(code codes.Code) {
	w.ResponseWriter.SetCode(code)
}

func (w *noResponseWriter) SetContentFormat(contentFormat MediaType) {
	w.ResponseWriter.SetContentFormat(contentFormat)
}

func (w *noResponseWriter) NewResponse(code codes.Code) Message {
	return w.ResponseWriter.NewResponse(code)
}

func (w *noResponseWriter) WriteMsg(msg Message) error {
	return w.WriteMsgWithContext(context.Background(), msg)
}

func (w *noResponseWriter) WriteMsgWithContext(ctx context.Context, msg Message) error {
	noRespValue, ok := w.ResponseWriter.getReq().Msg.Option(NoResponse).(uint32)
	if !ok {
		return ErrNotSupported
	}
	suppressedCodes := w.decodeNoResponseOption(noRespValue)

	for _, code := range suppressedCodes {
		if code == msg.Code() {
			return ErrMessageNotInterested
		}
	}
	return w.ResponseWriter.getReq().Client.WriteMsgWithContext(ctx, msg)
}
