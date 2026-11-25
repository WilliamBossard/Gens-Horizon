package operations

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreInPlaceDoesNotMutateRollbackSnapshot(t *testing.T) {
	tmp := t.TempDir()
	snapshotDir := filepath.Join(tmp, "snapshot")
	targetDir := filepath.Join(tmp, "target")
	if err := os.MkdirAll(snapshotDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}

	snapFile := filepath.Join(snapshotDir, "options.txt")
	if err := os.WriteFile(snapFile, []byte("original-snapshot-content"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := restoreInPlace(snapshotDir, targetDir); err != nil {
		t.Fatalf("restoreInPlace failed: %v", err)
	}

	// Simulate an in-place write (open + write without unlink) on the restored file.
	restoredFile := filepath.Join(targetDir, "options.txt")
	f, err := os.OpenFile(restoredFile, os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("mutated-by-game")
	_ = f.Close()

	snapBytes, err := os.ReadFile(snapFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(snapBytes) != "original-snapshot-content" {
		t.Fatalf("rollback snapshot was mutated via shared inode: got %q", string(snapBytes))
	}
}

func TestPruneOldRollbacksKeepsMostRecentPerInstance(t *testing.T) {
	tmp := t.TempDir()
	dirs := []string{
		"PackA_rollback_1000",
		"PackA_rollback_2000",
		"PackA_rollback_3000",
		"PackA_rollback_4000",
		"PackB_rollback_1000",
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(tmp, d), 0755); err != nil {
			t.Fatal(err)
		}
	}

	removed := PruneOldRollbacks(tmp, 2)
	if removed != 2 {
		t.Fatalf("expected 2 old rollbacks removed, got %d", removed)
	}
	for _, gone := range []string{"PackA_rollback_1000", "PackA_rollback_2000"} {
		if _, err := os.Stat(filepath.Join(tmp, gone)); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be deleted", gone)
		}
	}
	for _, kept := range []string{"PackA_rollback_3000", "PackA_rollback_4000", "PackB_rollback_1000"} {
		if _, err := os.Stat(filepath.Join(tmp, kept)); err != nil {
			t.Fatalf("expected %s to be kept, got err: %v", kept, err)
		}
	}
}
