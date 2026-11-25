package auth

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/WilliamBossard/Gens-Horizon/ipc"
	"github.com/WilliamBossard/Gens-Horizon/paths"
)

const (
	e2eKeyFileName   = "e2e.key"
	e2eDpapiPrefix   = "DPAPI:"
	e2eKeyringPrefix = "KEYRING:"
)

type e2eKeyPayload struct {
	Passphrase string `json:"passphrase"`
}

func getE2EKeyPath() string {
	return filepath.Join(paths.GetAuthDir(), e2eKeyFileName)
}

// SaveE2EPassphrase chiffre et sauvegarde la passphrase E2E localement (DPAPI / trousseau OS).
func SaveE2EPassphrase(passphrase string) error {
	clean := strings.TrimSpace(passphrase)
	if len(clean) < 12 {
		return errors.New("la passphrase E2E doit contenir au moins 12 caractères")
	}
	payload, err := json.Marshal(e2eKeyPayload{Passphrase: clean})
	if err != nil {
		return err
	}
	toWrite := payload
	sealedByOS := false
	if dpapiAvailable() {
		if sealed, err := dpapiProtect(payload); err == nil {
			toWrite = append([]byte(e2eDpapiPrefix), sealed...)
			sealedByOS = true
		} else {
			ipc.Logf("[auth] AVERTISSEMENT : échec protection DPAPI pour e2e.key (%v).", err)
		}
	} else if osKeyringAvailable() {
		if refID, err := osKeyringProtect(payload); err == nil {
			toWrite = append([]byte(e2eKeyringPrefix), refID...)
			sealedByOS = true
		} else {
			ipc.Logf("[auth] AVERTISSEMENT : échec protection trousseau OS pour e2e.key (%v).", err)
		}
	}
	if !sealedByOS {
		ipc.Logf("[auth] AVERTISSEMENT : aucun trousseau OS fonctionnel (DPAPI/keyring), e2e.key stocké localement avec permissions 0600 uniquement.")
	}
	target := getE2EKeyPath()
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	return writeAtomicFile(target, toWrite, 0600)
}

// LoadE2EPassphrase lit et déchiffre la passphrase E2E locale. Retourne ("", nil) si absente.
func LoadE2EPassphrase() (string, error) {
	target := getE2EKeyPath()
	raw, err := os.ReadFile(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	var plain []byte
	if bytes.HasPrefix(raw, []byte(e2eDpapiPrefix)) {
		plain, err = dpapiUnprotect(raw[len(e2eDpapiPrefix):])
		if err != nil {
			return "", fmt.Errorf("échec déchiffrement DPAPI clé E2E : %w", err)
		}
	} else if bytes.HasPrefix(raw, []byte(e2eKeyringPrefix)) {
		plain, err = osKeyringUnprotect(raw[len(e2eKeyringPrefix):])
		if err != nil {
			return "", fmt.Errorf("échec déchiffrement trousseau clé E2E : %w", err)
		}
	} else {
		plain = raw
	}
	var payload e2eKeyPayload
	if err := json.Unmarshal(plain, &payload); err != nil {
		return "", err
	}
	return strings.TrimSpace(payload.Passphrase), nil
}

// GenerateRecoveryPassphrase génère une clé de récupération lisible au format GH-XXXX-XXXX-XXXX-XXXX.
func GenerateRecoveryPassphrase() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	parts := make([]string, 4)
	for g := 0; g < 4; g++ {
		var sb strings.Builder
		for i := 0; i < 4; i++ {
			sb.WriteByte(alphabet[int(buf[g*4+i])%len(alphabet)])
		}
		parts[g] = sb.String()
	}
	return "GH-" + strings.Join(parts, "-"), nil
}
