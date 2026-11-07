package paths

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupOrphanedTemps(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "horizon_cleanup_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	oldTime := time.Now().Add(-15 * time.Minute)
	freshTime := time.Now()

	// Fichiers à supprimer (vieux temporaires orphelins)
	oldTempZip := filepath.Join(tempDir, "temp_upload_123.zip")
	oldTmp := filepath.Join(tempDir, "token.json.tmp")
	_ = os.WriteFile(oldTempZip, []byte("junk"), 0644)
	_ = os.WriteFile(oldTmp, []byte("junk"), 0644)
	_ = os.Chtimes(oldTempZip, oldTime, oldTime)
	_ = os.Chtimes(oldTmp, oldTime, oldTime)

	// Fichiers à conserver
	validSetting := filepath.Join(tempDir, "horizon_settings.json")
	freshTempZip := filepath.Join(tempDir, "temp_current_active.zip")
	_ = os.WriteFile(validSetting, []byte("{}"), 0644)
	_ = os.WriteFile(freshTempZip, []byte("active"), 0644)
	_ = os.Chtimes(validSetting, oldTime, oldTime)
	_ = os.Chtimes(freshTempZip, freshTime, freshTime)

	CleanupOrphanedTemps(tempDir)

	if _, err := os.Stat(oldTempZip); !os.IsNotExist(err) {
		t.Errorf("oldTempZip should have been deleted, but still exists")
	}
	if _, err := os.Stat(oldTmp); !os.IsNotExist(err) {
		t.Errorf("oldTmp should have been deleted, but still exists")
	}
	if _, err := os.Stat(validSetting); err != nil {
		t.Errorf("validSetting should have been preserved, got error: %v", err)
	}
	if _, err := os.Stat(freshTempZip); err != nil {
		t.Errorf("freshTempZip (<10min) should have been preserved, got error: %v", err)
	}
}

func TestResolveWithFallback(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "horizon_fallback_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	legacyPath := filepath.Join(tempDir, "legacy_file.json")
	newPath := filepath.Join(tempDir, "subfolder", "target_file.json")

	_ = os.WriteFile(legacyPath, []byte(`{"migrated":true}`), 0644)

	res := ResolveWithFallback(newPath, legacyPath)
	if res != newPath {
		t.Errorf("expected res to be %s, got %s", newPath, res)
	}

	data, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("failed to read migrated file: %v", err)
	}
	if string(data) != `{"migrated":true}` {
		t.Errorf("content mismatch, got: %s", string(data))
	}
}

