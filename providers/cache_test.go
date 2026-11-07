package providers

import (
	"bytes"
	"io"
	"os"
	"testing"
	"time"
)

func TestProgressReader(t *testing.T) {
	data := bytes.Repeat([]byte("A"), 1000)
	var processed int64
	lastPct := -1
	var updates []int

	pr := &ProgressReader{
		Reader:    bytes.NewReader(data),
		TotalSize: int64(len(data)),
		Processed: &processed,
		LastPct:   &lastPct,
		OnProgress: func(pct int) {
			updates = append(updates, pct)
		},
	}

	buf := make([]byte, 250)
	for {
		_, err := pr.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if processed != 1000 {
		t.Errorf("expected processed=1000, got %d", processed)
	}
	if len(updates) != 4 || updates[len(updates)-1] != 100 {
		t.Errorf("expected 4 progress updates ending in 100, got %v", updates)
	}
}

func TestCloudIndexCacheLifecycle(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "horizon_cache_test_*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	_ = os.Setenv("HORIZON_DATA_DIR", tmpDir)
	defer os.Unsetenv("HORIZON_DATA_DIR")

	files := []CloudFile{
		{ID: "id1", Name: "GensHorizon_Backup_Test.zip", Size: 4096, ModifiedTime: time.Now()},
	}

	SaveCachedCloudIndex("google", files)

	cached, ok := GetCachedCloudIndex("google", 45*time.Second)
	if !ok || len(cached) != 1 || cached[0].ID != "id1" {
		t.Errorf("expected cache hit with id1, got ok=%v, cached=%v", ok, cached)
	}

	// Un autre provider ne doit pas recevoir le cache de google
	if _, okOther := GetCachedCloudIndex("dropbox", 45*time.Second); okOther {
		t.Errorf("expected cache miss for dropbox")
	}

	// Invalidation immédiate
	InvalidateCloudIndexCache()
	if _, okAfter := GetCachedCloudIndex("google", 45*time.Second); okAfter {
		t.Errorf("expected cache miss after InvalidateCloudIndexCache")
	}
}
