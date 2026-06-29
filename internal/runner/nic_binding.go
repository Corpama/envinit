package runner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"envinit/internal/spec"
)

type interfaceBinding struct {
	Kind        string
	Name        string
	MAC         string
	CurrentName string
	Address     string
	Gateway     string
	Table       int
	NeedsReview bool
}

type netDevice struct {
	Name          string
	MAC           string
	PCI           string
	Driver        string
	SpeedMbps     int
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
	return bindings, nil
}

func (a *App) ensureAutoManagementInterfaces() error {
	if len(a.Machine.MgmtIfaces) > 0 {
		return nil
	}
	devices, err := a.discoverMgmtDevices()
	if err != nil {
		return err
	}
	switch len(devices) {
	case 0:
		return fmt.Errorf("no management interfaces are configured and no non-RDMA management candidates were discovered; configure defaults.mgmt_interfaces or inventory mgmt_iface fields")
	case 1, 2:
	default:
		return fmt.Errorf("no management interfaces are configured and discovered %d management candidates: %s; configure defaults.mgmt_interfaces or inventory mgmt_iface fields to avoid ambiguous bonding", len(devices), describeNetDevices(devices))
	}
	a.Machine.MgmtIfaces = make([]string, 0, len(devices))
	a.Machine.MgmtMACs = make([]string, 0, len(devices))
	for _, device := range devices {
		a.Machine.MgmtIfaces = append(a.Machine.MgmtIfaces, device.Name)
		a.Machine.MgmtMACs = append(a.Machine.MgmtMACs, device.MAC)
	}
	return nil
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
		}
		out = append(out, interfaceBinding{
			Kind:        "mgmt",
			Name:        name,
			MAC:         mac,
			CurrentName: current,
			Address:     plannedAddress(a.Machine.MgmtIP, a.Machine.MgmtPrefix),
			Gateway:     a.Machine.MgmtGateway,
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
		current := item.Name
		if device, ok := deviceByMAC[mac]; ok {
			current = device.Name
		}
		out = append(out, interfaceBinding{
			Kind:        "rdma",
			Name:        item.Name,
			MAC:         mac,
			CurrentName: current,
			Address:     plannedAddress(item.IP, item.Prefix),
			Gateway:     item.Gateway,
			Table:       item.Table,
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
			Kind:        "mgmt",
			Name:        name,
			MAC:         device.MAC,
			CurrentName: device.Name,
			Address:     plannedAddress(a.Machine.MgmtIP, a.Machine.MgmtPrefix),
			Gateway:     a.Machine.MgmtGateway,
		})
	}
	return out, nil
}

func (a *App) manualMgmtBindings(autoCandidates []netDevice) ([]interfaceBinding, error) {
	a.logf("management auto discovery matched %d candidate(s) for %d target name(s); entering manual NIC binding review without applying automatic candidates. Tip: set mgmt*_mac values in inventory for exact matching.", len(autoCandidates), len(a.Machine.MgmtIfaces))
	out := make([]interfaceBinding, 0, len(a.Machine.MgmtIfaces))
	for _, name := range a.Machine.MgmtIfaces {
		out = append(out, interfaceBinding{
			Kind:        "mgmt",
			Name:        name,
			Address:     plannedAddress(a.Machine.MgmtIP, a.Machine.MgmtPrefix),
			Gateway:     a.Machine.MgmtGateway,
			NeedsReview: true,
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
			Kind:        "rdma",
			Name:        item.Name,
			MAC:         device.MAC,
			CurrentName: device.Name,
			Address:     plannedAddress(item.IP, item.Prefix),
			Gateway:     item.Gateway,
			Table:       item.Table,
		})
	}
	return out, nil
}

func (a *App) manualRDMABindings(autoCandidates []netDevice) []interfaceBinding {
	a.logf("RDMA auto discovery matched %d candidate(s) for %d target name(s); entering manual NIC binding review without applying automatic candidates. Tip: set rdma*_mac values in inventory for exact matching.", len(autoCandidates), len(a.Machine.RDMA))
	out := make([]interfaceBinding, 0, len(a.Machine.RDMA))
	for _, item := range a.Machine.RDMA {
		out = append(out, interfaceBinding{
			Kind:        "rdma",
			Name:        item.Name,
			Address:     plannedAddress(item.IP, item.Prefix),
			Gateway:     item.Gateway,
			Table:       item.Table,
			NeedsReview: true,
		})
	}
	return out
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
	seen := map[string]bool{}
	var b strings.Builder
	b.WriteString("# This file is autogenerated. Do not edit manually.\n")
	for _, item := range bindings {
		if seen[item.Name] {
			continue
		}
		seen[item.Name] = true
		fmt.Fprintf(&b, "SUBSYSTEM==\"net\", ACTION==\"add\", DRIVERS==\"?*\", ATTR{address}==\"%s\", ATTR{type}==\"1\", NAME=\"%s\"\n", item.MAC, item.Name)
	}
	return b.String()
}

func (a *App) renameRDMATemporarily(bindings []interfaceBinding) error {
	return a.renameInterfacesTemporarily(bindings)
}

func (a *App) renameInterfacesTemporarily(bindings []interfaceBinding) error {
	type pendingRename struct {
		current string
		target  string
		temp    string
	}
	var pending []pendingRename
	reservedTemps := map[string]bool{}
	for _, binding := range bindings {
		current := strings.TrimSpace(binding.CurrentName)
		target := strings.TrimSpace(binding.Name)
		if current == "" || target == "" || current == target {
			continue
		}
		temp := a.availableTempInterfaceName(len(pending), reservedTemps)
		reservedTemps[temp] = true
		pending = append(pending, pendingRename{
			current: current,
			target:  target,
			temp:    temp,
		})
	}
	if len(pending) == 0 {
		return nil
	}
	for _, item := range pending {
		if err := a.runCmd("", nil, "ip", "link", "set", "dev", item.current, "down"); err != nil {
			return err
		}
		if err := a.runCmd("", nil, "ip", "link", "set", "dev", item.current, "name", item.temp); err != nil {
			return err
		}
	}
	for _, item := range pending {
		if err := a.runCmd("", nil, "ip", "link", "set", "dev", item.temp, "name", item.target); err != nil {
			return err
		}
	}
	return nil
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
	path := a.targetPath(filepath.Join("/sys/class/net", iface, "address"))
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read mac for %s: %w", iface, err)
	}
	mac, err := spec.NormalizeMAC(strings.TrimSpace(string(data)))
	if err != nil {
		return "", fmt.Errorf("normalize mac for %s: %w", iface, err)
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
	if len(a.Machine.MgmtIfaces) == 0 {
		sortNetDevices(candidates)
		return candidates, nil
	}
	return selectMgmtDevices(candidates, len(a.Machine.MgmtIfaces)), nil
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
	mgmtNames := map[string]bool{}
	for _, name := range a.Machine.MgmtIfaces {
		mgmtNames[name] = true
	}
	mgmtMACs := map[string]bool{}
	for _, mac := range a.Machine.MgmtMACs {
		if strings.TrimSpace(mac) != "" {
			mgmtMACs[strings.TrimSpace(mac)] = true
		}
	}
	candidates := make([]netDevice, 0, len(devices))
	for _, device := range devices {
		if mgmtNames[device.Name] || mgmtMACs[device.MAC] {
			continue
		}
		candidates = append(candidates, device)
	}
	return selectRDMADevices(candidates, len(a.Machine.RDMA)), nil
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
		carrier, carrierKnown := a.detectLinkState(name, filepath.Join(dir, name, "carrier"))
		devices = append(devices, netDevice{
			Name:          name,
			MAC:           mac,
			PCI:           pci,
			Driver:        driver,
			SpeedMbps:     speed,
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
	path := a.targetPath(filepath.Join("/sys/class/net", iface, "address"))
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	raw := strings.TrimSpace(string(data))
	mac, err := spec.NormalizeMAC(raw)
	if err != nil {
		if allowInvalid {
			return "", true
		}
		return "", false
	}
	return mac, true
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

type netDeviceSpeedGroup struct {
	Speed   int
	Devices []netDevice
}

type netDeviceModelGroup struct {
	Key     string
	Speed   int
	Devices []netDevice
}

func selectMgmtDevices(devices []netDevice, need int) []netDevice {
	if need <= 0 {
		return nil
	}
	groups := speedGroups(devices, true)
	selected := selectFromSortedSpeedGroups(groups, need, true)
	if len(selected) == need {
		return selected
	}
	if byModel := selectFromModelGroups(devices, need, true); len(byModel) == need {
		return byModel
	}
	return selected
}

func selectRDMADevices(devices []netDevice, need int) []netDevice {
	if need <= 0 {
		return nil
	}
	groups := speedGroups(devices, false)
	selected := selectFromSortedSpeedGroups(groups, need, false)
	if len(selected) == need {
		return selected
	}
	if byModel := selectFromModelGroups(devices, need, false); len(byModel) == need {
		return byModel
	}
	if len(selected) > 0 {
		return selected
	}
	return selectLegacyRDMADevices(devices, need)
}

func speedGroups(devices []netDevice, forMgmt bool) []netDeviceSpeedGroup {
	bySpeed := map[int][]netDevice{}
	for _, device := range devices {
		if device.SpeedMbps <= 0 {
			continue
		}
		if !forMgmt && device.SpeedMbps < 200000 && !device.HasInfiniband && !strings.EqualFold(device.Driver, "mlx5_core") {
			continue
		}
		bySpeed[device.SpeedMbps] = append(bySpeed[device.SpeedMbps], device)
	}
	groups := make([]netDeviceSpeedGroup, 0, len(bySpeed))
	for speed, items := range bySpeed {
		sortNetDevices(items)
		groups = append(groups, netDeviceSpeedGroup{Speed: speed, Devices: items})
	}
	return groups
}

func selectFromSortedSpeedGroups(groups []netDeviceSpeedGroup, need int, preferLowerSpeed bool) []netDevice {
	if len(groups) == 0 {
		return nil
	}
	sortSpeedGroups(groups, preferLowerSpeed)
	return selectFromSpeedGroups(groups, need, preferLowerSpeed)
}

func selectFromSpeedGroups(groups []netDeviceSpeedGroup, need int, preferLowerSpeed bool) []netDevice {
	var eligible []netDeviceSpeedGroup
	for _, group := range groups {
		if len(group.Devices) >= need {
			eligible = append(eligible, group)
		}
	}
	if len(eligible) == 0 {
		return nil
	}

	var linked []netDeviceSpeedGroup
	for _, group := range eligible {
		if linkUpCount(group.Devices) >= need {
			linked = append(linked, group)
		}
	}
	if len(linked) > 0 {
		sortSpeedGroups(linked, preferLowerSpeed)
		return firstN(linkUpFirst(linked[0].Devices), need)
	}

	var exact []netDeviceSpeedGroup
	for _, group := range eligible {
		if len(group.Devices) == need {
			exact = append(exact, group)
		}
	}
	if len(exact) > 0 {
		sortSpeedGroups(exact, preferLowerSpeed)
		return firstN(exact[0].Devices, need)
	}

	sortSpeedGroups(eligible, preferLowerSpeed)
	// Ambiguous: enough devices exist in this speed group, but link state does
	// not identify exactly which ports should be used. Return the whole group
	// so the caller falls back to the manual review path.
	return eligible[0].Devices
}

func selectFromModelGroups(devices []netDevice, need int, preferLowerSpeed bool) []netDevice {
	groups := modelGroups(devices)
	if len(groups) == 0 {
		return nil
	}

	var exact []netDeviceModelGroup
	for _, group := range groups {
		if len(group.Devices) == need {
			exact = append(exact, group)
		}
	}
	if len(exact) > 0 {
		sortModelGroups(exact, preferLowerSpeed)
		return firstN(exact[0].Devices, need)
	}

	var linked []netDeviceModelGroup
	for _, group := range groups {
		if len(group.Devices) > need && linkUpCount(group.Devices) >= need {
			linked = append(linked, group)
		}
	}
	if len(linked) > 0 {
		sortModelGroups(linked, preferLowerSpeed)
		return firstN(linkUpFirst(linked[0].Devices), need)
	}

	return nil
}

func modelGroups(devices []netDevice) []netDeviceModelGroup {
	byModel := map[string][]netDevice{}
	for _, device := range devices {
		key := deviceModelGroupKey(device)
		if key == "" {
			continue
		}
		byModel[key] = append(byModel[key], device)
	}

	groups := make([]netDeviceModelGroup, 0, len(byModel))
	for key, items := range byModel {
		sortNetDevices(items)
		groups = append(groups, netDeviceModelGroup{
			Key:     key,
			Speed:   representativeSpeed(items),
			Devices: items,
		})
	}
	return groups
}

func deviceModelGroupKey(device netDevice) string {
	vendor := strings.ToLower(strings.TrimSpace(device.VendorID))
	deviceID := strings.ToLower(strings.TrimSpace(device.DeviceID))
	if vendor != "" || deviceID != "" {
		return "pci:" + vendor + ":" + deviceID
	}
	driver := strings.ToLower(strings.TrimSpace(device.Driver))
	if driver != "" {
		return "driver:" + driver
	}
	return ""
}

func representativeSpeed(devices []netDevice) int {
	for _, device := range devices {
		if device.SpeedMbps > 0 {
			return device.SpeedMbps
		}
	}
	return 0
}

func selectLegacyRDMADevices(devices []netDevice, need int) []netDevice {
	var out []netDevice
	for _, device := range devices {
		if strings.EqualFold(device.Driver, "mlx5_core") || device.HasInfiniband {
			out = append(out, device)
		}
	}
	sortNetDevices(out)
	if len(out) >= need {
		return out
	}
	return nil
}

func sortModelGroups(groups []netDeviceModelGroup, lowerFirst bool) {
	sort.Slice(groups, func(i, j int) bool {
		leftLinked := linkUpCount(groups[i].Devices)
		rightLinked := linkUpCount(groups[j].Devices)
		if leftLinked != rightLinked {
			return leftLinked > rightLinked
		}
		if groups[i].Speed != groups[j].Speed {
			if lowerFirst {
				return groups[i].Speed < groups[j].Speed
			}
			return groups[i].Speed > groups[j].Speed
		}
		return groups[i].Key < groups[j].Key
	})
}

func sortSpeedGroups(groups []netDeviceSpeedGroup, lowerFirst bool) {
	sort.Slice(groups, func(i, j int) bool {
		leftLinked := linkUpCount(groups[i].Devices)
		rightLinked := linkUpCount(groups[j].Devices)
		if leftLinked != rightLinked {
			return leftLinked > rightLinked
		}
		if lowerFirst {
			return groups[i].Speed < groups[j].Speed
		}
		return groups[i].Speed > groups[j].Speed
	})
}

func linkUpCount(devices []netDevice) int {
	count := 0
	for _, device := range devices {
		if device.CarrierKnown && device.CarrierUp {
			count++
		}
	}
	return count
}

func linkUpFirst(devices []netDevice) []netDevice {
	out := append([]netDevice(nil), devices...)
	sort.SliceStable(out, func(i, j int) bool {
		left, right := out[i], out[j]
		if left.CarrierKnown && right.CarrierKnown && left.CarrierUp != right.CarrierUp {
			return left.CarrierUp
		}
		if left.CarrierKnown != right.CarrierKnown {
			return left.CarrierKnown
		}
		return false
	})
	return out
}

func firstN(devices []netDevice, n int) []netDevice {
	if len(devices) <= n {
		return devices
	}
	return append([]netDevice(nil), devices[:n]...)
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
