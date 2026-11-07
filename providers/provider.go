package providers

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/auth"
	"github.com/WilliamBossard/Gens-Horizon/config"
	"github.com/WilliamBossard/Gens-Horizon/ipc"
	"github.com/WilliamBossard/Gens-Horizon/paths"
)

type CloudFile struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	ModifiedTime time.Time `json:"modifiedTime"`
	Size         int64     `json:"size"`
}

type Provider interface {
	Name() string
	ListFiles(nameContains string) ([]CloudFile, error)
	DownloadFile(fileID string, destPath string, onProgress func(pct int), totalSize int64) error
	DownloadJSON(fileID string, target any) error
	UploadZip(name string, srcPath string, existingID string, onProgress func(pct int)) (string, error)
	UploadJSON(name string, data any, existingID string) (string, error)
	DeleteFile(fileID string) error
	GetQuota() (usedBytes int64, totalBytes int64, err error)
}

func GetTokenPath(providerName string) string {
	fileName := fmt.Sprintf("token_%s.json", providerName)
	newPath := filepath.Join(paths.GetAuthDir(), fileName)
	legacy1 := filepath.Join(paths.GetHorizonDataDir(), fileName)
	legacy2 := filepath.Join(paths.GetLegacyBinDir(), fileName)
	if providerName == "google" {
		legacy3 := filepath.Join(paths.GetHorizonDataDir(), "token.json")
		legacy4 := filepath.Join(paths.GetLegacyBinDir(), "token.json")
		return paths.ResolveWithFallback(newPath, legacy1, legacy2, legacy3, legacy4)
	}
	return paths.ResolveWithFallback(newPath, legacy1, legacy2)
}

// GetProvider instancie le provider sélectionné avec son token déchiffré
func GetProvider(settings config.Settings) (Provider, error) {
	name := settings.Provider
	if name == "" {
		name = "google"
	}

	tokenPath := GetTokenPath(name)
	if _, err := os.Stat(tokenPath); os.IsNotExist(err) {
		return nil, nil // Non connecté
	}

	var tokenData auth.TokenData
	if err := auth.GetSecureToken(tokenPath, &tokenData); err != nil {
		ipc.Logf("[provider] Token illisible pour '%s' : %v", name, err)
		// Sauvegarder backup du token corrompu
		corruptPath := fmt.Sprintf("%s.corrupted_%d", tokenPath, time.Now().UnixMilli())
		_ = os.Rename(tokenPath, corruptPath)
		return nil, nil
	}

	cfg, err := config.GetProviderConfig(name, paths.GetHorizonDataDir())
	if err != nil {
		return nil, err
	}

	switch name {
	case "google":
		return NewGoogleProvider(tokenData, cfg, tokenPath), nil
	case "dropbox":
		return NewDropboxProvider(tokenData, cfg, tokenPath), nil
	case "onedrive":
		return NewOneDriveProvider(tokenData, cfg, tokenPath), nil
	default:
		return nil, fmt.Errorf("fournisseur non reconnu : '%s'", name)
	}
}

