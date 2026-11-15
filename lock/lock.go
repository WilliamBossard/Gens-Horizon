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

// InspectLock reports whether a lock file currently exists, whether it is stale, and the recorded PID (0 if unavailable).
func InspectLock() (locked bool, stale bool, pid int) {
	lockPath := getLockFilePath()
	if _, err := os.Stat(lockPath); err != nil {
		return false, false, 0
	}
	if data, err := os.ReadFile(lockPath); err == nil {
		if parsed, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil {
			pid = parsed
		}
	}
	return true, isLockStale(lockPath), pid
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
	trimmed := strings.TrimSpace(string(content))
	if trimmed == "" {
		// Fenêtre de grâce brève pendant que le détenteur vient de créer le fichier avant d'y écrire son PID
		return age > 2*time.Second
	}
	pid, err := strconv.Atoi(trimmed)
	if err != nil {
		return true
	}

	alive := isProcessAlive(pid)
	if !alive {
		return true
	}
	// Si le processus tourne encore, n'invalider qu'après 5 min sans heartbeat (tolère une courte mise en veille)
	return age > 5*time.Minute
}

// AcquireLock tente d'acquérir le verrou. Renvoie true en cas de succès, false si une instance tourne déjà.
func AcquireLock() bool {
	lockMu.Lock()
	defer lockMu.Unlock()

	lockPath := getLockFilePath()

	for attempt := 0; attempt < 5; attempt++ {
		if _, err := os.Stat(lockPath); err == nil {
			if isLockStale(lockPath) {
				ipc.Logf("[lock] Verrou périmé détecté — nettoyage atomique.")
				// Renommage atomique avant suppression pour éliminer la race TOCTOU
				// entre deux processus qui détecteraient le même verrou périmé.
				staleTmp := fmt.Sprintf("%s.stale.%d.%d", lockPath, os.Getpid(), attempt)
				if renErr := os.Rename(lockPath, staleTmp); renErr == nil {
					_ = os.Remove(staleTmp)
				} else if !os.IsNotExist(renErr) {
					time.Sleep(50 * time.Millisecond)
					continue
				}
			} else {
				return false
			}
		}

		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			pidStr := fmt.Sprintf("%d", os.Getpid())
			_, writeErr := f.WriteString(pidStr)
			syncErr := f.Sync()
			closeErr := f.Close()
			if writeErr != nil || syncErr != nil || closeErr != nil {
				_ = os.Remove(lockPath)
				time.Sleep(50 * time.Millisecond)
				continue
			}

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

