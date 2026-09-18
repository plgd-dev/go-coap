package client

import (
	"errors"
	"fmt"
	"io"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
)

func q1ControlFromResponse(msg *pool.Message, blockCount uint32) (qblock.Control, bool, error) {
	control := qblock.Control{Token: message.Token(append([]byte(nil), msg.Token()...))}
	switch msg.Code() {
	case codes.Continue:
		if msg.HasOption(message.QBlock2) {
			return control, true, errQBlockMixedResponseOptions
		}
		if err := qblock.ValidateOptions(msg.Options(), true); err != nil {
			return control, true, err
		}
		if qblockOptionCount(msg, message.QBlock1) != 1 {
			return control, true, errors.New("q-block continue response requires one QBlock1")
		}
		value, err := msg.GetOptionUint32(message.QBlock1)
		if err != nil {
			return control, true, err
		}
		block, err := qblock.DecodeBlock(value)
		if err != nil {
			return control, true, err
		}
		control.Continue = &block.Number
		return control, true, nil
	case codes.RequestEntityIncomplete:
		if msg.HasOption(message.QBlock1) || msg.HasOption(message.QBlock2) {
			return control, true, errQBlockMixedResponseOptions
		}
		if qblockOptionCount(msg, message.ContentFormat) != 1 {
			return control, true, errors.New("q-block missing response requires Content-Format")
		}
		contentFormat, err := msg.ContentFormat()
		if err != nil {
			return control, true, err
		}
		if contentFormat != message.AppMissingBlocksCBORSeq {
			return control, true, errors.New("q-block missing response requires missing-blocks+cbor-seq")
		}
		body := msg.Body()
		if body == nil {
			return control, true, errors.New("q-block missing response requires payload")
		}
		payload, err := io.ReadAll(body)
		if err != nil {
			return control, true, err
		}
		missing, err := qblock.DecodeMissing(payload, blockCount, int(blockCount))
		if err != nil {
			return control, true, err
		}
		control.Missing = missing
		return control, true, nil
	default:
		if msg.HasOption(message.QBlock1) || msg.HasOption(message.QBlock2) {
			return control, true, errors.New("unexpected q-block option in terminal response")
		}
		return control, false, nil
	}
}

// handleDisabledQBlock prevents unsupported fragments reaching application handlers.
// Responses are not requests and must never elicit a Bad Option response.
func (cc *Conn) handleDisabledQBlock(w *responsewriter.ResponseWriter[*Conn], req *pool.Message) bool {
	opts := req.Options()
	if !opts.HasOption(message.QBlock1) && !opts.HasOption(message.QBlock2) {
		return false
	}
	request := req.Code() >= codes.GET && req.Code() < codes.Code(32)
	// The enabled private Q-Block client validates and consumes Q2 responses.
	// Other Q responses remain unsupported and never elicit another response.
	if !request {
		return cc.qblockClient == nil || !opts.HasOption(message.QBlock2)
	}
	validationErr := qblock.ValidateOptions(opts, request)
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
