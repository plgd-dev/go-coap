package client

import (
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"net"
)

func (cc *Conn) selectQBlock(req *pool.Message) (bool, error) {
	// Observation cancellation must remain ordinary even under Require.
	if value, err := req.GetOptionUint32(message.Observe); err == nil && value == 1 {
		return false, nil
	}
	if cc.qblockConfig == nil {
		return cc.qblockClient != nil && (cc.qblockClient.canPrepare(req) || cc.qblockClient.canPrepareQ1(req)), nil
	}
	eligible := (req.Code() == codes.GET && req.Body() == nil) || ((req.Code() == codes.POST || req.Code() == codes.PUT) && req.Body() != nil)
	for _, id := range []message.OptionID{message.Observe, message.Block1, message.Block2, message.QBlock1, message.QBlock2} {
		if req.HasOption(id) {
			eligible = false
		}
	}
	if control := req.ControlMessage(); control != nil && control.Dst.IsMulticast() {
		eligible = false
	}
	if peer, ok := cc.RemoteAddr().(*net.UDPAddr); ok && peer != nil && peer.IP.IsMulticast() {
		eligible = false
	}
	if !eligible {
		if cc.qblockConfig.Mode == qblock.Require {
			return false, qblock.ErrUnsupportedOperation
		}
		return false, nil
	}
	knowledge := cc.qblockCapabilityState()
	if knowledge == qblockCapabilitySupported {
		return true, nil
	}
	if cc.qblockConfig.Mode == qblock.Require {
		if knowledge == qblockCapabilityUnsupported {
			return false, qblock.ErrPeerUnsupported
		}
		return false, qblock.ErrCapabilityUnknown
	}
	return false, nil
}
