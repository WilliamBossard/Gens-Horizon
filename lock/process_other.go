//go:build !windows

package lock

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if runtime.GOOS == "linux" {
		if statBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
			s := string(statBytes)
			if rparen := strings.LastIndexByte(s, ')'); rparen != -1 && len(s) > rparen+2 {
				if s[rparen+2] == 'Z' {
					return false
				}
			}
		}
	}
	err := syscall.Kill(pid, 0)
	if err == nil || err == syscall.EPERM {
		return true
	}
	return false
}

func isHorizonProcess(pid int) bool {
	if pid <= 0 {
		return false
	}
	if runtime.GOOS == "darwin" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
		if err != nil {
			// Si le processus est mort entre-temps, isProcessAlive le confirme ; ne pas bloquer sur un PID mort.
			return isProcessAlive(pid)
		}
		comm := strings.ToLower(strings.TrimSpace(filepath.Base(string(out))))
		if comm == "" {
			return false
		}
		if selfExe, sErr := os.Executable(); sErr == nil {
			if comm == strings.ToLower(filepath.Base(selfExe)) {
				return true
			}
		}
		return comm == "horizon" || strings.HasPrefix(comm, "horizon-") || strings.HasPrefix(comm, "gens-horizon")
	}
	if runtime.GOOS != "linux" {
		return true
	}
	commBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return isProcessAlive(pid)
	}
	comm := strings.ToLower(strings.TrimSpace(string(commBytes)))
	if comm == "horizon" || strings.HasPrefix(comm, "horizon-") || strings.HasPrefix(comm, "gens-horizon") {
		return true
	}
	if selfExe, sErr := os.Executable(); sErr == nil {
		selfName := strings.ToLower(filepath.Base(selfExe))
		if comm == selfName || (len(selfName) > 15 && comm == selfName[:15]) {
			return true
		}
	}
	return false
}
