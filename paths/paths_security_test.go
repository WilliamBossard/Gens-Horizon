package paths

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSanitizeAndSafeJoinRejectReservedAndColon(t *testing.T) {
	badNames := []string{"CON", "nul", "COM1", "LPT9.txt", "foo:bar", "a/b", "a\\b", ".."}
	for _, name := range badNames {
		if _, err := SanitizeInstanceName(name); err == nil {
			t.Errorf("expected SanitizeInstanceName(%q) to fail", name)
		}
	}

	base := t.TempDir()
	badRels := []string{"CON/file.txt", "sub/NUL.dat", "foo:ads", "../escape"}
	for _, rel := range badRels {
		if _, err := SafeJoinUnder(base, rel); err == nil {
			t.Errorf("expected SafeJoinUnder(%q) to fail", rel)
		}
	}
}

func TestCleanupOrphanedTempsRemovesExpiredHorizonDirs(t *testing.T) {
	oldDir, err := os.MkdirTemp("", "horizon-sync-testold-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(oldDir)
	if err := os.WriteFile(filepath.Join(oldDir, "stale.zip"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-30 * time.Minute)
	if err := os.Chtimes(oldDir, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	freshDir, err := os.MkdirTemp("", "horizon-up-testfresh-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(freshDir)

	CleanupOrphanedTemps(t.TempDir())

	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("expected expired horizon-sync-* dir to be removed, stat err=%v", err)
	}
	if _, err := os.Stat(freshDir); err != nil {
		t.Fatalf("expected fresh horizon-up-* dir to be preserved, stat err=%v", err)
	}
}
