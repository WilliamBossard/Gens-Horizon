//go:build windows

package lock

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259
)

var (
	modKernel32                   = syscall.NewLazyDLL("kernel32.dll")
	procQueryFullProcessImageName = modKernel32.NewProc("QueryFullProcessImageNameW")
)

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)

	var exitCode uint32
	if err := syscall.GetExitCodeProcess(h, &exitCode); err != nil {
		return false
	}
	return exitCode == stillActive
}

func isHorizonProcess(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		// En cas d'accès refusé sur un processus actif, conserver le verrou par prudence
		return true
	}
	defer syscall.CloseHandle(h)

	buf := make([]uint16, 1024)
	size := uint32(len(buf))
	r, _, _ := procQueryFullProcessImageName.Call(
		uintptr(h),
		0,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if r == 0 || size == 0 {
		return true
	}
	exeName := strings.ToLower(filepath.Base(syscall.UTF16ToString(buf[:size])))
	if exeName == "horizon.exe" || strings.HasPrefix(exeName, "horizon-") || strings.HasPrefix(exeName, "gens-horizon") {
		return true
	}
	if selfExe, sErr := os.Executable(); sErr == nil {
		if exeName == strings.ToLower(filepath.Base(selfExe)) {
			return true
		}
	}
	return false
}

