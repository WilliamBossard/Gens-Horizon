package ziputils

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/WilliamBossard/Gens-Horizon/scanner"
)

// VerifyZipIntegrity vÃ©rifie la signature magique ZIP (0x50 0x4B 0x03 0x04) et le Central Directory
func VerifyZipIntegrity(zipPath string) error {
	f, err := os.Open(zipPath)
	if err != nil {
		return fmt.Errorf("impossible d'ouvrir le fichier zip : %w", err)
	}
	defer f.Close()

	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return errors.New("fichier trop court pour Ãªtre une archive ZIP")
	}

	if magic[0] != 0x50 || magic[1] != 0x4B || magic[2] != 0x03 || magic[3] != 0x04 {
		return fmt.Errorf("signature ZIP invalide (reÃ§u : 0x%02X%02X%02X%02X)", magic[0], magic[1], magic[2], magic[3])
	}

	// Validation du Central Directory via archive/zip standard
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("archive ZIP tronquÃ©e ou corrompue : %w", err)
	}
	_ = zr.Close()
	return nil
}

// ExtractZip extrait de maniÃ¨re sÃ©curisÃ©e une archive (protection Zip Slip stricte)
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

// CreateFullZip compresse l'intÃ©gralitÃ© d'un dossier d'instance en ignorant les dossiers exclus
func CreateFullZip(sourceFolder string, destZipPath string, onProgress func(pct int)) error {
	// Calcul de la taille totale pour la progression
	var totalBytes int64
	_ = filepath.Walk(sourceFolder, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if p != sourceFolder && scanner.IgnoredDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !scanner.IgnoredDirs[info.Name()] {
			totalBytes += info.Size()
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

	var processedBytes int64
	lastPct := -1

	err = filepath.Walk(sourceFolder, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if p != sourceFolder && scanner.IgnoredDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if scanner.IgnoredDirs[info.Name()] {
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
		header.Method = zip.Deflate

		w, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}

		inFile, err := os.Open(p)
		if err != nil {
			return nil // Fichier disparu ou inaccessible pendant la compression
		}
		defer inFile.Close()

		buf := make([]byte, 64*1024)
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

// CreateDeltaZip crÃ©e une archive contenant uniquement les fichiers spÃ©cifiÃ©s (ajoutÃ©s / modifiÃ©s)
func CreateDeltaZip(sourceFolder string, files []string, destZipPath string, onProgress func(pct int)) error {
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
		header.Method = zip.Deflate

		w, err := zw.CreateHeader(header)
		if err != nil {
			continue
		}

		inFile, err := os.Open(fullPath)
		if err != nil {
			continue
		}

		buf := make([]byte, 64*1024)
		for {
			n, rErr := inFile.Read(buf)
			if n > 0 {
				if _, wErr := w.Write(buf[:n]); wErr != nil {
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
		inFile.Close()
	}

	return nil
}

// ApplyDelta extrait les fichiers modifiÃ©s et supprime les fichiers supprimÃ©s
func ApplyDelta(deltaZipPath string, targetFolder string, deletedFiles []string, onProgress func(pct int)) error {
	// 1. Extraction des fichiers du delta
	if err := ExtractZip(deltaZipPath, targetFolder, onProgress); err != nil {
		return err
	}

	// 2. Suppression des fichiers marquÃ©s supprimÃ©s
	for _, rel := range deletedFiles {
		fullPath := filepath.Join(targetFolder, filepath.FromSlash(rel))
		_ = os.Remove(fullPath)
	}

	return nil
}

