package operations

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/ipc"
	"github.com/WilliamBossard/Gens-Horizon/paths"
	"github.com/WilliamBossard/Gens-Horizon/providers"
	"github.com/WilliamBossard/Gens-Horizon/retry"
)

const (
	PrefixBackup   = "GensHorizon_Backup_"
	PrefixDelta    = "GensHorizon_Delta_"
	PrefixManifest = "GensHorizon_Manifest_"
	PrefixMeta     = "GensHorizon_Meta_"
)

func Check(args []string) {
	if !retry.CheckConnectivity() {
		ipc.SendOffline("Internet indisponible.")
		return
	}

	settings := paths.LoadSettings()
	p, err := providers.GetProvider(settings)
	if err != nil || p == nil {
		ipc.Send(ipc.CheckResultMessage{
			Type:    "CHECK_RESULT",
			Status:  "NOT_LOGGED_IN",
			Updates: []string{},
		})
		return
	}

	retryOpts := retry.Options{
		MaxRetries: settings.MaxRetries,
		BaseDelay:  time.Duration(settings.RetryBaseDelay) * time.Millisecond,
		Label:      "[check]",
	}

	cloudIndex, err := providers.GetCloudIndexAndCleanDuplicates(p, retryOpts, "[check]")
	if err != nil {
		ipc.SendError("CHECK_FAILED", err.Error(), "")
		return
	}

	// Lecture de last_sync.json
	syncInfoPath := filepath.Join(paths.GetHorizonDataDir(), "last_sync.json")
	syncInfo := make(map[string]string)
	if data, err := os.ReadFile(syncInfoPath); err == nil {
		_ = json.Unmarshal(data, &syncInfo)
	}

	var cloudInstances []string
	seen := make(map[string]bool)
	for name := range cloudIndex {
		if strings.HasPrefix(name, PrefixBackup) && strings.HasSuffix(name, ".zip") {
			instName := strings.TrimSuffix(strings.TrimPrefix(name, PrefixBackup), ".zip")
			if !seen[instName] {
				seen[instName] = true
				cloudInstances = append(cloudInstances, instName)
			}
		}
	}

	updates := make([]string, 0)
	instDir := paths.GetInstancesFolder()

	for _, instName := range cloudInstances {
		baseName := PrefixBackup + instName + ".zip"
		baseFile := cloudIndex[baseName]
		cloudTime := baseFile.ModifiedTime.UnixMilli()

		var latestDeltaTime int64
		prefixDelta := PrefixDelta + instName + "_"
		for name := range cloudIndex {
			if strings.HasPrefix(name, prefixDelta) && strings.HasSuffix(name, ".zip") {
				rawTs := strings.TrimSuffix(strings.TrimPrefix(name, prefixDelta), ".zip")
				if ts, err := strconv.ParseInt(rawTs, 10, 64); err == nil {
					if ts > latestDeltaTime {
						latestDeltaTime = ts
					}
				}
			}
		}

		effectiveCloudTime := cloudTime
		if latestDeltaTime > effectiveCloudTime {
			effectiveCloudTime = latestDeltaTime
		}

		safeKey := paths.GetCanonicalName(instName)
		var lastSyncTime int64
		if rawTime, ok := syncInfo[safeKey]; ok {
			if t, err := time.Parse(time.RFC3339, rawTime); err == nil {
				lastSyncTime = t.UnixMilli()
			}
		}

		localPath := filepath.Join(instDir, safeKey)
		localExists := false
		if _, err := os.Stat(localPath); err == nil {
			localExists = true
		}

		if !localExists || effectiveCloudTime > lastSyncTime {
			updates = append(updates, instName)
		}
	}

	status := "UP_TO_DATE"
	if len(updates) > 0 {
		status = "UPDATE_AVAILABLE"
	}

	ipc.Send(ipc.CheckResultMessage{
		Type:    "CHECK_RESULT",
		Status:  status,
		Updates: updates,
	})
}

