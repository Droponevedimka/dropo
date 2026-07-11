package main

// Logging methods for dropo.
// This file contains all logging-related operations

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// setupLogPath sets up the log file path
func (a *App) setupLogPath() {
	if a.logPath != "" && a.tempLogPath != "" {
		return
	}

	var logDir string

	switch runtime.GOOS {
	case "windows":
		// %LOCALAPPDATA%\dropo\logs
		logDir = filepath.Join(os.Getenv("LOCALAPPDATA"), AppDataDirName, "logs")
	case "darwin":
		// ~/Library/Logs/dropo
		home, _ := os.UserHomeDir()
		logDir = filepath.Join(home, "Library", "Logs", AppDataDirName)
	default:
		// ~/.local/share/dropo/logs
		home, _ := os.UserHomeDir()
		logDir = filepath.Join(home, ".local", "share", AppDataDirName, "logs")
	}

	sessionName := "dropo-" + time.Now().Format("20060102-150405") + ".log"

	os.MkdirAll(logDir, 0755)
	a.logPath = filepath.Join(logDir, sessionName)

	tempLogDir := filepath.Join(os.TempDir(), AppDataDirName)
	if err := os.MkdirAll(tempLogDir, 0755); err == nil {
		a.tempLogPath = filepath.Join(tempLogDir, sessionName)
	}
}

// openLogFile opens log file with rotation
func (a *App) openLogFile() error {
	a.logFileMu.Lock()
	defer a.logFileMu.Unlock()

	if a.logFile != nil {
		return nil
	}

	// Check existing file size and rotate if needed
	if err := a.rotateLogIfNeeded(); err != nil {
		// Not critical, continue
	}

	var err error
	a.logFile, err = os.OpenFile(a.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	separator := fmt.Sprintf("\n=== Application Session Started: %s ===\nLog file: %s\n", timestamp, a.logPath)
	a.logFile.WriteString(separator)
	a.writeTempLogLineLocked(separator)

	return nil
}

// rotateLogIfNeeded checks log size and truncates if needed
func (a *App) rotateLogIfNeeded() error {
	info, err := os.Stat(a.logPath)
	if err != nil {
		return nil // File doesn't exist - ok
	}

	if info.Size() < MaxLogSize {
		return nil // Size is ok
	}

	// Read last TruncateToSize bytes
	file, err := os.Open(a.logPath)
	if err != nil {
		return err
	}
	defer file.Close()

	// Seek to position (size - TruncateToSize)
	offset := info.Size() - TruncateToSize
	if offset < 0 {
		offset = 0
	}
	file.Seek(offset, 0)

	// Skip first incomplete line
	reader := bufio.NewReader(file)
	reader.ReadString('\n')

	// Read remainder
	remainingData, err := io.ReadAll(reader)
	if err != nil {
		return err
	}

	// Rewrite file
	file.Close()
	err = os.WriteFile(a.logPath, remainingData, 0644)
	if err != nil {
		return err
	}

	// Add rotation marker
	marker := fmt.Sprintf("=== Log rotated at %s (old logs truncated) ===\n",
		time.Now().Format("2006-01-02 15:04:05"))
	f, _ := os.OpenFile(a.logPath, os.O_APPEND|os.O_WRONLY, 0644)
	if f != nil {
		f.WriteString(marker)
		f.Close()
	}

	return nil
}

// closeLogFile closes log file
func (a *App) closeLogFile() {
	a.logFileMu.Lock()
	defer a.logFileMu.Unlock()

	if a.logFile != nil {
		timestamp := time.Now().Format("2006-01-02 15:04:05")
		separator := fmt.Sprintf("=== Application Session Ended: %s ===\n", timestamp)
		a.logFile.WriteString(separator)
		a.writeTempLogLineLocked(separator)
		a.logFile.Close()
		a.logFile = nil
	}
}

// writeLog writes to log file
func (a *App) writeLog(message string) {
	a.logFileMu.Lock()
	defer a.logFileMu.Unlock()

	line := fmt.Sprintf("[%s] %s\n", time.Now().Format("15:04:05"), message)
	a.writeTempLogLineLocked(line)
	if a.logFile != nil {
		a.logFile.WriteString(line)
	}
	a.addLogBufferEntry(strings.TrimRight(line, "\r\n"))
}

func (a *App) writeTempLogLineLocked(line string) {
	if a.tempLogPath == "" {
		return
	}
	if err := rotatePlainLogIfNeeded(a.tempLogPath); err != nil {
		return
	}
	f, err := os.OpenFile(a.tempLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}

func rotatePlainLogIfNeeded(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if info.Size() < MaxLogSize {
		return nil
	}

	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	offset := info.Size() - TruncateToSize
	if offset < 0 {
		offset = 0
	}
	if _, err := file.Seek(offset, 0); err != nil {
		return err
	}
	reader := bufio.NewReader(file)
	_, _ = reader.ReadString('\n')
	remainingData, err := io.ReadAll(reader)
	if err != nil {
		return err
	}

	marker := fmt.Sprintf("=== Temp log rotated at %s ===\n", time.Now().Format("2006-01-02 15:04:05"))
	return os.WriteFile(path, append([]byte(marker), remainingData...), 0644)
}

// AddToLogBuffer adds message to log buffer for UI
func (a *App) AddToLogBuffer(message string) {
	timestamp := time.Now().Format("15:04:05")
	a.addLogBufferEntry(fmt.Sprintf("[%s] %s", timestamp, message))
}

func (a *App) addLogBufferEntry(entry string) {
	if strings.TrimSpace(entry) == "" {
		return
	}
	a.logBufferMu.Lock()
	defer a.logBufferMu.Unlock()

	// Limit buffer size
	if len(a.logBuffer) >= MaxLogBufferSize {
		a.logBuffer = a.logBuffer[100:] // Remove first 100 entries
	}

	a.logBuffer = append(a.logBuffer, entry)
}

// GetLogs returns logs from buffer (API for frontend)
func (a *App) GetLogs(lastN int) map[string]interface{} {
	a.logBufferMu.RLock()
	buffer := append([]string(nil), a.logBuffer...)
	a.logBufferMu.RUnlock()

	if len(buffer) == 0 && a.logPath != "" {
		if fileLogs, err := readLastLogLines(a.logPath, lastN); err == nil && len(fileLogs) > 0 {
			buffer = fileLogs
		}
	}

	if lastN <= 0 || lastN > len(buffer) {
		lastN = len(buffer)
	}
	// Return last N entries
	startIdx := len(buffer) - lastN
	if startIdx < 0 {
		startIdx = 0
	}

	logs := make([]string, lastN)
	copy(logs, buffer[startIdx:])

	return map[string]interface{}{
		"success": true,
		"logs":    logs,
		"total":   len(buffer),
	}
}

func readLastLogLines(path string, lastN int) ([]string, error) {
	if lastN <= 0 {
		lastN = MaxLogBufferSize
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			filtered = append(filtered, line)
		}
	}
	if len(filtered) > lastN {
		filtered = filtered[len(filtered)-lastN:]
	}
	return filtered, nil
}

// ClearLogs clears log buffer
func (a *App) ClearLogs() map[string]interface{} {
	a.logBufferMu.Lock()
	defer a.logBufferMu.Unlock()

	a.logBuffer = make([]string, 0, MaxLogBufferSize)

	return map[string]interface{}{
		"success": true,
		"message": "Логи очищены",
	}
}
