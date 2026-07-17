package runner

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"envinit/internal/spec"
)

func renderMgmtNetplan(machine spec.MachineConfig) string {
	var b strings.Builder
	b.WriteString("network:\n")
	b.WriteString("  version: 2\n")
	b.WriteString("  renderer: networkd\n")
	b.WriteString("  ethernets:\n")
	for _, iface := range machine.MgmtIfaces {
		if len(machine.MgmtIfaces) == 1 {
			fmt.Fprintf(&b, "    %s:\n", iface)
			fmt.Fprintf(&b, "      addresses:\n        - %s/%d\n", machine.MgmtIP, machine.MgmtPrefix)
			fmt.Fprintf(&b, "      mtu: %d\n", machine.MgmtMTU)
			writeNameservers(&b, machine.MgmtDNS, "      ")
			b.WriteString("      routes:\n")
			fmt.Fprintf(&b, "        - to: default\n          via: %s\n", machine.MgmtGateway)
		} else {
			fmt.Fprintf(&b, "    %s: {}\n", iface)
		}
	}
	if len(machine.MgmtIfaces) > 1 {
		fmt.Fprintf(&b, "  bonds:\n    %s:\n", machine.MgmtBondName)
		fmt.Fprintf(&b, "      interfaces: [%s]\n", strings.Join(machine.MgmtIfaces, ", "))
		fmt.Fprintf(&b, "      addresses:\n        - %s/%d\n", machine.MgmtIP, machine.MgmtPrefix)
		fmt.Fprintf(&b, "      mtu: %d\n", machine.MgmtMTU)
		writeNameservers(&b, machine.MgmtDNS, "      ")
		b.WriteString("      parameters:\n")
		fmt.Fprintf(&b, "        mode: %s\n", machine.BondMode)
		if strings.EqualFold(machine.BondMode, "802.3ad") {
			fmt.Fprintf(&b, "        lacp-rate: %s\n", machine.BondLACPRate)
			fmt.Fprintf(&b, "        transmit-hash-policy: %s\n", machine.BondXmitHash)
		}
		if strings.EqualFold(machine.BondMode, "active-backup") && machine.BondMII > 0 {
			fmt.Fprintf(&b, "        mii-monitor-interval: %d\n", machine.BondMII)
		}
		if strings.EqualFold(machine.BondMode, "active-backup") && strings.TrimSpace(machine.BondPrimary) != "" {
			fmt.Fprintf(&b, "        primary: %s\n", machine.BondPrimary)
		}
		b.WriteString("      routes:\n")
		fmt.Fprintf(&b, "        - to: default\n          via: %s\n", machine.MgmtGateway)
	}
	return b.String()
}

func writeNameservers(b *strings.Builder, dns []string, indent string) {
	fmt.Fprintf(b, "%snameservers:\n", indent)
	if len(dns) == 0 {
		fmt.Fprintf(b, "%s  addresses: []\n", indent)
		return
	}
	fmt.Fprintf(b, "%s  addresses:\n", indent)
	for _, item := range dns {
		fmt.Fprintf(b, "%s    - %s\n", indent, item)
	}
}

func renderRDMANetplan(item spec.RDMAConfig, mtu int) string {
	var b strings.Builder
	b.WriteString("network:\n")
	b.WriteString("  version: 2\n")
	b.WriteString("  renderer: networkd\n")
	b.WriteString("  ethernets:\n")
	fmt.Fprintf(&b, "    %s:\n", item.Name)
	fmt.Fprintf(&b, "      addresses:\n        - %s/%d\n", item.IP, item.Prefix)
	b.WriteString("      ignore-carrier: true\n")
	fmt.Fprintf(&b, "      mtu: %d\n", mtu)
	return b.String()
}

func ifcfgPath(iface string) string {
	return filepath.Join(networkScriptsDir, "ifcfg-"+iface)
}

func ifcfgRoutePath(iface string) string {
	return filepath.Join(networkScriptsDir, "route-"+iface)
}

func ifcfgRulePath(iface string) string {
	return filepath.Join(networkScriptsDir, "rule-"+iface)
}

func (a *App) writeIfcfgNetworkFiles() error {
	nmControlled := a.usesNetworkManager()
	if a.configureManagementNetwork() {
		if len(a.Machine.MgmtIfaces) == 1 {
			iface := a.Machine.MgmtIfaces[0]
			if err := a.writeManagedFile(ifcfgPath(iface), renderMgmtIfcfg(a.Machine, iface, nmControlled), 0o600); err != nil {
				return err
			}
		} else {
			if err := a.writeManagedFile(ifcfgPath(a.Machine.MgmtBondName), renderBondIfcfg(a.Machine, nmControlled), 0o600); err != nil {
				return err
			}
			for _, iface := range a.Machine.MgmtIfaces {
				if err := a.writeManagedFile(ifcfgPath(iface), renderBondSlaveIfcfg(a.Machine, iface, nmControlled), 0o600); err != nil {
					return err
				}
			}
		}
	}
	if a.Bundle.RDMAConfigureIPRoute() {
		for _, item := range a.Machine.RDMA {
			if err := a.writeManagedFile(ifcfgPath(item.Name), renderRDMAIfcfg(item, a.Machine.RDMAMTU, nmControlled), 0o600); err != nil {
				return err
			}
			if err := a.writeManagedFile(ifcfgRoutePath(item.Name), renderIfcfgRoute(item, a.Machine.RouteCIDR), 0o600); err != nil {
				return err
			}
			if err := a.writeManagedFile(ifcfgRulePath(item.Name), renderIfcfgRule(item, a.Machine.RoutePriority), 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

func renderMgmtIfcfg(machine spec.MachineConfig, iface string, nmControlled bool) string {
	var b strings.Builder
	writeIfcfgCommon(&b, iface, "Ethernet", machine.MgmtMTU, nmControlled)
	b.WriteString("BOOTPROTO=static\n")
	fmt.Fprintf(&b, "IPADDR=%s\n", machine.MgmtIP)
	fmt.Fprintf(&b, "PREFIX=%d\n", machine.MgmtPrefix)
	fmt.Fprintf(&b, "GATEWAY=%s\n", machine.MgmtGateway)
	writeIfcfgDNS(&b, machine.MgmtDNS)
	return b.String()
}

func renderBondIfcfg(machine spec.MachineConfig, nmControlled bool) string {
	var b strings.Builder
	writeIfcfgCommon(&b, machine.MgmtBondName, "Bond", machine.MgmtMTU, nmControlled)
	b.WriteString("BOOTPROTO=static\n")
	fmt.Fprintf(&b, "IPADDR=%s\n", machine.MgmtIP)
	fmt.Fprintf(&b, "PREFIX=%d\n", machine.MgmtPrefix)
	fmt.Fprintf(&b, "GATEWAY=%s\n", machine.MgmtGateway)
	fmt.Fprintf(&b, "BONDING_OPTS=%q\n", renderBondingOpts(machine))
	writeIfcfgDNS(&b, machine.MgmtDNS)
	return b.String()
}

func renderBondSlaveIfcfg(machine spec.MachineConfig, iface string, nmControlled bool) string {
	var b strings.Builder
	writeIfcfgCommon(&b, iface, "Ethernet", 0, nmControlled)
	b.WriteString("BOOTPROTO=none\n")
	fmt.Fprintf(&b, "MASTER=%s\n", machine.MgmtBondName)
	b.WriteString("SLAVE=yes\n")
	return b.String()
}

func renderRDMAIfcfg(item spec.RDMAConfig, mtu int, nmControlled bool) string {
	var b strings.Builder
	writeIfcfgCommon(&b, item.Name, "Ethernet", mtu, nmControlled)
	b.WriteString("BOOTPROTO=static\n")
	fmt.Fprintf(&b, "IPADDR=%s\n", item.IP)
	fmt.Fprintf(&b, "PREFIX=%d\n", item.Prefix)
	return b.String()
}

func writeIfcfgCommon(b *strings.Builder, name string, deviceType string, mtu int, nmControlled bool) {
	fmt.Fprintf(b, "DEVICE=%s\n", name)
	fmt.Fprintf(b, "NAME=%s\n", name)
	fmt.Fprintf(b, "TYPE=%s\n", deviceType)
	b.WriteString("ONBOOT=yes\n")
	if nmControlled {
		b.WriteString("NM_CONTROLLED=yes\n")
	} else {
		b.WriteString("NM_CONTROLLED=no\n")
	}
	if mtu > 0 {
		fmt.Fprintf(b, "MTU=%d\n", mtu)
	}
}

func writeIfcfgDNS(b *strings.Builder, dns []string) {
	for idx, item := range dns {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		fmt.Fprintf(b, "DNS%d=%s\n", idx+1, item)
	}
}

func renderBondingOpts(machine spec.MachineConfig) string {
	miimon := machine.BondMII
	if miimon == 0 {
		miimon = 100
	}
	parts := []string{
		"mode=" + machine.BondMode,
		fmt.Sprintf("miimon=%d", miimon),
	}
	if strings.EqualFold(machine.BondMode, "802.3ad") {
		if strings.TrimSpace(machine.BondLACPRate) != "" {
			parts = append(parts, "lacp_rate="+strings.TrimSpace(machine.BondLACPRate))
		}
		if strings.TrimSpace(machine.BondXmitHash) != "" {
			parts = append(parts, "xmit_hash_policy="+strings.TrimSpace(machine.BondXmitHash))
		}
	}
	if strings.EqualFold(machine.BondMode, "active-backup") && strings.TrimSpace(machine.BondPrimary) != "" {
		parts = append(parts, "primary="+strings.TrimSpace(machine.BondPrimary))
	}
	return strings.Join(parts, " ")
}

func renderIfcfgRoute(item spec.RDMAConfig, routeCIDR string) string {
	routeCIDR = effectiveRDMARouteCIDR(item, routeCIDR)
	var b strings.Builder
	fmt.Fprintf(&b, "default via %s dev %s table %d\n", item.Gateway, item.Name, item.Table)
	fmt.Fprintf(&b, "%s dev %s scope link table %d src %s proto static\n", routeCIDR, item.Name, item.Table, item.IP)
	return b.String()
}

func renderIfcfgRule(item spec.RDMAConfig, priority int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "from all oif %s table %d priority %d\n", item.Name, item.Table, priority)
	fmt.Fprintf(&b, "from %s table %d priority %d\n", item.IP, item.Table, priority)
	return b.String()
}

func (a *App) networkManagerApplyInterfaces() []string {
	out := []string{}
	if a.configureManagementNetwork() {
		if len(a.Machine.MgmtIfaces) == 1 {
			out = append(out, a.Machine.MgmtIfaces[0])
		} else {
			out = append(out, a.Machine.MgmtBondName)
		}
	}
	if a.Bundle.RDMAConfigureIPRoute() {
		for _, item := range a.Machine.RDMA {
			out = append(out, item.Name)
		}
	}
	return out
}

func (a *App) legacyNetworkApplyInterfaces() []string {
	out := []string{}
	if a.configureManagementNetwork() {
		if len(a.Machine.MgmtIfaces) == 1 {
			out = append(out, a.Machine.MgmtIfaces[0])
		} else {
			out = append(out, a.Machine.MgmtBondName)
		}
	}
	if a.Bundle.RDMAConfigureIPRoute() {
		for _, item := range a.Machine.RDMA {
			out = append(out, item.Name)
		}
	}
	return out
}

func renderNetworkManagerDispatcher(items []spec.RDMAConfig) string {
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString("set -euo pipefail\n\n")
	b.WriteString("iface=${1:-}\n")
	b.WriteString("action=${2:-}\n\n")
	b.WriteString("case \"$action\" in\n")
	b.WriteString("  up|dhcp4-change|dhcp6-change|connectivity-change)\n")
	b.WriteString("    ;;\n")
	b.WriteString("  *)\n")
	b.WriteString("    exit 0\n")
	b.WriteString("    ;;\n")
	b.WriteString("esac\n\n")
	for _, item := range items {
		script := filepath.Join(localSbinDir, "kunlun-"+fmt.Sprintf("config_rt_%s.sh", item.Name))
		fmt.Fprintf(&b, "if [ \"$iface\" = %q ] && [ -x %q ]; then\n", item.Name, script)
		fmt.Fprintf(&b, "  %q\n", script)
		b.WriteString("fi\n")
	}
	return b.String()
}

func renderRouteScript(item spec.RDMAConfig, routeCIDR string, priority int) string {
	routeCIDR = effectiveRDMARouteCIDR(item, routeCIDR)
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString("set -euo pipefail\n\n")
	fmt.Fprintf(&b, "IP=%q\n", item.IP)
	fmt.Fprintf(&b, "DEV=%q\n", item.Name)
	fmt.Fprintf(&b, "TABLE=%q\n", strconv.Itoa(item.Table))
	fmt.Fprintf(&b, "GW=%q\n", item.Gateway)
	fmt.Fprintf(&b, "ROUTE_CIDR=%q\n", routeCIDR)
	fmt.Fprintf(&b, "PRIORITY=%q\n", strconv.Itoa(priority))
	fmt.Fprintf(&b, "BROADCAST=%q\n\n", ipv4Broadcast(item.IP, item.Prefix))
	b.WriteString("if ! ip addr show \"$DEV\" | grep -q \"inet \"; then\n")
	b.WriteString("    echo \"device $DEV no ip address, so add ip address for it\"\n")
	b.WriteString("    ip addr add \"${IP}/" + strconv.Itoa(item.Prefix) + "\" brd \"$BROADCAST\" dev \"$DEV\"\n")
	b.WriteString("fi\n\n")
	b.WriteString("while read -r line; do\n")
	b.WriteString("    [ -n \"$line\" ] || continue\n")
	b.WriteString("    ip rule del \"${line#*: }\" 2>/dev/null || true\n")
	b.WriteString("done < <(ip rule show | grep \"$DEV\" || true)\n\n")
	b.WriteString("ip rule del from all oif \"$DEV\" table \"$TABLE\" priority \"$PRIORITY\" 2>/dev/null || true\n")
	b.WriteString("ip rule del from \"$IP\" table \"$TABLE\" priority \"$PRIORITY\" 2>/dev/null || true\n\n")
	b.WriteString("ip route replace default via \"$GW\" dev \"$DEV\" table \"$TABLE\"\n")
	b.WriteString("ip route replace \"$ROUTE_CIDR\" dev \"$DEV\" scope link table \"$TABLE\" src \"$IP\" proto static\n")
	b.WriteString("ip rule add from all oif \"$DEV\" table \"$TABLE\" priority \"$PRIORITY\"\n")
	b.WriteString("ip rule add from \"$IP\" table \"$TABLE\" priority \"$PRIORITY\"\n")
	return b.String()
}

func effectiveRDMARouteCIDR(item spec.RDMAConfig, fallback string) string {
	itemCIDR := strings.TrimSpace(item.RouteCIDR)
	if itemCIDR != "" {
		if strings.EqualFold(itemCIDR, "auto") {
			return rdmaConnectedCIDR(item)
		}
		return itemCIDR
	}
	fallback = strings.TrimSpace(fallback)
	if fallback == "" || strings.EqualFold(fallback, "auto") {
		return rdmaConnectedCIDR(item)
	}
	return fallback
}

func rdmaConnectedCIDR(item spec.RDMAConfig) string {
	ip := net.ParseIP(strings.TrimSpace(item.IP)).To4()
	if ip == nil || item.Prefix < 0 || item.Prefix > 32 {
		return strings.TrimSpace(item.RouteCIDR)
	}
	mask := net.CIDRMask(item.Prefix, 32)
	network := ip.Mask(mask)
	return (&net.IPNet{IP: network, Mask: mask}).String()
}

func renderPostBootScript(machine spec.MachineConfig, customBlock string) string {
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString("set -euo pipefail\n\n")
	b.WriteString("# This file is managed by envinit. The custom section is preserved on update.\n")
	b.WriteString("CNP_DSCP=\"48\"\n")
	b.WriteString("RDMA_PRIO=\"5\"\n")
	b.WriteString("RDMA_INTERFACES=(\n")
	for _, item := range machine.RDMA {
		fmt.Fprintf(&b, "  %q\n", item.Name)
	}
	b.WriteString(")\n\n")
	b.WriteString("echo \"disable PCIe ACSCtl for devices that expose ACS capability\"\n")
	b.WriteString("if command -v lspci >/dev/null 2>&1 && command -v setpci >/dev/null 2>&1; then\n")
	b.WriteString("    while read -r pdev; do\n")
	b.WriteString("        [ -n \"$pdev\" ] || continue\n")
	b.WriteString("        if ! setpci -s \"$pdev\" ECAP_ACS+06.w=0000; then\n")
	b.WriteString("            echo \"skip ACSCtl disable on $pdev: setpci failed\"\n")
	b.WriteString("        fi\n")
	b.WriteString("    done < <(lspci -vvv | grep -E \"^[a-f]|^[0-9]|ACSCtl\" | grep ACSCtl -B1 | grep -E \"^[a-f]|^[0-9]\" | awk '{print $1}')\n")
	b.WriteString("else\n")
	b.WriteString("    echo \"skip ACSCtl disable: lspci or setpci not found\"\n")
	b.WriteString("fi\n\n")
	b.WriteString("for iface in \"${RDMA_INTERFACES[@]}\"; do\n")
	b.WriteString("    if ! ip link show \"$iface\" >/dev/null 2>&1; then\n")
	b.WriteString("        echo \"skip missing interface: $iface\"\n")
	b.WriteString("        continue\n")
	b.WriteString("    fi\n\n")
	b.WriteString("    echo \"set ring buffer depth on $iface\"\n")
	b.WriteString("    if ! ethtool -G \"$iface\" rx 8192 tx 8192; then\n")
	b.WriteString("        echo \"skip ring buffer tuning on $iface: ethtool -G failed\"\n")
	b.WriteString("    fi\n\n")
	b.WriteString("    if ! bus_info=$(ethtool -i \"$iface\" 2>/dev/null | awk -F': ' '$1 == \"bus-info\" {print $2; exit}'); then\n")
	b.WriteString("        echo \"skip RoCE AR on $iface: ethtool -i failed\"\n")
	b.WriteString("        continue\n")
	b.WriteString("    fi\n")
	b.WriteString("    if [ -z \"$bus_info\" ]; then\n")
	b.WriteString("        echo \"skip RoCE AR on $iface: missing bus-info\"\n")
	b.WriteString("        continue\n")
	b.WriteString("    fi\n")
	b.WriteString("    if command -v setpci >/dev/null 2>&1; then\n")
	b.WriteString("        current_mrr=$(setpci -s \"$bus_info\" CAP_EXP+8.w 2>/dev/null || true)\n")
	b.WriteString("        if [ -n \"$current_mrr\" ]; then\n")
	b.WriteString("            desired_mrr=$(printf \"%04x\" $(( (0x$current_mrr & 0x8fff) | 0x5000 )))\n")
	b.WriteString("            echo \"set PCIe MaxReadReq 4096 on $iface ($bus_info), current=$current_mrr desired=$desired_mrr\"\n")
	b.WriteString("            if ! setpci -s \"$bus_info\" CAP_EXP+8.w=\"$desired_mrr\"; then\n")
	b.WriteString("                echo \"skip MaxReadReq tuning on $iface: setpci failed\"\n")
	b.WriteString("            fi\n")
	b.WriteString("        else\n")
	b.WriteString("            echo \"skip MaxReadReq tuning on $iface: CAP_EXP+8.w unavailable\"\n")
	b.WriteString("        fi\n")
	b.WriteString("    else\n")
	b.WriteString("        echo \"skip MaxReadReq tuning on $iface: setpci not found\"\n")
	b.WriteString("    fi\n\n")
	b.WriteString("    echo \"enable RoCE adaptive routing on $iface ($bus_info)\"\n")
	b.WriteString("    if ! mlxreg -d \"$bus_info\" --reg_name ROCE_ACCL --set adaptive_routing_forced_en=0x1 --yes; then\n")
	b.WriteString("        echo \"skip RoCE AR on $iface: mlxreg failed\"\n")
	b.WriteString("    fi\n")
	b.WriteString("    if [ -w \"/sys/class/net/$iface/ecn/roce_np/enable/$RDMA_PRIO\" ]; then\n")
	b.WriteString("        echo 1 > \"/sys/class/net/$iface/ecn/roce_np/enable/$RDMA_PRIO\"\n")
	b.WriteString("    else\n")
	b.WriteString("        echo \"skip RoCE NP enable on $iface: sysfs path unavailable\"\n")
	b.WriteString("    fi\n")
	b.WriteString("    if [ -w \"/sys/class/net/$iface/ecn/roce_rp/enable/$RDMA_PRIO\" ]; then\n")
	b.WriteString("        echo 1 > \"/sys/class/net/$iface/ecn/roce_rp/enable/$RDMA_PRIO\"\n")
	b.WriteString("    else\n")
	b.WriteString("        echo \"skip RoCE RP enable on $iface: sysfs path unavailable\"\n")
	b.WriteString("    fi\n")
	b.WriteString("    if [ -w \"/sys/class/net/$iface/ecn/roce_np/cnp_dscp\" ]; then\n")
	b.WriteString("        echo \"$CNP_DSCP\" > \"/sys/class/net/$iface/ecn/roce_np/cnp_dscp\"\n")
	b.WriteString("    else\n")
	b.WriteString("        echo \"skip CNP DSCP on $iface: sysfs path unavailable\"\n")
	b.WriteString("    fi\n")
	b.WriteString("done\n\n")
	b.WriteString(postBootCustomBegin + "\n")
	b.WriteString(customBlock)
	if !strings.HasSuffix(customBlock, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(postBootCustomEnd + "\n")
	return b.String()
}

func renderPostBootService() string {
	return strings.Join([]string{
		"[Unit]",
		"Description=Kunlun post-boot RDMA tuning",
		"After=network-online.target",
		"Wants=network-online.target",
		"",
		"[Service]",
		"Type=oneshot",
		"ExecStart=" + postBootScript,
		"RemainAfterExit=yes",
		"",
		"[Install]",
		"WantedBy=multi-user.target",
		"",
	}, "\n")
}

func defaultPostBootCustomBlock() string {
	return "# Add custom boot-time actions below. This section is preserved by envinit.\n"
}

func extractPostBootCustomBlock(content string) string {
	begin := strings.Index(content, postBootCustomBegin)
	end := strings.Index(content, postBootCustomEnd)
	if begin == -1 || end == -1 || end < begin {
		return defaultPostBootCustomBlock()
	}
	begin += len(postBootCustomBegin)
	custom := content[begin:end]
	custom = strings.TrimPrefix(custom, "\r\n")
	custom = strings.TrimPrefix(custom, "\n")
	if strings.TrimSpace(custom) == "" {
		return defaultPostBootCustomBlock()
	}
	return custom
}

func desiredSysctlLines(machine spec.MachineConfig) []string {
	lines := []string{
		"net.core.rmem_max = 212992000",
		"net.core.rmem_default = 212992000",
		"net.core.wmem_max = 212992000",
		"net.core.wmem_default = 212992000",
		"net.ipv4.tcp_rmem = 4096000 131072000 629145600",
		"net.ipv4.tcp_wmem = 4096000 16384000 419430400",
	}
	for _, item := range machine.RDMA {
		lines = append(lines,
			fmt.Sprintf("net.ipv4.conf.%s.arp_ignore=2", item.Name),
			fmt.Sprintf("net.ipv4.conf.%s.arp_announce=1", item.Name),
			fmt.Sprintf("net.ipv4.conf.%s.rp_filter=2", item.Name),
			fmt.Sprintf("net.ipv6.conf.%s.disable_ipv6=0", item.Name),
		)
	}
	return lines
}

func (a *App) ensureSysctlSettings() error {
	target := a.targetPath(sysctlFile)
	if !a.DryRun {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(target), err)
		}
	}
	existing, err := os.ReadFile(target)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read %s: %w", sysctlFile, err)
	}

	lines := strings.Split(strings.ReplaceAll(string(existing), "\r\n", "\n"), "\n")
	lastValues := map[string]string{}
	for _, line := range lines {
		key, value, ok := parseSysctlLine(line)
		if !ok {
			continue
		}
		lastValues[key] = value
	}

	changed := false
	for _, line := range desiredSysctlLines(a.Machine) {
		key, value, ok := parseSysctlLine(line)
		if !ok {
			continue
		}
		if lastValues[key] == value {
			continue
		}
		lines = append(lines, line)
		lastValues[key] = value
		changed = true
	}

	if !changed {
		a.logf("unchanged %s", sysctlFile)
		return nil
	}

	content := strings.Join(lines, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	a.logf("update %s", sysctlFile)
	if a.DryRun {
		return nil
	}
	return os.WriteFile(target, []byte(content), 0o644)
}

func parseSysctlLine(line string) (string, string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	parts := strings.SplitN(trimmed, "=", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	key := strings.TrimSpace(parts[0])
	value := strings.TrimSpace(parts[1])
	if key == "" {
		return "", "", false
	}
	return key, value, true
}

func parseMlxConfigValue(output string, key string) string {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != key {
			continue
		}
		return fields[len(fields)-1]
	}
	return ""
}

func findDirWithFiles(root string, files ...string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		for _, name := range files {
			if _, err := os.Stat(filepath.Join(path, name)); err != nil {
				return nil
			}
		}
		found = path
		return io.EOF
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("did not find directory containing %s under %s", strings.Join(files, ", "), root)
	}
	return found, nil
}

func ensureGrubCmdline(path string, required []string) (bool, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, "", fmt.Errorf("read grub config: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	changed := false
	found := false
	for idx, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "GRUB_CMDLINE_LINUX=") {
			continue
		}
		found = true
		value := strings.TrimPrefix(trimmed, "GRUB_CMDLINE_LINUX=")
		unquoted, err := strconv.Unquote(value)
		if err != nil {
			unquoted = strings.Trim(value, `"`)
		}
		tokens := strings.Fields(unquoted)
		filtered := make([]string, 0, len(tokens))
		for _, token := range tokens {
			if token == "quiet" {
				changed = true
				continue
			}
			filtered = append(filtered, token)
		}
		for _, token := range required {
			if !slices.Contains(filtered, token) {
				filtered = append(filtered, token)
				changed = true
			}
		}
		lines[idx] = fmt.Sprintf("GRUB_CMDLINE_LINUX=%q", strings.Join(filtered, " "))
	}
	if !found {
		changed = true
		lines = append(lines, fmt.Sprintf("GRUB_CMDLINE_LINUX=%q", strings.Join(required, " ")))
	}
	if !changed {
		return false, string(data), nil
	}
	return true, strings.Join(lines, "\n"), nil
}

func ipv4Broadcast(ip string, prefix int) string {
	parsed := net.ParseIP(ip).To4()
	if parsed == nil {
		return ""
	}
	mask := net.CIDRMask(prefix, 32)
	out := make(net.IP, len(parsed))
	for idx := range parsed {
		out[idx] = parsed[idx] | ^mask[idx]
	}
	return out.String()
}
