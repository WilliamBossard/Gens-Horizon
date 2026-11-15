package lock

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
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

func TestZeroByteLockGracePeriod(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "horizon_lock_grace_*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	t.Setenv("HORIZON_DATA_DIR", tmpDir)

	lockFile := filepath.Join(tmpDir, "horizon.lock")
	if err := os.WriteFile(lockFile, []byte{}, 0600); err != nil {
		t.Fatalf("WriteFile lock failed: %v", err)
	}

	// Lock de 0 octet très récent (< 2s) : ne doit pas être considéré périmé
	if AcquireLock() {
		ReleaseLock()
		t.Fatalf("expected AcquireLock to respect 2s grace period for fresh 0-byte lock")
	}

	// Vieillir artificiellement le lock de 0 octet (> 2s) : doit être nettoyé et acquis
	oldTime := time.Now().Add(-5 * time.Second)
	if err := os.Chtimes(lockFile, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes failed: %v", err)
	}

	if !AcquireLock() {
		t.Fatalf("expected AcquireLock to acquire aged 0-byte stale lock")
	}
	ReleaseLock()
}

func TestIsProcessAliveRejectsNonPositivePID(t *testing.T) {
	if isProcessAlive(0) {
		t.Fatalf("expected isProcessAlive(0) to return false")
	}
	if isProcessAlive(-42) {
		t.Fatalf("expected isProcessAlive(-42) to return false")
	}
}

func TestAliveLockSurvivesShortSleepHeartbeatGap(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HORIZON_DATA_DIR", tmpDir)

	lockFile := filepath.Join(tmpDir, "horizon.lock")
	pidBytes := []byte(filepath.Base(tmpDir))
	// PID courant (forcément vivant)
	pidStr := strconv.Itoa(os.Getpid())
	_ = pidBytes
	if err := os.WriteFile(lockFile, []byte(pidStr), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Vieillir de 45s (simule une courte mise en veille < 5m) : le lock vivant doit être respecté
	gap45s := time.Now().Add(-45 * time.Second)
	if err := os.Chtimes(lockFile, gap45s, gap45s); err != nil {
		t.Fatalf("Chtimes 45s failed: %v", err)
	}
	if AcquireLock() {
		ReleaseLock()
		t.Fatalf("expected live PID lock aged 45s not to be stolen")
	}

	// Vieillir au-delà de 5m : doit être considéré obsolète et nettoyé
	old6m := time.Now().Add(-6 * time.Minute)
	if err := os.Chtimes(lockFile, old6m, old6m); err != nil {
		t.Fatalf("Chtimes 6m failed: %v", err)
	}
	if !AcquireLock() {
		t.Fatalf("expected lock aged 6m without heartbeat to be reclaimed")
	}
	ReleaseLock()
}

