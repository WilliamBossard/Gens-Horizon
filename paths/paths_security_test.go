package paths

import "testing"

func TestSanitizeAndSafeJoinRejectReservedAndColon(t *testing.T) {
	badNames := []string{"CON", "nul", "COM1", "LPT9.txt", "foo:bar", "a/b", "a\\b", ".."}
	for _, name := range badNames {
		if _, err := SanitizeInstanceName(name); err == nil {
			t.Errorf("expected SanitizeInstanceName(%q) to fail", name)
		}
	}

	base := t.TempDir()
	badRels := []string{"CON/file.txt", "sub/NUL.dat", "foo:ads", "../escape"}
	for _, rel := range badRels {
		if _, err := SafeJoinUnder(base, rel); err == nil {
			t.Errorf("expected SafeJoinUnder(%q) to fail", rel)
		}
	}
}
