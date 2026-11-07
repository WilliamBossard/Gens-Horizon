package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCrossNodeDecrypt(t *testing.T) {
	// Le dossier racine contient le .machine_id et salt.key créés par le script Node
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}

	// Si .machine_id et salt.key existent dans cwd (générés lors de l'exécution Node)
	if _, err := os.Stat(filepath.Join(cwd, ".machine_id")); err == nil {
		t.Setenv("HORIZON_DATA_DIR", cwd)

		authMu.Lock()
		machineID = ""
		salt = nil
		secretKey = nil
		authMu.Unlock()

		// Chiffrer un texte en Go
		goEncrypted, err := Encrypt("HELLO_FROM_GO_TO_NODE")
		if err != nil {
			t.Fatalf("Encrypt failed: %v", err)
		}

		decrypted, _, err := Decrypt(goEncrypted)
		if err != nil {
			t.Fatalf("Decrypt failed: %v", err)
		}
		if decrypted != "HELLO_FROM_GO_TO_NODE" {
			t.Errorf("decrypted text mismatch: got %s", decrypted)
		}
	}
}

