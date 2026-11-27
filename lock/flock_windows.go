//go:build windows

package lock

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	procLockFileEx   = modKernel32.NewProc("LockFileEx")
	procUnlockFileEx = modKernel32.NewProc("UnlockFileEx")
)

const (
	lockfileFailImmediately = 0x00000001
	lockfileExclusiveLock   = 0x00000002
)

func acquireOSLock(path string) (*os.File, bool) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, false
	}
	var ol syscall.Overlapped
	r1, _, _ := procLockFileEx.Call(
		f.Fd(),
		uintptr(lockfileExclusiveLock|lockfileFailImmediately),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&ol)),
	)
	if r1 == 0 {
		_ = f.Close()
		return nil, false
	}
	return f, true
}

func releaseOSLock(f *os.File) {
	if f == nil {
		return
	}
	var ol syscall.Overlapped
	_, _, _ = procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	_ = f.Close()
}
