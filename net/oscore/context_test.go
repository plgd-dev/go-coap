package oscore

import (
	"bytes"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
)

func mustHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestRFC8613AppendixC2DerivationAndNonce(t *testing.T) {
	ctx, err := NewContext(ContextConfig{
		MasterSecret: mustHex(t, "0102030405060708090a0b0c0d0e0f10"),
		SenderID:     mustHex(t, "00"),
		RecipientID:  mustHex(t, "01"),
	})
	if err != nil {
		t.Fatal(err)
	}
	senderKey, err := deriveKey(
		mustHex(t, "0102030405060708090a0b0c0d0e0f10"),
		nil, mustHex(t, "00"), nil, "Key", 16, AEADAlgorithmAESCCM16_64_128,
	)
	if err != nil {
		t.Fatal(err)
	}
	recipientKey, err := deriveKey(
		mustHex(t, "0102030405060708090a0b0c0d0e0f10"),
		nil, mustHex(t, "01"), nil, "Key", 16, AEADAlgorithmAESCCM16_64_128,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(senderKey, mustHex(t, "321b26943253c7ffb6003b0b64d74041")) {
		t.Fatalf("sender key = %x", senderKey)
	}
	if !bytes.Equal(recipientKey, mustHex(t, "e57b5635815177cd679ab4bcec9d7dda")) {
		t.Fatalf("recipient key = %x", recipientKey)
	}
	if !bytes.Equal(ctx.commonIV, mustHex(t, "be35ae297d2dace910c52e99f9")) {
		t.Fatalf("common IV = %x", ctx.commonIV)
	}

	piv := []byte{0}
	senderNonce, err := ctx.nonce(piv, ctx.senderID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(senderNonce, mustHex(t, "bf35ae297d2dace910c52e99f9")) {
		t.Fatalf("sender nonce = %x", senderNonce)
	}
	recipientNonce, err := ctx.nonce(piv, ctx.recipientID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(recipientNonce, mustHex(t, "bf35ae297d2dace810c52e99f9")) {
		t.Fatalf("recipient nonce = %x", recipientNonce)
	}
}

func TestRFC8613AppendixC1DerivationWithMasterSalt(t *testing.T) {
	ctx, err := NewContext(ContextConfig{
		MasterSecret: mustHex(t, "0102030405060708090a0b0c0d0e0f10"),
		MasterSalt:   mustHex(t, "9e7ca92223786340"),
		SenderID:     []byte{},
		RecipientID:  mustHex(t, "01"),
	})
	if err != nil {
		t.Fatal(err)
	}
	senderKey, err := deriveKey(
		mustHex(t, "0102030405060708090a0b0c0d0e0f10"),
		mustHex(t, "9e7ca92223786340"), []byte{}, nil, "Key", 16, AEADAlgorithmAESCCM16_64_128,
	)
	if err != nil {
		t.Fatal(err)
	}
	recipientKey, err := deriveKey(
		mustHex(t, "0102030405060708090a0b0c0d0e0f10"),
		mustHex(t, "9e7ca92223786340"), mustHex(t, "01"), nil, "Key", 16, AEADAlgorithmAESCCM16_64_128,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(senderKey, mustHex(t, "f0910ed7295e6ad4b54fc793154302ff")) {
		t.Fatalf("sender key = %x", senderKey)
	}
	if !bytes.Equal(recipientKey, mustHex(t, "ffb14e093c94c9cac9471648b4f98710")) {
		t.Fatalf("recipient key = %x", recipientKey)
	}
	if !bytes.Equal(ctx.commonIV, mustHex(t, "4622d4dd6d944168eefb54987c")) {
		t.Fatalf("common IV = %x", ctx.commonIV)
	}
}

func TestRFC8613AppendixC3DerivationWithIDContext(t *testing.T) {
	ctx, err := NewContext(ContextConfig{
		MasterSecret: mustHex(t, "0102030405060708090a0b0c0d0e0f10"),
		MasterSalt:   mustHex(t, "9e7ca92223786340"),
		IDContext:    mustHex(t, "37cbf3210017a2d3"),
		SenderID:     []byte{},
		RecipientID:  mustHex(t, "01"),
	})
	if err != nil {
		t.Fatal(err)
	}

	senderKey, err := deriveKey(
		mustHex(t, "0102030405060708090a0b0c0d0e0f10"),
		mustHex(t, "9e7ca92223786340"), []byte{},
		mustHex(t, "37cbf3210017a2d3"), "Key", 16, AEADAlgorithmAESCCM16_64_128,
	)
	if err != nil {
		t.Fatal(err)
	}
	recipientKey, err := deriveKey(
		mustHex(t, "0102030405060708090a0b0c0d0e0f10"),
		mustHex(t, "9e7ca92223786340"), mustHex(t, "01"),
		mustHex(t, "37cbf3210017a2d3"), "Key", 16, AEADAlgorithmAESCCM16_64_128,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(senderKey, mustHex(t, "af2a1300a5e95788b356336eeecd2b92")) {
		t.Fatalf("sender key = %x", senderKey)
	}
	if !bytes.Equal(recipientKey, mustHex(t, "e39a0c7c77b43f03b4b39ab9a268699f")) {
		t.Fatalf("recipient key = %x", recipientKey)
	}
	if !bytes.Equal(ctx.commonIV, mustHex(t, "2ca58fb85ff1b81c0b7181b85e")) {
		t.Fatalf("common IV = %x", ctx.commonIV)
	}
}

func TestSupportedAEADAlgorithms(t *testing.T) {
	tests := []struct {
		name    string
		id      AEADAlgorithm
		keySize int
		tagSize int
	}{
		{"AES-CCM-16-64-128", AEADAlgorithmAESCCM16_64_128, 16, 8},
		{"AES-CCM-16-64-256", AEADAlgorithmAESCCM16_64_256, 32, 8},
		{"AES-CCM-16-128-128", AEADAlgorithmAESCCM16_128_128, 16, 16},
		{"AES-CCM-16-128-256", AEADAlgorithmAESCCM16_128_256, 32, 16},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secret := mustHex(t, "0102030405060708090a0b0c0d0e0f10")
			client, err := NewContext(ContextConfig{
				Algorithm:    tt.id,
				MasterSecret: secret,
				SenderID:     []byte{0},
				RecipientID:  []byte{1},
			})
			if err != nil {
				t.Fatal(err)
			}
			server, err := NewContext(ContextConfig{
				Algorithm:    tt.id,
				MasterSecret: secret,
				SenderID:     []byte{1},
				RecipientID:  []byte{0},
			})
			if err != nil {
				t.Fatal(err)
			}
			if client.Algorithm() != tt.id || client.senderAEAD.Overhead() != tt.tagSize {
				t.Fatalf("algorithm parameters = %d/%d", client.Algorithm(), client.senderAEAD.Overhead())
			}
			derivedKey, err := deriveKey(secret, nil, []byte{0}, nil, "Key", tt.keySize, tt.id)
			if err != nil {
				t.Fatal(err)
			}
			if len(derivedKey) != tt.keySize {
				t.Fatalf("derived key length = %d, want %d", len(derivedKey), tt.keySize)
			}

			aadBuilder := func(partialIV []byte) ([]byte, error) {
				return BuildAADWithAlgorithm(tt.id, []byte{0}, partialIV, nil)
			}
			piv, ciphertext, err := client.Seal([]byte("OSCORE algorithm test"), aadBuilder)
			if err != nil {
				t.Fatal(err)
			}
			aad, err := BuildAADWithAlgorithm(tt.id, []byte{0}, piv, nil)
			if err != nil {
				t.Fatal(err)
			}
			opened, err := server.Open(piv, ciphertext, aad)
			if err != nil {
				t.Fatal(err)
			}
			if string(opened) != "OSCORE algorithm test" {
				t.Fatalf("opened plaintext = %q", opened)
			}
		})
	}
}

func TestNewContextRejectsUnsupportedAEADAlgorithm(t *testing.T) {
	_, err := NewContext(ContextConfig{
		Algorithm:    AEADAlgorithm(999),
		MasterSecret: []byte{1},
	})
	if !errors.Is(err, ErrUnsupportedAlgorithm) {
		t.Fatalf("unsupported algorithm error = %v", err)
	}
}

func TestContextSealOpenAndReplayProtection(t *testing.T) {
	client, err := NewContext(ContextConfig{
		MasterSecret: mustHex(t, "0102030405060708090a0b0c0d0e0f10"),
		SenderID:     mustHex(t, "00"),
		RecipientID:  mustHex(t, "01"),
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewContext(ContextConfig{
		MasterSecret: mustHex(t, "0102030405060708090a0b0c0d0e0f10"),
		SenderID:     mustHex(t, "01"),
		RecipientID:  mustHex(t, "00"),
	})
	if err != nil {
		t.Fatal(err)
	}

	aad := []byte("external AAD")
	plaintext := []byte("serialized inner CoAP message")
	piv, ciphertext, err := client.Seal(plaintext, func([]byte) ([]byte, error) {
		return aad, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := server.Open(piv, ciphertext, aad)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("plaintext = %q, want %q", got, plaintext)
	}
	if _, replayErr := server.Open(piv, ciphertext, aad); !errors.Is(replayErr, ErrReplay) {
		t.Fatalf("replayed message error = %v, want ErrReplay", replayErr)
	}

	piv, ciphertext, err = client.Seal(plaintext, func([]byte) ([]byte, error) {
		return aad, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, authErr := server.Open(piv, ciphertext, []byte("wrong AAD")); authErr == nil {
		t.Fatal("expected authentication failure")
	}
	if _, openErr := server.Open(piv, ciphertext, aad); openErr != nil {
		t.Fatalf("failed authentication consumed replay sequence: %v", openErr)
	}
}

func TestContextReplayWindowRestoredAfterRestart(t *testing.T) {
	masterSecret := mustHex(t, "0102030405060708090a0b0c0d0e0f10")
	client, err := NewContext(ContextConfig{
		MasterSecret: masterSecret,
		SenderID:     mustHex(t, "00"),
		RecipientID:  mustHex(t, "01"),
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &testReplayWindowStore{}
	serverConfig := ContextConfig{
		MasterSecret:      masterSecret,
		SenderID:          mustHex(t, "01"),
		RecipientID:       mustHex(t, "00"),
		ReplayWindowStore: store,
	}
	server, err := NewContext(serverConfig)
	if err != nil {
		t.Fatal(err)
	}
	aad := []byte("external AAD")
	piv, ciphertext, err := client.Seal([]byte("request"), func([]byte) ([]byte, error) {
		return aad, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, openErr := server.Open(piv, ciphertext, aad); openErr != nil {
		t.Fatal(openErr)
	}

	restartedServer, err := NewContext(serverConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, replayErr := restartedServer.Open(piv, ciphertext, aad); !errors.Is(replayErr, ErrReplay) {
		t.Fatalf("replayed request after receiver restart error = %v, want ErrReplay", replayErr)
	}
}

func TestRFC8613AppendixC4Encryption(t *testing.T) {
	client, err := NewContext(ContextConfig{
		MasterSecret:         mustHex(t, "0102030405060708090a0b0c0d0e0f10"),
		MasterSalt:           mustHex(t, "9e7ca92223786340"),
		SenderID:             []byte{},
		RecipientID:          mustHex(t, "01"),
		SenderSequenceNumber: 20,
	})
	if err != nil {
		t.Fatal(err)
	}

	aad := mustHex(t, "8368456e63727970743040488501810a40411440")
	plaintext := mustHex(t, "01b3747631")
	partialIV, ciphertext, err := client.Seal(plaintext, func(partialIV []byte) ([]byte, error) {
		if !bytes.Equal(partialIV, mustHex(t, "14")) {
			t.Errorf("AAD builder Partial IV = %x, want 14", partialIV)
		}
		return aad, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(partialIV, mustHex(t, "14")) {
		t.Fatalf("Partial IV = %x, want 14", partialIV)
	}
	if !bytes.Equal(ciphertext, mustHex(t, "612f1092f1776f1c1668b3825e")) {
		t.Fatalf("ciphertext = %x", ciphertext)
	}

	server, err := NewContext(ContextConfig{
		MasterSecret: mustHex(t, "0102030405060708090a0b0c0d0e0f10"),
		MasterSalt:   mustHex(t, "9e7ca92223786340"),
		SenderID:     mustHex(t, "01"),
		RecipientID:  []byte{},
	})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := server.Open(partialIV, ciphertext, aad)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("opened plaintext = %x, want %x", opened, plaintext)
	}
}

func TestContextSequenceExhaustion(t *testing.T) {
	ctx, err := NewContext(ContextConfig{
		MasterSecret:         []byte{1},
		SenderSequenceNumber: maxPartialIV,
	})
	if err != nil {
		t.Fatal(err)
	}
	piv, err := ctx.NextPartialIV()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(piv, mustHex(t, "ffffffffff")) {
		t.Fatalf("max Partial IV = %x", piv)
	}
	if _, err := ctx.NextPartialIV(); !errors.Is(err, ErrSenderSequenceExhausted) {
		t.Fatalf("sequence exhaustion error = %v", err)
	}
}

func TestContextRejectsOversizedPlaintextWithoutConsumingSequence(t *testing.T) {
	ctx, err := NewContext(ContextConfig{MasterSecret: []byte{1}})
	if err != nil {
		t.Fatal(err)
	}
	builderCalled := false
	_, _, err = ctx.Seal(make([]byte, ctx.senderAEAD.MaxLength()+1), func([]byte) ([]byte, error) {
		builderCalled = true
		return nil, nil
	})
	if !errors.Is(err, ErrPlaintextTooLarge) {
		t.Fatalf("oversized plaintext error = %v", err)
	}
	if builderCalled {
		t.Fatal("AAD builder called for oversized plaintext")
	}
	piv, err := ctx.NextPartialIV()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(piv, []byte{0}) {
		t.Fatalf("sequence consumed on size failure: next Partial IV = %x", piv)
	}
}

func TestContextConcurrentSequenceAllocation(t *testing.T) {
	ctx, err := NewContext(ContextConfig{MasterSecret: []byte{1}})
	if err != nil {
		t.Fatal(err)
	}
	const count = 128
	values := make(chan uint64, count)
	var wg sync.WaitGroup
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			piv, err := ctx.NextPartialIV()
			if err != nil {
				t.Error(err)
				return
			}
			value, err := decodePartialIV(piv)
			if err != nil {
				t.Error(err)
				return
			}
			values <- value
		}()
	}
	wg.Wait()
	close(values)
	seen := make(map[uint64]bool, count)
	for value := range values {
		if seen[value] {
			t.Fatalf("duplicate sequence number %d", value)
		}
		seen[value] = true
	}
	if len(seen) != count {
		t.Fatalf("allocated %d unique sequence numbers, want %d", len(seen), count)
	}
}

func TestContextPersistentSequenceStoreSurvivesRecreation(t *testing.T) {
	store := &testSenderSequenceStore{}
	newContext := func() *Context {
		ctx, err := NewContext(ContextConfig{
			MasterSecret:        []byte{1},
			SenderSequenceStore: store,
		})
		if err != nil {
			t.Fatal(err)
		}
		return ctx
	}

	first, err := newContext().NextPartialIV()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newContext().NextPartialIV()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, []byte{0}) || !bytes.Equal(second, []byte{1}) {
		t.Fatalf("persistent sequence numbers = %x, %x; want 00, 01", first, second)
	}
}

func TestContextPersistentSequenceStoreConcurrentAcrossContexts(t *testing.T) {
	store := &testSenderSequenceStore{}
	newContext := func() *Context {
		ctx, err := NewContext(ContextConfig{
			MasterSecret:        []byte{1},
			SenderSequenceStore: store,
		})
		if err != nil {
			t.Fatal(err)
		}
		return ctx
	}
	contexts := []*Context{newContext(), newContext()}
	const count = 64
	values := make(chan uint64, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			piv, err := contexts[i%len(contexts)].NextPartialIV()
			if err != nil {
				t.Error(err)
				return
			}
			value, err := decodePartialIV(piv)
			if err != nil {
				t.Error(err)
				return
			}
			values <- value
		}()
	}
	wg.Wait()
	close(values)
	seen := make(map[uint64]bool, count)
	for value := range values {
		if seen[value] {
			t.Fatalf("persistent store returned duplicate sequence number %d", value)
		}
		seen[value] = true
	}
	if len(seen) != count {
		t.Fatalf("reserved %d unique sequence numbers, want %d", len(seen), count)
	}
}

func TestContextPersistentSequenceStoreErrorsAndExhaustion(t *testing.T) {
	storeErr := errors.New("storage unavailable")
	ctx, err := NewContext(ContextConfig{
		MasterSecret: []byte{1},
		SenderSequenceStore: &testSenderSequenceStore{
			err: storeErr,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, sequenceErr := ctx.NextPartialIV(); !errors.Is(sequenceErr, storeErr) {
		t.Fatalf("sequence store error = %v, want wrapped storage error", sequenceErr)
	}

	exhausted, err := NewContext(ContextConfig{
		MasterSecret: []byte{1},
		SenderSequenceStore: &testSenderSequenceStore{
			next: maxPartialIV,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	piv, err := exhausted.NextPartialIV()
	if err != nil || !bytes.Equal(piv, mustHex(t, "ffffffffff")) {
		t.Fatalf("last stored Partial IV = %x, error = %v", piv, err)
	}
	if _, err := exhausted.NextPartialIV(); !errors.Is(err, ErrSenderSequenceExhausted) {
		t.Fatalf("exhausted store error = %v", err)
	}
}

func TestContextRejectsConflictingSequenceConfiguration(t *testing.T) {
	_, err := NewContext(ContextConfig{
		MasterSecret:         []byte{1},
		SenderSequenceNumber: 1,
		SenderSequenceStore:  &testSenderSequenceStore{},
	})
	if !errors.Is(err, ErrInvalidContext) {
		t.Fatalf("conflicting sequence configuration error = %v", err)
	}
}

type testSenderSequenceStore struct {
	mu   sync.Mutex
	next uint64
	err  error
}

func (s *testSenderSequenceStore) Reserve() (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	value := s.next
	s.next++
	return value, nil
}
