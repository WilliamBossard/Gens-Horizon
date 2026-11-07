package ipc

import (
	"encoding/json"
	"testing"
)

func TestProgressMessageSerialization(t *testing.T) {
	msg := ProgressMessage{
		Type:     "PROGRESS",
		Step:     "UPLOADING",
		Value:    42,
		Instance: "GensBien",
	}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	var decoded ProgressMessage
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if decoded.Step != "UPLOADING" || decoded.Value != 42 || decoded.Instance != "GensBien" {
		t.Errorf("decoded mismatch: %+v", decoded)
	}
}

