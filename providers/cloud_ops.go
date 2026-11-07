package providers

import (
	"context"
	"sync"

	"github.com/WilliamBossard/Gens-Horizon/ipc"
	"github.com/WilliamBossard/Gens-Horizon/retry"
)

// GetCloudIndexAndCleanDuplicates rÃ©cupÃ¨re la liste des fichiers cloud et purge automatiquement les doublons obsolÃ¨tes
func GetCloudIndexAndCleanDuplicates(p Provider, opts retry.Options, logPrefix string) (map[string]CloudFile, error) {
	var cloudFiles []CloudFile
	err := retry.Do(context.Background(), opts, func() error {
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

	// Suppression parallÃ¨le des doublons (concurrence max: 4)
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
				_ = retry.Do(context.Background(), delOpts, func() error {
					return p.DeleteFile(file.ID)
				})
				ipc.Logf("%s Doublon purgÃ© du Cloud : %s (%s)", logPrefix, file.Name, file.ID)
			}(item)
		}
		wg.Wait()
	}

	return cloudIndex, nil
}
