package client

import (
	"fmt"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
)

// handleDisabledQBlock keeps unsupported fragments out of classic routing.
func (cc *Conn) handleDisabledQBlock(w *responsewriter.ResponseWriter[*Conn], req *pool.Message) bool {
	code := req.Code()
	if code == codes.Empty || code >= 32 {
		return false
	}
	var hasQ, mixed, classic bool
	for _, opt := range req.Options() {
		switch opt.ID {
		case message.QBlock1, message.QBlock2:
			hasQ = true
		case message.Block1, message.Block2:
			classic = true
		}
	}
	mixed = hasQ && classic
	invalid := qblock.ValidateOptions(req.Options(), true) != nil
	if !hasQ && !invalid {
		return false
	}
	if req.Type() == message.NonConfirmable && !mixed {
		w.Message().SetModified(false)
		return true
	}

	w.Message().SetCode(codes.BadOption)
	w.Message().ResetOptionsTo(nil)
	if req.Type() == message.Confirmable {
		w.Message().SetType(message.Acknowledgement)
		w.Message().SetMessageID(req.MessageID())
	} else {
		w.Message().SetType(message.NonConfirmable)
		w.Message().SetMessageID(cc.GetMessageID())
	}
	if err := cc.addResponseToCache(w.Message()); err != nil {
		cc.closeConnection()
		cc.errors(fmt.Errorf("cannot cache disabled Q-Block response: %w", err))
	}
	return true
}
