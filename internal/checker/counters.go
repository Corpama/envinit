package checker

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"envinit/internal/spec"
)

func collectNICCounterSnapshots(opts Options, targets []Target, phase string) (map[string]nicCounterSnapshot, []string) {
	snapshots := map[string]nicCounterSnapshot{}
	var failures []string
	for _, target := range targets {
		interfaces := targetCounterInterfaces(opts.Bundle, target)
		if len(interfaces) == 0 {
			fmt.Fprintf(opts.Output, "WARN nic-counters %s %s: no RDMA interfaces configured; continuing\n", phase, target.Name)
			continue
		}
		command := nicCounterCommand(interfaces)
		if opts.DryRun {
			fmt.Fprintf(opts.Output, "dry-run nic-counters %s %s: %s\n", phase, target.Name, command)
			continue
		}
		output, err := runCommand(opts.Bundle.Check, target, command)
		if err != nil {
			message := fmt.Sprintf("nic-counters %s %s: %v", phase, target.Name, err)
			failures = append(failures, message)
			fmt.Fprintf(opts.Output, "FAIL %s\n", message)
			continue
		}
		snapshots[target.Name] = nicCounterSnapshot{Interfaces: parseNICCounterOutput(output)}
	}
	return snapshots, failures
}

func targetCounterInterfaces(bundle spec.Bundle, target Target) []string {
	maxItems := maxInt(len(target.RDMA), len(bundle.Defaults.RDMAInterfaces))
	interfaces := make([]string, 0, maxItems)
	seen := map[string]bool{}
	for idx := 0; idx < maxItems; idx++ {
		name := ""
		if idx < len(target.RDMA) {
			name = strings.TrimSpace(target.RDMA[idx].Name)
		}
		if name == "" && idx < len(bundle.Defaults.RDMAInterfaces) {
			name = strings.TrimSpace(bundle.Defaults.RDMAInterfaces[idx].Name)
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		interfaces = append(interfaces, name)
	}
	return interfaces
}

func targetRDMAInterfaceName(bundle spec.Bundle, target Target, index int) string {
	if index >= 0 && index < len(target.RDMA) {
		if name := strings.TrimSpace(target.RDMA[index].Name); name != "" {
			return name
		}
	}
	if index >= 0 && index < len(bundle.Defaults.RDMAInterfaces) {
		return strings.TrimSpace(bundle.Defaults.RDMAInterfaces[index].Name)
	}
	return ""
}

func resolveBandwidthGroups(opts Options, targets []Target) (resolvedRDMAGroups, error) {
	out := resolvedRDMAGroups{}
	for _, target := range targets {
		groupCount := maxInt(len(opts.Bundle.Check.Bandwidth.RDMAGroups), maxInt(len(target.RDMA), len(opts.Bundle.Defaults.RDMAInterfaces)))
		if opts.DryRun && !opts.RunXCCL && len(opts.Bundle.Check.Bandwidth.RDMAGroups) > 0 {
			groupCount = len(opts.Bundle.Check.Bandwidth.RDMAGroups)
		}
		if groupCount == 0 {
			return nil, fmt.Errorf("resolve rdma groups for %s: inventory and bundle defaults contain no RDMA interfaces", target.Name)
		}
		groups := make([]spec.CheckRDMAGroup, groupCount)
		copy(groups, opts.Bundle.Check.Bandwidth.RDMAGroups)
		for idx := range groups {
			iface := targetRDMAInterfaceName(opts.Bundle, target, idx)
			iface = strings.TrimSpace(iface)
			if iface == "" {
				if strings.TrimSpace(groups[idx].IBDevice) == "" {
					return nil, fmt.Errorf("resolve rdma group for %s rdma%d: inventory is missing rdma%d_name", target.Name, idx+1, idx+1)
				}
				continue
			}
			if opts.DryRun && !opts.RunXCCL && strings.TrimSpace(opts.Bundle.Check.Bandwidth.MmapDevice) == "" {
				if strings.TrimSpace(groups[idx].IBDevice) == "" {
					groups[idx].IBDevice = fmt.Sprintf("<resolve-ib-device:%s>", iface)
				}
				continue
			}
			if opts.DryRun && !opts.RunXCCL && strings.TrimSpace(groups[idx].IBDevice) != "" && len(groups[idx].XPUOffsets) > 0 {
				continue
			}
			device, err := resolveIBDeviceForInterface(opts, target, iface)
			if err != nil {
				return nil, err
			}
			configured := strings.TrimSpace(groups[idx].IBDevice)
			groups[idx].IBDevice = device
			if opts.DryRun {
				fmt.Fprintf(opts.Output, "dry-run discovery rdma group: %s rdma%d iface=%s ib_device=%s\n", target.Name, idx+1, iface, device)
			} else if configured != "" && configured != device {
				fmt.Fprintf(opts.Output, "WARN rdma group resolve: %s rdma%d iface=%s configured_ib_device=%s actual_ib_device=%s; using actual device\n", target.Name, idx+1, iface, configured, device)
			} else {
				fmt.Fprintf(opts.Output, "INFO rdma group resolve: %s rdma%d iface=%s ib_device=%s\n", target.Name, idx+1, iface, device)
			}
		}
		out[target.Name] = groups
	}
	return out, nil
}

func resolveIBDeviceForInterface(opts Options, target Target, iface string) (string, error) {
	command := resolveIBDeviceCommand(iface)
	output, err := runDiscoveryCommand(opts, target, command)
	if err != nil {
		return "", fmt.Errorf("resolve ib device for %s %s: %w", target.Name, iface, err)
	}
	devices := parseResolvedIBDevices(output)
	if len(devices) == 0 {
		return "", fmt.Errorf("resolve ib device for %s %s: no infiniband device found under /sys/class/net/%s/device/infiniband", target.Name, iface, iface)
	}
	if len(devices) > 1 {
		return "", fmt.Errorf("resolve ib device for %s %s: multiple infiniband devices found: %s", target.Name, iface, strings.Join(devices, ", "))
	}
	return devices[0], nil
}

func runDiscoveryCommand(opts Options, target Target, command string) (string, error) {
	if opts.CommandRunner != nil {
		return opts.CommandRunner(opts.Bundle.Check, target, command)
	}
	return runCommand(opts.Bundle.Check, target, command)
}

func resolveIBDeviceCommand(iface string) string {
	quoted := shellQuote(iface)
	return fmt.Sprintf("for d in /sys/class/net/%s/device/infiniband/*; do [ -e \"$d\" ] || continue; basename \"$d\"; done", quoted)
}

func parseResolvedIBDevices(output string) []string {
	seen := map[string]bool{}
	var devices []string
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		devices = append(devices, line)
	}
	sort.Strings(devices)
	return devices
}

func nicCounterCommand(interfaces []string) string {
	quoted := make([]string, 0, len(interfaces))
	for _, name := range interfaces {
		quoted = append(quoted, shellQuote(name))
	}
	pattern := "port_xmit_discards|port_rcv_errors|packet_seq_err|local_ack_timeout_err|out_of_sequence|port_xmit_wait|np_cnp_sent|rp_cnp_handled|rx_prio[0-9]+_buf_discard|rx_prio5_pause_duration|tx_prio5_pause_duration|roce_adp_retrans|timeout|drop|discard|crc|err"
	return fmt.Sprintf("for i in %s; do echo __envinit_iface=$i; ethtool -i \"$i\" 2>/dev/null | grep -E \"^(driver|bus-info):\" || true; ip -br link show \"$i\" 2>/dev/null || true; ethtool -S \"$i\" 2>/dev/null | grep -E %s || true; done", strings.Join(quoted, " "), shellQuote(pattern))
}

func parseNICCounterOutput(output string) map[string]map[string]int64 {
	counters := map[string]map[string]int64{}
	currentInterface := ""
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "__envinit_iface=") {
			currentInterface = strings.TrimSpace(strings.TrimPrefix(line, "__envinit_iface="))
			if currentInterface != "" && counters[currentInterface] == nil {
				counters[currentInterface] = map[string]int64{}
			}
			continue
		}
		if currentInterface == "" {
			continue
		}
		name, value, ok := parseNICCounterLine(line)
		if !ok {
			continue
		}
		counters[currentInterface][name] = value
	}
	return counters
}

func parseNICCounterLine(line string) (string, int64, bool) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", 0, false
	}
	name := strings.TrimSpace(line[:idx])
	fields := strings.Fields(strings.TrimSpace(line[idx+1:]))
	if name == "" || len(fields) == 0 {
		return "", 0, false
	}
	value, err := strconv.ParseInt(strings.Trim(fields[0], ","), 10, 64)
	if err != nil {
		return "", 0, false
	}
	return name, value, true
}

func compareNICCounterSnapshots(opts Options, targets []Target, before map[string]nicCounterSnapshot, after map[string]nicCounterSnapshot) []string {
	if opts.DryRun {
		return nil
	}
	var failures []string
	var rows []nicCounterRow
	abnormalCount := 0
	for _, target := range targets {
		targetBefore, beforeOK := before[target.Name]
		targetAfter, afterOK := after[target.Name]
		if !beforeOK || !afterOK {
			continue
		}
		interfaces := targetCounterInterfaces(opts.Bundle, target)
		for _, iface := range interfaces {
			beforeCounters := targetBefore.Interfaces[iface]
			afterCounters := targetAfter.Interfaces[iface]
			if len(beforeCounters) == 0 && len(afterCounters) == 0 {
				fmt.Fprintf(opts.Output, "WARN nic-counters %s %s: no matching ethtool counters found\n", target.Name, iface)
				continue
			}
			names := unionCounterNames(beforeCounters, afterCounters)
			for _, name := range names {
				delta := afterCounters[name] - beforeCounters[name]
				if beforeCounters[name] == 0 && afterCounters[name] == 0 {
					continue
				}
				status := "SAME"
				failure := false
				if delta < 0 {
					status = "WARN"
				} else if delta > 0 && isAbnormalNICCounter(name) {
					status = "FAIL"
					failure = true
					abnormalCount++
				} else if delta > 0 {
					status = "INFO"
				}
				rows = append(rows, nicCounterRow{
					Status:  status,
					Node:    target.Name,
					Iface:   iface,
					Counter: name,
					Before:  beforeCounters[name],
					After:   afterCounters[name],
					Delta:   delta,
					Failure: failure,
				})
			}
		}
	}
	printNICCounterTable(opts.Output, rows)
	if abnormalCount > 0 {
		failures = append(failures, fmt.Sprintf("nic-counters detected %d abnormal counter delta(s); see NIC counter delta summary", abnormalCount))
	}
	return failures
}

func unionCounterNames(before map[string]int64, after map[string]int64) []string {
	seen := map[string]bool{}
	names := make([]string, 0, len(before)+len(after))
	for name := range before {
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	for name := range after {
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func printNICCounterTable(output io.Writer, rows []nicCounterRow) {
	if len(rows) == 0 {
		fmt.Fprintln(output, "PASS nic-counters no nonzero counters or counter deltas")
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left := rows[i]
		right := rows[j]
		if left.Failure != right.Failure {
			return left.Failure
		}
		for _, pair := range [][2]string{
			{left.Node, right.Node},
			{left.Iface, right.Iface},
			{left.Counter, right.Counter},
		} {
			if pair[0] != pair[1] {
				return pair[0] < pair[1]
			}
		}
		return false
	})

	headers := []string{"STATUS", "NODE", "IFACE", "COUNTER", "BEFORE", "AFTER", "DELTA"}
	widths := make([]int, len(headers))
	for idx, header := range headers {
		widths[idx] = len(header)
	}
	tableRows := make([][]string, 0, len(rows))
	for _, row := range rows {
		cells := []string{
			row.Status,
			row.Node,
			row.Iface,
			row.Counter,
			strconv.FormatInt(row.Before, 10),
			strconv.FormatInt(row.After, 10),
			formatSignedInt(row.Delta),
		}
		for idx, cell := range cells {
			if len(cell) > widths[idx] {
				widths[idx] = len(cell)
			}
		}
		tableRows = append(tableRows, cells)
	}

	fmt.Fprintln(output, "NIC counter delta summary:")
	fmt.Fprintln(output, formatTableLine(headers, widths))
	fmt.Fprintln(output, formatTableSeparator(widths))
	for idx, cells := range tableRows {
		line := formatTableLine(cells, widths)
		if rows[idx].Failure {
			line = redText(line)
		}
		fmt.Fprintln(output, line)
	}
}

func collectRDMADeviceCounterSnapshots(opts Options, targets []Target, groupsByTarget resolvedRDMAGroups, phase string) (map[string]rdmaDeviceCounterSnapshot, []string) {
	snapshots := map[string]rdmaDeviceCounterSnapshot{}
	var failures []string
	for _, target := range targets {
		devices := rdmaCounterDevices(groupsByTarget[target.Name])
		if len(devices) == 0 {
			continue
		}
		command := rdmaDeviceCounterCommand(devices)
		if opts.DryRun {
			fmt.Fprintf(opts.Output, "dry-run rdma-device-counters %s %s: %s\n", phase, target.Name, command)
			continue
		}
		output, err := runCommand(opts.Bundle.Check, target, command)
		if err != nil {
			message := fmt.Sprintf("rdma-device-counters %s %s: %v", phase, target.Name, err)
			failures = append(failures, message)
			fmt.Fprintf(opts.Output, "FAIL %s\n", message)
			continue
		}
		snapshots[target.Name] = rdmaDeviceCounterSnapshot{Devices: parseRDMADeviceCounterOutput(output)}
	}
	return snapshots, failures
}

func rdmaCounterDevices(groups []spec.CheckRDMAGroup) []string {
	seen := map[string]bool{}
	devices := make([]string, 0, len(groups))
	for _, group := range groups {
		device := strings.TrimSpace(group.IBDevice)
		if device == "" || seen[device] {
			continue
		}
		seen[device] = true
		devices = append(devices, device)
	}
	return devices
}

func rdmaDeviceCounterCommand(devices []string) string {
	quoted := make([]string, 0, len(devices))
	for _, device := range devices {
		quoted = append(quoted, shellQuote(device))
	}
	return fmt.Sprintf("for d in %s; do for p in /sys/class/infiniband/$d/ports/*; do [ -d \"$p\" ] || continue; port=${p##*/}; echo __envinit_rdma=$d:$port; for dir in counters hw_counters; do [ -d \"$p/$dir\" ] || continue; for f in \"$p/$dir\"/*; do [ -f \"$f\" ] || continue; v=$(cat \"$f\" 2>/dev/null) || true; case \"$v\" in ''|*[!0-9]*) continue;; esac; echo \"$dir.${f##*/}: $v\"; done; done; done; done", strings.Join(quoted, " "))
}

func parseRDMADeviceCounterOutput(output string) map[string]map[string]map[string]int64 {
	counters := map[string]map[string]map[string]int64{}
	currentDevice := ""
	currentPort := ""
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "__envinit_rdma=") {
			value := strings.TrimSpace(strings.TrimPrefix(line, "__envinit_rdma="))
			parts := strings.SplitN(value, ":", 2)
			currentDevice = strings.TrimSpace(parts[0])
			currentPort = ""
			if len(parts) == 2 {
				currentPort = strings.TrimSpace(parts[1])
			}
			if currentDevice != "" && currentPort != "" {
				if counters[currentDevice] == nil {
					counters[currentDevice] = map[string]map[string]int64{}
				}
				if counters[currentDevice][currentPort] == nil {
					counters[currentDevice][currentPort] = map[string]int64{}
				}
			}
			continue
		}
		if currentDevice == "" || currentPort == "" {
			continue
		}
		name, value, ok := parseNICCounterLine(line)
		if !ok {
			continue
		}
		counters[currentDevice][currentPort][name] = value
	}
	return counters
}

func compareRDMADeviceCounterSnapshots(opts Options, targets []Target, groupsByTarget resolvedRDMAGroups, before map[string]rdmaDeviceCounterSnapshot, after map[string]rdmaDeviceCounterSnapshot) []string {
	if opts.DryRun {
		return nil
	}
	var rows []rdmaDeviceCounterRow
	abnormalCount := 0
	for _, target := range targets {
		targetBefore, beforeOK := before[target.Name]
		targetAfter, afterOK := after[target.Name]
		if !beforeOK || !afterOK {
			continue
		}
		devices := rdmaCounterDevices(groupsByTarget[target.Name])
		for _, device := range devices {
			ports := unionRDMADevicePorts(targetBefore.Devices[device], targetAfter.Devices[device])
			if len(ports) == 0 {
				fmt.Fprintf(opts.Output, "WARN rdma-device-counters %s %s: no sysfs counters found\n", target.Name, device)
				continue
			}
			for _, port := range ports {
				beforeCounters := targetBefore.Devices[device][port]
				afterCounters := targetAfter.Devices[device][port]
				names := unionCounterNames(beforeCounters, afterCounters)
				for _, name := range names {
					delta := afterCounters[name] - beforeCounters[name]
					if beforeCounters[name] == 0 && afterCounters[name] == 0 {
						continue
					}
					status := "SAME"
					failure := false
					if delta < 0 {
						status = "WARN"
					} else if delta > 0 && isAbnormalRDMACounter(name) {
						status = "FAIL"
						failure = true
						abnormalCount++
					} else if delta > 0 {
						status = "INFO"
					}
					rows = append(rows, rdmaDeviceCounterRow{
						Status:  status,
						Node:    target.Name,
						Device:  device,
						Port:    port,
						Counter: name,
						Before:  beforeCounters[name],
						After:   afterCounters[name],
						Delta:   delta,
						Failure: failure,
					})
				}
			}
		}
	}
	printRDMADeviceCounterTable(opts.Output, rows)
	if abnormalCount > 0 {
		return []string{fmt.Sprintf("rdma-device-counters detected %d abnormal counter delta(s); see RDMA device counter delta summary", abnormalCount)}
	}
	return nil
}

func unionRDMADevicePorts(before map[string]map[string]int64, after map[string]map[string]int64) []string {
	seen := map[string]bool{}
	ports := make([]string, 0, len(before)+len(after))
	for port := range before {
		if seen[port] {
			continue
		}
		seen[port] = true
		ports = append(ports, port)
	}
	for port := range after {
		if seen[port] {
			continue
		}
		seen[port] = true
		ports = append(ports, port)
	}
	sort.Strings(ports)
	return ports
}

func printRDMADeviceCounterTable(output io.Writer, rows []rdmaDeviceCounterRow) {
	if len(rows) == 0 {
		fmt.Fprintln(output, "PASS rdma-device-counters no nonzero counters or counter deltas")
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left := rows[i]
		right := rows[j]
		if left.Failure != right.Failure {
			return left.Failure
		}
		for _, pair := range [][2]string{
			{left.Node, right.Node},
			{left.Device, right.Device},
			{left.Port, right.Port},
			{left.Counter, right.Counter},
		} {
			if pair[0] != pair[1] {
				return pair[0] < pair[1]
			}
		}
		return false
	})
	headers := []string{"STATUS", "NODE", "DEVICE", "PORT", "COUNTER", "BEFORE", "AFTER", "DELTA"}
	widths := make([]int, len(headers))
	for idx, header := range headers {
		widths[idx] = len(header)
	}
	tableRows := make([][]string, 0, len(rows))
	for _, row := range rows {
		cells := []string{
			row.Status,
			row.Node,
			row.Device,
			row.Port,
			row.Counter,
			strconv.FormatInt(row.Before, 10),
			strconv.FormatInt(row.After, 10),
			formatSignedInt(row.Delta),
		}
		for idx, cell := range cells {
			if len(cell) > widths[idx] {
				widths[idx] = len(cell)
			}
		}
		tableRows = append(tableRows, cells)
	}
	fmt.Fprintln(output, "RDMA device counter delta summary:")
	fmt.Fprintln(output, formatTableLine(headers, widths))
	fmt.Fprintln(output, formatTableSeparator(widths))
	for idx, cells := range tableRows {
		line := formatTableLine(cells, widths)
		if rows[idx].Failure {
			line = redText(line)
		}
		fmt.Fprintln(output, line)
	}
}

func isAbnormalRDMACounter(name string) bool {
	lower := strings.ToLower(name)
	for _, token := range roceAbnormalCounterTokens() {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func formatTableLine(cells []string, widths []int) string {
	parts := make([]string, 0, len(cells))
	for idx, cell := range cells {
		parts = append(parts, fmt.Sprintf("%-*s", widths[idx], cell))
	}
	return strings.Join(parts, "  ")
}

func formatTableSeparator(widths []int) string {
	parts := make([]string, 0, len(widths))
	for _, width := range widths {
		parts = append(parts, strings.Repeat("-", width))
	}
	return strings.Join(parts, "  ")
}

func formatSignedInt(value int64) string {
	if value > 0 {
		return "+" + strconv.FormatInt(value, 10)
	}
	return strconv.FormatInt(value, 10)
}

func redText(value string) string {
	return "\033[31m" + value + "\033[0m"
}

func isAbnormalNICCounter(name string) bool {
	lower := strings.ToLower(name)
	for _, token := range roceAbnormalCounterTokens() {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func roceAbnormalCounterTokens() []string {
	return []string{
		"port_xmit_discards",
		"port_rcv_errors",
		"packet_seq_err",
		"local_ack_timeout_err",
		"out_of_sequence",
		"port_xmit_wait",
		"np_cnp_sent",
		"rp_cnp_handled",
		"rx_prio0_buf_discard",
		"rx_prio1_buf_discard",
		"rx_prio2_buf_discard",
		"rx_prio3_buf_discard",
		"rx_prio4_buf_discard",
		"rx_prio5_buf_discard",
		"rx_prio6_buf_discard",
		"rx_prio7_buf_discard",
		"timeout",
		"drop",
		"discard",
		"crc",
		"err",
		"error",
		"retrans",
		"out_of_buffer",
		"overrun",
		"nak",
		"seq",
		"duplicate",
		"link_downed",
		"vl15_dropped",
	}
}
