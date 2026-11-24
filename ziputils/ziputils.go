package ziputils

import (
	"archive/zip"
	"bufio"
	"compress/flate"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/ipc"
	"github.com/WilliamBossard/Gens-Horizon/scanner"
)

const (
	maxZipExtractEntries    = 50000
	maxZipExtractTotalBytes = 8 * 1024 * 1024 * 1024 // 8 Go max décompressé
	maxZipSingleFileBytes   = 2 * 1024 * 1024 * 1024 // 2 Go max par entrée
	maxDeltaMetaBytes       = 10 * 1024 * 1024       // 10 Mo max pour __delta__.json
)

var zipBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 64*1024)
		return &b
	},
}

var windowsReservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

func isWindowsReservedSegment(seg string) bool {
	trimmed := strings.TrimRight(seg, " .")
	if trimmed == "" {
		return false
	}
	base := trimmed
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	return windowsReservedNames[strings.ToLower(base)]
}

// isZipEntrySafeUnder vérifie via filepath.Rel qu'une entrée ZIP reste strictement sous cleanTarget
// et ne contient ni ':' (ADS/lecteur Windows) ni nom de périphérique réservé Windows.
func isZipEntrySafeUnder(cleanTarget, entryName string) (string, bool) {
	if strings.ContainsAny(entryName, ":\x00") {
		return "", false
	}
	normalized := strings.ReplaceAll(entryName, "\\", "/")
	for _, seg := range strings.Split(normalized, "/") {
		if seg == "" || seg == "." {
			continue
		}
		if seg == ".." || isWindowsReservedSegment(seg) {
			return "", false
		}
	}
	destPath := filepath.Join(cleanTarget, filepath.FromSlash(normalized))
	cleanDest := filepath.Clean(destPath)
	rel, err := filepath.Rel(cleanTarget, cleanDest)
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return cleanDest, true
}

// resolveNearestRealDir vérifie avant toute création de dossier que le plus proche ancêtre existant
// sous cleanTargetReal n'est pas un lien symbolique ou une jonction pointant hors de la racine cible.
func resolveNearestRealDir(cleanTargetReal, candidateDir string) error {
	cur := filepath.Clean(candidateDir)
	for {
		info, err := os.Lstat(cur)
		if err == nil {
			if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
				return fmt.Errorf("lien symbolique ou jonction détecté dans le dossier cible : %s", cur)
			}
			realCur, rErr := filepath.EvalSymlinks(cur)
			if rErr != nil {
				return rErr
			}
			rel, relErr := filepath.Rel(cleanTargetReal, realCur)
			if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("évasion via symlink/jonction hors du dossier cible : %s -> %s", cur, realCur)
			}
			return nil
		}
		if !os.IsNotExist(err) {
			return err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return nil
		}
		cur = parent
	}
}

func ensureSafeDestDir(cleanTargetReal, dirPath string) error {
	if err := resolveNearestRealDir(cleanTargetReal, dirPath); err != nil {
		return err
	}
	return os.MkdirAll(dirPath, 0755)
}

func removeFileWithRetry(path string) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		err := os.Remove(path)
		if err == nil || os.IsNotExist(err) {
			return nil
		}
		lastErr = err
		time.Sleep(time.Duration(attempt+1) * 40 * time.Millisecond)
	}
	return lastErr
}

func pruneEmptyParentDirs(cleanTarget, startDir string) {
	cur := filepath.Clean(startDir)
	for cur != cleanTarget && cur != "." && cur != string(filepath.Separator) {
		rel, err := filepath.Rel(cleanTarget, cur)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			break
		}
		entries, rErr := os.ReadDir(cur)
		if rErr != nil || len(entries) > 0 {
			break
		}
		if remErr := os.Remove(cur); remErr != nil {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
}

func zipDedupKey(cleanDest string) string {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.ToLower(cleanDest)
	}
	return cleanDest
}

// sanitizeZipFileMode supprime setuid/setgid/sticky et garantit un mode fichier valide.
func sanitizeZipFileMode(mode os.FileMode) os.FileMode {
	perm := mode.Perm() &^ (os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
	if perm == 0 {
		return 0644
	}
	return perm | 0600
}

// extractZipEntriesParallel extrait une liste validée d'entrées ZIP en parallèle avec limites anti-Zip-Bomb et progression throttlée.
func extractZipEntriesParallel(cleanTarget string, filesToExtract []*zip.File, onProgress func(pct int)) error {
	return extractZipEntriesParallelWithLimits(cleanTarget, filesToExtract, maxZipExtractTotalBytes, maxZipSingleFileBytes, onProgress)
}

type atomicBudgetReader struct {
	r           io.Reader
	total       *atomic.Int64
	maxTotal    int64
	maxSingle   int64
	singleRead  int64
	entryName   string
	exceededErr error
}

func (br *atomicBudgetReader) Read(p []byte) (int, error) {
	n, err := br.r.Read(p)
	if n > 0 {
		br.singleRead += int64(n)
		if br.singleRead > br.maxSingle {
			br.exceededErr = fmt.Errorf("fichier ZIP trop volumineux (%s > %d octets)", br.entryName, br.maxSingle)
			return n, br.exceededErr
		}
		if br.total.Add(int64(n)) > br.maxTotal {
			br.exceededErr = fmt.Errorf("archive ZIP trop volumineuse après décompression (> %d octets)", br.maxTotal)
			return n, br.exceededErr
		}
	}
	return n, err
}

func extractZipEntriesParallelWithLimits(cleanTarget string, filesToExtract []*zip.File, maxTotalBytes int64, maxSingleBytes int64, onProgress func(pct int)) error {
	total := len(filesToExtract)
	if total == 0 {
		if onProgress != nil {
			onProgress(100)
		}
		return nil
	}

	workerCount := runtime.NumCPU() * 2
	if workerCount < 4 {
		workerCount = 4
	}
	if workerCount > 16 {
		workerCount = 16
	}
	if workerCount > total {
		workerCount = total
	}

	taskChan := make(chan *zip.File, total)
	for _, zf := range filesToExtract {
		taskChan <- zf
	}
	close(taskChan)

	var extractedCount atomic.Int64
	var totalExtractedBytes atomic.Int64
	var lastReportedPct atomic.Int32
	lastReportedPct.Store(-1)

	var firstErr error
	var errMu sync.Mutex
	setErr := func(err error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
	}

	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for zf := range taskChan {
				errMu.Lock()
				hasErr := firstErr != nil
				errMu.Unlock()
				if hasErr {
					return
				}

				cleanDest, ok := isZipEntrySafeUnder(cleanTarget, zf.Name)
				if !ok || cleanDest == cleanTarget {
					continue
				}

				if totalExtractedBytes.Load() >= maxTotalBytes {
					setErr(fmt.Errorf("archive ZIP trop volumineuse après décompression (> %d octets)", maxTotalBytes))
					return
				}

				rc, err := zf.Open()
				if err != nil {
					setErr(err)
					return
				}

				// Détache tout hardlink ou symlink éventuel (protection du snapshot rollback et anti-traversée) avant d'écrire
				if info, lErr := os.Lstat(cleanDest); lErr == nil && info.IsDir() {
					if rmDirErr := os.RemoveAll(cleanDest); rmDirErr != nil {
						_ = rc.Close()
						setErr(fmt.Errorf("impossible de remplacer le dossier %s : %w", cleanDest, rmDirErr))
						return
					}
				} else if rmErr := removeFileWithRetry(cleanDest); rmErr != nil {
					_ = rc.Close()
					setErr(fmt.Errorf("impossible de remplacer %s : %w", cleanDest, rmErr))
					return
				}
				outFile, err := os.OpenFile(cleanDest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, sanitizeZipFileMode(zf.Mode()))
				if err != nil {
					_ = rc.Close()
					setErr(err)
					return
				}

				if totalExtractedBytes.Load() >= maxTotalBytes {
					_ = outFile.Close()
					_ = rc.Close()
					_ = os.Remove(cleanDest)
					setErr(fmt.Errorf("archive ZIP trop volumineuse après décompression (> %d octets)", maxTotalBytes))
					return
				}

				budgetReader := &atomicBudgetReader{
					r:         rc,
					total:     &totalExtractedBytes,
					maxTotal:  maxTotalBytes,
					maxSingle: maxSingleBytes,
					entryName: zf.Name,
				}
				bufPtr := zipBufPool.Get().(*[]byte)
				_, copyErr := io.CopyBuffer(outFile, budgetReader, *bufPtr)
				zipBufPool.Put(bufPtr)
				closeErr := outFile.Close()
				_ = rc.Close()

				if budgetReader.exceededErr != nil {
					_ = os.Remove(cleanDest)
					setErr(budgetReader.exceededErr)
					return
				}
				if copyErr != nil {
					_ = os.Remove(cleanDest)
					setErr(copyErr)
					return
				}
				if closeErr != nil {
					_ = os.Remove(cleanDest)
					setErr(closeErr)
					return
				}

				cur := extractedCount.Add(1)
				if onProgress != nil && total > 0 {
					pct := int((cur * 100) / int64(total))
					if pct > 99 {
						pct = 99
					}
					prev := lastReportedPct.Load()
					if int32(pct) > prev && lastReportedPct.CompareAndSwap(prev, int32(pct)) {
						onProgress(pct)
					}
				}
			}
		}()
	}
	wg.Wait()

	if firstErr != nil {
		return firstErr
	}

	if onProgress != nil {
		onProgress(100)
	}
	return nil
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

	if magic[0] != 0x50 || magic[1] != 0x4B || !((magic[2] == 0x03 && magic[3] == 0x04) || (magic[2] == 0x05 && magic[3] == 0x06)) {
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

// ExtractZip extrait de manière sécurisée une archive (protection Zip Slip + anti Zip-Bomb) avec extraction parallèle
func ExtractZip(zipPath string, targetPath string, onProgress func(pct int)) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("archive corrompue : %w", err)
	}
	defer zr.Close()

	if len(zr.File) > maxZipExtractEntries {
		return fmt.Errorf("archive ZIP contient trop d'entrées (%d > %d)", len(zr.File), maxZipExtractEntries)
	}

	cleanTarget := filepath.Clean(targetPath)
	if err := os.MkdirAll(cleanTarget, 0755); err != nil {
		return err
	}
	cleanTargetReal, err := filepath.EvalSymlinks(cleanTarget)
	if err != nil {
		return err
	}

	// Étape 1 : Création synchrone rapide de tous les répertoires et filtrage des fichiers valides
	verifiedDirs := map[string]bool{cleanTarget: true}
	ensureSafeCachedDir := func(dirPath string) error {
		cleanDir := filepath.Clean(dirPath)
		if verifiedDirs[cleanDir] {
			return nil
		}
		if err := ensureSafeDestDir(cleanTargetReal, cleanDir); err != nil {
			return err
		}
		verifiedDirs[cleanDir] = true
		return nil
	}

	var declaredTotal uint64
	var filesToExtract []*zip.File
	seenPathsLower := make(map[string]int, len(zr.File))
	for _, zf := range zr.File {
		// Ignorer les liens symboliques
		if zf.Mode()&os.ModeSymlink != 0 {
			continue
		}

		cleanDest, ok := isZipEntrySafeUnder(cleanTarget, zf.Name)
		if !ok {
			ipc.Logf("[ziputils] Entrée ZIP dangereuse ignorée (Zip Slip) : %s", zf.Name)
			continue
		}

		if zf.FileInfo().IsDir() {
			if err := ensureSafeCachedDir(cleanDest); err != nil {
				return err
			}
			continue
		}

		if cleanDest == cleanTarget {
			continue
		}

		if zf.UncompressedSize64 > maxZipSingleFileBytes {
			return fmt.Errorf("entrée ZIP trop volumineuse (%s : %d octets)", zf.Name, zf.UncompressedSize64)
		}
		declaredTotal += zf.UncompressedSize64
		if declaredTotal > maxZipExtractTotalBytes {
			return fmt.Errorf("archive ZIP dépasse la taille maximale décompressée (%d octets)", maxZipExtractTotalBytes)
		}

		if err := ensureSafeCachedDir(filepath.Dir(cleanDest)); err != nil {
			return err
		}

		// Évite les écritures concurrentes sur deux entrées dupliquées (ou ne différant que par la casse sur FS insensible à la casse)
		dedupKey := zipDedupKey(cleanDest)
		if prevIdx, exists := seenPathsLower[dedupKey]; exists {
			filesToExtract[prevIdx] = zf
		} else {
			seenPathsLower[dedupKey] = len(filesToExtract)
			filesToExtract = append(filesToExtract, zf)
		}
	}

	return extractZipEntriesParallel(cleanTarget, filesToExtract, onProgress)
}

// WriteFullZip compresse l'intégralité d'un dossier d'instance directement dans un io.Writer
func WriteFullZip(sourceFolder string, out io.Writer, onProgress func(pct int)) error {
	return WriteFullZipWithHint(sourceFolder, 0, out, onProgress)
}

// WriteFullZipWithHint compresse l'intégralité d'un dossier d'instance dans un io.Writer en réutilisant totalBytesHint
// s'il est fourni (> 0) pour éviter une double traversée WalkDir.
func WriteFullZipWithHint(sourceFolder string, totalBytesHint int64, out io.Writer, onProgress func(pct int)) error {
	isIgnoredEntry := func(d os.DirEntry, fullPath string) bool {
		if d.Type()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return true
		}
		lowerName := strings.ToLower(d.Name())
		if d.IsDir() {
			return fullPath != sourceFolder && scanner.IgnoredDirs[lowerName]
		}
		return scanner.IgnoredDirs[lowerName] || scanner.IsIgnoredFile(d.Name())
	}

	// Calcul de la taille totale pour la progression uniquement si aucun indice valide n'est fourni et qu'on rapporte la progression
	totalBytes := totalBytesHint
	if onProgress != nil && totalBytes <= 0 {
		totalBytes = 0
		_ = filepath.WalkDir(sourceFolder, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if isIgnoredEntry(d, p) {
					return filepath.SkipDir
				}
				return nil
			}
			if !isIgnoredEntry(d, p) {
				if info, err := d.Info(); err == nil {
					totalBytes += info.Size()
				}
			}
			return nil
		})
	}

	zw := zip.NewWriter(out)

	// Optimisation : compression niveau 1 (BestSpeed) pour accélérer par 3 à 5x les sauvegardes
	zw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.BestSpeed)
	})

	var processedBytes int64
	lastPct := -1

	err := filepath.WalkDir(sourceFolder, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if isIgnoredEntry(d, p) {
				return filepath.SkipDir
			}
			return nil
		}
		if isIgnoredEntry(d, p) {
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
			if os.IsNotExist(err) {
				return nil // Fichier disparu entre WalkDir et ouverture
			}
			return err
		}

		bufPtr := zipBufPool.Get().(*[]byte)
		buf := *bufPtr

		var writeErr error
		for {
			n, rErr := inFile.Read(buf)
			if n > 0 {
				if _, wErr := w.Write(buf[:n]); wErr != nil {
					writeErr = wErr
					break
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
				if rErr != io.EOF {
					writeErr = fmt.Errorf("erreur lecture fichier '%s' pendant archivage : %w", rel, rErr)
				}
				break
			}
		}
		zipBufPool.Put(bufPtr)
		_ = inFile.Close()
		return writeErr
	})

	closeErr := zw.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// CreateFullZip compresse l'intégralité d'un dossier d'instance dans un fichier avec tampon de 256 Ko
func CreateFullZip(sourceFolder string, destZipPath string, onProgress func(pct int)) (retErr error) {
	return CreateFullZipWithHint(sourceFolder, 0, destZipPath, onProgress)
}

// CreateFullZipWithHint compresse l'intégralité d'un dossier d'instance dans un fichier avec tampon de 256 Ko
// en exploitant totalBytesHint (> 0) pour éviter une passe WalkDir redondante.
func CreateFullZipWithHint(sourceFolder string, totalBytesHint int64, destZipPath string, onProgress func(pct int)) (retErr error) {
	outFile, err := os.OpenFile(destZipPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			_ = os.Remove(destZipPath)
		}
	}()

	bufWriter := bufio.NewWriterSize(outFile, 256*1024)
	if err := WriteFullZipWithHint(sourceFolder, totalBytesHint, bufWriter, onProgress); err != nil {
		_ = outFile.Close()
		return err
	}
	if err := bufWriter.Flush(); err != nil {
		_ = outFile.Close()
		return err
	}
	return outFile.Close()
}

type DeltaMetadata struct {
	DeletedFiles []string `json:"deletedFiles"`
	CreatedAt    string   `json:"createdAt"`
}

// WriteDeltaZip écrit une archive différentielle directement dans un io.Writer
func WriteDeltaZip(sourceFolder string, files []string, deletedFiles []string, out io.Writer, onProgress func(pct int)) error {
	var totalBytes int64
	for _, rel := range files {
		fullPath := filepath.Join(sourceFolder, filepath.FromSlash(rel))
		if info, err := os.Stat(fullPath); err == nil && !info.IsDir() {
			totalBytes += info.Size()
		}
	}

	zw := zip.NewWriter(out)

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
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		_ = zw.Close()
		return err
	}
	mw, err := zw.Create("__delta__.json")
	if err != nil {
		_ = zw.Close()
		return err
	}
	if _, err := mw.Write(metaBytes); err != nil {
		_ = zw.Close()
		return err
	}

	var processedBytes int64
	lastPct := -1

	for _, rel := range files {
		fullPath := filepath.Join(sourceFolder, filepath.FromSlash(rel))
		info, err := os.Lstat(fullPath)
		if err != nil || info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			continue
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			_ = zw.Close()
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if isPrecompressed(header.Name) {
			header.Method = zip.Store
		} else {
			header.Method = zip.Deflate
		}

		w, err := zw.CreateHeader(header)
		if err != nil {
			_ = zw.Close()
			return err
		}

		inFile, err := os.Open(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			_ = zw.Close()
			return err
		}

		bufPtr := zipBufPool.Get().(*[]byte)
		buf := *bufPtr

		for {
			n, rErr := inFile.Read(buf)
			if n > 0 {
				if _, wErr := w.Write(buf[:n]); wErr != nil {
					zipBufPool.Put(bufPtr)
					_ = inFile.Close()
					_ = zw.Close()
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
				if rErr != io.EOF {
					zipBufPool.Put(bufPtr)
					_ = inFile.Close()
					_ = zw.Close()
					return fmt.Errorf("erreur lecture fichier delta '%s' pendant archivage : %w", rel, rErr)
				}
				break
			}
		}
		zipBufPool.Put(bufPtr)
		_ = inFile.Close()
	}

	return zw.Close()
}

// CreateDeltaZip crée une archive contenant uniquement les fichiers spécifiés avec tampon de 256 Ko
func CreateDeltaZip(sourceFolder string, files []string, deletedFiles []string, destZipPath string, onProgress func(pct int)) (retErr error) {
	outFile, err := os.OpenFile(destZipPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			_ = os.Remove(destZipPath)
		}
	}()

	bufWriter := bufio.NewWriterSize(outFile, 256*1024)
	if err := WriteDeltaZip(sourceFolder, files, deletedFiles, bufWriter, onProgress); err != nil {
		_ = outFile.Close()
		return err
	}
	if err := bufWriter.Flush(); err != nil {
		_ = outFile.Close()
		return err
	}
	return outFile.Close()
}

// ApplyDelta extrait les fichiers modifiés et supprime les fichiers supprimés indiqués dans __delta__.json en parallèle
func ApplyDelta(deltaZipPath string, targetFolder string, onProgress func(pct int)) error {
	zr, err := zip.OpenReader(deltaZipPath)
	if err != nil {
		return fmt.Errorf("archive delta corrompue : %w", err)
	}
	defer zr.Close()

	if len(zr.File) > maxZipExtractEntries {
		return fmt.Errorf("archive delta contient trop d'entrées (%d > %d)", len(zr.File), maxZipExtractEntries)
	}

	cleanTarget := filepath.Clean(targetFolder)
	if err := os.MkdirAll(cleanTarget, 0755); err != nil {
		return err
	}
	cleanTargetReal, err := filepath.EvalSymlinks(cleanTarget)
	if err != nil {
		return err
	}
	var deletedFiles []string
	var declaredTotal uint64
	var filesToExtract []*zip.File
	var dirsToEnsure []string
	seenDirs := map[string]bool{cleanTarget: true}
	queueDir := func(d string) {
		cleanDir := filepath.Clean(d)
		if !seenDirs[cleanDir] {
			seenDirs[cleanDir] = true
			dirsToEnsure = append(dirsToEnsure, cleanDir)
		}
	}
	seenPathsLower := make(map[string]int, len(zr.File))

	// 1. Détection de __delta__.json, validation Zip Slip / budget et collecte des entrées
	for _, zf := range zr.File {
		if zf.Name == "__delta__.json" {
			rc, err := zf.Open()
			if err != nil {
				return fmt.Errorf("impossible d'ouvrir __delta__.json : %w", err)
			}
			data, readErr := io.ReadAll(io.LimitReader(rc, maxDeltaMetaBytes))
			_ = rc.Close()
			if readErr != nil {
				return fmt.Errorf("impossible de lire __delta__.json : %w", readErr)
			}
			var meta DeltaMetadata
			if err := json.Unmarshal(data, &meta); err != nil {
				return fmt.Errorf("__delta__.json corrompu : %w", err)
			}
			deletedFiles = meta.DeletedFiles
			continue
		}

		// Ignorer les liens symboliques
		if zf.Mode()&os.ModeSymlink != 0 {
			continue
		}

		cleanDest, ok := isZipEntrySafeUnder(cleanTarget, zf.Name)
		if !ok {
			ipc.Logf("[ziputils] Entrée ZIP dangereuse ignorée (Zip Slip) : %s", zf.Name)
			continue
		}

		if zf.FileInfo().IsDir() {
			queueDir(cleanDest)
			continue
		}

		if cleanDest == cleanTarget {
			continue
		}

		if zf.UncompressedSize64 > maxZipSingleFileBytes {
			return fmt.Errorf("entrée delta trop volumineuse (%s : %d octets)", zf.Name, zf.UncompressedSize64)
		}
		declaredTotal += zf.UncompressedSize64
		if declaredTotal > maxZipExtractTotalBytes {
			return fmt.Errorf("archive delta dépasse la taille maximale décompressée (%d octets)", maxZipExtractTotalBytes)
		}

		queueDir(filepath.Dir(cleanDest))

		dedupKey := zipDedupKey(cleanDest)
		if prevIdx, exists := seenPathsLower[dedupKey]; exists {
			filesToExtract[prevIdx] = zf
		} else {
			seenPathsLower[dedupKey] = len(filesToExtract)
			filesToExtract = append(filesToExtract, zf)
		}
	}

	// 2. Suppression des fichiers orphelins indiqués dans le manifest du delta + élagage des dossiers vides
	//    Exécuté AVANT la création des répertoires cibles afin que pruneEmptyParentDirs ne supprime pas un
	//    dossier qui doit recevoir un nouveau fichier du même delta (ex: rotation d'une DLL dans natives/).
	for _, rel := range deletedFiles {
		cleanDel, ok := isZipEntrySafeUnder(cleanTarget, rel)
		if !ok || cleanDel == cleanTarget {
			continue
		}
		if err := resolveNearestRealDir(cleanTargetReal, filepath.Dir(cleanDel)); err != nil {
			ipc.Logf("[ziputils] AVERTISSEMENT : suppression ignorée pour %s (%v)", rel, err)
			continue
		}
		if remErr := removeFileWithRetry(cleanDel); remErr != nil {
			ipc.Logf("[ziputils] AVERTISSEMENT : impossible de supprimer %s : %v", cleanDel, remErr)
			continue
		}
		pruneEmptyParentDirs(cleanTarget, filepath.Dir(cleanDel))
	}

	// 3. Création et vérification de tous les répertoires nécessaires aux fichiers extraits
	for _, dir := range dirsToEnsure {
		if err := ensureSafeDestDir(cleanTargetReal, dir); err != nil {
			return err
		}
	}

	return extractZipEntriesParallel(cleanTarget, filesToExtract, onProgress)
}

