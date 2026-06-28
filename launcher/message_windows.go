package main

import (
	"syscall"
	"unsafe"
)

var (
	user32           = syscall.NewLazyDLL("user32.dll")
	messageBoxW      = user32.NewProc("MessageBoxW")
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	getConsoleWindow = kernel32.NewProc("GetConsoleWindow")
)

func showLauncherError(message string) error {
	title, _ := syscall.UTF16PtrFromString("dropo")
	text, _ := syscall.UTF16PtrFromString(message)
	hwnd, _, _ := getConsoleWindow.Call()
	messageBoxW.Call(hwnd, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x00000010)
	return nil
}
