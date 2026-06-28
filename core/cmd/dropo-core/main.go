package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"strings"
	"sync/atomic"

	"dropo/core/routing"
)

var connected atomic.Bool

func main() {
	listen := flag.String("listen", "127.0.0.1:17890", "HTTP listen address")
	dev := flag.Bool("dev", false, "enable development responses")
	flag.Parse()

	policy := routing.DefaultPolicy()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"name":      "dropo",
			"bridge":    "dropo-core",
			"mode":      modeLabel(*dev),
			"connected": connected.Load(),
		})
	})

	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"connected": connected.Load(),
			"mode":      "smart-routing",
			"summary":   "Direct by default; AI/dev services are VPN-forced; DPI services try free bypass first.",
		})
	})

	mux.HandleFunc("/api/services", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, policy.Rules)
	})

	mux.HandleFunc("/api/route", func(w http.ResponseWriter, r *http.Request) {
		host := r.URL.Query().Get("host")
		process := r.URL.Query().Get("process")
		subscription := strings.EqualFold(r.URL.Query().Get("subscription"), "true")
		writeJSON(w, policy.Decide(host, process, subscription))
	})

	mux.HandleFunc("/api/connect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		connected.Store(true)
		writeJSON(w, map[string]any{"connected": true})
	})

	mux.HandleFunc("/api/disconnect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		connected.Store(false)
		writeJSON(w, map[string]any{"connected": false})
	})

	log.Printf("dropo-core listening on http://%s", *listen)
	if err := http.ListenAndServe(*listen, mux); err != nil {
		log.Fatal(err)
	}
}

func modeLabel(dev bool) string {
	if dev {
		return "dev"
	}
	return "release"
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "http://localhost")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
