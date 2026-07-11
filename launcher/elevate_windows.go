//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	shell32Launcher = syscall.NewLazyDLL("shell32.dll")
	shellExecuteW   = shell32Launcher.NewProc("ShellExecuteW")
)

func startElevatedCore(corePath string) error {
	corePath = filepath.Clean(corePath)
	if info, err := os.Stat(corePath); err != nil || info.IsDir() {
		if err == nil {
			err = fmt.Errorf("path is a directory")
		}
		return fmt.Errorf("dropo-core.exe is unavailable: %w", err)
	}
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(corePath)
	params, _ := syscall.UTF16PtrFromString("--listen 127.0.0.1:17890")
	dir, _ := syscall.UTF16PtrFromString(filepath.Dir(corePath))
	result, _, callErr := shellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(params)),
		uintptr(unsafe.Pointer(dir)),
		0,
	)
	if result <= 32 {
		return fmt.Errorf("ShellExecute runas failed (code %d): %v", result, callErr)
	}
	return nil
}
