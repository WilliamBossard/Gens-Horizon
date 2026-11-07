package scanner

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/WilliamBossard/Gens-Horizon/ipc"
)

var IgnoredDirs = map[string]bool{
	"versions":      true,
	"libraries":     true,
	"assets":        true,
	"logs":          true,
	"crash-reports": true,
	"cache":         true,
	"webcache":      true,
	"temp":          true,
	"screenshots":   true,
	"backups":       true,
	".git":          true,
	"node_modules":  true,
	".mixin.out":    true,
	"session.lock":  true,
}

type FileEntry struct {
	Hash  string `json:"hash"`
	Mtime int64  `json:"mtime"` // Milliseconds
	Size  int64  `json:"size"`
}

type Manifest map[string]FileEntry

type ManifestDiff struct {
	Added      []string `json:"added"`
	Modified   []string `json:"modified"`
	Deleted    []string `json:"deleted"`
	HasChanges bool     `json:"hasChanges"`
}

func HashFile(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	buf := make([]byte, 64*1024)
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type scanTask struct {
	fullPath string
	relPath  string
	size     int64
	mtimeMs  int64
}

// GenerateManifest scanne rÃ©cursivement targetDir et gÃ©nÃ¨re le Manifest
func GenerateManifest(targetDir string, oldManifest Manifest) (Manifest, error) {
	manifest := make(Manifest)
	var manifestMu sync.Mutex

	var tasks []scanTask

	err := filepath.Walk(targetDir, func(currentPath string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			ipc.Logf("[scanner] AccÃ¨s refusÃ© ou erreur : %s (%v)", currentPath, err)
			return nil
		}

		name := info.Name()
		if info.IsDir() {
			if currentPath != targetDir && IgnoredDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}

		if IgnoredDirs[name] {
			return nil
		}

		rel, err := filepath.Rel(targetDir, currentPath)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		// VÃ©rification rapide dans le cache du manifest prÃ©cÃ©dent
		mtimeMs := info.ModTime().UnixMilli()
		size := info.Size()

		if oldManifest != nil {
			if cached, ok := oldManifest[rel]; ok {
				if cached.Mtime == mtimeMs && cached.Size == size {
					manifestMu.Lock()
					manifest[rel] = cached
					manifestMu.Unlock()
					return nil
				}
			}
		}

		tasks = append(tasks, scanTask{
			fullPath: currentPath,
			relPath:  rel,
			size:     size,
			mtimeMs:  mtimeMs,
		})
		return nil
	})

	if err != nil && !os.IsNotExist(err) {
		return manifest, err
	}

	if len(tasks) == 0 {
		return manifest, nil
	}

	// Traitement parallÃ¨le des hashes via un pool de workers (concurrence max: 16)
	workerCount := 16
	if len(tasks) < workerCount {
		workerCount = len(tasks)
	}

	taskChan := make(chan scanTask, len(tasks))
	for _, t := range tasks {
		taskChan <- t
	}
	close(taskChan)

	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range taskChan {
				hash, err := HashFile(t.fullPath)
				if err != nil {
					ipc.Logf("[scanner] Fichier ignorÃ© (%v) : %s", err, t.fullPath)
					// Conserver l'ancienne entrÃ©e en cas d'erreur EBUSY/EPERM
					if oldManifest != nil {
						if oldEntry, ok := oldManifest[t.relPath]; ok {
							manifestMu.Lock()
							manifest[t.relPath] = oldEntry
							manifestMu.Unlock()
						}
					}
					continue
				}

				manifestMu.Lock()
				manifest[t.relPath] = FileEntry{
					Hash:  hash,
					Mtime: t.mtimeMs,
					Size:  t.size,
				}
				manifestMu.Unlock()
			}
		}()
	}
	wg.Wait()

	return manifest, nil
}

// CompareManifests compare deux manifests et retourne les fichiers ajoutÃ©s, modifiÃ©s et supprimÃ©s
func CompareManifests(oldM, newM Manifest) ManifestDiff {
	diff := ManifestDiff{
		Added:    []string{},
		Modified: []string{},
		Deleted:  []string{},
	}

	if oldM == nil {
		oldM = make(Manifest)
	}
	if newM == nil {
		newM = make(Manifest)
	}

	for f, newEntry := range newM {
		if oldEntry, exists := oldM[f]; !exists {
			diff.Added = append(diff.Added, f)
		} else if oldEntry.Hash != newEntry.Hash {
			diff.Modified = append(diff.Modified, f)
		}
	}

	for f := range oldM {
		if _, exists := newM[f]; !exists {
			diff.Deleted = append(diff.Deleted, f)
		}
	}

	diff.HasChanges = len(diff.Added) > 0 || len(diff.Modified) > 0 || len(diff.Deleted) > 0
	return diff
}
