package ziputils

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestE2EEncryptDecryptRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "plain.zip")
	enc := filepath.Join(tmpDir, "plain.zip.e2e")
	dec := filepath.Join(tmpDir, "plain.zip.dec")

	// Taille > 64 Ko pour tester plusieurs blocs + cas exact
	payload := bytes.Repeat([]byte("MinecraftWorldChunkData_1234567890!"), 4000)
	if err := os.WriteFile(src, payload, 0600); err != nil {
		t.Fatal(err)
	}

	if err := EncryptFileE2E(src, enc, "GH-TEST-1234-ABCD"); err != nil {
		t.Fatalf("EncryptFileE2E failed: %v", err)
	}
	isEnc, err := IsE2EEncryptedFile(enc)
	if err != nil || !isEnc {
		t.Fatalf("expected IsE2EEncryptedFile=true, got %v (err=%v)", isEnc, err)
	}
	isPlainEnc, err := IsE2EEncryptedFile(src)
	if err != nil || isPlainEnc {
		t.Fatalf("expected IsE2EEncryptedFile(src)=false, got %v (err=%v)", isPlainEnc, err)
	}

	if err := DecryptFileE2E(enc, dec, "WRONG-KEY"); !errors.Is(err, ErrE2EMismatch) {
		t.Fatalf("expected ErrE2EMismatch with wrong key, got %v", err)
	}

	if err := DecryptFileE2E(enc, dec, "GH-TEST-1234-ABCD"); err != nil {
		t.Fatalf("DecryptFileE2E failed: %v", err)
	}
	got, err := os.ReadFile(dec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("decrypted payload mismatch (got %d bytes, want %d bytes)", len(got), len(payload))
	}
}

func TestE2EDecryptBackwardCompatV1(t *testing.T) {
	tmpDir := t.TempDir()
	encV1 := filepath.Join(tmpDir, "legacy_v1.zip.e2e")
	dec := filepath.Join(tmpDir, "legacy_v1.zip")

	pass := "GH-LEGACY-V1-KEY"
	plain := []byte("legacy-v1-archive-payload")
	salt := make([]byte, e2eSaltLen)
	baseNonce := make([]byte, e2eNonceLen)
	_, _ = rand.Read(salt)
	_, _ = rand.Read(baseNonce)

	key, err := deriveE2EKey(pass, salt, e2ePbkdf2ItersV1)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	var aad [9]byte
	aad[8] = 1
	ct := gcm.Seal(nil, makeChunkNonce(baseNonce, 0, true), plain, aad[:])

	var buf bytes.Buffer
	buf.WriteString(e2eMagicV1)
	buf.Write(salt)
	buf.Write(baseNonce)
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(ct)))
	buf.Write(lenBuf[:])
	buf.Write(ct)

	if err := os.WriteFile(encV1, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if ok, err := IsE2EEncryptedFile(encV1); err != nil || !ok {
		t.Fatalf("expected IsE2EEncryptedFile=true for V1, got %v (err=%v)", ok, err)
	}
	if err := DecryptFileE2E(encV1, dec, pass); err != nil {
		t.Fatalf("DecryptFileE2E(V1) failed: %v", err)
	}
	got, _ := os.ReadFile(dec)
	if !bytes.Equal(got, plain) {
		t.Fatalf("V1 payload mismatch: got %q, want %q", got, plain)
	}
}

func TestEncryptStreamE2ERoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	enc := filepath.Join(tmpDir, "stream.zip.e2e")
	dec := filepath.Join(tmpDir, "stream.zip")
	payload := bytes.Repeat([]byte("StreamChunkPayload_XYZ_987654321!"), 2000)

	if err := EncryptStreamE2E(bytes.NewReader(payload), enc, "GH-STREAM-KEY-99"); err != nil {
		t.Fatalf("EncryptStreamE2E failed: %v", err)
	}
	if err := DecryptFileE2E(enc, dec, "GH-STREAM-KEY-99"); err != nil {
		t.Fatalf("DecryptFileE2E failed: %v", err)
	}
	got, err := os.ReadFile(dec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("stream decrypted payload mismatch: got %d, want %d", len(got), len(payload))
	}
}
