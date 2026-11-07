package auth

import (
	"crypto/aes"
	"crypto/cipher"
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
	"golang.org/x/crypto/pbkdf2"
)

var (
	authMu    sync.Mutex
	machineID string
	salt      []byte
	secretKey []byte
)

func getMachineIDFile() string {
	newPath := filepath.Join(paths.GetAuthDir(), ".machine_id")
	legacy1 := filepath.Join(paths.GetHorizonDataDir(), ".machine_id")
	legacy2 := filepath.Join(paths.GetLegacyBinDir(), ".machine_id")
	return paths.ResolveWithFallback(newPath, legacy1, legacy2)
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

	mIDFile := getMachineIDFile()
	sFile := getSaltFile()

	// 1. Initialisation de .machine_id
	if data, err := os.ReadFile(mIDFile); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		machineID = strings.TrimSpace(string(data))
	} else {
		buf := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, buf); err == nil {
			machineID = hex.EncodeToString(buf)
			if err := os.WriteFile(mIDFile, []byte(machineID), 0600); err != nil {
				ipc.Logf("[Auth] AVERTISSEMENT : Écriture de .machine_id échouée (%v). Utilisation du fallback matériel.", err)
				machineID = generateHardwareFallbackID()
			}
		} else {
			machineID = generateHardwareFallbackID()
		}
	}

	// 2. Initialisation de salt.key
	if data, err := os.ReadFile(sFile); err == nil && len(data) >= 16 {
		salt = data
	} else {
		salt = make([]byte, 16)
		if _, err := io.ReadFull(rand.Reader, salt); err != nil {
			return fmt.Errorf("impossible de générer un salt aléatoire : %w", err)
		}
		if err := os.WriteFile(sFile, salt, 0600); err != nil {
			return fmt.Errorf("impossible d'écrire le salt de sécurité (salt.key) : %w", err)
		}
	}

	// 3. Dérivation PBKDF2 (600 000 itérations, SHA-256, clé 32 octets)
	secretKey = pbkdf2.Key([]byte(machineID), salt, 600000, 32, sha256.New)
	return nil
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

// Encrypt chiffre du texte en AES-256-GCM selon le format standard Horizon:
// hex(iv) + ":" + hex(authTag) + ":" + hex(ciphertext)
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

	return hex.EncodeToString(iv) + ":" + hex.EncodeToString(authTag) + ":" + hex.EncodeToString(ciphertext), nil
}

// Decrypt déchiffre une chaîne chiffrée en AES-256-GCM ou l'ancien format AES-CBC
func Decrypt(encryptedText string) (string, bool, error) {
	key, err := GetSecretKey()
	if err != nil {
		return "", false, err
	}
	parts := strings.Split(strings.TrimSpace(encryptedText), ":")

	if len(parts) == 3 {
		// Format AES-256-GCM (iv:authTag:ciphertext)
		iv, err := hex.DecodeString(parts[0])
		if err != nil {
			return "", false, errors.New("IV hex invalide")
		}
		authTag, err := hex.DecodeString(parts[1])
		if err != nil {
			return "", false, errors.New("AuthTag hex invalide")
		}
		ciphertext, err := hex.DecodeString(parts[2])
		if err != nil {
			return "", false, errors.New("Ciphertext hex invalide")
		}

		block, err := aes.NewCipher(key)
		if err != nil {
			return "", false, err
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return "", false, err
		}

		// Reconstitution : ciphertext + tag
		ciphertextWithTag := append(ciphertext, authTag...)
		plaintext, err := gcm.Open(nil, iv, ciphertextWithTag, nil)
		if err != nil {
			return "", false, fmt.Errorf("échec de déchiffrement GCM (tag invalide ou clé incorrecte) : %w", err)
		}
		return string(plaintext), false, nil
	} else if len(parts) >= 2 {
		// Format legacy AES-256-CBC (iv:ciphertext)
		iv, err := hex.DecodeString(parts[0])
		if err != nil {
			return "", false, errors.New("IV CBC hex invalide")
		}
		ciphertext, err := hex.DecodeString(strings.Join(parts[1:], ":"))
		if err != nil {
			return "", false, errors.New("Ciphertext CBC hex invalide")
		}

		block, err := aes.NewCipher(key)
		if err != nil {
			return "", false, err
		}
		if len(ciphertext)%aes.BlockSize != 0 {
			return "", false, errors.New("taille de bloc CBC invalide")
		}

		mode := cipher.NewCBCDecrypter(block, iv)
		plaintext := make([]byte, len(ciphertext))
		mode.CryptBlocks(plaintext, ciphertext)

		// Unpadding PKCS#7
		unpadded, err := pkcs7Unpad(plaintext)
		if err != nil {
			return "", false, err
		}
		return string(unpadded), true, nil
	}

	return "", false, errors.New("format de token chiffré non reconnu")
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
		return errors.New("fichier de token vide")
	}

	// Si le fichier commence par '{', c'est du JSON en clair -> migrer vers chiffré
	if strings.HasPrefix(content, "{") {
		if err := json.Unmarshal([]byte(content), target); err != nil {
			return err
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
		return fmt.Errorf("json token invalide après déchiffrement : %w", err)
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
	tmpFile := filePath + ".tmp"
	if err := os.WriteFile(tmpFile, []byte(encrypted), 0600); err != nil {
		return err
	}
	return os.Rename(tmpFile, filePath)
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
