//go:build !windows

package lock

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
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
		out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
		if err != nil {
			return true
		}
		comm := strings.ToLower(strings.TrimSpace(filepath.Base(string(out))))
		if comm == "" {
			return false
		}
		if strings.Contains(comm, "horizon") {
			return true
		}
		if selfExe, sErr := os.Executable(); sErr == nil {
			if comm == strings.ToLower(filepath.Base(selfExe)) {
				return true
			}
		}
		return false
	}
	if runtime.GOOS != "linux" {
		return true
	}
	commBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return true
	}
	comm := strings.ToLower(strings.TrimSpace(string(commBytes)))
	if strings.Contains(comm, "horizon") {
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

