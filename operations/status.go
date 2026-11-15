package operations

import (
	"os"

	"github.com/WilliamBossard/Gens-Horizon/config"
	"github.com/WilliamBossard/Gens-Horizon/ipc"
	"github.com/WilliamBossard/Gens-Horizon/lock"
	"github.com/WilliamBossard/Gens-Horizon/paths"
	"github.com/WilliamBossard/Gens-Horizon/providers"
)

// Status reports the local configuration, authentication presence, and lock state without network calls.
func Status(_ []string) {
	settings := paths.LoadSettings()
	locked, stale, lockPid := lock.InspectLock()
	tokenPath := providers.GetTokenPath(settings.Provider)
	_, tokErr := os.Stat(tokenPath)
	loggedIn := tokErr == nil

	message := "Ready"
	if !loggedIn {
		message = "Not authenticated for active provider"
	} else if locked && !stale {
		message = "Operation currently in progress"
	}

	ipc.Send(map[string]any{
		"type":       "STATUS",
		"appVersion": config.AppVersion,
		"provider":   settings.Provider,
		"loggedIn":   loggedIn,
		"locked":     locked,
		"staleLock":  stale,
		"lockPid":    lockPid,
		"syncMode":   settings.SyncMode,
		"autoSync":   settings.AutoSync,
		"autoUpload": settings.AutoUpload,
		"dataDir":    paths.GetHorizonDataDir(),
		"message":    message,
	})
}
