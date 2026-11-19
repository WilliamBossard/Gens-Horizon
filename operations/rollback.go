package operations

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

	safeInst, nameErr := paths.SanitizeInstanceName(paths.GetCanonicalName(targetInst))
	if nameErr != nil {
		ipc.SendError("INVALID_INSTANCE_NAME", nameErr.Error(), targetInst)
		return
	}

	if !lock.AcquireLock() {
		ipc.SendError("ERR_ALREADY_RUNNING", "ERR_ALREADY_RUNNING", targetInst)
		return
	}
	defer lock.ReleaseLock()
	paths.CleanupOrphanedTemps(paths.GetHorizonDataDir())

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

	targetPath, pathErr := paths.SafeJoinUnder(instDir, safeInst)
	if pathErr != nil {
		ipc.SendError("ROLLBACK_ERROR", pathErr.Error(), targetInst)
		return
	}
	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		if alt, altErr := paths.SafeJoinUnder(instDir, targetInst); altErr == nil {
			targetPath = alt
		}
	}

	// Restauration atomique via dossier temporaire pour éviter toute perte si la copie échoue
	if err := swapRestoreSnapshot(bestFolder, targetPath); err != nil {
		ipc.SendError("RESTORE_FAILED", fmt.Sprintf("Échec de restauration du rollback : %v", err), targetInst)
		return
	}

	// Régénération du manifest local
	newManifest, _ := scanner.GenerateManifest(targetPath, nil)
	localManifestFile := paths.GetManifestPath(safeInst)
	if b, err := json.MarshalIndent(newManifest, "", "  "); err == nil {
		_ = osWriteFileAtomic(localManifestFile, b, 0644)
	}

	pruneOldRollbacks(instDir, 3, prefix1, prefix2)

	ipc.Send(ipc.BaseMessage{
		Type:      "SUCCESS",
		Instance:  targetInst,
		Message:   "Rollback completed successfully.",
		ErrorCode: "ROLLBACK_SUCCESS",
	})
}

// pruneOldRollbacks conserve au maximum maxKeep snapshots récents pour les préfixes donnés.
func pruneOldRollbacks(instDir string, maxKeep int, prefixes ...string) {
	if maxKeep <= 0 {
		return
	}
	entries, err := os.ReadDir(instDir)
	if err != nil {
		return
	}
	type rbEntry struct {
		path string
		ts   int64
	}
	var list []rbEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		matched := false
		for _, p := range prefixes {
			if p != "" && strings.HasPrefix(name, p) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		idx := strings.LastIndex(name, "_rollback_")
		if idx == -1 {
			continue
		}
		if ts, err := strconv.ParseInt(name[idx+len("_rollback_"):], 10, 64); err == nil {
			list = append(list, rbEntry{path: filepath.Join(instDir, name), ts: ts})
		}
	}
	if len(list) <= maxKeep {
		return
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].ts > list[j].ts
	})
	for _, old := range list[maxKeep:] {
		_ = os.RemoveAll(old.path)
	}
}

