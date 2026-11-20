package configure

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"envinit/internal/inventory"
	"envinit/internal/spec"
)

type Template struct {
	Bundle                   spec.Bundle
	Table                    *inventory.TemplateTable
	BundleJSON, InventoryCSV []byte
	Pending, Review, Info    []string
	Machines                 []spec.MachineConfig
}

// PrepareTemplate validates supplied values independently of completeness.
// Runtime resolution is used for derived values, never as a completeness gate.
func PrepareTemplate(data []byte, table *inventory.TemplateTable) (*Template, error) {
	b, err := DecodeBundle(data)
	if err != nil {
		return nil, err
	}
	if b.Platform.OSFamily == "" || b.Platform.OSFamily == "auto" {
		return nil, fmt.Errorf("target platform.os_family must be explicit in the bundle template")
	}
	// Do not inherit the runtime's power-action default into a new template.
	if b.PostPowerAction.Action == "" {
		b.PostPowerAction.Action = "none"
	}
	b.ApplyDefaults()
	if err := validateTemplatePolicies(b); err != nil {
		return nil, err
	}
	// Work on a copy so failed reviews do not mutate the caller's import.
	tableCopy, err := inventory.ParseTemplate(table.CSV())
	if err != nil {
		return nil, err
	}
	t := &Template{Bundle: b, Table: tableCopy, Review: append(append([]string{}, b.Warnings...), table.Warnings...)}
	records, err := tableCopy.Records()
	if err != nil {
		return nil, err
	}
	// Legacy defaults are removed by Bundle.MarshalJSON; materialize explicit
	// per-machine defaults first so normalization cannot discard user planning.
	for i, r := range records {
		fill := func(field, current, fallback string) {
			if current == "" && fallback != "" {
				t.Table.Set(i, field, fallback)
				t.Info = append(t.Info, fmt.Sprintf("row %d %s <- legacy defaults: %s", i+2, field, fallback))
			}
		}
		fill("mgmt_target_name", r.MgmtTargetName, b.Defaults.MgmtBondName)
		if b.Defaults.MgmtPrefix > 0 {
			fill("mgmt_prefix", r.MgmtPrefix, strconv.Itoa(b.Defaults.MgmtPrefix))
		}
		fill("mgmt_gateway", r.MgmtGateway, b.Defaults.MgmtGateway)
		for j, name := range b.Defaults.MgmtInterfaces {
			if j == 0 {
				fill("mgmt_iface1", r.MgmtIface1, name)
			}
			if j == 1 {
				fill("mgmt_iface2", r.MgmtIface2, name)
			}
		}
		count := len(r.RDMA)
		if len(b.Defaults.RDMAInterfaces) > count {
			count = len(b.Defaults.RDMAInterfaces)
		}
		for j := 0; j < count; j++ {
			p := spec.RDMARecord{}
			if j < len(r.RDMA) {
				p = r.RDMA[j]
			}
			prefix := fmt.Sprintf("rdma%d_", j+1)
			if b.Defaults.RDMAPrefix > 0 {
				fill(prefix+"prefix", p.Prefix, strconv.Itoa(b.Defaults.RDMAPrefix))
			}
			if j < len(b.Defaults.RDMAInterfaces) {
				d := b.Defaults.RDMAInterfaces[j]
				fill(prefix+"name", p.Name, d.Name)
				fill(prefix+"gateway", p.Gateway, d.Gateway)
			}
		}
	}
	records, err = t.Table.Records()
	if err != nil {
		return nil, err
	}
	hosts, ips, macs := map[string]string{}, map[string]string{}, map[string]string{}
	unique := func(values map[string]string, value, label string) error {
		if value == "" {
			return nil
		}
		if previous := values[value]; previous != "" {
			return fmt.Errorf("duplicate value %s: %s and %s", value, previous, label)
		}
		values[value] = label
		return nil
	}
	if len(records) == 0 {
		t.Pending = append(t.Pending, "inventory: 添加机器行并填写 hostname、管理网络及 RDMA 规划")
	}
	for i, r := range records {
		label := fmt.Sprintf("row %d (%s)", i+2, r.Hostname)
		if r.Hostname == "" {
			t.Pending = append(t.Pending, label+": hostname")
		} else {
			if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*$`).MatchString(r.Hostname) || len(r.Hostname) > 253 {
				return nil, fmt.Errorf("%s: invalid hostname", label)
			}
			if err := unique(hosts, strings.ToLower(r.Hostname), label+" hostname"); err != nil {
				return nil, err
			}
		}
		mode := strings.ToLower(r.MgmtMode)
		if mode != "" && mode != "auto" && mode != "single" && mode != "bond" {
			return nil, fmt.Errorf("%s: invalid mgmt_mode %q", label, mode)
		}
		one := r.MgmtIface1 != "" || r.MgmtMAC1 != ""
		two := r.MgmtIface2 != "" || r.MgmtMAC2 != ""
		if mode == "single" && one && two {
			return nil, fmt.Errorf("%s: single mode has two management slots", label)
		}
		if mode == "" || mode == "auto" {
			if one && two {
				mode = "bond"
			} else if one || two {
				mode = "single"
			}
		}
		if mode == "single" && r.MgmtTargetName != "" {
			iface := r.MgmtIface1
			if iface == "" {
				iface = r.MgmtIface2
			}
			if iface != "" && iface != r.MgmtTargetName {
				return nil, fmt.Errorf("%s: single mgmt_target_name conflicts with interface", label)
			}
		}
		names := map[string]string{}
		for _, name := range []string{r.MgmtIface1, r.MgmtIface2} {
			if err := checkInterfaceName(name); err != nil {
				return nil, fmt.Errorf("%s: %w", label, err)
			}
			if err := unique(names, name, label+" management interface"); err != nil {
				return nil, err
			}
		}
		if err := checkInterfaceName(r.MgmtTargetName); err != nil {
			return nil, err
		}
		if mode == "bond" {
			name := r.MgmtTargetName
			if name == "" {
				name = "bond0"
			}
			if err := unique(names, name, label+" bond target"); err != nil {
				return nil, err
			}
		}
		if err := t.address(r.MgmtIP, r.MgmtPrefix, r.MgmtGateway, label+" management", b.ConfigureManagementNetwork()); err != nil {
			return nil, err
		}
		if err := unique(ips, r.MgmtIP, label+" mgmt_ip"); err != nil {
			return nil, err
		}
		allMACs := []string{r.MgmtMAC1, r.MgmtMAC2}
		tables := map[string]string{}
		for j, p := range r.RDMA {
			portLabel := fmt.Sprintf("%s rdma%d", label, j+1)
			if err := t.address(p.IP, p.Prefix, p.Gateway, portLabel, b.RDMAConfigureIPRoute()); err != nil {
				return nil, err
			}
			if err := unique(ips, p.IP, portLabel+" IP"); err != nil {
				return nil, err
			}
			allMACs = append(allMACs, p.MAC)
			if p.Table != "" {
				n, e := strconv.ParseUint(p.Table, 10, 32)
				if e != nil || n == 0 {
					return nil, fmt.Errorf("%s: invalid route table %q", portLabel, p.Table)
				}
			}
			if b.RDMAConfigureIPRoute() {
				table := p.Table
				if table == "" {
					table = strconv.Itoa(101 + j)
				}
				n, _ := strconv.ParseUint(table, 10, 32)
				if err := unique(tables, strconv.FormatUint(n, 10), portLabel+" table"); err != nil {
					return nil, err
				}
			}
			if p.RouteCIDR != "" && p.RouteCIDR != "auto" {
				ip, _, e := net.ParseCIDR(p.RouteCIDR)
				if e != nil || ip.To4() == nil {
					return nil, fmt.Errorf("%s: invalid route CIDR %q", portLabel, p.RouteCIDR)
				}
			}
			name := p.Name
			if name == "" {
				name = fmt.Sprintf("xgbe%d", j)
			}
			if err := checkInterfaceName(name); err != nil {
				return nil, err
			}
			if err := unique(names, name, portLabel+" name"); err != nil {
				return nil, err
			}
			if p.MAC == "" && b.RDMAExists() {
				t.Pending = append(t.Pending, portLabel+": MAC/物理接口对应关系（人工或现场确认）")
			}
		}
		for _, mac := range allMACs {
			normal, e := spec.NormalizeMAC(mac)
			if e != nil {
				return nil, fmt.Errorf("%s: %w", label, e)
			}
			if err := unique(macs, normal, label+" MAC"); err != nil {
				return nil, err
			}
		}
		if b.ConfigureManagementNetwork() && (!one || (mode == "bond" && !two)) {
			t.Pending = append(t.Pending, label+": 管理接口/MAC（人工或现场确认）")
		}
		// Resolve only complete management addresses. RDMA names-only resolution
		// still supplies exactly the shared name/table/CIDR derivation rules.
		previewBundle := b
		disabled := false
		if r.MgmtIP == "" || r.MgmtPrefix == "" || r.MgmtGateway == "" {
			previewBundle.Defaults.ConfigureManagementNetwork = &disabled
		}
		if b.RDMAExists() {
			previewBundle.Defaults.RDMAMode = spec.RDMAModeNamesOnly
		}
		previewRecord := r
		if mode == "single" && r.MgmtIface1 == "" && r.MgmtIface2 == "" && r.MgmtMAC1 == "" && r.MgmtMAC2 == "" {
			// A target name without a physical binding is pending, not a conflict.
			previewRecord.MgmtTargetName = ""
		}
		if previewRecord.Hostname == "" {
			previewRecord.Hostname = "pending"
		}
		m, e := spec.ResolveMachine(previewBundle, previewRecord, nil)
		if e != nil {
			return nil, fmt.Errorf("%s: %w", label, e)
		}
		m.Hostname = r.Hostname
		m.HostID = r.Hostname
		t.Machines = append(t.Machines, m)
		for j, port := range m.RDMA {
			p := r.RDMA[j]
			// Keep the ordinary inventory compact. Name is useful as a planning
			// hint, while table and route CIDR are derived at runtime from the
			// slot and the IP/prefix and must not be materialized into new files.
			for _, field := range []struct{ name, current, derived string }{{"name", p.Name, port.Name}} {
				if field.current == "" && field.derived != "" {
					column := fmt.Sprintf("rdma%d_%s", j+1, field.name)
					t.Table.Set(i, column, field.derived)
					t.Info = append(t.Info, fmt.Sprintf("%s %s = %s（公共规则派生）", label, column, field.derived))
				}
			}
		}
	}
	if b.PostPowerAction.Action != "none" {
		t.Review = append(t.Review, "post_power_action="+b.PostPowerAction.Action+"；请核对执行后的电源动作")
	}
	t.Info = append(t.Info, "auto 参数在目标现场解析；config 不采集现场信息。")
	t.BundleJSON, err = json.MarshalIndent(b, "", "  ")
	if err != nil {
		return nil, err
	}
	t.BundleJSON = append(t.BundleJSON, '\n')
	t.InventoryCSV = t.Table.CSV()
	sort.Strings(t.Review)
	return t, nil
}

func (t *Template) address(ip, prefix, gateway, label string, required bool) error {
	for _, item := range []struct{ name, value string }{{"IP", ip}, {"prefix", prefix}, {"gateway", gateway}} {
		if item.value == "" {
			if required {
				t.Pending = append(t.Pending, label+": "+item.name)
			}
			continue
		}
		if item.name == "prefix" {
			n, err := strconv.Atoi(item.value)
			if err != nil || n < 1 || n > 31 {
				return fmt.Errorf("%s: prefix must be 1..31", label)
			}
		} else {
			a := net.ParseIP(item.value)
			if a == nil || a.To4() == nil || !a.IsGlobalUnicast() || a.IsLoopback() {
				return fmt.Errorf("%s: invalid unicast IPv4 %q", label, item.value)
			}
		}
	}
	if ip != "" && gateway != "" && ip == gateway {
		return fmt.Errorf("%s: IP equals gateway", label)
	}
	if prefix != "" {
		n, _ := strconv.Atoi(prefix)
		for _, value := range []string{ip, gateway} {
			if value == "" || n == 31 {
				continue
			}
			a := net.ParseIP(value).To4()
			mask := net.CIDRMask(n, 32)
			network, broadcast := true, true
			for i := range a {
				network = network && a[i]&^mask[i] == 0
				broadcast = broadcast && a[i]|mask[i] == 255
			}
			if network || broadcast {
				return fmt.Errorf("%s: network/broadcast address cannot be used", label)
			}
		}
	}
	if ip != "" && prefix != "" && gateway != "" {
		return validateAddress(ip, prefix, gateway, label)
	}
	return nil
}

func checkInterfaceName(name string) error {
	if name != "" && (name == "." || name == ".." || len(name) > 15 || !regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`).MatchString(name)) {
		return fmt.Errorf("invalid interface name %q", name)
	}
	return nil
}

func validateTemplatePolicies(b spec.Bundle) error {
	if err := b.Validate(); err != nil {
		return err
	}
	for _, v := range []int{b.Defaults.MgmtMTU, b.Defaults.RDMAMTU} {
		if v < 576 || v > 65535 {
			return fmt.Errorf("MTU must be 576..65535")
		}
	}
	if b.Defaults.RoutePriority < 1 {
		return fmt.Errorf("route_priority must be positive")
	}
	if b.Defaults.BondMode != "802.3ad" && b.Defaults.BondMode != "active-backup" {
		return fmt.Errorf("bond_mode must be 802.3ad or active-backup")
	}
	if b.Defaults.BondLACPRate != "" && b.Defaults.BondLACPRate != "slow" && b.Defaults.BondLACPRate != "fast" {
		return fmt.Errorf("bond_lacp_rate must be slow or fast")
	}
	if b.Defaults.BondMIIMonitorInterval < 0 {
		return fmt.Errorf("bond_mii_monitor_interval must not be negative")
	}
	switch b.PostPowerAction.Action {
	case "none", "soft", "reboot", "shutdown", "off", "power_off", "poweroff", "cycle", "power_cycle", "reset", "on", "power_on", "status":
	default:
		return fmt.Errorf("invalid post_power_action %q", b.PostPowerAction.Action)
	}
	return nil
}
