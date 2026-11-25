package paths

import (
	"encoding/json"
	"fmt"
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
	if runtime.GOOS == "windows" {
		if val := os.Getenv("APPDATA"); val != "" {
			return val
		}
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

// GetLegacyBinDir renvoie l'ancien dossier 'bin' à côté de 'Horizon' s'il existe
func GetLegacyBinDir() string {
	dataDir := GetHorizonDataDir()
	if strings.EqualFold(filepath.Base(dataDir), "Horizon") {
		return filepath.Join(filepath.Dir(dataDir), "bin")
	}
	return dataDir
}

// GetAuthDir renvoie le sous-dossier auth/ (stockage sécurisé des tokens et clés)
func GetAuthDir() string {
	dir := filepath.Join(GetHorizonDataDir(), "auth")
	_ = os.MkdirAll(dir, 0700)
	return dir
}

// GetMetadataDir renvoie le sous-dossier metadata/ (métadonnées des instances)
func GetMetadataDir() string {
	dir := filepath.Join(GetHorizonDataDir(), "metadata")
	_ = os.MkdirAll(dir, 0755)
	return dir
}

// GetManifestsDir renvoie le sous-dossier manifests/ (signatures locales)
func GetManifestsDir() string {
	dir := filepath.Join(GetHorizonDataDir(), "manifests")
	_ = os.MkdirAll(dir, 0755)
	return dir
}

// GetCacheDir renvoie le sous-dossier cache/ (caches UI)
func GetCacheDir() string {
	dir := filepath.Join(GetHorizonDataDir(), "cache")
	_ = os.MkdirAll(dir, 0755)
	return dir
}

// ResolveWithFallback vérifie si newPath existe. Si non, teste les legacyPaths et migre automatiquement
// en préservant les permissions d'origine du fichier source.
func ResolveWithFallback(newPath string, legacyPaths ...string) string {
	if _, err := os.Stat(newPath); err == nil {
		return newPath
	}
	for _, lp := range legacyPaths {
		if lp == "" || lp == newPath {
			continue
		}
		info, statErr := os.Lstat(lp)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || info.IsDir() {
			continue
		}
		if data, err := os.ReadFile(lp); err == nil {
			_ = os.MkdirAll(filepath.Dir(newPath), 0755)
			perm := info.Mode().Perm()
			if perm == 0 {
				perm = 0600
			}
			tmp := fmt.Sprintf("%s.tmp.%d", newPath, time.Now().UnixNano())
			if err := os.WriteFile(tmp, data, perm); err == nil {
				if err := os.Rename(tmp, newPath); err == nil {
					return newPath
				}
				_ = os.Remove(tmp)
			}
		}
	}
	return newPath
}

func isWindowsReservedName(segment string) bool {
	s := strings.TrimRight(strings.TrimSpace(segment), ". ")
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		s = s[:dot]
	}
	switch strings.ToUpper(s) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	}
	return false
}

// SanitizeInstanceName valide qu'un nom d'instance est un segment unique sûr (pas de traversal ni de séparateurs).
func SanitizeInstanceName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || trimmed == "." || trimmed == ".." {
		return "", fmt.Errorf("nom d'instance invalide")
	}
	if strings.ContainsAny(trimmed, "/\\:\x00") || strings.Contains(trimmed, "..") {
		return "", fmt.Errorf("nom d'instance contient des caractères interdits : %q", name)
	}
	if isWindowsReservedName(trimmed) {
		return "", fmt.Errorf("nom d'instance réservé Windows interdit : %q", name)
	}
	return trimmed, nil
}

// SafeJoinUnder joint baseDir et relPath en garantissant que le résultat reste strictement sous baseDir.
func SafeJoinUnder(baseDir, relPath string) (string, error) {
	if relPath == "" {
		return "", fmt.Errorf("chemin relatif vide")
	}
	if strings.ContainsAny(relPath, ":\x00") {
		return "", fmt.Errorf("caractère interdit dans le chemin : %q", relPath)
	}
	for _, seg := range strings.Split(filepath.ToSlash(relPath), "/") {
		if seg == "" || seg == "." {
			continue
		}
		if seg == ".." || isWindowsReservedName(seg) {
			return "", fmt.Errorf("segment de chemin interdit : %q", relPath)
		}
	}
	absBase, err := filepath.Abs(filepath.Clean(baseDir))
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("chemin absolu interdit : %q", relPath)
	}
	absTarget, err := filepath.Abs(filepath.Join(absBase, filepath.FromSlash(relPath)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absBase, absTarget)
	if err != nil {
		return "", err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("évasion de répertoire détectée : %q", relPath)
	}
	return absTarget, nil
}

// GetSettingsPath renvoie le chemin vers horizon_settings.json avec rétrocompatibilité
func GetSettingsPath() string {
	newPath := filepath.Join(GetHorizonDataDir(), "horizon_settings.json")
	legacy := filepath.Join(GetLegacyBinDir(), "horizon_settings.json")
	return ResolveWithFallback(newPath, legacy)
}

// GetLastSyncPath renvoie le chemin vers last_sync.json avec rétrocompatibilité
func GetLastSyncPath() string {
	newPath := filepath.Join(GetHorizonDataDir(), "last_sync.json")
	legacy := filepath.Join(GetLegacyBinDir(), "last_sync.json")
	return ResolveWithFallback(newPath, legacy)
}

// GetMetaPath renvoie le chemin vers meta_<inst>.json avec rétrocompatibilité
func GetMetaPath(safeInst string) string {
	fileName := fmt.Sprintf("meta_%s.json", safeInst)
	newPath := filepath.Join(GetMetadataDir(), fileName)
	legacy1 := filepath.Join(GetHorizonDataDir(), fileName)
	legacy2 := filepath.Join(GetLegacyBinDir(), fileName)
	return ResolveWithFallback(newPath, legacy1, legacy2)
}

// GetManifestPath renvoie le chemin vers manifest_<inst>.json avec rétrocompatibilité
func GetManifestPath(safeInst string) string {
	fileName := fmt.Sprintf("manifest_%s.json", safeInst)
	newPath := filepath.Join(GetManifestsDir(), fileName)
	legacy1 := filepath.Join(GetHorizonDataDir(), fileName)
	legacy2 := filepath.Join(GetLegacyBinDir(), fileName)
	return ResolveWithFallback(newPath, legacy1, legacy2)
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

	loaded := config.DefaultSettings()
	if err := json.Unmarshal(data, &loaded); err != nil {
		return defaults
	}
	return loaded
}

// CleanupOrphanedTemps supprime les fichiers temporaires orphelins (horizon_*.zip, temp_*.zip, *.tmp)
// dans tous les sous-dossiers Horizon, dans os.TempDir() et dans l'ancien bin pour ne rien laisser traîner.
func CleanupOrphanedTemps(dataDir string) {
	dirsToClean := []string{
		dataDir,
		os.TempDir(),
		GetAuthDir(),
		GetMetadataDir(),
		GetManifestsDir(),
		GetCacheDir(),
		GetLegacyBinDir(),
	}
	threshold := time.Now().Add(-10 * time.Minute)
	seen := make(map[string]bool)

	for _, d := range dirsToClean {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			lower := strings.ToLower(name)
			if entry.IsDir() {
				if d == os.TempDir() && (strings.HasPrefix(lower, "horizon-sync-") || strings.HasPrefix(lower, "horizon-up-") || strings.HasPrefix(lower, "horizon-delta-")) {
					if info, err := entry.Info(); err == nil && info.ModTime().Before(threshold) {
						_ = os.RemoveAll(filepath.Join(d, name))
					}
				}
				continue
			}
			isHorizonTemp := strings.HasPrefix(lower, "horizon_") && strings.HasSuffix(lower, ".zip")
			isOldTemp := strings.HasPrefix(lower, "temp_") || strings.HasSuffix(lower, ".tmp") || strings.Contains(lower, ".tmp.") || strings.Contains(lower, ".stale.")

			if d == os.TempDir() {
				if !isHorizonTemp {
					continue
				}
			} else {
				if !isOldTemp && !isHorizonTemp {
					continue
				}
			}

			info, err := entry.Info()
			if err != nil {
				continue
			}
			if info.ModTime().Before(threshold) {
				_ = os.Remove(filepath.Join(d, name))
			}
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
