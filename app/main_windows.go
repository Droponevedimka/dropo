//go:build windows

package main

import (
	"embed"
	"log"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/energye/systray"
	"github.com/wailsapp/wails/v2/pkg/options"
	wailsWindows "github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed assets/icons/icon_grey.ico
var iconGrey []byte

//go:embed assets/icons/icon_green.ico
var iconGreen []byte

//go:embed assets/icons/icon_red.ico
var iconRed []byte

// Keep embed imported on Windows together with the icon directives.
var _ embed.FS

var (
	kernel32                    = syscall.NewLazyDLL("kernel32.dll")
	user32                      = syscall.NewLazyDLL("user32.dll")
	createMutex                 = kernel32.NewProc("CreateMutexW")
	findWindow                  = user32.NewProc("FindWindowW")
	showWindow                  = user32.NewProc("ShowWindow")
	setForeground               = user32.NewProc("SetForegroundWindow")
	sendMessage                 = user32.NewProc("SendMessageW")
	createIconFromResourceEx    = user32.NewProc("CreateIconFromResourceEx")
	destroyIcon                 = user32.NewProc("DestroyIcon")
	lookupIconIdFromDirectoryEx = user32.NewProc("LookupIconIdFromDirectoryEx")
)

const (
	swRestore      = 9
	wmSetIcon      = 0x0080
	iconSmall      = 0
	iconBig        = 1
	lrDefaultColor = 0x00000000
)

var systrayReady = make(chan struct{})
var systrayReadyClosed atomic.Bool

func acquireSingleInstance() (func(), bool) {
	mutexName, _ := syscall.UTF16PtrFromString("Global\\dropo_SingleInstance")
	handle, _, err := createMutex.Call(0, 1, uintptr(unsafe.Pointer(mutexName)))
	if err == syscall.Errno(183) || (handle != 0 && err == syscall.Errno(183)) {
		windowName, _ := syscall.UTF16PtrFromString(AppDisplayName)
		hwnd, _, _ := findWindow.Call(0, uintptr(unsafe.Pointer(windowName)))
		if hwnd != 0 {
			showWindow.Call(hwnd, swRestore)
			setForeground.Call(hwnd)
		}
		if handle != 0 {
			_ = syscall.CloseHandle(syscall.Handle(handle))
		}
		return nil, true
	}
	if handle == 0 {
		return nil, false
	}
	return func() {
		_ = syscall.CloseHandle(syscall.Handle(handle))
	}, false
}

func startPlatformTray() {
	go func() {
		systray.Run(onSystrayReady, onSystrayExit)
	}()

	select {
	case <-systrayReady:
	case <-time.After(1500 * time.Millisecond):
		log.Println("systray initialization timeout, continuing Wails startup")
	}
}

func applyPlatformWailsOptions(appOptions *options.App) {
	appOptions.Windows = &wailsWindows.Options{
		WebviewIsTransparent: false,
		WindowIsTranslucent:  false,
		DisableWindowIcon:    false,
	}
}

func onSystrayReady() {
	trayName := getTrayDisplayName()
	systray.SetIcon(iconGrey)
	systray.SetTitle(trayName)
	systray.SetTooltip(trayName + " - Отключено")

	systray.SetOnClick(func(menu systray.IMenu) {
		if appInstance != nil {
			appInstance.ShowWindow()
		}
	})
	systray.SetOnDClick(func(menu systray.IMenu) {
		if appInstance != nil {
			appInstance.ShowWindow()
		}
	})
	systray.SetOnRClick(func(menu systray.IMenu) {
		menu.ShowMenu()
	})

	mShow := systray.AddMenuItem("Открыть", "Показать окно")
	systray.AddSeparator()
	mLogs := systray.AddMenuItem("Логи", "Открыть файл логов")
	mAbout := systray.AddMenuItem("О программе", "Информация о программе")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Выход", "Закрыть приложение")

	if appInstance != nil {
		appInstance.writeLog("Systray ready")
	}
	markSystrayReady()

	mShow.Click(func() {
		if appInstance != nil {
			appInstance.ShowWindow()
		}
	})
	mLogs.Click(func() {
		if appInstance != nil {
			appInstance.OpenLogs()
		}
	})
	mAbout.Click(func() {
		if appInstance != nil {
			appInstance.ShowAbout()
		}
	})
	mQuit.Click(func() {
		if appInstance != nil {
			appInstance.writeLog("Systray quit requested")
			appInstance.QuitWithTelegramNotice()
		}
	})
}

func onSystrayExit() {
	systrayReadyFlag.Store(false)
	if appInstance != nil && !appInstance.isShuttingDown() {
		appInstance.writeLog("Systray exited unexpectedly; showing main window")
		appInstance.ShowWindow()
	}
}

func markSystrayReady() {
	if systrayReadyFlag.CompareAndSwap(false, true) && systrayReadyClosed.CompareAndSwap(false, true) {
		close(systrayReady)
	}
}

func UpdateTrayIcon(status string) {
	var iconData []byte
	var tooltip string

	switch status {
	case "connected":
		iconData = iconGreen
		tooltip = getTrayDisplayName() + " - Подключено"
	case "error":
		iconData = iconRed
		tooltip = getTrayDisplayName() + " - Ошибка"
	default:
		iconData = iconGrey
		tooltip = getTrayDisplayName() + " - Отключено"
	}

	log.Printf("UpdateTrayIcon: status=%s, iconLen=%d", status, len(iconData))
	systray.SetIcon(iconData)
	systray.SetTooltip(tooltip)

	go func() {
		time.Sleep(100 * time.Millisecond)
		setWindowIcon(iconData)
	}()
}

func setWindowIcon(iconData []byte) {
	if len(iconData) == 0 {
		return
	}

	windowName, _ := syscall.UTF16PtrFromString(AppDisplayName)
	hwnd, _, _ := findWindow.Call(0, uintptr(unsafe.Pointer(windowName)))
	if hwnd == 0 {
		return
	}

	hIcon := createIconFromICO(iconData, 32, 32)
	hIconSmall := createIconFromICO(iconData, 16, 16)
	if hIcon != 0 {
		sendMessage.Call(hwnd, wmSetIcon, iconBig, hIcon)
	}
	if hIconSmall != 0 {
		sendMessage.Call(hwnd, wmSetIcon, iconSmall, hIconSmall)
	}
}

func createIconFromICO(icoData []byte, width, height int) uintptr {
	if len(icoData) < 6 {
		return 0
	}
	if icoData[0] != 0 || icoData[1] != 0 || icoData[2] != 1 || icoData[3] != 0 {
		return 0
	}

	count := int(icoData[4]) | int(icoData[5])<<8
	if count == 0 {
		return 0
	}

	bestIdx := 0
	bestSize := 0
	for i := 0; i < count; i++ {
		entryOffset := 6 + i*16
		if entryOffset+16 > len(icoData) {
			break
		}
		w := int(icoData[entryOffset])
		h := int(icoData[entryOffset+1])
		if w == 0 {
			w = 256
		}
		if h == 0 {
			h = 256
		}
		size := w
		if w == width && h == height {
			bestIdx = i
			break
		}
		if size > bestSize && size <= width*2 {
			bestSize = size
			bestIdx = i
		}
	}

	entryOffset := 6 + bestIdx*16
	if entryOffset+16 > len(icoData) {
		return 0
	}

	bytesInRes := int(icoData[entryOffset+8]) | int(icoData[entryOffset+9])<<8 |
		int(icoData[entryOffset+10])<<16 | int(icoData[entryOffset+11])<<24
	imageOffset := int(icoData[entryOffset+12]) | int(icoData[entryOffset+13])<<8 |
		int(icoData[entryOffset+14])<<16 | int(icoData[entryOffset+15])<<24
	if imageOffset+bytesInRes > len(icoData) {
		return 0
	}

	imageData := icoData[imageOffset : imageOffset+bytesInRes]
	hIcon, _, _ := createIconFromResourceEx.Call(
		uintptr(unsafe.Pointer(&imageData[0])),
		uintptr(bytesInRes),
		1,
		0x00030000,
		uintptr(width),
		uintptr(height),
		lrDefaultColor,
	)
	return hIcon
}

var _ = destroyIcon
var _ = lookupIconIdFromDirectoryEx
