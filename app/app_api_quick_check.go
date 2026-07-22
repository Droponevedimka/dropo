package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// A working desync answers in well under a second; anything that needs the
	// full window is effectively blocked. Tighter budgets + more workers make
	// the whole sweep finish in a few seconds instead of ~17s.
	clientQuickCheckTimeout        = 45 * time.Second
	clientQuickCheckRequestTimeout = 5 * time.Second
	clientQuickCheckConcurrency    = 16
	// One cheap retry filters out a single transient reset while a trial plan is
	// being applied, so a service isn't mislabeled as
	// blocked — and so it doesn't spuriously trigger strategy maintenance.
	clientQuickCheckRetryDelay   = 300 * time.Millisecond
	clientQuickCheckRetryTimeout = 2 * time.Second
)

type clientQuickCheckService struct {
	Index    int
	Name     string
	URL      string
	Category string
	Regional bool
}

type clientQuickHTTPResult struct {
	Success bool
	Status  int
	TimeMS  int64
	Error   string
}

type clientQuickCheckResult struct {
	Index        int    `json:"index"`
	Name         string `json:"name"`
	Category     string `json:"category"`
	URL          string `json:"url"`
	StatusText   string `json:"statusText"`
	Success      bool   `json:"success"`
	ProxyRescued bool   `json:"proxyRescued"`

	NormalSuccess bool   `json:"normalSuccess"`
	NormalStatus  int    `json:"normalStatus"`
	NormalTimeMS  int64  `json:"normalTimeMs"`
	NormalError   string `json:"normalError,omitempty"`

	ProxyChecked bool   `json:"proxyChecked"`
	ProxySuccess bool   `json:"proxySuccess,omitempty"`
	ProxyStatus  int    `json:"proxyStatus,omitempty"`
	ProxyTimeMS  int64  `json:"proxyTimeMs,omitempty"`
	ProxyError   string `json:"proxyError,omitempty"`
	Regional     bool   `json:"regional,omitempty"`
}

var clientQuickCheckServices = []clientQuickCheckService{
	{Index: 0, Name: "Yandex", URL: "https://ya.ru", Category: "Direct-RU"},
	{Index: 1, Name: "Yandex Mail", URL: "https://mail.yandex.ru", Category: "Direct-RU"},
	{Index: 2, Name: "VK", URL: "https://vk.com", Category: "Direct-RU"},
	{Index: 3, Name: "Ozon", URL: "https://www.ozon.ru", Category: "Direct-RU"},
	{Index: 4, Name: "Sber", URL: "https://www.sberbank.ru", Category: "Direct-RU"},
	{Index: 5, Name: "Gosuslugi", URL: "https://www.gosuslugi.ru", Category: "Direct-RU", Regional: true},
	{Index: 6, Name: "Rutube", URL: "https://rutube.ru", Category: "Direct-RU"},
	{Index: 7, Name: "Habr", URL: "https://habr.com", Category: "Direct-RU"},
	{Index: 8, Name: "Google", URL: "https://www.google.com", Category: "Direct-Foreign"},
	{Index: 9, Name: "GitHub", URL: "https://github.com", Category: "Direct-Foreign"},
	{Index: 10, Name: "Wikipedia", URL: "https://www.wikipedia.org", Category: "Direct-Foreign"},
	{Index: 11, Name: "StackOverflow", URL: "https://stackoverflow.com", Category: "Direct-Foreign"},
	{Index: 12, Name: "Discord", URL: "https://discord.com", Category: "Blocked"},
	{Index: 13, Name: "Discord API", URL: "https://discord.com/api/v10/gateway", Category: "Blocked"},
	{Index: 14, Name: "Discord CDN", URL: "https://cdn.discordapp.com", Category: "Blocked"},
	{Index: 15, Name: "YouTube", URL: "https://www.youtube.com", Category: "Blocked"},
	{Index: 16, Name: "YouTube API", URL: "https://youtubei.googleapis.com", Category: "Blocked"},
	{Index: 17, Name: "YouTube Images", URL: "https://i.ytimg.com/generate_204", Category: "Blocked"},
	{Index: 18, Name: "YouTube video", URL: "https://redirector.googlevideo.com", Category: "Blocked"},
	{Index: 19, Name: "Instagram", URL: "https://www.instagram.com", Category: "Blocked"},
	{Index: 20, Name: "Facebook", URL: "https://www.facebook.com", Category: "Blocked"},
	{Index: 21, Name: "X", URL: "https://x.com", Category: "Blocked"},
	{Index: 22, Name: "LinkedIn", URL: "https://www.linkedin.com", Category: "Blocked"},
	{Index: 23, Name: "Spotify", URL: "https://open.spotify.com", Category: "Blocked"},
	{Index: 24, Name: "Twitch", URL: "https://www.twitch.tv", Category: "Blocked"},
	{Index: 25, Name: "Telegram", URL: "https://telegram.org", Category: "Blocked"},
	{Index: 26, Name: "Signal", URL: "https://signal.org", Category: "Blocked"},
	{Index: 27, Name: "WhatsApp Web", URL: "https://web.whatsapp.com", Category: "Blocked"},
	{Index: 28, Name: "WhatsApp CDN", URL: "https://static.whatsapp.net", Category: "Blocked"},
	{Index: 29, Name: "FaceTime", URL: "https://facetime.apple.com", Category: "Blocked"},
	{Index: 30, Name: "Viber", URL: "https://www.viber.com", Category: "Blocked"},
	{Index: 31, Name: "Snapchat", URL: "https://www.snapchat.com", Category: "Blocked"},
	{Index: 32, Name: "TikTok", URL: "https://www.tiktok.com", Category: "Blocked"},
	{Index: 33, Name: "ChatGPT", URL: "https://chatgpt.com", Category: "AI-VPNOnly"},
	{Index: 34, Name: "OpenAI API", URL: "https://api.openai.com", Category: "AI-VPNOnly"},
	{Index: 35, Name: "Copilot proxy", URL: "https://copilot-proxy.githubusercontent.com", Category: "AI-VPNOnly"},
	{Index: 36, Name: "Cursor API", URL: "https://api2.cursor.sh", Category: "AI-VPNOnly"},
	{Index: 37, Name: "Canva", URL: "https://www.canva.com", Category: "VPNOnly"},
	{Index: 38, Name: "Notion", URL: "https://www.notion.com", Category: "VPNOnly"},
	{Index: 39, Name: "Slack", URL: "https://slack.com", Category: "VPNOnly"},
	{Index: 40, Name: "Miro", URL: "https://miro.com", Category: "VPNOnly"},
	{Index: 41, Name: "Wix", URL: "https://www.wix.com", Category: "VPNOnly"},
	{Index: 42, Name: "Coda", URL: "https://coda.io", Category: "VPNOnly"},
	{Index: 43, Name: "Grammarly", URL: "https://www.grammarly.com", Category: "VPNOnly"},
	{Index: 46, Name: "Docker Hub", URL: "https://registry-1.docker.io/v2/", Category: "VPNOnly"},
	{Index: 47, Name: "ClickUp", URL: "https://app.clickup.com", Category: "VPNOnly"},
	{Index: 48, Name: "Manychat", URL: "https://app.manychat.com", Category: "VPNOnly"},
	{Index: 49, Name: "Help Scout", URL: "https://secure.helpscout.net", Category: "VPNOnly"},
	{Index: 50, Name: "Trello", URL: "https://trello.com", Category: "VPNOnly"},
	{Index: 51, Name: "Bitbucket", URL: "https://bitbucket.org", Category: "VPNOnly"},
}

// RunClientQuickCheck performs the in-app service availability check. It is
// intentionally native and event-driven: the UI button should not open
// PowerShell, block on a long combined output, or write the full result to app logs.
func (a *App) RunClientQuickCheck(deep bool) map[string]interface{} {
	a.waitForInit()

	startedAt := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), clientQuickCheckTimeout)
	defer cancel()

	a.ensureTransparentBypassForClientQuickCheck()

	proxyURL := a.quickCheckProxyURL()
	services := append([]clientQuickCheckService(nil), clientQuickCheckServices...)
	results := make([]clientQuickCheckResult, len(services))
	directClient := newQuickCheckHTTPClient(nil)
	var proxyClient *http.Client
	if proxyURL != "" {
		if proxyParsed, err := url.Parse(proxyURL); err == nil {
			proxyClient = newQuickCheckHTTPClient(http.ProxyURL(proxyParsed))
		}
	}

	a.emitClientQuickCheck("client-check-start", map[string]interface{}{
		"total":    len(services),
		"proxyUrl": proxyURL,
	})

	jobs := make(chan clientQuickCheckService)
	var wg sync.WaitGroup
	workers := clientQuickCheckConcurrency
	if workers > len(services) {
		workers = len(services)
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for svc := range jobs {
				select {
				case <-ctx.Done():
					return
				default:
				}
				a.emitClientQuickCheck("client-check-progress", map[string]interface{}{
					"index": svc.Index,
					"name":  svc.Name,
					"url":   svc.URL,
				})
				result := runSingleClientQuickCheck(ctx, svc, directClient, proxyClient)
				results[svc.Index] = result
				a.emitClientQuickCheck("client-check-service", result)
			}
		}()
	}

enqueue:
	for _, svc := range services {
		select {
		case <-ctx.Done():
			break enqueue
		case jobs <- svc:
		}
	}
	close(jobs)
	wg.Wait()

	duration := time.Since(startedAt)
	completed := 0
	okCount := 0
	proxyRescued := 0
	directFailed := 0
	blockedFailed := 0
	for _, result := range results {
		if result.Name == "" {
			continue
		}
		completed++
		if result.Success {
			okCount++
		}
		if result.ProxyRescued {
			proxyRescued++
		}
		if strings.HasPrefix(result.Category, "Direct") && !result.Success {
			directFailed++
		}
		if !strings.HasPrefix(result.Category, "Direct") && !result.Success {
			blockedFailed++
		}
	}

	sort.SliceStable(results, func(i, j int) bool {
		return results[i].Index < results[j].Index
	})
	output := formatClientQuickCheckOutput(results, proxyURL, duration, directFailed, blockedFailed, proxyRescued)
	success := completed == len(services) && blockedFailed == 0 && directFailed == 0
	if ctx.Err() == context.DeadlineExceeded {
		success = false
	}

	payload := map[string]interface{}{
		"success":       success,
		"durationMs":    duration.Milliseconds(),
		"services":      results,
		"output":        output,
		"total":         len(services),
		"completed":     completed,
		"okCount":       okCount,
		"failedCount":   completed - okCount,
		"proxyRescued":  proxyRescued,
		"directFailed":  directFailed,
		"blockedFailed": blockedFailed,
		"proxyUrl":      proxyURL,
	}
	if ctx.Err() != nil {
		payload["error"] = ctx.Err().Error()
	}
	a.handleClientQuickCheckFailures(results)
	a.emitClientQuickCheck("client-check-done", payload)
	return payload
}

func (a *App) ensureTransparentBypassForClientQuickCheck() {
	if a == nil || a.trafficEngine == nil || a.storage == nil {
		return
	}
	a.mu.Lock()
	running := a.isRunning
	a.mu.Unlock()
	if !running {
		return
	}
	settings := a.storage.GetAppSettings()
	if !FreeMethodsAllowed(settings) || a.trafficEngine.ActiveTag() != "" {
		return
	}
	if err := a.startComposedTransparentEngine(""); err != nil {
		a.writeLog(fmt.Sprintf("[ClientCheck] failed to restore Windows Unified per-service engine before service check: %v", err))
		return
	}
	a.writeLog("[ClientCheck] Windows Unified per-service engine checked before service test")
}

func runSingleClientQuickCheck(ctx context.Context, svc clientQuickCheckService, directClient *http.Client, proxyClient *http.Client) clientQuickCheckResult {
	normal := invokeQuickCheckURL(ctx, directClient, svc.URL)
	if !normal.Success && !svc.Regional && quickCheckRetryableError(normal.Error) {
		select {
		case <-ctx.Done():
		case <-time.After(clientQuickCheckRetryDelay):
			retryCtx, cancel := context.WithTimeout(ctx, clientQuickCheckRetryTimeout)
			retry := invokeQuickCheckURL(retryCtx, directClient, svc.URL)
			cancel()
			if retry.Success {
				normal = retry
			}
		}
	}
	var proxy clientQuickHTTPResult
	proxyChecked := proxyClient != nil && !svc.Regional
	if proxyChecked && !normal.Success {
		proxy = invokeQuickCheckURL(ctx, proxyClient, svc.URL)
	}

	statusText := "FAIL"
	success := false
	proxyRescued := false
	if normal.Success {
		statusText = "OK"
		success = true
	} else if svc.Regional {
		statusText = "REGION_LIMIT"
		success = true
	} else if proxyChecked && proxy.Success {
		if strings.Contains(svc.Category, "VPNOnly") {
			statusText = "VPN_PROXY_OK"
		} else {
			statusText = "TUN_FAIL_PROXY_OK"
		}
		success = true
		proxyRescued = true
	}

	return clientQuickCheckResult{
		Index:         svc.Index,
		Name:          svc.Name,
		Category:      svc.Category,
		URL:           svc.URL,
		StatusText:    statusText,
		Success:       success,
		ProxyRescued:  proxyRescued,
		NormalSuccess: normal.Success,
		NormalStatus:  normal.Status,
		NormalTimeMS:  normal.TimeMS,
		NormalError:   normal.Error,
		ProxyChecked:  proxyChecked,
		ProxySuccess:  proxy.Success,
		ProxyStatus:   proxy.Status,
		ProxyTimeMS:   proxy.TimeMS,
		ProxyError:    proxy.Error,
		Regional:      svc.Regional,
	}
}

// quickCheckRetryableError reports whether an error looks like a transient
// connection drop (reset/abort/timeout/EOF) that is worth a single quick retry,
// as opposed to a hard failure (e.g. DNS) where retrying just wastes time.
func quickCheckRetryableError(errText string) bool {
	if errText == "" {
		return false
	}
	lower := strings.ToLower(errText)
	for _, needle := range []string{
		"forcibly closed", "connection reset", "reset by peer", "connection was aborted",
		"deadline exceeded", "timeout", "eof", "broken pipe",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func invokeQuickCheckURL(ctx context.Context, client *http.Client, target string) clientQuickHTTPResult {
	startedAt := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return clientQuickHTTPResult{TimeMS: time.Since(startedAt).Milliseconds(), Error: err.Error()}
	}
	req.Header.Set("User-Agent", "dropo-client-quick-check/2.0")
	resp, err := client.Do(req)
	elapsed := time.Since(startedAt).Milliseconds()
	if err != nil {
		return clientQuickHTTPResult{TimeMS: elapsed, Error: compactProbeError(err)}
	}
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, 512)
	return clientQuickHTTPResult{
		Success: resp.StatusCode >= 200 && resp.StatusCode < 500,
		Status:  resp.StatusCode,
		TimeMS:  elapsed,
	}
}

func newQuickCheckHTTPClient(proxy func(*http.Request) (*url.URL, error)) *http.Client {
	transport := &http.Transport{
		Proxy: proxy,
		DialContext: (&net.Dialer{
			Timeout:   clientQuickCheckRequestTimeout,
			KeepAlive: 15 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   clientQuickCheckRequestTimeout,
		ResponseHeaderTimeout: clientQuickCheckRequestTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &http.Client{
		Timeout:   clientQuickCheckRequestTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

func (a *App) quickCheckProxyURL() string {
	configPath := ""
	if a.storage != nil {
		configPath = a.storage.ActiveConfigFilePath()
	}
	if configPath == "" && a.basePath != "" {
		configPath = filepath.Join(a.basePath, ResourcesFolder, "active_config.json")
	}
	if configPath == "" {
		return ""
	}
	config, err := readJSONConfig(configPath)
	if err != nil {
		return ""
	}
	port := quickCheckMixedInboundPort(config)
	if port <= 0 || !loopbackPortReady(port, 250*time.Millisecond) {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

func quickCheckMixedInboundPort(config map[string]interface{}) int {
	inbounds, _ := config["inbounds"].([]interface{})
	for _, inbound := range inbounds {
		inboundMap, ok := inbound.(map[string]interface{})
		if !ok || inboundMap["type"] != "mixed" {
			continue
		}
		return mixedInboundPort(inboundMap["listen_port"])
	}
	return 0
}

func formatClientQuickCheckOutput(results []clientQuickCheckResult, proxyURL string, duration time.Duration, directFailed, blockedFailed, proxyRescued int) string {
	var b strings.Builder
	b.WriteString("dropo client quick check\n")
	if proxyURL != "" {
		b.WriteString("Mixed proxy: " + proxyURL + "\n")
	} else {
		b.WriteString("Mixed proxy: not available\n")
	}
	b.WriteString("\n")
	for _, result := range results {
		if result.Name == "" {
			continue
		}
		fmt.Fprintf(&b, "Testing %-18s %s", result.Name, result.StatusText)
		if result.NormalTimeMS > 0 {
			fmt.Fprintf(&b, " (%d ms)", result.NormalTimeMS)
		}
		if result.ProxyRescued && result.ProxyTimeMS > 0 {
			fmt.Fprintf(&b, " proxy=%d ms", result.ProxyTimeMS)
		}
		if !result.Success && result.NormalError != "" {
			fmt.Fprintf(&b, " - %s", result.NormalError)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\nSummary\n")
	fmt.Fprintf(&b, "  Duration:      %s\n", duration.Round(time.Millisecond))
	fmt.Fprintf(&b, "  Normal failed: %d/%d\n", directFailed+blockedFailed, len(results))
	fmt.Fprintf(&b, "  Proxy rescued: %d\n", proxyRescued)
	fmt.Fprintf(&b, "  Direct failed: %d\n", directFailed)
	fmt.Fprintf(&b, "  Blocked failed:%d\n", blockedFailed)
	return b.String()
}

func (a *App) emitClientQuickCheck(event string, payload interface{}) {
	if a == nil || a.isShuttingDown() {
		return
	}
	a.emitEvent(event, payload)
}
