package operations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/ipc"
	"github.com/WilliamBossard/Gens-Horizon/lock"
	"github.com/WilliamBossard/Gens-Horizon/paths"
	"github.com/WilliamBossard/Gens-Horizon/providers"
	"github.com/WilliamBossard/Gens-Horizon/retry"
)

type CleanItem struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Reason string `json:"reason"`
}

// Clean analyse le Cloud et purge les deltas obsolètes (post-consolidation), métadonnées orphelines et résidus
func Clean(args []string) {
	CleanWithContext(context.Background(), args)
}

// CleanWithContext exécute le nettoyage Cloud en respectant l'annulation par contexte.
func CleanWithContext(ctx context.Context, args []string) {
	if ctx == nil {
		ctx = context.Background()
	}
	apply := hasFlag(args, "--apply")

	if apply {
		if !lock.AcquireLock() {
			ipc.SendError("ERR_ALREADY_RUNNING", "ERR_ALREADY_RUNNING", "")
			os.Exit(1)
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

	retryOpts := retry.FromSettings(settings.MaxRetries, settings.RetryBaseDelay, "[clean]")

	providers.InvalidateCloudIndexCache()
	cloudIndex, err := providers.GetCloudIndexAndCleanDuplicatesWithContext(ctx, p, retryOpts, "[clean]")
	if err != nil {
		ipc.SendError("CLOUD_ERROR", err.Error(), "")
		return
	}

	itemsToClean, totalFreed := collectCleanItems(cloudIndex)

	formattedFreed := formatByteSize(totalFreed)

	// Mode PREVIEW (dry-run)
	if !apply {
		ipc.Send(map[string]any{
			"type":           "CLEAN_PREVIEW",
			"items":          itemsToClean,
			"count":          len(itemsToClean),
			"freedBytes":     totalFreed,
			"freedFormatted": formattedFreed,
		})
		return
	}

	// Mode APPLY : suppression effective des fichiers avec retry et comptage exact
	var deletedCount int
	var deletedBytes int64
	var failedCount int
	for _, item := range itemsToClean {
		if ctx.Err() != nil {
			ipc.Logf("[clean] Nettoyage interrompu par arrêt gracieux : %d/%d supprimé(s).", deletedCount, len(itemsToClean))
			break
		}
		delOpts := retryOpts
		delOpts.Label = "clean(" + item.Name + ")"
		delErr := retry.Do(ctx, delOpts, func() error {
			err := p.DeleteFile(item.ID)
			if errors.Is(err, providers.ErrNotFound) {
				return nil
			}
			return err
		})
		if delErr == nil {
			deletedCount++
			deletedBytes += item.Size
		} else {
			failedCount++
			ipc.Logf("[clean] Échec suppression fichier %s (%s) : %v", item.Name, item.ID, delErr)
		}
	}

	providers.InvalidateCloudIndexCache()
	formattedDeleted := formatByteSize(deletedBytes)

	ipc.Send(map[string]any{
		"type":           "CLEAN_COMPLETE",
		"items":          itemsToClean,
		"count":          deletedCount,
		"failedCount":    failedCount,
		"freedBytes":     deletedBytes,
		"freedFormatted": formattedDeleted,
		"message":        fmt.Sprintf("Nettoyage terminé : %d fichier(s) supprimé(s), %s libéré(s).", deletedCount, formattedDeleted),
	})
}

func formatByteSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d o", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %co", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func collectCleanItems(cloudIndex map[string]providers.CloudFile) ([]CleanItem, int64) {
	baseFiles := make(map[string]providers.CloudFile)
	for name, file := range cloudIndex {
		if strings.HasPrefix(name, PrefixBackup) && strings.HasSuffix(name, ".zip") {
			slug := strings.TrimSuffix(strings.TrimPrefix(name, PrefixBackup), ".zip")
			baseFiles[slug] = file
		}
	}

	var itemsToClean []CleanItem
	var totalFreed int64

	activeSlugs := make(map[string]bool)
	for slug := range baseFiles {
		activeSlugs[slug] = true
	}

	stagingGraceCutoff := time.Now().Add(-15 * time.Minute)

	for name, file := range cloudIndex {
		// A. Deltas
		if slug, deltaTs, ok := ParseDeltaFileName(name); ok {
			base, hasBase := baseFiles[slug]
			if !hasBase {
				itemsToClean = append(itemsToClean, CleanItem{
					ID:     file.ID,
					Name:   file.Name,
					Size:   file.Size,
					Reason: "Delta orphelin (aucune archive de base correspondante)",
				})
				totalFreed += file.Size
			} else {
				// Comparer les horodatages serveur (ModifiedTime) pour éviter la dérive d'horloge client
				isObsolete := false
				if !file.ModifiedTime.IsZero() && !base.ModifiedTime.IsZero() {
					isObsolete = file.ModifiedTime.Before(base.ModifiedTime)
				} else {
					isObsolete = deltaTs < base.ModifiedTime.UnixMilli()
				}
				if isObsolete {
					itemsToClean = append(itemsToClean, CleanItem{
						ID:     file.ID,
						Name:   file.Name,
						Size:   file.Size,
						Reason: "Delta obsolète (déjà consolidé dans l'archive de base)",
					})
					totalFreed += file.Size
				} else {
					activeSlugs[slug] = true
				}
			}
		}

		// B. Fichiers temporaires, staging interrompus ou corrompus
		// Fenêtre de grâce de 15 minutes sur GensHorizon_Staging_* pour ne pas supprimer un upload actif d'un autre PC.
		if strings.HasPrefix(name, "GensHorizon_Staging_") || strings.HasPrefix(name, ".__staging_bak_") || strings.HasSuffix(name, ".tmp") || strings.HasSuffix(name, ".part") || strings.HasPrefix(name, "tmp_") {
			if strings.HasPrefix(name, "GensHorizon_Staging_") && !file.ModifiedTime.IsZero() && file.ModifiedTime.After(stagingGraceCutoff) {
				continue
			}
			reason := "Fichier temporaire résiduel"
			if strings.HasPrefix(name, "GensHorizon_Staging_") {
				reason = "Fichier staging résiduel (upload interrompu)"
			}
			itemsToClean = append(itemsToClean, CleanItem{
				ID:     file.ID,
				Name:   file.Name,
				Size:   file.Size,
				Reason: reason,
			})
			totalFreed += file.Size
		}
	}

	// C. Métadonnées ou manifestes orphelins (instance sans base ni delta actif)
	for name, file := range cloudIndex {
		if strings.HasPrefix(name, PrefixMeta) && strings.HasSuffix(name, ".json") {
			slug := strings.TrimSuffix(strings.TrimPrefix(name, PrefixMeta), ".json")
			if !activeSlugs[slug] {
				itemsToClean = append(itemsToClean, CleanItem{
					ID:     file.ID,
					Name:   file.Name,
					Size:   file.Size,
					Reason: "Métadonnées orphelines (aucune archive de jeu restante)",
				})
				totalFreed += file.Size
			}
		}
		if strings.HasPrefix(name, PrefixManifest) && strings.HasSuffix(name, ".json") {
			slug := strings.TrimSuffix(strings.TrimPrefix(name, PrefixManifest), ".json")
			if !activeSlugs[slug] {
				itemsToClean = append(itemsToClean, CleanItem{
					ID:     file.ID,
					Name:   file.Name,
					Size:   file.Size,
					Reason: "Manifeste orphelin (aucune archive de jeu restante)",
				})
				totalFreed += file.Size
			}
		}
	}

	return itemsToClean, totalFreed
}
