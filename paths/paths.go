package paths

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/config"
)

// GetHorizonDataDir renvoie le dossier où vivent les fichiers de données d'Horizon (bin)
func GetHorizonDataDir() string {
	if val := os.Getenv("HORIZON_DATA_DIR"); val != "" {
		return val
	}
	exe, err := os.Executable()
	if err == nil {
		return filepath.Dir(exe)
	}
	cwd, err := os.Getwd()
	if err == nil {
		return cwd
	}
	return "."
}

// GetAppDataPath renvoie le chemin AppData du système
func GetAppDataPath() string {
	if val := os.Getenv("APPDATA"); val != "" {
		return val
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(home, "AppData", "Roaming")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support")
	default:
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			return xdg
		}
		return filepath.Join(home, ".config")
	}
}

// GetInstancesFolder renvoie le chemin du dossier contenant les instances du Launcher
func GetInstancesFolder() string {
	if val := os.Getenv("GENS_INSTANCES_DIR"); val != "" {
		return val
	}
	return filepath.Join(GetAppDataPath(), "GensLauncher", "instances")
}

// ScanInstances liste les dossiers d'instances Minecraft valides (contenant instance.json)
func ScanInstances() ([]string, error) {
	instDir := GetInstancesFolder()
	if err := os.MkdirAll(instDir, 0755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(instDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	var instances []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.Contains(name, "_rollback_") {
			continue
		}
		jsonPath := filepath.Join(instDir, name, "instance.json")
		if _, err := os.Stat(jsonPath); err == nil {
			instances = append(instances, name)
		}
	}
	return instances, nil
}

// GetSettingsPath renvoie le chemin vers horizon_settings.json
func GetSettingsPath() string {
	return filepath.Join(GetHorizonDataDir(), "horizon_settings.json")
}

// LoadSettings charge ou initialise horizon_settings.json
func LoadSettings() config.Settings {
	settingsPath := GetSettingsPath()
	defaults := config.DefaultSettings()

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		// Écrit les paramètres par défaut
		if b, err := json.MarshalIndent(defaults, "", "  "); err == nil {
			_ = os.WriteFile(settingsPath, b, 0644)
		}
		return defaults
	}

	var loaded config.Settings
	if err := json.Unmarshal(data, &loaded); err != nil {
		return defaults
	}
	return loaded
}

// CleanupOrphanedTemps supprime les fichiers temporaires orphelins (temp_*.zip, *.tmp)
// laissés dans le dossier de données après une coupure ou un crash.
// Seuls les fichiers vieux de plus de 10 minutes sont supprimés pour ne pas impacter une opération concurrente.
func CleanupOrphanedTemps(dataDir string) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return
	}
	threshold := time.Now().Add(-10 * time.Minute)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		isTemp := strings.HasPrefix(name, "temp_") || strings.HasSuffix(name, ".tmp")
		if !isTemp {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(threshold) {
			_ = os.Remove(filepath.Join(dataDir, name))
		}
	}
}

// GetProviderName extrait le provider depuis les arguments CLI ou les settings
func GetProviderName(args []string, sets config.Settings) string {
	valid := map[string]bool{"google": true, "dropbox": true, "onedrive": true}
	for i, arg := range args {
		if strings.HasPrefix(arg, "--provider=") {
			val := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(arg, "--provider=")))
			if valid[val] {
				return val
			}
		}
		if arg == "--provider" && i+1 < len(args) {
			val := strings.ToLower(strings.TrimSpace(args[i+1]))
			if valid[val] {
				return val
			}
		}
		val := strings.ToLower(strings.TrimSpace(arg))
		if valid[val] {
			return val
		}
	}
	if sets.Provider != "" && valid[sets.Provider] {
		return sets.Provider
	}
	return "google"
}

// GetCanonicalName normalise le nom de l'instance pour les noms de fichiers
func GetCanonicalName(name string) string {
	trimmed := strings.TrimSpace(name)
	return trimmed
}

