package providers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/paths"
)

const DefaultCloudIndexTTL = 45 * time.Second

type cloudIndexCacheEntry struct {
	Timestamp int64       `json:"timestamp"`
	Provider  string      `json:"provider"`
	Files     []CloudFile `json:"files"`
}

func getCloudIndexCachePath(providerName string) string {
	if providerName == "" {
		providerName = "google"
	}
	return filepath.Join(paths.GetCacheDir(), fmt.Sprintf("cloud_index_%s.json", providerName))
}

// GetCachedCloudIndex lit l'index Cloud depuis le cache local s'il a moins de maxAge
func GetCachedCloudIndex(providerName string, maxAge time.Duration) ([]CloudFile, bool) {
	cachePath := getCloudIndexCachePath(providerName)
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, false
	}

	var entry cloudIndexCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, false
	}

	if entry.Provider != providerName {
		return nil, false
	}

	age := time.Since(time.UnixMilli(entry.Timestamp))
	if age < 0 || age > maxAge {
		return nil, false
	}

	return entry.Files, true
}

// SaveCachedCloudIndex enregistre l'index Cloud dans le cache éphémère sur disque
func SaveCachedCloudIndex(providerName string, files []CloudFile) {
	cachePath := getCloudIndexCachePath(providerName)
	entry := cloudIndexCacheEntry{
		Timestamp: time.Now().UnixMilli(),
		Provider:  providerName,
		Files:     files,
	}
	if b, err := json.Marshal(entry); err == nil {
		_ = os.WriteFile(cachePath, b, 0644)
	}
}

// InvalidateCloudIndexCache purge immédiatement tous les fichiers de cache d'index Cloud
func InvalidateCloudIndexCache() {
	cacheDir := paths.GetCacheDir()
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "cloud_index_") {
			_ = os.Remove(filepath.Join(cacheDir, e.Name()))
		}
	}
}
