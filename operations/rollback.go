package operations

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/ipc"
	"github.com/WilliamBossard/Gens-Horizon/lock"
	"github.com/WilliamBossard/Gens-Horizon/paths"
	"github.com/WilliamBossard/Gens-Horizon/scanner"
)

func Rollback(args []string) {
	targetInst := parseTargetInstance(args)
	instDir := paths.GetInstancesFolder()

	if targetInst == "" {
		entries, err := os.ReadDir(instDir)
		if err != nil {
			ipc.SendInfo("Aucun dossier d'instances trouvé.")
			return
		}

		var rollbacks []ipc.RollbackItem
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if idx := strings.LastIndex(name, "_rollback_"); idx != -1 {
				instName := name[:idx]
				rawTs := name[idx+len("_rollback_"):]
				var tsStr *string
				if ts, err := strconv.ParseInt(rawTs, 10, 64); err == nil {
					s := time.UnixMilli(ts).Format(time.RFC3339)
					tsStr = &s
				}
				rollbacks = append(rollbacks, ipc.RollbackItem{
					Folder:    name,
					Instance:  instName,
					Timestamp: tsStr,
				})
			}
		}

		ipc.Send(ipc.RollbackListMessage{
			Type: "ROLLBACK_LIST",
			Data: rollbacks,
		})
		return
	}

	safeInst := paths.GetCanonicalName(targetInst)
	entries, err := os.ReadDir(instDir)
	if err != nil {
		ipc.SendError("ROLLBACK_ERROR", err.Error(), targetInst)
		return
	}

	var bestFolder string
	var bestTime int64

	prefix1 := safeInst + "_rollback_"
	prefix2 := targetInst + "_rollback_"

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, prefix1) || strings.HasPrefix(name, prefix2) {
			idx := strings.LastIndex(name, "_rollback_")
			if ts, err := strconv.ParseInt(name[idx+len("_rollback_"):], 10, 64); err == nil {
				if ts > bestTime {
					bestTime = ts
					bestFolder = filepath.Join(instDir, name)
				}
			}
		}
	}

	if bestFolder == "" {
		ipc.SendError("NO_ROLLBACK", "Aucune sauvegarde rollback disponible pour cette instance.", targetInst)
		return
	}

	if !lock.AcquireLock() {
		ipc.SendError("ERR_ALREADY_RUNNING", "ERR_ALREADY_RUNNING", targetInst)
		return
	}
	defer lock.ReleaseLock()

	targetPath := filepath.Join(instDir, safeInst)
	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		targetPath = filepath.Join(instDir, targetInst)
	}

	// Restauration : vider le dossier actuel et recopier depuis le rollback
	_ = os.RemoveAll(targetPath)
	if err := copyDir(bestFolder, targetPath); err != nil {
		ipc.SendError("RESTORE_FAILED", fmt.Sprintf("Échec de restauration du rollback : %v", err), targetInst)
		return
	}

	// Régénération du manifest local
	newManifest, _ := scanner.GenerateManifest(targetPath, nil)
	localManifestFile := paths.GetManifestPath(safeInst)
	if b, err := json.MarshalIndent(newManifest, "", "  "); err == nil {
		_ = osWriteFileAtomic(localManifestFile, b, 0644)
	}

	ipc.Send(ipc.BaseMessage{
		Type:      "SUCCESS",
		Instance:  targetInst,
		Message:   "Rollback completed successfully.",
		ErrorCode: "ROLLBACK_SUCCESS",
	})
}

