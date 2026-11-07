package operations

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/ipc"
	"github.com/WilliamBossard/Gens-Horizon/lock"
	"github.com/WilliamBossard/Gens-Horizon/paths"
	"github.com/WilliamBossard/Gens-Horizon/providers"
	"github.com/WilliamBossard/Gens-Horizon/retry"
	"github.com/WilliamBossard/Gens-Horizon/scanner"
	"github.com/WilliamBossard/Gens-Horizon/ziputils"
)

func Sync(args []string) {
	isList := hasFlag(args, "--list")
	isDelete := hasFlag(args, "--delete")

	if !isList {
		if !lock.AcquireLock() {
			ipc.SendError("ERR_ALREADY_RUNNING", "ERR_ALREADY_RUNNING", "")
			os.Exit(1)
		}
		defer lock.ReleaseLock()
	}

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
		Label:      "[sync]",
	}

	cloudIndex, err := providers.GetCloudIndexAndCleanDuplicates(p, retryOpts, "[sync]")
	if err != nil {
		ipc.SendError("CLOUD_ERROR", err.Error(), "")
		return
	}

	dataDir := paths.GetHorizonDataDir()
	instDir := paths.GetInstancesFolder()

	// 1. Traitement de --list
	if isList {
		handleCloudList(cloudIndex, p)
		return
	}

	targetInst := parseTargetInstance(args)

	// 2. Traitement de --delete
	if isDelete {
		if targetInst == "" {
			ipc.SendError("ARG_MISSING", "Veuillez spécifier l'instance à supprimer du cloud.", "")
			return
		}
		handleDelete(targetInst, cloudIndex, p, dataDir)
		return
	}

	// 3. Synchronisation Cloud vers Local
	syncInfoPath := paths.GetLastSyncPath()
	syncInfo := make(map[string]string)
	if data, err := os.ReadFile(syncInfoPath); err == nil {
		_ = json.Unmarshal(data, &syncInfo)
	}

	var instancesToSync []string
	if targetInst != "" {
		instancesToSync = []string{targetInst}
	} else {
		seen := make(map[string]bool)
		for name := range cloudIndex {
			if strings.HasPrefix(name, PrefixBackup) && strings.HasSuffix(name, ".zip") {
				instName := strings.TrimSuffix(strings.TrimPrefix(name, PrefixBackup), ".zip")
				if !seen[instName] {
					seen[instName] = true
					instancesToSync = append(instancesToSync, instName)
				}
			}
		}
	}

	for _, inst := range instancesToSync {
		safeInst := paths.GetCanonicalName(inst)
		baseBackupName := fmt.Sprintf("%s%s.zip", PrefixBackup, safeInst)
		baseFile, hasBase := cloudIndex[baseBackupName]
		if !hasBase {
			ipc.SendError("NOT_FOUND", fmt.Sprintf("Aucune sauvegarde cloud trouvée pour l'instance '%s'.", inst), inst)
			continue
		}

		localPath := filepath.Join(instDir, safeInst)
		isNewInstance := false
		if _, err := os.Stat(localPath); os.IsNotExist(err) {
			isNewInstance = true
			_ = os.MkdirAll(localPath, 0755)
		}

		// Lecture du timestamp de dernière synchro locale
		var lastSyncTime int64
		if rawTime, ok := syncInfo[safeInst]; ok {
			if t, err := time.Parse(time.RFC3339, rawTime); err == nil {
				lastSyncTime = t.UnixMilli()
			}
		}

		baseCloudTime := baseFile.ModifiedTime.UnixMilli()
		needsBaseExtract := isNewInstance || baseCloudTime > lastSyncTime

		// Application chronologique des Deltas plus récents que le base/sync
		prefixDelta := fmt.Sprintf("%s%s_", PrefixDelta, safeInst)
		type deltaEntry struct {
			file      providers.CloudFile
			timestamp int64
		}
		var deltas []deltaEntry

		for name, f := range cloudIndex {
			if strings.HasPrefix(name, prefixDelta) && strings.HasSuffix(name, ".zip") {
				rawTs := strings.TrimSuffix(strings.TrimPrefix(name, prefixDelta), ".zip")
				if ts, err := strconv.ParseInt(rawTs, 10, 64); err == nil {
					if ts > lastSyncTime || needsBaseExtract {
						deltas = append(deltas, deltaEntry{file: f, timestamp: ts})
					}
				}
			}
		}

		// Optimisation performance : Si aucun changement, terminer immédiatement sans copie disque
		if !needsBaseExtract && len(deltas) == 0 {
			ipc.Send(ipc.BaseMessage{
				Type:      "SUCCESS",
				Instance:  inst,
				Message:   "Instance is already up to date.",
				ErrorCode: "UP_TO_DATE",
			})
			continue
		}

		// Création d'un rollback local UNIQUEMENT si des modifications vont être appliquées
		var rollbackPath string
		if !isNewInstance {
			rollbackPath = createRollbackSnapshot(localPath)
		}

		if needsBaseExtract {
			tempBase := filepath.Join(dataDir, fmt.Sprintf("temp_base_%s_%d.zip", safeInst, time.Now().UnixMilli()))
			ipc.SendProgress("DOWNLOADING", 0, inst)

			err = p.DownloadFile(baseFile.ID, tempBase, func(pct int) {
				ipc.SendProgress("DOWNLOADING", pct, inst)
			}, baseFile.Size)
			if err != nil {
				_ = os.Remove(tempBase)
				ipc.SendError("DOWNLOAD_FAILED", err.Error(), inst)
				continue
			}

			if err := ziputils.VerifyZipIntegrity(tempBase); err != nil {
				_ = os.Remove(tempBase)
				ipc.SendError("CORRUPT_ARCHIVE", err.Error(), inst)
				continue
			}

			ipc.SendProgress("EXTRACTING", 0, inst)
			err = ziputils.ExtractZip(tempBase, localPath, func(pct int) {
				ipc.SendProgress("EXTRACTING", pct, inst)
			})
			_ = os.Remove(tempBase)
			if err != nil {
				ipc.SendError("EXTRACT_FAILED", err.Error(), inst)
				continue
			}
		}

		// Tri chronologique croissant
		sort.Slice(deltas, func(i, j int) bool {
			return deltas[i].timestamp < deltas[j].timestamp
		})

		for _, d := range deltas {
			ipc.Logf("[sync] Application du delta cloud : %s (taille: %d octets)", d.file.Name, d.file.Size)
			tempDelta := filepath.Join(dataDir, fmt.Sprintf("temp_delta_%s_%d.zip", safeInst, d.timestamp))

			err = p.DownloadFile(d.file.ID, tempDelta, nil, d.file.Size)
			if err != nil {
				_ = os.Remove(tempDelta)
				ipc.Logf("[sync] Échec téléchargement delta %s : %v", d.file.Name, err)
				continue
			}

			if err := ziputils.VerifyZipIntegrity(tempDelta); err == nil {
				_ = ziputils.ApplyDelta(tempDelta, localPath, func(pct int) {
					ipc.SendDeltaProgress("APPLYING_DELTA", pct, inst, d.file.Name)
				})
			}
			_ = os.Remove(tempDelta)
		}

		// Téléchargement du manifest distant pour synchroniser la référence locale
		manifestName := fmt.Sprintf("%s%s.json", PrefixManifest, safeInst)
		if mf, ok := cloudIndex[manifestName]; ok {
			var remoteM scanner.Manifest
			if err := p.DownloadJSON(mf.ID, &remoteM); err == nil {
				localManifestFile := paths.GetManifestPath(safeInst)
				if b, err := json.MarshalIndent(remoteM, "", "  "); err == nil {
					_ = osWriteFileAtomic(localManifestFile, b, 0644)
				}
			}
		}

		updateLastSync(safeInst)

		if rollbackPath != "" {
			cleanupRollback(rollbackPath)
		}

		syncMsg := fmt.Sprintf("%d delta(s) applied.", len(deltas))
		if needsBaseExtract {
			syncMsg = "Base + delta(s) applied."
		}

		ipc.Send(ipc.BaseMessage{
			Type:      "SUCCESS",
			Instance:  inst,
			Message:   syncMsg,
			ErrorCode: "SYNC_SUCCESS",
		})
	}
}

func handleCloudList(cloudIndex map[string]providers.CloudFile, p providers.Provider) {
	var list []string
	seen := make(map[string]bool)
	for name := range cloudIndex {
		if strings.HasPrefix(name, PrefixBackup) && strings.HasSuffix(name, ".zip") {
			instName := strings.TrimSuffix(strings.TrimPrefix(name, PrefixBackup), ".zip")
			if !seen[instName] {
				seen[instName] = true
				list = append(list, instName)
			}
		}
	}

	type metaResult struct {
		RealName string  `json:"realName"`
		IconData *string `json:"iconData"`
		Loader   *string `json:"loader"`
	}

	// Récupération concurrente des métadonnées avec mise en cache locale sur disque (comme la version Node.js)
	metaCache := make(map[string]metaResult)
	var metaMu sync.Mutex
	var metaWg sync.WaitGroup
	metaSem := make(chan struct{}, 6)

	for _, instName := range list {
		metaName := PrefixMeta + instName + ".json"
		mf, ok := cloudIndex[metaName]
		if !ok {
			continue
		}

		localMetaPath := paths.GetMetaPath(instName)
		cloudTime := mf.ModifiedTime

		needsDownload := true
		if stat, err := os.Stat(localMetaPath); err == nil {
			if stat.ModTime().UnixMilli() >= cloudTime.UnixMilli() {
				if data, err := os.ReadFile(localMetaPath); err == nil {
					var meta metaResult
					if err := json.Unmarshal(data, &meta); err == nil {
						metaMu.Lock()
						metaCache[instName] = meta
						metaMu.Unlock()
						needsDownload = false
					}
				}
			}
		}

		if needsDownload {
			metaWg.Add(1)
			metaSem <- struct{}{}
			go func(instName string, fID string, localPath string, cloudModTime time.Time) {
				defer metaWg.Done()
				defer func() { <-metaSem }()

				var meta metaResult
				if err := p.DownloadJSON(fID, &meta); err == nil {
					metaMu.Lock()
					metaCache[instName] = meta
					metaMu.Unlock()

					if b, err := json.MarshalIndent(meta, "", "  "); err == nil {
						_ = osWriteFileAtomic(localPath, b, 0644)
						_ = os.Chtimes(localPath, cloudModTime, cloudModTime)
					}
				}
			}(instName, mf.ID, localMetaPath, cloudTime)
		}
	}
	metaWg.Wait()

	var richList []ipc.RichInstanceInfo
	for _, instName := range list {
		baseName := PrefixBackup + instName + ".zip"
		baseFile := cloudIndex[baseName]

		var totalSize int64 = baseFile.Size
		var deltaCount int
		prefixDelta := PrefixDelta + instName + "_"
		for name, f := range cloudIndex {
			if strings.HasPrefix(name, prefixDelta) && strings.HasSuffix(name, ".zip") {
				deltaCount++
				totalSize += f.Size
			}
		}

		realName := instName
		var iconData *string
		var loader *string
		if m, ok := metaCache[instName]; ok {
			if m.RealName != "" {
				realName = m.RealName
			}
			iconData = m.IconData
			loader = m.Loader
		}

		lastBackupStr := baseFile.ModifiedTime.Format(time.RFC3339)
		richList = append(richList, ipc.RichInstanceInfo{
			Name:       instName,
			RealName:   realName,
			DeltaCount: deltaCount,
			SizeBytes:  totalSize,
			LastBackup: &lastBackupStr,
			IconData:   iconData,
			Loader:     loader,
		})
	}

	ipc.Send(ipc.CloudListMessage{
		Type:     "CLOUD_LIST",
		Data:     list,
		RichData: richList,
	})
}

func handleDelete(targetInst string, cloudIndex map[string]providers.CloudFile, p providers.Provider, dataDir string) {
	safeInst := paths.GetCanonicalName(targetInst)
	baseBackupName := fmt.Sprintf("%s%s.zip", PrefixBackup, safeInst)
	manifestName := fmt.Sprintf("%s%s.json", PrefixManifest, safeInst)
	metaName := fmt.Sprintf("%s%s.json", PrefixMeta, safeInst)
	prefixDelta := fmt.Sprintf("%s%s_", PrefixDelta, safeInst)

	for name, f := range cloudIndex {
		if name == baseBackupName || name == manifestName || name == metaName || strings.HasPrefix(name, prefixDelta) {
			_ = p.DeleteFile(f.ID)
		}
	}

	// Suppression locale
	_ = os.Remove(paths.GetManifestPath(safeInst))
	_ = os.Remove(filepath.Join(dataDir, fmt.Sprintf("manifest_%s.json", safeInst)))
	_ = os.Remove(paths.GetMetaPath(safeInst))
	_ = os.Remove(filepath.Join(dataDir, fmt.Sprintf("meta_%s.json", safeInst)))

	syncInfoPath := paths.GetLastSyncPath()
	var syncInfo map[string]string
	if data, err := os.ReadFile(syncInfoPath); err == nil {
		if json.Unmarshal(data, &syncInfo) == nil {
			delete(syncInfo, safeInst)
			if b, err := json.MarshalIndent(syncInfo, "", "  "); err == nil {
				_ = osWriteFileAtomic(syncInfoPath, b, 0644)
			}
		}
	}

	ipc.Send(ipc.BaseMessage{
		Type:      "SUCCESS",
		Instance:  targetInst,
		Message:   "Deleted from Cloud.",
		ErrorCode: "DELETED_CLOUD",
	})
}

func createRollbackSnapshot(localPath string) string {
	instDir := filepath.Dir(localPath)
	folderName := filepath.Base(localPath)
	timestamp := time.Now().UnixMilli()
	rollbackFolder := filepath.Join(instDir, fmt.Sprintf("%s_rollback_%d", folderName, timestamp))

	if err := copyDir(localPath, rollbackFolder); err != nil {
		ipc.Logf("[sync] Impossible de créer le snapshot rollback : %v", err)
		return ""
	}

	ipc.Logf("[sync] Snapshot rollback créé : %s", filepath.Base(rollbackFolder))
	return rollbackFolder
}

func cleanupRollback(rollbackPath string) {
	_ = os.RemoveAll(rollbackPath)
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = ioCopy(out, in)
		return err
	})
}

func ioCopy(dst *os.File, src *os.File) (int64, error) {
	buf := make([]byte, 64*1024)
	var total int64
	for {
		n, rErr := src.Read(buf)
		if n > 0 {
			wN, wErr := dst.Write(buf[:n])
			total += int64(wN)
			if wErr != nil {
				return total, wErr
			}
		}
		if rErr != nil {
			if rErr == os.ErrClosed || rErr.Error() == "EOF" {
				break
			}
			return total, rErr
		}
	}
	return total, nil
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			return true
		}
	}
	return false
}

