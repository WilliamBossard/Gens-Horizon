package main

import (
	"bufio"
	"os"
	"strings"

	"github.com/WilliamBossard/Gens-Horizon/config"
	"github.com/WilliamBossard/Gens-Horizon/ipc"
	"github.com/WilliamBossard/Gens-Horizon/lock"
	"github.com/WilliamBossard/Gens-Horizon/operations"
	"github.com/WilliamBossard/Gens-Horizon/paths"
)

func main() {
	// Écoute de l'arrêt gracieux demandé par le Launcher via stdin
	go func() {
		stdinScanner := bufio.NewScanner(os.Stdin)
		for stdinScanner.Scan() {
			text := strings.TrimSpace(stdinScanner.Text())
			if text == "SHUTDOWN" {
				ipc.Logf("[Horizon] Arrêt gracieux (SHUTDOWN) reçu sur stdin.")
				lock.ReleaseLock()
				os.Exit(0)
			}
		}
	}()

	// Nettoyage automatique des fichiers temporaires orphelins (> 10 min)
	paths.CleanupOrphanedTemps(paths.GetHorizonDataDir())

	// Initialisation des settings par défaut si absents
	_ = paths.LoadSettings()

	args := os.Args[1:]

	if len(args) == 0 {
		ipc.Send(ipc.BaseMessage{
			Type:    "ERROR",
			Message: "Commande manquante. Utiliser : --login, --check, --sync, --upload, --quota, --rollback, --delete",
		})
		return
	}

	if containsCommand(args, "--login", "login") {
		operations.Login(args)
	} else if containsCommand(args, "--check", "check") {
		operations.Check(args)
	} else if containsCommand(args, "--sync", "sync") {
		operations.Sync(args)
	} else if containsCommand(args, "--upload", "upload") {
		operations.Upload(args)
	} else if containsCommand(args, "--quota", "quota") {
		operations.Quota(args)
	} else if containsCommand(args, "--rollback", "rollback") {
		operations.Rollback(args)
	} else if containsCommand(args, "--delete", "delete") {
		operations.Sync(args)
	} else if containsCommand(args, "--version", "-v") {
		ipc.Send(map[string]any{
			"type":    "INFO",
			"version": config.AppVersion,
		})
	} else {
		ipc.Send(ipc.BaseMessage{
			Type:    "ERROR",
			Message: "Commande manquante ou non reconnue. Utiliser : --login, --check, --sync, --upload, --quota, --rollback, --delete",
		})
	}
}

func containsCommand(args []string, flags ...string) bool {
	for _, arg := range args {
		for _, f := range flags {
			if arg == f || strings.HasPrefix(arg, f+"=") {
				return true
			}
		}
	}
	return false
}

