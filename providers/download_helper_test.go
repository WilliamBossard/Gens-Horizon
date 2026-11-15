package providers

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestDownloadMultiRange(t *testing.T) {
	// Création d'un contenu de test (100 Ko avec motifs uniques)
	const testSize = int64(100 * 1024)
	testData := make([]byte, testSize)
	for i := range testData {
		testData[i] = byte(i % 256)
	}

	tmpDir, err := os.MkdirTemp("", "horizon_multirange_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dest := filepath.Join(tmpDir, "multirange.bin")
	var rangeRequestsCount atomic.Int32
	var maxProgress atomic.Int32

	err = downloadMultiRange(dest, testSize, func(pct int) {
		curr := maxProgress.Load()
		if int32(pct) > curr {
			maxProgress.Store(int32(pct))
		}
	}, func(start int64, end int64) (*http.Response, error) {
		rangeRequestsCount.Add(1)
		if start < 0 || start >= testSize {
			return nil, fmt.Errorf("invalid range start: %d", start)
		}
		if end < start || end >= testSize {
			end = testSize - 1
		}

		slice := testData[start : end+1]
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Header:     http.Header{"Content-Range": []string{fmt.Sprintf("bytes %d-%d/%d", start, end, testSize)}},
			Body:       io.NopCloser(bytes.NewReader(slice)),
		}, nil
	})

	if err != nil {
		t.Fatalf("downloadMultiRange failed: %v", err)
	}

	if rangeRequestsCount.Load() < 2 {
		t.Errorf("expected at least 2 range requests, got %d", rangeRequestsCount.Load())
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("failed to read downloaded file: %v", err)
	}

	if !bytes.Equal(got, testData) {
		t.Fatalf("downloaded data does not match original data byte-for-byte")
	}

	if maxProgress.Load() < 90 {
		t.Errorf("expected progress close to 100, got %d", maxProgress.Load())
	}
}

func TestDownloadMultiRangeFallbackToSingleStream(t *testing.T) {
	testData := []byte("Full content delivered in one 200 OK stream without partial support")
	testSize := int64(len(testData))

	tmpDir, err := os.MkdirTemp("", "horizon_multirange_fallback_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dest := filepath.Join(tmpDir, "fallback.bin")

	err = downloadMultiRange(dest, testSize, nil, func(start int64, end int64) (*http.Response, error) {
		// Le serveur ignore Range et renvoie 200 OK direct
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(testData)),
		}, nil
	})

	if err != nil {
		t.Fatalf("downloadMultiRange fallback failed: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	if !bytes.Equal(got, testData) {
		t.Fatalf("expected '%s', got '%s'", string(testData), string(got))
	}
}

type chunkedSlowReader struct {
	chunks [][]byte
	idx    int
	delay  time.Duration
	closed atomic.Bool
	unblock chan struct{}
}

func (r *chunkedSlowReader) Read(p []byte) (int, error) {
	if r.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	if r.idx >= len(r.chunks) {
		return 0, io.EOF
	}
	select {
	case <-r.unblock:
		return 0, io.ErrClosedPipe
	case <-time.After(r.delay):
	}
	if r.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	n := copy(p, r.chunks[r.idx])
	r.idx++
	return n, nil
}

func (r *chunkedSlowReader) Close() error {
	if r.closed.CompareAndSwap(false, true) {
		close(r.unblock)
	}
	return nil
}

func TestIdleTimeoutReaderAllowsActiveSlowDownloadAndAbortsStall(t *testing.T) {
	// 5 chunks de 20 ms = 100 ms total (> timeout 55 ms), mais jamais > 55 ms d'inactivité
	active := &chunkedSlowReader{
		chunks:  [][]byte{[]byte("a"), []byte("b"), []byte("c"), []byte("d"), []byte("e")},
		delay:   20 * time.Millisecond,
		unblock: make(chan struct{}),
	}
	r := newIdleTimeoutReader(active, 55*time.Millisecond)
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatalf("expected active slow stream to succeed, got error: %v", err)
	}
	if string(got) != "abcde" {
		t.Fatalf("expected abcde, got %q", string(got))
	}

	// Flux bloqué 150 ms (> timeout 40 ms) : doit être interrompu proprement
	stalled := &chunkedSlowReader{
		chunks:  [][]byte{[]byte("never")},
		delay:   200 * time.Millisecond,
		unblock: make(chan struct{}),
	}
	r2 := newIdleTimeoutReader(stalled, 40*time.Millisecond)
	_, err = io.ReadAll(r2)
	_ = r2.Close()
	if err == nil {
		t.Fatalf("expected stalled stream to fail on idle timeout")
	}
}

func TestDownloadWithResumeLeavesNoCorruptDestOnFailure(t *testing.T) {
	tmpDir := t.TempDir()
	dest := filepath.Join(tmpDir, "atomic_fail.bin")

	err := downloadWithResume(dest, 100, nil, func(offset int64) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       io.NopCloser(bytes.NewReader([]byte("server error"))),
		}, nil
	})
	if err == nil {
		t.Fatalf("expected downloadWithResume to fail on HTTP 500")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("expected no target file at %s after failed download, stat err: %v", dest, statErr)
	}
}

func TestParseContentRangeStartAnd206Validation(t *testing.T) {
	if s, ok := parseContentRangeStart("bytes 1024-2047/4096"); !ok || s != 1024 {
		t.Fatalf("expected start=1024, ok=true; got %d, %v", s, ok)
	}
	if _, ok := parseContentRangeStart("invalid"); ok {
		t.Fatalf("expected ok=false for malformed Content-Range")
	}

	// Simulate interrupted first attempt at 4 bytes ("ABCD"), then server responds 206 starting at 0 ("ABCDEFGH")
	tmpDir := t.TempDir()
	dest := filepath.Join(tmpDir, "range206.bin")
	calls := 0
	err := downloadWithResume(dest, 8, nil, func(offset int64) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(&partialErrReader{data: []byte("ABCD")}),
			}, nil
		}
		hdr := make(http.Header)
		hdr.Set("Content-Range", "bytes 0-7/8")
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Header:     hdr,
			Body:       io.NopCloser(bytes.NewReader([]byte("ABCDEFGH"))),
		}, nil
	})
	if err != nil {
		t.Fatalf("downloadWithResume failed: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != "ABCDEFGH" {
		t.Fatalf("expected ABCDEFGH after 206 start=0 rewind, got %q (err=%v)", string(got), err)
	}
}

type partialErrReader struct {
	data []byte
	sent bool
}

func (r *partialErrReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(p, r.data)
	r.sent = true
	return n, io.ErrUnexpectedEOF
}

func TestDownloadWithResumeRejectsTruncatedEOF(t *testing.T) {
	tmpDir := t.TempDir()
	dest := filepath.Join(tmpDir, "truncated.bin")

	err := downloadWithResume(dest, 20, nil, func(offset int64) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader([]byte("ONLY8BYT"))),
		}, nil
	})
	if err == nil {
		t.Fatalf("expected error on truncated HTTP 200 EOF when totalSize=20")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("expected incomplete target file %s to be cleaned up", dest)
	}
}
