package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// streamCmdOutput reads a child process pipe line-by-line into the app log so
// every spawned helper's output is captured for diagnosis.
func streamCmdOutput(r io.Reader, label string, logger func(string)) {
	if r == nil || logger == nil {
		return
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 4096), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			logger(label + ": " + line)
		}
	}
	if err := scanner.Err(); err != nil {
		logger(label + ": output read error: " + err.Error())
	}
}

type TransparentBypassManager struct {
	basePath     string
	hostlistPath string
	ipsetPath    string
	strategies   []TransparentFreeAccessStrategy
	logger       func(string)

	mu          sync.Mutex
	activeTag   string
	activeCmd   *exec.Cmd
	activeStop  chan struct{}
	activeLabel string
	activeArgs  []string

	validationMu    sync.Mutex
	validatedArgSet map[string]struct{}
}

func NewTransparentBypassManager(basePath string, strategies []TransparentFreeAccessStrategy, logger func(string)) *TransparentBypassManager {
	return &TransparentBypassManager{
		basePath:        basePath,
		hostlistPath:    filepath.Join(basePath, ResourcesFolder, "zapret-hostlist.txt"),
		ipsetPath:       filepath.Join(basePath, ResourcesFolder, "zapret-ipset.txt"),
		strategies:      strategies,
		logger:          logger,
		validatedArgSet: make(map[string]struct{}),
	}
}

func (m *TransparentBypassManager) log(message string) {
	if m.logger != nil {
		m.logger(fmt.Sprintf("[Zapret2] %s", message))
	}
}

func (m *TransparentBypassManager) strategyPath(strategy TransparentFreeAccessStrategy) string {
	return filepath.Join(m.basePath, "bin", strategy.ExeName)
}

func (m *TransparentBypassManager) IsInstalled() bool {
	return len(m.AvailableStrategies()) > 0
}

func (m *TransparentBypassManager) ActiveTag() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeTag
}

func (m *TransparentBypassManager) AvailableStrategies() []TransparentFreeAccessStrategy {
	result := make([]TransparentFreeAccessStrategy, 0, len(m.strategies))
	for _, strategy := range m.strategies {
		if !methodSupportsCurrentPlatform(strategy.Platforms) {
			continue
		}
		if fileExists(m.strategyPath(strategy)) {
			missingRuntimeFile := false
			for _, file := range []string{"cygwin1.dll", "WinDivert.dll", "WinDivert64.sys"} {
				if !fileExists(filepath.Join(m.basePath, "bin", file)) {
					missingRuntimeFile = true
					break
				}
			}
			if missingRuntimeFile {
				continue
			}
			missingRequiredFile := false
			for _, file := range strategy.RequiredFiles {
				if !fileExists(filepath.Join(m.basePath, "bin", file)) {
					missingRequiredFile = true
					break
				}
			}
			if missingRequiredFile {
				continue
			}
			result = append(result, strategy)
		}
	}
	return result
}

const composedStrategyTag = "per-service-composed"

// StartComposedStrategy runs winws2 with a fully-formed, per-service composed
// argument list (see composeServiceWinwsArgs). Unlike StartSelected it does not
// resolve placeholders or inject a combined hostlist/ipset — the args already
// carry one --hostlist per service. Passing empty args stops the engine.
func (m *TransparentBypassManager) StartComposedStrategy(label string, args []string) error {
	if len(args) == 0 {
		m.Stop()
		return nil
	}

	exePath := filepath.Join(m.basePath, "bin", ZapretProcessName)
	if !fileExists(exePath) {
		return fmt.Errorf("%s not found: %s", ZapretProcessName, exePath)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	alreadyRunning := m.activeTag == composedStrategyTag && m.activeCmd != nil && slices.Equal(m.activeArgs, args)
	if alreadyRunning {
		m.log(fmt.Sprintf("%s already uses the requested composed arguments; restart skipped", label))
		return nil
	}
	previousLabel := m.activeLabel
	previousArgs := append([]string(nil), m.activeArgs...)
	previousWasComposed := m.activeTag == composedStrategyTag && len(previousArgs) > 0

	// winws2 --dry-run still opens the configured WinDivert filter. Running it
	// beside the current composed instance therefore fails with "A copy of
	// winws2 is already running with the same filter". Serialize recomposition
	// and stop the old filter before validating the replacement. If validation
	// fails, restore the last known-good composed process immediately.
	m.stopLocked()
	if err := m.validateZapret2Args(exePath, args); err != nil {
		m.restoreComposedLocked(previousWasComposed, previousLabel, previousArgs)
		return fmt.Errorf("%s configuration is invalid: %w", label, err)
	}

	// Composed args change as per-service selections change, so always restart.
	m.logWinDivertStatus("before composed start")
	cmd, exitCh, err := m.startRawProcessValidated(label, args)
	if err != nil {
		m.restoreComposedLocked(previousWasComposed, previousLabel, previousArgs)
		m.logWinDivertStatus("composed start failed")
		return err
	}
	if err := waitForTransparentStartup(exitCh); err != nil {
		terminateProcessTree(cmd)
		m.restoreComposedLocked(previousWasComposed, previousLabel, previousArgs)
		m.logWinDivertStatus("composed startup failed")
		return err
	}
	m.logWinDivertStatus("after composed start")

	stopCh := make(chan struct{})
	m.activeTag = composedStrategyTag
	m.activeCmd = cmd
	m.activeStop = stopCh
	m.activeLabel = label
	m.activeArgs = append([]string(nil), args...)
	m.log(fmt.Sprintf("%s selected and started (pid=%d)", label, cmd.Process.Pid))
	go m.watchPersistentStrategy(TransparentFreeAccessStrategy{Tag: composedStrategyTag, Label: label}, cmd, exitCh, stopCh)
	return nil
}

func (m *TransparentBypassManager) restoreComposedLocked(restore bool, label string, args []string) {
	if !restore || len(args) == 0 {
		return
	}
	cmd, exitCh, err := m.startRawProcessValidated(label, args)
	if err != nil {
		m.log(fmt.Sprintf("failed to restore previous composed strategy: %v", err))
		return
	}
	if err := waitForTransparentStartup(exitCh); err != nil {
		terminateProcessTree(cmd)
		m.log(fmt.Sprintf("previous composed strategy did not recover: %v", err))
		return
	}
	stopCh := make(chan struct{})
	m.activeTag = composedStrategyTag
	m.activeCmd = cmd
	m.activeStop = stopCh
	m.activeLabel = label
	m.activeArgs = append([]string(nil), args...)
	m.log(fmt.Sprintf("restored previous composed strategy (pid=%d)", cmd.Process.Pid))
	go m.watchPersistentStrategy(TransparentFreeAccessStrategy{Tag: composedStrategyTag, Label: label}, cmd, exitCh, stopCh)
}

func (m *TransparentBypassManager) startRawProcess(label string, args []string) (*exec.Cmd, <-chan error, error) {
	exePath := filepath.Join(m.basePath, "bin", ZapretProcessName)
	if !fileExists(exePath) {
		return nil, nil, fmt.Errorf("%s not found: %s", ZapretProcessName, exePath)
	}
	if err := m.validateZapret2Args(exePath, args); err != nil {
		return nil, nil, fmt.Errorf("%s configuration is invalid: %w", label, err)
	}
	return m.startRawProcessValidated(label, args)
}

func (m *TransparentBypassManager) startRawProcessValidated(label string, args []string) (*exec.Cmd, <-chan error, error) {
	exePath := filepath.Join(m.basePath, "bin", ZapretProcessName)
	m.log(fmt.Sprintf("starting %s: %s %s", label, exePath, formatProcessArgs(args)))
	cmd := exec.Command(exePath, args...)
	cmd.Dir = filepath.Dir(exePath)
	stdout, stdoutErr := cmd.StdoutPipe()
	stderr, stderrErr := cmd.StderrPipe()
	configureBackgroundCommand(cmd)
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	attachManagedCmdToJob(cmd, label, m.log)
	if stdoutErr == nil {
		go m.logProcessOutput(stdout, "composed", "OUT")
	}
	if stderrErr == nil {
		go m.logProcessOutput(stderr, "composed", "ERR")
	}
	exitCh := make(chan error, 1)
	go func() {
		exitCh <- cmd.Wait()
	}()
	return cmd, exitCh, nil
}

func (m *TransparentBypassManager) StartForProbe(strategy TransparentFreeAccessStrategy) (func(), error) {
	if !methodSupportsCurrentPlatform(strategy.Platforms) {
		return nil, fmt.Errorf("%s is not supported on %s", strategy.Tag, runtime.GOOS)
	}
	if !fileExists(m.strategyPath(strategy)) {
		return nil, fmt.Errorf("%s not found: %s", strategy.ExeName, m.strategyPath(strategy))
	}

	m.logWinDivertStatus("before probe")
	cmd, exitCh, err := m.startStrategyProcess(strategy)
	if err != nil {
		m.logWinDivertStatus("probe start failed")
		return nil, err
	}
	if err := waitForTransparentStartup(exitCh); err != nil {
		terminateProcessTree(cmd)
		m.logWinDivertStatus("probe startup failed")
		return nil, err
	}
	m.logWinDivertStatus("after probe start")
	m.log(fmt.Sprintf("%s probe started (pid=%d)", strategy.Label, cmd.Process.Pid))
	return func() {
		terminateProcessTree(cmd)
		m.logWinDivertStatus("after probe stop")
		if m.ActiveTag() == "" {
			cleanupWinDivertServiceIfOwned([]string{m.basePath}, "probe stop", m.log)
		}
		m.log(fmt.Sprintf("%s probe stopped", strategy.Label))
	}, nil
}

func (m *TransparentBypassManager) startStrategyProcess(strategy TransparentFreeAccessStrategy) (*exec.Cmd, <-chan error, error) {
	exePath := m.strategyPath(strategy)
	scopeArgs := []string{}
	hostlistPath, hostlistErr := m.ensureHostlist()
	if hostlistErr == nil && hostlistPath != "" {
		scopeArgs = append(scopeArgs, "--hostlist="+hostlistPath)
	} else if hostlistErr != nil {
		m.log(fmt.Sprintf("hostlist was not prepared, starting without it: %v", hostlistErr))
	}
	ipsetPath, ipsetErr := m.ensureIPSet()
	if ipsetErr == nil && ipsetPath != "" {
		scopeArgs = append(scopeArgs, "--ipset="+ipsetPath)
	} else if ipsetErr != nil {
		m.log(fmt.Sprintf("ipset was not prepared, starting without it: %v", ipsetErr))
	}
	var args []string
	if strategy.ManualScope {
		args = resolveTransparentStrategyArgs(strategy.Args, hostlistPath, ipsetPath, filepath.Dir(exePath))
	} else {
		args = applyTransparentScopeArgs(strategy.Args, scopeArgs)
	}
	args = ensureTransparentScopeArgs(args, scopeArgs)
	if zapretPacketDebugEnabled() {
		if debugPath, err := m.prepareDebugLog(strategy.Tag); err == nil && debugPath != "" {
			args = append([]string{"--debug=@" + debugPath}, args...)
			m.log(fmt.Sprintf("%s packet debug log: %s", strategy.Label, debugPath))
		} else if err != nil {
			m.log(fmt.Sprintf("%s packet debug log disabled: %v", strategy.Label, err))
		}
	}
	if err := m.validateZapret2Args(exePath, args); err != nil {
		return nil, nil, fmt.Errorf("%s configuration is invalid: %w", strategy.Label, err)
	}
	m.log(fmt.Sprintf("starting %s: %s %s", strategy.Label, exePath, formatProcessArgs(args)))
	cmd := exec.Command(exePath, args...)
	cmd.Dir = filepath.Dir(exePath)
	stdout, stdoutErr := cmd.StdoutPipe()
	if stdoutErr != nil {
		m.log(fmt.Sprintf("%s stdout pipe disabled: %v", strategy.Label, stdoutErr))
	}
	stderr, stderrErr := cmd.StderrPipe()
	if stderrErr != nil {
		m.log(fmt.Sprintf("%s stderr pipe disabled: %v", strategy.Label, stderrErr))
	}
	configureBackgroundCommand(cmd)
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	attachManagedCmdToJob(cmd, strategy.Label, m.log)
	if stdoutErr == nil {
		go m.logProcessOutput(stdout, strategy.Tag, "OUT")
	}
	if stderrErr == nil {
		go m.logProcessOutput(stderr, strategy.Tag, "ERR")
	}
	exitCh := make(chan error, 1)
	go func() {
		exitCh <- cmd.Wait()
	}()
	return cmd, exitCh, nil
}

// validateZapret2Args asks winws2 to parse the complete generated command line
// without installing a WinDivert filter or starting packet processing. Keeping
// --dry-run last is required by zapret2's parser.
func (m *TransparentBypassManager) validateZapret2Args(exePath string, args []string) error {
	cacheKey := zapret2ValidationKey(exePath, args)
	m.validationMu.Lock()
	_, cached := m.validatedArgSet[cacheKey]
	m.validationMu.Unlock()
	if cached {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	checkArgs := append(append([]string(nil), args...), "--dry-run")
	cmd := exec.CommandContext(ctx, exePath, checkArgs...)
	cmd.Dir = filepath.Dir(exePath)
	configureBackgroundCommand(cmd)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("dry-run timed out: %w", ctx.Err())
	}
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			return err
		}
		return fmt.Errorf("%v: %s", err, detail)
	}
	m.validationMu.Lock()
	if len(m.validatedArgSet) >= 256 {
		clear(m.validatedArgSet)
	}
	m.validatedArgSet[cacheKey] = struct{}{}
	m.validationMu.Unlock()
	return nil
}

func zapret2ValidationKey(exePath string, args []string) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, filepath.Clean(exePath))
	if info, err := os.Stat(exePath); err == nil {
		_, _ = io.WriteString(hash, fmt.Sprintf("\x00%d\x00%d", info.Size(), info.ModTime().UnixNano()))
	}
	for _, arg := range args {
		_, _ = io.WriteString(hash, "\x00"+arg)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (m *TransparentBypassManager) logWinDivertStatus(stage string) {
	if runtime.GOOS != "windows" {
		return
	}
	status, err := nativeWinDivertServiceStatus()
	if err != nil {
		m.log(fmt.Sprintf("WinDivert status %s: %v", stage, err))
		return
	}
	m.log(fmt.Sprintf("WinDivert status %s: %s", stage, status))
}

const winDivertServiceName = "WinDivert"

func cleanupWinDivertServiceIfOwned(roots []string, reason string, logger func(string)) {
	if runtime.GOOS != "windows" {
		return
	}
	cleanupWinDivertServiceNative(roots, reason, logger)
}

// cleanupWinDivertServiceIfOwnedExternal documents the previous sc.exe/reg.exe
// implementation for compatibility investigations. Runtime cleanup uses the
// native service-manager implementation above.
func cleanupWinDivertServiceIfOwnedExternal(roots []string, reason string, logger func(string)) {
	if runtime.GOOS != "windows" {
		return
	}
	log := func(format string, args ...interface{}) {
		if logger != nil {
			logger(fmt.Sprintf(format, args...))
		}
	}
	defer cleanupWinDivertEventLogSourceIfOwned(roots, reason, log)

	output, err := serviceControlOutput("qc", winDivertServiceName)
	outputText := string(output)
	status := compactServiceControlOutput(outputText)
	if err != nil {
		if serviceControlSaysMissing(outputText) {
			log("WinDivert cleanup skipped (%s): service is not installed", reason)
			return
		}
		if status == "" {
			status = "no service output"
		}
		log("WinDivert cleanup skipped (%s): service query failed: %v; %s", reason, err, status)
		return
	}

	binaryPath := parseWinDivertBinaryPath(outputText)
	if binaryPath == "" {
		if registryPath := queryWinDivertRegistryImagePath(); registryPath != "" {
			binaryPath = registryPath
			log("WinDivert cleanup (%s): service path read from registry ImagePath: %s", reason, binaryPath)
		}
	}
	if binaryPath == "" {
		log("WinDivert cleanup skipped (%s): service has no BINARY_PATH_NAME; %s", reason, status)
		return
	}
	if !winDivertBinaryOwnedByRoots(binaryPath, roots) {
		log("WinDivert cleanup skipped (%s): service binary is outside dropo roots: %s", reason, binaryPath)
		return
	}

	log("WinDivert cleanup (%s): stopping owned service at %s", reason, binaryPath)
	if err := runBackgroundCommandWithTimeout(3*time.Second, "sc", "stop", winDivertServiceName); err != nil {
		log("WinDivert cleanup (%s): stop returned %v; continuing with delete", reason, err)
	}
	if err := runBackgroundCommandWithTimeout(3*time.Second, "sc", "delete", winDivertServiceName); err != nil {
		if winDivertServiceMissing() {
			log("WinDivert cleanup (%s): owned service already removed", reason)
			return
		}
		log("WinDivert cleanup (%s): delete failed: %v", reason, err)
		return
	}
	log("WinDivert cleanup (%s): owned service deleted", reason)
}

const winDivertEventLogSourceKey = `HKLM\SYSTEM\CurrentControlSet\Services\EventLog\System\WinDivert`

func cleanupWinDivertEventLogSourceIfOwned(roots []string, reason string, log func(string, ...interface{})) {
	output, err := registryOutput("query", winDivertEventLogSourceKey, "/v", "EventMessageFile")
	outputText := string(output)
	if err != nil {
		if registryOutputSaysMissing(outputText) {
			return
		}
		log("WinDivert EventLog cleanup skipped (%s): query failed: %v", reason, err)
		return
	}

	eventMessageFile := parseRegistryStringValue(outputText, "EventMessageFile")
	if eventMessageFile == "" || !winDivertBinaryOwnedByRoots(eventMessageFile, roots) {
		return
	}
	if err := runBackgroundCommandWithTimeout(3*time.Second, "reg", "delete", winDivertEventLogSourceKey, "/f"); err != nil {
		log("WinDivert EventLog cleanup (%s): delete failed: %v", reason, err)
		return
	}
	log("WinDivert EventLog cleanup (%s): owned source deleted", reason)
}

func winDivertServiceMissing() bool {
	output, err := serviceControlOutput("query", winDivertServiceName)
	return err != nil && serviceControlSaysMissing(string(output))
}

func serviceControlOutput(args ...string) ([]byte, error) {
	cmd := newBackgroundCommand("sc", args...)
	return cmd.CombinedOutput()
}

func registryOutput(args ...string) ([]byte, error) {
	cmd := newBackgroundCommand("reg", args...)
	return cmd.CombinedOutput()
}

func queryWinDivertRegistryImagePath() string {
	output, err := registryOutput("query", `HKLM\SYSTEM\CurrentControlSet\Services\`+winDivertServiceName, "/v", "ImagePath")
	if err != nil {
		return ""
	}
	return normalizeWinDivertBinaryPath(parseRegistryStringValue(string(output), "ImagePath"))
}

func serviceControlSaysMissing(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "1060") ||
		strings.Contains(lower, "does not exist") ||
		strings.Contains(lower, "specified service does not exist")
}

func registryOutputSaysMissing(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "unable to find the specified registry key") ||
		strings.Contains(lower, "unable to find the specified registry value") ||
		strings.Contains(lower, "cannot find") ||
		strings.Contains(lower, "не удается найти") ||
		strings.Contains(lower, "не удалось найти")
}

func parseRegistryStringValue(output, name string) string {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	lowerName := strings.ToLower(name)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		fields := strings.Fields(line)
		if len(fields) < 3 || strings.ToLower(fields[0]) != lowerName {
			continue
		}
		return strings.Join(fields[2:], " ")
	}
	return ""
}

func parseWinDivertBinaryPath(output string) string {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.Contains(strings.ToLower(line), "binary_path_name") {
			continue
		}
		_, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		return normalizeWinDivertBinaryPath(value)
	}
	return ""
}

func normalizeWinDivertBinaryPath(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "\"")
	value = strings.TrimSpace(value)
	for _, prefix := range []string{`\??\`, `\\??\`} {
		value = strings.TrimPrefix(value, prefix)
	}
	value = os.ExpandEnv(strings.TrimSpace(strings.Trim(value, "\"")))
	if value == "" {
		return ""
	}
	return filepath.Clean(value)
}

func winDivertBinaryOwnedByRoots(binaryPath string, roots []string) bool {
	binaryPath = normalizeWinDivertBinaryPath(binaryPath)
	if binaryPath == "" || len(roots) == 0 {
		return false
	}
	baseName := strings.ToLower(filepath.Base(binaryPath))
	if baseName != "windivert64.sys" && baseName != "windivert32.sys" {
		return false
	}
	absBinary, err := filepath.Abs(binaryPath)
	if err != nil {
		return false
	}
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if pathIsInside(absBinary, filepath.Join(absRoot, "bin")) {
			return true
		}
	}
	return false
}

func compactServiceControlOutput(output string) string {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	parts := make([]string, 0, 3)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts = append(parts, line)
		if len(parts) >= 3 {
			break
		}
	}
	return strings.Join(parts, " | ")
}

func (m *TransparentBypassManager) prepareDebugLog(tag string) (string, error) {
	dir := filepath.Join(os.TempDir(), AppName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("zapret-%s.log", safeLogFilePart(tag))
	path := filepath.Join(dir, name)
	_ = os.Remove(path)
	return path, nil
}

func zapretPacketDebugEnabled() bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv("DROPO_ZAPRET_PACKET_DEBUG")))
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func (m *TransparentBypassManager) logProcessOutput(r io.Reader, tag, stream string) {
	if r == nil {
		return
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 4096), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		m.log(fmt.Sprintf("%s %s: %s", tag, stream, line))
	}
	if err := scanner.Err(); err != nil {
		m.log(fmt.Sprintf("%s %s read error: %v", tag, stream, err))
	}
}

func safeLogFilePart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "default"
	}
	return b.String()
}

func formatProcessArgs(args []string) string {
	formatted := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "" {
			formatted = append(formatted, `""`)
			continue
		}
		if strings.ContainsAny(arg, " \t\"") {
			formatted = append(formatted, `"`+strings.ReplaceAll(arg, `"`, `\"`)+`"`)
		} else {
			formatted = append(formatted, arg)
		}
	}
	return strings.Join(formatted, " ")
}

func resolveTransparentStrategyArgs(args []string, hostlistPath string, ipsetPath string, binDir string) []string {
	result := make([]string, 0, len(args))
	binPrefix := binDir
	if binPrefix != "" && !strings.HasSuffix(binPrefix, string(os.PathSeparator)) {
		binPrefix += string(os.PathSeparator)
	}
	for _, arg := range args {
		if strings.Contains(arg, "${HOSTLIST}") {
			if hostlistPath == "" {
				continue
			}
			arg = strings.ReplaceAll(arg, "${HOSTLIST}", hostlistPath)
		}
		if strings.Contains(arg, "${IPSET}") {
			if ipsetPath == "" {
				continue
			}
			arg = strings.ReplaceAll(arg, "${IPSET}", ipsetPath)
		}
		arg = strings.ReplaceAll(arg, "${BIN}", binPrefix)
		result = append(result, arg)
	}
	return result
}

func applyTransparentScopeArgs(args []string, scopeArgs []string) []string {
	result := make([]string, 0, len(args)+len(scopeArgs)*3)
	if len(scopeArgs) == 0 {
		return append(result, args...)
	}

	applied := false
	for _, arg := range args {
		result = append(result, arg)
		if strings.HasPrefix(arg, "--filter-tcp=") || strings.HasPrefix(arg, "--filter-udp=") {
			result = append(result, scopeArgs...)
			applied = true
		}
	}
	if !applied {
		result = append(result, scopeArgs...)
	}
	return result
}

func ensureTransparentScopeArgs(args []string, scopeArgs []string) []string {
	if len(scopeArgs) == 0 || len(args) == 0 {
		return append([]string(nil), args...)
	}

	result := make([]string, 0, len(args)+len(scopeArgs)*3)
	segment := make([]string, 0, len(args))
	flush := func() {
		if len(segment) == 0 {
			return
		}
		result = append(result, scopeTransparentSegment(segment, scopeArgs)...)
		segment = segment[:0]
	}

	for _, arg := range args {
		if arg == "--new" {
			flush()
			segment = append(segment, arg)
			continue
		}
		segment = append(segment, arg)
	}
	flush()
	return result
}

func scopeTransparentSegment(segment []string, scopeArgs []string) []string {
	hasFilter := false
	hasScope := false
	for _, arg := range segment {
		if strings.HasPrefix(arg, "--filter-tcp=") || strings.HasPrefix(arg, "--filter-udp=") {
			hasFilter = true
		}
		if transparentScopeArg(arg) {
			hasScope = true
		}
	}
	if !hasFilter || hasScope {
		return append([]string(nil), segment...)
	}

	result := make([]string, 0, len(segment)+len(scopeArgs))
	inserted := false
	for _, arg := range segment {
		result = append(result, arg)
		if !inserted && (strings.HasPrefix(arg, "--filter-tcp=") || strings.HasPrefix(arg, "--filter-udp=")) {
			result = append(result, scopeArgs...)
			inserted = true
		}
	}
	if !inserted {
		result = append(result, scopeArgs...)
	}
	return result
}

func transparentScopeArg(arg string) bool {
	return arg == "--hostlist" ||
		strings.HasPrefix(arg, "--hostlist=") ||
		strings.HasPrefix(arg, "--hostlist-domains=") ||
		arg == "--ipset" ||
		strings.HasPrefix(arg, "--ipset=")
}

func (m *TransparentBypassManager) ensureHostlist() (string, error) {
	if m.hostlistPath == "" {
		return "", nil
	}
	if err := os.MkdirAll(filepath.Dir(m.hostlistPath), 0755); err != nil {
		return "", err
	}

	domains := make([]string, 0)
	for _, svc := range DefaultFreeAccessServices {
		if svc.RequiresVPN {
			continue
		}
		for _, suffix := range svc.DomainSuffixes {
			normalized := strings.TrimSpace(strings.TrimPrefix(suffix, "."))
			if normalized != "" {
				domains = append(domains, normalized)
			}
		}
	}
	domains = uniqueStrings(domains)
	if len(domains) == 0 {
		return "", nil
	}
	return m.hostlistPath, os.WriteFile(m.hostlistPath, []byte(strings.Join(domains, "\n")+"\n"), 0644)
}

func (m *TransparentBypassManager) ensureIPSet() (string, error) {
	if m.ipsetPath == "" {
		return "", nil
	}
	if err := os.MkdirAll(filepath.Dir(m.ipsetPath), 0755); err != nil {
		return "", err
	}

	ranges := make([]string, 0)
	for _, svc := range DefaultFreeAccessServices {
		if svc.RequiresVPN {
			continue
		}
		for _, cidr := range svc.IPCIDRs {
			normalized := strings.TrimSpace(cidr)
			if normalized != "" {
				ranges = append(ranges, normalized)
			}
		}
	}
	ranges = uniqueStrings(ranges)
	if len(ranges) == 0 {
		_ = os.Remove(m.ipsetPath)
		return "", nil
	}
	return m.ipsetPath, os.WriteFile(m.ipsetPath, []byte(strings.Join(ranges, "\n")+"\n"), 0644)
}

func (m *TransparentBypassManager) watchPersistentStrategy(strategy TransparentFreeAccessStrategy, cmd *exec.Cmd, exitCh <-chan error, stopCh <-chan struct{}) {
	err := <-exitCh
	select {
	case <-stopCh:
		return
	default:
	}

	m.mu.Lock()
	if m.activeCmd == cmd {
		m.activeCmd = nil
		m.activeTag = ""
		m.activeStop = nil
		m.activeLabel = ""
		m.activeArgs = nil
	}
	m.mu.Unlock()
	if err != nil {
		m.log(fmt.Sprintf("%s exited: %v", strategy.Label, err))
		return
	}
	m.log(fmt.Sprintf("%s exited", strategy.Label))
}

func (m *TransparentBypassManager) Stop() {
	m.logWinDivertStatus("before stop")
	m.mu.Lock()
	cmd := m.activeCmd
	m.stopLocked()
	m.activeLabel = ""
	m.activeArgs = nil
	m.mu.Unlock()
	if cmd != nil {
		m.log("transparent method stopped")
	}
	m.logWinDivertStatus("after stop")
	cleanupWinDivertServiceIfOwned([]string{m.basePath}, "transparent method stop", m.log)
	m.logWinDivertStatus("after service cleanup")
}

func (m *TransparentBypassManager) stopLocked() {
	cmd := m.activeCmd
	stopCh := m.activeStop
	m.activeCmd = nil
	m.activeTag = ""
	m.activeStop = nil
	m.activeLabel = ""
	m.activeArgs = nil
	if stopCh != nil {
		close(stopCh)
	}
	if cmd != nil {
		terminateProcessTree(cmd)
	}
}

func waitForTransparentStartup(exitCh <-chan error) error {
	timer := time.NewTimer(transparentStartupWait)
	defer timer.Stop()
	select {
	case err := <-exitCh:
		if err != nil {
			return err
		}
		return fmt.Errorf("process exited during startup")
	case <-timer.C:
		return nil
	}
}

func loopbackPortReady(port int, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func terminateProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if runtime.GOOS == "windows" {
		// os.Process.Kill maps to TerminateProcess on Windows. Managed children
		// are also attached to the process-wide kill-on-close job, so spawning a
		// separate taskkill.exe for every strategy restart is unnecessary.
		_ = cmd.Process.Kill()
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
}
