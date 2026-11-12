package oscore

import "context"

type requestInfoContextKey struct{}

// RequestInfo describes the OSCORE identity of an incoming request. RecipientID
// is the request sender's identifier as seen by this endpoint. IDContextSet
// distinguishes an absent ID Context from a present empty one.
type RequestInfo struct {
	RecipientID  []byte
	IDContext    []byte
	IDContextSet bool
	Protected    bool
}

// ContextWithRequestInfo adds OSCORE request metadata to ctx. It is intended
// for transport adapters that have successfully processed an incoming request.
func ContextWithRequestInfo(ctx context.Context, info RequestInfo) context.Context {
	info.RecipientID = append([]byte(nil), info.RecipientID...)
	if info.IDContext != nil {
		info.IDContext = append([]byte{}, info.IDContext...)
	}
	return context.WithValue(ctx, requestInfoContextKey{}, info)
}

// RequestInfoFromContext returns the OSCORE request metadata attached by a
// transport adapter. The returned identifier slices are independent copies.
func RequestInfoFromContext(ctx context.Context) (RequestInfo, bool) {
	if ctx == nil {
		return RequestInfo{}, false
	}
	info, ok := ctx.Value(requestInfoContextKey{}).(RequestInfo)
	if !ok {
		return RequestInfo{}, false
	}
	info.RecipientID = append([]byte(nil), info.RecipientID...)
	if info.IDContext != nil {
		info.IDContext = append([]byte{}, info.IDContext...)
	}
	return info, true
}
