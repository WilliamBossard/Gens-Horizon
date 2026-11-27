package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/ipc"
	"github.com/WilliamBossard/Gens-Horizon/paths"
)

var (
	// ErrTokenCorrupted indique que le fichier de token ou sa charge chiffrée est structurellement corrompu
	// (distinct d'une indisponibilité transitoire du trousseau OS / DPAPI).
	ErrTokenCorrupted = errors.New("token corrompu ou illisible")

	authMu    sync.Mutex
	machineID string
	salt      []byte
	secretKey []byte
)

func getMachineIDFile() string {
	newPath := filepath.Join(paths.GetAuthDir(), ".machine_id")
	legacy1 := filepath.Join(paths.GetHorizonDataDir(), ".machine_id")
	legacy2 := filepath.Join(paths.GetLegacyBinDir(), ".machine_id")
	var candidates []string
	candidates = append(candidates, newPath, legacy1, legacy2)
	if cfgDir, err := os.UserConfigDir(); err == nil && cfgDir != "" {
		candidates = append(candidates, filepath.Join(cfgDir, "GensHorizon", ".machine_id"))
	}
	return paths.ResolveWithFallback(candidates[0], candidates[1:]...)
}

func getSaltFile() string {
	newPath := filepath.Join(paths.GetAuthDir(), "salt.key")
	legacy1 := filepath.Join(paths.GetHorizonDataDir(), "salt.key")
	legacy2 := filepath.Join(paths.GetLegacyBinDir(), "salt.key")
	return paths.ResolveWithFallback(newPath, legacy1, legacy2)
}

func initAuthData() error {
	if machineID != "" && salt != nil {
		return nil
	}

	dataDir := paths.GetHorizonDataDir()
	_ = os.MkdirAll(dataDir, 0755)

	_ = os.MkdirAll(paths.GetAuthDir(), 0700)

	mIDFile := getMachineIDFile()
	sFile := getSaltFile()

	// 1. Initialisation de .machine_id (écriture atomique + 0600)
	if data, err := os.ReadFile(mIDFile); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		machineID = strings.TrimSpace(string(data))
	} else {
		buf := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, buf); err == nil {
			machineID = hex.EncodeToString(buf)
			if err := writeAtomicFile(mIDFile, []byte(machineID), 0600); err != nil {
				savedFallback := false
				if cfgDir, cErr := os.UserConfigDir(); cErr == nil && cfgDir != "" {
					secDir := filepath.Join(cfgDir, "GensHorizon")
					if mkErr := os.MkdirAll(secDir, 0700); mkErr == nil {
						if wErr := writeAtomicFile(filepath.Join(secDir, ".machine_id"), []byte(machineID), 0600); wErr == nil {
							savedFallback = true
						}
					}
				}
				if !savedFallback {
					ipc.Logf("[Auth] AVERTISSEMENT : Écriture de .machine_id échouée (%v). Utilisation du fallback matériel.", err)
					machineID = generateHardwareFallbackID()
				}
			}
		} else {
			machineID = generateHardwareFallbackID()
		}
	}

	// 2. Initialisation de salt.key (scellement DPAPI Windows ou Trousseau OS macOS/Linux + compatibilité héritée)
	const dpapiPrefix = "DPAPIv1:"
	const keyringPrefix = "KEYRINGv1:"
	saltData, saltReadErr := os.ReadFile(sFile)
	if saltReadErr == nil {
		if len(saltData) < 16 {
			return fmt.Errorf("salt.key existant tronqué ou corrompu (%d octets) — écrasement refusé pour protéger les tokens", len(saltData))
		}
		data := saltData
		if len(data) > len(dpapiPrefix) && string(data[:len(dpapiPrefix)]) == dpapiPrefix {
			unsealed, err := dpapiUnprotect(data[len(dpapiPrefix):])
			if err != nil || len(unsealed) < 16 {
				return fmt.Errorf("impossible de désceller salt.key via DPAPI : %w", err)
			}
			salt = unsealed
		} else if len(data) > len(keyringPrefix) && string(data[:len(keyringPrefix)]) == keyringPrefix {
			unsealed, err := osKeyringUnprotect(data[len(keyringPrefix):])
			if err != nil || len(unsealed) < 16 {
				return fmt.Errorf("impossible de récupérer salt.key depuis le trousseau OS : %w", err)
			}
			salt = unsealed
		} else {
			salt = data
			// Migration transparente du salt en clair vers DPAPI (Windows) ou Trousseau OS (macOS/Linux)
			if dpapiAvailable() {
				if sealed, err := dpapiProtect(salt); err == nil {
					payload := append([]byte(dpapiPrefix), sealed...)
					if wErr := writeAtomicFile(sFile, payload, 0600); wErr != nil {
						ipc.Logf("[auth] AVERTISSEMENT : échec migration DPAPI salt.key (%v).", wErr)
					}
				}
			} else if osKeyringAvailable() {
				if refID, err := osKeyringProtect(salt); err == nil {
					payload := append([]byte(keyringPrefix), refID...)
					if wErr := writeAtomicFile(sFile, payload, 0600); wErr != nil {
						ipc.Logf("[auth] AVERTISSEMENT : échec migration trousseau salt.key (%v).", wErr)
					}
				}
			}
		}
	} else if !os.IsNotExist(saltReadErr) {
		return fmt.Errorf("impossible de lire salt.key : %w", saltReadErr)
	} else {
		salt = make([]byte, 16)
		if _, err := io.ReadFull(rand.Reader, salt); err != nil {
			return fmt.Errorf("impossible de générer un salt aléatoire : %w", err)
		}
		toWrite := salt
		sealedByOS := false
		if dpapiAvailable() {
			if sealed, err := dpapiProtect(salt); err == nil {
				toWrite = append([]byte(dpapiPrefix), sealed...)
				sealedByOS = true
			} else {
				ipc.Logf("[auth] AVERTISSEMENT : échec protection DPAPI pour salt.key (%v).", err)
			}
		} else if osKeyringAvailable() {
			if refID, err := osKeyringProtect(salt); err == nil {
				toWrite = append([]byte(keyringPrefix), refID...)
				sealedByOS = true
			} else {
				ipc.Logf("[auth] AVERTISSEMENT : échec protection trousseau OS pour salt.key (%v).", err)
			}
		}
		if !sealedByOS {
			ipc.Logf("[auth] AVERTISSEMENT : aucun trousseau OS fonctionnel (DPAPI/keyring), salt.key stocké localement avec permissions 0600 uniquement.")
		}
		if err := writeAtomicFile(sFile, toWrite, 0600); err != nil {
			return fmt.Errorf("impossible d'écrire le salt de sécurité (salt.key) : %w", err)
		}
	}

	// 3. Dérivation rapide HKDF-SHA256 sur machineID 256-bit (le format legacy PBKDF2-600k reste déchiffré paresseusement pour migration)
	derivedKey, err := hkdf.Key(sha256.New, []byte(machineID), salt, "GensHorizon-Token-v2", 32)
	if err != nil {
		return fmt.Errorf("échec dérivation clé HKDF : %w", err)
	}
	secretKey = derivedKey
	return nil
}

func deriveLegacyPBKDF2Key() ([]byte, error) {
	authMu.Lock()
	mID := machineID
	s := salt
	authMu.Unlock()
	if mID == "" || len(s) == 0 {
		return nil, nil
	}
	return pbkdf2.Key(sha256.New, mID, s, 600000, 32)
}

func generateHardwareFallbackID() string {
	hostname, _ := os.Hostname()
	mac := "00:00:00:00:00:00"
	if ifaces, err := net.Interfaces(); err == nil {
		for _, iface := range ifaces {
			if len(iface.HardwareAddr) > 0 && iface.HardwareAddr.String() != "00:00:00:00:00:00" {
				mac = iface.HardwareAddr.String()
				break
			}
		}
	}
	userName := ""
	if u, err := user.Current(); err == nil {
		userName = u.Username
	} else {
		userName = os.Getenv("USERNAME")
		if userName == "" {
			userName = os.Getenv("USER")
		}
	}
	h := sha256.New()
	h.Write([]byte(hostname + mac + userName))
	return hex.EncodeToString(h.Sum(nil))
}

func GetSecretKey() ([]byte, error) {
	authMu.Lock()
	defer authMu.Unlock()
	if secretKey != nil {
		return secretKey, nil
	}
	if err := initAuthData(); err != nil {
		return nil, err
	}
	return secretKey, nil
}

// Encrypt chiffre du texte en AES-256-GCM selon le format v2 Horizon (HKDF-SHA256):
// "v2:" + hex(iv) + ":" + hex(authTag) + ":" + hex(ciphertext)
func Encrypt(text string) (string, error) {
	key, err := GetSecretKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	iv := make([]byte, 12) // 96-bit IV standard pour GCM
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return "", err
	}

	// Go append automatiquement le tag (16 octets) à la fin du ciphertext
	sealed := gcm.Seal(nil, iv, []byte(text), nil)
	tagSize := gcm.Overhead() // 16 octets
	ciphertext := sealed[:len(sealed)-tagSize]
	authTag := sealed[len(sealed)-tagSize:]

	return "v2:" + hex.EncodeToString(iv) + ":" + hex.EncodeToString(authTag) + ":" + hex.EncodeToString(ciphertext), nil
}

func openGCM(key []byte, ivHex, tagHex, ctHex string) (string, error) {
	iv, err := hex.DecodeString(ivHex)
	if err != nil {
		return "", fmt.Errorf("%w: IV hex invalide", ErrTokenCorrupted)
	}
	authTag, err := hex.DecodeString(tagHex)
	if err != nil {
		return "", fmt.Errorf("%w: AuthTag hex invalide", ErrTokenCorrupted)
	}
	ciphertext, err := hex.DecodeString(ctHex)
	if err != nil {
		return "", fmt.Errorf("%w: Ciphertext hex invalide", ErrTokenCorrupted)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(iv) != gcm.NonceSize() {
		return "", fmt.Errorf("%w: taille IV GCM invalide", ErrTokenCorrupted)
	}
	ciphertextWithTag := append(ciphertext, authTag...)
	plaintext, err := gcm.Open(nil, iv, ciphertextWithTag, nil)
	if err != nil {
		return "", fmt.Errorf("%w: échec de déchiffrement GCM (%v)", ErrTokenCorrupted, err)
	}
	return string(plaintext), nil
}

// Decrypt déchiffre une chaîne chiffrée en AES-256-GCM (v2 HKDF ou v1 legacy PBKDF2) ou l'ancien format AES-CBC
func Decrypt(encryptedText string) (string, bool, error) {
	key, err := GetSecretKey()
	if err != nil {
		return "", false, err
	}
	parts := strings.Split(strings.TrimSpace(encryptedText), ":")

	if len(parts) == 4 && parts[0] == "v2" {
		plaintext, err := openGCM(key, parts[1], parts[2], parts[3])
		if err != nil {
			return "", false, err
		}
		return plaintext, false, nil
	} else if len(parts) == 3 {
		// Format legacy AES-256-GCM (PBKDF2-600k ou clé directe de test) -> migration vers v2
		if legacyKey, lErr := deriveLegacyPBKDF2Key(); lErr == nil && len(legacyKey) > 0 {
			if plaintext, err := openGCM(legacyKey, parts[0], parts[1], parts[2]); err == nil {
				return plaintext, true, nil
			}
		}
		plaintext, err := openGCM(key, parts[0], parts[1], parts[2])
		if err != nil {
			return "", false, err
		}
		return plaintext, true, nil
	} else if len(parts) >= 2 {
		// Format legacy AES-256-CBC (iv:ciphertext)
		iv, err := hex.DecodeString(parts[0])
		if err != nil {
			return "", false, fmt.Errorf("%w: IV CBC hex invalide", ErrTokenCorrupted)
		}
		ciphertext, err := hex.DecodeString(strings.Join(parts[1:], ":"))
		if err != nil {
			return "", false, fmt.Errorf("%w: Ciphertext CBC hex invalide", ErrTokenCorrupted)
		}

		cbcKey := key
		if legacyKey, lErr := deriveLegacyPBKDF2Key(); lErr == nil && len(legacyKey) > 0 {
			cbcKey = legacyKey
		}

		block, err := aes.NewCipher(cbcKey)
		if err != nil {
			return "", false, err
		}
		if len(iv) != aes.BlockSize {
			return "", false, fmt.Errorf("%w: taille IV CBC invalide", ErrTokenCorrupted)
		}
		if len(ciphertext)%aes.BlockSize != 0 {
			return "", false, fmt.Errorf("%w: taille de bloc CBC invalide", ErrTokenCorrupted)
		}

		mode := cipher.NewCBCDecrypter(block, iv)
		plaintext := make([]byte, len(ciphertext))
		mode.CryptBlocks(plaintext, ciphertext)

		// Unpadding PKCS#7
		unpadded, err := pkcs7Unpad(plaintext)
		if err != nil {
			return "", false, fmt.Errorf("%w: %v", ErrTokenCorrupted, err)
		}
		return string(unpadded), true, nil
	}

	return "", false, fmt.Errorf("%w: format de token chiffré non reconnu", ErrTokenCorrupted)
}

func pkcs7Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("données vides")
	}
	padLen := int(data[len(data)-1])
	if padLen == 0 || padLen > aes.BlockSize || padLen > len(data) {
		return nil, errors.New("padding PKCS#7 invalide")
	}
	for i := len(data) - padLen; i < len(data); i++ {
		if data[i] != byte(padLen) {
			return nil, errors.New("padding PKCS#7 corrompu")
		}
	}
	return data[:len(data)-padLen], nil
}

// GetSecureToken lit et déchiffre le token OAuth depuis son fichier
func GetSecureToken(filePath string, target any) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	content := strings.TrimSpace(string(data))
	if len(content) == 0 {
		return fmt.Errorf("%w: fichier de token vide", ErrTokenCorrupted)
	}

	// Si le fichier commence par '{', c'est du JSON en clair -> migrer vers chiffré
	if strings.HasPrefix(content, "{") {
		if err := json.Unmarshal([]byte(content), target); err != nil {
			return fmt.Errorf("%w: json token invalide (%v)", ErrTokenCorrupted, err)
		}
		ipc.Logf("[Auth] Sécurisation immédiate du token en clair : %s", filePath)
		_ = EncryptToken(filePath, target)
		return nil
	}

	decrypted, needsMigration, err := Decrypt(content)
	if err != nil {
		return fmt.Errorf("impossible de déchiffrer le token : %w", err)
	}

	if err := json.Unmarshal([]byte(decrypted), target); err != nil {
		return fmt.Errorf("%w: json token invalide après déchiffrement (%v)", ErrTokenCorrupted, err)
	}

	if needsMigration {
		ipc.Logf("[Auth] Migration du token legacy CBC vers GCM : %s", filePath)
		_ = EncryptToken(filePath, target)
	}

	return nil
}

// EncryptToken chiffre et écrit les données de token sur disque de façon atomique
func EncryptToken(filePath string, tokenData any) error {
	rawJSON, err := json.Marshal(tokenData)
	if err != nil {
		return err
	}
	encrypted, err := Encrypt(string(rawJSON))
	if err != nil {
		return err
	}

	// Écriture atomique via fichier temporaire
	return writeAtomicFile(filePath, []byte(encrypted), 0600)
}

func writeAtomicFile(filePath string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(filePath), 0700); err != nil {
		return err
	}
	tmpFile := fmt.Sprintf("%s.tmp.%d.%d", filePath, os.Getpid(), time.Now().UnixNano())
	f, err := os.OpenFile(tmpFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpFile)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpFile)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpFile)
		return err
	}
	var renameErr error
	for attempt := 0; attempt < 5; attempt++ {
		if err := os.Rename(tmpFile, filePath); err == nil {
			return nil
		} else {
			renameErr = err
		}
		if attempt < 4 {
			time.Sleep(time.Duration(40*(attempt+1)) * time.Millisecond)
		}
	}
	_ = os.Remove(tmpFile)
	return renameErr
}

// TokenData représente la structure standard d'un jeton OAuth2
type TokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiryDate   int64  `json:"expiry_date,omitempty"` // Timestamp Unix ms
	TokenType    string `json:"token_type,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// HasExpired vérifie si le token expire bientôt (marge de 60s)
func (t *TokenData) HasExpired() bool {
	if t.AccessToken == "" {
		return true
	}
	if t.ExpiryDate == 0 {
		return false
	}
	return (t.ExpiryDate - 60000) < time.Now().UnixMilli()
}
