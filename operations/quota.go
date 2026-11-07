package operations

import (
	"sort"
	"strings"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/ipc"
	"github.com/WilliamBossard/Gens-Horizon/paths"
	"github.com/WilliamBossard/Gens-Horizon/providers"
	"github.com/WilliamBossard/Gens-Horizon/retry"
)

func Quota(args []string) {
	if !retry.CheckConnectivity() {
		ipc.SendOffline("Internet indisponible.")
		return
	}

	settings := paths.LoadSettings()
	p, err := providers.GetProvider(settings)
	if err != nil || p == nil {
		ipc.SendError("AUTH_EXPIRED", "Session expirÃ©e. Veuillez lier Ã  nouveau votre compte depuis les paramÃ¨tres.", "")
		return
	}

	retryOpts := retry.Options{
		MaxRetries: settings.MaxRetries,
		BaseDelay:  time.Duration(settings.RetryBaseDelay) * time.Millisecond,
		Label:      "[quota]",
	}

	var usedBytes, totalBytes int64
	err = retry.Do(nil, retryOpts, func() error {
		var qErr error
		usedBytes, totalBytes, qErr = p.GetQuota()
		return qErr
	})
	if err != nil {
		ipc.SendError("QUOTA_ERROR", err.Error(), "")
		return
	}

	var cloudFiles []providers.CloudFile
	_ = retry.Do(nil, retryOpts, func() error {
		var lErr error
		cloudFiles, lErr = p.ListFiles("GensHorizon_")
		return lErr
	})

	var horizonUsed int64
	instanceMap := make(map[string]int64)
	deltaCountMap := make(map[string]int)

	for _, f := range cloudFiles {
		horizonUsed += f.Size
		var instName string
		isDelta := false

		if strings.HasPrefix(f.Name, PrefixBackup) {
			instName = strings.TrimSuffix(strings.TrimPrefix(f.Name, PrefixBackup), ".zip")
		} else if strings.HasPrefix(f.Name, PrefixDelta) {
			body := strings.TrimSuffix(strings.TrimPrefix(f.Name, PrefixDelta), ".zip")
			parts := strings.Split(body, "_")
			if len(parts) > 1 {
				instName = strings.Join(parts[:len(parts)-1], "_")
				isDelta = true
			}
		} else if strings.HasPrefix(f.Name, PrefixManifest) {
			instName = strings.TrimSuffix(strings.TrimPrefix(f.Name, PrefixManifest), ".json")
		} else if strings.HasPrefix(f.Name, PrefixMeta) {
			instName = strings.TrimSuffix(strings.TrimPrefix(f.Name, PrefixMeta), ".json")
		}

		if instName != "" {
			instanceMap[instName] += f.Size
			if isDelta {
				deltaCountMap[instName]++
			}
		}
	}

	var instances []ipc.QuotaInstance
	for name, bytes := range instanceMap {
		instances = append(instances, ipc.QuotaInstance{
			Name:       name,
			Bytes:      bytes,
			DeltaCount: deltaCountMap[name],
		})
	}

	sort.Slice(instances, func(i, j int) bool {
		return instances[i].Bytes > instances[j].Bytes
	})

	ipc.Send(ipc.QuotaMessage{
		Type:        "QUOTA",
		UsedBytes:   usedBytes,
		TotalBytes:  totalBytes,
		HorizonUsed: horizonUsed,
		Instances:   instances,
	})
}

