package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompareManifests(t *testing.T) {
	oldM := Manifest{
		"options.txt": FileEntry{Hash: "111", Mtime: 100, Size: 10},
		"servers.dat": FileEntry{Hash: "222", Mtime: 100, Size: 20},
		"old_mod.jar": FileEntry{Hash: "333", Mtime: 100, Size: 30},
	}
	newM := Manifest{
		"options.txt": FileEntry{Hash: "111", Mtime: 100, Size: 10}, // Identique
		"servers.dat": FileEntry{Hash: "999", Mtime: 200, Size: 25}, // Modifié
		"new_mod.jar": FileEntry{Hash: "444", Mtime: 200, Size: 40}, // Ajouté
	}

	diff := CompareManifests(oldM, newM)
	if !diff.HasChanges {
		t.Errorf("expected hasChanges=true")
	}
	if len(diff.Added) != 1 || diff.Added[0] != "new_mod.jar" {
		t.Errorf("unexpected added: %v", diff.Added)
	}
	if len(diff.Modified) != 1 || diff.Modified[0] != "servers.dat" {
		t.Errorf("unexpected modified: %v", diff.Modified)
	}
	if len(diff.Deleted) != 1 || diff.Deleted[0] != "old_mod.jar" {
		t.Errorf("unexpected deleted: %v", diff.Deleted)
	}
}

func TestGenerateManifestIgnoresDirs(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "horizon_scanner_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Création de structure Minecraft
	_ = os.WriteFile(filepath.Join(tmpDir, "options.txt"), []byte("lang:fr_FR"), 0644)
	_ = os.MkdirAll(filepath.Join(tmpDir, "config"), 0755)
	_ = os.WriteFile(filepath.Join(tmpDir, "config", "mod.cfg"), []byte("enabled=true"), 0644)

	// Dossiers à ignorer
	_ = os.MkdirAll(filepath.Join(tmpDir, "logs"), 0755)
	_ = os.WriteFile(filepath.Join(tmpDir, "logs", "latest.log"), []byte("log data"), 0644)
	_ = os.MkdirAll(filepath.Join(tmpDir, "assets"), 0755)
	_ = os.WriteFile(filepath.Join(tmpDir, "assets", "sound.ogg"), []byte("sound"), 0644)

	manifest, err := GenerateManifest(tmpDir, nil)
	if err != nil {
		t.Fatalf("GenerateManifest failed: %v", err)
	}

	if _, ok := manifest["options.txt"]; !ok {
		t.Errorf("options.txt missing in manifest")
	}
	if _, ok := manifest["config/mod.cfg"]; !ok {
		t.Errorf("config/mod.cfg missing in manifest")
	}
	if _, ok := manifest["logs/latest.log"]; ok {
		t.Errorf("logs/latest.log should have been ignored")
	}
	if _, ok := manifest["assets/sound.ogg"]; ok {
		t.Errorf("assets/sound.ogg should have been ignored")
	}
}

