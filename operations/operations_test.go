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

func TestExtractInstanceMetadata(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "horizon_meta_test_*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Cas 1 : data: URI direct dans instance.json
	inst1 := filepath.Join(tmpDir, "InstDataUri")
	_ = os.MkdirAll(inst1, 0755)
	inst1Json := `{"name":"MonPack","loader":"fabric","icon":"data:image/svg+xml;charset=UTF-8,%3Csvg%3E%3C/svg%3E"}`
	_ = os.WriteFile(filepath.Join(inst1, "instance.json"), []byte(inst1Json), 0644)

	meta1 := extractInstanceMetadata(inst1, "InstDataUri")
	if meta1["realName"] != "MonPack" {
		t.Errorf("expected realName 'MonPack', got '%v'", meta1["realName"])
	}
	if meta1["loader"] != "fabric" {
		t.Errorf("expected loader 'fabric', got '%v'", meta1["loader"])
	}
	if meta1["iconData"] != "data:image/svg+xml;charset=UTF-8,%3Csvg%3E%3C/svg%3E" {
		t.Errorf("expected iconData data: URI, got '%v'", meta1["iconData"])
	}

	// Cas 2 : fichier icon.png physique dans le dossier
	inst2 := filepath.Join(tmpDir, "InstPhysicalIcon")
	_ = os.MkdirAll(inst2, 0755)
	_ = os.WriteFile(filepath.Join(inst2, "icon.png"), []byte("FAKE_PNG_BINARY_CONTENT"), 0644)
	_ = os.WriteFile(filepath.Join(inst2, "instance.json"), []byte(`{"name":"Inst2","loader":"forge"}`), 0644)

	meta2 := extractInstanceMetadata(inst2, "InstPhysicalIcon")
	iconData2, ok := meta2["iconData"].(string)
	if !ok || iconData2 == "" {
		t.Fatalf("expected non-empty iconData from physical icon.png")
	}
	if len(iconData2) < 25 || iconData2[:22] != "data:image/png;base64," {
		t.Errorf("expected data:image/png;base64,... format, got: %s", iconData2)
	}

	// Cas 3 : gens/launcher_config.json fallback
	inst3 := filepath.Join(tmpDir, "InstGensConfig")
	_ = os.MkdirAll(filepath.Join(inst3, "gens"), 0755)
	_ = os.WriteFile(filepath.Join(inst3, "gens", "launcher_config.json"), []byte(`{"loader":"neoforge","icon":"data:image/png;base64,12345"}`), 0644)

	meta3 := extractInstanceMetadata(inst3, "InstGensConfig")
	if meta3["loader"] != "neoforge" {
		t.Errorf("expected loader 'neoforge' from launcher_config.json, got: %v", meta3["loader"])
	}
	if meta3["iconData"] != "data:image/png;base64,12345" {
		t.Errorf("expected iconData from launcher_config.json, got: %v", meta3["iconData"])
	}
}


