package scanner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/WilliamBossard/Gens-Horizon/ipc"
)

var IgnoredDirs = map[string]bool{
	"versions":                  true,
	"libraries":                 true,
	"assets":                    true,
	"logs":                      true,
	"crash-reports":             true,
	"cache":                     true,
	"webcache":                  true,
	"temp":                      true,
	"screenshots":               true,
	"backups":                   true,
	".git":                      true,
	"node_modules":              true,
	".mixin.out":                true,
	"session.lock":              true,
	".spotlight-v100":           true,
	".trashes":                  true,
	".fseventsd":                true,
	"$recycle.bin":              true,
	"system volume information": true,
}

var IgnoredFilesExact = map[string]bool{
	".ds_store":    true,
	"thumbs.db":    true,
	"desktop.ini":  true,
	"ehthumbs.db":  true,
	"session.lock": true,
}

// IsIgnoredFile détermine si un fichier doit être exclu du scan (pollution OS ou fichier temporaire)
func IsIgnoredFile(name string) bool {
	lower := strings.ToLower(name)
	if IgnoredFilesExact[lower] {
		return true
	}
	// Métadonnées AppleDouble sur macOS (ex: ._icon.png)
	if strings.HasPrefix(lower, "._") {
		return true
	}
	// Fichiers temporaires de téléchargement ou de verrou
	if strings.HasSuffix(lower, ".tmp") ||
		strings.HasSuffix(lower, ".part") ||
		strings.HasSuffix(lower, ".crdownload") ||
		strings.HasSuffix(lower, ".download") ||
		strings.HasSuffix(lower, ".lck") {
		return true
	}
	return false
}

// FlexibleInt64 gère la désérialisation JSON d'entiers, de nombres à virgule flottante (mtimeMs hérité de Node.js) ou de chaînes
type FlexibleInt64 int64

func (f *FlexibleInt64) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == "" {
		*f = 0
		return nil
	}
	s = strings.Trim(s, "\"")

	// Essai en tant qu'entier
	if val, err := strconv.ParseInt(s, 10, 64); err == nil {
		*f = FlexibleInt64(val)
		return nil
	}

	// Essai en tant que float64 (ex: 1790268044168.8877 issu de stats.mtimeMs dans Node.js)
	if fval, err := strconv.ParseFloat(s, 64); err == nil {
		*f = FlexibleInt64(int64(fval))
		return nil
	}

	return fmt.Errorf("impossible de convertir %s en FlexibleInt64", string(b))
}

func (f FlexibleInt64) MarshalJSON() ([]byte, error) {
	return strconv.AppendInt(nil, int64(f), 10), nil
}

func (f FlexibleInt64) Int64() int64 {
	return int64(f)
}

type FileEntry struct {
	Hash  string        `json:"hash"`
	Mtime FlexibleInt64 `json:"mtime"` // Milliseconds
	Size  FlexibleInt64 `json:"size"`
}

type Manifest map[string]FileEntry

type ManifestDiff struct {
	Added      []string `json:"added"`
	Modified   []string `json:"modified"`
	Deleted    []string `json:"deleted"`
	HasChanges bool     `json:"hasChanges"`
}

var bufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 64*1024)
		return &b
	},
}

func HashFile(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	bufPtr := bufPool.Get().(*[]byte)
	defer bufPool.Put(bufPtr)

	h := sha256.New()
	if _, err := io.CopyBuffer(h, f, *bufPtr); err != nil {
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

// GenerateManifest scanne récursivement targetDir et génère le Manifest avec WalkDir optimisé
func GenerateManifest(targetDir string, oldManifest Manifest) (Manifest, error) {
	manifest := make(Manifest)
	var manifestMu sync.Mutex

	var tasks []scanTask

	err := filepath.WalkDir(targetDir, func(currentPath string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			ipc.Logf("[scanner] Accès refusé ou erreur : %s (%v)", currentPath, err)
			return nil
		}

		name := d.Name()
		lowerName := strings.ToLower(name)
		if d.IsDir() {
			if currentPath != targetDir && (IgnoredDirs[name] || IgnoredDirs[lowerName]) {
				return filepath.SkipDir
			}
			return nil
		}

		if IgnoredDirs[name] || IgnoredDirs[lowerName] || IsIgnoredFile(name) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		rel, err := filepath.Rel(targetDir, currentPath)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		// Vérification rapide dans le cache du manifest précédent
		mtimeMs := info.ModTime().UnixMilli()
		size := info.Size()

		if oldManifest != nil {
			if cached, ok := oldManifest[rel]; ok {
				if cached.Mtime.Int64() == mtimeMs && cached.Size.Int64() == size {
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

	// Traitement parallèle des hashes via un pool de workers (concurrence max: 16)
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
					ipc.Logf("[scanner] Fichier ignoré (%v) : %s", err, t.fullPath)
					// Conserver l'ancienne entrée en cas d'erreur EBUSY/EPERM
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
					Mtime: FlexibleInt64(t.mtimeMs),
					Size:  FlexibleInt64(t.size),
				}
				manifestMu.Unlock()
			}
		}()
	}
	wg.Wait()

	return manifest, nil
}

// CompareManifests compare deux manifests et retourne les fichiers ajoutés, modifiés et supprimés
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
