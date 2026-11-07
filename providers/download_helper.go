package providers

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

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

