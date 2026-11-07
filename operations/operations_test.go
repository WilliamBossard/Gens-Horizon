package operations

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/WilliamBossard/Gens-Horizon/ziputils"
)

func TestParseTargetInstance(t *testing.T) {
	if got := parseTargetInstance([]string{"--upload", "GensBien"}); got != "GensBien" {
		t.Errorf("expected GensBien, got %s", got)
	}
	if got := parseTargetInstance([]string{"--sync", "--instance=Modpack1"}); got != "Modpack1" {
		t.Errorf("expected Modpack1, got %s", got)
	}
}

func TestHasFlag(t *testing.T) {
	args := []string{"--upload", "--force"}
	if !hasFlag(args, "--force") {
		t.Errorf("expected hasFlag to detect --force")
	}
	if hasFlag(args, "--dry-run") {
		t.Errorf("did not expect hasFlag to detect --dry-run")
	}
}

func TestUpdateLastSync(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "horizon_lastsync_test_*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	_ = os.Setenv("HORIZON_DATA_DIR", tmpDir)
	defer os.Unsetenv("HORIZON_DATA_DIR")

	updateLastSync("GensBien")
	syncPath := filepath.Join(tmpDir, "last_sync.json")
	if _, err := os.Stat(syncPath); err != nil {
		t.Fatalf("expected last_sync.json to exist: %v", err)
	}
}

func TestHardlinkRollbackIsolation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "horizon_rollback_test_*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	instDir := filepath.Join(tmpDir, "MyInstance")
	_ = os.MkdirAll(filepath.Join(instDir, "config"), 0755)
	origFile := filepath.Join(instDir, "config", "settings.txt")
	_ = os.WriteFile(origFile, []byte("original_value"), 0644)

	rollbackPath := createRollbackSnapshot(instDir)
	if rollbackPath == "" {
		t.Fatalf("expected rollback snapshot to be created")
	}
	defer cleanupRollback(rollbackPath)

	// Crée une archive ZIP contenant une nouvelle version de config/settings.txt
	zipPath := filepath.Join(tmpDir, "update.zip")
	zf, _ := os.Create(zipPath)
	zw := zip.NewWriter(zf)
	w, _ := zw.Create("config/settings.txt")
	_, _ = w.Write([]byte("modified_by_extract"))
	_ = zw.Close()
	_ = zf.Close()

	if err := ziputils.ExtractZip(zipPath, instDir, nil); err != nil {
		t.Fatalf("ExtractZip failed: %v", err)
	}

	// Le fichier dans le snapshot rollback ne doit PAS avoir été altéré grâce au détachement d'inode
	rbContent, err := os.ReadFile(filepath.Join(rollbackPath, "config", "settings.txt"))
	if err != nil {
		t.Fatalf("failed reading rollback file: %v", err)
	}
	if string(rbContent) != "original_value" {
		t.Errorf("rollback file was mutated! expected 'original_value', got '%s'", string(rbContent))
	}
}

