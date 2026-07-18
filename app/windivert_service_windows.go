//go:build windows

package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const winDivertEventLogRegistryPath = `SYSTEM\CurrentControlSet\Services\EventLog\System\WinDivert`

func nativeWinDivertServiceStatus() (string, error) {
	manager, err := mgr.Connect()
	if err != nil {
		return "", err
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(winDivertServiceName)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return "not installed", nil
		}
		return "", err
	}
	defer service.Close()
	status, err := service.Query()
	if err != nil {
		return "", err
	}
	config, configErr := service.Config()
	if configErr != nil {
		return fmt.Sprintf("state=%d pid=%d", status.State, status.ProcessId), nil
	}
	return fmt.Sprintf("state=%d pid=%d binary=%s", status.State, status.ProcessId, normalizeWinDivertBinaryPath(config.BinaryPathName)), nil
}

func cleanupWinDivertServiceNative(roots []string, reason string, logger func(string)) {
	log := func(format string, args ...interface{}) {
		if logger != nil {
			logger(fmt.Sprintf(format, args...))
		}
	}
	defer cleanupWinDivertEventLogNative(roots, reason, log)

	manager, err := mgr.Connect()
	if err != nil {
		log("WinDivert cleanup skipped (%s): connect service manager: %v", reason, err)
		return
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(winDivertServiceName)
	if err != nil {
		if !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			log("WinDivert cleanup skipped (%s): open service: %v", reason, err)
		}
		return
	}
	defer service.Close()

	config, err := service.Config()
	if err != nil {
		log("WinDivert cleanup skipped (%s): query config: %v", reason, err)
		return
	}
	binaryPath := normalizeWinDivertBinaryPath(config.BinaryPathName)
	if !winDivertBinaryOwnedByRoots(binaryPath, roots) {
		log("WinDivert cleanup skipped (%s): service binary is outside dropo roots: %s", reason, binaryPath)
		return
	}

	status, queryErr := service.Query()
	if queryErr == nil && status.State != svc.Stopped {
		if _, stopErr := service.Control(svc.Stop); stopErr != nil && !errors.Is(stopErr, windows.ERROR_SERVICE_NOT_ACTIVE) {
			log("WinDivert cleanup (%s): stop returned %v; continuing with delete", reason, stopErr)
		} else {
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				status, queryErr = service.Query()
				if queryErr != nil || status.State == svc.Stopped {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
	if err := service.Delete(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
		log("WinDivert cleanup (%s): delete failed: %v", reason, err)
		return
	}
	log("WinDivert cleanup (%s): owned service deleted", reason)
}

func cleanupWinDivertEventLogNative(roots []string, reason string, log func(string, ...interface{})) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, winDivertEventLogRegistryPath, registry.QUERY_VALUE)
	if err != nil {
		return
	}
	value, _, valueErr := key.GetStringValue("EventMessageFile")
	_ = key.Close()
	if valueErr != nil || !winDivertBinaryOwnedByRoots(strings.TrimSpace(value), roots) {
		return
	}
	if err := registry.DeleteKey(registry.LOCAL_MACHINE, winDivertEventLogRegistryPath); err != nil && !errors.Is(err, registry.ErrNotExist) {
		log("WinDivert EventLog cleanup (%s): delete failed: %v", reason, err)
		return
	}
	log("WinDivert EventLog cleanup (%s): owned source deleted", reason)
}
