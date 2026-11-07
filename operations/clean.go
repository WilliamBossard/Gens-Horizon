package operations

import (
	"fmt"
	"os"
	"strconv"
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
	apply := hasFlag(args, "--apply")

	if apply {
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
		Label:      "[clean]",
	}

	cloudIndex, err := providers.GetCloudIndexAndCleanDuplicates(p, retryOpts, "[clean]")
	if err != nil {
		ipc.SendError("CLOUD_ERROR", err.Error(), "")
		return
	}

	// 1. Répertoire des bases existantes et horodatages
	baseFiles := make(map[string]providers.CloudFile) // instanceSlug -> CloudFile
	for name, file := range cloudIndex {
		if strings.HasPrefix(name, PrefixBackup) && strings.HasSuffix(name, ".zip") {
			slug := strings.TrimSuffix(strings.TrimPrefix(name, PrefixBackup), ".zip")
			baseFiles[slug] = file
		}
	}

	// 2. Détection des éléments à nettoyer
	var itemsToClean []CleanItem
	var totalFreed int64

	// Instances ayant au moins un fichier valide (base ou delta valide)
	activeSlugs := make(map[string]bool)
	for slug := range baseFiles {
		activeSlugs[slug] = true
	}

	for name, file := range cloudIndex {
		// A. Deltas
		if strings.HasPrefix(name, PrefixDelta) && strings.HasSuffix(name, ".zip") {
			stripped := strings.TrimSuffix(strings.TrimPrefix(name, PrefixDelta), ".zip")
			idx := strings.LastIndex(stripped, "_")
			if idx > 0 {
				slug := stripped[:idx]
				rawTs := stripped[idx+1:]
				deltaTs, parseErr := strconv.ParseInt(rawTs, 10, 64)

				base, hasBase := baseFiles[slug]
				if !hasBase {
					// Delta sans archive de base : delta orphelin
					itemsToClean = append(itemsToClean, CleanItem{
						ID:     file.ID,
						Name:   file.Name,
						Size:   file.Size,
						Reason: "Delta orphelin (aucune archive de base correspondante)",
					})
					totalFreed += file.Size
				} else if parseErr == nil {
					// Si le delta a un timestamp antérieur à la base, il a déjà été consolidé
					baseTs := base.ModifiedTime.UnixMilli()
					if deltaTs < baseTs {
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
		}

		// B. Fichiers temporaires ou corrompus
		if strings.HasSuffix(name, ".tmp") || strings.HasSuffix(name, ".part") || strings.HasPrefix(name, "tmp_") {
			itemsToClean = append(itemsToClean, CleanItem{
				ID:     file.ID,
				Name:   file.Name,
				Size:   file.Size,
				Reason: "Fichier temporaire résiduel",
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

	// Mode APPLY : suppression effective des fichiers
	var deletedCount int
	for _, item := range itemsToClean {
		if err := p.DeleteFile(item.ID); err == nil {
			deletedCount++
		} else {
			ipc.Logf("[clean] Échec suppression fichier %s (%s) : %v", item.Name, item.ID, err)
		}
	}

	providers.InvalidateCloudIndexCache()

	ipc.Send(map[string]any{
		"type":           "CLEAN_COMPLETE",
		"items":          itemsToClean,
		"count":          deletedCount,
		"freedBytes":     totalFreed,
		"freedFormatted": formattedFreed,
		"message":        fmt.Sprintf("Nettoyage terminé : %d fichier(s) supprimé(s), %s libéré(s).", deletedCount, formattedFreed),
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
