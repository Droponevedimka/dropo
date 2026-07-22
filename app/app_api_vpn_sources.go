package main

import (
	"fmt"
	"strings"
	"time"
)

func (a *App) GetVPNSources() map[string]interface{} {
	a.waitForInit()
	if a.storage == nil {
		return map[string]interface{}{"success": false, "error": "storage is not initialized"}
	}
	profile, err := a.storage.GetActiveProfile()
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	return map[string]interface{}{
		"success": true, "sources": publicVPNSources(profile.VPNSources),
		"activeSource": a.activeVPNSource(),
	}
}

func (a *App) AddVPNSource(name, uri string) map[string]interface{} {
	return a.changeVPNSources(func(profile *ProfileData) error {
		source, err := newVPNSource(nextVPNSourceID(profile.VPNSources), name, uri)
		if err != nil {
			return err
		}
		profile.VPNSources = append(profile.VPNSources, source)
		return nil
	})
}

func (a *App) RemoveVPNSource(id string) map[string]interface{} {
	return a.changeVPNSources(func(profile *ProfileData) error {
		id = normalizeVPNSourceID(id)
		for index, source := range profile.VPNSources {
			if source.ID == id {
				profile.VPNSources = append(profile.VPNSources[:index], profile.VPNSources[index+1:]...)
				return nil
			}
		}
		return fmt.Errorf("VPN source %q not found", id)
	})
}

func (a *App) SetVPNSourceNode(id string, nodeIndex int) map[string]interface{} {
	return a.changeVPNSources(func(profile *ProfileData) error {
		id = normalizeVPNSourceID(id)
		for index := range profile.VPNSources {
			source := &profile.VPNSources[index]
			if source.ID != id {
				continue
			}
			if nodeIndex < 0 || (source.NodeCount > 0 && nodeIndex >= source.NodeCount) {
				return fmt.Errorf("node index %d is outside source range", nodeIndex)
			}
			source.SelectedNode = nodeIndex
			source.SelectedNodeID = ""
			return nil
		}
		return fmt.Errorf("VPN source %q not found", id)
	})
}

func (a *App) SetVPNSourceEnabled(id string, enabled bool) map[string]interface{} {
	return a.changeVPNSources(func(profile *ProfileData) error {
		id = normalizeVPNSourceID(id)
		for index := range profile.VPNSources {
			if profile.VPNSources[index].ID == id {
				profile.VPNSources[index].Disabled = !enabled
				return nil
			}
		}
		return fmt.Errorf("VPN source %q not found", id)
	})
}

func (a *App) MoveVPNSource(id string, newIndex int) map[string]interface{} {
	return a.changeVPNSources(func(profile *ProfileData) error {
		if newIndex < 0 || newIndex >= len(profile.VPNSources) {
			return fmt.Errorf("source index %d is outside range", newIndex)
		}
		id = normalizeVPNSourceID(id)
		oldIndex := -1
		for index, source := range profile.VPNSources {
			if source.ID == id {
				oldIndex = index
				break
			}
		}
		if oldIndex < 0 {
			return fmt.Errorf("VPN source %q not found", id)
		}
		source := profile.VPNSources[oldIndex]
		profile.VPNSources = append(profile.VPNSources[:oldIndex], profile.VPNSources[oldIndex+1:]...)
		profile.VPNSources = append(profile.VPNSources, VPNSource{})
		copy(profile.VPNSources[newIndex+1:], profile.VPNSources[newIndex:])
		profile.VPNSources[newIndex] = source
		return nil
	})
}

func (a *App) RefreshVPNSources() map[string]interface{} {
	return a.changeVPNSources(func(_ *ProfileData) error { return nil })
}

func (a *App) changeVPNSources(change func(*ProfileData) error) map[string]interface{} {
	a.waitForInit()
	if a.storage == nil || a.configBuilder == nil {
		return map[string]interface{}{"success": false, "error": "VPN storage is not initialized"}
	}
	profile, err := a.storage.GetActiveProfile()
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	profile.VPNSources = append([]VPNSource(nil), profile.VPNSources...)
	if err := change(profile); err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	for index := range profile.VPNSources {
		if strings.TrimSpace(profile.VPNSources[index].ID) == "" {
			profile.VPNSources[index].ID = nextVPNSourceID(profile.VPNSources)
		}
	}
	wasRunning := a.isVPNRunning()
	if wasRunning {
		a.Stop()
	}
	busyID := a.beginBusy("Обновляем цепочку VPN-источников...")
	defer a.endBusy(busyID)
	if err := a.configBuilder.BuildConfigForProfileSources(profile.ID, profile.VPNSources, profile.WireGuardConfigs); err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	updated, _ := a.storage.GetActiveProfile()
	if wasRunning {
		go func() {
			time.Sleep(500 * time.Millisecond)
			a.Start()
		}()
	}
	return map[string]interface{}{
		"success": true, "sources": publicVPNSources(updated.VPNSources),
		"sourceCount": len(updated.VPNSources), "wasRunning": wasRunning,
	}
}

// publicVPNSources deliberately excludes subscription URIs and cached node
// keys. The trusted local UI only needs display metadata and never receives
// reusable credentials through the JSON bridge.
func publicVPNSources(sources []VPNSource) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(sources))
	for _, source := range sources {
		result = append(result, map[string]interface{}{
			"id": source.ID, "name": source.Name, "kind": source.Kind,
			"disabled": source.Disabled, "selected_node": source.SelectedNode,
			"selected_node_id": source.SelectedNodeID, "node_count": source.NodeCount,
			"node_names":   append([]string(nil), source.NodeNames...),
			"last_updated": source.LastUpdated, "last_error": source.LastError,
		})
	}
	return result
}
