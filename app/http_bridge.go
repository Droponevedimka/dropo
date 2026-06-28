package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
)

type bridgeCallRequest struct {
	Method string            `json:"method"`
	Args   []json.RawMessage `json:"args"`
}

func newBridgeMux(app *App) http.Handler {
	mux := http.NewServeMux()
	handle := func(path string, fn http.HandlerFunc) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			setBridgeHeaders(w)
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
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

	handle("/api/connect", func(w http.ResponseWriter, r *http.Request) {
		if requireMethod(w, r, http.MethodPost) {
			writeBridgeJSON(w, app.Start())
		}
	})

	handle("/api/disconnect", func(w http.ResponseWriter, r *http.Request) {
		if requireMethod(w, r, http.MethodPost) {
			writeBridgeJSON(w, app.Stop())
		}
	})

	handle("/api/dependencies/status", func(w http.ResponseWriter, r *http.Request) {
		writeBridgeJSON(w, app.DependenciesStatus())
	})

	handle("/api/dependencies/download", func(w http.ResponseWriter, r *http.Request) {
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

	handle("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		lastN, _ := strconv.Atoi(r.URL.Query().Get("lastN"))
		if lastN <= 0 {
			lastN = 200
		}
		writeBridgeJSON(w, app.GetLogs(lastN))
	})

	handle("/api/tray/ensure", func(w http.ResponseWriter, r *http.Request) {
		if requireMethod(w, r, http.MethodPost) {
			writeBridgeJSON(w, app.EnsureTray())
		}
	})

	handle("/api/call", func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
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

	handle("/api/quit", func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		writeBridgeJSON(w, app.PrepareQuit())
	})

	handle("/api/quit/finalize", func(w http.ResponseWriter, r *http.Request) {
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

func setBridgeHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
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
