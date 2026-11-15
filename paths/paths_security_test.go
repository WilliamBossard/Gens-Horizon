package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeInstanceName(t *testing.T) {
	valid := []string{"MyModpack", "1.20.1-Fabric", "survival_server"}
	for _, v := range valid {
		got, err := SanitizeInstanceName(v)
		if err != nil {
			t.Fatalf("SanitizeInstanceName(%q) unexpected error: %v", v, err)
		}
		if got != v {
			t.Fatalf("SanitizeInstanceName(%q) = %q, want %q", v, got, v)
		}
	}

	invalid := []string{"", "   ", ".", "..", "../evil", "..\\evil", "a/b", "a\\b", "foo..bar", "bad\x00name"}
	for _, v := range invalid {
		if _, err := SanitizeInstanceName(v); err == nil {
			t.Errorf("SanitizeInstanceName(%q) expected error, got nil", v)
		}
	}
}

func TestSafeJoinUnder(t *testing.T) {
	base := t.TempDir()

	got, err := SafeJoinUnder(base, "mods/jei.jar")
	if err != nil {
		t.Fatalf("SafeJoinUnder valid path failed: %v", err)
	}
	want := filepath.Join(base, "mods", "jei.jar")
	if got != want {
		t.Fatalf("SafeJoinUnder got %q, want %q", got, want)
	}

	badPaths := []string{"", ".", "..", "../escape", "../../etc/passwd", "mods/../../escape"}
	for _, rel := range badPaths {
		if _, err := SafeJoinUnder(base, rel); err == nil {
			t.Errorf("SafeJoinUnder(%q) expected error, got nil", rel)
		}
	}
}

func TestResolveWithFallbackMigratesAtomically(t *testing.T) {
	tmp := t.TempDir()
	newPath := filepath.Join(tmp, "auth", "token.json")
	legacyPath := filepath.Join(tmp, "legacy", "token.json")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0755); err != nil {
		t.Fatal(err)
	}
	content := []byte(`{"access_token":"abc"}`)
	if err := os.WriteFile(legacyPath, content, 0600); err != nil {
		t.Fatal(err)
	}

	resolved := ResolveWithFallback(newPath, legacyPath)
	if resolved != newPath {
		t.Fatalf("ResolveWithFallback returned %q, want %q", resolved, newPath)
	}
	got, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("expected migrated file at %q: %v", newPath, err)
	}
	if string(got) != string(content) {
		t.Fatalf("migrated content mismatch: got %q", string(got))
	}
}

