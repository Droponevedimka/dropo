package main

import (
	"strings"
	"testing"
)

func TestWriteLogFeedsUILogBuffer(t *testing.T) {
	app := &App{logBuffer: make([]string, 0, MaxLogBufferSize)}
	app.writeLog("[Zapret2] WinDivert status after start: STATE RUNNING")

	result := app.GetLogs(10)
	logs, ok := result["logs"].([]string)
	if !ok {
		t.Fatalf("logs type = %T, want []string", result["logs"])
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "WinDivert status after start") {
		t.Fatalf("UI logs do not contain detailed WinDivert line: %q", joined)
	}
}
