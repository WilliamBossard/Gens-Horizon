package ziputils

import (
	"archive/zip"
	"compress/flate"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/scanner"
)

var zipBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 64*1024)
		return &b
	},
}

// isPrecompressed détermine si un fichier est déjà compressé afin d'utiliser zip.Store (copie directe sans CPU)
func isPrecompressed(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".jar", ".zip", ".gz", ".png", ".jpg", ".jpeg", ".ogg", ".mp3", ".webp", ".7z", ".rar":
		return true
	default:
		return false
	}
}

// VerifyZipIntegrity vérifie la signature magique ZIP (0x50 0x4B 0x03 0x04) et le Central Directory
func VerifyZipIntegrity(zipPath string) error {
	f, err := os.Open(zipPath)
	if err != nil {
		return fmt.Errorf("impossible d'ouvrir le fichier zip : %w", err)
	}
	defer f.Close()

	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return errors.New("fichier trop court pour être une archive ZIP")
	}

	if magic[0] != 0x50 || magic[1] != 0x4B || magic[2] != 0x03 || magic[3] != 0x04 {
		return fmt.Errorf("signature ZIP invalide (reçu : 0x%02X%02X%02X%02X)", magic[0], magic[1], magic[2], magic[3])
	}

	// Validation du Central Directory via archive/zip standard
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("archive ZIP tronquée ou corrompue : %w", err)
	}
	_ = zr.Close()
	return nil
}

// ExtractZip extrait de manière sécurisée une archive (protection Zip Slip stricte)
func ExtractZip(zipPath string, targetPath string, onProgress func(pct int)) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("archive corrompue : %w", err)
	}
	defer zr.Close()

	cleanTarget := filepath.Clean(targetPath)
	total := len(zr.File)
	count := 0

	for _, zf := range zr.File {
		destPath := filepath.Join(cleanTarget, zf.Name)
		cleanDest := filepath.Clean(destPath)

		// Protection anti Zip-Slip
		if !strings.HasPrefix(cleanDest, cleanTarget+string(filepath.Separator)) && cleanDest != cleanTarget {
			continue // Fichier malveillant tentant de s'extraire hors du dossier cible
		}

		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(cleanDest, 0755); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(cleanDest), 0755); err != nil {
			return err
		}

		rc, err := zf.Open()
		if err != nil {
			return err
		}

		outFile, err := os.OpenFile(cleanDest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, zf.Mode())
		if err != nil {
			_ = rc.Close()
			return err
		}

		_, err = io.Copy(outFile, rc)
		_ = outFile.Close()
		_ = rc.Close()
		if err != nil {
			return err
		}

		count++
		if onProgress != nil && total > 0 {
			pct := (count * 100) / total
			if pct > 99 {
				pct = 99
			}
			onProgress(pct)
		}
	}

	if onProgress != nil {
		onProgress(100)
	}
	return nil
}

// CreateFullZip compresse l'intégralité d'un dossier d'instance en ignorant les dossiers exclus
func CreateFullZip(sourceFolder string, destZipPath string, onProgress func(pct int)) error {
	// Calcul de la taille totale pour la progression via WalkDir rapide
	var totalBytes int64
	_ = filepath.WalkDir(sourceFolder, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != sourceFolder && scanner.IgnoredDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !scanner.IgnoredDirs[d.Name()] {
			if info, err := d.Info(); err == nil {
				totalBytes += info.Size()
			}
		}
		return nil
	})

	outFile, err := os.Create(destZipPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	zw := zip.NewWriter(outFile)
	defer zw.Close()

	// Optimisation : compression niveau 1 (BestSpeed) pour accélérer par 3 à 5x les sauvegardes
	zw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.BestSpeed)
	})

	var processedBytes int64
	lastPct := -1

	err = filepath.WalkDir(sourceFolder, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != sourceFolder && scanner.IgnoredDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if scanner.IgnoredDirs[d.Name()] {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		rel, err := filepath.Rel(sourceFolder, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = rel
		if isPrecompressed(header.Name) {
			header.Method = zip.Store
		} else {
			header.Method = zip.Deflate
		}

		w, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}

		inFile, err := os.Open(p)
		if err != nil {
			return nil // Fichier disparu ou inaccessible pendant la compression
		}
		defer inFile.Close()

		bufPtr := zipBufPool.Get().(*[]byte)
		defer zipBufPool.Put(bufPtr)
		buf := *bufPtr

		for {
			n, rErr := inFile.Read(buf)
			if n > 0 {
				if _, wErr := w.Write(buf[:n]); wErr != nil {
					return wErr
				}
				processedBytes += int64(n)
				if onProgress != nil && totalBytes > 0 {
					pct := int((processedBytes * 100) / totalBytes)
					if pct > 100 {
						pct = 100
					}
					if pct != lastPct && (pct >= lastPct+2 || pct == 100) {
						onProgress(pct)
						lastPct = pct
					}
				}
			}
			if rErr != nil {
				break
			}
		}
		return nil
	})

	return err
}

type DeltaMetadata struct {
	DeletedFiles []string `json:"deletedFiles"`
	CreatedAt    string   `json:"createdAt"`
}

// CreateDeltaZip crée une archive contenant uniquement les fichiers spécifiés (ajoutés / modifiés) ainsi que __delta__.json
func CreateDeltaZip(sourceFolder string, files []string, deletedFiles []string, destZipPath string, onProgress func(pct int)) error {
	var totalBytes int64
	for _, rel := range files {
		fullPath := filepath.Join(sourceFolder, filepath.FromSlash(rel))
		if info, err := os.Stat(fullPath); err == nil && !info.IsDir() {
			totalBytes += info.Size()
		}
	}

	outFile, err := os.Create(destZipPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	zw := zip.NewWriter(outFile)
	defer zw.Close()

	// Optimisation : compression niveau 1 (BestSpeed) pour des deltas ultra-rapides
	zw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.BestSpeed)
	})

	// 1. Écriture du fichier __delta__.json pour la traçabilité des suppressions
	if deletedFiles == nil {
		deletedFiles = []string{}
	}
	meta := DeltaMetadata{
		DeletedFiles: deletedFiles,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	if metaBytes, err := json.MarshalIndent(meta, "", "  "); err == nil {
		if w, err := zw.Create("__delta__.json"); err == nil {
			_, _ = w.Write(metaBytes)
		}
	}

	var processedBytes int64
	lastPct := -1

	for _, rel := range files {
		fullPath := filepath.Join(sourceFolder, filepath.FromSlash(rel))
		info, err := os.Stat(fullPath)
		if err != nil || info.IsDir() {
			continue
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			continue
		}
		header.Name = filepath.ToSlash(rel)
		if isPrecompressed(header.Name) {
			header.Method = zip.Store
		} else {
			header.Method = zip.Deflate
		}

		w, err := zw.CreateHeader(header)
		if err != nil {
			continue
		}

		inFile, err := os.Open(fullPath)
		if err != nil {
			continue
		}

		bufPtr := zipBufPool.Get().(*[]byte)
		buf := *bufPtr

		for {
			n, rErr := inFile.Read(buf)
			if n > 0 {
				if _, wErr := w.Write(buf[:n]); wErr != nil {
					zipBufPool.Put(bufPtr)
					inFile.Close()
					return wErr
				}
				processedBytes += int64(n)
				if onProgress != nil && totalBytes > 0 {
					pct := int((processedBytes * 100) / totalBytes)
					if pct > 100 {
						pct = 100
					}
					if pct != lastPct && (pct >= lastPct+2 || pct == 100) {
						onProgress(pct)
						lastPct = pct
					}
				}
			}
			if rErr != nil {
				break
			}
		}
		zipBufPool.Put(bufPtr)
		inFile.Close()
	}

	return nil
}

// ApplyDelta extrait les fichiers modifiés et supprime les fichiers supprimés indiqués dans __delta__.json
func ApplyDelta(deltaZipPath string, targetFolder string, onProgress func(pct int)) error {
	zr, err := zip.OpenReader(deltaZipPath)
	if err != nil {
		return fmt.Errorf("archive delta corrompue : %w", err)
	}
	defer zr.Close()

	cleanTarget := filepath.Clean(targetFolder)
	var deletedFiles []string
	total := len(zr.File)
	count := 0

	for _, zf := range zr.File {
		// Interception de __delta__.json pour récupérer la liste des suppressions sans l'extraire sur le disque
		if zf.Name == "__delta__.json" {
			rc, err := zf.Open()
			if err == nil {
				data, err := io.ReadAll(rc)
				_ = rc.Close()
				if err == nil {
					var meta DeltaMetadata
					if err := json.Unmarshal(data, &meta); err == nil {
						deletedFiles = meta.DeletedFiles
					}
				}
			}
			count++
			continue
		}

		destPath := filepath.Join(cleanTarget, zf.Name)
		cleanDest := filepath.Clean(destPath)

		// Protection anti Zip-Slip
		if !strings.HasPrefix(cleanDest, cleanTarget+string(filepath.Separator)) && cleanDest != cleanTarget {
			continue
		}

		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(cleanDest, 0755); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(cleanDest), 0755); err != nil {
			return err
		}

		rc, err := zf.Open()
		if err != nil {
			return err
		}

		outFile, err := os.OpenFile(cleanDest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, zf.Mode())
		if err != nil {
			_ = rc.Close()
			return err
		}

		_, err = io.Copy(outFile, rc)
		_ = outFile.Close()
		_ = rc.Close()
		if err != nil {
			return err
		}

		count++
		if onProgress != nil && total > 0 {
			pct := (count * 100) / total
			if pct > 99 {
				pct = 99
			}
			onProgress(pct)
		}
	}

	// Suppression des fichiers marqués supprimés
	for _, rel := range deletedFiles {
		absPath := filepath.Join(cleanTarget, filepath.FromSlash(rel))
		cleanAbs := filepath.Clean(absPath)
		if strings.HasPrefix(cleanAbs, cleanTarget+string(filepath.Separator)) {
			_ = os.Remove(cleanAbs)
		}
	}

	if onProgress != nil {
		onProgress(100)
	}

	return nil
}

