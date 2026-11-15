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
	Cleaned   bool        `json:"cleaned,omitempty"`
	Files     []CloudFile `json:"files"`
}

// DeduplicateCloudFiles conserve uniquement la version la plus récente de chaque fichier par nom logique.
func DeduplicateCloudFiles(files []CloudFile) []CloudFile {
	if len(files) <= 1 {
		return append([]CloudFile(nil), files...)
	}
	byName := make(map[string]CloudFile, len(files))
	order := make([]string, 0, len(files))
	for _, f := range files {
		existing, ok := byName[f.Name]
		if !ok {
			byName[f.Name] = f
			order = append(order, f.Name)
			continue
		}
		if existing.ID == f.ID {
			continue
		}
		isFNewer := f.ModifiedTime.After(existing.ModifiedTime) ||
			(f.ModifiedTime.Equal(existing.ModifiedTime) && f.Size > existing.Size)
		if isFNewer {
			byName[f.Name] = f
		}
	}
	out := make([]CloudFile, 0, len(order))
	for _, name := range order {
		out = append(out, byName[name])
	}
	return out
}

func getCloudIndexCachePath(providerName string) string {
	if providerName == "" {
		providerName = "google"
	}
	return filepath.Join(paths.GetCacheDir(), fmt.Sprintf("cloud_index_%s.json", providerName))
}

func readCloudIndexEntry(providerName string, maxAge time.Duration) (cloudIndexCacheEntry, bool) {
	cachePath := getCloudIndexCachePath(providerName)
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return cloudIndexCacheEntry{}, false
	}

	var entry cloudIndexCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return cloudIndexCacheEntry{}, false
	}

	if entry.Provider != providerName {
		return cloudIndexCacheEntry{}, false
	}

	age := time.Since(time.UnixMilli(entry.Timestamp))
	if age < 0 || age > maxAge {
		return cloudIndexCacheEntry{}, false
	}

	return entry, true
}

// GetCachedCloudIndex lit l'index Cloud depuis le cache local s'il a moins de maxAge
func GetCachedCloudIndex(providerName string, maxAge time.Duration) ([]CloudFile, bool) {
	entry, ok := readCloudIndexEntry(providerName, maxAge)
	if !ok {
		return nil, false
	}
	return entry.Files, true
}

// GetCleanedCloudIndex lit l'index Cloud uniquement s'il provient d'une vue déjà dédupliquée.
func GetCleanedCloudIndex(providerName string, maxAge time.Duration) ([]CloudFile, bool) {
	entry, ok := readCloudIndexEntry(providerName, maxAge)
	if !ok || !entry.Cleaned {
		return nil, false
	}
	return DeduplicateCloudFiles(entry.Files), true
}

// SaveCachedCloudIndex enregistre l'index Cloud dans le cache éphémère sur disque (dédupliqué).
func SaveCachedCloudIndex(providerName string, files []CloudFile) {
	SaveCloudIndexCache(providerName, DeduplicateCloudFiles(files), true)
}

// SaveCloudIndexCache enregistre l'index Cloud en précisant si les doublons distants ont déjà été purgés.
func SaveCloudIndexCache(providerName string, files []CloudFile, cleaned bool) {
	if files == nil {
		files = []CloudFile{}
	} else if cleaned {
		files = DeduplicateCloudFiles(files)
	}
	cachePath := getCloudIndexCachePath(providerName)
	entry := cloudIndexCacheEntry{
		Timestamp: time.Now().UnixMilli(),
		Provider:  providerName,
		Cleaned:   cleaned,
		Files:     files,
	}
	if b, err := json.Marshal(entry); err == nil {
		if tmpFile, err := os.CreateTemp(filepath.Dir(cachePath), filepath.Base(cachePath)+".tmp.*"); err == nil {
			tmpPath := tmpFile.Name()
			if _, wErr := tmpFile.Write(b); wErr == nil {
				if cErr := tmpFile.Close(); cErr == nil {
					if rErr := os.Rename(tmpPath, cachePath); rErr == nil {
						return
					}
				}
			} else {
				_ = tmpFile.Close()
			}
			_ = os.Remove(tmpPath)
		}
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

