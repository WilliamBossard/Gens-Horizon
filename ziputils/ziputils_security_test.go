package ziputils

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func createTestZip(t *testing.T, dest string, entries []struct {
	name    string
	content string
	mode    os.FileMode
}) {
	t.Helper()
	f, err := os.Create(dest)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, e := range entries {
		hdr := &zip.FileHeader{
			Name:   e.name,
			Method: zip.Deflate,
		}
		if e.mode != 0 {
			hdr.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExtractZipBlocksZipSlipAndSymlinks(t *testing.T) {
	tmp := t.TempDir()
	zipPath := filepath.Join(tmp, "test.zip")
	targetDir := filepath.Join(tmp, "out")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}

	createTestZip(t, zipPath, []struct {
		name    string
		content string
		mode    os.FileMode
	}{
		{name: "../outside.txt", content: "evil", mode: 0644},
		{name: "sub/../../outside2.txt", content: "evil2", mode: 0644},
		{name: "link_escape", content: "/etc/passwd", mode: os.ModeSymlink | 0777},
		{name: "valid/hello.txt", content: "ok", mode: os.ModeSetuid | 0755},
	})

	if err := ExtractZip(zipPath, targetDir, nil); err != nil {
		t.Fatalf("ExtractZip failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(tmp, "outside.txt")); !os.IsNotExist(err) {
		t.Errorf("Zip-Slip outside.txt should not have been extracted")
	}
	if _, err := os.Stat(filepath.Join(tmp, "outside2.txt")); !os.IsNotExist(err) {
		t.Errorf("Zip-Slip outside2.txt should not have been extracted")
	}
	if _, err := os.Lstat(filepath.Join(targetDir, "link_escape")); !os.IsNotExist(err) {
		t.Errorf("Symlink entry link_escape should have been skipped")
	}

	got, err := os.ReadFile(filepath.Join(targetDir, "valid", "hello.txt"))
	if err != nil {
		t.Fatalf("expected valid/hello.txt: %v", err)
	}
	if string(got) != "ok" {
		t.Fatalf("valid/hello.txt content = %q, want ok", string(got))
	}
}

func TestWriteFullZipFiltersIgnoredCaseInsensitiveAndSpecialFiles(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "instance")
	for _, d := range []string{"config", "Saves", "Versions", "CRASH-REPORTS"} {
		if err := os.MkdirAll(filepath.Join(srcDir, d), 0755); err != nil {
			t.Fatal(err)
		}
	}

	mustWrite := func(rel, data string) {
		if err := os.WriteFile(filepath.Join(srcDir, rel), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("config/app.toml", "setting=1")
	mustWrite("Saves/level.dat", "world")
	mustWrite("Versions/1.20.jar", "ignored-dir")
	mustWrite("CRASH-REPORTS/crash.txt", "ignored-dir")
	mustWrite("._DS_Store", "ignored-file")
	mustWrite("download.tmp", "ignored-tmp")
	mustWrite(".DS_Store", "ignored-exact")

	var buf bytes.Buffer
	if err := WriteFullZip(srcDir, &buf, nil); err != nil {
		t.Fatalf("WriteFullZip failed: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader failed: %v", err)
	}

	seen := make(map[string]bool)
	for _, f := range zr.File {
		seen[f.Name] = true
		if strings.Contains(f.Name, "Versions/") || strings.Contains(f.Name, "CRASH-REPORTS/") {
			t.Errorf("ignored dir entry included: %s", f.Name)
		}
		if strings.HasSuffix(f.Name, ".tmp") || strings.Contains(f.Name, "._DS_Store") || strings.Contains(f.Name, ".DS_Store") {
			t.Errorf("ignored file entry included: %s", f.Name)
		}
	}
	if !seen["config/app.toml"] || !seen["Saves/level.dat"] {
		t.Errorf("expected config/app.toml and Saves/level.dat in archive, got: %v", seen)
	}
}

func TestVerifyZipIntegrityAcceptsEmptyArchive(t *testing.T) {
	tmp := t.TempDir()
	emptyZip := filepath.Join(tmp, "empty.zip")
	f, err := os.Create(emptyZip)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	if err := VerifyZipIntegrity(emptyZip); err != nil {
		t.Errorf("VerifyZipIntegrity should accept valid empty ZIP archive, got: %v", err)
	}
}

func TestExtractZipCaseCollisionLastEntryWinsWithoutRace(t *testing.T) {
	tmp := t.TempDir()
	zipPath := filepath.Join(tmp, "collision.zip")
	targetDir := filepath.Join(tmp, "out")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}

	createTestZip(t, zipPath, []struct {
		name    string
		content string
		mode    os.FileMode
	}{
		{name: "config/MyFile.txt", content: "first-version", mode: 0644},
		{name: "config/myfile.txt", content: "second-version", mode: 0644},
	})

	if err := ExtractZip(zipPath, targetDir, nil); err != nil {
		t.Fatalf("ExtractZip with case collision failed: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(targetDir, "config", "myfile.txt"))
	if err != nil {
		t.Fatalf("failed to read extracted file: %v", err)
	}
	if string(got) != "second-version" {
		t.Fatalf("expected last entry 'second-version', got %q", string(got))
	}
}

func TestResolveNearestRealDirBlocksSymlinkParentEscape(t *testing.T) {
	tmp := t.TempDir()
	outside := filepath.Join(tmp, "outside")
	root := filepath.Join(tmp, "root")
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(root, "escape")
	if err := os.Symlink(outside, linkDir); err != nil {
		t.Skipf("os.Symlink unavailable on this host: %v", err)
	}

	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureSafeDestDir(rootReal, filepath.Join(linkDir, "nested", "sub")); err == nil {
		t.Fatalf("expected ensureSafeDestDir to reject symlinked parent directory escaping target root")
	}
}

func TestApplyDeltaPrunesEmptyParentDirectories(t *testing.T) {
	tmp := t.TempDir()
	targetDir := filepath.Join(tmp, "inst")
	nestedFile := filepath.Join(targetDir, "mods", "optional", "old.jar")
	if err := os.MkdirAll(filepath.Dir(nestedFile), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nestedFile, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	deltaZip := filepath.Join(tmp, "delta.zip")
	if err := CreateDeltaZip(targetDir, nil, []string{"mods/optional/old.jar"}, deltaZip, nil); err != nil {
		t.Fatalf("CreateDeltaZip failed: %v", err)
	}
	if err := ApplyDelta(deltaZip, targetDir, nil); err != nil {
		t.Fatalf("ApplyDelta failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(targetDir, "mods")); !os.IsNotExist(err) {
		t.Fatalf("expected empty parent directory 'mods' to be pruned after deletion")
	}
	if _, err := os.Stat(targetDir); err != nil {
		t.Fatalf("target root directory should still exist: %v", err)
	}
}

func TestExtractZipReplacesPreexistingDirectoryAtFilePath(t *testing.T) {
	tmp := t.TempDir()
	zipPath := filepath.Join(tmp, "replace.zip")
	targetDir := filepath.Join(tmp, "out")
	if err := os.MkdirAll(filepath.Join(targetDir, "config", "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	// Pre-existing directory where the archive expects a regular file "config"
	if err := os.WriteFile(filepath.Join(targetDir, "config", "nested", "old.txt"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	createTestZip(t, zipPath, []struct {
		name    string
		content string
		mode    os.FileMode
	}{
		{name: "config", content: "new-file-content", mode: 0644},
	})

	if err := ExtractZip(zipPath, targetDir, nil); err != nil {
		t.Fatalf("ExtractZip failed when replacing directory with file: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(targetDir, "config"))
	if err != nil {
		t.Fatalf("expected regular file 'config': %v", err)
	}
	if string(got) != "new-file-content" {
		t.Fatalf("unexpected content: %q", string(got))
	}
}

func TestExtractZipEntriesParallelEnforcesTotalBytesBudgetRealTime(t *testing.T) {
	tmp := t.TempDir()
	zipPath := filepath.Join(tmp, "budget.zip")
	targetDir := filepath.Join(tmp, "out")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}

	chunk := strings.Repeat("A", 400)
	createTestZip(t, zipPath, []struct {
		name    string
		content string
		mode    os.FileMode
	}{
		{name: "f1.bin", content: chunk, mode: 0644},
		{name: "f2.bin", content: chunk, mode: 0644},
		{name: "f3.bin", content: chunk, mode: 0644},
		{name: "f4.bin", content: chunk, mode: 0644},
	})

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()

	// Plafond total = 1000 octets (inférieur à 4*400=1600), alors que chaque fichier fait 400 (< maxSingle=2000)
	err = extractZipEntriesParallelWithLimits(targetDir, zr.File, 1000, 2000, nil)
	if err == nil || !strings.Contains(err.Error(), "trop volumineuse") {
		t.Fatalf("expected total-bytes real-time overflow error, got: %v", err)
	}

	var diskTotal int64
	_ = filepath.WalkDir(targetDir, func(_ string, d os.DirEntry, walkErr error) error {
		if walkErr == nil && !d.IsDir() {
			if info, iErr := d.Info(); iErr == nil {
				diskTotal += info.Size()
			}
		}
		return nil
	})
	if diskTotal > 1000 {
		t.Fatalf("expected extracted bytes on disk <= 1000 due to real-time limit, got %d", diskTotal)
	}
}

func TestCreateFullZipWithHintMatchesCreateFullZip(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "inst")
	if err := os.MkdirAll(filepath.Join(src, "config"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "logs"), 0755); err != nil {
		t.Fatal(err)
	}
	payload := strings.Repeat("config-line\n", 50)
	if err := os.WriteFile(filepath.Join(src, "config", "mod.cfg"), []byte(payload), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "logs", "latest.log"), []byte("ignored"), 0644); err != nil {
		t.Fatal(err)
	}

	zipHintPath := filepath.Join(tmp, "hint.zip")
	var lastPct int
	if err := CreateFullZipWithHint(src, int64(len(payload)), zipHintPath, func(pct int) {
		lastPct = pct
	}); err != nil {
		t.Fatalf("CreateFullZipWithHint failed: %v", err)
	}
	if lastPct != 100 {
		t.Fatalf("expected progress to reach 100%%, got %d", lastPct)
	}

	zr, err := zip.OpenReader(zipHintPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	if len(zr.File) != 1 || zr.File[0].Name != "config/mod.cfg" {
		t.Fatalf("expected only config/mod.cfg in archive, got %d entries", len(zr.File))
	}
}


func TestApplyDeltaFailsOnCorruptedDeltaJSON(t *testing.T) {
	tmp := t.TempDir()
	zipPath := filepath.Join(tmp, "bad_delta.zip")
	targetDir := filepath.Join(tmp, "out")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}

	createTestZip(t, zipPath, []struct {
		name    string
		content string
		mode    os.FileMode
	}{
		{name: "__delta__.json", content: "{corrupted-json", mode: 0644},
		{name: "config/ok.txt", content: "ok", mode: 0644},
	})

	if err := ApplyDelta(zipPath, targetDir, nil); err == nil {
		t.Fatalf("expected ApplyDelta to fail on corrupted __delta__.json")
	}
}
