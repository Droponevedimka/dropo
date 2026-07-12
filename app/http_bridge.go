package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"strings"
)

type bridgeCallRequest struct {
	Method string            `json:"method"`
	Args   []json.RawMessage `json:"args"`
}

const maxBridgeCallBodyBytes = 2 << 20

// bridgeCallableMethods is the complete public RPC surface exposed to the
// Flutter UI. Keeping this list explicit is important because App also has
// exported maintenance helpers which run with the core's elevated token and
// must never become remotely callable just because they are exported in Go.
var bridgeCallableMethods = map[string]struct{}{
	"AddWireGuard":              {},
	"CaptureDPIFingerprint":     {},
	"CheckExternalVPNConflicts": {},
	"CheckForUpdates":           {},
	"CreateProfile":             {},
	"DeleteProfile":             {},
	"DeleteWireGuard":           {},
	"GetAppConfig":              {},
	"GetBypassRouteSummary":     {},
	"GetCurrentSubscription":    {},
	"GetFreeAccessConfig":       {},
	"GetHideRuTraffic":          {},
	"GetNetworkMode":            {},
	"GetProfiles":               {},
	"GetRoutingMode":            {},
	"GetTrafficStats":           {},
	"GetWireGuardConfig":        {},
	"GetWireGuardList":          {},
	"HideWindow":                {},
	"OpenConfigFolder":          {},
	"OpenExternalLink":          {},
	"OpenFingerprintFolder":     {},
	"OpenLogs":                  {},
	"ParseWireGuardConfigAPI":   {},
	"RemoveVPNSubscription":     {},
	"ResetTrafficStats":         {},
	"ResolveAutoStartPrompt":    {},
	"RunClientQuickCheck":       {},
	"SaveAppConfig":             {},
	"SetActiveProfile":          {},
	"SetDisableFreeAccess":      {},
	"SetHideRuTraffic":          {},
	"SetNetworkMode":            {},
	"SetRoutingMode":            {},
	"SetVPNSubscription":        {},
	"ShowWindow":                {},
	"TestVPNConnection":         {},
	"UpdateProfile":             {},
	"UpdateWireGuard":           {},
}

func newBridgeMux(app *App, token string) http.Handler {
	mux := http.NewServeMux()
	handle := func(path string, fn http.HandlerFunc) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			setBridgeHeaders(w)
			// Reject anything whose Host header is not loopback. The bridge only
			// ever serves the co-located Flutter UI over 127.0.0.1, so a request
			// arriving with any other Host (e.g. attacker.example resolved to
			// 127.0.0.1) is a DNS-rebinding attempt and must be refused.
			if !hostHeaderLoopback(r.Host) {
				writeBridgeError(w, http.StatusForbidden, "invalid host")
				return
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			fn(w, r)
		})
	}
	// guarded wraps handlers for state-changing endpoints behind the bridge
	// token. Read-only endpoints registered via handle() stay unauthenticated.
	guarded := func(path string, fn http.HandlerFunc) {
		handle(path, func(w http.ResponseWriter, r *http.Request) {
			if !bridgeTokenAuthorized(r, token) {
				writeBridgeError(w, http.StatusUnauthorized, "missing or invalid "+bridgeAuthHeader)
				return
			}
			fn(w, r)
		})
	}

	handle("/api/info", func(w http.ResponseWriter, r *http.Request) {
		writeBridgeJSON(w, map[string]interface{}{
			"success":      true,
			"bridge":       "dropo-core",
			"version":      GetVersionInfo(),
			"dependencies": app.DependenciesStatus(),
		})
	})

	handle("/api/status", func(w http.ResponseWriter, r *http.Request) {
		status := app.GetStatus()
		status["success"] = true
		status["dependencies"] = app.DependenciesStatus()
		status["version"] = GetVersionInfo()
		writeBridgeJSON(w, status)
	})

	guarded("/api/connect", func(w http.ResponseWriter, r *http.Request) {
		if requireMethod(w, r, http.MethodPost) {
			writeBridgeJSON(w, app.Start())
		}
	})

	guarded("/api/disconnect", func(w http.ResponseWriter, r *http.Request) {
		if requireMethod(w, r, http.MethodPost) {
			writeBridgeJSON(w, app.Stop())
		}
	})

	handle("/api/dependencies/status", func(w http.ResponseWriter, r *http.Request) {
		writeBridgeJSON(w, app.DependenciesStatus())
	})

	guarded("/api/dependencies/download", func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		if err := app.DownloadDependencies(); err != nil {
			writeBridgeJSON(w, map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		writeBridgeJSON(w, map[string]interface{}{"success": true, "dependencies": app.DependenciesStatus()})
	})

	handle("/api/events", func(w http.ResponseWriter, r *http.Request) {
		since, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
		writeBridgeJSON(w, map[string]interface{}{
			"success": true,
			"events":  app.eventSnapshot(since),
		})
	})

	// Logs can carry sensitive detail (paths, routing diagnostics, subscription
	// operations), so unlike the health-probe GETs they require the bridge token.
	guarded("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		lastN, _ := strconv.Atoi(r.URL.Query().Get("lastN"))
		if lastN <= 0 {
			lastN = 200
		}
		writeBridgeJSON(w, app.GetLogs(lastN))
	})

	guarded("/api/tray/ensure", func(w http.ResponseWriter, r *http.Request) {
		if requireMethod(w, r, http.MethodPost) {
			writeBridgeJSON(w, app.EnsureTray())
		}
	})

	guarded("/api/call", func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBridgeCallBodyBytes)
		var req bridgeCallRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeBridgeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		result, err := callAppMethod(app, req)
		if err != nil {
			writeBridgeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeBridgeJSON(w, result)
	})

	guarded("/api/quit", func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		writeBridgeJSON(w, app.PrepareQuit())
	})

	guarded("/api/quit/finalize", func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		writeBridgeJSON(w, map[string]interface{}{"success": true})
		go app.FinalizeQuit()
	})

	return mux
}

func callAppMethod(app *App, req bridgeCallRequest) (interface{}, error) {
	if strings.TrimSpace(req.Method) == "" {
		return nil, fmt.Errorf("method is required")
	}
	if _, allowed := bridgeCallableMethods[req.Method]; !allowed {
		return nil, fmt.Errorf("method is not available through the bridge: %s", req.Method)
	}
	method := reflect.ValueOf(app).MethodByName(req.Method)
	if !method.IsValid() {
		return nil, fmt.Errorf("unknown method: %s", req.Method)
	}
	mt := method.Type()
	if mt.NumIn() != len(req.Args) {
		return nil, fmt.Errorf("%s expects %d args, got %d", req.Method, mt.NumIn(), len(req.Args))
	}

	args := make([]reflect.Value, 0, mt.NumIn())
	for i := 0; i < mt.NumIn(); i++ {
		value, err := decodeBridgeArg(req.Args[i], mt.In(i))
		if err != nil {
			return nil, fmt.Errorf("arg %d: %w", i, err)
		}
		args = append(args, value)
	}

	values := method.Call(args)
	if len(values) == 0 {
		return map[string]interface{}{"success": true}, nil
	}
	if len(values) == 1 {
		v := values[0].Interface()
		if err, ok := v.(error); ok {
			if err != nil {
				return map[string]interface{}{"success": false, "error": err.Error()}, nil
			}
			return map[string]interface{}{"success": true}, nil
		}
		return v, nil
	}
	out := make([]interface{}, len(values))
	for i, value := range values {
		out[i] = value.Interface()
	}
	return out, nil
}

func decodeBridgeArg(raw json.RawMessage, target reflect.Type) (reflect.Value, error) {
	if len(raw) == 0 {
		return reflect.Zero(target), nil
	}
	if target.Kind() == reflect.Interface && target.NumMethod() == 0 {
		var v interface{}
		if err := json.Unmarshal(raw, &v); err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(v), nil
	}
	value := reflect.New(target)
	if err := json.Unmarshal(raw, value.Interface()); err != nil {
		return reflect.Value{}, err
	}
	return value.Elem(), nil
}

func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	writeBridgeError(w, http.StatusMethodNotAllowed, method+" required")
	return false
}

// hostHeaderLoopback reports whether the request Host targets the local machine.
// The native Flutter UI always connects to 127.0.0.1/localhost, so only loopback
// hosts (any port) are accepted; a mismatch signals DNS-rebinding. An empty Host
// (HTTP/1.0 tooling) is allowed since rebinding always carries a hostname.
func hostHeaderLoopback(host string) bool {
	if host == "" {
		return true
	}
	hostname := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostname = h
	}
	hostname = strings.TrimSuffix(strings.TrimPrefix(hostname, "["), "]")
	switch strings.ToLower(hostname) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	if ip := net.ParseIP(hostname); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// setBridgeHeaders sets the response content type. The bridge intentionally does
// NOT emit Access-Control-Allow-Origin: the co-located Flutter UI talks to it via
// a native HTTP client (not a browser), so no CORS grant is needed — and omitting
// it stops arbitrary web pages from reading bridge responses cross-origin.
func setBridgeHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
}

func writeBridgeJSON(w http.ResponseWriter, value interface{}) {
	setBridgeHeaders(w)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func writeBridgeError(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": message})
}
