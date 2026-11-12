// Package oscore contains transport-independent RFC 8613 security-context and
// message-protection support.
package oscore

import (
	"bytes"
	"crypto/aes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/pion/dtls/v3/pkg/crypto/ccm"
	"golang.org/x/crypto/hkdf"
)

const (
	aeadNonceSize     = 13
	maxPartialIV      = uint64(1<<40 - 1)
	maxIdentifierSize = 7
)

// AEADAlgorithm identifies a COSE AEAD algorithm supported by OSCORE.
type AEADAlgorithm uint64

const (
	// AEADAlgorithmAESCCM16_64_128 is AES-CCM with a 64-bit tag and 128-bit key.
	AEADAlgorithmAESCCM16_64_128 AEADAlgorithm = 10
	// AEADAlgorithmAESCCM16_64_256 is AES-CCM with a 64-bit tag and 256-bit key.
	AEADAlgorithmAESCCM16_64_256 AEADAlgorithm = 11
	// AEADAlgorithmAESCCM16_128_128 is AES-CCM with a 128-bit tag and 128-bit key.
	AEADAlgorithmAESCCM16_128_128 AEADAlgorithm = 30
	// AEADAlgorithmAESCCM16_128_256 is AES-CCM with a 128-bit tag and 256-bit key.
	AEADAlgorithmAESCCM16_128_256 AEADAlgorithm = 31
)

var (
	ErrInvalidContext          = errors.New("invalid OSCORE security context")
	ErrUnsupportedAlgorithm    = errors.New("unsupported OSCORE AEAD algorithm")
	ErrSenderSequenceExhausted = errors.New("OSCORE sender sequence number exhausted")
	ErrReplay                  = errors.New("OSCORE replay detected")
	ErrInvalidPartialIV        = errors.New("invalid OSCORE Partial IV")
	ErrPlaintextTooLarge       = errors.New("OSCORE plaintext exceeds AES-CCM limit")
)

// ContextConfig contains pre-established RFC 8613 security-context parameters.
// Set SenderSequenceNumber to the next sequence number to use, or provide a
// SenderSequenceStore for durable reservations across process restarts. Never
// reset the sequence number for an existing sender key; doing so can reuse an
// AEAD nonce.
type ContextConfig struct {
	// Algorithm is a COSE AEAD algorithm. Zero selects AES-CCM-16-64-128.
	Algorithm AEADAlgorithm
	// MasterSecret is high-entropy secret material provisioned securely to
	// both peers, not a human password. NewContext derives its keys immediately
	// and does not retain this slice.
	MasterSecret         []byte
	MasterSalt           []byte
	IDContext            []byte
	SenderID             []byte
	RecipientID          []byte
	SenderSequenceNumber uint64
	SenderSequenceStore  SenderSequenceReserver
	ReplayWindowSize     uint8
	ReplayWindowStore    ReplayWindowStore
}

// SenderSequenceReserver returns sender sequence numbers from durably reserved
// space. Implementations shared by multiple Context instances must coordinate
// reservations so that a number is never returned more than once. Values may
// be skipped, including after a restart. A returned number remains consumed
// even if message protection or transmission later fails.
type SenderSequenceReserver interface {
	Reserve() (uint64, error)
}

type noCopy struct{}

// Lock and Unlock are no-ops recognized by go vet's copylocks analyzer.
func (*noCopy) Lock() {
	// The marker prevents accidental copies; it is not a runtime mutex.
}

func (*noCopy) Unlock() {
	// The marker prevents accidental copies; it is not a runtime mutex.
}

type senderSequenceState struct {
	mu        sync.Mutex
	next      uint64
	store     SenderSequenceReserver
	exhausted bool
}

// Context holds the derived keys and mutable sender and recipient state for one
// pair of OSCORE endpoints. A Context must not be copied after first use.
type Context struct {
	noCopy                   noCopy
	commonIV                 []byte
	senderContextFingerprint [sha256.Size]byte
	senderSequence           *senderSequenceState
	senderID                 []byte
	recipientID              []byte
	idContext                []byte
	idContextSet             bool
	algorithm                AEADAlgorithm
	senderAEAD               ccm.CCM
	recipientAEAD            ccm.CCM
	replay                   *ReplayWindow
}

// NewContext derives keys for the configured AEAD algorithm. The zero-value
// Algorithm selects AES-CCM-16-64-128. Input byte slices are consumed during
// construction; identifier slices retained by the
// Context are copied. Go cannot guarantee complete zeroization of temporary
// secret copies.
func NewContext(cfg ContextConfig) (*Context, error) {
	cfg, err := validateContextConfig(cfg)
	if err != nil {
		return nil, err
	}

	var idContext []byte
	if cfg.IDContext != nil {
		idContext = append([]byte{}, cfg.IDContext...)
	}
	senderID := append([]byte{}, cfg.SenderID...)
	recipientID := append([]byte{}, cfg.RecipientID...)
	algorithm := algorithmParameters(cfg.Algorithm)

	senderKey, err := deriveKey(cfg.MasterSecret, cfg.MasterSalt, senderID, idContext, "Key", algorithm.keySize, cfg.Algorithm)
	if err != nil {
		return nil, err
	}
	recipientKey, err := deriveKey(cfg.MasterSecret, cfg.MasterSalt, recipientID, idContext, "Key", algorithm.keySize, cfg.Algorithm)
	if err != nil {
		return nil, err
	}
	commonIV, err := deriveKey(cfg.MasterSecret, cfg.MasterSalt, []byte{}, idContext, "IV", aeadNonceSize, cfg.Algorithm)
	if err != nil {
		return nil, err
	}
	fingerprintInput := make([]byte, 0, len(senderKey)+len(commonIV))
	fingerprintInput = append(fingerprintInput, senderKey...)
	fingerprintInput = append(fingerprintInput, commonIV...)
	senderContextFingerprint := sha256.Sum256(fingerprintInput)

	senderAEAD, err := newContextAEAD(senderKey, algorithm.tagSize, "sender")
	if err != nil {
		return nil, err
	}
	recipientAEAD, err := newContextAEAD(recipientKey, algorithm.tagSize, "recipient")
	if err != nil {
		return nil, err
	}

	replay, err := loadReplayWindow(cfg)
	if err != nil {
		return nil, err
	}
	return &Context{
		commonIV:                 commonIV,
		senderContextFingerprint: senderContextFingerprint,
		senderSequence: &senderSequenceState{
			next:  cfg.SenderSequenceNumber,
			store: cfg.SenderSequenceStore,
		},
		senderID:      senderID,
		recipientID:   recipientID,
		idContext:     idContext,
		idContextSet:  cfg.IDContext != nil,
		algorithm:     cfg.Algorithm,
		senderAEAD:    senderAEAD,
		recipientAEAD: recipientAEAD,
		replay:        replay,
	}, nil
}

func validateContextConfig(cfg ContextConfig) (ContextConfig, error) {
	if cfg.Algorithm == 0 {
		cfg.Algorithm = AEADAlgorithmAESCCM16_64_128
	}
	if algorithmParameters(cfg.Algorithm).keySize == 0 {
		return cfg, fmt.Errorf("%w: COSE algorithm %d", ErrUnsupportedAlgorithm, cfg.Algorithm)
	}
	if len(cfg.MasterSecret) == 0 {
		return cfg, fmt.Errorf("%w: master secret is empty", ErrInvalidContext)
	}
	if len(cfg.SenderID) > maxIdentifierSize || len(cfg.RecipientID) > maxIdentifierSize {
		return cfg, fmt.Errorf("%w: AES-CCM sender and recipient IDs must be at most %d bytes", ErrInvalidContext, maxIdentifierSize)
	}
	if bytes.Equal(cfg.SenderID, cfg.RecipientID) {
		return cfg, fmt.Errorf("%w: sender and recipient IDs must differ", ErrInvalidContext)
	}
	maxIdentifierLength := max(len(cfg.SenderID), len(cfg.RecipientID))
	maxIDContextSize := maxOSCOREOptionLength - 1 - 5 - 1 - maxIdentifierLength
	if len(cfg.IDContext) > maxIDContextSize {
		return cfg, fmt.Errorf("%w: ID Context exceeds %d bytes for the request OSCORE option", ErrInvalidContext, maxIDContextSize)
	}
	if cfg.SenderSequenceNumber > maxPartialIV {
		return cfg, fmt.Errorf("%w: initial sender sequence number exceeds 40 bits", ErrInvalidContext)
	}
	if cfg.SenderSequenceStore != nil && cfg.SenderSequenceNumber != 0 {
		return cfg, fmt.Errorf("%w: configure either a persistent sender sequence store or an initial sender sequence number", ErrInvalidContext)
	}
	if cfg.ReplayWindowSize == 0 {
		cfg.ReplayWindowSize = 32
	}
	if cfg.ReplayWindowSize > 64 {
		return cfg, fmt.Errorf("%w: replay window exceeds 64 entries", ErrInvalidReplayWindow)
	}
	return cfg, nil
}

type aeadParameters struct {
	keySize int
	tagSize int
}

func algorithmParameters(algorithm AEADAlgorithm) aeadParameters {
	switch algorithm {
	case AEADAlgorithmAESCCM16_64_128:
		return aeadParameters{keySize: 16, tagSize: 8}
	case AEADAlgorithmAESCCM16_64_256:
		return aeadParameters{keySize: 32, tagSize: 8}
	case AEADAlgorithmAESCCM16_128_128:
		return aeadParameters{keySize: 16, tagSize: 16}
	case AEADAlgorithmAESCCM16_128_256:
		return aeadParameters{keySize: 32, tagSize: 16}
	default:
		return aeadParameters{}
	}
}

func newContextAEAD(key []byte, tagSize int, endpoint string) (ccm.CCM, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create OSCORE %s cipher: %w", endpoint, err)
	}
	aead, err := ccm.NewCCM(block, tagSize, aeadNonceSize)
	if err != nil {
		return nil, fmt.Errorf("create OSCORE %s AEAD: %w", endpoint, err)
	}
	return aead, nil
}

func loadReplayWindow(cfg ContextConfig) (*ReplayWindow, error) {
	replayState := ReplayWindowState{}
	var err error
	if cfg.ReplayWindowStore != nil {
		replayState, err = cfg.ReplayWindowStore.Load()
		if err != nil {
			return nil, fmt.Errorf("load OSCORE replay state: %w", err)
		}
	}
	replay, err := newReplayWindow(cfg.ReplayWindowSize, replayState, cfg.ReplayWindowStore)
	if err != nil {
		return nil, err
	}
	return replay, nil
}

// SenderID returns a copy of the sender identifier.
func (c *Context) SenderID() []byte {
	return append([]byte(nil), c.senderID...)
}

// RecipientID returns a copy of the recipient identifier.
func (c *Context) RecipientID() []byte {
	return append([]byte(nil), c.recipientID...)
}

// IDContext returns a copy of the ID Context and whether one was configured.
// The boolean distinguishes an absent ID Context from a present empty one.
func (c *Context) IDContext() ([]byte, bool) {
	if !c.idContextSet {
		return nil, false
	}
	return append([]byte{}, c.idContext...), true
}

func deriveKey(secret, salt, id, idContext []byte, label string, length int, algorithm AEADAlgorithm) ([]byte, error) {
	info := make([]byte, 0, 32+len(id)+len(idContext))
	info = appendCBORHead(info, 4, 5)
	info = appendCBORBytes(info, id)
	if idContext == nil {
		info = append(info, 0xf6)
	} else {
		info = appendCBORBytes(info, idContext)
	}
	info = appendCBORHead(info, 0, uint64(algorithm))
	info = appendCBORHead(info, 3, uint64(len(label)))
	info = append(info, label...)
	info = appendCBORHead(info, 0, uint64(uint(length)))

	key := make([]byte, length)
	if _, err := io.ReadFull(hkdf.New(sha256.New, secret, salt, info), key); err != nil {
		return nil, fmt.Errorf("derive OSCORE %s: %w", label, err)
	}
	return key, nil
}

// Algorithm returns the COSE AEAD algorithm configured for this context.
func (c *Context) Algorithm() AEADAlgorithm {
	return c.algorithm
}

func appendCBORHead(dst []byte, majorType uint8, value uint64) []byte {
	prefix := majorType << 5
	switch {
	case value < 24:
		return append(dst, prefix|byte(value))
	case value <= 0xff:
		return append(dst, prefix|24, byte(value))
	case value <= 0xffff:
		return append(dst, prefix|25, byte(value>>8), byte(value&0xff))
	case value <= 0xffffffff:
		return append(dst, prefix|26, byte((value>>24)&0xff), byte((value>>16)&0xff), byte((value>>8)&0xff), byte(value&0xff))
	default:
		return append(dst, prefix|27,
			byte((value>>56)&0xff), byte((value>>48)&0xff), byte((value>>40)&0xff), byte((value>>32)&0xff),
			byte((value>>24)&0xff), byte((value>>16)&0xff), byte((value>>8)&0xff), byte(value&0xff))
	}
}

func appendCBORBytes(dst, value []byte) []byte {
	dst = appendCBORHead(dst, 2, uint64(len(value)))
	return append(dst, value...)
}

// NextPartialIV reserves the next sender sequence number and returns its
// minimal OSCORE Partial IV encoding. A reserved value remains consumed even if
// the caller later fails to send a message.
func (c *Context) NextPartialIV() ([]byte, error) {
	state := c.senderSequence
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.exhausted {
		return nil, ErrSenderSequenceExhausted
	}
	if state.store != nil {
		seq, err := state.store.Reserve()
		if err != nil {
			return nil, fmt.Errorf("reserve persistent OSCORE sender sequence number: %w", err)
		}
		if seq > maxPartialIV {
			state.exhausted = true
			return nil, ErrSenderSequenceExhausted
		}
		if seq == maxPartialIV {
			state.exhausted = true
		}
		return encodePartialIV(seq), nil
	}
	seq := state.next
	if seq == maxPartialIV {
		state.exhausted = true
	} else {
		state.next++
	}
	return encodePartialIV(seq), nil
}

func encodePartialIV(value uint64) []byte {
	var encoded [5]byte
	for i := len(encoded) - 1; i >= 0; i-- {
		encoded[i] = byte(value)
		value >>= 8
	}
	first := 0
	for first < len(encoded)-1 && encoded[first] == 0 {
		first++
	}
	return append([]byte(nil), encoded[first:]...)
}

func decodePartialIV(partialIV []byte) (uint64, error) {
	if len(partialIV) == 0 || len(partialIV) > 5 || (len(partialIV) > 1 && partialIV[0] == 0) {
		return 0, ErrInvalidPartialIV
	}
	var value uint64
	for _, b := range partialIV {
		value = value<<8 | uint64(b)
	}
	return value, nil
}

func (c *Context) nonce(partialIV, senderID []byte) ([]byte, error) {
	if _, err := decodePartialIV(partialIV); err != nil {
		return nil, err
	}
	if len(senderID) > maxIdentifierSize {
		return nil, fmt.Errorf("%w: sender ID exceeds %d bytes", ErrInvalidContext, maxIdentifierSize)
	}
	nonceInput := make([]byte, aeadNonceSize)
	nonceInput[0] = byte(len(senderID) & 0xff)
	copy(nonceInput[1+aeadNonceSize-6-len(senderID):1+aeadNonceSize-6], senderID)
	copy(nonceInput[aeadNonceSize-len(partialIV):], partialIV)
	for i := range nonceInput {
		nonceInput[i] ^= c.commonIV[i]
	}
	return nonceInput, nil
}

// AADBuilder builds the RFC 8613 COSE_Encrypt0 Enc_structure using the fresh
// Partial IV that Seal is about to use.
type AADBuilder func(partialIV []byte) ([]byte, error)

// Seal protects the serialized inner message with the sender context. The
// method reserves a fresh sequence number before building the AAD or encrypting.
// This low-level primitive does not enforce CoAP Exchange lifecycle; callers
// must preserve Partial IV uniqueness and use high-level message methods for
// exchange-bound messages.
func (c *Context) Seal(plaintext []byte, buildAAD AADBuilder) (partialIV, ciphertext []byte, err error) {
	return c.sealWithNonceID(plaintext, buildAAD, c.senderID)
}

func (c *Context) sealWithNonceID(plaintext []byte, buildAAD AADBuilder, nonceID []byte) (partialIV, ciphertext []byte, err error) {
	if buildAAD == nil {
		return nil, nil, fmt.Errorf("%w: missing AAD builder", ErrInvalidContext)
	}
	if len(plaintext) > c.senderAEAD.MaxLength() {
		return nil, nil, ErrPlaintextTooLarge
	}
	partialIV, err = c.NextPartialIV()
	if err != nil {
		return nil, nil, err
	}
	aad, err := buildAAD(append([]byte(nil), partialIV...))
	if err != nil {
		return nil, nil, err
	}
	nonce, err := c.nonce(partialIV, nonceID)
	if err != nil {
		return nil, nil, err
	}
	return partialIV, c.senderAEAD.Seal(nil, nonce, plaintext, aad), nil
}

// SealResponse protects a response with its own Partial IV, using the
// response sender ID for nonce construction. This low-level primitive does not
// track Exchange lifecycle or response ordering.
func (c *Context) SealResponse(plaintext []byte, buildAAD AADBuilder) (partialIV, ciphertext []byte, err error) {
	return c.sealWithNonceID(plaintext, buildAAD, c.senderID)
}

// SealResponseWithRequestNonce protects a non-notification response using the
// request Partial IV and request sender ID. No new sender sequence number is
// consumed and the response OSCORE option can be empty. This low-level
// primitive does not track Exchange lifecycle; repeated use with the same
// request Partial IV can reuse an AEAD nonce. Prefer ProtectResponse.
func (c *Context) SealResponseWithRequestNonce(plaintext, requestPartialIV []byte, buildAAD AADBuilder) ([]byte, error) {
	if buildAAD == nil {
		return nil, fmt.Errorf("%w: missing AAD builder", ErrInvalidContext)
	}
	if len(plaintext) > c.senderAEAD.MaxLength() {
		return nil, ErrPlaintextTooLarge
	}
	if _, err := decodePartialIV(requestPartialIV); err != nil {
		return nil, err
	}
	aad, err := buildAAD(append([]byte(nil), requestPartialIV...))
	if err != nil {
		return nil, err
	}
	nonce, err := c.nonce(requestPartialIV, c.recipientID)
	if err != nil {
		return nil, err
	}
	return c.senderAEAD.Seal(nil, nonce, plaintext, aad), nil
}

// Open authenticates and decrypts a serialized inner message. The recipient
// replay window is updated only after authentication succeeds. It does not
// bind the plaintext to a CoAP request or response Exchange.
func (c *Context) Open(partialIV, ciphertext, aad []byte) ([]byte, error) {
	return c.openWithNonceID(partialIV, ciphertext, aad, c.recipientID)
}

// OpenResponse authenticates a response with its own Partial IV, using the
// response sender ID for nonce construction. It does not update request replay
// state or enforce response replay/order; use UnprotectResponse for
// exchange-scoped response replay protection.
func (c *Context) OpenResponse(partialIV, ciphertext, aad []byte) ([]byte, error) {
	nonce, err := c.nonce(partialIV, c.recipientID)
	if err != nil {
		return nil, err
	}
	return c.recipientAEAD.Open(nil, nonce, ciphertext, aad)
}

// OpenResponseForExchange opens a response that is bound to an original
// request. Responses without a Partial IV use the request nonce and do not
// advance the recipient replay window. This primitive does not track exchange
// completion, enforce response replay/order, or authenticate the Exchange
// lifecycle; use UnprotectResponse with the Exchange returned by ProtectRequest
// or UnprotectRequest.
func (c *Context) OpenResponseForExchange(responsePartialIV, requestPartialIV, ciphertext, aad []byte) ([]byte, error) {
	if len(responsePartialIV) > 0 {
		nonce, err := c.nonce(responsePartialIV, c.recipientID)
		if err != nil {
			return nil, err
		}
		return c.recipientAEAD.Open(nil, nonce, ciphertext, aad)
	}
	if _, err := decodePartialIV(requestPartialIV); err != nil {
		return nil, err
	}
	nonce, err := c.nonce(requestPartialIV, c.senderID)
	if err != nil {
		return nil, err
	}
	return c.recipientAEAD.Open(nil, nonce, ciphertext, aad)
}

func (c *Context) openWithNonceID(partialIV, ciphertext, aad, nonceID []byte) ([]byte, error) {
	seq, err := decodePartialIV(partialIV)
	if err != nil {
		return nil, err
	}
	nonce, err := c.nonce(partialIV, nonceID)
	if err != nil {
		return nil, err
	}
	var plaintext []byte
	err = c.replay.Accept(seq, func() error {
		plaintext, err = c.recipientAEAD.Open(nil, nonce, ciphertext, aad)
		return err
	})
	if err != nil {
		return nil, err
	}
	return plaintext, nil
}
