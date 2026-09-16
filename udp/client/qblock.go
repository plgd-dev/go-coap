package client

import (
	"errors"
	"fmt"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
)

// handleDisabledQBlock prevents unsupported fragments reaching application handlers.
// Responses are not requests and must never elicit a Bad Option response.
func (cc *Conn) handleDisabledQBlock(w *responsewriter.ResponseWriter[*Conn], req *pool.Message) bool {
	opts := req.Options()
	if !opts.HasOption(message.QBlock1) && !opts.HasOption(message.QBlock2) {
		return false
	}
	request := req.Code() >= codes.GET && req.Code() < codes.Code(32)
	validationErr := qblock.ValidateOptions(opts, request)
	// Q responses cannot be processed until Q support is enabled. Drop them,
	// including malformed responses, without producing a response to a response.
	if !request {
		return true
	}
	if req.Type() != message.Confirmable && req.Type() != message.NonConfirmable {
		return true
	}
	if req.Type() == message.NonConfirmable && !errors.Is(validationErr, qblock.ErrMixedOptions) {
		return true
	}
	// Use direct message setters so No-Response cannot suppress a critical-option error.
	resp := w.Message()
	resp.SetCode(codes.BadOption)
	resp.SetToken(req.Token())
	if req.Type() == message.Confirmable {
		resp.SetType(message.Acknowledgement)
		resp.SetMessageID(req.MessageID())
	} else {
		resp.SetType(message.NonConfirmable)
		resp.SetMessageID(cc.GetMessageID())
	}
	// The existing cache indexes response MID, which matches request MID only
	// for ACKs. Do not cache fresh-MID NON errors under an unrelated request key.
	if req.Type() == message.Confirmable {
		if err := cc.addResponseToCache(resp); err != nil {
			cc.errors(fmt.Errorf("cannot cache Q-Block rejection: %w", err))
		}
	}
	return true
}
