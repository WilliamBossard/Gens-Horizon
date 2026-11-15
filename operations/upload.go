package operations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	cloudIndex, err := providers.GetCloudIndexAndCleanDuplicates(p, retryOpts, "[upload]")
	if err != nil {
		ipc.SendError("CLOUD_ERROR", err.Error(), "")
		return
	}

	instDir := paths.GetInstancesFolder()

	for _, inst := range instances {
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
		newManifest, err := scanner.GenerateManifestWithOptions(localInstFolder, oldLocalManifest, verifyHashes)
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
			err = p.DownloadJSON(manifestFile.ID, &remoteManifest)
			if err == nil {
				hasRemoteManifest = true
			} else {
				ipc.Logf("[upload] Erreur téléchargement remote manifest, fallback full : %v", err)
			}
		}

		var totalDeltasSize int64
		var oldestDeltaTime int64 = time.Now().UnixMilli()
		for _, d := range existingDeltas {
			totalDeltasSize += d.Size
			if d.ModifiedTime.UnixMilli() < oldestDeltaTime {
				oldestDeltaTime = d.ModifiedTime.UnixMilli()
			}
		}

		isDeltaThresholdExceeded := len(existingDeltas) >= cleanupThreshold
		isSizeSignificant := hasBase && baseFile.Size > 0 && totalDeltasSize >= (baseFile.Size/2)
		isDeltaTooOld := len(existingDeltas) > 0 && time.Since(time.UnixMilli(oldestDeltaTime)) > 30*24*time.Hour
		isHardCapReached := len(existingDeltas) >= cleanupThreshold*2

		// Smart Consolidation : consolidation complète si le seuil est atteint ET (taille cumulée > 50% de la base, deltas > 30j, ou plafond de sécurité)
		needsFullBackup := !hasBase || !hasRemoteManifest ||
			(isDeltaThresholdExceeded && (isSizeSignificant || isDeltaTooOld || isHardCapReached))

		if needsFullBackup {
			ipc.Logf("[upload] Création d'une sauvegarde complète pour %s (deltas=%d, tailleDeltas=%d, baseSize=%d, max=%d)", inst, len(existingDeltas), totalDeltasSize, baseFile.Size, cleanupThreshold)
			tempZip := filepath.Join(os.TempDir(), fmt.Sprintf("horizon_full_%s_%d.zip", safeInst, time.Now().UnixMilli()))

			var manifestTotalBytes int64
			for _, entry := range newManifest {
				if s := entry.Size.Int64(); s > 0 {
					manifestTotalBytes += s
				}
			}

			ipc.SendProgress("COMPRESSING", 0, inst)
			err = ziputils.CreateFullZipWithHint(localInstFolder, manifestTotalBytes, tempZip, func(pct int) {
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
			mExistingID := ""
			if hasManifest {
				mExistingID = manifestFile.ID
			}

			newBaseID, newMID, stepErrCode, upErr := uploadFullBackupAtomic(
				p,
				inst,
				safeInst,
				tempZip,
				baseBackupName,
				existingID,
				manifestName,
				mExistingID,
				newManifest,
				retryOpts,
			)
			_ = os.Remove(tempZip)
			if upErr != nil {
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
			metaExistingID := ""
			if mf, ok := cloudIndex[metaName]; ok {
				metaExistingID = mf.ID
			}
			_, _ = p.UploadJSON(metaName, metaData, metaExistingID)
			if b, err := json.MarshalIndent(metaData, "", "  "); err == nil {
				_ = osWriteFileAtomic(paths.GetMetaPath(safeInst), b, 0644)
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
						if delErr := retry.Do(context.Background(), delOpts, func() error {
							return p.DeleteFile(df.ID)
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
				_ = osWriteFileAtomic(localManifestFile, b, 0644)
			}

			serverTime := fetchServerTimeForInstance(p, retryOpts, safeInst, newBaseID, newMID)
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
			tempDelta := filepath.Join(os.TempDir(), fmt.Sprintf("horizon_delta_%s_%d.zip", safeInst, time.Now().UnixMilli()))

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

			// Mise à jour du manifest avec retry (nettoie le delta orphelin si le manifest échoue)
			var newMID string
			err = retry.Do(context.Background(), retryOpts, func() error {
				id, upErr := p.UploadJSON(manifestName, newManifest, manifestFile.ID)
				if upErr != nil {
					return upErr
				}
				newMID = id
				return nil
			})
			if err != nil {
				if deltaID != "" {
					if rbErr := p.DeleteFile(deltaID); rbErr != nil {
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
			metaExistingID := ""
			if mf, ok := cloudIndex[metaName]; ok {
				metaExistingID = mf.ID
			}
			_, _ = p.UploadJSON(metaName, metaData, metaExistingID)
			if b, err := json.MarshalIndent(metaData, "", "  "); err == nil {
				_ = osWriteFileAtomic(paths.GetMetaPath(safeInst), b, 0644)
			}

			// Sauvegarde du manifest en local
			if b, err := json.MarshalIndent(newManifest, "", "  "); err == nil {
				_ = osWriteFileAtomic(localManifestFile, b, 0644)
			}

			serverTime := fetchServerTimeForInstance(p, retryOpts, safeInst, deltaID, newMID)
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
	prefixDelta := PrefixDelta + safeInst + "_"
	for name, f := range cloudIndex {
		if strings.HasPrefix(name, prefixDelta) && strings.HasSuffix(name, ".zip") {
			consider(f.ModifiedTime)
		}
	}
	if maxT.IsZero() {
		return time.Now().UTC()
	}
	return maxT.UTC()
}

func fetchServerTimeForInstance(p providers.Provider, opts retry.Options, safeInst string, fileIDs ...string) time.Time {
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
			if err := retry.Do(context.Background(), fetchOpts, func() error {
				t, gErr := fetcher.GetFileModifiedTime(id)
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
	freshIndex, err := providers.GetCloudIndexAndCleanDuplicates(p, opts, "[upload-ts]")
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
		_ = osWriteFileAtomic(syncInfoPath, b, 0644)
	}
}

func readFileBase64(filePath string) (string, string, error) {
	ext := strings.ToLower(filepath.Ext(filePath))
	var mime string
	switch ext {
	case ".png":
		mime = "image/png"
	case ".jpg", ".jpeg":
		mime = "image/jpeg"
	case ".webp":
		mime = "image/webp"
	case ".gif":
		mime = "image/gif"
	case ".bmp":
		mime = "image/bmp"
	default:
		return "", "", fmt.Errorf("format d'icône non autorisé : %s", ext)
	}
	bytes, err := os.ReadFile(filePath)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(bytes), mime, nil
}

func isPathStrictlyUnder(baseDir, targetPath string) bool {
	absBase, err1 := filepath.Abs(filepath.Clean(baseDir))
	absTarget, err2 := filepath.Abs(filepath.Clean(targetPath))
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(absBase, absTarget)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

var validImageDataURIRe = regexp.MustCompile(`^data:image/(?:png|jpeg|jpg|webp|gif|bmp);base64,[A-Za-z0-9+/=]+$`)
var validSvgDataURIRe = regexp.MustCompile(`^data:image/svg\+xml;(?:charset=[A-Za-z0-9-]+,[A-Za-z0-9%._~:/?#\[\]@!$&()*+,;=-]+|base64,[A-Za-z0-9+/=]+)$`)
var svgDangerousEventRe = regexp.MustCompile(`(?i)(?:^|[\s/"'])on[a-z]+\s*=`)

func isDangerousSVGContent(svg string) bool {
	lower := strings.ToLower(svg)
	if strings.Contains(lower, "<script") ||
		strings.Contains(lower, "<foreignobject") ||
		strings.Contains(lower, "javascript:") ||
		strings.Contains(lower, "vbscript:") ||
		strings.Contains(lower, "data:text/html") {
		return true
	}
	return svgDangerousEventRe.MatchString(svg)
}

func isValidImageDataURI(raw string) bool {
	if len(raw) == 0 || len(raw) > 3*1024*1024 {
		return false
	}
	if validImageDataURIRe.MatchString(raw) {
		return true
	}
	if validSvgDataURIRe.MatchString(raw) {
		if idx := strings.Index(strings.ToLower(raw), ";base64,"); idx != -1 {
			b64Payload := raw[idx+len(";base64,"):]
			decoded, err := base64.StdEncoding.DecodeString(b64Payload)
			if err != nil {
				return false
			}
			return !isDangerousSVGContent(string(decoded))
		}
		lower := strings.ToLower(raw)
		if strings.Contains(lower, "%3cscript") || strings.Contains(lower, "%3cforeignobject") || isDangerousSVGContent(lower) {
			return false
		}
		return true
	}
	return false
}

func extractInstanceMetadata(instFolder string, instName string) map[string]any {
	meta := map[string]any{
		"realName": instName,
		"loader":   "vanilla",
		"iconData": nil,
	}

	var iconField string
	var loaderField string
	var nameField string

	// 1. Lire instance.json ou fallback gens/launcher_config.json
	instJsonPath := filepath.Join(instFolder, "instance.json")
	var data []byte
	var err error
	if data, err = os.ReadFile(instJsonPath); err != nil {
		configPath := filepath.Join(instFolder, "gens", "launcher_config.json")
		data, err = os.ReadFile(configPath)
	}

	if err == nil {
		var instData struct {
			Name     string `json:"name"`
			Loader   string `json:"loader"`
			Icon     string `json:"icon"`
			IconData string `json:"iconData"`
		}
		if err := json.Unmarshal(data, &instData); err == nil {
			nameField = instData.Name
			loaderField = instData.Loader
			if instData.Icon != "" {
				iconField = instData.Icon
			} else if instData.IconData != "" {
				iconField = instData.IconData
			}
		}
	}

	if nameField != "" {
		meta["realName"] = nameField
	}
	if loaderField != "" {
		meta["loader"] = loaderField
	}

	// 2. Traitement de l'icône personnalisée
	var finalIconData string

	if isValidImageDataURI(iconField) {
		finalIconData = iconField
	} else if strings.HasPrefix(iconField, "file://") {
		// Nettoyage de l'URL file:// (confinée strictement au dossier de l'instance)
		filePath := strings.TrimPrefix(iconField, "file://")
		if strings.HasPrefix(filePath, "/") && len(filePath) > 2 && filePath[2] == ':' {
			filePath = filePath[1:] // Pour Windows : /C:/path -> C:/path
		}
		filePath = filepath.FromSlash(filePath)
		if isPathStrictlyUnder(instFolder, filePath) {
			if info, err := os.Stat(filePath); err == nil && !info.IsDir() && info.Size() < 2*1024*1024 {
				if b64, mime, err := readFileBase64(filePath); err == nil {
					finalIconData = fmt.Sprintf("data:%s;base64,%s", mime, b64)
				}
			}
		}
	}

	// 3. Fallback : scanner les fichiers d'icônes physiques dans instFolder
	if finalIconData == "" {
		exts := []string{".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp"}
		for _, ext := range exts {
			candidate := filepath.Join(instFolder, "icon"+ext)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Size() < 2*1024*1024 {
				if b64, mime, err := readFileBase64(candidate); err == nil {
					finalIconData = fmt.Sprintf("data:%s;base64,%s", mime, b64)
					break
				}
			}
		}
	}

	if finalIconData != "" {
		meta["iconData"] = finalIconData
	}

	return meta
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
	renamer, canStage := p.(providers.FileRenamer)
	useStaging := canStage && existingBaseID != ""

	if useStaging {
		stamp := time.Now().UnixMilli()
		stagingBackupName := fmt.Sprintf("GensHorizon_Staging_%s_backup_%d.zip", safeInst, stamp)
		stagingManifestName := fmt.Sprintf("GensHorizon_Staging_%s_manifest_%d.json", safeInst, stamp)

		stagedBaseID, err := p.UploadZip(stagingBackupName, tempZip, "", func(pct int) {
			ipc.SendProgress("UPLOADING", pct, inst)
		})
		if err != nil {
			return "", "", "UPLOAD_FAILED", err
		}

		var stagedManifestID string
		mOpts := retryOpts
		mOpts.Label = "uploadStagingManifest(" + inst + ")"
		err = retry.Do(context.Background(), mOpts, func() error {
			id, upErr := p.UploadJSON(stagingManifestName, newManifest, "")
			if upErr != nil {
				return upErr
			}
			stagedManifestID = id
			return nil
		})
		if err != nil {
			_ = p.DeleteFile(stagedBaseID)
			ipc.Logf("[upload] Échec envoi manifest staging pour %s ; sauvegarde cloud existante préservée.", inst)
			return "", "", "MANIFEST_UPLOAD_FAILED", fmt.Errorf("échec envoi manifest cloud : %w", err)
		}

		promotedBaseID, err := renamer.RenameFile(stagedBaseID, baseBackupName, existingBaseID)
		if err != nil {
			_ = p.DeleteFile(stagedBaseID)
			_ = p.DeleteFile(stagedManifestID)
			return "", "", "UPLOAD_FAILED", fmt.Errorf("échec promotion archive de base : %w", err)
		}

		promotedManifestID, err := renamer.RenameFile(stagedManifestID, manifestName, existingManifestID)
		if err != nil {
			// Tenter un upload JSON direct de rattrapage avant de déclarer l'échec
			fallbackID, fbErr := p.UploadJSON(manifestName, newManifest, existingManifestID)
			if fbErr != nil {
				return "", "", "MANIFEST_UPLOAD_FAILED", fmt.Errorf("échec promotion manifest cloud : %w", err)
			}
			_ = p.DeleteFile(stagedManifestID)
			promotedManifestID = fallbackID
		}

		return promotedBaseID, promotedManifestID, "", nil
	}

	newBaseID, err := p.UploadZip(baseBackupName, tempZip, existingBaseID, func(pct int) {
		ipc.SendProgress("UPLOADING", pct, inst)
	})
	if err != nil {
		return "", "", "UPLOAD_FAILED", err
	}

	var newMID string
	err = retry.Do(context.Background(), retryOpts, func() error {
		id, upErr := p.UploadJSON(manifestName, newManifest, existingManifestID)
		if upErr != nil {
			return upErr
		}
		newMID = id
		return nil
	})
	if err != nil {
		ipc.Logf("[upload] AVERTISSEMENT : archive de base envoyée mais échec manifest %s ; anciens deltas conservés par sécurité.", manifestName)
		return "", "", "MANIFEST_UPLOAD_FAILED", fmt.Errorf("échec envoi manifest cloud : %w", err)
	}
	return newBaseID, newMID, "", nil
}
