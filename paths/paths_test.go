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
	oldHorizonZip := filepath.Join(tempDir, "horizon_full_123.zip")
	oldTmp := filepath.Join(tempDir, "token.json.tmp")
	_ = os.WriteFile(oldTempZip, []byte("junk"), 0644)
	_ = os.WriteFile(oldHorizonZip, []byte("junk"), 0644)
	_ = os.WriteFile(oldTmp, []byte("junk"), 0644)
	_ = os.Chtimes(oldTempZip, oldTime, oldTime)
	_ = os.Chtimes(oldHorizonZip, oldTime, oldTime)
	_ = os.Chtimes(oldTmp, oldTime, oldTime)

	// Fichiers à conserver
	validSetting := filepath.Join(tempDir, "horizon_settings.json")
	freshTempZip := filepath.Join(tempDir, "temp_current_active.zip")
	freshHorizonZip := filepath.Join(tempDir, "horizon_delta_active.zip")
	_ = os.WriteFile(validSetting, []byte("{}"), 0644)
	_ = os.WriteFile(freshTempZip, []byte("active"), 0644)
	_ = os.WriteFile(freshHorizonZip, []byte("active"), 0644)
	_ = os.Chtimes(validSetting, oldTime, oldTime)
	_ = os.Chtimes(freshTempZip, freshTime, freshTime)
	_ = os.Chtimes(freshHorizonZip, freshTime, freshTime)

	CleanupOrphanedTemps(tempDir)

	if _, err := os.Stat(oldTempZip); !os.IsNotExist(err) {
		t.Errorf("oldTempZip should have been deleted, but still exists")
	}
	if _, err := os.Stat(oldHorizonZip); !os.IsNotExist(err) {
		t.Errorf("oldHorizonZip should have been deleted, but still exists")
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
	if _, err := os.Stat(freshHorizonZip); err != nil {
		t.Errorf("freshHorizonZip (<10min) should have been preserved, got error: %v", err)
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

func TestCleanupOrphanedTempsRemovesPidAndStaleFiles(t *testing.T) {
	tempDir := t.TempDir()
	oldTime := time.Now().Add(-15 * time.Minute)

	oldPidTmp := filepath.Join(tempDir, "token.json.tmp.99999")
	oldStaleLock := filepath.Join(tempDir, "horizon.lock.stale.99999.0")
	_ = os.WriteFile(oldPidTmp, []byte("junk"), 0644)
	_ = os.WriteFile(oldStaleLock, []byte("junk"), 0644)
	_ = os.Chtimes(oldPidTmp, oldTime, oldTime)
	_ = os.Chtimes(oldStaleLock, oldTime, oldTime)

	CleanupOrphanedTemps(tempDir)

	if _, err := os.Stat(oldPidTmp); !os.IsNotExist(err) {
		t.Errorf("oldPidTmp should have been deleted")
	}
	if _, err := os.Stat(oldStaleLock); !os.IsNotExist(err) {
		t.Errorf("oldStaleLock should have been deleted")
	}
}

func TestLoadSettingsPreservesDefaultsOnPartialJSON(t *testing.T) {
	tempDir := t.TempDir()
	_ = os.Setenv("HORIZON_DATA_DIR", tempDir)
	defer os.Unsetenv("HORIZON_DATA_DIR")

	settingsPath := GetSettingsPath()
	_ = os.MkdirAll(filepath.Dir(settingsPath), 0755)
	_ = os.WriteFile(settingsPath, []byte(`{"provider":"dropbox"}`), 0644)

	got := LoadSettings()
	if got.Provider != "dropbox" {
		t.Errorf("expected provider dropbox, got %s", got.Provider)
	}
	if got.MaxRetries <= 0 || got.DeltaCleanupThreshold <= 0 {
		t.Errorf("expected defaults to be preserved for omitted fields, got %+v", got)
	}
}

