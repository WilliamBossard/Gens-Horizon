package auth

import (
	"testing"
)

func TestSaveE2EPassphraseEnforcesMinLength12(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("APPDATA", tmp)
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)

	if err := SaveE2EPassphrase("short123"); err == nil {
		t.Fatal("expected SaveE2EPassphrase to reject passphrase shorter than 12 characters")
	}
	if err := SaveE2EPassphrase("strong-passphrase-1234"); err != nil {
		t.Fatalf("expected valid 12+ char passphrase to succeed, got: %v", err)
	}
}
