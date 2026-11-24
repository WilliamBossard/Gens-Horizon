package operations

import (
	"context"
	"encoding/json"
	"os"
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
	CheckWithContext(context.Background(), args)
}

func CheckWithContext(ctx context.Context, args []string) {
	if ctx == nil || ctx.Err() != nil {
		if ctx != nil && ctx.Err() != nil {
			return
		}
	}
	if !retry.CheckConnectivity() {
		ipc.SendOffline("Internet indisponible.")
		return
	}

	settings := paths.LoadSettings()
	p, err := providers.GetProvider(settings)
	if err != nil {
		ipc.SendError("AUTH_KEYRING_ERROR", err.Error(), "")
		return
	}
	if p == nil {
		ipc.Send(ipc.CheckResultMessage{
			Type:    "CHECK_RESULT",
			Status:  "NOT_LOGGED_IN",
			Updates: []string{},
		})
		return
	}

	retryOpts := retry.FromSettings(settings.MaxRetries, settings.RetryBaseDelay, "[check]")

	providers.InvalidateCloudIndexCache()
	cloudIndex, err := providers.GetCloudIndexReadOnlyWithContext(ctx, p, retryOpts, "[check]")
	if err != nil {
		ipc.SendError("CHECK_FAILED", err.Error(), "")
		return
	}

	// Lecture de last_sync.json
	syncInfoPath := paths.GetLastSyncPath()
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
		for name, f := range cloudIndex {
			if _, ok := MatchDeltaForInstance(name, instName); ok {
				ts := f.ModifiedTime.UnixMilli()
				if ts > latestDeltaTime {
					latestDeltaTime = ts
				}
			}
		}

		effectiveCloudTime := cloudTime
		if latestDeltaTime > effectiveCloudTime {
			effectiveCloudTime = latestDeltaTime
		}

		safeKey, nameErr := paths.SanitizeInstanceName(paths.GetCanonicalName(instName))
		if nameErr != nil {
			continue
		}
		var lastSyncTime int64
		if rawTime, ok := syncInfo[safeKey]; ok {
			if t, err := time.Parse(time.RFC3339Nano, rawTime); err == nil {
				lastSyncTime = t.UnixMilli()
			}
		}

		localPath, pathErr := paths.SafeJoinUnder(instDir, safeKey)
		if pathErr != nil {
			continue
		}
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

