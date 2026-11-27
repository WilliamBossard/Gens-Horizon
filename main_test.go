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
	if !containsCommand([]string{"--status"}, "--status", "status") {
		t.Errorf("expected containsCommand to match --status")
	}
	if !containsCommand([]string{"--help"}, "--help", "-h") {
		t.Errorf("expected containsCommand to match --help")
	}
	if containsCommand([]string{"--sync", "status"}, "--status", "status") {
		t.Errorf("did not expect positional instance name 'status' to match --status")
	}
	if !containsCommand([]string{"delete", "MyInstance"}, "--delete", "delete") {
		t.Errorf("expected bare command 'delete' at args[0] to match")
	}
}

