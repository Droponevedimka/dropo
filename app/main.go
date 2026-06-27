package main

import (
	"context"
	"embed"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/energye/systray"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed assets/icons/icon_grey.ico
var iconGrey []byte

//go:embed assets/icons/icon_green.ico
var iconGreen []byte

//go:embed assets/icons/icon_red.ico
var iconRed []byte

//go:embed config/template.json
var embeddedTemplate []byte

var appInstance *App
var systrayReady = make(chan struct{})
var systrayReadyFlag atomic.Bool

// Windows API для single instance и смены иконки
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
	SW_RESTORE      = 9
	WM_SETICON      = 0x0080
	ICON_SMALL      = 0
	ICON_BIG        = 1
	LR_DEFAULTCOLOR = 0x00000000
)

// copyEmbeddedTemplate копирует встроенный template.json в указанный путь
func copyEmbeddedTemplate(destPath string) error {
	return os.WriteFile(destPath, embeddedTemplate, 0644)
}

func main() {
	// Проверяем single instance
	mutexName, _ := syscall.UTF16PtrFromString("Global\\dropo_SingleInstance")
	handle, _, err := createMutex.Call(0, 1, uintptr(unsafe.Pointer(mutexName)))

	// ERROR_ALREADY_EXISTS = 183
	if err == syscall.Errno(183) || (handle != 0 && err == syscall.Errno(183)) {
		// Приложение уже запущено - показываем существующее окно
		windowName, _ := syscall.UTF16PtrFromString(AppDisplayName)
		hwnd, _, _ := findWindow.Call(0, uintptr(unsafe.Pointer(windowName)))
		if hwnd != 0 {
			showWindow.Call(hwnd, SW_RESTORE)
			setForeground.Call(hwnd)
		}
		log.Println("Application already running, activating existing window")
		os.Exit(0)
	}

	if handle != 0 {
		defer syscall.CloseHandle(syscall.Handle(handle))
	}

	appInstance = NewApp()
	if err := appInstance.openLogFile(); err != nil {
		log.Printf("failed to open session log: %v", err)
	}

	// Запускаем systray в отдельной горутине (более надёжно на Windows)
	go func() {
		systray.Run(onSystrayReady, onSystrayExit)
	}()

	// Небольшая задержка для инициализации systray
	select {
	case <-systrayReady:
	case <-time.After(1500 * time.Millisecond):
		log.Println("systray initialization timeout, continuing Wails startup")
	}

	// Запускаем Wails в main goroutine (более стабильно для GUI)
	runWails()
}

func runWails() {
	err := wails.Run(&options.App{
		Title:     AppDisplayName,
		Width:     WindowWidth,
		Height:    WindowHeight,
		MinWidth:  MinWindowWidth,
		MinHeight: MinWindowHeight,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		OnStartup:        appInstance.startup,
		OnBeforeClose: func(ctx context.Context) bool {
			if appInstance != nil && !appInstance.isShuttingDown() {
				if shouldHideToTrayOnClose(appInstance) {
					appInstance.writeLog("Window close requested; hiding to tray")
					appInstance.HideWindow()
					return true
				}
				appInstance.writeLog("Window close requested while systray is unavailable; closing application to avoid hidden background process")
				appInstance.requestShutdown()
			}
			return false
		},
		OnShutdown: appInstance.shutdown,
		Bind: []interface{}{
			appInstance,
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			DisableWindowIcon:    false,
		},
		Frameless: false,
		// При закрытии окна - скрывать в трей
		HideWindowOnClose: false,
	})

	if err != nil {
		log.Fatal(err)
	}
}

func onSystrayReady() {
	trayName := getTrayDisplayName()
	systray.SetIcon(iconGrey)
	systray.SetTitle(trayName)
	systray.SetTooltip(trayName + " - Отключено")

	// Левый клик - открыть приложение
	systray.SetOnClick(func(menu systray.IMenu) {
		if appInstance != nil {
			appInstance.ShowWindow()
		}
	})

	// Двойной клик - тоже открыть
	systray.SetOnDClick(func(menu systray.IMenu) {
		if appInstance != nil {
			appInstance.ShowWindow()
		}
	})

	// Правый клик - показать меню
	systray.SetOnRClick(func(menu systray.IMenu) {
		menu.ShowMenu()
	})

	// Пункты меню (показываются по правому клику)
	mShow := systray.AddMenuItem("Открыть", "Показать окно")
	systray.AddSeparator()
	mLogs := systray.AddMenuItem("Логи", "Открыть файл логов")
	mAbout := systray.AddMenuItem("О программе", "Информация о программе")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Выход", "Закрыть приложение")

	// Сигнализируем что systray готов
	if appInstance != nil {
		appInstance.writeLog("Systray ready")
	}
	markSystrayReady()

	// Обработка кликов по пунктам меню
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
	if systrayReadyFlag.CompareAndSwap(false, true) {
		close(systrayReady)
	}
}

func isSystrayReady() bool {
	return systrayReadyFlag.Load()
}

func shouldHideToTrayOnClose(app *App) bool {
	return app != nil && !app.isShuttingDown() && isSystrayReady()
}

func getTrayDisplayName() string {
	if value := strings.TrimSpace(os.Getenv("DROPO_TRAY_LABEL")); value != "" {
		return value
	}
	return AppDisplayName
}

// UpdateTrayIcon обновляет иконку в трее и в окне приложения
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

	// Обновляем иконку в трее
	systray.SetIcon(iconData)
	systray.SetTooltip(tooltip)

	// Обновляем иконку окна с небольшой задержкой
	go func() {
		time.Sleep(100 * time.Millisecond)
		setWindowIcon(iconData)
	}()
}

// setWindowIcon устанавливает иконку окна через Windows API
func setWindowIcon(iconData []byte) {
	if len(iconData) == 0 {
		return
	}

	// Находим окно по заголовку
	windowName, _ := syscall.UTF16PtrFromString(AppDisplayName)
	hwnd, _, _ := findWindow.Call(0, uintptr(unsafe.Pointer(windowName)))
	if hwnd == 0 {
		return
	}

	// Создаем иконку из данных .ico файла
	// ICO файл содержит директорию иконок, нужно найти нужный размер
	hIcon := createIconFromICO(iconData, 32, 32)      // Большая иконка
	hIconSmall := createIconFromICO(iconData, 16, 16) // Маленькая иконка

	if hIcon != 0 {
		sendMessage.Call(hwnd, WM_SETICON, ICON_BIG, hIcon)
	}
	if hIconSmall != 0 {
		sendMessage.Call(hwnd, WM_SETICON, ICON_SMALL, hIconSmall)
	}
}

// createIconFromICO создает HICON из данных .ico файла
func createIconFromICO(icoData []byte, width, height int) uintptr {
	if len(icoData) < 6 {
		return 0
	}

	// Структура ICO файла:
	// ICONDIR (6 bytes): reserved(2), type(2), count(2)
	// ICONDIRENTRY (16 bytes each): width, height, colorCount, reserved, planes(2), bitCount(2), bytesInRes(4), imageOffset(4)

	// Проверяем заголовок ICO
	if icoData[0] != 0 || icoData[1] != 0 || icoData[2] != 1 || icoData[3] != 0 {
		return 0 // Не ICO файл
	}

	count := int(icoData[4]) | int(icoData[5])<<8
	if count == 0 {
		return 0
	}

	// Ищем подходящий размер иконки
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

		// Ищем ближайший размер к запрошенному
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

	// Получаем данные выбранной иконки
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

	// Создаем иконку из ресурса
	imageData := icoData[imageOffset : imageOffset+bytesInRes]

	hIcon, _, _ := createIconFromResourceEx.Call(
		uintptr(unsafe.Pointer(&imageData[0])),
		uintptr(bytesInRes),
		1,          // TRUE = icon
		0x00030000, // Version
		uintptr(width),
		uintptr(height),
		LR_DEFAULTCOLOR,
	)

	return hIcon
}
