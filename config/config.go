package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type ProviderConfig struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	AuthURI      string `json:"auth_uri"`
	TokenURI     string `json:"token_uri"`
	RedirectURI  string `json:"redirect_uri"`
	Scope        string `json:"scope,omitempty"`
}

type Settings struct {
	SystemEnabled         bool   `json:"systemEnabled"`
	SyncMode              string `json:"syncMode"`
	AutoSync              bool   `json:"autoSync"`
	AutoUpload            bool   `json:"autoUpload"`
	Provider              string `json:"provider"`
	MaxRetries            int    `json:"maxRetries"`
	RetryBaseDelay        int    `json:"retryBaseDelay"`
	DeltaCleanupThreshold int    `json:"deltaCleanupThreshold"`
	Language              string `json:"language,omitempty"`
}

func DefaultSettings() Settings {
	return Settings{
		SystemEnabled:         true,
		SyncMode:              "SMART",
		AutoSync:              true,
		AutoUpload:            true,
		Provider:              "google",
		MaxRetries:            3,
		RetryBaseDelay:        1500,
		DeltaCleanupThreshold: 10,
		Language:              "fr",
	}
}

// GetProviderConfig récupère la configuration d'un provider soit via les variables
// compilées (-ldflags), soit via config.json local, soit via les variables d'environnement.
func GetProviderConfig(providerName string, dataDir string) (ProviderConfig, error) {
	// 1. Valeurs compilées via -ldflags
	switch providerName {
	case "google":
		if GoogleClientID != "" {
			return ProviderConfig{
				ClientID:     GoogleClientID,
				ClientSecret: GoogleClientSecret,
				AuthURI:      "https://accounts.google.com/o/oauth2/auth",
				TokenURI:     "https://oauth2.googleapis.com/token",
				RedirectURI:  "http://127.0.0.1:12543",
			}, nil
		}
	case "dropbox":
		if DropboxClientID != "" {
			return ProviderConfig{
				ClientID:     DropboxClientID,
				ClientSecret: DropboxClientSecret,
				AuthURI:      "https://www.dropbox.com/oauth2/authorize",
				TokenURI:     "https://api.dropbox.com/oauth2/token",
				RedirectURI:  "http://127.0.0.1:12544",
			}, nil
		}
	case "onedrive":
		if OneDriveClientID != "" {
			return ProviderConfig{
				ClientID:     OneDriveClientID,
				ClientSecret: "",
				AuthURI:      "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
				TokenURI:     "https://login.microsoftonline.com/common/oauth2/v2.0/token",
				RedirectURI:  "http://127.0.0.1:12545",
				Scope:        "Files.ReadWrite offline_access User.Read",
			}, nil
		}
	}

	// 2. Fichier config.json local (développement)
	candidates := []string{
		filepath.Join(dataDir, "config.json"),
		"config.json",
	}
	for _, cand := range candidates {
		data, err := os.ReadFile(cand)
		if err == nil {
			var root struct {
				Credentials map[string]ProviderConfig `json:"credentials"`
			}
			if err := json.Unmarshal(data, &root); err == nil {
				if cfg, ok := root.Credentials[providerName]; ok && cfg.ClientID != "" && cfg.ClientID != "VOTRE_"+providerName+"_CLIENT_ID" {
					return cfg, nil
				}
			}
		}
	}

	// 3. Variables d'environnement
	switch providerName {
	case "google":
		if id := os.Getenv("HORIZON_GOOGLE_CLIENT_ID"); id != "" {
			return ProviderConfig{
				ClientID:     id,
				ClientSecret: os.Getenv("HORIZON_GOOGLE_CLIENT_SECRET"),
				AuthURI:      "https://accounts.google.com/o/oauth2/auth",
				TokenURI:     "https://oauth2.googleapis.com/token",
				RedirectURI:  "http://127.0.0.1:12543",
			}, nil
		}
	case "dropbox":
		if id := os.Getenv("HORIZON_DROPBOX_CLIENT_ID"); id != "" {
			return ProviderConfig{
				ClientID:     id,
				ClientSecret: os.Getenv("HORIZON_DROPBOX_CLIENT_SECRET"),
				AuthURI:      "https://www.dropbox.com/oauth2/authorize",
				TokenURI:     "https://api.dropbox.com/oauth2/token",
				RedirectURI:  "http://127.0.0.1:12544",
			}, nil
		}
	case "onedrive":
		if id := os.Getenv("HORIZON_ONEDRIVE_CLIENT_ID"); id != "" {
			return ProviderConfig{
				ClientID:     id,
				ClientSecret: "",
				AuthURI:      "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
				TokenURI:     "https://login.microsoftonline.com/common/oauth2/v2.0/token",
				RedirectURI:  "http://127.0.0.1:12545",
				Scope:        "Files.ReadWrite offline_access User.Read",
			}, nil
		}
	}

	return ProviderConfig{}, fmt.Errorf("configuration OAuth2 manquante pour le provider '%s'", providerName)
}

