package scanner

import (
	"encoding/json"
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

	// Fichiers et dossiers de pollution OS et temporaires
	_ = os.WriteFile(filepath.Join(tmpDir, ".DS_Store"), []byte("mac junk"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "Thumbs.db"), []byte("win junk"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "desktop.ini"), []byte("win config"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "config", "._mod.cfg"), []byte("apple double"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "archive.part"), []byte("partial download"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "update.tmp"), []byte("temp file"), 0644)
	_ = os.MkdirAll(filepath.Join(tmpDir, "saves", "world"), 0755)
	_ = os.WriteFile(filepath.Join(tmpDir, "saves", "world", "session.lock"), []byte("lock"), 0644)
	_ = os.MkdirAll(filepath.Join(tmpDir, ".Spotlight-V100"), 0755)
	_ = os.WriteFile(filepath.Join(tmpDir, ".Spotlight-V100", "index.db"), []byte("index"), 0644)

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

	// Vérification de l'exclusion des fichiers de pollution OS et temporaires
	unwanted := []string{
		".DS_Store",
		"Thumbs.db",
		"desktop.ini",
		"config/._mod.cfg",
		"archive.part",
		"update.tmp",
		"saves/world/session.lock",
		".Spotlight-V100/index.db",
	}
	for _, u := range unwanted {
		if _, ok := manifest[u]; ok {
			t.Errorf("pollution file %s should have been ignored by scanner", u)
		}
	}
}

func TestUnmarshalNodeManifestFloatMtime(t *testing.T) {
	rawJSON := `{
		"command_history.txt": {
			"hash": "abc123def456",
			"mtime": 1790268044168.8877,
			"size": 3254
		},
		"servers.dat": {
			"hash": "def456abc123",
			"mtime": "1790268055000",
			"size": 1024
		}
	}`
	var m Manifest
	err := json.Unmarshal([]byte(rawJSON), &m)
	if err != nil {
		t.Fatalf("Failed to unmarshal manifest with float mtime: %v", err)
	}
	if entry, ok := m["command_history.txt"]; !ok {
		t.Fatal("Missing command_history.txt")
	} else {
		if entry.Mtime.Int64() != 1790268044168 {
			t.Fatalf("Expected 1790268044168, got %d", entry.Mtime.Int64())
		}
		if entry.Size.Int64() != 3254 {
			t.Fatalf("Expected 3254, got %d", entry.Size.Int64())
		}
	}
	if entry, ok := m["servers.dat"]; !ok {
		t.Fatal("Missing servers.dat")
	} else {
		if entry.Mtime.Int64() != 1790268055000 {
			t.Fatalf("Expected 1790268055000, got %d", entry.Mtime.Int64())
		}
	}
}

