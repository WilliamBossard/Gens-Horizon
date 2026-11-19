package auth

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "horizon_auth_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	t.Setenv("HORIZON_DATA_DIR", tmpDir)

	// Reset cached globals
	authMu.Lock()
	machineID = ""
	salt = nil
	secretKey = nil
	authMu.Unlock()

	original := `{"access_token":"ya29.test12345","refresh_token":"1//test_refresh","expiry_date":1780000000000}`

	encrypted, err := Encrypt(original)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	decrypted, needsMigration, err := Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}

	if needsMigration {
		t.Errorf("expected needsMigration=false for GCM, got true")
	}

	if decrypted != original {
		t.Errorf("decrypted text mismatch:\nwant: %s\ngot:  %s", original, decrypted)
	}
}

func TestDecryptTamperedFails(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "horizon_auth_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	t.Setenv("HORIZON_DATA_DIR", tmpDir)

	authMu.Lock()
	machineID = ""
	salt = nil
	secretKey = nil
	authMu.Unlock()

	_, _, err = Decrypt("0102030405060708090a0b0c:112233445566778899aabbccddeeff00:deadbeef")
	if err == nil {
		t.Errorf("expected error when decrypting tampered data, got nil")
	}
}

func TestGetSecureTokenPlaintextMigration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "horizon_auth_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	t.Setenv("HORIZON_DATA_DIR", tmpDir)

	authMu.Lock()
	machineID = ""
	salt = nil
	secretKey = nil
	authMu.Unlock()

	tokenPath := filepath.Join(tmpDir, "token_google.json")
	plainJSON := `{"access_token":"sample","refresh_token":"sample_ref"}`
	if err := os.WriteFile(tokenPath, []byte(plainJSON), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	var data TokenData
	if err := GetSecureToken(tokenPath, &data); err != nil {
		t.Fatalf("GetSecureToken failed: %v", err)
	}

	if data.AccessToken != "sample" {
		t.Errorf("expected access_token='sample', got '%s'", data.AccessToken)
	}

	// Verify file is now encrypted
	rawOnDisk, _ := os.ReadFile(tokenPath)
	if string(rawOnDisk)[0] == '{' {
		t.Errorf("expected token file to be encrypted after migration, but still starts with '{'")
	}
}

func TestSaltKeyDPAPIMigrationAndBackwardsCompat(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "horizon_dpapi_test_*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	t.Setenv("HORIZON_DATA_DIR", tmpDir)

	resetGlobals := func() {
		authMu.Lock()
		machineID = ""
		salt = nil
		secretKey = nil
		authMu.Unlock()
	}
	resetGlobals()

	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0700); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	legacyMID := "legacy-machine-id-0123456789abcdef"
	legacySalt := []byte("0123456789abcdef0123456789abcdef")
	if err := os.WriteFile(filepath.Join(authDir, ".machine_id"), []byte(legacyMID), 0600); err != nil {
		t.Fatalf("WriteFile .machine_id failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(authDir, "salt.key"), legacySalt, 0600); err != nil {
		t.Fatalf("WriteFile salt.key failed: %v", err)
	}

	// Capture derived secret key using legacy salt
	if _, err := GetSecretKey(); err != nil {
		t.Fatalf("GetSecretKey failed: %v", err)
	}
	keyFromLegacy := append([]byte(nil), secretKey...)

	if dpapiAvailable() {
		onDisk, err := os.ReadFile(filepath.Join(authDir, "salt.key"))
		if err != nil {
			t.Fatalf("ReadFile migrated salt.key failed: %v", err)
		}
		if !bytes.HasPrefix(onDisk, []byte("DPAPIv1:")) {
			t.Fatalf("expected salt.key to be sealed with DPAPIv1 prefix on Windows")
		}
	}

	// Reload from disk with empty globals: must unseal identical salt & derive identical secretKey
	resetGlobals()
	if _, err := GetSecretKey(); err != nil {
		t.Fatalf("GetSecretKey after DPAPI reload failed: %v", err)
	}
	if !bytes.Equal(keyFromLegacy, secretKey) {
		t.Fatalf("derived secretKey changed across DPAPI migration/reload")
	}

	// Verify EncryptToken atomic write round-trip
	tokPath := filepath.Join(authDir, "token_google.json")
	tok := TokenData{AccessToken: "dpapi-ok", RefreshToken: "ref-ok", ExpiryDate: 9999999999999}
	if err := EncryptToken(tokPath, tok); err != nil {
		t.Fatalf("EncryptToken failed: %v", err)
	}
	var got TokenData
	if err := GetSecureToken(tokPath, &got); err != nil {
		t.Fatalf("GetSecureToken failed: %v", err)
	}
	if got.AccessToken != tok.AccessToken || got.RefreshToken != tok.RefreshToken {
		t.Errorf("token mismatch: got %+v, want %+v", got, tok)
	}
}

func TestLegacyV1PBKDF2DecryptionAndMigrationToV2(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HORIZON_DATA_DIR", tmpDir)

	authMu.Lock()
	machineID = ""
	salt = nil
	secretKey = nil
	authMu.Unlock()

	if _, err := GetSecretKey(); err != nil {
		t.Fatalf("GetSecretKey failed: %v", err)
	}

	// Build a legacy v1 (3-part PBKDF2-600k) token payload
	authMu.Lock()
	mID := machineID
	s := salt
	authMu.Unlock()
	legacyKey, err := pbkdf2.Key(sha256.New, mID, s, 600000, 32)
	if err != nil {
		t.Fatalf("pbkdf2 failed: %v", err)
	}
	block, _ := aes.NewCipher(legacyKey)
	gcm, _ := cipher.NewGCM(block)
	iv := make([]byte, 12)
	_, _ = io.ReadFull(rand.Reader, iv)
	plain := `{"access_token":"legacy-v1-ok","refresh_token":"ref-v1"}`
	sealed := gcm.Seal(nil, iv, []byte(plain), nil)
	tagSize := gcm.Overhead()
	v1Ciphertext := hex.EncodeToString(iv) + ":" +
		hex.EncodeToString(sealed[len(sealed)-tagSize:]) + ":" +
		hex.EncodeToString(sealed[:len(sealed)-tagSize])

	tokenPath := filepath.Join(tmpDir, "auth", "token_google.json")
	if err := os.WriteFile(tokenPath, []byte(v1Ciphertext), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	var loaded TokenData
	if err := GetSecureToken(tokenPath, &loaded); err != nil {
		t.Fatalf("GetSecureToken legacy v1 failed: %v", err)
	}
	if loaded.AccessToken != "legacy-v1-ok" {
		t.Fatalf("expected legacy-v1-ok, got %q", loaded.AccessToken)
	}

	migratedRaw, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("ReadFile migrated token failed: %v", err)
	}
	if !strings.HasPrefix(string(migratedRaw), "v2:") {
		t.Fatalf("expected legacy token to migrate to v2: format, got %q", string(migratedRaw))
	}
}

func TestGetSecureTokenReturnsErrTokenCorruptedOnMalformedCiphertext(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HORIZON_DATA_DIR", tmpDir)

	authMu.Lock()
	machineID = ""
	salt = nil
	secretKey = nil
	authMu.Unlock()

	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0700); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	badTokenPath := filepath.Join(authDir, "token_bad.json")
	if err := os.WriteFile(badTokenPath, []byte("v2:deadbeef:not_hex"), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	var target TokenData
	err := GetSecureToken(badTokenPath, &target)
	if !errors.Is(err, ErrTokenCorrupted) {
		t.Fatalf("expected ErrTokenCorrupted on malformed ciphertext, got: %v", err)
	}
}

