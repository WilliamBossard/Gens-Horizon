package auth

import (
	"os"
	"path/filepath"
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

