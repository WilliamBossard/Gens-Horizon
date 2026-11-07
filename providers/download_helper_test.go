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
