package runner

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"envinit/internal/nicdetect"
	"envinit/internal/spec"
)

type interfaceBinding struct {
	Kind          string
	Name          string
	GroupName     string
	MAC           string
	CurrentName   string
	Address       string
	Gateway       string
	GatewaySource string
	Table         int
	NeedsReview   bool
	Reason        string
	Confidence    string
	ProbeDetails  []string
	ProbeWarning  string
}

type pendingInterfaceRename struct {
	current string
	target  string
	temp    string
	wasUp   bool
	mac     string
}

type netDevice struct {
	Name          string
	MAC           string
	PCI           string
	Driver        string
	SpeedMbps     int
	MaxSpeedMbps  int
	MTU           int
	VendorID      string
	DeviceID      string
	PhysPortName  string
	OperState     string
	CarrierUp     bool
	CarrierKnown  bool
	HasInfiniband bool
	DevPort       int
	HasDevPort    bool
}

func (a *App) interfaceBindings() ([]interfaceBinding, error) {
	deviceByMAC, err := a.netDeviceByMAC()
	if err != nil {
		return nil, err
	}
	bindings, err := a.mgmtInterfaceBindings(deviceByMAC)
	if err != nil {
		return nil, err
	}
	mgmtNeedsReview := bindingsNeedManualReview(bindings)
	var rdmaBindings []interfaceBinding
	if mgmtNeedsReview && rdmaBindingsNeedDiscovery(a.Machine.RDMA) {
		rdmaBindings = a.manualRDMABindings(nil)
	} else {
		rdmaBindings, err = a.rdmaInterfaceBindings(deviceByMAC)
		if err != nil {
			return nil, err
		}
	}
	bindings = append(bindings, rdmaBindings...)
	if a.managementIsBond() {
		for idx := range bindings {
			if bindings[idx].Kind == "mgmt" {
				bindings[idx].GroupName = a.Machine.MgmtBondName
			}
		}
	}
	return bindings, nil
}

func (a *App) managementIsBond() bool {
	return a.Machine.MgmtMode == spec.MgmtModeBond ||
		(len(a.Machine.MgmtIfaces) > 1 && strings.TrimSpace(a.Machine.MgmtBondName) != "")
}

func (a *App) ensureAutoManagementInterfaces() error {
	if a.Machine.MgmtMode != "" && a.Machine.MgmtMode != spec.MgmtModeAuto {
		complete := len(a.Machine.MgmtIfaces) > 0
		for _, name := range a.Machine.MgmtIfaces {
			if name == "" {
				complete = false
			}
		}
		if complete {
			return a.validateManagementTarget()
		}
		devices, err := a.discoverNetDevices()
		if err != nil {
			return err
		}
		return a.resolveExplicitManagementDevices(devices)
	}
	if len(a.Machine.MgmtIfaces) > 0 {
		return nil
	}
	allDevices, err := a.discoverNetDevices()
	if err != nil {
		return err
	}
	devices := a.managementCandidates(allDevices)
	if len(devices) == 0 {
		return fmt.Errorf("no management NIC candidates; fill mgmt_iface1/mgmt_mac1 in inventory")
	}
	if len(devices) > 2 {
		devices, err = selectManagementNICs(devices, false)
		if err != nil {
			return err
		}
	}
	a.Machine.MgmtIfaces = make([]string, 0, len(devices))
	a.Machine.MgmtMACs = make([]string, 0, len(devices))
	for _, device := range devices {
		a.Machine.MgmtIfaces = append(a.Machine.MgmtIfaces, device.Name)
		a.Machine.MgmtMACs = append(a.Machine.MgmtMACs, device.MAC)
	}
	if len(devices) == 1 {
		a.Machine.MgmtMode = spec.MgmtModeSingle
	} else {
		a.Machine.MgmtMode = spec.MgmtModeBond
	}
	return a.validateManagementTarget()
}

func (a *App) validateManagementTarget() error {
	target := a.Machine.MgmtTargetName
	if a.Machine.MgmtMode == spec.MgmtModeBond {
		if target == "" {
			target = a.Machine.MgmtBondName
		}
		if target == "" {
			target = "bond0"
		}
		for _, name := range a.Machine.MgmtIfaces {
			if name == target {
				return fmt.Errorf("mgmt_target_name %q must differ from bond members", target)
			}
		}
		a.Machine.MgmtBondName = target
	} else if len(a.Machine.MgmtIfaces) == 1 {
		if target != "" && target != a.Machine.MgmtIfaces[0] {
			return fmt.Errorf("single mgmt_target_name must equal selected interface %q", a.Machine.MgmtIfaces[0])
		}
		target = a.Machine.MgmtIfaces[0]
		a.Machine.MgmtBondName = ""
	}
	a.Machine.MgmtTargetName = target
	return nil
}

func (a *App) managementCandidates(devices []netDevice) []netDevice {
	decision := a.recommendNICRoles(devices, 0, false)
	var candidates []netDevice
	for _, device := range netDevicesExcludingBindings(devices, decision.RDMA) {
		if !device.HasInfiniband && device.Driver != "mlx5_core" {
			candidates = append(candidates, device)
		}
	}
	if len(candidates) == 0 {
		decision = a.recommendNICRoles(devices, 1, false)
		candidates = netDevicesForBindings(devices, decision.Management)
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].CarrierUp && !candidates[j].CarrierUp })
	return candidates
}

// Explicit mode fixes slot count. Keep slot order and known members rather than
// sorting the selected hardware (which would break partial MAC bindings).
func (a *App) resolveExplicitManagementDevices(devices []netDevice) error {
	count := 1
	if a.Machine.MgmtMode == spec.MgmtModeBond {
		count = 2
	}
	decision := a.recommendNICRoles(devices, count, false)
	if count == 2 {
		candidates := a.managementCandidates(devices)
		if len(candidates) == 1 {
			selected, err := selectManagementNICs(candidates, true)
			if err != nil {
				return err
			}
			a.Machine.MgmtIfaces = []string{selected[0].Name}
			a.Machine.MgmtMACs = []string{selected[0].MAC}
			return a.validateManagementTarget()
		}
	}
	if len(decision.Conflicts) > 0 || len(decision.Management) != count {
		return fmt.Errorf("management mode %s requires %d unambiguous physical interfaces; specify mgmt_iface1/mgmt_mac1%s in inventory and retry (no mode downgrade)", a.Machine.MgmtMode, count, map[bool]string{true: " and mgmt_iface2/mgmt_mac2", false: ""}[count == 2])
	}
	names := make([]string, count)
	macs := make([]string, count)
	for _, binding := range decision.Management {
		if binding.Slot < 0 || binding.Slot >= count || (binding.Confidence != nicdetect.ConfidenceExact && binding.Confidence != nicdetect.ConfidenceStrong) {
			return fmt.Errorf("management binding is ambiguous; confirm inventory members")
		}
		names[binding.Slot] = binding.NIC.Name
		macs[binding.Slot] = binding.NIC.MAC
		if binding.Slot < len(a.Machine.MgmtIfaces) && a.Machine.MgmtIfaces[binding.Slot] != "" {
			names[binding.Slot] = a.Machine.MgmtIfaces[binding.Slot]
		}
	}
	for _, name := range names {
		if name == "" || name == a.Machine.MgmtBondName {
			return fmt.Errorf("invalid management member %q", name)
		}
	}
	a.Machine.MgmtIfaces = names
	a.Machine.MgmtMACs = macs
	return a.validateManagementTarget()
}

func (a *App) mgmtInterfaceBindings(deviceByMAC map[string]netDevice) ([]interfaceBinding, error) {
	needsDiscovery := false
	out := make([]interfaceBinding, 0, len(a.Machine.MgmtIfaces))
	for idx, name := range a.Machine.MgmtIfaces {
		mac := ""
		if idx < len(a.Machine.MgmtMACs) {
			mac = strings.TrimSpace(a.Machine.MgmtMACs[idx])
		}
		if mac == "" && !a.interfaceExists(name) {
			needsDiscovery = true
			break
		}
	}
	if needsDiscovery {
		return a.discoverMgmtBindings()
	}
	for idx, name := range a.Machine.MgmtIfaces {
		mac := ""
		if idx < len(a.Machine.MgmtMACs) {
			mac = strings.TrimSpace(a.Machine.MgmtMACs[idx])
		}
		if mac == "" {
			var err error
			mac, err = a.readMAC(name)
			if err != nil {
				return nil, err
			}
		}
		current := name
		if device, ok := deviceByMAC[mac]; ok {
			current = device.Name
		} else if mac != "" {
			actual, err := a.readMAC(name)
			if err != nil || !strings.EqualFold(actual, mac) {
				return nil, fmt.Errorf("planned management MAC %s is not present; review the physical NIC binding", mac)
			}
		}
		out = append(out, interfaceBinding{
			Kind:          "mgmt",
			Name:          name,
			MAC:           mac,
			CurrentName:   current,
			Address:       plannedAddress(a.Machine.MgmtIP, a.Machine.MgmtPrefix),
			Gateway:       a.Machine.MgmtGateway,
			GatewaySource: a.managementGatewaySource(),
			Reason:        "existing name/MAC",
			Confidence:    nicdetect.ConfidenceExact,
		})
	}
	return out, nil
}

func (a *App) rdmaInterfaceBindings(deviceByMAC map[string]netDevice) ([]interfaceBinding, error) {
	if !a.Bundle.RDMAExists() {
		return nil, nil
	}
	needsDiscovery := false
	for _, item := range a.Machine.RDMA {
		if strings.TrimSpace(item.MAC) == "" {
			needsDiscovery = true
			break
		}
	}
	if needsDiscovery {
		return a.discoverRDMABindings()
	}
	out := make([]interfaceBinding, 0, len(a.Machine.RDMA))
	for _, item := range a.Machine.RDMA {
		mac := strings.TrimSpace(item.MAC)
		device, ok := deviceByMAC[mac]
		if !ok {
			return nil, fmt.Errorf("planned RDMA MAC %s for %s is not present; review the physical NIC binding", mac, item.Name)
		}
		current := device.Name
		out = append(out, interfaceBinding{
			Kind:          "rdma",
			Name:          item.Name,
			MAC:           mac,
			CurrentName:   current,
			Address:       plannedAddress(item.IP, item.Prefix),
			Gateway:       item.Gateway,
			GatewaySource: a.rdmaGatewaySource(len(out)),
			Table:         item.Table,
			Reason:        "MAC exact",
			Confidence:    nicdetect.ConfidenceExact,
		})
	}
	return out, nil
}

func (a *App) discoverMgmtBindings() ([]interfaceBinding, error) {
	devices, err := a.discoverMgmtDevices()
	if err != nil {
		return nil, err
	}
	if len(devices) != len(a.Machine.MgmtIfaces) {
		return a.manualMgmtBindings(devices)
	}
	out := make([]interfaceBinding, 0, len(a.Machine.MgmtIfaces))
	for idx, name := range a.Machine.MgmtIfaces {
		device := devices[idx]
		if idx < len(a.Machine.MgmtMACs) && strings.TrimSpace(a.Machine.MgmtMACs[idx]) != "" && !strings.EqualFold(strings.TrimSpace(a.Machine.MgmtMACs[idx]), device.MAC) {
			return nil, fmt.Errorf("mgmt%d_mac %s does not match discovered device %s (%s)", idx+1, a.Machine.MgmtMACs[idx], device.Name, device.MAC)
		}
		for len(a.Machine.MgmtMACs) <= idx {
			a.Machine.MgmtMACs = append(a.Machine.MgmtMACs, "")
		}
		a.Machine.MgmtMACs[idx] = device.MAC
		out = append(out, interfaceBinding{
			Kind:          "mgmt",
			Name:          name,
			MAC:           device.MAC,
			CurrentName:   device.Name,
			Address:       plannedAddress(a.Machine.MgmtIP, a.Machine.MgmtPrefix),
			Gateway:       a.Machine.MgmtGateway,
			GatewaySource: a.managementGatewaySource(),
			Reason:        "unified hardware group",
			Confidence:    nicdetect.ConfidenceStrong,
		})
	}
	return out, nil
}

func (a *App) manualMgmtBindings(autoCandidates []netDevice) ([]interfaceBinding, error) {
	a.logf("management auto discovery matched %d candidate(s) for %d target name(s); entering manual NIC binding review without applying automatic candidates. Tip: set mgmt*_mac values in inventory for exact matching.", len(autoCandidates), len(a.Machine.MgmtIfaces))
	out := make([]interfaceBinding, 0, len(a.Machine.MgmtIfaces))
	for _, name := range a.Machine.MgmtIfaces {
		out = append(out, interfaceBinding{
			Kind:          "mgmt",
			Name:          name,
			Address:       plannedAddress(a.Machine.MgmtIP, a.Machine.MgmtPrefix),
			Gateway:       a.Machine.MgmtGateway,
			GatewaySource: a.managementGatewaySource(),
			NeedsReview:   true,
			Reason:        "ambiguous hardware groups",
			Confidence:    nicdetect.ConfidenceAmbiguous,
		})
	}
	return out, nil
}

func (a *App) discoverRDMABindings() ([]interfaceBinding, error) {
	devices, err := a.discoverRDMADevices()
	if err != nil {
		return nil, err
	}
	if len(devices) != len(a.Machine.RDMA) {
		return a.manualRDMABindings(devices), nil
	}
	out := make([]interfaceBinding, 0, len(a.Machine.RDMA))
	for idx, item := range a.Machine.RDMA {
		device := devices[idx]
		if strings.TrimSpace(item.MAC) != "" && !strings.EqualFold(strings.TrimSpace(item.MAC), device.MAC) {
			return nil, fmt.Errorf("rdma%d_mac %s does not match discovered device %s (%s)", idx+1, item.MAC, device.Name, device.MAC)
		}
		a.Machine.RDMA[idx].MAC = device.MAC
		out = append(out, interfaceBinding{
			Kind:          "rdma",
			Name:          item.Name,
			MAC:           device.MAC,
			CurrentName:   device.Name,
			Address:       plannedAddress(item.IP, item.Prefix),
			Gateway:       item.Gateway,
			GatewaySource: a.rdmaGatewaySource(idx),
			Table:         item.Table,
			Reason:        "unified hardware group",
			Confidence:    nicdetect.ConfidenceStrong,
		})
	}
	return out, nil
}

func (a *App) manualRDMABindings(autoCandidates []netDevice) []interfaceBinding {
	a.logf("RDMA auto discovery matched %d candidate(s) for %d target name(s); entering manual NIC binding review without applying automatic candidates. Tip: set rdma*_mac values in inventory for exact matching.", len(autoCandidates), len(a.Machine.RDMA))
	out := make([]interfaceBinding, 0, len(a.Machine.RDMA))
	for idx, item := range a.Machine.RDMA {
		out = append(out, interfaceBinding{
			Kind:          "rdma",
			Name:          item.Name,
			Address:       plannedAddress(item.IP, item.Prefix),
			Gateway:       item.Gateway,
			GatewaySource: a.rdmaGatewaySource(idx),
			Table:         item.Table,
			NeedsReview:   true,
			Reason:        "ambiguous hardware groups",
			Confidence:    nicdetect.ConfidenceAmbiguous,
		})
	}
	return out
}

func (a *App) managementGatewaySource() string {
	if a.hasApplySourceRecord && strings.TrimSpace(a.applySourceRecord.MgmtGateway) != "" {
		return "inventory explicit"
	}
	if strings.TrimSpace(a.Bundle.Defaults.MgmtGateway) != "" {
		return "bundle default"
	}
	return "derived from planned IP"
}

func (a *App) rdmaGatewaySource(index int) string {
	if a.hasApplySourceRecord && index < len(a.applySourceRecord.RDMA) && strings.TrimSpace(a.applySourceRecord.RDMA[index].Gateway) != "" {
		return "inventory explicit"
	}
	if a.applySourceRecord.LegacySchema && index < len(a.Bundle.Defaults.RDMAInterfaces) && strings.TrimSpace(a.Bundle.Defaults.RDMAInterfaces[index].Gateway) != "" {
		return "bundle default"
	}
	return "derived from planned IP"
}

func rdmaBindingsNeedDiscovery(items []spec.RDMAConfig) bool {
	for _, item := range items {
		if strings.TrimSpace(item.MAC) == "" {
			return true
		}
	}
	return false
}

func plannedAddress(ip string, prefix int) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return "-"
	}
	if prefix > 0 {
		return fmt.Sprintf("%s/%d", ip, prefix)
	}
	return ip
}

func renderUdevRules(bindings []interfaceBinding) string {
	return renderUdevRulesForKind(bindings, "")
}

func renderUdevRulesForKind(bindings []interfaceBinding, kind string) string {
	seen := map[string]bool{}
	var b strings.Builder
	b.WriteString("# This file is autogenerated. Do not edit manually.\n")
	for _, item := range bindings {
		if kind != "" && item.Kind != kind {
			continue
		}
		if seen[item.Name] {
			continue
		}
		seen[item.Name] = true
		fmt.Fprintf(&b, "SUBSYSTEM==\"net\", ACTION==\"add\", DRIVERS==\"?*\", ATTR{address}==\"%s\", ATTR{type}==\"1\", NAME=\"%s\"\n", item.MAC, item.Name)
	}
	return b.String()
}

func renderSelectedRDMAInterfaces(bindings []interfaceBinding) string {
	var b strings.Builder
	b.WriteString("# This file is autogenerated by envinit. Do not edit manually.\n")
	b.WriteString("# Columns: target_name current_name mac\n")
	for _, item := range bindings {
		if item.Kind != "rdma" {
			continue
		}
		target := strings.TrimSpace(item.Name)
		current := strings.TrimSpace(item.CurrentName)
		mac := strings.ToLower(strings.TrimSpace(item.MAC))
		if target == "" {
			continue
		}
		if current == "" {
			current = "-"
		}
		if mac == "" {
			mac = "-"
		}
		fmt.Fprintf(&b, "%s %s %s\n", target, current, mac)
	}
	return b.String()
}

func (a *App) renameRDMATemporarily(bindings []interfaceBinding) error {
	return a.renameInterfacesTemporarily(bindings)
}

func (a *App) renameInterfacesTemporarily(bindings []interfaceBinding) error {
	var pending []pendingInterfaceRename
	reservedTemps := map[string]bool{}
	currentNames := map[string]bool{}
	for _, binding := range bindings {
		current := strings.TrimSpace(binding.CurrentName)
		target := strings.TrimSpace(binding.Name)
		if current != "" && target != "" && current != target {
			currentNames[current] = true
		}
	}
	for _, binding := range bindings {
		current := strings.TrimSpace(binding.CurrentName)
		target := strings.TrimSpace(binding.Name)
		if current == "" || target == "" || current == target {
			continue
		}
		// A target may legitimately be the current primary name of another NIC
		// participating in the same swap. Any other primary-name occupant would
		// make the second phase fail after interfaces have already been moved.
		if a.interfaceExists(target) && !currentNames[target] {
			return fmt.Errorf("cannot rename interface %s to %s: target primary name is already in use by an interface outside this rename set", current, target)
		}
		temp := a.availableTempInterfaceName(len(pending), reservedTemps)
		reservedTemps[temp] = true
		pending = append(pending, pendingInterfaceRename{
			current: current,
			target:  target,
			temp:    temp,
			wasUp:   a.interfaceAdministrativelyUp(current),
			mac:     func() string { mac, _ := a.readMAC(current); return mac }(),
		})
	}
	if len(pending) == 0 {
		return nil
	}
	var movedToTemp []pendingInterfaceRename
	for _, item := range pending {
		if err := a.runNetworkCommand("link-down", item.current, "ip", "link", "set", "dev", item.current, "down"); err != nil {
			return a.renameFailureWithRollback(fmt.Errorf("bring interface %s down before rename: %w", item.current, err), movedToTemp, nil)
		}
		if err := a.runNetworkCommand("link-rename", item.mac+"/"+item.temp, "ip", "link", "set", "dev", item.current, "name", item.temp); err != nil {
			if item.wasUp {
				_ = a.runCmdAllowFailure("", nil, "ip", "link", "set", "dev", item.current, "up")
			}
			return a.renameFailureWithRollback(fmt.Errorf("move interface %s to temporary name %s: %w", item.current, item.temp, err), movedToTemp, nil)
		}
		movedToTemp = append(movedToTemp, item)
	}
	var movedToTarget []pendingInterfaceRename
	for _, item := range pending {
		// Linux keeps predictable names as alternate names. This is especially
		// visible when Apply renames a NIC and a later Apply changes it back: the
		// requested target can still be an altname on the very same link, and a
		// normal RTM_SETLINK rename then fails with EEXIST. Removing a missing
		// altname is harmless and deliberately best-effort for older iproute2.
		_ = a.runCmdAllowFailure("", nil, "ip", "link", "property", "del", "dev", item.temp, "altname", item.target)
		if err := a.runNetworkCommand("link-rename", item.mac+"/"+item.target, "ip", "link", "set", "dev", item.temp, "name", item.target); err != nil {
			return a.renameFailureWithRollback(fmt.Errorf("move temporary interface %s to target name %s: %w", item.temp, item.target, err), movedToTemp, movedToTarget)
		}
		movedToTarget = append(movedToTarget, item)
	}
	return nil
}

// renameFailureWithRollback restores the pre-rename primary names after a
// partially completed two-phase rename. Without this, one RTNETLINK failure
// leaves interfaces under ei-tmp* names and can strand their network config.
func (a *App) renameFailureWithRollback(cause error, movedToTemp, movedToTarget []pendingInterfaceRename) error {
	var rollbackErrs []error
	for idx := len(movedToTarget) - 1; idx >= 0; idx-- {
		item := movedToTarget[idx]
		_ = a.runCmdAllowFailure("", nil, "ip", "link", "set", "dev", item.target, "down")
		if err := a.runNetworkCommand("link-rename", item.mac+"/"+item.temp, "ip", "link", "set", "dev", item.target, "name", item.temp); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("restore target %s to temporary name %s: %w", item.target, item.temp, err))
		}
	}
	for idx := len(movedToTemp) - 1; idx >= 0; idx-- {
		item := movedToTemp[idx]
		_ = a.runCmdAllowFailure("", nil, "ip", "link", "property", "del", "dev", item.temp, "altname", item.current)
		if err := a.runNetworkCommand("link-rename", item.mac+"/"+item.current, "ip", "link", "set", "dev", item.temp, "name", item.current); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("restore temporary interface %s to original name %s: %w", item.temp, item.current, err))
			continue
		}
		if item.wasUp {
			if err := a.runNetworkCommand("link-up", item.current, "ip", "link", "set", "dev", item.current, "up"); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("bring restored interface %s up: %w", item.current, err))
			}
		}
	}
	if len(rollbackErrs) == 0 {
		return fmt.Errorf("%w; original interface names were restored", cause)
	}
	return fmt.Errorf("%w; automatic rename rollback was incomplete: %v", cause, errors.Join(rollbackErrs...))
}

func (a *App) interfaceAdministrativelyUp(iface string) bool {
	data, err := os.ReadFile(a.targetPath(filepath.Join("/sys/class/net", iface, "flags")))
	if err != nil {
		return false
	}
	flags, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(string(data), "0x")), 16, 64)
	return err == nil && flags&1 != 0
}

func (a *App) availableTempInterfaceName(idx int, reserved map[string]bool) string {
	for suffix := idx; ; suffix++ {
		name := fmt.Sprintf("ei-tmp%d", suffix)
		if !reserved[name] && !a.interfaceExists(name) {
			return name
		}
	}
}

func (a *App) readMAC(iface string) (string, error) {
	mac, ok := readSysfsInterfaceMAC(a.targetPath("/sys/class/net"), iface)
	if !ok {
		return "", fmt.Errorf("read mac for %s", iface)
	}
	return mac, nil
}

func (a *App) netDeviceByMAC() (map[string]netDevice, error) {
	devices, err := a.discoverNetDevices()
	if err != nil {
		return nil, err
	}
	out := make(map[string]netDevice, len(devices))
	for _, device := range devices {
		if device.MAC == "" {
			continue
		}
		out[device.MAC] = device
	}
	return out, nil
}

func (a *App) discoverMgmtDevices() ([]netDevice, error) {
	devices, err := a.discoverNetDevices()
	if err != nil {
		return nil, err
	}
	decision := a.recommendNICRoles(devices, len(a.Machine.MgmtIfaces), false)
	if decision.Confidence == nicdetect.ConfidenceStrong || decision.Confidence == nicdetect.ConfidenceExact {
		selected := netDevicesForBindings(devices, decision.Management)
		if len(selected) == len(a.Machine.MgmtIfaces) {
			return selected, nil
		}
	}
	return netDevicesExcludingBindings(devices, decision.RDMA), nil
}

func (a *App) discoverManualMgmtDevices() ([]netDevice, error) {
	devices, err := a.discoverNetDevices()
	if err != nil {
		return nil, err
	}
	rdmaNames := map[string]bool{}
	for _, item := range a.Machine.RDMA {
		rdmaNames[item.Name] = true
	}
	rdmaMACs := map[string]bool{}
	for _, item := range a.Machine.RDMA {
		if strings.TrimSpace(item.MAC) != "" {
			rdmaMACs[strings.TrimSpace(item.MAC)] = true
		}
	}
	candidates := make([]netDevice, 0, len(devices))
	for _, device := range devices {
		if rdmaNames[device.Name] || rdmaMACs[device.MAC] {
			continue
		}
		candidates = append(candidates, device)
	}
	sortNetDevices(candidates)
	return candidates, nil
}

func (a *App) discoverRDMADevices() ([]netDevice, error) {
	devices, err := a.discoverNetDevices()
	if err != nil {
		return nil, err
	}
	decision := a.recommendNICRoles(devices, len(a.Machine.MgmtIfaces), false)
	if decision.Confidence == nicdetect.ConfidenceStrong || decision.Confidence == nicdetect.ConfidenceExact {
		selected := netDevicesForBindings(devices, decision.RDMA)
		if len(selected) == len(a.Machine.RDMA) {
			return selected, nil
		}
	}
	return netDevicesExcludingBindings(devices, decision.Management), nil
}

func (a *App) recommendNICRoles(devices []netDevice, managementCount int, allowRDMAExpansion bool) nicdetect.Decision {
	plan := nicdetect.Plan{
		ManagementCount:    managementCount,
		RDMACount:          len(a.Machine.RDMA),
		AllowRDMAExpansion: allowRDMAExpansion,
	}
	for idx, name := range a.Machine.MgmtIfaces {
		hint := nicdetect.SlotHint{Index: idx, Name: name, IP: a.Machine.MgmtIP, Prefix: a.Machine.MgmtPrefix}
		if idx < len(a.Machine.MgmtMACs) {
			hint.MAC = a.Machine.MgmtMACs[idx]
		}
		plan.ManagementHints = append(plan.ManagementHints, hint)
	}
	for idx, item := range a.Machine.RDMA {
		plan.RDMAHints = append(plan.RDMAHints, nicdetect.SlotHint{Index: idx, Name: item.Name, MAC: item.MAC, IP: item.IP, Prefix: item.Prefix})
	}
	return recommendNICRolesWithPlan(devices, plan)
}

func recommendNICRolesWithPlan(devices []netDevice, plan nicdetect.Plan) nicdetect.Decision {
	facts := make([]nicdetect.Facts, 0, len(devices))
	for _, item := range devices {
		facts = append(facts, netDeviceFacts(item))
	}
	return nicdetect.Recommend(plan, facts)
}

func netDeviceFacts(item netDevice) nicdetect.Facts {
	return nicdetect.Facts{
		Name:             item.Name,
		MAC:              item.MAC,
		PCI:              item.PCI,
		Driver:           item.Driver,
		VendorID:         item.VendorID,
		DeviceID:         item.DeviceID,
		CurrentSpeedMbps: item.SpeedMbps,
		MaxSpeedMbps:     item.MaxSpeedMbps,
		MTU:              item.MTU,
		LinkUp:           item.CarrierUp,
		LinkKnown:        item.CarrierKnown,
		HasRDMA:          item.HasInfiniband || strings.EqualFold(item.Driver, "mlx5_core"),
		PhysPortName:     item.PhysPortName,
		DevPort:          item.DevPort,
		HasDevPort:       item.HasDevPort,
	}
}

func netDevicesForBindings(devices []netDevice, bindings []nicdetect.Binding) []netDevice {
	byName := map[string]netDevice{}
	for _, item := range devices {
		byName[item.Name] = item
	}
	out := make([]netDevice, 0, len(bindings))
	for _, binding := range bindings {
		if item, ok := byName[binding.NIC.Name]; ok {
			out = append(out, item)
		}
	}
	sortNetDevicesPhysical(out)
	return out
}

func netDevicesExcludingBindings(devices []netDevice, bindings []nicdetect.Binding) []netDevice {
	excluded := map[string]bool{}
	for _, binding := range bindings {
		excluded[binding.NIC.Name] = true
	}
	var out []netDevice
	for _, item := range devices {
		if !excluded[item.Name] {
			out = append(out, item)
		}
	}
	sortNetDevices(out)
	return out
}

func (a *App) discoverNetDevices() ([]netDevice, error) {
	return a.discoverNetDevicesWithOptions(false)
}

func (a *App) discoverReviewNetDevices() ([]netDevice, error) {
	return a.discoverNetDevicesWithOptions(true)
}

func (a *App) discoverNetDevicesWithOptions(allowInvalidMAC bool) ([]netDevice, error) {
	dir := a.targetPath("/sys/class/net")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	devices := make([]netDevice, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		name := entry.Name()
		if shouldIgnoreNetDeviceName(name) {
			continue
		}
		mac, ok := a.readDiscoveredMAC(name, allowInvalidMAC)
		if !ok {
			continue
		}
		devicePath, _ := filepath.EvalSymlinks(filepath.Join(dir, name, "device"))
		driver := ""
		if devicePath != "" {
			if driverPath, err := filepath.EvalSymlinks(filepath.Join(devicePath, "driver")); err == nil {
				driver = filepath.Base(driverPath)
			}
		}
		pci := ""
		if devicePath != "" {
			pci = filepath.Base(devicePath)
		}
		devPort, hasDevPort := readOptionalInt(filepath.Join(dir, name, "dev_port"))
		speed, _ := readOptionalInt(filepath.Join(dir, name, "speed"))
		maxSpeed := ethtoolMaxSupportedSpeed(name)
		if maxSpeed <= 0 {
			maxSpeed = speed
		}
		mtu, _ := readOptionalInt(filepath.Join(dir, name, "mtu"))
		carrier, carrierKnown := a.detectLinkState(name, filepath.Join(dir, name, "carrier"))
		devices = append(devices, netDevice{
			Name:          name,
			MAC:           mac,
			PCI:           pci,
			Driver:        driver,
			SpeedMbps:     speed,
			MaxSpeedMbps:  maxSpeed,
			MTU:           mtu,
			VendorID:      readDeviceOptionalTrim(devicePath, "vendor"),
			DeviceID:      readDeviceOptionalTrim(devicePath, "device"),
			PhysPortName:  readOptionalTrim(filepath.Join(dir, name, "phys_port_name")),
			OperState:     readOptionalTrim(filepath.Join(dir, name, "operstate")),
			CarrierUp:     carrier,
			CarrierKnown:  carrierKnown,
			HasInfiniband: hasInfinibandDevice(devicePath),
			DevPort:       devPort,
			HasDevPort:    hasDevPort,
		})
	}
	sortNetDevices(devices)
	return devices, nil
}

func (a *App) readDiscoveredMAC(iface string, allowInvalid bool) (string, bool) {
	if mac, ok := readSysfsInterfaceMAC(a.targetPath("/sys/class/net"), iface); ok {
		return mac, true
	}
	if allowInvalid {
		return "", true
	}
	return "", false
}

func sortNetDevices(devices []netDevice) {
	sort.Slice(devices, func(i, j int) bool {
		left, right := devices[i], devices[j]
		leftScore := netDeviceAutoOrderScore(left)
		rightScore := netDeviceAutoOrderScore(right)
		if leftScore != rightScore {
			return leftScore > rightScore
		}
		if left.PCI != right.PCI {
			return left.PCI < right.PCI
		}
		if left.HasDevPort != right.HasDevPort {
			return left.HasDevPort
		}
		if left.DevPort != right.DevPort {
			return left.DevPort < right.DevPort
		}
		if left.PhysPortName != right.PhysPortName {
			return left.PhysPortName < right.PhysPortName
		}
		return left.Name < right.Name
	})
}

func sortNetDevicesPhysical(devices []netDevice) {
	sort.Slice(devices, func(i, j int) bool {
		left, right := devices[i], devices[j]
		if left.PCI != right.PCI {
			return left.PCI < right.PCI
		}
		if left.HasDevPort != right.HasDevPort {
			return left.HasDevPort
		}
		if left.DevPort != right.DevPort {
			return left.DevPort < right.DevPort
		}
		if left.PhysPortName != right.PhysPortName {
			return left.PhysPortName < right.PhysPortName
		}
		return left.Name < right.Name
	})
}

func shouldIgnoreNetDeviceName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return true
	}
	if name == "lo" {
		return true
	}
	prefixes := []string{
		"bond",
		"team",
		"dummy",
		"ifb",
		"tap",
		"tun",
		"tunl",
		"sit",
		"gre",
		"gretap",
		"erspan",
		"ip6gre",
		"ip6tnl",
		"ip_vti",
		"vti",
		"vxlan",
		"genev",
		"flannel",
		"cni",
		"kube",
		"weave",
		"cilium",
		"calico",
		"cali",
		"docker",
		"podman",
		"nerdctl",
		"containerd",
		"veth",
		"lxc",
		"lxd",
		"virbr",
		"br",
		"br-",
		"ovs",
		"ovs-system",
		"wg",
		"tailscale",
		"zt",
		"nebula",
		"macvlan",
		"ipvlan",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func netDeviceAutoOrderScore(device netDevice) int {
	score := 0
	if device.CarrierKnown && device.CarrierUp {
		score += 100
	}
	if strings.EqualFold(device.OperState, "up") {
		score += 20
	}
	if device.SpeedMbps > 0 {
		score += 10
	}
	return score
}

func readOptionalTrim(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func readOptionalInt(path string) (int, bool) {
	raw := readOptionalTrim(path)
	if raw == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return value, true
}

func readCarrier(path string) (bool, bool) {
	raw := readOptionalTrim(path)
	switch raw {
	case "1":
		return true, true
	case "0":
		return false, true
	default:
		return false, false
	}
}

func (a *App) detectLinkState(iface string, carrierPath string) (bool, bool) {
	if linkUp, ok := ethtoolLinkDetected(iface); ok {
		return linkUp, true
	}
	return readCarrier(carrierPath)
}

func ethtoolLinkDetected(iface string) (bool, bool) {
	if strings.TrimSpace(iface) == "" {
		return false, false
	}
	output, err := exec.Command("ethtool", iface).CombinedOutput()
	if err != nil {
		return false, false
	}
	for _, line := range strings.Split(string(output), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "Link detected") {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "yes":
			return true, true
		case "no":
			return false, true
		}
	}
	return false, false
}

func ethtoolMaxSupportedSpeed(iface string) int {
	if strings.TrimSpace(iface) == "" {
		return 0
	}
	output, err := exec.Command("ethtool", iface).CombinedOutput()
	if err != nil {
		return 0
	}
	inSupportedModes := false
	maxSpeed := 0
	for _, line := range strings.Split(string(output), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Supported link modes:") {
			inSupportedModes = true
		}
		if inSupportedModes && strings.HasPrefix(trimmed, "Supported ") && !strings.HasPrefix(trimmed, "Supported link modes:") {
			break
		}
		if !inSupportedModes {
			continue
		}
		for _, field := range strings.Fields(trimmed) {
			idx := strings.Index(field, "base")
			if idx <= 0 {
				continue
			}
			value, err := strconv.Atoi(field[:idx])
			if err == nil && value > maxSpeed {
				maxSpeed = value
			}
		}
	}
	return maxSpeed
}

func hasInfinibandDevice(devicePath string) bool {
	if strings.TrimSpace(devicePath) == "" {
		return false
	}
	entries, err := os.ReadDir(filepath.Join(devicePath, "infiniband"))
	return err == nil && len(entries) > 0
}

func readDeviceOptionalTrim(devicePath string, name string) string {
	if strings.TrimSpace(devicePath) == "" {
		return ""
	}
	return readOptionalTrim(filepath.Join(devicePath, name))
}

func describeNetDevices(devices []netDevice) string {
	if len(devices) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(devices))
	for _, device := range devices {
		parts = append(parts, fmt.Sprintf("%s(mac=%s,pci=%s,driver=%s,speed=%s,link=%s,ib=%t,model=%s,port=%s,dev_port=%d)", device.Name, device.MAC, device.PCI, device.Driver, deviceSpeedLabel(device), deviceLinkLabel(device), device.HasInfiniband, deviceModelLabel(device), device.PhysPortName, device.DevPort))
	}
	return strings.Join(parts, ", ")
}
