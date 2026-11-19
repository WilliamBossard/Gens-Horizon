package scanner

import "testing"

func TestInstanceLockIgnored(t *testing.T) {
	if !IsIgnoredFile("instance.lock") {
		t.Fatalf("instance.lock must be ignored by scanner")
	}
	if !IsIgnoredFile("INSTANCE.LOCK") {
		t.Fatalf("INSTANCE.LOCK must be ignored case-insensitively")
	}
}
