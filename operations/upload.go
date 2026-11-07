package operations

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/ipc"
	"github.com/WilliamBossard/Gens-Horizon/lock"
	"github.com/WilliamBossard/Gens-Horizon/paths"
	"github.com/WilliamBossard/Gens-Horizon/providers"
	"github.com/WilliamBossard/Gens-Horizon/retry"
	"github.com/WilliamBossard/Gens-Horizon/scanner"
	"github.com/WilliamBossard/Gens-Horizon/ziputils"
)

func parseTargetInstance(args []string) string {
	for i, arg := range args {
		if strings.HasPrefix(arg, "--instance=") {
			return strings.TrimPrefix(arg, "--instance=")
		}
		if arg == "--instance" && i+1 < len(args) {
			return args[i+1]
		}
	}
	// Recherche d'un argument positionnel qui n'est pas un flag
	for _, arg := range args {
		if !strings.HasPrefix(arg, "--") && arg != "upload" && arg != "sync" && arg != "check" && arg != "login" && arg != "quota" && arg != "rollback" {
			return arg
		}
	}
	return ""
}

func Upload(args []string) {
	if !lock.AcquireLock() {
		ipc.SendError("ERR_ALREADY_RUNNING", "ERR_ALREADY_RUNNING", "")
		os.Exit(1)
	}
	defer lock.ReleaseLock()

	if !retry.CheckConnectivity() {
		ipc.SendOffline("Internet indisponible.")
		return
	}

	settings := paths.LoadSettings()
	p, err := providers.GetProvider(settings)
	if err != nil || p == nil {
		ipc.SendError("AUTH_EXPIRED", "Session expirée. Veuillez lier à nouveau votre compte depuis les paramètres.", "")
		return
	}

	retryOpts := retry.Options{
		MaxRetries: settings.MaxRetries,
		BaseDelay:  time.Duration(settings.RetryBaseDelay) * time.Millisecond,
		Label:      "[upload]",
	}

	targetInst := parseTargetInstance(args)
	var instances []string
	if targetInst != "" {
		instances = []string{targetInst}
	} else {
		scanned, err := paths.ScanInstances()
		if err != nil {
			ipc.SendError("SCAN_ERROR", err.Error(), "")
			return
		}
		instances = scanned
	}

	if len(instances) == 0 {
		ipc.SendInfo("Aucune instance locale à envoyer vers le Cloud.")
		return
	}

	cloudIndex, err := providers.GetCloudIndexAndCleanDuplicates(p, retryOpts, "[upload]")
	if err != nil {
		ipc.SendError("CLOUD_ERROR", err.Error(), "")
		return
	}

	dataDir := paths.GetHorizonDataDir()
	instDir := paths.GetInstancesFolder()

	for _, inst := range instances {
		safeInst := paths.GetCanonicalName(inst)
		localInstFolder := filepath.Join(instDir, safeInst)
		if _, err := os.Stat(localInstFolder); os.IsNotExist(err) {
			localInstFolder = filepath.Join(instDir, inst)
			if _, err := os.Stat(localInstFolder); os.IsNotExist(err) {
				ipc.Logf("[upload] Dossier d'instance introuvable : %s", inst)
				continue
			}
		}

		ipc.SendProgress("SCANNING", 0, inst)

		// 1. Lecture du manifest local existant
		localManifestFile := paths.GetManifestPath(safeInst)
		var oldLocalManifest scanner.Manifest
		if data, err := os.ReadFile(localManifestFile); err == nil {
			_ = json.Unmarshal(data, &oldLocalManifest)
		}

		// 2. Scan et génération du nouveau manifest
		newManifest, err := scanner.GenerateManifest(localInstFolder, oldLocalManifest)
		if err != nil {
			ipc.SendError("SCAN_FAILED", err.Error(), inst)
			continue
		}

		ipc.SendProgress("SCANNING", 100, inst)

		baseBackupName := fmt.Sprintf("%s%s.zip", PrefixBackup, safeInst)
		manifestName := fmt.Sprintf("%s%s.json", PrefixManifest, safeInst)
		metaName := fmt.Sprintf("%s%s.json", PrefixMeta, safeInst)

		baseFile, hasBase := cloudIndex[baseBackupName]
		manifestFile, hasManifest := cloudIndex[manifestName]

		// Comptage des deltas existants
		prefixDelta := fmt.Sprintf("%s%s_", PrefixDelta, safeInst)
		var existingDeltas []providers.CloudFile
		for name, f := range cloudIndex {
			if strings.HasPrefix(name, prefixDelta) && strings.HasSuffix(name, ".zip") {
				existingDeltas = append(existingDeltas, f)
			}
		}

		cleanupThreshold := settings.DeltaCleanupThreshold
		if cleanupThreshold <= 0 {
			cleanupThreshold = 10
		}

		var remoteManifest scanner.Manifest
		hasRemoteManifest := false
		if hasManifest {
			err = p.DownloadJSON(manifestFile.ID, &remoteManifest)
			if err == nil {
				hasRemoteManifest = true
			} else {
				ipc.Logf("[upload] Erreur téléchargement remote manifest, fallback full : %v", err)
			}
		}

		needsFullBackup := !hasBase || !hasRemoteManifest || len(existingDeltas) >= cleanupThreshold

		if needsFullBackup {
			ipc.Logf("[upload] Création d'une sauvegarde complète pour %s (deltas=%d, max=%d)", inst, len(existingDeltas), cleanupThreshold)
			tempZip := filepath.Join(dataDir, fmt.Sprintf("temp_full_%s_%d.zip", safeInst, time.Now().UnixMilli()))

			ipc.SendProgress("COMPRESSING", 0, inst)
			err = ziputils.CreateFullZip(localInstFolder, tempZip, func(pct int) {
				ipc.SendProgress("COMPRESSING", pct, inst)
			})
			if err != nil {
				_ = os.Remove(tempZip)
				ipc.SendError("COMPRESS_FAILED", err.Error(), inst)
				continue
			}

			ipc.SendProgress("UPLOADING", 0, inst)
			existingID := ""
			if hasBase {
				existingID = baseFile.ID
			}

			newBaseID, err := p.UploadZip(baseBackupName, tempZip, existingID, func(pct int) {
				ipc.SendProgress("UPLOADING", pct, inst)
			})
			_ = os.Remove(tempZip)
			if err != nil {
				ipc.SendError("UPLOAD_FAILED", err.Error(), inst)
				continue
			}
			cloudIndex[baseBackupName] = providers.CloudFile{
				ID:           newBaseID,
				Name:         baseBackupName,
				ModifiedTime: time.Now(),
			}

			// Upload Manifest
			mExistingID := ""
			if hasManifest {
				mExistingID = manifestFile.ID
			}
			newMID, err := p.UploadJSON(manifestName, newManifest, mExistingID)
			if err == nil {
				cloudIndex[manifestName] = providers.CloudFile{
					ID:           newMID,
					Name:         manifestName,
					ModifiedTime: time.Now(),
				}
			}

			// Upload Metadata
			metaData := extractInstanceMetadata(localInstFolder, inst)
			metaExistingID := ""
			if mf, ok := cloudIndex[metaName]; ok {
				metaExistingID = mf.ID
			}
			_, _ = p.UploadJSON(metaName, metaData, metaExistingID)

			// Nettoyage des anciens deltas consolidés
			for _, deltaFile := range existingDeltas {
				_ = p.DeleteFile(deltaFile.ID)
				delete(cloudIndex, deltaFile.Name)
			}

			// Sauvegarde du manifest en local
			if b, err := json.MarshalIndent(newManifest, "", "  "); err == nil {
				_ = osWriteFileAtomic(localManifestFile, b, 0644)
			}

			updateLastSync(safeInst)

			ipc.Send(map[string]any{
				"type":     "SUCCESS",
				"instance": inst,
				"mode":     "FULL",
			})
		} else {
			// Sauvegarde différentielle (Delta Sync)
			diff := scanner.CompareManifests(remoteManifest, newManifest)
			if !diff.HasChanges {
				ipc.Send(ipc.BaseMessage{
					Type:      "SUCCESS",
					Instance:  inst,
					Message:   "Instance is already up to date on Cloud.",
					ErrorCode: "UP_TO_DATE_CLOUD",
				})
				updateLastSync(safeInst)
				continue
			}

			ipc.Logf("[upload] Delta sync pour %s : +%d, ~%d, -%d fichiers", inst, len(diff.Added), len(diff.Modified), len(diff.Deleted))

			filesToZip := append(diff.Added, diff.Modified...)
			tempDelta := filepath.Join(dataDir, fmt.Sprintf("temp_delta_%s_%d.zip", safeInst, time.Now().UnixMilli()))

			ipc.SendProgress("COMPRESSING", 0, inst)
			err = ziputils.CreateDeltaZip(localInstFolder, filesToZip, diff.Deleted, tempDelta, func(pct int) {
				ipc.SendProgress("COMPRESSING", pct, inst)
			})
			if err != nil {
				_ = os.Remove(tempDelta)
				ipc.SendError("COMPRESS_FAILED", err.Error(), inst)
				continue
			}

			deltaName := fmt.Sprintf("%s%s_%d.zip", PrefixDelta, safeInst, time.Now().UnixMilli())
			ipc.SendProgress("UPLOADING", 0, inst)
			deltaID, err := p.UploadZip(deltaName, tempDelta, "", func(pct int) {
				ipc.SendProgress("UPLOADING", pct, inst)
			})
			_ = os.Remove(tempDelta)
			if err != nil {
				ipc.SendError("UPLOAD_FAILED", err.Error(), inst)
				continue
			}
			cloudIndex[deltaName] = providers.CloudFile{
				ID:           deltaID,
				Name:         deltaName,
				ModifiedTime: time.Now(),
			}

			// Mise à jour du manifest
			newMID, err := p.UploadJSON(manifestName, newManifest, manifestFile.ID)
			if err == nil {
				cloudIndex[manifestName] = providers.CloudFile{
					ID:           newMID,
					Name:         manifestName,
					ModifiedTime: time.Now(),
				}
			}

			// Mise à jour metadata
			metaData := extractInstanceMetadata(localInstFolder, inst)
			metaExistingID := ""
			if mf, ok := cloudIndex[metaName]; ok {
				metaExistingID = mf.ID
			}
			_, _ = p.UploadJSON(metaName, metaData, metaExistingID)

			// Sauvegarde du manifest en local
			if b, err := json.MarshalIndent(newManifest, "", "  "); err == nil {
				_ = osWriteFileAtomic(localManifestFile, b, 0644)
			}

			updateLastSync(safeInst)

			summary := fmt.Sprintf("+%d ajouté(s), ~%d modifié(s), -%d supprimé(s)", len(diff.Added), len(diff.Modified), len(diff.Deleted))
			ipc.Send(map[string]any{
				"type":     "SUCCESS",
				"instance": inst,
				"mode":     "SMART",
				"summary":  summary,
			})
		}
	}
}

func updateLastSync(safeInst string) {
	syncInfoPath := paths.GetLastSyncPath()
	syncInfo := make(map[string]string)
	if data, err := os.ReadFile(syncInfoPath); err == nil {
		_ = json.Unmarshal(data, &syncInfo)
	}
	syncInfo[safeInst] = time.Now().Format(time.RFC3339)
	if b, err := json.MarshalIndent(syncInfo, "", "  "); err == nil {
		_ = osWriteFileAtomic(syncInfoPath, b, 0644)
	}
}

func extractInstanceMetadata(instFolder string, instName string) map[string]any {
	meta := map[string]any{
		"realName": instName,
		"loader":   nil,
		"iconData": nil,
	}

	instJsonPath := filepath.Join(instFolder, "instance.json")
	if data, err := os.ReadFile(instJsonPath); err == nil {
		var instData struct {
			Name     string `json:"name"`
			Loader   string `json:"loader"`
			IconData string `json:"iconData"`
		}
		if err := json.Unmarshal(data, &instData); err == nil {
			if instData.Name != "" {
				meta["realName"] = instData.Name
			}
			if instData.Loader != "" {
				meta["loader"] = instData.Loader
			}
			if instData.IconData != "" {
				meta["iconData"] = instData.IconData
			}
		}
	}
	return meta
}
