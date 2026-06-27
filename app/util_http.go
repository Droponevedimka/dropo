// Package main provides utility functions for dropo.
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// HTTPClient is a shared HTTP client with default timeout.
var HTTPClient = &http.Client{
	Timeout: DefaultHTTPTimeout,
}

// ShortHTTPClient is a HTTP client with shorter timeout.
var ShortHTTPClient = &http.Client{
	Timeout: ShortHTTPTimeout,
}

// LongHTTPClient is a HTTP client for longer operations.
var LongHTTPClient = &http.Client{
	Timeout: LongHTTPTimeout,
}

// ClashHTTPClient is a HTTP client for Clash API requests.
var ClashHTTPClient = &http.Client{
	Timeout: ClashAPITimeout,
}

// httpGet performs a GET request with context and timeout.
func httpGet(ctx context.Context, url string) ([]byte, error) {
	return httpGetWithClient(ctx, url, HTTPClient)
}

// httpGetWithClient performs a GET request with specified client.
func httpGetWithClient(ctx context.Context, url string, client *http.Client) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	return body, nil
}

// httpGetWithTimeout performs a GET request with custom timeout.
func httpGetWithTimeout(url string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	client := &http.Client{Timeout: timeout}
	return httpGetWithClient(ctx, url, client)
}

// readFile reads file content with error handling.
func readFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", path, err)
	}
	return data, nil
}

// writeFile writes content to file with error handling.
func writeFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write file %s: %w", path, err)
	}
	return nil
}

// NOTE: fileExists is already defined in app.go, don't duplicate

// ensureDir creates directory if it doesn't exist.
func ensureDir(path string) error {
	return os.MkdirAll(path, 0755)
}

// copyFile copies a file from src to dst.
func copyFile(src, dst string) error {
	data, err := readFile(src)
	if err != nil {
		return err
	}
	return writeFile(dst, data)
}

// formatBytes formats bytes to human-readable string.
func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// formatDuration formats duration to human-readable string.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d сек", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d мин", int(d.Minutes()))
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	if mins == 0 {
		return fmt.Sprintf("%d ч", hours)
	}
	return fmt.Sprintf("%d ч %d мин", hours, mins)
}

// formatDurationEN formats duration to English human-readable string.
func formatDurationEN(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d sec", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	if mins == 0 {
		return fmt.Sprintf("%d h", hours)
	}
	return fmt.Sprintf("%d h %d min", hours, mins)
}

// NOTE: min and max functions are already defined in app.go and subscription.go
// Don't duplicate them here

// truncateString truncates string to maxLen with ellipsis.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
