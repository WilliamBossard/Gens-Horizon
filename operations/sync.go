package operations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/auth"
	"github.com/WilliamBossard/Gens-Horizon/ipc"
	"github.com/WilliamBossard/Gens-Horizon/lock"
	"github.com/WilliamBossard/Gens-Horizon/paths"
	"github.com/WilliamBossard/Gens-Horizon/providers"
	"github.com/WilliamBossard/Gens-Horizon/retry"
	"github.com/WilliamBossard/Gens-Horizon/scanner"
	"github.com/WilliamBossard/Gens-Horizon/ziputils"
)

func maybeDecryptDownloadedZip(zipPath string) (string, string, error) {
	enc, err := ziputils.IsE2EEncryptedFile(zipPath)
	if err != nil {
		return "", "CORRUPT_ARCHIVE", err
	}
	if !enc {
		return zipPath, "", nil
	}
	pass, err := auth.LoadE2EPassphrase()
	if err != nil {
		return "", "E2E_KEY_LOAD_FAILED", err
	}
	if pass == "" {
		return "", "E2E_KEY_REQUIRED", errors.New("archive chiffrée : veuillez saisir votre clé de récupération E2E")
	}
	decPath := zipPath + ".dec"
	if err := ziputils.DecryptFileE2E(zipPath, decPath, pass); err != nil {
		if errors.Is(err, ziputils.ErrE2EMismatch) {
			return "", "E2E_KEY_MISMATCH", errors.New("clé E2E incorrecte pour cette archive")
		}
		return "", "E2E_DECRYPT_FAILED", err
	}
	return decPath, "", nil
}

func Sync(args []string) {
	SyncWithContext(context.Background(), args)
}

func SyncWithContext(ctx context.Context, args []string) {
	if ctx == nil {
		ctx = context.Background()
	}
	isList := hasFlag(args, "--list")
	isDelete := hasFlag(args, "--delete")
	isDryRun := hasFlag(args, "--dry-run")

	if !isList && !isDryRun {
		if !lock.AcquireLock() {
			ipc.SendError("ERR_ALREADY_RUNNING", "ERR_ALREADY_RUNNING", "")
			return
		}
		defer lock.ReleaseLock()
		paths.CleanupOrphanedTemps(paths.GetHorizonDataDir())
	}

	if !retry.CheckConnectivity() {
		ipc.SendOffline("Internet indisponible.")
		return
	}

	settings := paths.LoadSettings()
	p, err := providers.GetProvider(settings)
	if err != nil {
		ipc.SendError("AUTH_KEYRING_ERROR", fmt.Sprintf("Erreur temporaire d'accès au trousseau d'authentification : %v", err), "")
		return
	}
	if p == nil {
		ipc.SendError("AUTH_EXPIRED", "Session expirée. Veuillez lier à nouveau votre compte depuis les paramètres.", "")
		return
	}

	retryOpts := retry.FromSettings(settings.MaxRetries, settings.RetryBaseDelay, "[sync]")

	var cloudIndex map[string]providers.CloudFile
	if isList || isDryRun {
		cloudIndex, err = providers.GetCloudIndexReadOnlyWithContext(ctx, p, retryOpts, "[sync]")
	} else {
		providers.InvalidateCloudIndexCache()
		cloudIndex, err = providers.GetCloudIndexAndCleanDuplicatesWithContext(ctx, p, retryOpts, "[sync]")
	}
	if err != nil {
		ipc.SendError("CLOUD_ERROR", err.Error(), "")
		return
	}

	dataDir := paths.GetHorizonDataDir()
	instDir := paths.GetInstancesFolder()

	// 1. Traitement de --list
	if isList {
		handleCloudListWithContext(ctx, cloudIndex, p)
		return
	}

	targetInst := parseTargetInstance(args)

	// 2. Traitement de --delete
	if isDelete {
		if targetInst == "" {
			ipc.SendError("ARG_MISSING", "Veuillez spécifier l'instance à supprimer du cloud.", "")
			return
		}
		handleDeleteWithContext(ctx, retryOpts, targetInst, cloudIndex, p, dataDir)
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

	if isDryRun {
		type previewItem struct {
			Instance   string `json:"instance"`
			NeedsBase  bool   `json:"needsBase"`
			DeltaCount int    `json:"deltaCount"`
			TotalBytes int64  `json:"totalBytes"`
		}
		var previewItems []previewItem
		for _, inst := range instancesToSync {
			safeInst, nameErr := paths.SanitizeInstanceName(paths.GetCanonicalName(inst))
			if nameErr != nil {
				continue
			}
			localPath, pathErr := paths.SafeJoinUnder(instDir, safeInst)
			if pathErr != nil {
				continue
			}
			baseBackupName := fmt.Sprintf("%s%s.zip", PrefixBackup, safeInst)
			baseFile, hasBase := cloudIndex[baseBackupName]
			if !hasBase {
				continue
			}
			_, statErr := os.Stat(localPath)
			isNew := os.IsNotExist(statErr)
			var lastSyncTime int64
			if rawTime, ok := syncInfo[safeInst]; ok {
				if t, err := time.Parse(time.RFC3339Nano, rawTime); err == nil {
					lastSyncTime = t.UnixMilli()
				}
			}
			needsBase := isNew || baseFile.ModifiedTime.UnixMilli() > lastSyncTime
			deltas := selectSyncDeltas(cloudIndex, safeInst, baseFile, lastSyncTime, needsBase)
			if !needsBase && len(deltas) == 0 {
				continue
			}
			var totalBytes int64
			if needsBase {
				totalBytes += baseFile.Size
			}
			for _, d := range deltas {
				totalBytes += d.file.Size
			}
			previewItems = append(previewItems, previewItem{
				Instance:   inst,
				NeedsBase:  needsBase,
				DeltaCount: len(deltas),
				TotalBytes: totalBytes,
			})
		}
		ipc.Send(map[string]any{
			"type":  "SYNC_PREVIEW",
			"items": previewItems,
		})
		return
	}

	for _, inst := range instancesToSync {
		if ctx.Err() != nil {
			ipc.Logf("[sync] Synchronisation interrompue par arrêt gracieux avant '%s'.", inst)
			return
		}
		safeInst, nameErr := paths.SanitizeInstanceName(paths.GetCanonicalName(inst))
		if nameErr != nil {
			ipc.SendError("INVALID_INSTANCE_NAME", nameErr.Error(), inst)
			continue
		}
		localPath, pathErr := paths.SafeJoinUnder(instDir, safeInst)
		if pathErr != nil {
			ipc.SendError("INVALID_INSTANCE_PATH", pathErr.Error(), inst)
			continue
		}

		baseBackupName := fmt.Sprintf("%s%s.zip", PrefixBackup, safeInst)
		baseFile, hasBase := cloudIndex[baseBackupName]
		if !hasBase {
			ipc.SendError("NOT_FOUND", fmt.Sprintf("Aucune sauvegarde cloud trouvée pour l'instance '%s'.", inst), inst)
			continue
		}

		isNewInstance := false
		if _, err := os.Stat(localPath); os.IsNotExist(err) {
			isNewInstance = true
			_ = os.MkdirAll(localPath, 0755)
		}

		// Lecture du timestamp de dernière synchro locale (horodatage serveur RFC3339Nano)
		var lastSyncTime int64
		if rawTime, ok := syncInfo[safeInst]; ok {
			if t, err := time.Parse(time.RFC3339Nano, rawTime); err == nil {
				lastSyncTime = t.UnixMilli()
			}
		}

		baseCloudTime := baseFile.ModifiedTime.UnixMilli()
		needsBaseExtract := isNewInstance || baseCloudTime > lastSyncTime

		// Application chronologique des Deltas plus récents que le dernier sync serveur
		deltas := selectSyncDeltas(cloudIndex, safeInst, baseFile, lastSyncTime, needsBaseExtract)

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

		// Récupération optionnelle des empreintes SHA-256 cloud depuis meta_<inst>.json
		var cloudMeta struct {
			ArchiveSha256 string            `json:"archiveSha256"`
			DeltaSha256s  map[string]string `json:"deltaSha256s"`
		}
		metaName := fmt.Sprintf("%s%s.json", PrefixMeta, safeInst)
		if mf, ok := cloudIndex[metaName]; ok {
			if metaErr := retry.Do(ctx, retryOpts, func() error {
				return p.DownloadJSON(ctx, mf.ID, &cloudMeta)
			}); metaErr != nil && !errors.Is(metaErr, providers.ErrNotFound) {
				ipc.SendError("META_FETCH_FAILED", fmt.Sprintf("Impossible de récupérer les empreintes de sécurité (%s) : %v", metaName, metaErr), inst)
				continue
			}
		}

		// Rollback paresseux : créé uniquement après téléchargement et validation des archives
		var rollbackPath string
		ensureRollback := func() error {
			if !isNewInstance && rollbackPath == "" {
				var snapErr error
				rollbackPath, snapErr = createRollbackSnapshot(localPath)
				if snapErr != nil {
					return snapErr
				}
				pruneOldRollbacks(instDir, 3, safeInst+"_rollback_", filepath.Base(localPath)+"_rollback_")
			}
			return nil
		}

		restoreOnSyncFailure := func() {
			if isNewInstance {
				_ = os.RemoveAll(localPath)
				return
			}
			if rollbackPath != "" {
				if rErr := swapRestoreSnapshot(rollbackPath, localPath); rErr != nil {
					ipc.Logf("[sync] AVERTISSEMENT : échec restauration automatique rollback %s : %v", rollbackPath, rErr)
				} else {
					ipc.Logf("[sync] Rollback restauré automatiquement suite à l'échec de synchro : %s", inst)
					cleanupRollback(rollbackPath)
				}
			}
		}

		if needsBaseExtract {
			baseTmpDir, tmpDirErr := os.MkdirTemp("", "horizon-sync-*")
			if tmpDirErr != nil {
				ipc.SendError("DOWNLOAD_FAILED", tmpDirErr.Error(), inst)
				continue
			}
			tempBase := filepath.Join(baseTmpDir, fmt.Sprintf("base_%s.zip", safeInst))
			ipc.SendProgress("DOWNLOADING", 0, inst)

			err = p.DownloadFile(ctx, baseFile.ID, tempBase, func(pct int) {
				ipc.SendProgress("DOWNLOADING", pct, inst)
			}, baseFile.Size)
			if err != nil {
				_ = os.RemoveAll(baseTmpDir)
				if isNewInstance {
					_ = os.RemoveAll(localPath)
				} else if rollbackPath != "" {
					cleanupRollback(rollbackPath)
				}
				ipc.SendError("DOWNLOAD_FAILED", err.Error(), inst)
				continue
			}
			if cloudMeta.ArchiveSha256 != "" {
				actualSha, shaErr := computeFileSHA256(tempBase)
				if shaErr != nil || !strings.EqualFold(actualSha, cloudMeta.ArchiveSha256) {
					_ = os.RemoveAll(baseTmpDir)
					if isNewInstance {
						_ = os.RemoveAll(localPath)
					}
					ipc.SendError("CORRUPT_ARCHIVE", fmt.Sprintf("Empreinte SHA-256 invalide pour l'archive cloud (attendu %s, obtenu %s).", cloudMeta.ArchiveSha256, actualSha), inst)
					continue
				}
			}

			plainBase, decCode, decErr := maybeDecryptDownloadedZip(tempBase)
			if decErr != nil {
				_ = os.RemoveAll(baseTmpDir)
				if isNewInstance {
					_ = os.RemoveAll(localPath)
				}
				ipc.SendError(decCode, decErr.Error(), inst)
				continue
			}

			if err := ziputils.VerifyZipIntegrity(plainBase); err != nil {
				_ = os.RemoveAll(baseTmpDir)
				if isNewInstance {
					_ = os.RemoveAll(localPath)
				}
				ipc.SendError("CORRUPT_ARCHIVE", err.Error(), inst)
				continue
			}

			if rbErr := ensureRollback(); rbErr != nil {
				_ = os.RemoveAll(baseTmpDir)
				ipc.SendError("ROLLBACK_SNAPSHOT_FAILED", rbErr.Error(), inst)
				continue
			}
			selectiveProfile := resolveSelectiveProfile(settings, inst, safeInst)
			if !isNewInstance {
				if cErr := cleanNonIgnoredEntriesForProfile(localPath, selectiveProfile); cErr != nil {
					_ = os.RemoveAll(baseTmpDir)
					restoreOnSyncFailure()
					ipc.SendError("CLEAN_FAILED", cErr.Error(), inst)
					continue
				}
			}

			ipc.SendProgress("EXTRACTING", 0, inst)
			err = ziputils.ExtractZip(plainBase, localPath, func(pct int) {
				ipc.SendProgress("EXTRACTING", pct, inst)
			})
			_ = os.RemoveAll(baseTmpDir)
			if err != nil {
				restoreOnSyncFailure()
				ipc.SendError("EXTRACT_FAILED", err.Error(), inst)
				continue
			}
		}

		// Tri chronologique croissant
		sort.Slice(deltas, func(i, j int) bool {
			return deltas[i].timestamp < deltas[j].timestamp
		})

		// Pré-téléchargement parallèle des deltas (concurrence max: 4)
		type downloadedDelta struct {
			tempDir  string
			tempPath string
			valid    bool
			errMsg   string
		}
		dlResults := make([]downloadedDelta, len(deltas))
		if len(deltas) > 0 {
			var dlWg sync.WaitGroup
			dlSem := make(chan struct{}, 4)
			for i, d := range deltas {
				dlWg.Add(1)
				dlSem <- struct{}{}
				go func(idx int, item syncDeltaEntry) {
					defer dlWg.Done()
					defer func() { <-dlSem }()
					dTmpDir, dErr := os.MkdirTemp("", "horizon-delta-*")
					if dErr != nil {
						dlResults[idx] = downloadedDelta{errMsg: dErr.Error()}
						return
					}
					tempDelta := filepath.Join(dTmpDir, fmt.Sprintf("delta_%s_%d.zip", safeInst, item.timestamp))
					if dErr := p.DownloadFile(ctx, item.file.ID, tempDelta, nil, item.file.Size); dErr != nil {
						_ = os.RemoveAll(dTmpDir)
						ipc.Logf("[sync] Échec téléchargement delta %s : %v", item.file.Name, dErr)
						dlResults[idx] = downloadedDelta{errMsg: dErr.Error()}
						return
					}
					if expectedDeltaSha := cloudMeta.DeltaSha256s[item.file.Name]; expectedDeltaSha != "" {
						actualSha, shaErr := computeFileSHA256(tempDelta)
						if shaErr != nil || !strings.EqualFold(actualSha, expectedDeltaSha) {
							_ = os.RemoveAll(dTmpDir)
							errTxt := fmt.Sprintf("Empreinte SHA-256 invalide pour %s (attendu %s, obtenu %s)", item.file.Name, expectedDeltaSha, actualSha)
							ipc.Logf("[sync] %s", errTxt)
							dlResults[idx] = downloadedDelta{errMsg: errTxt}
							return
						}
					}
					plainDelta, _, decErr := maybeDecryptDownloadedZip(tempDelta)
					if decErr != nil {
						_ = os.RemoveAll(dTmpDir)
						ipc.Logf("[sync] Échec déchiffrement delta %s : %v", item.file.Name, decErr)
						dlResults[idx] = downloadedDelta{errMsg: decErr.Error()}
						return
					}
					if vErr := ziputils.VerifyZipIntegrity(plainDelta); vErr != nil {
						_ = os.RemoveAll(dTmpDir)
						ipc.Logf("[sync] Archive delta corrompue %s : %v", item.file.Name, vErr)
						dlResults[idx] = downloadedDelta{errMsg: vErr.Error()}
						return
					}
					dlResults[idx] = downloadedDelta{tempDir: dTmpDir, tempPath: plainDelta, valid: true}
				}(i, d)
			}
			dlWg.Wait()
		}

		cleanupDeltaTemps := func() {
			for _, r := range dlResults {
				if r.tempDir != "" {
					_ = os.RemoveAll(r.tempDir)
				} else if r.tempPath != "" {
					_ = os.Remove(r.tempPath)
				}
			}
		}

		deltaFailed := ""
		for i, r := range dlResults {
			if !r.valid {
				deltaFailed = fmt.Sprintf("Delta %s invalide ou introuvable : %s", deltas[i].file.Name, r.errMsg)
				break
			}
		}
		if deltaFailed != "" {
			cleanupDeltaTemps()
			if needsBaseExtract {
				restoreOnSyncFailure()
			} else if rollbackPath != "" {
				cleanupRollback(rollbackPath)
			}
			ipc.SendError("DELTA_DOWNLOAD_FAILED", deltaFailed, inst)
			continue
		}

		if len(deltas) > 0 {
			if rbErr := ensureRollback(); rbErr != nil {
				cleanupDeltaTemps()
				ipc.SendError("ROLLBACK_SNAPSHOT_FAILED", rbErr.Error(), inst)
				continue
			}
		}
		// Application séquentielle stricte dans l'ordre chronologique
		applyFailed := ""
		for i, d := range deltas {
			res := dlResults[i]
			ipc.Logf("[sync] Application du delta cloud : %s (taille: %d octets)", d.file.Name, d.file.Size)
			if aErr := ziputils.ApplyDelta(res.tempPath, localPath, func(pct int) {
				ipc.SendDeltaProgress("APPLYING_DELTA", pct, inst, d.file.Name)
			}); aErr != nil {
				applyFailed = fmt.Sprintf("Échec application delta %s : %v", d.file.Name, aErr)
				break
			}
		}
		cleanupDeltaTemps()
		if applyFailed != "" {
			restoreOnSyncFailure()
			ipc.SendError("DELTA_APPLY_FAILED", applyFailed, inst)
			continue
		}

		// Téléchargement du manifest distant pour synchroniser la référence locale
		manifestName := fmt.Sprintf("%s%s.json", PrefixManifest, safeInst)
		if mf, ok := cloudIndex[manifestName]; ok {
			var remoteM scanner.Manifest
			err = retry.Do(ctx, retryOpts, func() error {
				remoteM = nil
				return p.DownloadJSON(ctx, mf.ID, &remoteM)
			})
			if err != nil {
				restoreOnSyncFailure()
				ipc.SendError("MANIFEST_DOWNLOAD_FAILED", fmt.Sprintf("Échec téléchargement manifest cloud : %v", err), inst)
				continue
			}
			localManifestFile := paths.GetManifestPath(safeInst)
			if b, err := json.MarshalIndent(remoteM, "", "  "); err == nil {
				if wErr := osWriteFileAtomic(localManifestFile, b, 0644); wErr != nil {
					restoreOnSyncFailure()
					ipc.SendError("MANIFEST_SAVE_FAILED", wErr.Error(), inst)
					continue
				}
			}
		}

		updateLastSyncWithTime(safeInst, maxServerTimeForInstance(cloudIndex, safeInst))

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
	handleCloudListWithContext(context.Background(), cloudIndex, p)
}

func handleCloudListWithContext(ctx context.Context, cloudIndex map[string]providers.CloudFile, p providers.Provider) {
	if ctx == nil {
		ctx = context.Background()
	}
	var list []string
	seen := make(map[string]bool)
	for name := range cloudIndex {
		if strings.HasPrefix(name, PrefixBackup) && strings.HasSuffix(name, ".zip") {
			rawName := strings.TrimSuffix(strings.TrimPrefix(name, PrefixBackup), ".zip")
			instName, err := paths.SanitizeInstanceName(rawName)
			if err != nil {
				continue
			}
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

	for _, rawInstName := range list {
		instName, err := paths.SanitizeInstanceName(rawInstName)
		if err != nil {
			continue
		}
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
				if err := retry.Do(ctx, retry.DefaultOptions(), func() error {
					return p.DownloadJSON(ctx, fID, &meta)
				}); err == nil {
					metaMu.Lock()
					metaCache[instName] = meta
					metaMu.Unlock()

					if b, err := json.MarshalIndent(meta, "", "  "); err == nil {
						_ = osWriteFileAtomic(localPath, b, 0644)
						if !cloudModTime.IsZero() {
							_ = os.Chtimes(localPath, cloudModTime, cloudModTime)
						}
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
		for name, f := range cloudIndex {
			if _, ok := MatchDeltaForInstance(name, instName); ok {
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
			if m.IconData != nil && isValidImageDataURI(*m.IconData) {
				iconData = m.IconData
			}
			loader = m.Loader
		}

		// Fallback intelligent : si l'icône est absente de la métadonnée cloud mais que l'instance existe localement sur le disque
		if iconData == nil || *iconData == "" {
			instDir := paths.GetInstancesFolder()
			if safeKey, nameErr := paths.SanitizeInstanceName(paths.GetCanonicalName(instName)); nameErr == nil {
				localInstFolder, pathErr := paths.SafeJoinUnder(instDir, safeKey)
				if pathErr == nil {
					if _, err := os.Stat(localInstFolder); os.IsNotExist(err) {
						if safeRaw, rawErr := paths.SanitizeInstanceName(instName); rawErr == nil {
							if altFolder, altErr := paths.SafeJoinUnder(instDir, safeRaw); altErr == nil {
								localInstFolder = altFolder
							}
						}
					}
					if _, err := os.Stat(localInstFolder); err == nil {
						localMeta := extractInstanceMetadata(localInstFolder, instName)
						if b64, ok := localMeta["iconData"].(string); ok && b64 != "" {
							iconData = &b64
						}
						if ldr, ok := localMeta["loader"].(string); ok && ldr != "" && (loader == nil || *loader == "") {
							loader = &ldr
						}
					}
				}
			}
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
	handleDeleteWithContext(context.Background(), retry.Options{MaxRetries: 1}, targetInst, cloudIndex, p, dataDir)
}

func handleDeleteWithContext(ctx context.Context, retryOpts retry.Options, targetInst string, cloudIndex map[string]providers.CloudFile, p providers.Provider, dataDir string) {
	safeInst, err := paths.SanitizeInstanceName(paths.GetCanonicalName(targetInst))
	if err != nil {
		ipc.SendError("INVALID_INSTANCE_NAME", err.Error(), targetInst)
		return
	}
	baseBackupName := fmt.Sprintf("%s%s.zip", PrefixBackup, safeInst)
	manifestName := fmt.Sprintf("%s%s.json", PrefixManifest, safeInst)
	metaName := fmt.Sprintf("%s%s.json", PrefixMeta, safeInst)

	var deleteErrs []string
	for name, f := range cloudIndex {
		_, isMatchingDelta := MatchDeltaForInstance(name, safeInst)
		if name == baseBackupName || name == manifestName || name == metaName || isMatchingDelta {
			delErr := retry.Do(ctx, retryOpts, func() error {
				err := p.DeleteFile(ctx, f.ID)
				if errors.Is(err, providers.ErrNotFound) {
					return nil
				}
				return err
			})
			if delErr != nil {
				ipc.Logf("[sync] Échec suppression Cloud '%s' (%s) : %v", name, f.ID, delErr)
				deleteErrs = append(deleteErrs, fmt.Sprintf("%s: %v", name, delErr))
			}
		}
	}
	providers.InvalidateCloudIndexCache()

	if len(deleteErrs) > 0 {
		ipc.SendError("DELETE_FAILED", fmt.Sprintf("Failed to delete from Cloud: %s", strings.Join(deleteErrs, "; ")), targetInst)
		return
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

func createRollbackSnapshot(localPath string) (string, error) {
	instDir := filepath.Dir(localPath)
	folderName := filepath.Base(localPath)
	timestamp := time.Now().UnixMilli()
	rollbackFolder := filepath.Join(instDir, fmt.Sprintf("%s_rollback_%d", folderName, timestamp))

	if err := copyDir(localPath, rollbackFolder, true); err != nil {
		ipc.Logf("[sync] Impossible de créer le snapshot rollback : %v", err)
		return "", err
	}

	ipc.Logf("[sync] Snapshot rollback créé : %s", filepath.Base(rollbackFolder))
	return rollbackFolder, nil
}

func cleanupRollback(rollbackPath string) {
	_ = os.RemoveAll(rollbackPath)
}

func swapRestoreSnapshot(snapshotDir, targetPath string) error {
	stagingPath := fmt.Sprintf("%s.restore_tmp_%d", targetPath, time.Now().UnixMilli())
	if err := copyDir(snapshotDir, stagingPath, false); err != nil {
		_ = os.RemoveAll(stagingPath)
		return err
	}

	backupOldPath := fmt.Sprintf("%s.restore_old_%d", targetPath, time.Now().UnixMilli())
	hasExisting := false
	if _, err := os.Stat(targetPath); err == nil {
		hasExisting = true
		if err := os.Rename(targetPath, backupOldPath); err != nil {
			_ = os.RemoveAll(stagingPath)
			ipc.Logf("[sync] Rename direct impossible (%v), tentative de restauration sélective in-place", err)
			return restoreInPlace(snapshotDir, targetPath)
		}
	}

	if err := os.Rename(stagingPath, targetPath); err != nil {
		if hasExisting {
			if rbErr := os.Rename(backupOldPath, targetPath); rbErr != nil {
				ipc.Logf("[sync] AVERTISSEMENT : échec rollback rename (%v), tentative restoreInPlace...", rbErr)
				_ = restoreInPlace(backupOldPath, targetPath)
			}
		}
		_ = os.RemoveAll(stagingPath)
		return err
	}
	if hasExisting {
		preserveIgnoredEntries(backupOldPath, targetPath)
		_ = os.RemoveAll(backupOldPath)
	}
	return nil
}

// preserveIgnoredEntries transfère les dossiers/fichiers exclus de la synchro (logs, screenshots, versions, etc.)
// depuis l'ancien dossier restauré vers targetPath avant suppression du backup temporaire.
func preserveIgnoredEntries(backupOldPath, targetPath string) {
	_ = filepath.WalkDir(backupOldPath, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		rel, err := filepath.Rel(backupOldPath, path)
		if err != nil || rel == "." {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		lower := strings.ToLower(d.Name())
		isIgnoredDir := d.IsDir() && scanner.IgnoredDirs[lower]
		isIgnoredFile := !d.IsDir() && (scanner.IgnoredDirs[lower] || scanner.IsIgnoredFile(d.Name()))
		if !isIgnoredDir && !isIgnoredFile {
			return nil
		}

		dest := filepath.Join(targetPath, rel)
		if _, statErr := os.Lstat(dest); statErr == nil || !os.IsNotExist(statErr) {
			if isIgnoredDir {
				return filepath.SkipDir
			}
			return nil
		}
		if mkErr := os.MkdirAll(filepath.Dir(dest), 0755); mkErr != nil {
			if isIgnoredDir {
				return filepath.SkipDir
			}
			return nil
		}
		if renameErr := os.Rename(path, dest); renameErr != nil {
			if isIgnoredDir {
				_ = copyDir(path, dest, false)
			} else if info, infoErr := d.Info(); infoErr == nil {
				_ = copySingleFile(path, dest, info.Mode())
			}
		}
		if isIgnoredDir {
			return filepath.SkipDir
		}
		return nil
	})
}

type syncDeltaEntry struct {
	file      providers.CloudFile
	timestamp int64
}

func selectSyncDeltas(
	cloudIndex map[string]providers.CloudFile,
	safeInst string,
	baseFile providers.CloudFile,
	lastSyncTime int64,
	needsBaseExtract bool,
) []syncDeltaEntry {
	var deltas []syncDeltaEntry
	for name, f := range cloudIndex {
		orderTs, ok := MatchDeltaForInstance(name, safeInst)
		if ok {
			serverModMs := f.ModifiedTime.UnixMilli()
			isOlderThanBase := !baseFile.ModifiedTime.IsZero() && !f.ModifiedTime.IsZero() && f.ModifiedTime.Before(baseFile.ModifiedTime)
			if (serverModMs > lastSyncTime && !isOlderThanBase) || (needsBaseExtract && !isOlderThanBase) {
				deltas = append(deltas, syncDeltaEntry{file: f, timestamp: orderTs})
			}
		}
	}
	return deltas
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			return true
		}
	}
	return false
}

