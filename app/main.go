package main

import (
	"context"
	"embed"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

//go:embed config/template.json
var embeddedTemplate []byte

// Keep embed imported for the directive above.
var _ embed.FS

var appInstance *App

func copyEmbeddedTemplate(destPath string) error {
	return os.WriteFile(destPath, embeddedTemplate, 0644)
}

func main() {
	listen := flag.String("listen", "127.0.0.1:17890", "HTTP bridge listen address")
	noTray := flag.Bool("no-tray", false, "disable Windows tray integration")
	flag.Parse()

	releaseSingleInstance, alreadyRunning := acquireSingleInstance()
	if alreadyRunning {
		log.Println("Application core already running, activating existing window")
		return
	}
	if releaseSingleInstance != nil {
		defer releaseSingleInstance()
	}

	appInstance = NewApp()
	if err := appInstance.openLogFile(); err != nil {
		log.Printf("failed to open session log: %v", err)
	}

	if !*noTray {
		startPlatformTray()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	appInstance.startup(ctx)

	bridgeToken, err := ensureBridgeToken(appInstance.dataPath)
	if err != nil {
		log.Printf("bridge token provisioning failed: %v", err)
		appInstance.requestShutdown()
		cancel()
		appInstance.shutdown(context.Background())
		return
	}
	defer removeBridgeToken(appInstance.dataPath)

	server := &http.Server{
		Addr:              *listen,
		Handler:           newBridgeMux(appInstance, bridgeToken),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("dropo core bridge listening on http://%s", *listen)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("bridge server failed: %v", err)
			appInstance.requestShutdown()
		}
	}()

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	appInstance.requestShutdown()
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)
	appInstance.shutdown(shutdownCtx)
}
