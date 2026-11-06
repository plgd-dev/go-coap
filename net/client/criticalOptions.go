package client

import (
	"strconv"
	"strings"

	"github.com/plgd-dev/go-coap/v3/message"
)

// GetUnknownCriticalOptions returns unique unknown critical option IDs in option-order.
// Known critical options with illegal value length are treated as unknown (RFC7252 section 5.4.3).
func GetUnknownCriticalOptions(options message.Options) []message.OptionID {
	return GetUnknownCriticalOptionsWithQBlock(options, false)
}

// GetUnknownCriticalOptionsWithQBlock permits Q options only when the caller
// has an active datagram Q-Block receiver. TCP and legacy callers reject them.
func GetUnknownCriticalOptionsWithQBlock(options message.Options, enabled bool) []message.OptionID {
	unknownCriticalOpts := make([]message.OptionID, 0, 4)
	var lastUnknownCriticalOpt message.OptionID
	for _, opt := range options {
		if opt.ID%2 == 0 {
			continue
		}
		isDisabledQ := !enabled && (opt.ID == message.QBlock1 || opt.ID == message.QBlock2)
		if _, ok := message.CoapOptionDefs[opt.ID]; ok && !isDisabledQ && message.VerifyOptLen(opt.ID, len(opt.Value)) {
			continue
		}
		if len(unknownCriticalOpts) > 0 && opt.ID == lastUnknownCriticalOpt {
			continue
		}
		unknownCriticalOpts = append(unknownCriticalOpts, opt.ID)
		lastUnknownCriticalOpt = opt.ID
	}
	return unknownCriticalOpts
}

func FormatUnknownCriticalOptionsDiagnostic(unknownCriticalOpts []message.OptionID) string {
	parts := make([]string, 0, len(unknownCriticalOpts))
	for _, opt := range unknownCriticalOpts {
		parts = append(parts, strconv.FormatUint(uint64(opt), 10))
	}
	return "unknown critical option(s): " + strings.Join(parts, ",")
}
