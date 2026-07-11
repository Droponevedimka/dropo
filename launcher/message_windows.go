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

func confirmCertificateTrust(subject, thumbprint string) bool {
	message := "dropo — pet-проект с самоподписанным сертификатом.\n\n" +
		"Установка добавит сертификат в доверенные корневые центры и доверенные издатели ТОЛЬКО текущего пользователя Windows. " +
		"После этого Windows перестанет показывать предупреждение неизвестного издателя.\n\n" +
		"Важно: доверенными станут все программы, подписанные тем же закрытым ключом.\n\n" +
		"Субъект: " + subject + "\nОтпечаток: " + thumbprint + "\n\nУстановить сертификат сейчас?"
	title, _ := syscall.UTF16PtrFromString("dropo — доверие сертификату")
	text, _ := syscall.UTF16PtrFromString(message)
	hwnd, _, _ := getConsoleWindow.Call()
	result, _, _ := messageBoxW.Call(
		hwnd,
		uintptr(unsafe.Pointer(text)),
		uintptr(unsafe.Pointer(title)),
		0x00000004|0x00000030|0x00000100, // Yes/No, warning, default No
	)
	return result == 6 // IDYES
}
