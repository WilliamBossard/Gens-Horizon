package ziputils

import (
	"archive/zip"
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

