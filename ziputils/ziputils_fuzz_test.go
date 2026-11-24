package ziputils

import (
	"path/filepath"
	"strings"
	"testing"
)

func FuzzIsZipEntrySafeUnder(f *testing.F) {
	seeds := []string{
		"saves/world/level.dat",
		"../evil.txt",
		"..\\..\\Windows\\System32\\cmd.exe",
		"mods/CON.jar",
		"mods/nul.txt",
		"config/test:stream.txt",
		"a/b/c\x00d",
		"/etc/passwd",
		"C:\\Windows\\win.ini",
		"",
		".",
		"./options.txt",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	target := filepath.Join(f.TempDir(), "instance")
	cleanTarget := filepath.Clean(target)

	f.Fuzz(func(t *testing.T, entryName string) {
		dest, ok := isZipEntrySafeUnder(cleanTarget, entryName)
		if !ok {
			return
		}
		if strings.ContainsAny(dest, "\x00") {
			t.Fatalf("unsafe null byte in dest %q", dest)
		}
		rel, err := filepath.Rel(cleanTarget, dest)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("zip slip escape detected for %q -> %q (rel=%q, err=%v)", entryName, dest, rel, err)
		}
	})
}
