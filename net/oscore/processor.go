package oscore

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	pkgMath "github.com/plgd-dev/go-coap/v3/pkg/math"
)

var (
	ErrNoSecurityContext = errors.New("no matching OSCORE security context")
	ErrAmbiguousContext  = errors.New("multiple OSCORE security contexts are configured")
	ErrPlaintextDisabled = errors.New("unprotected CoAP is not permitted")
	ErrOSCORERequired    = errors.New("OSCORE protection is required")
)

type unprotectedProcessingErrorKey struct{}

// IsUnprotectedProcessingErrorCode reports whether RFC 8613 permits this
// response code for an OSCORE processing error without OSCORE protection.
func IsUnprotectedProcessingErrorCode(code codes.Code) bool {
	switch code {
	case codes.BadRequest, codes.Unauthorized, codes.BadOption:
		return true
	default:
		return false
	}
}

// ContextWithUnprotectedProcessingError marks a received response as an
// unprotected OSCORE processing error. The caller must pass a non-nil context.
func ContextWithUnprotectedProcessingError(ctx context.Context) context.Context {
	return context.WithValue(ctx, unprotectedProcessingErrorKey{}, true)
}

// IsUnprotectedProcessingError reports whether the response context was marked
// as an unprotected OSCORE processing error by a client transport.
func IsUnprotectedProcessingError(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	marked, _ := ctx.Value(unprotectedProcessingErrorKey{}).(bool)
	return marked
}

// OutgoingContextSelector chooses a security context for an outgoing request.
// The contexts slice is a snapshot of the processor's configured contexts.
// Return one of those contexts or an error; returning nil without an error is
// treated as ErrNoSecurityContext.
type OutgoingContextSelector func(request message.Message, contexts []*Context) (*Context, error)

// ProcessorConfig configures pre-established contexts and endpoint plaintext
// policy. A Processor is opt-in; applications that do not create one retain
// their existing CoAP behavior.
type ProcessorConfig struct {
	Contexts                []*Context
	OutgoingContextSelector OutgoingContextSelector
	AllowUnprotected        bool
	RequireOSCORE           bool
}

type contextKey struct {
	recipientID  string
	idContext    string
	idContextSet bool
}

// Processor selects contexts for incoming protected requests and applies the
// endpoint's plaintext policy. Responses remain explicitly bound to the
// context and Exchange returned by ProcessIncomingRequest.
type Processor struct {
	mu               sync.RWMutex
	contexts         map[contextKey]*Context
	senderContexts   map[[32]byte]struct{}
	outgoingContexts []*Context
	outgoingSelector OutgoingContextSelector
	allowUnprotected bool
	requireOSCORE    bool
}

func NewProcessor(cfg ProcessorConfig) (*Processor, error) {
	if cfg.AllowUnprotected && cfg.RequireOSCORE {
		return nil, fmt.Errorf("%w: plaintext cannot both be allowed and required to use OSCORE", ErrInvalidContext)
	}
	processor := &Processor{
		contexts:         make(map[contextKey]*Context, len(cfg.Contexts)),
		senderContexts:   make(map[[32]byte]struct{}, len(cfg.Contexts)),
		outgoingSelector: cfg.OutgoingContextSelector,
		allowUnprotected: cfg.AllowUnprotected,
		requireOSCORE:    cfg.RequireOSCORE,
	}
	for _, context := range cfg.Contexts {
		if context == nil {
			return nil, fmt.Errorf("%w: nil security context", ErrInvalidContext)
		}
		key := keyForContext(context)
		if _, exists := processor.contexts[key]; exists {
			return nil, fmt.Errorf("%w: duplicate recipient ID and ID Context", ErrInvalidContext)
		}
		if _, exists := processor.senderContexts[context.senderContextFingerprint]; exists {
			return nil, fmt.Errorf("%w: duplicate sender key and common IV", ErrInvalidContext)
		}
		processor.contexts[key] = context
		processor.senderContexts[context.senderContextFingerprint] = struct{}{}
		processor.outgoingContexts = append(processor.outgoingContexts, context)
	}
	if cfg.RequireOSCORE && len(cfg.Contexts) == 0 {
		return nil, fmt.Errorf("%w: required OSCORE has no security contexts", ErrInvalidContext)
	}
	return processor, nil
}

func keyForContext(context *Context) contextKey {
	idContext, idContextSet := context.IDContext()
	return contextKey{
		recipientID:  string(context.RecipientID()),
		idContext:    string(idContext),
		idContextSet: idContextSet,
	}
}

func keyForOption(option OptionValue) contextKey {
	return contextKey{
		recipientID:  string(option.SenderID),
		idContext:    string(option.IDContext),
		idContextSet: option.IDContextSet,
	}
}

// AddContext adds a bidirectional context to the processor. Its recipient ID
// and ID Context must be unique among configured peers.
func (p *Processor) AddContext(context *Context) error {
	if context == nil {
		return fmt.Errorf("%w: nil security context", ErrInvalidContext)
	}
	key := keyForContext(context)
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.contexts[key]; exists {
		return fmt.Errorf("%w: duplicate recipient ID and ID Context", ErrInvalidContext)
	}
	if _, exists := p.senderContexts[context.senderContextFingerprint]; exists {
		return fmt.Errorf("%w: duplicate sender key and common IV", ErrInvalidContext)
	}
	p.contexts[key] = context
	p.senderContexts[context.senderContextFingerprint] = struct{}{}
	p.outgoingContexts = append(p.outgoingContexts, context)
	return nil
}

func (p *Processor) contextForRequest(options message.Options) (*Context, error) {
	option, _, err := parseOSCOREOuter(options)
	if err != nil {
		return nil, err
	}
	if !option.SenderIDSet {
		return nil, fmt.Errorf("%w: request has no KID", ErrInvalidOSCOREMessage)
	}
	p.mu.RLock()
	context := p.contexts[keyForOption(option)]
	p.mu.RUnlock()
	if context == nil {
		return nil, ErrNoSecurityContext
	}
	return context, nil
}

// ProtectRequest protects an outgoing request using the sole configured
// context, or the configured OutgoingContextSelector when multiple contexts
// are available.
func (p *Processor) ProtectRequest(request message.Message) (message.Message, Exchange, error) {
	protected, _, exchange, err := p.ProtectRequestWithContext(request)
	return protected, exchange, err
}

// ProtectRequestWithContext protects an outgoing request and returns the
// context needed to open its response. Multiple configured contexts require
// an OutgoingContextSelector.
func (p *Processor) ProtectRequestWithContext(request message.Message) (message.Message, *Context, Exchange, error) {
	p.mu.RLock()
	contexts := append([]*Context(nil), p.outgoingContexts...)
	selector := p.outgoingSelector
	p.mu.RUnlock()
	if len(contexts) == 0 {
		return message.Message{}, nil, Exchange{}, ErrNoSecurityContext
	}
	var context *Context
	switch {
	case len(contexts) == 1:
		context = contexts[0]
	case selector == nil:
		return message.Message{}, nil, Exchange{}, ErrAmbiguousContext
	default:
		selectorRequest := request
		selectorRequest.Token = append(message.Token(nil), request.Token...)
		selectorRequest.Options = cloneOptions(request.Options)
		selectorRequest.Payload = append([]byte(nil), request.Payload...)
		selected, err := selector(selectorRequest, append([]*Context(nil), contexts...))
		if err != nil {
			return message.Message{}, nil, Exchange{}, fmt.Errorf("select outgoing OSCORE context: %w", err)
		}
		if selected == nil || !containsContext(contexts, selected) {
			return message.Message{}, nil, Exchange{}, ErrNoSecurityContext
		}
		context = selected
	}
	protected, exchange, err := context.ProtectRequest(request)
	if err != nil {
		return message.Message{}, nil, Exchange{}, err
	}
	return protected, context, exchange, nil
}

// MaxRequestOverhead returns a conservative bound for the additional wire size
// OSCORE adds to a request with the given number of options. It accounts for
// the largest configured request option (including ID Context and KID), the
// inner code, AEAD tag, and option delta growth after class splitting.
func (p *Processor) MaxRequestOverhead(optionCount int, observes bool) uint32 {
	p.mu.RLock()
	contexts := append([]*Context(nil), p.outgoingContexts...)
	p.mu.RUnlock()

	if optionCount < 0 {
		optionCount = 0
	}
	const maxUint32 = uint64(^uint32(0))
	optionCount64, err := pkgMath.SafeCastTo[uint64](optionCount)
	if err != nil {
		return ^uint32(0)
	}
	var maxOverhead uint32
	for _, context := range contexts {
		idContext, idContextSet := context.IDContext()
		optionValueSize := 1 + 5 + len(context.SenderID())
		if idContextSet {
			optionValueSize += 1 + len(idContext)
		}
		fixedOverhead, err := pkgMath.SafeCastTo[uint64](3 + optionValueSize + 1 + algorithmParameters(context.algorithm).tagSize)
		if err != nil {
			return ^uint32(0)
		}
		if observes {
			fixedOverhead += 5
		}
		if optionCount64 > (maxUint32-fixedOverhead)/2 {
			return ^uint32(0)
		}
		overhead := fixedOverhead + 2*optionCount64
		safeOverhead, err := pkgMath.SafeCastTo[uint32](overhead)
		if err != nil {
			return ^uint32(0)
		}
		maxOverhead = max(maxOverhead, safeOverhead)
	}
	return maxOverhead
}

func containsContext(contexts []*Context, selected *Context) bool {
	for _, context := range contexts {
		if context == selected {
			return true
		}
	}
	return false
}

// ProcessIncomingRequest returns plaintext requests unchanged only when the
// configured policy allows them. Once an OSCORE option is present, all parse,
// context, replay, and authentication failures are returned without fallback.
// For protected requests, the returned context and Exchange must be retained
// to protect any corresponding response.
func (p *Processor) ProcessIncomingRequest(request message.Message) (message.Message, *Context, Exchange, error) {
	if !request.Options.HasOption(message.OSCORE) {
		if p.requireOSCORE {
			return message.Message{}, nil, Exchange{}, ErrOSCORERequired
		}
		if !p.allowUnprotected {
			return message.Message{}, nil, Exchange{}, ErrPlaintextDisabled
		}
		return request, nil, Exchange{}, nil
	}
	context, err := p.contextForRequest(request.Options)
	if err != nil {
		return message.Message{}, nil, Exchange{}, err
	}
	plaintext, exchange, err := context.UnprotectRequest(request)
	if err != nil {
		return message.Message{}, nil, Exchange{}, err
	}
	return plaintext, context, exchange, nil
}

// UnprotectResponse decrypts a response using the context associated with its
// outstanding request.
func (p *Processor) UnprotectResponse(response message.Message, context *Context, exchange Exchange) (message.Message, error) {
	if context == nil || !p.containsContext(context) {
		return message.Message{}, ErrNoSecurityContext
	}
	return context.UnprotectResponse(response, exchange)
}

func (p *Processor) containsContext(context *Context) bool {
	key := keyForContext(context)
	p.mu.RLock()
	configured := p.contexts[key]
	p.mu.RUnlock()
	return configured == context
}
