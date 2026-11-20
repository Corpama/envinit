package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type bindingState struct {
	Version     int                `json:"version"`
	Fingerprint string             `json:"configuration_sha256"`
	Bindings    []interfaceBinding `json:"bindings"`
}

func (a *App) restoreBindingState(fingerprint string) error {
	raw, err := os.ReadFile(a.targetPath("/var/lib/envinit/nic-bindings.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var state bindingState
	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf("cannot read saved NIC bindings: %w", err)
	}
	if state.Version != 1 || state.Fingerprint != fingerprint {
		a.logf("saved NIC bindings invalidated by configuration change")
		return nil
	}
	if err := validateReviewBindings(state.Bindings); err != nil {
		a.logf("saved NIC bindings invalidated because the physical mapping is incomplete or duplicated: %v", err)
		return nil
	}
	byMAC, err := a.netDeviceByMAC()
	if err != nil {
		return err
	}
	for _, b := range state.Bindings {
		found := false
		for mac := range byMAC {
			if strings.EqualFold(mac, b.MAC) {
				found = true
				break
			}
		}
		if !found {
			a.logf("saved NIC %s is absent; require binding review", b.MAC)
			return nil
		}
	}
	syncConfirmedInterfaceBindings(a, state.Bindings)
	for _, b := range state.Bindings {
		a.logf("reuse confirmed binding %s -> %s (%s)", b.Name, b.MAC, b.Reason)
	}
	return nil
}

func (a *App) saveBindingState(fingerprint string, bindings []interfaceBinding) error {
	if a.DryRun {
		return nil
	}
	p := a.targetPath("/var/lib/envinit/nic-bindings.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(bindingState{Version: 1, Fingerprint: fingerprint, Bindings: bindings}, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(p, append(data, '\n'), 0o600)
}
