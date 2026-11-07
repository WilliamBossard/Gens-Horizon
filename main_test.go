package main

import "testing"

func TestContainsCommand(t *testing.T) {
	args := []string{"--upload", "MyInstance"}
	if !containsCommand(args, "--upload", "upload") {
		t.Errorf("expected containsCommand to match --upload")
	}
	if containsCommand(args, "--sync", "sync") {
		t.Errorf("did not expect containsCommand to match --sync")
	}
}
