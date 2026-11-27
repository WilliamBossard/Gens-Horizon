//go:build windows

package auth

import (
	"errors"
	"runtime"
	"syscall"
	"unsafe"
)

var (
	modCrypt32          = syscall.NewLazyDLL("crypt32.dll")
	modKernel32         = syscall.NewLazyDLL("kernel32.dll")
	procCryptProtect    = modCrypt32.NewProc("CryptProtectData")
	procCryptUnprotect  = modCrypt32.NewProc("CryptUnprotectData")
	procLocalFree       = modKernel32.NewProc("LocalFree")
)

type dataBlob struct {
	cbData uint32
	pbData *byte
}

func dpapiAvailable() bool {
	return true
}

func dpapiProtect(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("dpapi: empty input")
	}
	inBlob := dataBlob{cbData: uint32(len(data)), pbData: &data[0]}
	var outBlob dataBlob
	r, _, err := procCryptProtect.Call(
		uintptr(unsafe.Pointer(&inBlob)),
		0,
		0,
		0,
		0,
		0x1, // CRYPTPROTECT_UI_FORBIDDEN
		uintptr(unsafe.Pointer(&outBlob)),
	)
	runtime.KeepAlive(data)
	runtime.KeepAlive(inBlob)
	if r == 0 {
		return nil, err
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(outBlob.pbData)))
	out := unsafe.Slice(outBlob.pbData, outBlob.cbData)
	cp := make([]byte, len(out))
	copy(cp, out)
	return cp, nil
}

func dpapiUnprotect(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("dpapi: empty input")
	}
	inBlob := dataBlob{cbData: uint32(len(data)), pbData: &data[0]}
	var outBlob dataBlob
	r, _, err := procCryptUnprotect.Call(
		uintptr(unsafe.Pointer(&inBlob)),
		0,
		0,
		0,
		0,
		0x1, // CRYPTPROTECT_UI_FORBIDDEN
		uintptr(unsafe.Pointer(&outBlob)),
	)
	runtime.KeepAlive(data)
	runtime.KeepAlive(inBlob)
	if r == 0 {
		return nil, err
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(outBlob.pbData)))
	out := unsafe.Slice(outBlob.pbData, outBlob.cbData)
	cp := make([]byte, len(out))
	copy(cp, out)
	return cp, nil
}

func osKeyringAvailable() bool {
	return false
}

func osKeyringProtect(_ []byte) ([]byte, error) {
	return nil, errors.New("os keyring unused on windows (dpapi used)")
}

func osKeyringDelete(_ []byte) {}

func osKeyringUnprotect(_ []byte) ([]byte, error) {
	return nil, errors.New("os keyring unused on windows (dpapi used)")
}
