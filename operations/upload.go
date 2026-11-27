package operations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/auth"
	"github.com/WilliamBossard/Gens-Horizon/config"
	"github.com/WilliamBossard/Gens-Horizon/ipc"
	"github.com/WilliamBossard/Gens-Horizon/lock"
	"github.com/WilliamBossard/Gens-Horizon/paths"
	"github.com/WilliamBossard/Gens-Horizon/providers"
	"github.com/WilliamBossard/Gens-Horizon/retry"
	"github.com/WilliamBossard/Gens-Horizon/scanner"
	"github.com/WilliamBossard/Gens-Horizon/ziputils"
)

func resolveSelectiveProfile(settings config.Settings, inst, safeInst string) string {
	if !settings.SelectiveSync || len(settings.SelectiveProfiles) == 0 {
		return ""
	}
	if p, ok := settings.SelectiveProfiles[inst]; ok && p != "" {
		return p
	}
	if p, ok := settings.SelectiveProfiles[safeInst]; ok && p != "" {
		return p
	}
	return ""
}

func buildUploadArchive(tempZip string, settings config.Settings, writeStream func(io.Writer) error, writeFile func(string) error) (string, string, error) {
	if !settings.E2EEncryption {
		if err := writeFile(tempZip); err != nil {
			return "", "COMPRESS_FAILED", err
		}
		return tempZip, "", nil
	}
	pass, err := auth.LoadE2EPassphrase()
	if err != nil {
		return "", "E2E_KEY_LOAD_FAILED", err
	}
	if pass == "" {
		return "", "E2E_KEY_REQUIRED", errors.New("chiffrement E2E activé mais aucune clé configurée")
	}
	encPath := tempZip + ".e2e"
	pr, pw := io.Pipe()
	zipErrCh := make(chan error, 1)
	go func() {
		wErr := writeStream(pw)
		_ = pw.CloseWithError(wErr)
		zipErrCh <- wErr
	}()
	encErr := ziputils.EncryptStreamE2E(pr, encPath, pass)
	_ = pr.CloseWithError(encErr)
	zipErr := <-zipErrCh
	if zipErr != nil {
		_ = os.Remove(encPath)
		return "", "COMPRESS_FAILED", zipErr
	}
	if encErr != nil {
		_ = os.Remove(encPath)
		return "", "E2E_ENCRYPT_FAILED", encErr
	}
	return encPath, "", nil
}

func parseTargetInstance(args []string) string {
	for i, arg := range args {
		if strings.HasPrefix(arg, "--instance=") {
			return strings.TrimPrefix(arg, "--instance=")
		}
		if arg == "--instance" && i+1 < len(args) {
			return args[i+1]
		}
	}
	// Recherche d'un argument positionnel qui n'est pas un flag ni la valeur d'un flag à paramètre
	skipNext := false
	for _, arg := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if arg == "--provider" {
			skipNext = true
			continue
		}
		if strings.HasPrefix(arg, "--") {
			continue
		}
		switch arg {
		case "upload", "sync", "check", "login", "quota", "rollback", "clean", "delete", "status":
			continue
		default:
			return arg
		}
	}
	return ""
}

func Upload(args []string) {
	UploadWithContext(context.Background(), args)
}

func UploadWithContext(ctx context.Context, args []string) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !lock.AcquireLock() {
		ipc.SendError("ERR_ALREADY_RUNNING", "ERR_ALREADY_RUNNING", "")
		return
	}
	defer lock.ReleaseLock()
	paths.CleanupOrphanedTemps(paths.GetHorizonDataDir())

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

	retryOpts := retry.FromSettings(settings.MaxRetries, settings.RetryBaseDelay, "[upload]")

	verifyHashes := hasFlag(args, "--verify-hashes")
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

	providers.InvalidateCloudIndexCache()
	cloudIndex, err := providers.GetCloudIndexAndCleanDuplicatesWithContext(ctx, p, retryOpts, "[upload]")
	if err != nil {
		ipc.SendError("CLOUD_ERROR", err.Error(), "")
		return
	}

	instDir := paths.GetInstancesFolder()

	for _, inst := range instances {
		if ctx.Err() != nil {
			ipc.Logf("[upload] Upload interrompu par arrêt gracieux avant '%s'.", inst)
			return
		}
		safeInst, nameErr := paths.SanitizeInstanceName(paths.GetCanonicalName(inst))
		if nameErr != nil {
			ipc.SendError("INVALID_INSTANCE_NAME", nameErr.Error(), inst)
			continue
		}
		localInstFolder, pathErr := paths.SafeJoinUnder(instDir, safeInst)
		if pathErr != nil {
			ipc.SendError("INVALID_INSTANCE_PATH", pathErr.Error(), inst)
			continue
		}
		if _, err := os.Stat(localInstFolder); os.IsNotExist(err) {
			altFolder, altErr := paths.SafeJoinUnder(instDir, inst)
			if altErr != nil {
				ipc.Logf("[upload] Chemin d'instance invalide : %s", inst)
				continue
			}
			localInstFolder = altFolder
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
		selectiveProfile := resolveSelectiveProfile(settings, inst, safeInst)
		newManifest, err := scanner.GenerateManifestWithProfile(localInstFolder, oldLocalManifest, verifyHashes, selectiveProfile)
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

		// Comptage des deltas existants (correspondance stricte du slug pour éviter toute collision de préfixe)
		var existingDeltas []providers.CloudFile
		for name, f := range cloudIndex {
			if _, ok := MatchDeltaForInstance(name, safeInst); ok {
				existingDeltas = append(existingDeltas, f)
			}
		}

		cleanupThreshold := settings.DeltaCleanupThreshold
		if cleanupThreshold <= 0 {
			cleanupThreshold = 10
		}

		// Vérification conflit : une version plus récente existe-t-elle sur le Cloud ?
		if conflictMsg := detectUploadConflict(
			safeInst,
			hasBase,
			hasManifest,
			baseFile,
			manifestFile,
			existingDeltas,
			paths.GetLastSyncPath(),
			hasFlag(args, "--force"),
		); conflictMsg != "" {
			ipc.SendError("CONFLICT_DETECTED", conflictMsg, inst)
			continue
		}

		var remoteManifest scanner.Manifest
		hasRemoteManifest := false
		if hasManifest {
			err = retry.Do(ctx, retryOpts, func() error {
				remoteManifest = nil
				return p.DownloadJSON(ctx, manifestFile.ID, &remoteManifest)
			})
			if err == nil {
				hasRemoteManifest = true
			} else {
				ipc.SendError("UPLOAD_FAILED", fmt.Sprintf("Impossible de télécharger le manifeste distant : %v", err), inst)
				continue
			}
		}

		var totalDeltasSize int64
		var oldestDeltaTime int64
		for _, d := range existingDeltas {
			totalDeltasSize += d.Size
			if !d.ModifiedTime.IsZero() {
				ms := d.ModifiedTime.UnixMilli()
				if oldestDeltaTime == 0 || ms < oldestDeltaTime {
					oldestDeltaTime = ms
				}
			}
		}

		isDeltaThresholdExceeded := len(existingDeltas) >= cleanupThreshold
		isSizeSignificant := hasBase && baseFile.Size > 0 && totalDeltasSize >= (baseFile.Size/2)
		isDeltaTooOld := len(existingDeltas) > 0 && oldestDeltaTime > 0 && time.Since(time.UnixMilli(oldestDeltaTime)) > 30*24*time.Hour
		isHardCapReached := len(existingDeltas) >= cleanupThreshold*2

		// Smart Consolidation : consolidation complète si le seuil est atteint ET (taille cumulée > 50% de la base, deltas > 30j, ou plafond de sécurité)
		needsFullBackup := !hasBase || !hasRemoteManifest ||
			(isDeltaThresholdExceeded && (isSizeSignificant || isDeltaTooOld || isHardCapReached))

		if needsFullBackup {
			ipc.Logf("[upload] Création d'une sauvegarde complète pour %s (deltas=%d, tailleDeltas=%d, baseSize=%d, max=%d)", inst, len(existingDeltas), totalDeltasSize, baseFile.Size, cleanupThreshold)
			tmpZipDir, tmpDirErr := os.MkdirTemp("", "horizon-up-*")
			if tmpDirErr != nil {
				ipc.SendError("COMPRESS_FAILED", tmpDirErr.Error(), inst)
				continue
			}
			tempZip := filepath.Join(tmpZipDir, fmt.Sprintf("full_%s.zip", safeInst))

			var manifestTotalBytes int64
			for _, entry := range newManifest {
				if s := entry.Size.Int64(); s > 0 {
					manifestTotalBytes += s
				}
			}

			ipc.SendProgress("COMPRESSING", 0, inst)
			var uploadZipPath, encCode string
			var encErr error
			if selectiveProfile != "" && selectiveProfile != "FULL" {
				allFiles := make([]string, 0, len(newManifest))
				for relPath := range newManifest {
					allFiles = append(allFiles, relPath)
				}
				uploadZipPath, encCode, encErr = buildUploadArchive(
					tempZip,
					settings,
					func(w io.Writer) error {
						return ziputils.WriteDeltaZip(localInstFolder, allFiles, nil, w, func(pct int) {
							ipc.SendProgress("COMPRESSING", pct, inst)
						})
					},
					func(p string) error {
						return ziputils.CreateDeltaZip(localInstFolder, allFiles, nil, p, func(pct int) {
							ipc.SendProgress("COMPRESSING", pct, inst)
						})
					},
				)
			} else {
				uploadZipPath, encCode, encErr = buildUploadArchive(
					tempZip,
					settings,
					func(w io.Writer) error {
						return ziputils.WriteFullZipWithHint(localInstFolder, manifestTotalBytes, w, func(pct int) {
							ipc.SendProgress("COMPRESSING", pct, inst)
						})
					},
					func(p string) error {
						return ziputils.CreateFullZipWithHint(localInstFolder, manifestTotalBytes, p, func(pct int) {
							ipc.SendProgress("COMPRESSING", pct, inst)
						})
					},
				)
			}
			if encErr != nil {
				_ = os.RemoveAll(tmpZipDir)
				ipc.SendError(encCode, encErr.Error(), inst)
				continue
			}
			baseArchiveSha, shaErr := computeFileSHA256(uploadZipPath)
			if shaErr != nil {
				_ = os.RemoveAll(tmpZipDir)
				ipc.SendError("HASH_FAILED", shaErr.Error(), inst)
				continue
			}

			ipc.SendProgress("UPLOADING", 0, inst)
			existingID := ""
			if hasBase {
				existingID = baseFile.ID
			}
			mExistingID := ""
			if hasManifest {
				mExistingID = manifestFile.ID
			}

			newBaseID, newMID, stepErrCode, upErr := uploadFullBackupAtomicWithContext(
				ctx,
				p,
				inst,
				safeInst,
				uploadZipPath,
				baseBackupName,
				existingID,
				manifestName,
				mExistingID,
				newManifest,
				retryOpts,
			)
			_ = os.RemoveAll(tmpZipDir)
			if upErr != nil {
				// Si la nouvelle archive de base a été promue mais que la promotion du manifest a échoué,
				// mettre à jour meta_<inst>.json avec le nouveau archiveSha256 pour éviter CORRUPT_ARCHIVE côté clients.
				if newBaseID != "" && baseArchiveSha != "" {
					metaData := extractInstanceMetadata(localInstFolder, inst)
					metaData["archiveSha256"] = baseArchiveSha
					metaData["deltaSha256s"] = map[string]string{}
					metaExistingID := ""
					if mf, ok := cloudIndex[metaName]; ok {
						metaExistingID = mf.ID
					}
					_ = retry.Do(ctx, retryOpts, func() error {
						_, mUpErr := p.UploadJSON(ctx, metaName, metaData, metaExistingID)
						return mUpErr
					})
				}
				ipc.SendError(stepErrCode, upErr.Error(), inst)
				continue
			}

			cloudIndex[baseBackupName] = providers.CloudFile{
				ID:           newBaseID,
				Name:         baseBackupName,
				ModifiedTime: time.Now(),
			}
			cloudIndex[manifestName] = providers.CloudFile{
				ID:           newMID,
				Name:         manifestName,
				ModifiedTime: time.Now(),
			}

			// Upload Metadata
			metaData := extractInstanceMetadata(localInstFolder, inst)
			if baseArchiveSha != "" {
				metaData["archiveSha256"] = baseArchiveSha
			}
			metaData["deltaSha256s"] = map[string]string{}
			metaExistingID := ""
			if mf, ok := cloudIndex[metaName]; ok {
				metaExistingID = mf.ID
			}
			metaOpts := retryOpts
			metaOpts.Label = "uploadMeta(" + inst + ")"
			if mErr := retry.Do(ctx, metaOpts, func() error {
				_, upErr := p.UploadJSON(ctx, metaName, metaData, metaExistingID)
				return upErr
			}); mErr != nil {
				ipc.Logf("[upload] avertissement : upload metadata cloud échoué pour %s : %v", inst, mErr)
			}
			if b, err := json.MarshalIndent(metaData, "", "  "); err == nil {
				if wErr := osWriteFileAtomic(paths.GetMetaPath(safeInst), b, 0644); wErr != nil {
					ipc.Logf("[upload] avertissement : sauvegarde metadata locale échouée : %v", wErr)
				}
			}

			// Nettoyage parallèle des anciens deltas consolidés (concurrence max: 4)
			if len(existingDeltas) > 0 {
				var delWg sync.WaitGroup
				delSem := make(chan struct{}, 4)
				for _, deltaFile := range existingDeltas {
					delWg.Add(1)
					delSem <- struct{}{}
					go func(df providers.CloudFile) {
						defer delWg.Done()
						defer func() { <-delSem }()
						delOpts := retryOpts
						delOpts.Label = "deleteOldDelta(" + df.Name + ")"
						if delErr := retry.Do(ctx, delOpts, func() error {
							return p.DeleteFile(ctx, df.ID)
						}); delErr != nil {
							ipc.Logf("[upload] AVERTISSEMENT : échec suppression ancien delta %s : %v", df.Name, delErr)
						}
					}(deltaFile)
					delete(cloudIndex, deltaFile.Name)
				}
				delWg.Wait()
			}

			// Sauvegarde du manifest en local
			if b, err := json.MarshalIndent(newManifest, "", "  "); err == nil {
				if wErr := osWriteFileAtomic(localManifestFile, b, 0644); wErr != nil {
					ipc.Logf("[upload] avertissement : sauvegarde manifest local échouée : %v", wErr)
				}
			}

			serverTime := fetchServerTimeForInstanceWithContext(ctx, p, retryOpts, safeInst, newBaseID, newMID)
			updateLastSyncWithTime(safeInst, serverTime)

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
				updateLastSyncWithTime(safeInst, maxServerTimeForInstance(cloudIndex, safeInst))
				continue
			}

			ipc.Logf("[upload] Delta sync pour %s : +%d, ~%d, -%d fichiers", inst, len(diff.Added), len(diff.Modified), len(diff.Deleted))

			filesToZip := append(diff.Added, diff.Modified...)
			tmpDeltaDir, tmpDirErr := os.MkdirTemp("", "horizon-delta-*")
			if tmpDirErr != nil {
				ipc.SendError("COMPRESS_FAILED", tmpDirErr.Error(), inst)
				continue
			}
			tempDelta := filepath.Join(tmpDeltaDir, fmt.Sprintf("delta_%s.zip", safeInst))

			ipc.SendProgress("COMPRESSING", 0, inst)
			uploadDeltaPath, encCode, encErr := buildUploadArchive(
				tempDelta,
				settings,
				func(w io.Writer) error {
					return ziputils.WriteDeltaZip(localInstFolder, filesToZip, diff.Deleted, w, func(pct int) {
						ipc.SendProgress("COMPRESSING", pct, inst)
					})
				},
				func(p string) error {
					return ziputils.CreateDeltaZip(localInstFolder, filesToZip, diff.Deleted, p, func(pct int) {
						ipc.SendProgress("COMPRESSING", pct, inst)
					})
				},
			)
			if encErr != nil {
				_ = os.RemoveAll(tmpDeltaDir)
				ipc.SendError(encCode, encErr.Error(), inst)
				continue
			}
			deltaArchiveSha, shaErr := computeFileSHA256(uploadDeltaPath)
			if shaErr != nil {
				_ = os.RemoveAll(tmpDeltaDir)
				ipc.SendError("HASH_FAILED", shaErr.Error(), inst)
				continue
			}

			deltaName := fmt.Sprintf("%s%s_%d.zip", PrefixDelta, safeInst, time.Now().UnixMilli())
			ipc.SendProgress("UPLOADING", 0, inst)
			var deltaID string
			upDeltaOpts := retryOpts
			upDeltaOpts.Label = "uploadDelta(" + inst + ")"
			err = retry.Do(ctx, upDeltaOpts, func() error {
				id, upErr := p.UploadZip(ctx, deltaName, uploadDeltaPath, "", func(pct int) {
					ipc.SendProgress("UPLOADING", pct, inst)
				})
				if upErr != nil {
					return upErr
				}
				deltaID = id
				return nil
			})
			_ = os.RemoveAll(tmpDeltaDir)
			if err != nil {
				ipc.SendError("UPLOAD_FAILED", err.Error(), inst)
				continue
			}
			cloudIndex[deltaName] = providers.CloudFile{
				ID:           deltaID,
				Name:         deltaName,
				ModifiedTime: time.Now(),
			}

			// Mise à jour du manifest avec retry (nettoie le delta orphelin si le manifest échoue)
			var newMID string
			err = retry.Do(ctx, retryOpts, func() error {
				id, upErr := p.UploadJSON(ctx, manifestName, newManifest, manifestFile.ID)
				if upErr != nil {
					return upErr
				}
				newMID = id
				return nil
			})
			if err != nil {
				if deltaID != "" {
					if rbErr := p.DeleteFile(ctx, deltaID); rbErr != nil {
						ipc.Logf("[upload] Échec nettoyage delta orphelin %s : %v", deltaName, rbErr)
					}
				}
				delete(cloudIndex, deltaName)
				ipc.SendError("MANIFEST_UPLOAD_FAILED", fmt.Sprintf("Échec mise à jour manifest cloud : %v", err), inst)
				continue
			}
			cloudIndex[manifestName] = providers.CloudFile{
				ID:           newMID,
				Name:         manifestName,
				ModifiedTime: time.Now(),
			}

			// Mise à jour metadata
			metaData := extractInstanceMetadata(localInstFolder, inst)
			var prevMeta struct {
				ArchiveSha256 string            `json:"archiveSha256"`
				DeltaSha256s  map[string]string `json:"deltaSha256s"`
			}
			canUpdateMeta := true
			if mf, ok := cloudIndex[metaName]; ok {
				metaFetchErr := retry.Do(ctx, retryOpts, func() error {
					return p.DownloadJSON(ctx, mf.ID, &prevMeta)
				})
				if metaFetchErr != nil && !errors.Is(metaFetchErr, providers.ErrNotFound) {
					if rawMeta, rErr := os.ReadFile(paths.GetMetaPath(safeInst)); rErr == nil {
						_ = json.Unmarshal(rawMeta, &prevMeta)
					} else {
						ipc.Logf("[upload] avertissement : impossible de lire metadata existante pour %s (%v) ; mise à jour meta ignorée pour préserver les empreintes SHA-256.", inst, metaFetchErr)
						canUpdateMeta = false
					}
				}
			} else if rawMeta, rErr := os.ReadFile(paths.GetMetaPath(safeInst)); rErr == nil {
				_ = json.Unmarshal(rawMeta, &prevMeta)
			}
			if canUpdateMeta {
				if prevMeta.ArchiveSha256 != "" {
					metaData["archiveSha256"] = prevMeta.ArchiveSha256
				}
				deltaShaMap := prevMeta.DeltaSha256s
				if deltaShaMap == nil {
					deltaShaMap = make(map[string]string)
				}
				if deltaArchiveSha != "" {
					deltaShaMap[deltaName] = deltaArchiveSha
				}
				metaData["deltaSha256s"] = deltaShaMap
				metaExistingID := ""
				if mf, ok := cloudIndex[metaName]; ok {
					metaExistingID = mf.ID
				}
				metaOpts := retryOpts
				metaOpts.Label = "uploadMeta(" + inst + ")"
				if mErr := retry.Do(ctx, metaOpts, func() error {
					_, upErr := p.UploadJSON(ctx, metaName, metaData, metaExistingID)
					return upErr
				}); mErr != nil {
					ipc.Logf("[upload] avertissement : upload metadata cloud échoué pour %s : %v", inst, mErr)
				}
				if b, err := json.MarshalIndent(metaData, "", "  "); err == nil {
					if wErr := osWriteFileAtomic(paths.GetMetaPath(safeInst), b, 0644); wErr != nil {
						ipc.Logf("[upload] avertissement : sauvegarde metadata locale échouée : %v", wErr)
					}
				}
			}

			// Sauvegarde du manifest en local
			if b, err := json.MarshalIndent(newManifest, "", "  "); err == nil {
				if wErr := osWriteFileAtomic(localManifestFile, b, 0644); wErr != nil {
					ipc.Logf("[upload] avertissement : sauvegarde manifest local échouée : %v", wErr)
				}
			}

			serverTime := fetchServerTimeForInstanceWithContext(ctx, p, retryOpts, safeInst, deltaID, newMID)
			updateLastSyncWithTime(safeInst, serverTime)

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

func maxServerTimeForInstance(cloudIndex map[string]providers.CloudFile, safeInst string) time.Time {
	var maxT time.Time
	consider := func(t time.Time) {
		if t.After(maxT) {
			maxT = t
		}
	}
	baseName := PrefixBackup + safeInst + ".zip"
	if bf, ok := cloudIndex[baseName]; ok {
		consider(bf.ModifiedTime)
	}
	manifestName := PrefixManifest + safeInst + ".json"
	if mf, ok := cloudIndex[manifestName]; ok {
		consider(mf.ModifiedTime)
	}
	for name, f := range cloudIndex {
		if _, ok := MatchDeltaForInstance(name, safeInst); ok {
			consider(f.ModifiedTime)
		}
	}
	if maxT.IsZero() {
		return time.Now().UTC()
	}
	return maxT.UTC()
}

func fetchServerTimeForInstance(p providers.Provider, opts retry.Options, safeInst string, fileIDs ...string) time.Time {
	return fetchServerTimeForInstanceWithContext(context.Background(), p, opts, safeInst, fileIDs...)
}

func fetchServerTimeForInstanceWithContext(ctx context.Context, p providers.Provider, opts retry.Options, safeInst string, fileIDs ...string) time.Time {
	if ctx == nil {
		ctx = context.Background()
	}
	providers.InvalidateCloudIndexCache()
	if fetcher, ok := p.(providers.FileTimestampFetcher); ok && len(fileIDs) > 0 {
		var maxT time.Time
		for _, id := range fileIDs {
			if id == "" {
				continue
			}
			var modTime time.Time
			fetchOpts := opts
			fetchOpts.Label = "getFileModifiedTime(" + id + ")"
			if err := retry.Do(ctx, fetchOpts, func() error {
				t, gErr := fetcher.GetFileModifiedTime(ctx, id)
				if gErr != nil {
					return gErr
				}
				modTime = t
				return nil
			}); err == nil && modTime.After(maxT) {
				maxT = modTime
			}
		}
		if !maxT.IsZero() {
			return maxT.UTC()
		}
	}
	freshIndex, err := providers.GetCloudIndexAndCleanDuplicatesWithContext(ctx, p, opts, "[upload-ts]")
	if err != nil {
		return time.Now().UTC()
	}
	return maxServerTimeForInstance(freshIndex, safeInst)
}

func updateLastSync(safeInst string) {
	updateLastSyncWithTime(safeInst, time.Now().UTC())
}

func updateLastSyncWithTime(safeInst string, serverTime time.Time) {
	if serverTime.IsZero() {
		serverTime = time.Now().UTC()
	}
	syncInfoPath := paths.GetLastSyncPath()
	syncInfo := make(map[string]string)
	if data, err := os.ReadFile(syncInfoPath); err == nil {
		_ = json.Unmarshal(data, &syncInfo)
	}
	syncInfo[safeInst] = serverTime.UTC().Format(time.RFC3339Nano)
	if b, err := json.MarshalIndent(syncInfo, "", "  "); err == nil {
		if wErr := osWriteFileAtomic(syncInfoPath, b, 0644); wErr != nil {
			ipc.Logf("[upload] avertissement : sauvegarde last_sync échouée : %v", wErr)
		}
	}
}

func detectUploadConflict(
	safeInst string,
	hasBase bool,
	hasManifest bool,
	baseFile providers.CloudFile,
	manifestFile providers.CloudFile,
	existingDeltas []providers.CloudFile,
	syncInfoPath string,
	force bool,
) string {
	if force || (!hasBase && !hasManifest) {
		return ""
	}

	var lastSyncTime int64
	if data, err := os.ReadFile(syncInfoPath); err == nil {
		var syncInfo map[string]string
		if json.Unmarshal(data, &syncInfo) == nil {
			if rawTime, ok := syncInfo[safeInst]; ok {
				if t, err := time.Parse(time.RFC3339Nano, rawTime); err == nil {
					lastSyncTime = t.UnixMilli()
				}
			}
		}
	}

	if lastSyncTime == 0 {
		return "Existing backup found on Cloud with no local sync history. Synchronize first or use --force to overwrite."
	}

	var cloudLatestTime int64
	if hasBase {
		cloudLatestTime = baseFile.ModifiedTime.UnixMilli()
	}
	if hasManifest && manifestFile.ModifiedTime.UnixMilli() > cloudLatestTime {
		cloudLatestTime = manifestFile.ModifiedTime.UnixMilli()
	}
	for _, d := range existingDeltas {
		if d.ModifiedTime.UnixMilli() > cloudLatestTime {
			cloudLatestTime = d.ModifiedTime.UnixMilli()
		}
	}

	// Si le cloud a été modifié après notre dernière synchro locale (+ marge de 2s)
	if cloudLatestTime > lastSyncTime+2000 {
		return "A newer version exists on Cloud. Synchronize first or use --force to overwrite."
	}
	return ""
}

// uploadFullBackupAtomic effectue un upload en deux phases (staging puis promotion) lorsque le provider
// implémente providers.FileRenamer et qu'une archive de base existe déjà, afin de préserver la cohérence
// base+manifest si une erreur réseau survient entre les deux envois.
func uploadFullBackupAtomic(
	p providers.Provider,
	inst string,
	safeInst string,
	tempZip string,
	baseBackupName string,
	existingBaseID string,
	manifestName string,
	existingManifestID string,
	newManifest scanner.Manifest,
	retryOpts retry.Options,
) (string, string, string, error) {
	return uploadFullBackupAtomicWithContext(
		context.Background(),
		p,
		inst,
		safeInst,
		tempZip,
		baseBackupName,
		existingBaseID,
		manifestName,
		existingManifestID,
		newManifest,
		retryOpts,
	)
}

func uploadFullBackupAtomicWithContext(
	ctx context.Context,
	p providers.Provider,
	inst string,
	safeInst string,
	tempZip string,
	baseBackupName string,
	existingBaseID string,
	manifestName string,
	existingManifestID string,
	newManifest scanner.Manifest,
	retryOpts retry.Options,
) (string, string, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	renamer, canStage := p.(providers.FileRenamer)
	useStaging := canStage && existingBaseID != ""

	if useStaging {
		stamp := time.Now().UnixMilli()
		stagingBackupName := fmt.Sprintf("GensHorizon_Staging_%s_backup_%d.zip", safeInst, stamp)
		stagingManifestName := fmt.Sprintf("GensHorizon_Staging_%s_manifest_%d.json", safeInst, stamp)

		var stagedBaseID string
		upStageOpts := retryOpts
		upStageOpts.Label = "uploadStagingBase(" + inst + ")"
		err := retry.Do(ctx, upStageOpts, func() error {
			id, upErr := p.UploadZip(ctx, stagingBackupName, tempZip, "", func(pct int) {
				ipc.SendProgress("UPLOADING", pct, inst)
			})
			if upErr != nil {
				return upErr
			}
			stagedBaseID = id
			return nil
		})
		if err != nil {
			return "", "", "UPLOAD_FAILED", err
		}

		var stagedManifestID string
		mOpts := retryOpts
		mOpts.Label = "uploadStagingManifest(" + inst + ")"
		err = retry.Do(ctx, mOpts, func() error {
			id, upErr := p.UploadJSON(ctx, stagingManifestName, newManifest, "")
			if upErr != nil {
				return upErr
			}
			stagedManifestID = id
			return nil
		})
		if err != nil {
			_ = p.DeleteFile(ctx, stagedBaseID)
			ipc.Logf("[upload] Échec envoi manifest staging pour %s ; sauvegarde cloud existante préservée.", inst)
			return "", "", "MANIFEST_UPLOAD_FAILED", fmt.Errorf("échec envoi manifest cloud : %w", err)
		}

		promotedBaseID, err := renamer.RenameFile(ctx, stagedBaseID, baseBackupName, existingBaseID)
		if err != nil {
			_ = p.DeleteFile(ctx, stagedBaseID)
			_ = p.DeleteFile(ctx, stagedManifestID)
			return "", "", "UPLOAD_FAILED", fmt.Errorf("échec promotion archive de base : %w", err)
		}

		promotedManifestID, err := renamer.RenameFile(ctx, stagedManifestID, manifestName, existingManifestID)
		if err != nil {
			// Tenter un upload JSON direct de rattrapage avant de déclarer l'échec
			fallbackID, fbErr := p.UploadJSON(ctx, manifestName, newManifest, existingManifestID)
			if fbErr != nil {
				_ = p.DeleteFile(ctx, stagedManifestID)
				ipc.Logf("[upload] AVERTISSEMENT : archive de base promue (%s) mais promotion et rattrapage du manifeste échoués pour %s ; relancez un upload --force pour resynchroniser le manifeste.", promotedBaseID, inst)
				return promotedBaseID, "", "MANIFEST_UPLOAD_FAILED", fmt.Errorf("échec promotion manifest cloud (base promue, manifeste désynchronisé — relancez --upload --force) : %w", err)
			}
			_ = p.DeleteFile(ctx, stagedManifestID)
			promotedManifestID = fallbackID
		}

		return promotedBaseID, promotedManifestID, "", nil
	}

	var newBaseID string
	upBaseOpts := retryOpts
	upBaseOpts.Label = "uploadBase(" + inst + ")"
	err := retry.Do(ctx, upBaseOpts, func() error {
		id, upErr := p.UploadZip(ctx, baseBackupName, tempZip, existingBaseID, func(pct int) {
			ipc.SendProgress("UPLOADING", pct, inst)
		})
		if upErr != nil {
			return upErr
		}
		newBaseID = id
		return nil
	})
	if err != nil {
		return "", "", "UPLOAD_FAILED", err
	}

	var newMID string
	err = retry.Do(ctx, retryOpts, func() error {
		id, upErr := p.UploadJSON(ctx, manifestName, newManifest, existingManifestID)
		if upErr != nil {
			return upErr
		}
		newMID = id
		return nil
	})
	if err != nil {
		ipc.Logf("[upload] AVERTISSEMENT : archive de base envoyée mais échec manifest %s ; anciens deltas conservés par sécurité.", manifestName)
		return newBaseID, "", "MANIFEST_UPLOAD_FAILED", fmt.Errorf("échec envoi manifest cloud : %w", err)
	}
	return newBaseID, newMID, "", nil
}
