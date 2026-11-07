package lock

import (
	"os"
	"testing"
)

func TestAcquireAndReleaseLock(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "horizon_lock_test_*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	_ = os.Setenv("HORIZON_DATA_DIR", tmpDir)
	defer os.Unsetenv("HORIZON_DATA_DIR")

	if !AcquireLock() {
		t.Fatalf("expected AcquireLock to succeed on clean directory")
	}
	ReleaseLock()
}
