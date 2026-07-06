package client

import (
	"strconv"
	"strings"

	"github.com/plgd-dev/go-coap/v3/message"
)

// GetUnknownCriticalOptions returns unique unknown critical option IDs in option-order.
func GetUnknownCriticalOptions(options message.Options) []message.OptionID {
	unknownCriticalOpts := make([]message.OptionID, 0, 4)
	var lastUnknownCriticalOpt message.OptionID
	for _, opt := range options {
		if opt.ID%2 == 0 {
			continue
		}
		if _, ok := message.CoapOptionDefs[opt.ID]; ok {
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
