package ziputils

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyZipIntegrity(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "horizon_zip_test_*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validZip := filepath.Join(tmpDir, "valid.zip")
	f, _ := os.Create(validZip)
	zw := zip.NewWriter(f)
	w, _ := zw.Create("test.txt")
	_, _ = w.Write([]byte("hello world"))
	_ = zw.Close()
	_ = f.Close()

	if err := VerifyZipIntegrity(validZip); err != nil {
		t.Errorf("expected valid zip to pass, got: %v", err)
	}

	invalidZip := filepath.Join(tmpDir, "invalid.txt")
	_ = os.WriteFile(invalidZip, []byte("NOT A ZIP ARCHIVE AT ALL"), 0644)
	if err := VerifyZipIntegrity(invalidZip); err == nil {
		t.Errorf("expected invalid file to fail integrity check, got nil")
	}
}

func TestZipAndExtractRoundTrip(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "horizon_zip_roundtrip_*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	sourceDir := filepath.Join(tmpDir, "source")
	_ = os.MkdirAll(filepath.Join(sourceDir, "mods"), 0755)
	_ = os.WriteFile(filepath.Join(sourceDir, "options.txt"), []byte("fov:90"), 0644)
	_ = os.WriteFile(filepath.Join(sourceDir, "mods", "jei.jar"), []byte("dummy jei jar content"), 0644)

	destZip := filepath.Join(tmpDir, "backup.zip")
	if err := CreateFullZip(sourceDir, destZip, nil); err != nil {
		t.Fatalf("CreateFullZip failed: %v", err)
	}

	zr, err := zip.OpenReader(destZip)
	if err != nil {
		t.Fatalf("OpenReader failed: %v", err)
	}
	for _, zf := range zr.File {
		switch zf.Name {
		case "mods/jei.jar":
			if zf.Method != zip.Store {
				t.Errorf("expected mods/jei.jar to use zip.Store, got %d", zf.Method)
			}
		case "options.txt":
			if zf.Method != zip.Deflate {
				t.Errorf("expected options.txt to use zip.Deflate, got %d", zf.Method)
			}
		}
	}
	_ = zr.Close()

	extractedDir := filepath.Join(tmpDir, "extracted")
	if err := ExtractZip(destZip, extractedDir, nil); err != nil {
		t.Fatalf("ExtractZip failed: %v", err)
	}

	optData, err := os.ReadFile(filepath.Join(extractedDir, "options.txt"))
	if err != nil || string(optData) != "fov:90" {
		t.Errorf("options.txt content mismatch: %v, '%s'", err, string(optData))
	}

	modData, err := os.ReadFile(filepath.Join(extractedDir, "mods", "jei.jar"))
	if err != nil || string(modData) != "dummy jei jar content" {
		t.Errorf("jei.jar content mismatch: %v, '%s'", err, string(modData))
	}
}

func TestDeltaZipAndApplyDelta(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "horizon_delta_test_*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	sourceDir := filepath.Join(tmpDir, "source")
	_ = os.MkdirAll(sourceDir, 0755)
	_ = os.WriteFile(filepath.Join(sourceDir, "options.txt"), []byte("fov:100"), 0644)
	_ = os.WriteFile(filepath.Join(sourceDir, "newfile.txt"), []byte("new content"), 0644)

	targetDir := filepath.Join(tmpDir, "target")
	_ = os.MkdirAll(targetDir, 0755)
	_ = os.WriteFile(filepath.Join(targetDir, "options.txt"), []byte("fov:70"), 0644)
	_ = os.WriteFile(filepath.Join(targetDir, "to_delete.txt"), []byte("obsolete"), 0644)

	deltaZip := filepath.Join(tmpDir, "delta.zip")
	filesToZip := []string{"options.txt", "newfile.txt"}
	deletedFiles := []string{"to_delete.txt"}

	if err := CreateDeltaZip(sourceDir, filesToZip, deletedFiles, deltaZip, nil); err != nil {
		t.Fatalf("CreateDeltaZip failed: %v", err)
	}

	if err := ApplyDelta(deltaZip, targetDir, nil); err != nil {
		t.Fatalf("ApplyDelta failed: %v", err)
	}

	// 1. options.txt must be updated to fov:100
	optData, err := os.ReadFile(filepath.Join(targetDir, "options.txt"))
	if err != nil || string(optData) != "fov:100" {
		t.Errorf("expected options.txt to be updated, got '%s'", string(optData))
	}

	// 2. newfile.txt must exist
	newData, err := os.ReadFile(filepath.Join(targetDir, "newfile.txt"))
	if err != nil || string(newData) != "new content" {
		t.Errorf("expected newfile.txt to exist, got error: %v", err)
	}

	// 3. to_delete.txt must be deleted
	if _, err := os.Stat(filepath.Join(targetDir, "to_delete.txt")); !os.IsNotExist(err) {
		t.Errorf("expected to_delete.txt to be removed")
	}

	// 4. __delta__.json must NOT be extracted to disk
	if _, err := os.Stat(filepath.Join(targetDir, "__delta__.json")); !os.IsNotExist(err) {
		t.Errorf("__delta__.json should not have been extracted to target disk")
	}
}

func TestStreamingZip(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "horizon_stream_test_*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	sourceDir := filepath.Join(tmpDir, "source")
	_ = os.MkdirAll(sourceDir, 0755)
	_ = os.WriteFile(filepath.Join(sourceDir, "test.txt"), []byte("streaming test"), 0644)

	var fullBuf bytes.Buffer
	if err := WriteFullZip(sourceDir, &fullBuf, nil); err != nil {
		t.Fatalf("WriteFullZip failed: %v", err)
	}
	if fullBuf.Len() == 0 {
		t.Fatalf("expected non-empty fullBuf")
	}

	var deltaBuf bytes.Buffer
	if err := WriteDeltaZip(sourceDir, []string{"test.txt"}, nil, &deltaBuf, nil); err != nil {
		t.Fatalf("WriteDeltaZip failed: %v", err)
	}
	if deltaBuf.Len() == 0 {
		t.Fatalf("expected non-empty deltaBuf")
	}
}

func TestParallelExtraction(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "horizon_parallel_extract_*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	sourceDir := filepath.Join(tmpDir, "source")
	_ = os.MkdirAll(sourceDir, 0755)

	numFiles := 50
	for i := 0; i < numFiles; i++ {
		sub := filepath.Join(sourceDir, fmt.Sprintf("sub_%d", i%5))
		_ = os.MkdirAll(sub, 0755)
		_ = os.WriteFile(filepath.Join(sub, fmt.Sprintf("file_%d.txt", i)), fmt.Appendf(nil, "content_%d", i), 0644)
	}

	zipPath := filepath.Join(tmpDir, "parallel.zip")
	if err := CreateFullZip(sourceDir, zipPath, nil); err != nil {
		t.Fatalf("CreateFullZip failed: %v", err)
	}

	destDir := filepath.Join(tmpDir, "extracted")
	if err := ExtractZip(zipPath, destDir, nil); err != nil {
		t.Fatalf("ExtractZip failed: %v", err)
	}

	for i := 0; i < numFiles; i++ {
		sub := filepath.Join(destDir, fmt.Sprintf("sub_%d", i%5))
		data, err := os.ReadFile(filepath.Join(sub, fmt.Sprintf("file_%d.txt", i)))
		if err != nil || string(data) != fmt.Sprintf("content_%d", i) {
			t.Errorf("mismatch on file_%d.txt: %v, got '%s'", i, err, string(data))
		}
	}
}



