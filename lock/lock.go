package lock

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/ipc"
	"github.com/WilliamBossard/Gens-Horizon/paths"
)

var (
	lockMu            sync.Mutex
	heartbeatDone     chan struct{}
	activeLockPath    string
	signalHandlerOnce sync.Once
)

func getLockFilePath() string {
	return filepath.Join(paths.GetHorizonDataDir(), "horizon.lock")
}

// IsLockStale vérifie si le fichier de lock correspond à un processus mort ou à un heartbeat dépassé
func isLockStale(lockPath string) bool {
	info, err := os.Stat(lockPath)
	if err != nil {
		return true
	}
	age := time.Since(info.ModTime())

	content, err := os.ReadFile(lockPath)
	if err != nil {
		return true
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil {
		return true
	}

	alive := isProcessAlive(pid)
	if !alive {
		return true
	}
	// Si le processus tourne encore mais que le heartbeat n'a pas été mis à jour depuis 30s
	return age > 30*time.Second
}

// AcquireLock tente d'acquérir le verrou. Renvoie true en cas de succès, false si une instance tourne déjà.
func AcquireLock() bool {
	lockMu.Lock()
	defer lockMu.Unlock()

	lockPath := getLockFilePath()

	for attempt := 0; attempt < 5; attempt++ {
		if _, err := os.Stat(lockPath); err == nil {
			if isLockStale(lockPath) {
				ipc.Logf("[lock] Verrou périmé détecté — nettoyage.")
				_ = os.Remove(lockPath)
			} else {
				return false
			}
		}

		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err == nil {
			pidStr := fmt.Sprintf("%d", os.Getpid())
			_, _ = f.WriteString(pidStr)
			_ = f.Close()

			activeLockPath = lockPath
			startHeartbeat(lockPath)
			setupSignalHandler()
			return true
		}

		time.Sleep(50 * time.Millisecond)
	}

	return false
}

func startHeartbeat(lockPath string) {
	heartbeatDone = make(chan struct{})
	ticker := time.NewTicker(5 * time.Second)

	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatDone:
				return
			case t := <-ticker.C:
				_ = os.Chtimes(lockPath, t, t)
			}
		}
	}()
}

// ReleaseLock libère le verrou si le PID correspond
func ReleaseLock() {
	lockMu.Lock()
	defer lockMu.Unlock()

	if heartbeatDone != nil {
		close(heartbeatDone)
		heartbeatDone = nil
	}

	if activeLockPath == "" {
		activeLockPath = getLockFilePath()
	}

	content, err := os.ReadFile(activeLockPath)
	if err == nil {
		pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
		if err == nil && pid == os.Getpid() {
			_ = os.Remove(activeLockPath)
		}
	}
	activeLockPath = ""
}

func setupSignalHandler() {
	signalHandlerOnce.Do(func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-sigChan
			ReleaseLock()
			os.Exit(0)
		}()
	})
}

