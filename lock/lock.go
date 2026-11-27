package lock

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/ipc"
	"github.com/WilliamBossard/Gens-Horizon/paths"
)

var (
	lockMu         sync.Mutex
	heartbeatDone  chan struct{}
	activeLockPath string
	activeOSLock   *os.File
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

const heartbeatStaleTimeout = 10 * time.Minute

// IsLockStale vérifie si le fichier de lock correspond à un processus mort, recyclé ou à un heartbeat dépassé
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
	if !alive || !isHorizonProcess(pid) {
		return true
	}
	// Si le processus tourne encore, n'invalider qu'après 10 min sans heartbeat (aligné sur ipc-horizon.js)
	return age > heartbeatStaleTimeout
}

// AcquireLock tente d'acquérir le verrou. Renvoie true en cas de succès, false si une instance tourne déjà.
func AcquireLock() bool {
	lockMu.Lock()
	defer lockMu.Unlock()

	lockPath := getLockFilePath()
	osLock, ok := acquireOSLock(lockPath + ".os")
	if !ok {
		return false
	}
	acquired := false
	defer func() {
		if !acquired {
			releaseOSLock(osLock)
		}
	}()

	for attempt := 0; attempt < 5; attempt++ {
		if _, err := os.Stat(lockPath); err == nil {
			if isLockStale(lockPath) {
				cleanupLock := lockPath + ".cleanup"
				if cInfo, cErr := os.Stat(cleanupLock); cErr == nil && time.Since(cInfo.ModTime()) < 5*time.Second {
					time.Sleep(50 * time.Millisecond)
					continue
				} else if cErr == nil {
					_ = os.Remove(cleanupLock)
				}
				cf, cfErr := os.OpenFile(cleanupLock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if cfErr != nil {
					time.Sleep(50 * time.Millisecond)
					continue
				}
				_ = cf.Close()
				// Re-vérifier après acquisition du verrou de nettoyage pour éviter toute race TOCTOU
				if isLockStale(lockPath) {
					ipc.Logf("[lock] Verrou périmé détecté — nettoyage atomique.")
					staleTmp := fmt.Sprintf("%s.stale.%d.%d", lockPath, os.Getpid(), attempt)
					if renErr := os.Rename(lockPath, staleTmp); renErr == nil {
						_ = os.Remove(staleTmp)
					} else if !os.IsNotExist(renErr) {
						_ = os.Remove(cleanupLock)
						time.Sleep(50 * time.Millisecond)
						continue
					}
				}
				_ = os.Remove(cleanupLock)
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

			if heartbeatDone != nil {
				close(heartbeatDone)
				heartbeatDone = nil
				heartbeatWg.Wait()
			}
			activeLockPath = lockPath
			activeOSLock = osLock
			acquired = true
			heartbeatDone = make(chan struct{})
			startHeartbeat(lockPath, heartbeatDone)
			return true
		}

		time.Sleep(50 * time.Millisecond)
	}

	return false
}

var heartbeatWg sync.WaitGroup

func startHeartbeat(lockPath string, done <-chan struct{}) {
	ticker := time.NewTicker(5 * time.Second)
	heartbeatWg.Add(1)
	go func() {
		defer heartbeatWg.Done()
		defer ticker.Stop()
		for {
			select {
			case <-done:
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
		heartbeatWg.Wait()
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
	if activeOSLock != nil {
		releaseOSLock(activeOSLock)
		activeOSLock = nil
	}
	activeLockPath = ""
}
