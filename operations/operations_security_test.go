package operations

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestSwapRestoreSnapshotAtomicAndPreservesTargetOnMissingSnapshot(t *testing.T) {
	tmp := t.TempDir()
	snapshotDir := filepath.Join(tmp, "snapshot")
	targetDir := filepath.Join(tmp, "target")

	if err := os.MkdirAll(filepath.Join(snapshotDir, "saves"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, "saves", "level.dat"), []byte("good-snapshot"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "corrupted.txt"), []byte("bad"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := swapRestoreSnapshot(snapshotDir, targetDir); err != nil {
		t.Fatalf("swapRestoreSnapshot failed: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(targetDir, "saves", "level.dat"))
	if err != nil || string(got) != "good-snapshot" {
		t.Fatalf("expected restored file, got err=%v content=%q", err, string(got))
	}
	if _, err := os.Stat(filepath.Join(targetDir, "corrupted.txt")); !os.IsNotExist(err) {
		t.Errorf("expected old corrupted.txt to be replaced by snapshot")
	}

	// Now verify missing snapshot does NOT wipe existing targetDir
	if err := swapRestoreSnapshot(filepath.Join(tmp, "does-not-exist"), targetDir); err == nil {
		t.Fatalf("expected error for missing snapshot")
	}
	if _, err := os.Stat(filepath.Join(targetDir, "saves", "level.dat")); err != nil {
		t.Errorf("existing targetDir was unexpectedly damaged after failed restore: %v", err)
	}
}

func TestIsPathStrictlyUnder(t *testing.T) {
	base := filepath.Join(t.TempDir(), "instance")
	inside := filepath.Join(base, "icon.png")
	outside := filepath.Join(filepath.Dir(base), "secret.png")

	if !isPathStrictlyUnder(base, inside) {
		t.Errorf("expected %q to be inside %q", inside, base)
	}
	if isPathStrictlyUnder(base, outside) {
		t.Errorf("expected %q NOT to be inside %q", outside, base)
	}
	if isPathStrictlyUnder(base, base) {
		t.Errorf("expected root %q NOT to pass strict file check", base)
	}
}

func TestIsValidImageDataURISVGDecodesBase64(t *testing.T) {
	xssSvg := `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`
	xssURI := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(xssSvg))
	if isValidImageDataURI(xssURI) {
		t.Fatalf("expected base64 SVG containing <script> to be rejected")
	}

	onloadSvg := `<svg xmlns="http://www.w3.org/2000/svg" onload="fetch('https://evil.test')"></svg>`
	onloadURI := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(onloadSvg))
	if isValidImageDataURI(onloadURI) {
		t.Fatalf("expected base64 SVG containing onload= to be rejected")
	}

	foSvg := `<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><div>hi</div></foreignObject></svg>`
	foURI := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(foSvg))
	if isValidImageDataURI(foURI) {
		t.Fatalf("expected base64 SVG containing <foreignObject> to be rejected")
	}

	safeSvg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><rect width="16" height="16" fill="#22c55e"/></svg>`
	safeURI := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(safeSvg))
	if !isValidImageDataURI(safeURI) {
		t.Fatalf("expected benign base64 SVG to be accepted")
	}
}

func TestIsRealPathStrictlyUnderRejectsSymlink(t *testing.T) {
	tmp := t.TempDir()
	instDir := filepath.Join(tmp, "instance")
	outsideDir := filepath.Join(tmp, "outside")
	if err := os.MkdirAll(instDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outsideDir, 0755); err != nil {
		t.Fatal(err)
	}
	secretImg := filepath.Join(outsideDir, "secret.png")
	if err := os.WriteFile(secretImg, []byte("PNG"), 0644); err != nil {
		t.Fatal(err)
	}
	validIcon := filepath.Join(instDir, "icon.png")
	if err := os.WriteFile(validIcon, []byte("PNG"), 0644); err != nil {
		t.Fatal(err)
	}
	if !isRealPathStrictlyUnder(instDir, validIcon) {
		t.Fatalf("expected regular file inside instDir to pass isRealPathStrictlyUnder")
	}

	linkIcon := filepath.Join(instDir, "leak.png")
	if err := os.Symlink(secretImg, linkIcon); err == nil {
		if isRealPathStrictlyUnder(instDir, linkIcon) {
			t.Fatalf("expected symlink pointing outside instDir to be rejected")
		}
	}
}

