package providers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/paths"
)

func effectiveMaxRetries() int {
	n := paths.LoadSettings().MaxRetries
	if n < 1 {
		return 3
	}
	if n > 10 {
		return 10
	}
	return n
}

type uploadSessionEntry struct {
	URL       string `json:"url"`
	ExpiresAt int64  `json:"expiresAt"`
}

var uploadSessionMu sync.Mutex

func getUploadSessionsPath() string {
	return filepath.Join(paths.GetCacheDir(), "upload_sessions.json")
}

func computeUploadSessionKey(provider, remoteName, srcPath string, fileSize int64) string {
	f, err := os.Open(srcPath)
	if err != nil {
		return fmt.Sprintf("%s:%s:%d", provider, remoteName, fileSize)
	}
	defer f.Close()
	h := sha256.New()
	_, _ = io.Copy(h, f)
	return fmt.Sprintf("%s:%s:%d:%s", provider, remoteName, fileSize, hex.EncodeToString(h.Sum(nil)))
}

func loadUploadSession(key string) (string, bool) {
	if key == "" {
		return "", false
	}
	uploadSessionMu.Lock()
	defer uploadSessionMu.Unlock()

	data, err := os.ReadFile(getUploadSessionsPath())
	if err != nil {
		return "", false
	}
	var sessions map[string]uploadSessionEntry
	if err := json.Unmarshal(data, &sessions); err != nil {
		return "", false
	}
	entry, ok := sessions[key]
	if !ok || entry.URL == "" || time.Now().Unix() >= entry.ExpiresAt {
		return "", false
	}
	return entry.URL, true
}

func saveUploadSession(key, sessionURL string, ttl time.Duration) {
	if key == "" || sessionURL == "" || ttl <= 0 {
		return
	}
	uploadSessionMu.Lock()
	defer uploadSessionMu.Unlock()

	p := getUploadSessionsPath()
	_ = os.MkdirAll(filepath.Dir(p), 0700)
	sessions := make(map[string]uploadSessionEntry)
	if data, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(data, &sessions)
	}
	now := time.Now().Unix()
	for k, v := range sessions {
		if now >= v.ExpiresAt {
			delete(sessions, k)
		}
	}
	sessions[key] = uploadSessionEntry{
		URL:       sessionURL,
		ExpiresAt: time.Now().Add(ttl).Unix(),
	}
	writeUploadSessionsLocked(p, sessions)
}

func clearUploadSession(key string) {
	if key == "" {
		return
	}
	uploadSessionMu.Lock()
	defer uploadSessionMu.Unlock()

	p := getUploadSessionsPath()
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var sessions map[string]uploadSessionEntry
	if err := json.Unmarshal(data, &sessions); err != nil {
		return
	}
	if _, ok := sessions[key]; !ok {
		return
	}
	delete(sessions, key)
	writeUploadSessionsLocked(p, sessions)
}

func writeUploadSessionsLocked(p string, sessions map[string]uploadSessionEntry) {
	b, err := json.MarshalIndent(sessions, "", "  ")
	if err != nil {
		return
	}
	tmp := fmt.Sprintf("%s.tmp.%d.%d", p, os.Getpid(), time.Now().UnixNano())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return
	}
	_, wErr := f.Write(b)
	sErr := f.Sync()
	cErr := f.Close()
	if wErr == nil && sErr == nil && cErr == nil {
		if rErr := os.Rename(tmp, p); rErr == nil {
			return
		}
	}
	_ = os.Remove(tmp)
}
