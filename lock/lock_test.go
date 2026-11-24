package lock

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
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

	// Vieillir de 6m (simule une mise en veille < 10m) : le lock vivant doit être respecté
	gap6m := time.Now().Add(-6 * time.Minute)
	if err := os.Chtimes(lockFile, gap6m, gap6m); err != nil {
		t.Fatalf("Chtimes 6m failed: %v", err)
	}
	if AcquireLock() {
		ReleaseLock()
		t.Fatalf("expected live PID lock aged 6m not to be stolen")
	}

	// Vieillir au-delà de 10m : doit être considéré obsolète et nettoyé
	old11m := time.Now().Add(-11 * time.Minute)
	if err := os.Chtimes(lockFile, old11m, old11m); err != nil {
		t.Fatalf("Chtimes 11m failed: %v", err)
	}
	if !AcquireLock() {
		t.Fatalf("expected lock aged 11m without heartbeat to be reclaimed")
	}
	ReleaseLock()
}

func TestIsHorizonProcessSelf(t *testing.T) {
	if !isHorizonProcess(os.Getpid()) {
		t.Fatalf("expected isHorizonProcess(os.Getpid()) to return true")
	}
	if isHorizonProcess(0) {
		t.Fatalf("expected isHorizonProcess(0) to return false")
	}
}

func TestAcquireLockConcurrentExclusion(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HORIZON_DATA_DIR", tmpDir)

	const workers = 12
	var acquiredCount atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if AcquireLock() {
				acquiredCount.Add(1)
				time.Sleep(30 * time.Millisecond)
				ReleaseLock()
			}
		}()
	}

	close(start)
	wg.Wait()

	if got := acquiredCount.Load(); got != 1 {
		t.Fatalf("expected exactly 1 concurrent goroutine to acquire lock, got %d", got)
	}
}

