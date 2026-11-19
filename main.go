package main

import (
	"bufio"
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/config"
	"github.com/WilliamBossard/Gens-Horizon/ipc"
	"github.com/WilliamBossard/Gens-Horizon/lock"
	"github.com/WilliamBossard/Gens-Horizon/operations"
	"github.com/WilliamBossard/Gens-Horizon/paths"
	"github.com/WilliamBossard/Gens-Horizon/providers"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	providers.SetBaseContext(ctx)

	// Écoute de l'arrêt gracieux demandé par le Launcher via stdin
	go func() {
		stdinScanner := bufio.NewScanner(os.Stdin)
		for stdinScanner.Scan() {
			text := strings.TrimSpace(stdinScanner.Text())
			if text == "SHUTDOWN" {
				ipc.Logf("[Horizon] Arrêt gracieux (SHUTDOWN) reçu sur stdin.")
				cancel()
				time.AfterFunc(3*time.Second, func() {
					ipc.Logf("[Horizon] Délai d'arrêt gracieux dépassé, libération du verrou et sortie forcée.")
					lock.ReleaseLock()
					os.Exit(130)
				})
				return
			}
		}
		if err := stdinScanner.Err(); err != nil {
			ipc.Logf("[Horizon] Erreur lecture stdin : %v", err)
		}
	}()

	// Initialisation des settings par défaut si absents
	_ = paths.LoadSettings()

	args := os.Args[1:]

	if len(args) == 0 {
		ipc.Send(ipc.BaseMessage{
			Type:    "ERROR",
			Message: "Missing command. Available commands: --login, --check, --sync, --upload, --quota, --rollback, --clean, --delete, --status, --help, --version",
		})
		return
	}

	if containsCommand(args, "--help", "-h") {
		ipc.Send(map[string]any{
			"type":       "HELP",
			"appVersion": config.AppVersion,
			"usage":      "Horizon <command> [instance] [flags]",
			"commands": []map[string]string{
				{"command": "--login", "description": "Authenticate with the configured cloud provider"},
				{"command": "--check", "description": "Check for remote cloud updates"},
				{"command": "--sync", "description": "Synchronize cloud instances to local storage"},
				{"command": "--upload", "description": "Upload local instance changes to the cloud"},
				{"command": "--quota", "description": "Retrieve cloud storage quota and per-instance usage"},
				{"command": "--rollback", "description": "List or restore local rollback snapshots"},
				{"command": "--clean", "description": "Clean obsolete cloud deltas and temporary artifacts"},
				{"command": "--delete", "description": "Delete a remote cloud backup"},
				{"command": "--status", "description": "Display local configuration, lock, and authentication status"},
				{"command": "--version", "description": "Display the Horizon module version"},
				{"command": "--help", "description": "Display available CLI commands"},
			},
		})
	} else if containsCommand(args, "--status", "status") {
		operations.Status(args)
	} else if containsCommand(args, "--login", "login") {
		operations.LoginWithContext(ctx, args)
	} else if containsCommand(args, "--check", "check") {
		operations.CheckWithContext(ctx, args)
	} else if containsCommand(args, "--sync", "sync") {
		operations.SyncWithContext(ctx, args)
	} else if containsCommand(args, "--upload", "upload") {
		operations.UploadWithContext(ctx, args)
	} else if containsCommand(args, "--quota", "quota") {
		operations.QuotaWithContext(ctx, args)
	} else if containsCommand(args, "--rollback", "rollback") {
		operations.Rollback(args)
	} else if containsCommand(args, "--clean", "clean") {
		operations.CleanWithContext(ctx, args)
	} else if containsCommand(args, "--delete", "delete") {
		operations.SyncWithContext(ctx, args)
	} else if containsCommand(args, "--version", "-v") {
		ipc.Send(map[string]any{
			"type":    "INFO",
			"version": config.AppVersion,
		})
	} else {
		ipc.Send(ipc.BaseMessage{
			Type:    "ERROR",
			Message: "Unknown command. Available commands: --login, --check, --sync, --upload, --quota, --rollback, --clean, --delete, --status, --help, --version",
		})
	}

	if ctx.Err() != nil {
		os.Exit(130)
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

