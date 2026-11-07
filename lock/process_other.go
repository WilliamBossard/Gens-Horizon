//go:build !windows

package lock

import (
	"syscall"
)

func isProcessAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	if err == nil || err == syscall.EPERM {
		return true
	}
	return false
}

