package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	traffic "dropo/trafficorchestrator"
)

// NativeTrafficManager owns the single in-process packet engine. It never
// launches command interpreters, Lua runtimes or third-party bypass processes.
type NativeTrafficManager struct {
	basePath string
	logger   func(string)

	mu        sync.Mutex
	engine    *traffic.Engine
	processor *traffic.Processor
	plan      traffic.TrafficPlan
	activeTag string
	openCount uint64
}

func NewNativeTrafficManager(basePath string, logger func(string)) *NativeTrafficManager {
	return &NativeTrafficManager{basePath: basePath, logger: logger}
}

func (m *NativeTrafficManager) log(message string) {
	if m != nil && m.logger != nil {
		m.logger("[TrafficEngine] " + message)
	}
}

func (m *NativeTrafficManager) dllPath() string {
	if m == nil {
		return ""
	}
	return filepath.Join(m.basePath, "bin", "WinDivert.dll")
}

func (m *NativeTrafficManager) driverPath() string {
	if m == nil {
		return ""
	}
	return filepath.Join(m.basePath, "bin", "WinDivert64.sys")
}

func (m *NativeTrafficManager) IsInstalled() bool {
	return runtime.GOOS == "windows" && fileExists(m.dllPath()) && fileExists(m.driverPath())
}

func (m *NativeTrafficManager) ActiveTag() string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeTag
}

// SuccessfulOpenCount reports how many times this manager successfully opened
// the single WinDivert owner. The counter intentionally survives Stop so
// lifecycle diagnostics can distinguish "never opened" from "opened and then
// became unnecessary after every service resolved to direct/VPN fallback".
func (m *NativeTrafficManager) SuccessfulOpenCount() uint64 {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.openCount
}

func (m *NativeTrafficManager) AvailableStrategies() []TransparentFreeAccessStrategy {
	if m == nil || !m.IsInstalled() {
		return nil
	}
	builtin := traffic.BuiltinStrategies()
	result := make([]TransparentFreeAccessStrategy, 0, len(builtin))
	for _, strategy := range builtin {
		result = append(result, TransparentFreeAccessStrategy{
			Tag:       strategy.ID,
			Label:     strategy.Label,
			ExeName:   "WinDivert.dll",
			Platforms: []string{"windows"},
		})
	}
	return result
}

func (m *NativeTrafficManager) strategyPath(_ TransparentFreeAccessStrategy) string {
	return m.dllPath()
}

func (m *NativeTrafficManager) prepareDebugLog(tag string) (string, error) {
	if m == nil || m.basePath == "" {
		return "", errors.New("traffic engine is not initialized")
	}
	directory := filepath.Join(m.basePath, ResourcesFolder, "traffic-diagnostics")
	if err := os.MkdirAll(directory, 0755); err != nil {
		return "", err
	}
	path := filepath.Join(directory, safeFileComponent(tag)+".log")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		return "", err
	}
	return path, nil
}

// StartPlan validates and atomically installs a complete plan. The first plan
// opens one WinDivert handle; later plans only swap immutable processor state.
func (m *NativeTrafficManager) StartPlan(plan traffic.TrafficPlan) error {
	if m == nil {
		return errors.New("traffic engine manager is nil")
	}
	if !m.IsInstalled() {
		return errors.New("bundled WinDivert runtime is not installed")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.engine != nil {
		if plan.Revision <= m.plan.Revision {
			plan.Revision = m.plan.Revision + 1
		}
		if err := m.engine.ApplyPlan(plan); err != nil {
			return err
		}
		m.plan = plan
		m.activeTag = composedStrategyTag
		return nil
	}
	processor, err := traffic.NewProcessor(plan)
	if err != nil {
		return fmt.Errorf("compile traffic plan: %w", err)
	}
	backend, err := traffic.OpenWinDivertBackend(m.dllPath())
	if err != nil {
		return fmt.Errorf("open WinDivert: %w", err)
	}
	engine, err := traffic.NewEngine(backend, processor, m.log)
	if err != nil {
		_ = backend.Close()
		return err
	}
	if err := engine.Start(); err != nil {
		_ = backend.Close()
		return err
	}
	m.engine = engine
	m.processor = processor
	m.plan = plan
	m.activeTag = composedStrategyTag
	m.openCount++
	m.log(fmt.Sprintf("single WinDivert owner active; revision=%d services=%d workNetworks=%d", plan.Revision, len(plan.Services), len(plan.WorkNetworks)))
	return nil
}

func (m *NativeTrafficManager) CurrentPlan() traffic.TrafficPlan {
	if m == nil {
		return traffic.TrafficPlan{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneTrafficPlan(m.plan)
}

func (m *NativeTrafficManager) Counters() map[string]traffic.ServiceCounters {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	processor := m.processor
	m.mu.Unlock()
	if processor == nil {
		return nil
	}
	return processor.Counters()
}

func (m *NativeTrafficManager) Stop() {
	if m == nil {
		return
	}
	m.mu.Lock()
	engine := m.engine
	m.engine = nil
	m.processor = nil
	m.plan = traffic.TrafficPlan{}
	m.activeTag = ""
	m.mu.Unlock()
	if engine != nil {
		if err := engine.Stop(); err != nil {
			m.log("stop error: " + err.Error())
		}
	}
}

// StartForProbe temporarily selects one native strategy for every service. The
// returned closure restores the exact previous immutable plan.
func (m *NativeTrafficManager) StartForProbe(strategy TransparentFreeAccessStrategy) (func(), error) {
	previous := m.CurrentPlan()
	if previous.Revision == 0 {
		return nil, errors.New("no active traffic plan")
	}
	trial := cloneTrafficPlan(previous)
	trial.Revision++
	for index := range trial.Selections {
		trial.Selections[index].StrategyID = strategy.Tag
	}
	if err := m.StartPlan(trial); err != nil {
		return nil, err
	}
	return func() {
		previous.Revision = trial.Revision + 1
		if err := m.StartPlan(previous); err != nil {
			m.log("failed to restore probe plan: " + err.Error())
		}
	}, nil
}

func cloneTrafficPlan(plan traffic.TrafficPlan) traffic.TrafficPlan {
	copyPlan := plan
	copyPlan.Strategies = append([]traffic.TrafficStrategy(nil), plan.Strategies...)
	copyPlan.Services = append([]traffic.ServiceRule(nil), plan.Services...)
	copyPlan.Selections = append([]traffic.ServiceSelection(nil), plan.Selections...)
	copyPlan.WorkNetworks = append([]traffic.WorkNetworkRule(nil), plan.WorkNetworks...)
	return copyPlan
}

func safeFileComponent(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' || character == '_' {
			builder.WriteRune(character)
		}
	}
	if builder.Len() == 0 {
		return "traffic"
	}
	return builder.String()
}

// StartComposedStrategy remains only as a migration guard. Any call means a
// legacy command-line composition path escaped the native plan builder.
func (m *NativeTrafficManager) StartComposedStrategy(_ string, _ []string) error {
	return errors.New("legacy command-line traffic strategies are disabled")
}
