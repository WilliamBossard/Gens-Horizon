package providers

import (
	"context"
	"sync"

	"github.com/WilliamBossard/Gens-Horizon/ipc"
	"github.com/WilliamBossard/Gens-Horizon/retry"
)

// GetCloudIndexReadOnlyWithContext récupère l'index des fichiers cloud sans supprimer de doublons ni modifier le cache disque (sûr sans horizon.lock).
func GetCloudIndexReadOnlyWithContext(ctx context.Context, p Provider, opts retry.Options, logPrefix string) (map[string]CloudFile, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cachedFiles, ok := GetCleanedCloudIndex(p.Name(), DefaultCloudIndexTTL); ok {
		cloudIndex := make(map[string]CloudFile, len(cachedFiles))
		for _, f := range cachedFiles {
			cloudIndex[f.Name] = f
		}
		return cloudIndex, nil
	}

	var cloudFiles []CloudFile
	err := retry.Do(ctx, opts, func() error {
		files, err := p.ListFiles("GensHorizon_")
		if err != nil {
			return err
		}
		cloudFiles = files
		return nil
	})
	if err != nil {
		return nil, err
	}

	cloudIndex := make(map[string]CloudFile, len(cloudFiles))
	for _, f := range cloudFiles {
		existing, ok := cloudIndex[f.Name]
		if !ok {
			cloudIndex[f.Name] = f
			continue
		}
		if f.ModifiedTime.After(existing.ModifiedTime) || (f.ModifiedTime.Equal(existing.ModifiedTime) && f.Size > existing.Size) {
			cloudIndex[f.Name] = f
		}
	}
	return cloudIndex, nil
}

// GetCloudIndexAndCleanDuplicates récupère la liste des fichiers cloud (avec cache éphémère 45s) et purge automatiquement les doublons obsolètes
func GetCloudIndexAndCleanDuplicates(p Provider, opts retry.Options, logPrefix string) (map[string]CloudFile, error) {
	return GetCloudIndexAndCleanDuplicatesWithContext(context.Background(), p, opts, logPrefix)
}

// GetCloudIndexAndCleanDuplicatesWithContext respecte le contexte d'annulation lors de l'indexation et de la purge des doublons.
func GetCloudIndexAndCleanDuplicatesWithContext(ctx context.Context, p Provider, opts retry.Options, logPrefix string) (map[string]CloudFile, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cachedFiles, ok := GetCleanedCloudIndex(p.Name(), DefaultCloudIndexTTL); ok {
		cloudIndex := make(map[string]CloudFile, len(cachedFiles))
		for _, f := range cachedFiles {
			cloudIndex[f.Name] = f
		}
		return cloudIndex, nil
	}

	var cloudFiles []CloudFile
	err := retry.Do(ctx, opts, func() error {
		files, err := p.ListFiles("GensHorizon_")
		if err != nil {
			return err
		}
		cloudFiles = files
		return nil
	})
	if err != nil {
		return nil, err
	}

	cloudIndex := make(map[string]CloudFile)
	var deleteQueue []CloudFile

	for _, f := range cloudFiles {
		existing, ok := cloudIndex[f.Name]
		if !ok {
			cloudIndex[f.Name] = f
		} else if existing.ID == f.ID {
			continue
		} else {
			fTime := f.ModifiedTime
			existTime := existing.ModifiedTime

			isFNewer := fTime.After(existTime) || (fTime.Equal(existTime) && f.Size > existing.Size)
			toKeep := existing
			toDelete := f
			if isFNewer {
				toKeep = f
				toDelete = existing
			}
			cloudIndex[f.Name] = toKeep
			deleteQueue = append(deleteQueue, toDelete)
		}
	}

	// Suppression parallèle des doublons (concurrence max: 4)
	if len(deleteQueue) > 0 {
		var wg sync.WaitGroup
		sem := make(chan struct{}, 4)
		for _, item := range deleteQueue {
			wg.Add(1)
			sem <- struct{}{}
			go func(file CloudFile) {
				defer wg.Done()
				defer func() { <-sem }()
				delOpts := opts
				delOpts.Label = "deleteDuplicate(" + file.Name + ")"
				if delErr := retry.Do(ctx, delOpts, func() error {
					return p.DeleteFile(file.ID)
				}); delErr != nil {
					ipc.Logf("%s Échec purge doublon Cloud %s (%s) : %v", logPrefix, file.Name, file.ID, delErr)
				} else {
					ipc.Logf("%s Doublon purgé du Cloud : %s (%s)", logPrefix, file.Name, file.ID)
				}
			}(item)
		}
		wg.Wait()
	}

	// Enregistrement dans le cache éphémère (fichiers dédupliqués)
	cleanList := make([]CloudFile, 0, len(cloudIndex))
	for _, f := range cloudIndex {
		cleanList = append(cleanList, f)
	}
	SaveCloudIndexCache(p.Name(), cleanList, true)

	return cloudIndex, nil
}
