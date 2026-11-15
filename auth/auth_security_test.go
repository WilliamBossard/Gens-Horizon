package auth

import (
	"crypto/aes"
	"encoding/hex"
	"testing"
)

func TestDecryptRejectsMalformedIVWithoutPanic(t *testing.T) {
	authMu.Lock()
	prevKey := secretKey
	secretKey = make([]byte, 32)
	authMu.Unlock()
	defer func() {
		authMu.Lock()
		secretKey = prevKey
		authMu.Unlock()
	}()

	// Valid roundtrip GCM
	enc, err := Encrypt("secret-token")
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}
	dec, isLegacy, err := Decrypt(enc)
	if err != nil || isLegacy || dec != "secret-token" {
		t.Fatalf("Decrypt roundtrip failed: dec=%q legacy=%v err=%v", dec, isLegacy, err)
	}

	// Short GCM IV (must fail with error, not panic)
	badGCM := hex.EncodeToString([]byte{1, 2, 3}) + ":" + hex.EncodeToString(make([]byte, 16)) + ":" + hex.EncodeToString(make([]byte, 16))
	if _, _, err := Decrypt(badGCM); err == nil {
		t.Errorf("Decrypt should fail on invalid GCM IV length")
	}

	// Short CBC IV (must fail with error, not panic)
	badCBC := hex.EncodeToString([]byte{1, 2, 3, 4}) + ":" + hex.EncodeToString(make([]byte, aes.BlockSize))
	if _, _, err := Decrypt(badCBC); err == nil {
		t.Errorf("Decrypt should fail on invalid CBC IV length")
	}

	// Non-block-aligned CBC ciphertext
	badCBCBlock := hex.EncodeToString(make([]byte, aes.BlockSize)) + ":" + hex.EncodeToString([]byte{1, 2, 3})
	if _, _, err := Decrypt(badCBCBlock); err == nil {
		t.Errorf("Decrypt should fail on non-aligned CBC ciphertext")
	}
}

