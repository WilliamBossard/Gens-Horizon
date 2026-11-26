package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/retry"
)

// MinMultiRangeSize définit la taille minimale pour déclencher un téléchargement parallèle multi-segments (30 Mo)
const MinMultiRangeSize = int64(30 * 1024 * 1024)

func parseContentRangeStart(hdr string) (int64, bool) {
	trimmed := strings.TrimSpace(hdr)
	if !strings.HasPrefix(strings.ToLower(trimmed), "bytes ") {
		return 0, false
	}
	rangePart := strings.TrimSpace(trimmed[6:])
	dashIdx := strings.IndexByte(rangePart, '-')
	if dashIdx <= 0 {
		return 0, false
	}
	start, err := strconv.ParseInt(strings.TrimSpace(rangePart[:dashIdx]), 10, 64)
	if err != nil || start < 0 {
		return 0, false
	}
	return start, true
}

// downloadIdleTimeout définit le délai d'inactivité maximal toléré entre deux lectures sur un flux HTTP
var downloadIdleTimeout = 60 * time.Second

var (
	dlBuf64Pool  = sync.Pool{New: func() any { b := make([]byte, 64*1024); return &b }}
	dlBuf128Pool = sync.Pool{New: func() any { b := make([]byte, 128*1024); return &b }}
)

type idleTimeoutReadCloser struct {
	rc        io.ReadCloser
	timeout   time.Duration
	timedOut  atomic.Bool
	mu        sync.Mutex
	reading   bool
	readGen   uint64
	timer     *time.Timer
	done      chan struct{}
	closeOnce sync.Once
}

func newIdleTimeoutReader(rc io.ReadCloser, timeout time.Duration) *idleTimeoutReadCloser {
	itr := &idleTimeoutReadCloser{
		rc:      rc,
		timeout: timeout,
		done:    make(chan struct{}),
	}
	if timeout > 0 {
		itr.timer = time.NewTimer(timeout)
		itr.timer.Stop()
		go func() {
			for {
				select {
				case <-itr.done:
					return
				case <-itr.timer.C:
					itr.mu.Lock()
					shouldClose := itr.reading
					itr.mu.Unlock()
					if shouldClose {
						itr.timedOut.Store(true)
						_ = itr.rc.Close()
						return
					}
				}
			}
		}()
	}
	return itr
}

func (itr *idleTimeoutReadCloser) Read(p []byte) (int, error) {
	if itr.timeout <= 0 || itr.timer == nil {
		return itr.rc.Read(p)
	}
	itr.mu.Lock()
	itr.readGen++
	itr.reading = true
	itr.timer.Reset(itr.timeout)
	itr.mu.Unlock()

	n, err := itr.rc.Read(p)

	itr.mu.Lock()
	itr.reading = false
	itr.timer.Stop()
	itr.mu.Unlock()

	if err != nil && itr.timedOut.Load() {
		return n, fmt.Errorf("download stalled (no data for %s): %w", itr.timeout, err)
	}
	return n, err
}

func (itr *idleTimeoutReadCloser) Close() error {
	itr.closeOnce.Do(func() {
		itr.mu.Lock()
		itr.reading = false
		if itr.timer != nil {
			itr.timer.Stop()
		}
		itr.mu.Unlock()
		close(itr.done)
	})
	return itr.rc.Close()
}

func createTempDownloadFile(destPath string) (*os.File, string, error) {
	f, err := os.CreateTemp(filepath.Dir(destPath), filepath.Base(destPath)+".part.*")
	if err != nil {
		return nil, "", err
	}
	return f, f.Name(), nil
}

func finalizeTempDownloadFile(out *os.File, tmpPath string, destPath string) error {
	if err := out.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	_ = os.Chmod(tmpPath, 0644)
	var renameErr error
	for attempt := 0; attempt < 5; attempt++ {
		renameErr = os.Rename(tmpPath, destPath)
		if renameErr == nil {
			return nil
		}
		time.Sleep(time.Duration(attempt+1) * 40 * time.Millisecond)
	}
	_ = os.Remove(tmpPath)
	return renameErr
}

// RangeRequestFunc est la signature de la fonction construisant une requête HTTP pour une plage [start, end].
// Si end < 0, la plage est [start, fin_du_fichier].
type RangeRequestFunc func(start int64, end int64) (*http.Response, error)

// errMultiRangeFallback signale qu'un serveur ne respecte plus Range sur un segment et impose un fallback flux unique.
var errMultiRangeFallback = errors.New("multirange unsupported on subsequent chunk")

// downloadSmart choisit intelligemment entre le téléchargement parallèle multi-segments et le flux unique avec reprise
func downloadSmart(
	destPath string,
	totalSize int64,
	onProgress func(pct int),
	makeRequest RangeRequestFunc,
) error {
	return downloadSmartWithContext(baseContext(), destPath, totalSize, onProgress, makeRequest)
}

// waitChunkRetryAfter attend le délai Retry-After si présent sur 429/503, sinon backoff linéaire par tentative.
func waitChunkRetryAfter(ctx context.Context, resp *http.Response, attempt int) error {
	var wait time.Duration
	if resp != nil && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable) {
		wait = retry.ParseRetryAfter(resp.Header.Get("Retry-After"))
	}
	if wait <= 0 {
		if attempt <= 0 {
			return nil
		}
		wait = time.Duration(attempt*1500) * time.Millisecond
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

func downloadSmartWithContext(
	ctx context.Context,
	destPath string,
	totalSize int64,
	onProgress func(pct int),
	makeRequest RangeRequestFunc,
) error {
	if ctx == nil {
		ctx = baseContext()
	}
	if totalSize < MinMultiRangeSize {
		return downloadWithResumeWithContext(ctx, destPath, totalSize, onProgress, func(offset int64) (*http.Response, error) {
			return makeRequest(offset, -1)
		})
	}

	err := downloadMultiRangeWithContext(ctx, destPath, totalSize, onProgress, makeRequest)
	if errors.Is(err, errMultiRangeFallback) {
		return downloadWithResumeWithContext(ctx, destPath, totalSize, onProgress, func(offset int64) (*http.Response, error) {
			return makeRequest(offset, -1)
		})
	}
	return err
}

// downloadMultiRange télécharge un gros fichier en N segments parallèles avec écriture concurrente via WriteAt
func downloadMultiRange(
	destPath string,
	totalSize int64,
	onProgress func(pct int),
	makeRequest RangeRequestFunc,
) error {
	return downloadMultiRangeWithContext(baseContext(), destPath, totalSize, onProgress, makeRequest)
}

func downloadMultiRangeWithContext(
	parentCtx context.Context,
	destPath string,
	totalSize int64,
	onProgress func(pct int),
	makeRequest RangeRequestFunc,
) error {
	if parentCtx == nil {
		parentCtx = baseContext()
	}
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
		return downloadWithResumeWithContext(parentCtx, destPath, totalSize, onProgress, func(offset int64) (*http.Response, error) {
			return makeRequest(offset, -1)
		})
	}

	// Si le serveur a renvoyé 200 OK au lieu de 206 Partial Content (absence de support Range),
	// on consomme le corps de resp0 comme flux complet sans gaspiller la requête
	if resp0.StatusCode == http.StatusOK {
		return streamSingleResponseWithContext(parentCtx, resp0, destPath, totalSize, onProgress)
	}

	if resp0.StatusCode != http.StatusPartialContent {
		_ = resp0.Body.Close()
		return downloadWithResumeWithContext(parentCtx, destPath, totalSize, onProgress, func(offset int64) (*http.Response, error) {
			return makeRequest(offset, -1)
		})
	}

	// Préparation du fichier temporaire cible (évite tout fichier partiel visible à destPath)
	out, tmpPath, err := createTempDownloadFile(destPath)
	if err != nil {
		_ = resp0.Body.Close()
		return err
	}
	tmpClosed := false
	defer func() {
		if !tmpClosed {
			_ = out.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if err := out.Truncate(totalSize); err != nil {
		_ = resp0.Body.Close()
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

	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	streamChunk := func(c chunkRange, initialResp *http.Response) error {
		const maxRetries = 3
		var chunkErr error
		currentOffset := c.start
		pendingResp := initialResp

		for attempt := 0; attempt < maxRetries; attempt++ {
			if ctx.Err() != nil {
				if pendingResp != nil {
					_ = pendingResp.Body.Close()
				}
				return ctx.Err()
			}
			resp := pendingResp
			pendingResp = nil
			if resp == nil {
				if attempt > 0 {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(time.Duration(attempt*1000) * time.Millisecond):
					}
				}
				r, err := makeRequest(currentOffset, c.end)
				if err != nil {
					chunkErr = err
					continue
				}
				// Les requêtes Range de segments DOIVENT recevoir 206 Partial Content
				// (un 200 OK indique l'absence de support Range : bascule sur flux unique).
				if r.StatusCode == http.StatusOK {
					_ = r.Body.Close()
					return errMultiRangeFallback
				}
				if r.StatusCode != http.StatusPartialContent {
					b, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
					_ = r.Body.Close()
					chunkErr = fmt.Errorf("chunk %d HTTP %d (expected 206): %s", c.id, r.StatusCode, sanitizeHTTPBody(b))
					if ra := retry.ParseRetryAfter(r.Header.Get("Retry-After")); ra > 0 && (r.StatusCode == http.StatusTooManyRequests || r.StatusCode == http.StatusServiceUnavailable) {
						select {
						case <-ctx.Done():
							return ctx.Err()
						case <-time.After(ra):
						}
					}
					continue
				}
				if crHdr := r.Header.Get("Content-Range"); crHdr != "" {
					if startByte, ok := parseContentRangeStart(crHdr); !ok || startByte != currentOffset {
						_ = r.Body.Close()
						chunkErr = fmt.Errorf("chunk %d Content-Range mismatch (%s, expected start %d)", c.id, crHdr, currentOffset)
						continue
					}
				}
				resp = r
			}

			bufPtr := dlBuf128Pool.Get().(*[]byte)
			buf := *bufPtr
			bodyReader := newIdleTimeoutReader(resp.Body, downloadIdleTimeout)
			var streamErr error
			for {
				if ctx.Err() != nil {
					streamErr = ctx.Err()
					break
				}
				n, rErr := bodyReader.Read(buf)
				if n > 0 {
					if currentOffset+int64(n) > c.end+1 {
						_ = bodyReader.Close()
			dlBuf128Pool.Put(bufPtr)
						return fmt.Errorf("chunk %d overflow: serveur a renvoyé plus d'octets que la plage [%d..%d]", c.id, c.start, c.end)
					}
					if _, wErr := out.WriteAt(buf[:n], currentOffset); wErr != nil {
						_ = bodyReader.Close()
			dlBuf128Pool.Put(bufPtr)
						return fmt.Errorf("chunk %d write error: %w", c.id, wErr)
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
			_ = bodyReader.Close()
			dlBuf128Pool.Put(bufPtr)

			if streamErr == nil && currentOffset == c.end+1 {
				return nil
			}
			if streamErr != nil {
				chunkErr = streamErr
			} else {
				chunkErr = fmt.Errorf("chunk %d incomplete EOF (%d/%d)", c.id, currentOffset, c.end+1)
			}
		}

		if pendingResp != nil {
			_ = pendingResp.Body.Close()
		}
		return fmt.Errorf("chunk %d failed after %d attempts: %w", c.id, maxRetries, chunkErr)
	}

	var g sync.WaitGroup
	var fallbackToSingle atomic.Bool
	errChan := make(chan error, numWorkers)

	for i := 0; i < numWorkers; i++ {
		chunk := chunks[i]
		var initial *http.Response
		if i == 0 {
			initial = resp0
		}
		g.Add(1)
		go func(c chunkRange, initResp *http.Response) {
			defer g.Done()
			if err := streamChunk(c, initResp); err != nil {
				if errors.Is(err, errMultiRangeFallback) {
					fallbackToSingle.Store(true)
				}
				cancel()
				errChan <- err
			}
		}(chunk, initial)
	}

	g.Wait()
	close(errChan)

	if fallbackToSingle.Load() {
		return errMultiRangeFallback
	}

	if firstErr := <-errChan; firstErr != nil {
		return firstErr
	}

	tmpClosed = true
	if err := finalizeTempDownloadFile(out, tmpPath, destPath); err != nil {
		return err
	}

	if onProgress != nil {
		onProgress(100)
	}
	return nil
}

// streamSingleResponse consomme une réponse HTTP complète en flux unique
func streamSingleResponse(resp *http.Response, destPath string, totalSize int64, onProgress func(pct int)) error {
	return streamSingleResponseWithContext(baseContext(), resp, destPath, totalSize, onProgress)
}

func streamSingleResponseWithContext(ctx context.Context, resp *http.Response, destPath string, totalSize int64, onProgress func(pct int)) error {
	if ctx == nil {
		ctx = baseContext()
	}
	bodyReader := newIdleTimeoutReader(resp.Body, downloadIdleTimeout)
	defer bodyReader.Close()

	out, tmpPath, err := createTempDownloadFile(destPath)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = out.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	var downloaded int64
	lastPct := -1
	bufPtr := dlBuf128Pool.Get().(*[]byte)
	defer dlBuf128Pool.Put(bufPtr)
	buf := *bufPtr

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, rErr := bodyReader.Read(buf)
		if n > 0 {
			if _, wErr := out.Write(buf[:n]); wErr != nil {
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
			return rErr
		}
	}
	if totalSize > 0 && downloaded != totalSize {
		return fmt.Errorf("download incomplete EOF (%d/%d bytes)", downloaded, totalSize)
	}
	closed = true
	if err := finalizeTempDownloadFile(out, tmpPath, destPath); err != nil {
		return err
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
	return downloadWithResumeWithContext(baseContext(), destPath, totalSize, onProgress, makeRequest)
}

func downloadWithResumeWithContext(
	ctx context.Context,
	destPath string,
	totalSize int64,
	onProgress func(pct int),
	makeRequest func(offset int64) (*http.Response, error),
) error {
	if ctx == nil {
		ctx = baseContext()
	}
	out, tmpPath, err := createTempDownloadFile(destPath)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = out.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	var downloaded int64
	lastPct := -1
	bufPtr := dlBuf64Pool.Get().(*[]byte)
	defer dlBuf64Pool.Put(bufPtr)
	buf := *bufPtr

	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt*1500) * time.Millisecond):
			}
		}

		resp, reqErr := makeRequest(downloaded)
		if reqErr != nil {
			if attempt == maxAttempts-1 {
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
		} else if downloaded > 0 && resp.StatusCode == http.StatusPartialContent {
			if startByte, ok := parseContentRangeStart(resp.Header.Get("Content-Range")); ok && startByte != downloaded {
				if startByte == 0 {
					if _, sErr := out.Seek(0, io.SeekStart); sErr == nil {
						_ = out.Truncate(0)
						downloaded = 0
					}
				} else {
					_ = resp.Body.Close()
					if attempt == maxAttempts-1 {
						return fmt.Errorf("download HTTP 206 Content-Range mismatch: got start %d, expected %d", startByte, downloaded)
					}
					continue
				}
			}
		}

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			_ = resp.Body.Close()
			if attempt == maxAttempts-1 || (resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests) {
				return fmt.Errorf("download HTTP %d : %s", resp.StatusCode, sanitizeHTTPBody(b))
			}
			if ra := retry.ParseRetryAfter(resp.Header.Get("Retry-After")); ra > 0 && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable) {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(ra):
				}
			}
			continue
		}

		bodyReader := newIdleTimeoutReader(resp.Body, downloadIdleTimeout)
		var streamErr error
		for {
			if ctx.Err() != nil {
				streamErr = ctx.Err()
				break
			}
			n, rErr := bodyReader.Read(buf)
			if n > 0 {
				if _, wErr := out.Write(buf[:n]); wErr != nil {
					_ = bodyReader.Close()
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
		_ = bodyReader.Close()

		if streamErr == nil && totalSize > 0 && downloaded != totalSize {
			streamErr = fmt.Errorf("download incomplete EOF (%d/%d bytes)", downloaded, totalSize)
		}

		if streamErr == nil {
			closed = true
			if err := finalizeTempDownloadFile(out, tmpPath, destPath); err != nil {
				return err
			}
			if onProgress != nil {
				onProgress(100)
			}
			return nil
		}

		if attempt == maxAttempts-1 {
			return streamErr
		}
	}

	return fmt.Errorf("échec téléchargement après %d tentatives", maxAttempts)
}
