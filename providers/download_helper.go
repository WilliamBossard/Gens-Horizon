package providers

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// MinMultiRangeSize définit la taille minimale pour déclencher un téléchargement parallèle multi-segments (30 Mo)
const MinMultiRangeSize = int64(30 * 1024 * 1024)

// RangeRequestFunc est la signature de la fonction construisant une requête HTTP pour une plage [start, end].
// Si end < 0, la plage est [start, fin_du_fichier].
type RangeRequestFunc func(start int64, end int64) (*http.Response, error)

// downloadSmart choisit intelligemment entre le téléchargement parallèle multi-segments et le flux unique avec reprise
func downloadSmart(
	destPath string,
	totalSize int64,
	onProgress func(pct int),
	makeRequest RangeRequestFunc,
) error {
	if totalSize < MinMultiRangeSize {
		return downloadWithResume(destPath, totalSize, onProgress, func(offset int64) (*http.Response, error) {
			return makeRequest(offset, -1)
		})
	}

	return downloadMultiRange(destPath, totalSize, onProgress, makeRequest)
}

// downloadMultiRange télécharge un gros fichier en N segments parallèles avec écriture concurrente via WriteAt
func downloadMultiRange(
	destPath string,
	totalSize int64,
	onProgress func(pct int),
	makeRequest RangeRequestFunc,
) error {
	// Détermination du nombre de segments (entre 2 et 4, min 8 Mo par segment)
	numWorkers := int(totalSize / (10 * 1024 * 1024))
	if numWorkers < 2 {
		numWorkers = 2
	}
	if numWorkers > 4 {
		numWorkers = 4
	}

	chunkSize := totalSize / int64(numWorkers)

	// Test du premier segment [0, chunkSize-1] pour vérifier le support HTTP 206 Partial Content
	firstEnd := chunkSize - 1
	resp0, err := makeRequest(0, firstEnd)
	if err != nil {
		// Fallback immédiat sur flux unique avec reprise
		return downloadWithResume(destPath, totalSize, onProgress, func(offset int64) (*http.Response, error) {
			return makeRequest(offset, -1)
		})
	}

	// Si le serveur a renvoyé 200 OK au lieu de 206 Partial Content (absence de support Range),
	// on consomme le corps de resp0 comme flux complet sans gaspiller la requête
	if resp0.StatusCode == http.StatusOK {
		return streamSingleResponse(resp0, destPath, totalSize, onProgress)
	}

	if resp0.StatusCode != http.StatusPartialContent {
		_ = resp0.Body.Close()
		return downloadWithResume(destPath, totalSize, onProgress, func(offset int64) (*http.Response, error) {
			return makeRequest(offset, -1)
		})
	}

	// Préparation du fichier cible
	out, err := os.OpenFile(destPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		_ = resp0.Body.Close()
		return err
	}
	defer out.Close()

	if err := out.Truncate(totalSize); err != nil {
		_ = resp0.Body.Close()
		_ = os.Remove(destPath)
		return err
	}

	var downloaded atomic.Int64
	var lastPct atomic.Int32
	lastPct.Store(-1)

	reportProgress := func(n int) {
		newDownloaded := downloaded.Add(int64(n))
		if onProgress != nil && totalSize > 0 {
			pct := int((newDownloaded * 100) / totalSize)
			if pct > 100 {
				pct = 100
			}
			currLast := lastPct.Load()
			if int32(pct) != currLast && (pct >= int(currLast)+1 || pct == 100) {
				if lastPct.CompareAndSwap(currLast, int32(pct)) {
					onProgress(pct)
				}
			}
		}
	}

	type chunkRange struct {
		id    int
		start int64
		end   int64
	}

	chunks := make([]chunkRange, numWorkers)
	for i := 0; i < numWorkers; i++ {
		start := int64(i) * chunkSize
		end := start + chunkSize - 1
		if i == numWorkers-1 {
			end = totalSize - 1
		}
		chunks[i] = chunkRange{id: i, start: start, end: end}
	}

	var g sync.WaitGroup
	errChan := make(chan error, numWorkers)

	// Worker 0 consomme resp0 déjà ouvert
	g.Add(1)
	go func() {
		defer g.Done()
		defer resp0.Body.Close()

		buf := make([]byte, 128*1024)
		currentOffset := chunks[0].start
		for {
			n, rErr := resp0.Body.Read(buf)
			if n > 0 {
				if _, wErr := out.WriteAt(buf[:n], currentOffset); wErr != nil {
					errChan <- fmt.Errorf("chunk 0 write error: %w", wErr)
					return
				}
				currentOffset += int64(n)
				reportProgress(n)
			}
			if rErr != nil {
				if rErr == io.EOF {
					break
				}
				errChan <- fmt.Errorf("chunk 0 read error: %w", rErr)
				return
			}
		}
	}()

	// Workers 1 à numWorkers-1
	for i := 1; i < numWorkers; i++ {
		chunk := chunks[i]
		g.Add(1)
		go func(c chunkRange) {
			defer g.Done()

			const maxRetries = 3
			var chunkErr error
			currentOffset := c.start

			for attempt := 0; attempt < maxRetries; attempt++ {
				if attempt > 0 {
					time.Sleep(time.Duration(attempt*1000) * time.Millisecond)
				}

				resp, err := makeRequest(currentOffset, c.end)
				if err != nil {
					chunkErr = err
					continue
				}

				if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
					b, _ := io.ReadAll(resp.Body)
					_ = resp.Body.Close()
					chunkErr = fmt.Errorf("chunk %d HTTP %d: %s", c.id, resp.StatusCode, string(b))
					continue
				}

				buf := make([]byte, 128*1024)
				var streamErr error
				for {
					n, rErr := resp.Body.Read(buf)
					if n > 0 {
						if _, wErr := out.WriteAt(buf[:n], currentOffset); wErr != nil {
							_ = resp.Body.Close()
							errChan <- fmt.Errorf("chunk %d write error: %w", c.id, wErr)
							return
						}
						currentOffset += int64(n)
						reportProgress(n)
					}
					if rErr != nil {
						if rErr != io.EOF {
							streamErr = rErr
						}
						break
					}
				}
				_ = resp.Body.Close()

				if streamErr == nil && currentOffset > c.end {
					return
				}
				chunkErr = streamErr
			}

			if chunkErr != nil {
				errChan <- fmt.Errorf("chunk %d failed after %d attempts: %w", c.id, maxRetries, chunkErr)
			}
		}(chunk)
	}

	g.Wait()
	close(errChan)

	if firstErr := <-errChan; firstErr != nil {
		_ = out.Close()
		_ = os.Remove(destPath)
		return firstErr
	}

	if onProgress != nil {
		onProgress(100)
	}
	return nil
}

// streamSingleResponse consomme une réponse HTTP complète en flux unique
func streamSingleResponse(resp *http.Response, destPath string, totalSize int64, onProgress func(pct int)) error {
	defer resp.Body.Close()
	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	var downloaded int64
	lastPct := -1
	buf := make([]byte, 128*1024)

	for {
		n, rErr := resp.Body.Read(buf)
		if n > 0 {
			if _, wErr := out.Write(buf[:n]); wErr != nil {
				_ = out.Close()
				_ = os.Remove(destPath)
				return wErr
			}
			downloaded += int64(n)
			if onProgress != nil && totalSize > 0 {
				pct := int((downloaded * 100) / totalSize)
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
			if rErr == io.EOF {
				break
			}
			_ = out.Close()
			_ = os.Remove(destPath)
			return rErr
		}
	}
	if onProgress != nil {
		onProgress(100)
	}
	return nil
}

// downloadWithResume télécharge un flux HTTP vers destPath avec reprise automatique sur coupure (Range: bytes=X-)
func downloadWithResume(
	destPath string,
	totalSize int64,
	onProgress func(pct int),
	makeRequest func(offset int64) (*http.Response, error),
) error {
	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	var downloaded int64
	lastPct := -1
	buf := make([]byte, 64*1024)

	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*1500) * time.Millisecond)
		}

		resp, reqErr := makeRequest(downloaded)
		if reqErr != nil {
			if attempt == maxAttempts-1 {
				_ = out.Close()
				_ = os.Remove(destPath)
				return reqErr
			}
			continue
		}

		// Si on avait demandé un Range mais que le serveur renvoie 200 OK depuis 0, on repart proprement de 0
		if downloaded > 0 && resp.StatusCode == http.StatusOK {
			if _, sErr := out.Seek(0, io.SeekStart); sErr == nil {
				_ = out.Truncate(0)
				downloaded = 0
			}
		}

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if attempt == maxAttempts-1 || (resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests) {
				_ = out.Close()
				_ = os.Remove(destPath)
				return fmt.Errorf("download HTTP %d : %s", resp.StatusCode, string(b))
			}
			continue
		}

		var streamErr error
		for {
			n, rErr := resp.Body.Read(buf)
			if n > 0 {
				if _, wErr := out.Write(buf[:n]); wErr != nil {
					_ = resp.Body.Close()
					_ = out.Close()
					_ = os.Remove(destPath)
					return wErr
				}
				downloaded += int64(n)
				if onProgress != nil && totalSize > 0 {
					pct := int((downloaded * 100) / totalSize)
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
				if rErr == io.EOF {
					streamErr = nil
				} else {
					streamErr = rErr
				}
				break
			}
		}
		_ = resp.Body.Close()

		if streamErr == nil {
			if onProgress != nil {
				onProgress(100)
			}
			return nil
		}

		if attempt == maxAttempts-1 {
			_ = out.Close()
			_ = os.Remove(destPath)
			return streamErr
		}
	}

	_ = out.Close()
	_ = os.Remove(destPath)
	return fmt.Errorf("échec téléchargement après %d tentatives", maxAttempts)
}
