package operations

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/WilliamBossard/Gens-Horizon/scanner"
)

// restoreInPlace restaure les fichiers du snapshot directement dans targetPath sans renommer le dossier racine
// (utile sous Windows lorsque logs/ ou un fichier tiers empêche os.Rename sur la racine).
func restoreInPlace(snapshotDir, targetPath string) error {
	snapshotSet := make(map[string]bool)
	err := filepath.WalkDir(snapshotDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return nil
		}
		rel, err := filepath.Rel(snapshotDir, path)
		if err != nil || rel == "." {
			return err
		}
		snapshotSet[rel] = true
		dest := filepath.Join(targetPath, rel)
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		if d.IsDir() {
			return os.MkdirAll(dest, info.Mode())
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return err
		}
		if info, lErr := os.Lstat(dest); lErr == nil && info.IsDir() {
			_ = os.RemoveAll(dest)
		} else {
			_ = os.Remove(dest)
		}
		return copySingleFile(path, dest, info.Mode())
	})
	if err != nil {
		return err
	}

	var extraPaths []string
	_ = filepath.WalkDir(targetPath, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		rel, err := filepath.Rel(targetPath, path)
		if err != nil || rel == "." {
			return nil
		}
		lower := strings.ToLower(d.Name())
		if d.IsDir() && scanner.IgnoredDirs[lower] {
			return filepath.SkipDir
		}
		if !d.IsDir() && scanner.IsIgnoredFile(d.Name()) {
			return nil
		}
		if !snapshotSet[rel] {
			extraPaths = append(extraPaths, path)
		}
		return nil
	})
	for i := len(extraPaths) - 1; i >= 0; i-- {
		_ = os.Remove(extraPaths[i])
	}
	return nil
}

var rollbackCopyBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 64*1024)
		return &b
	},
}

func copySingleFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	bufPtr := rollbackCopyBufPool.Get().(*[]byte)
	_, copyErr := io.CopyBuffer(out, in, *bufPtr)
	rollbackCopyBufPool.Put(bufPtr)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func copyDir(src, dst string, skipIgnored bool) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if skipIgnored && rel != "." {
			lower := strings.ToLower(d.Name())
			if d.IsDir() && scanner.IgnoredDirs[lower] {
				return filepath.SkipDir
			}
			if !d.IsDir() && scanner.IsIgnoredFile(d.Name()) {
				return nil
			}
		}
		target := filepath.Join(dst, rel)
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		return copySingleFile(path, target, info.Mode())
	})
}

func cleanNonIgnoredEntries(localPath string) error {
	return cleanNonIgnoredEntriesForProfile(localPath, "")
}

func cleanNonIgnoredEntriesForProfile(localPath, profile string) error {
	entries, err := os.ReadDir(localPath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		lower := strings.ToLower(entry.Name())
		if scanner.IgnoredDirs[lower] || (!entry.IsDir() && scanner.IsIgnoredFile(entry.Name())) {
			continue
		}
		if profile != "" && profile != "FULL" && !scanner.IsPathAllowedByProfile(entry.Name(), profile) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(localPath, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
