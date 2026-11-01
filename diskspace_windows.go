//go:build windows

package main

import (
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

func freeDiskBytes(path string) uint64 {
	if strings.TrimSpace(path) == "" {
		return 0
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0
	}
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getFree := kernel32.NewProc("GetDiskFreeSpaceExW")
	p, err := syscall.UTF16PtrFromString(abs)
	if err != nil {
		return 0
	}
	var freeAvailable, totalBytes, totalFree uint64
	r, _, _ := getFree.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&freeAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r == 0 {
		return 0
	}
	return freeAvailable
}
