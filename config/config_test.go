package config

import "testing"

func TestDefaultSettings(t *testing.T) {
	s := DefaultSettings()
	if !s.SystemEnabled {
		t.Errorf("expected SystemEnabled=true by default")
	}
	if s.Provider != "google" {
		t.Errorf("expected default provider=google, got %s", s.Provider)
	}
	if s.MaxRetries <= 0 {
		t.Errorf("expected positive MaxRetries, got %d", s.MaxRetries)
	}
}
