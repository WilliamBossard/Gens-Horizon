package providers

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUploadSessionPersistenceRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HORIZON_DATA_DIR", tmpDir)

	sample := filepath.Join(tmpDir, "archive.zip")
	if err := os.WriteFile(sample, []byte("zip-content"), 0600); err != nil {
		t.Fatal(err)
	}
	key := computeUploadSessionKey("google", "backup_Test.zip", sample, 11)
	saveUploadSession(key, "https://www.googleapis.com/upload/drive/v3/files?upload_id=abc", time.Hour)

	got, ok := loadUploadSession(key)
	if !ok || got != "https://www.googleapis.com/upload/drive/v3/files?upload_id=abc" {
		t.Fatalf("expected persisted session URL, got (%q, %v)", got, ok)
	}

	clearUploadSession(key)
	if _, ok := loadUploadSession(key); ok {
		t.Fatalf("expected session to be cleared")
	}
}
