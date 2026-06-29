package runner

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"path/filepath"
)

func (a *App) runNetworkStage() error {
	switch a.networkBackend() {
	case "networkmanager":
		return a.runNetworkManagerStage()
	case "network":
		return a.runLegacyNetworkStage()
	}
	deferApply, err := a.prepareNetworkApply()
	if err != nil {
		return err
	}
	if a.Bundle.BackupExistingNetplan() {
		if err := a.disableExistingNetplan(); err != nil {
			return err
		}
	}

	if a.configureManagementNetwork() {
		mgmtPath := filepath.Join(netplanDir, "00-kunlun-bond.yaml")
		if err := a.writeManagedFile(mgmtPath, renderMgmtNetplan(a.Machine), 0o600); err != nil {
			return err
		}
	}

	for _, item := range a.Machine.RDMA {
		if a.Bundle.RDMAConfigureIPRoute() {
			netplanPath := filepath.Join(netplanDir, fmt.Sprintf("10-kunlun-%s.yaml", item.Name))
			if err := a.writeManagedFile(netplanPath, renderRDMANetplan(item, a.Machine.RDMAMTU), 0o600); err != nil {
				return err
			}

			routePath := a.routeScriptPath(item.Name)
			if err := a.writeManagedFile(routePath, renderRouteScript(item, a.Machine.RouteCIDR, a.Machine.RoutePriority), 0o755); err != nil {
				return err
			}
		}
	}

	if a.Bundle.ApplyNetworkImmediately() && !deferApply {
		if err := a.applyNetworkSettings(); err != nil {
			return err
		}
	}
	if a.Bundle.RDMAExists() && !deferApply {
		a.enableRoCEAdaptiveRouting()
	}
	if err := a.persistUdevRules(); err != nil {
		return err
	}
	return nil
}

func (a *App) runNetworkManagerStage() error {
	deferApply, err := a.prepareNetworkApply()
	if err != nil {
		return err
	}
	if err := a.ensureExplicitNetworkBackendService(); err != nil {
		return err
	}
	if a.Bundle.BackupExistingNetwork() {
		if err := a.disableExistingIfcfg(); err != nil {
			return err
		}
	}
	if err := a.writeIfcfgNetworkFiles(); err != nil {
		return err
	}
	if a.Bundle.RDMAConfigureIPRoute() {
		for _, item := range a.Machine.RDMA {
			routePath := a.routeScriptPath(item.Name)
			if err := a.writeManagedFile(routePath, renderRouteScript(item, a.Machine.RouteCIDR, a.Machine.RoutePriority), 0o755); err != nil {
				return err
			}
		}
		if err := a.writeManagedFile(nmDispatcherFile, renderNetworkManagerDispatcher(a.Machine.RDMA), 0o755); err != nil {
			return err
		}
		if err := a.runCmdAllowFailure("", nil, "restorecon", nmDispatcherFile); err != nil {
			return err
		}
	}
	if a.Bundle.ApplyNetworkImmediately() && !deferApply {
		if err := a.applyNetworkSettings(); err != nil {
			return err
		}
	}
	if a.Bundle.RDMAExists() && !deferApply {
		a.enableRoCEAdaptiveRouting()
	}
	if err := a.persistUdevRules(); err != nil {
		return err
	}
	return nil
}

func (a *App) runLegacyNetworkStage() error {
	deferApply, err := a.prepareNetworkApply()
	if err != nil {
		return err
	}
	if err := a.ensureExplicitNetworkBackendService(); err != nil {
		return err
	}
	if a.Bundle.BackupExistingNetwork() {
		if err := a.disableExistingIfcfg(); err != nil {
			return err
		}
	}
	if err := a.writeIfcfgNetworkFiles(); err != nil {
		return err
	}
	if a.Bundle.RDMAConfigureIPRoute() {
		for _, item := range a.Machine.RDMA {
			routePath := a.routeScriptPath(item.Name)
			if err := a.writeManagedFile(routePath, renderRouteScript(item, a.Machine.RouteCIDR, a.Machine.RoutePriority), 0o755); err != nil {
				return err
			}
		}
	}
	if a.Bundle.ApplyNetworkImmediately() && !deferApply {
		if err := a.applyNetworkSettings(); err != nil {
			return err
		}
	}
	if a.Bundle.RDMAExists() && !deferApply {
		a.enableRoCEAdaptiveRouting()
	}
	if err := a.persistUdevRules(); err != nil {
		return err
	}
	return nil
}

func (a *App) prepareNetworkApply() (bool, error) {
	missing := a.missingPlannedInterfaces()
	if len(missing) == 0 {
		return false, nil
	}
	bindings, err := a.confirmedNICBindings()
	if err != nil {
		return false, err
	}
	if err := a.renameInterfacesTemporarily(bindings); err != nil {
		return false, err
	}
	a.logf("temporarily renamed target interface names before applying network settings: %s", strings.Join(missing, ", "))
	return false, nil
}

func (a *App) applyDeferredNetworkSettings() error {
	if !a.networkApplyDeferred || !a.Bundle.ApplyNetworkImmediately() {
		return nil
	}
	if !a.DryRun {
		if err := a.ensureInterfacesReady(); err != nil {
			return err
		}
	}
	if err := a.applyNetworkSettings(); err != nil {
		return err
	}
	a.networkApplyDeferred = false
	if a.Bundle.RDMAExists() {
		a.enableRoCEAdaptiveRouting()
	}
	return nil
}

func (a *App) applyNetworkSettings() error {
	switch a.networkBackend() {
	case "networkmanager":
		if err := a.runCmd("", nil, "nmcli", "connection", "reload"); err != nil {
			return err
		}
		for _, iface := range a.networkManagerApplyInterfaces() {
			if err := a.runCmdAllowFailure("", nil, "nmcli", "connection", "up", iface); err != nil {
				return err
			}
		}
	case "network":
		for _, iface := range a.legacyNetworkApplyInterfaces() {
			if err := a.runCmdAllowFailure("", nil, "ifup", iface); err != nil {
				return err
			}
		}
	default:
		if err := a.runCmd("", nil, "netplan", "generate"); err != nil {
			return err
		}
		if err := a.runCmd("", nil, "netplan", "apply"); err != nil {
			return err
		}
	}
	if a.Bundle.RDMAConfigureIPRoute() {
		for _, item := range a.Machine.RDMA {
			routePath := a.routeScriptPath(item.Name)
			if err := a.runCmd("", nil, "bash", routePath); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *App) enableRoCEAdaptiveRouting() {
	for _, item := range a.Machine.RDMA {
		output, err := a.captureCmd("", nil, "ethtool", "-i", item.Name)
		if err != nil {
			a.logf("skip RoCE adaptive routing on %s: %v", item.Name, err)
			continue
		}
		busInfo, err := parseEthtoolBusInfo(output)
		if err != nil {
			a.logf("skip RoCE adaptive routing on %s: %v", item.Name, err)
			continue
		}
		_ = a.runCmdAllowFailure("", nil, "mlxreg", "-d", busInfo, "--reg_name", "ROCE_ACCL", "--set", "adaptive_routing_forced_en=0x1", "--yes")
	}
}

func parseEthtoolBusInfo(output string) (string, error) {
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "bus-info" {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			break
		}
		return value, nil
	}
	return "", errors.New("bus-info not found in ethtool output")
}

func (a *App) runUdevStage() error {
	if a.udevRulesPersisted {
		a.logf("skip udev: persistent NIC naming rules were already written by the network stage")
		return nil
	}
	return a.persistUdevRules()
}

func (a *App) persistUdevRules() error {
	if !a.hasPersistentNICNamingTargets() && !a.interfaceBindingsConfirmed {
		a.logf("skip udev persistent naming: no management or RDMA NIC targets are configured")
		return nil
	}
	bindings, err := a.confirmedNICBindings()
	if err != nil {
		return err
	}
	content := renderUdevRules(bindings)
	if err := a.writeManagedFile(udevFile, content, 0o644); err != nil {
		return err
	}
	if err := a.runCmd("", nil, "udevadm", "control", "--reload-rules"); err != nil {
		return err
	}
	a.logf("udev rules reloaded; reboot is still required for persistent udev naming.")
	a.udevRulesPersisted = true
	return nil
}

func (a *App) confirmedNICBindings() ([]interfaceBinding, error) {
	if a.interfaceBindingsConfirmed {
		bindings := cloneInterfaceBindings(a.confirmedInterfaceBindings)
		if err := validateReviewBindings(bindings); err != nil {
			return nil, incompleteNICBindingError(err)
		}
		return bindings, nil
	}
	bindings, err := a.interfaceBindings()
	if err != nil {
		return nil, err
	}
	bindings, err = a.confirmInterfaceBindings(bindings)
	if err != nil {
		return nil, err
	}
	if err := validateReviewBindings(bindings); err != nil {
		return nil, incompleteNICBindingError(err)
	}
	a.confirmedInterfaceBindings = cloneInterfaceBindings(bindings)
	a.interfaceBindingsConfirmed = true
	return cloneInterfaceBindings(bindings), nil
}

func incompleteNICBindingError(err error) error {
	return fmt.Errorf("NIC binding review did not produce complete bindings: %w; choose every NIC in the TUI or provide mgmt*_mac/rdma*_mac values in inventory for exact matching", err)
}

func (a *App) confirmInterfaceBindings(bindings []interfaceBinding) ([]interfaceBinding, error) {
	if a.DryRun {
		if bindingsNeedManualReview(bindings) {
			return nil, errors.New("manual NIC binding review is required because automatic discovery was ambiguous; run apply from an interactive terminal to choose NICs in the TUI, or provide mgmt*_mac/rdma*_mac values in inventory for exact matching")
		}
		return bindings, nil
	}
	devices, err := a.discoverReviewNetDevices()
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		if bindingsNeedManualReview(bindings) {
			return nil, errors.New("manual NIC binding review is required, but no selectable NICs were discovered; check /sys/class/net visibility, or provide mgmt*_mac/rdma*_mac values in inventory for exact matching")
		}
		return bindings, nil
	}

	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		if bindingsNeedManualReview(bindings) {
			return nil, fmt.Errorf("manual NIC binding review is required because automatic discovery was ambiguous, but /dev/tty is not available; run from an interactive terminal to choose NICs in the TUI, or provide mgmt*_mac/rdma*_mac values in inventory for exact matching: %w", err)
		}
		a.logf("skip interactive NIC binding review: /dev/tty is not available; using automatic mapping")
		return bindings, nil
	}
	defer tty.Close()

	if bindingsNeedManualReview(bindings) {
		a.logf("open manual NIC binding TUI for %d target(s) and %d selectable NIC(s)", len(bindings), len(devices))
	}
	review := newNICBindingReview(bindings, devices)
	confirmed, err := runNICBindingReview(tty, review)
	if err != nil {
		return nil, err
	}
	syncConfirmedInterfaceBindings(a, confirmed)
	return confirmed, nil
}

func bindingsNeedManualReview(bindings []interfaceBinding) bool {
	for _, binding := range bindings {
		if binding.NeedsReview {
			return true
		}
	}
	return false
}

func syncConfirmedInterfaceBindings(a *App, bindings []interfaceBinding) {
	mgmtIdx := 0
	rdmaIdx := 0
	for _, binding := range bindings {
		switch binding.Kind {
		case "mgmt":
			for len(a.Machine.MgmtMACs) <= mgmtIdx {
				a.Machine.MgmtMACs = append(a.Machine.MgmtMACs, "")
			}
			a.Machine.MgmtMACs[mgmtIdx] = binding.MAC
			mgmtIdx++
		case "rdma":
			if rdmaIdx < len(a.Machine.RDMA) {
				a.Machine.RDMA[rdmaIdx].MAC = binding.MAC
			}
			rdmaIdx++
		}
	}
}
