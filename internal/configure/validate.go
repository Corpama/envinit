// Package configure validates project planning without reading the local
// operating system, interfaces, or kernel. The target profile is authoritative.
package configure

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"envinit/internal/inventory"
	"envinit/internal/spec"
)

type Summary struct {
	MachineCount int
	RDMASlots    int
	Warnings     []string
	Machines     []spec.MachineConfig
}

// DecodeBundle checks schema compatibility without consulting the local host or
// filling defaults. The wizard uses the same decoder before asking for inputs.
func DecodeBundle(bundleJSON []byte) (spec.Bundle, error) {
	var b spec.Bundle
	d := json.NewDecoder(bytes.NewReader(bundleJSON))
	d.DisallowUnknownFields()
	if err := d.Decode(&b); err != nil {
		return b, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return b, fmt.Errorf("bundle must contain exactly one JSON object")
	}
	if bytes.Equal(bytes.TrimSpace(bundleJSON), []byte("null")) {
		return b, fmt.Errorf("bundle must be an object")
	}
	return b, nil
}

func ValidateProject(bundleJSON, inventoryCSV []byte) (Summary, error) {
	var summary Summary
	b, err := DecodeBundle(bundleJSON)
	if err != nil {
		return summary, err
	}
	summary.Warnings = append(summary.Warnings, b.Warnings...)
	if b.Platform.OSFamily == "" || b.Platform.OSFamily == "auto" {
		return summary, fmt.Errorf("target platform.os_family must be explicit")
	}
	defaults := b.Defaults
	b.ApplyDefaults()
	if err := b.Validate(); err != nil {
		return summary, err
	}
	records, err := inventory.ParseDelimitedBytes(inventoryCSV)
	if err != nil {
		return summary, err
	}
	hosts, ips, macs := map[string]bool{}, map[string]string{}, map[string]string{}
	addIP := func(ip, label string) error {
		parsed := net.ParseIP(ip)
		if parsed == nil || parsed.To4() == nil || !parsed.IsGlobalUnicast() || parsed.IsLoopback() {
			return fmt.Errorf("%s: invalid unicast IPv4 %q", label, ip)
		}
		ip = parsed.String()
		if old := ips[ip]; old != "" {
			return fmt.Errorf("duplicate IP %s: %s and %s", ip, old, label)
		}
		ips[ip] = label
		return nil
	}
	for _, r := range records {
		id := strings.TrimSpace(r.Hostname)
		if id == "" {
			id = strings.TrimSpace(r.HostID)
		}
		if id == "" || hosts[id] {
			return summary, fmt.Errorf("hostname must be present and unique: %q", id)
		}
		hosts[id] = true
		if b.ConfigureManagementNetwork() {
			if r.MgmtMode != "single" && r.MgmtMode != "bond" && r.MgmtMode != "auto" && r.MgmtMode != "" {
				return summary, fmt.Errorf("%s: specify mgmt_mode single, bond, or auto", id)
			}
			if r.MgmtMode == "" && r.LegacySchema {
				return summary, fmt.Errorf("%s: mgmt_mode is required when management network configuration is enabled", id)
			}
			prefix := r.MgmtPrefix
			legacy := r.LegacySchema
			if prefix == "" && legacy && defaults.MgmtPrefix > 0 {
				prefix = strconv.Itoa(defaults.MgmtPrefix)
			}
			gw := r.MgmtGateway
			if gw == "" && legacy {
				gw = defaults.MgmtGateway
			}
			if err := validateAddress(r.MgmtIP, prefix, gw, id+" management"); err != nil {
				return summary, err
			}
		}
		if r.MgmtIP != "" {
			if err := addIP(r.MgmtIP, id+" management"); err != nil {
				return summary, err
			}
		}
		for i, port := range r.RDMA {
			if !b.RDMAConfigureIPRoute() {
				break
			}
			label := fmt.Sprintf("%s rdma%d", id, i+1)
			prefix := port.Prefix
			legacy := r.LegacySchema
			if prefix == "" && legacy && defaults.RDMAPrefix > 0 {
				prefix = strconv.Itoa(defaults.RDMAPrefix)
			}
			gw := port.Gateway
			if gw == "" && legacy && i < len(defaults.RDMAInterfaces) {
				gw = defaults.RDMAInterfaces[i].Gateway
			}
			if err := validateAddress(port.IP, prefix, gw, label); err != nil {
				return summary, err
			}
			if err := addIP(port.IP, label); err != nil {
				return summary, err
			}
		}
		m, err := spec.ResolveMachine(b, r, nil)
		if err != nil {
			return summary, fmt.Errorf("%s: %w", id, err)
		}
		if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*$`).MatchString(m.Hostname) || len(m.Hostname) > 253 {
			return summary, fmt.Errorf("%s: invalid target hostname", id)
		}
		names := append([]string{}, m.MgmtIfaces...)
		if m.MgmtMode == "bond" {
			names = append(names, m.MgmtBondName)
		}
		for _, p := range m.RDMA {
			names = append(names, p.Name)
		}
		seenNames := map[string]bool{}
		for _, name := range names {
			if name == "" {
				continue
			}
			if name == "." || name == ".." || len(name) > 15 || !regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`).MatchString(name) || seenNames[name] {
				return summary, fmt.Errorf("%s: invalid or duplicate interface name %q", id, name)
			}
			seenNames[name] = true
		}
		allMACs := append([]string{}, m.MgmtMACs...)
		for _, p := range m.RDMA {
			allMACs = append(allMACs, p.MAC)
		}
		for _, mac := range allMACs {
			if mac == "" {
				continue
			}
			mac = strings.ToLower(mac)
			if old := macs[mac]; old != "" {
				return summary, fmt.Errorf("duplicate MAC %s (%s and %s)", mac, old, id)
			}
			macs[mac] = id
		}
		summary.Machines = append(summary.Machines, m)
		summary.RDMASlots += len(m.RDMA)
	}
	summary.MachineCount = len(records)
	summary.Warnings = append(summary.Warnings, "物理网卡与规划的对应关系尚待目标服务器核对；下载机不会探测或配置网卡。", "post 内置调优包含 CNP DSCP=48、RoCE priority=5；具体生效情况须在目标机验证。")
	for stage, material := range map[string]string{"ofed": b.Artifacts.OFEDArchive, "xre": b.Artifacts.XREInstaller, "xdr": b.Artifacts.XDRArchive, "firmware": b.Artifacts.FirmwareArchive} {
		if material != "" && b.StageChecks[stage] == "" {
			summary.Warnings = append(summary.Warnings, stage+" 未提供 stage_checks 完成检查；执行状态未验证时，后续重跑会要求处理。")
		}
	}
	sort.Strings(summary.Warnings)
	return summary, nil
}

func validateAddress(ip, prefix, gw, label string) error {
	if ip == "" || prefix == "" || gw == "" {
		return fmt.Errorf("%s: IP, prefix and gateway must be explicitly planned", label)
	}
	p, err := strconv.Atoi(prefix)
	if err != nil || p < 1 || p > 31 {
		return fmt.Errorf("%s: use IPv4 prefix 1..31; /32 requires a separate routing plan", label)
	}
	a, g := net.ParseIP(ip).To4(), net.ParseIP(gw).To4()
	if a == nil || g == nil || !net.IP(g).IsGlobalUnicast() || net.IP(g).IsLoopback() {
		return fmt.Errorf("%s: invalid IPv4 address or gateway", label)
	}
	n := &net.IPNet{IP: a.Mask(net.CIDRMask(p, 32)), Mask: net.CIDRMask(p, 32)}
	if !n.Contains(g) || a.Equal(g) {
		return fmt.Errorf("%s: gateway must be a distinct address in %s", label, n)
	}
	for _, v := range []net.IP{a, g} {
		if p == 31 {
			break
		}
		network := true
		broadcast := true
		for i := 0; i < 4; i++ {
			if v[i]&^n.Mask[i] != 0 {
				network = false
			}
			if v[i]|n.Mask[i] != 255 {
				broadcast = false
			}
		}
		if network || broadcast {
			return fmt.Errorf("%s: network/broadcast address cannot be used", label)
		}
	}
	return nil
}
