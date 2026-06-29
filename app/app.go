package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// App is the main application struct that holds all state and dependencies.
type App struct {
	ctx               context.Context
	cmd               *exec.Cmd
	cmdDone           chan error
	isRunning         bool
	isStarting        bool
	hasError          bool
	stoppedManually   bool // Manual stop flag
	initialized       bool // Initialization complete flag
	shuttingDown      bool
	windowVisible     bool // Window visibility flag for ping optimization
	mu                sync.Mutex
	basePath          string // Base path (exe directory)
	singboxPath       string
	logPath           string
	tempLogPath       string
	logFile           *os.File
	logFileMu         sync.Mutex
	storage           *Storage                 // Unified storage for all settings
	configBuilder     *ConfigBuilderForStorage // Config builder for storage
	trafficStats      *TrafficStats
	nativeWG          *NativeWireGuardManager // Native WireGuard tunnel manager
	byeDPI            *ByeDPIManager          // Free access (DPI-bypass) process manager
	spoofDPI          *ProxyBypassManager     // Optional proxy-mode DPI-bypass methods
	zapret            *TransparentBypassManager
	tgwsproxy         *TgWsProxyManager  // Telegram MTProto-over-WebSocket proxy sidecar
	xrayBridge        *XrayBridgeManager // Xray bridge for VLESS xhttp profiles
	lastRouteProbe    map[string]routeProbeServiceResult
	lastRouteProbeMu  sync.RWMutex
	routeLatencyMu    sync.Mutex
	routeLatencyCache map[string]routeSummaryLatencyEntry
	routeProbeRunMu   sync.Mutex
	routeProbeRunning bool
	routeProbeDone    chan struct{}
	routeStrategyJobs chan string
	routeStrategyLoop atomic.Bool
	// routeStrategy* enforce a single, unhurried, in-order strategy search per
	// service for the lifetime of one VPN session: once a service has been
	// searched (regardless of outcome) it is not searched again until the next
	// VPN start. routeStrategyQueued de-duplicates jobs that are still pending.
	routeStrategyMu            sync.Mutex
	routeStrategyAttempted     map[string]bool
	routeStrategyQueued        map[string]bool
	transparentReselectionDone bool
	serviceStrategyCacheMu     sync.Mutex
	tgProxyStartedSession      atomic.Bool // tg-ws-proxy sidecar was started this session (gates the exit notice)
	tgProxyPromptedSession     atomic.Bool // tg://proxy was opened at most once per VPN session
	busySeq                    uint64
	vpnStopping                atomic.Bool
	frontendQuitRequested      atomic.Bool
	initializedReady           atomic.Bool
	shutdownRequested          atomic.Bool
	windowVisibleFlag          atomic.Bool
	logBuffer                  []string // Log buffer for UI
	logBufferMu                sync.RWMutex
	events                     *EventHub
}

// NewApp creates a new App application struct.
func NewApp() *App {
	a := &App{
		logBuffer:         make([]string, 0, MaxLogBufferSize),
		windowVisible:     true,
		routeStrategyJobs: make(chan string, 64),
		events:            NewEventHub(512),
	}
	a.windowVisibleFlag.Store(true)
	a.setupLogPath()
	return a
}

// startup is called when the app starts.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// Perform heavy initialization in goroutine to not block UI
	go func() {
		startedAt := time.Now()
		busyID := a.beginBusy("Инициализация приложения...")
		defer a.endBusy(busyID)

		a.updateBusy(busyID, "Подготовка путей и логов...")
		a.setupLogPath()
		a.findPaths()
		a.cleanupDropoRuntimeResidue("startup")
		if a.isShuttingDown() {
			return
		}

		// Initialize unified storage (replaces appConfig, profileManager, configBuilder)
		a.updateBusy(busyID, "Загрузка настроек...")
		a.initStorage()
		a.ensureAutoStartRegistration()
		if a.isShuttingDown() {
			return
		}

		// Initialize Native WireGuard Manager
		a.updateBusy(busyID, "Проверка WireGuard...")
		a.initNativeWireGuard()
		if a.isShuttingDown() {
			return
		}

		// Initialize free access (DPI-bypass) manager
		a.updateBusy(busyID, "Подготовка бесплатных методов обхода...")
		a.initFreeAccess()
		if a.isShuttingDown() {
			return
		}

		// Initialize Xray bridge for xhttp subscriptions
		a.updateBusy(busyID, "Проверка Xray bridge...")
		a.initXrayBridge()
		if a.isShuttingDown() {
			return
		}

		// Initialize traffic stats
		a.updateBusy(busyID, "Загрузка статистики...")
		a.initTrafficStats()
		if a.isShuttingDown() {
			return
		}

		a.mu.Lock()
		a.initialized = true
		a.mu.Unlock()
		a.initializedReady.Store(true)

		// Set initial tray icon to disconnected (grey)
		UpdateTrayIcon("disconnected")
		a.startRouteStrategyMaintenanceListener()

		a.runStartupRoutingPreparation(busyID)
		if a.isShuttingDown() {
			return
		}

		a.updateBusy(busyID, "Готово")
		shouldRestoreVPN := a.shouldRestoreVPNOnStartup()
		a.writeLog(fmt.Sprintf("Application initialized in %s", time.Since(startedAt).Round(time.Millisecond)))
		if shouldRestoreVPN && !a.isShuttingDown() {
			go a.restoreVPNOnStartup()
		}
	}()
}

// waitForInit waits for initialization to complete (max 5 sec)
func (a *App) waitForInit() bool {
	for i := 0; i < 50; i++ {
		if a.initializedReady.Load() {
			return true
		}
		a.mu.Lock()
		initialized := a.initialized
		a.mu.Unlock()
		if initialized {
			a.initializedReady.Store(true)
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func (a *App) isInitialized() bool {
	if a.initializedReady.Load() {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.initialized {
		a.initializedReady.Store(true)
		return true
	}
	return false
}

func (a *App) requestShutdown() {
	a.shutdownRequested.Store(true)
}

func (a *App) isShuttingDown() bool {
	return a.shutdownRequested.Load()
}

func (a *App) ensureAutoStartRegistration() {
	if a.storage == nil {
		return
	}
	settings := a.storage.GetAppSettings()
	if err := SetAutoStart(settings.AutoStart); err != nil {
		a.writeLog(fmt.Sprintf("Failed to sync autostart setting: %v", err))
		return
	}
	a.writeLog(fmt.Sprintf("Autostart synchronized: %v", settings.AutoStart))
}

func (a *App) shouldRestoreVPNOnStartup() bool {
	if a.storage == nil {
		return false
	}
	settings := a.storage.GetAppSettings()
	return settings.RestoreVPNOnStartup
}

func (a *App) setRestoreVPNOnStartup(enabled bool) {
	if a.storage == nil {
		return
	}
	settings := a.storage.GetAppSettings()
	if settings.RestoreVPNOnStartup == enabled {
		return
	}
	settings.RestoreVPNOnStartup = enabled
	if err := a.storage.UpdateAppSettings(settings); err != nil {
		a.writeLog(fmt.Sprintf("Failed to save VPN restore state: %v", err))
		return
	}
	a.writeLog(fmt.Sprintf("VPN restore on startup: %v", enabled))
}

func (a *App) restoreVPNOnStartup() {
	if a.isShuttingDown() {
		return
	}
	a.writeLog("Restoring VPN state from previous session")
	result := a.Start()
	if ok, _ := result["success"].(bool); !ok {
		a.writeLog(fmt.Sprintf("Failed to restore VPN on startup: %v", result["error"]))
	}
}

// shutdown is called when the app is closing
func (a *App) shutdown(ctx context.Context) {
	a.requestShutdown()
	// Stop sing-box
	a.Stop()

	// Stop WireGuard health check and all tunnels
	if a.nativeWG != nil {
		a.writeLog("Stopping WireGuard health check...")
		a.nativeWG.StopHealthCheck()
		a.writeLog("Stopping all Native WireGuard tunnels...")
		a.nativeWG.StopAllTunnels()
	}

	// Stop free access (ByeDPI) process
	if a.byeDPI != nil {
		a.byeDPI.Stop()
	}
	if a.spoofDPI != nil {
		a.spoofDPI.Stop()
	}
	if a.zapret != nil {
		a.zapret.Stop()
	}
	if a.tgwsproxy != nil {
		a.tgwsproxy.Stop()
	}
	if a.xrayBridge != nil {
		a.xrayBridge.Stop()
	}
	a.cleanupDropoRuntimeResidue("shutdown")
	closeManagedProcessJob(a.writeLog)

	a.closeLogFile()

	// Save traffic stats
	if a.trafficStats != nil {
		a.trafficStats.Save()
	}

	// Storage auto-saves on every change, no need to save here
}

// initStorage initializes the unified storage
func (a *App) initStorage() {
	if a.basePath == "" {
		return
	}

	a.storage = NewStorage(a.basePath)
	if err := a.storage.Init(); err != nil {
		a.writeLog(fmt.Sprintf("Failed to init storage: %v", err))
		return
	}

	// Migrate from old format if needed
	if err := a.storage.MigrateFromOldFormat(a.basePath); err != nil {
		a.writeLog(fmt.Sprintf("Migration error: %v", err))
	}

	// Create config builder for storage
	a.configBuilder = NewConfigBuilderForStorage(a.storage)

	// Set routing mode from settings
	settings := a.storage.GetAppSettings()
	if settings.RoutingMode != "" {
		a.configBuilder.SetRoutingMode(settings.RoutingMode)
	}

	a.writeLog("Storage initialized: " + a.storage.GetResourcesPath())
}

func (a *App) runStartupRoutingPreparation(busyID string) {
	if a.basePath == "" || a.isShuttingDown() {
		return
	}

	a.updateBusy(busyID, "Проверяем встроенные базы маршрутизации...")
	filterManager := NewFilterManager(a.basePath)
	if !filterManager.EnsureFiltersExist() {
		a.writeLog("Routing filters are missing from the build bundle; rebuild with build-time filter update")
	} else if info, err := filterManager.GetInfo(); err == nil {
		a.writeLog(fmt.Sprintf("Routing filters bundled: v%s, %d file(s), updated %s",
			info.Version, info.FilterCount, info.UpdatedAt))
	} else {
		a.writeLog(fmt.Sprintf("Failed to read bundled routing filters info: %v", err))
	}
	a.updateBusy(busyID, "Готово")
	a.writeLog("[RouteProbe] startup discovery skipped; saved/default free-access strategies will be applied on VPN start")
}

// initNativeWireGuard initializes the Native WireGuard Manager
func (a *App) initNativeWireGuard() {
	if a.basePath == "" {
		return
	}

	// Create native WireGuard manager - uses bundled binaries
	a.nativeWG = NewNativeWireGuardManager(a.basePath, a.writeLog)

	if err := a.nativeWG.Init(); err != nil {
		a.writeLog(fmt.Sprintf("Failed to init Native WireGuard: %v", err))
		return
	}

	if a.nativeWG.IsInstalled() {
		a.writeLog(fmt.Sprintf("Native WireGuard v%s available: %s", WireGuardVersion, a.nativeWG.wireguardPath))
	} else {
		a.writeLog(fmt.Sprintf("Native WireGuard v%s - bundled binaries not found", WireGuardVersion))
	}
}

// initFreeAccess initializes the free access (DPI-bypass) manager.
// Actually starting/stopping the ByeDPI process happens alongside VPN
// start/stop (app_api_vpn.go), not here — this only prepares the manager.
func (a *App) initFreeAccess() {
	if a.basePath == "" {
		return
	}

	a.byeDPI = NewByeDPIManager(a.basePath, a.writeLog)
	a.spoofDPI = NewProxyBypassManager(a.basePath, DefaultSpoofDPIMethods, a.writeLog)
	a.zapret = NewTransparentBypassManager(a.basePath, DefaultZapretTransparentStrategies, a.writeLog)
	a.tgwsproxy = NewTgWsProxyManager(a.basePath, a.writeLog)

	if a.byeDPI.IsInstalled() {
		a.writeLog("Free access (ByeDPI) binary found")
	} else {
		a.writeLog("Free access (ByeDPI) binary not found - bundle ciadpi.exe in bin/ to enable")
	}
	if a.spoofDPI.IsInstalled() {
		a.writeLog("Free access (SpoofDPI) binary found")
	} else {
		a.writeLog("Free access (SpoofDPI) binary not found - optional on this platform")
	}
	if a.zapret.IsInstalled() {
		a.writeLog("Free access (zapret/winws) binary found")
	} else {
		a.writeLog("Free access (zapret/winws) binary not found - transparent methods unavailable")
	}
	if a.tgwsproxy.IsInstalled() {
		a.writeLog("Telegram MTProto proxy (tg-ws-proxy) binary found")
	} else {
		a.writeLog("Telegram MTProto proxy (tg-ws-proxy) binary not found - Telegram app proxy unavailable")
	}
}

func (a *App) initXrayBridge() {
	if a.basePath == "" || a.storage == nil {
		return
	}

	a.xrayBridge = NewXrayBridgeManager(a.basePath, a.storage.GetResourcesPath(), a.writeLog)
	if a.xrayBridge.IsInstalled() {
		a.writeLog("Xray bridge binary found")
	} else {
		a.writeLog("Xray bridge binary not found - bundle xray.exe in bin/ to enable xhttp")
	}
}

// findPaths finds paths to sing-box and base directory
func (a *App) findPaths() {
	// Get executable directory
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	exePath, _ = filepath.EvalSymlinks(exePath)
	exeDir := filepath.Dir(exePath)

	// Set base path
	a.basePath = exeDir
	a.refreshSingBoxPath()
}

// refreshSingBoxPath refreshes the sing-box executable path inside the current
// basePath. Split portable builds download bin/ after startup, so this must be
// callable without recalculating basePath from os.Executable().
func (a *App) refreshSingBoxPath() {
	if a.basePath == "" {
		return
	}
	a.singboxPath = ""
	singboxName := singBoxBinaryName()
	exeDir := a.basePath

	// 1. Look in bin/ folder next to exe (portable distribution)
	singboxPath := filepath.Join(exeDir, "bin", singboxName)
	if _, err := os.Stat(singboxPath); err == nil {
		a.singboxPath = singboxPath
		a.writeLog(fmt.Sprintf("Using bundled sing-box: %s", singboxPath))
		return
	}

	// 2. Look next to exe
	singboxPath = filepath.Join(exeDir, singboxName)
	if _, err := os.Stat(singboxPath); err == nil {
		a.singboxPath = singboxPath
		a.writeLog(fmt.Sprintf("Using sing-box: %s", singboxPath))
		return
	}

	// 3. Platform-specific fallbacks
	if runtime.GOOS == "windows" {
		// In Program Files
		singboxPath = "C:\\Program Files\\sing-box\\sing-box.exe"
		if _, err := os.Stat(singboxPath); err == nil {
			a.singboxPath = singboxPath
			return
		}
	} else {
		// In PATH
		if path, err := exec.LookPath("sing-box"); err == nil {
			a.singboxPath = path
			return
		}
		// In /usr/local/bin
		singboxPath = "/usr/local/bin/sing-box"
		if _, err := os.Stat(singboxPath); err == nil {
			a.singboxPath = singboxPath
		}
	}
}

// fileExists checks if a file exists
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
