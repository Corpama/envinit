package checker

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"envinit/internal/spec"
)

type Options struct {
	Bundle       spec.Bundle
	Records      []spec.MachineRecord
	Hosts        []string
	RunBandwidth bool
	RunRDMAPing  bool
	DryRun       bool
	Output       io.Writer
}

type Target struct {
	Input            string
	Name             string
	ExpectedHostname string
	Address          string
	RDMA             []spec.RDMARecord
	Local            bool
}

type Result struct {
	Server      Target
	Client      Target
	ServerGroup spec.CheckRDMAGroup
	ClientGroup spec.CheckRDMAGroup
	ServerXP    string
	ClientXP    string
	Port        int
	GBits       float64
	Passed      bool
	Output      string
}

type bandwidthResultRow struct {
	Status    string
	Client    string
	Server    string
	ClientDev string
	ServerDev string
	Port      string
	ClientXP  string
	ServerXP  string
	Bandwidth string
	Failure   bool
}

type checkStream struct {
	ServerGroup     spec.CheckRDMAGroup
	ServerRDMAIndex int
	ClientGroup     spec.CheckRDMAGroup
	ClientRDMAIndex int
	ServerOffset    string
	ClientOffset    string
	Port            int
}

type rdmaPingItem struct {
	Index         int
	SourceName    string
	DestinationIP string
}

type rdmaPingResultRow struct {
	Status        string
	Source        string
	Destination   string
	RDMA          string
	SourceIface   string
	DestinationIP string
	Payload       string
	Result        string
	Failure       bool
}

type nicCounterSnapshot struct {
	Interfaces map[string]map[string]int64
}

type rdmaDeviceCounterSnapshot struct {
	Devices map[string]map[string]map[string]int64
}

type resolvedRDMAGroups map[string][]spec.CheckRDMAGroup

type nicCounterRow struct {
	Status  string
	Node    string
	Iface   string
	Counter string
	Before  int64
	After   int64
	Delta   int64
	Failure bool
}

type rdmaDeviceCounterRow struct {
	Status  string
	Node    string
	Device  string
	Port    string
	Counter string
	Before  int64
	After   int64
	Delta   int64
	Failure bool
}

func Run(opts Options) error {
	if opts.Output == nil {
		opts.Output = io.Discard
	}
	runBandwidth := opts.RunBandwidth
	runRDMAPing := opts.RunRDMAPing
	if !runBandwidth && !runRDMAPing {
		runBandwidth = true
	}
	targets, err := ResolveTargets(opts.Records, opts.Hosts)
	if err != nil {
		return err
	}
	if len(targets) < 2 {
		return errors.New("check requires at least two hosts")
	}
	targets = markLocalTargets(targets)
	if !opts.DryRun {
		warnHostnameMismatches(opts, targets)
	}
	if runBandwidth && len(opts.Bundle.Check.RDMAGroups) == 0 {
		return errors.New("bundle check.rdma_groups is required")
	}
	if runBandwidth {
		for _, group := range opts.Bundle.Check.RDMAGroups {
			if err := validateGroup(opts.Bundle.Check, group); err != nil {
				return err
			}
		}
	}
	resolvedGroups := resolvedRDMAGroups{}
	if runBandwidth {
		var err error
		resolvedGroups, err = resolveBandwidthGroups(opts, targets)
		if err != nil {
			return err
		}
	}

	var failures []string
	var bandwidthResults []Result
	nicBefore, nicFailures := collectNICCounterSnapshots(opts, targets, "before")
	failures = append(failures, nicFailures...)
	if runRDMAPing {
		failures = append(failures, runRDMAPingChecks(opts, targets)...)
	}
	var rdmaDeviceBefore map[string]rdmaDeviceCounterSnapshot
	if runBandwidth {
		var rdmaFailures []string
		rdmaDeviceBefore, rdmaFailures = collectRDMADeviceCounterSnapshots(opts, targets, resolvedGroups, "before")
		failures = append(failures, rdmaFailures...)
	}
	if runBandwidth {
		for i := 0; i < len(targets); i++ {
			for j := i + 1; j < len(targets); j++ {
				for _, pair := range [][2]Target{{targets[i], targets[j]}, {targets[j], targets[i]}} {
					if opts.Bundle.Check.Parallel {
						results, errs := runParallel(opts, resolvedGroups, pair[0], pair[1])
						for _, err := range errs {
							failures = append(failures, err.Error())
							fmt.Fprintf(opts.Output, "FAIL %s -> %s: %v\n", pair[1].Name, pair[0].Name, err)
						}
						for _, result := range results {
							bandwidthResults = append(bandwidthResults, result)
							failures = appendBandwidthResultFailure(opts, failures, result)
						}
						continue
					}

					for _, stream := range bandwidthStreams(opts.Bundle.Check) {
						stream = resolveStreamGroups(resolvedGroups, pair[0], pair[1], stream)
						result, err := runStream(opts, pair[0], pair[1], stream)
						if err != nil {
							failures = append(failures, err.Error())
							fmt.Fprintf(opts.Output, "FAIL %s -> %s %s: %v\n", pair[1].Name, pair[0].Name, streamLabel(stream), err)
							continue
						}
						bandwidthResults = append(bandwidthResults, result)
						failures = appendBandwidthResultFailure(opts, failures, result)
					}
				}
			}
		}
	}
	if runBandwidth {
		printBandwidthResultTable(opts.Output, bandwidthResults)
		rdmaDeviceAfter, rdmaFailures := collectRDMADeviceCounterSnapshots(opts, targets, resolvedGroups, "after")
		failures = append(failures, rdmaFailures...)
		failures = append(failures, compareRDMADeviceCounterSnapshots(opts, targets, resolvedGroups, rdmaDeviceBefore, rdmaDeviceAfter)...)
	}
	nicAfter, nicFailures := collectNICCounterSnapshots(opts, targets, "after")
	failures = append(failures, nicFailures...)
	failures = append(failures, compareNICCounterSnapshots(opts, targets, nicBefore, nicAfter)...)
	if len(failures) > 0 {
		return fmt.Errorf("check failed: %s", strings.Join(failures, "; "))
	}
	return nil
}

func appendBandwidthResultFailure(opts Options, failures []string, result Result) []string {
	label := resultLabel(result)
	if !result.Passed {
		failures = append(failures, fmt.Sprintf("%s -> %s %s %.2f Gbps below %.2f Gbps", result.Client.Name, result.Server.Name, label, result.GBits, opts.Bundle.Check.MinGBits))
	}
	return failures
}

func printBandwidthResultTable(output io.Writer, results []Result) {
	if len(results) == 0 {
		fmt.Fprintln(output, "WARN bandwidth results: no completed bandwidth streams")
		return
	}
	rows := make([]bandwidthResultRow, 0, len(results))
	for _, result := range results {
		status := "PASS"
		if !result.Passed {
			status = "FAIL"
		}
		row := bandwidthResultRow{
			Status:    status,
			Client:    result.Client.Name,
			Server:    result.Server.Name,
			ClientDev: result.ClientGroup.IBDevice,
			ServerDev: result.ServerGroup.IBDevice,
			Port:      "-",
			ClientXP:  "-",
			ServerXP:  "-",
			Bandwidth: "unknown",
			Failure:   !result.Passed,
		}
		if result.Port > 0 {
			row.Port = strconv.Itoa(result.Port)
		}
		if strings.TrimSpace(result.ClientXP) != "" {
			row.ClientXP = strings.TrimSpace(result.ClientXP)
		}
		if strings.TrimSpace(result.ServerXP) != "" {
			row.ServerXP = strings.TrimSpace(result.ServerXP)
		}
		if !math.IsNaN(result.GBits) {
			row.Bandwidth = fmt.Sprintf("%.2f Gbps", result.GBits)
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left := rows[i]
		right := rows[j]
		if left.Failure != right.Failure {
			return left.Failure
		}
		for _, pair := range [][2]string{
			{left.Client, right.Client},
			{left.Server, right.Server},
			{left.ClientDev, right.ClientDev},
			{left.ServerDev, right.ServerDev},
			{left.Port, right.Port},
			{left.ClientXP, right.ClientXP},
			{left.ServerXP, right.ServerXP},
		} {
			if pair[0] != pair[1] {
				return pair[0] < pair[1]
			}
		}
		return false
	})

	headers := []string{"STATUS", "CLIENT", "SERVER", "CLIENT_DEV", "SERVER_DEV", "PORT", "CLIENT_XPU", "SERVER_XPU", "BANDWIDTH"}
	widths := make([]int, len(headers))
	for idx, header := range headers {
		widths[idx] = len(header)
	}
	tableRows := make([][]string, 0, len(rows))
	for _, row := range rows {
		cells := []string{
			row.Status,
			row.Client,
			row.Server,
			row.ClientDev,
			row.ServerDev,
			row.Port,
			row.ClientXP,
			row.ServerXP,
			row.Bandwidth,
		}
		for idx, cell := range cells {
			if len(cell) > widths[idx] {
				widths[idx] = len(cell)
			}
		}
		tableRows = append(tableRows, cells)
	}

	fmt.Fprintln(output, "Bandwidth result summary:")
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

func ResolveTargets(records []spec.MachineRecord, hostInputs []string) ([]Target, error) {
	var targets []Target
	seen := map[string]bool{}
	for _, raw := range hostInputs {
		for _, input := range splitHosts(raw) {
			target, err := resolveTarget(records, input)
			if err != nil {
				return nil, err
			}
			key := strings.ToLower(target.Address)
			if seen[key] {
				continue
			}
			seen[key] = true
			targets = append(targets, target)
		}
	}
	return targets, nil
}

func splitHosts(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '|' || r == ' ' || r == '\t' || r == '\n'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

func resolveTarget(records []spec.MachineRecord, input string) (Target, error) {
	input = strings.TrimSpace(input)
	for _, record := range records {
		if matchesRecord(record, input) {
			address := strings.TrimSpace(record.MgmtIP)
			if address == "" {
				return Target{}, fmt.Errorf("inventory record %q has no mgmt_ip", input)
			}
			return Target{
				Input:            input,
				Name:             firstNonEmpty(record.Hostname, record.HostID, record.MgmtIP),
				ExpectedHostname: firstNonEmpty(record.Hostname, record.HostID),
				Address:          address,
				RDMA:             append([]spec.RDMARecord{}, record.RDMA...),
			}, nil
		}
	}
	if input == "" {
		return Target{}, errors.New("empty host in --hosts")
	}
	if net.ParseIP(input) != nil {
		return Target{Input: input, Name: input, Address: input}, nil
	}
	return Target{Input: input, Name: input, Address: input}, nil
}

func matchesRecord(record spec.MachineRecord, input string) bool {
	input = strings.TrimSpace(input)
	for _, value := range []string{record.HostID, record.Hostname, record.MgmtIP} {
		if strings.EqualFold(strings.TrimSpace(value), input) {
			return true
		}
	}
	return false
}

func markLocalTargets(targets []Target) []Target {
	localIPs := localIPSet()
	for idx := range targets {
		address := strings.TrimSpace(targets[idx].Address)
		ip := net.ParseIP(address)
		if ip != nil && (ip.IsLoopback() || localIPs[ip.String()]) {
			targets[idx].Local = true
		}
	}
	return targets
}

func localIPSet() map[string]bool {
	out := map[string]bool{}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, addr := range addrs {
		var ip net.IP
		switch value := addr.(type) {
		case *net.IPNet:
			ip = value.IP
		case *net.IPAddr:
			ip = value.IP
		}
		if ip == nil {
			continue
		}
		out[ip.String()] = true
	}
	return out
}

func warnHostnameMismatches(opts Options, targets []Target) {
	for _, target := range targets {
		expected := strings.TrimSpace(target.ExpectedHostname)
		if expected == "" {
			continue
		}
		actual, err := runCommand(opts.Bundle.Check, target, "hostnamectl --static 2>/dev/null || hostname")
		if err != nil {
			fmt.Fprintf(opts.Output, "WARN hostname check failed for %s (%s): %v; continuing\n", target.Name, target.Address, err)
			continue
		}
		actual = strings.TrimSpace(actual)
		if actual == "" || strings.EqualFold(actual, expected) {
			continue
		}
		location := "remote"
		if target.Local {
			location = "local"
		}
		fmt.Fprintf(opts.Output, "WARN hostname mismatch: inventory %s expects hostname=%s, %s target %s reports %s; continuing because target is selected by mgmt_ip=%s\n", target.Name, expected, location, target.Name, actual, target.Address)
	}
}

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
		groups := make([]spec.CheckRDMAGroup, len(opts.Bundle.Check.RDMAGroups))
		copy(groups, opts.Bundle.Check.RDMAGroups)
		for idx := range groups {
			iface := targetRDMAInterfaceName(opts.Bundle, target, idx)
			iface = strings.TrimSpace(iface)
			if iface == "" {
				continue
			}
			if opts.DryRun {
				continue
			}
			device, err := resolveIBDeviceForInterface(opts, target, iface)
			if err != nil {
				return nil, err
			}
			configured := strings.TrimSpace(groups[idx].IBDevice)
			groups[idx].IBDevice = device
			if configured != "" && configured != device {
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
	output, err := runCommand(opts.Bundle.Check, target, command)
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

func runRDMAPingChecks(opts Options, targets []Target) []string {
	var failures []string
	var rows []rdmaPingResultRow
	for i := 0; i < len(targets); i++ {
		for j := i + 1; j < len(targets); j++ {
			for _, pair := range [][2]Target{{targets[i], targets[j]}, {targets[j], targets[i]}} {
				items, err := rdmaPingItems(opts.Bundle, pair[0], pair[1])
				if err != nil {
					failures = append(failures, err.Error())
					rows = append(rows, rdmaPingResultRow{
						Status:      "FAIL",
						Source:      pair[0].Name,
						Destination: pair[1].Name,
						RDMA:        "-",
						SourceIface: "-",
						Payload:     strconv.Itoa(opts.Bundle.Check.RDMAPingPayloadSize),
						Result:      err.Error(),
						Failure:     true,
					})
					continue
				}
				pairRows, pairFailures := runRDMAPingPair(opts, pair[0], pair[1], items)
				rows = append(rows, pairRows...)
				failures = append(failures, pairFailures...)
			}
		}
	}
	printRDMAPingResultTable(opts.Output, rows)
	return failures
}

func runRDMAPingPair(opts Options, source Target, destination Target, items []rdmaPingItem) ([]rdmaPingResultRow, []string) {
	rows := make([]rdmaPingResultRow, len(items))
	failures := make([]string, 0)
	type pingResult struct {
		index int
		err   error
	}
	ch := make(chan pingResult, len(items))
	var wg sync.WaitGroup
	for index, item := range items {
		index := index
		item := item
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch <- pingResult{index: index, err: runRDMAPingOne(opts, source, destination, item)}
		}()
	}
	wg.Wait()
	close(ch)
	errs := make([]error, len(items))
	for result := range ch {
		errs[result.index] = result.err
	}
	for index, item := range items {
		if errs[index] != nil {
			failures = append(failures, errs[index].Error())
			rows[index] = rdmaPingResultRow{
				Status:        "FAIL",
				Source:        source.Name,
				Destination:   destination.Name,
				RDMA:          fmt.Sprintf("rdma%d", item.Index+1),
				SourceIface:   item.SourceName,
				DestinationIP: item.DestinationIP,
				Payload:       strconv.Itoa(opts.Bundle.Check.RDMAPingPayloadSize),
				Result:        errs[index].Error(),
				Failure:       true,
			}
			continue
		}
		rows[index] = rdmaPingResultRow{
			Status:        "PASS",
			Source:        source.Name,
			Destination:   destination.Name,
			RDMA:          fmt.Sprintf("rdma%d", item.Index+1),
			SourceIface:   item.SourceName,
			DestinationIP: item.DestinationIP,
			Payload:       strconv.Itoa(opts.Bundle.Check.RDMAPingPayloadSize),
			Result:        "ok",
		}
	}
	return rows, failures
}

func printRDMAPingResultTable(output io.Writer, rows []rdmaPingResultRow) {
	if len(rows) == 0 {
		fmt.Fprintln(output, "WARN rdma-ping results: no completed ping checks")
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left := rows[i]
		right := rows[j]
		if left.Failure != right.Failure {
			return left.Failure
		}
		for _, pair := range [][2]string{
			{left.Source, right.Source},
			{left.Destination, right.Destination},
			{left.RDMA, right.RDMA},
			{left.SourceIface, right.SourceIface},
			{left.DestinationIP, right.DestinationIP},
		} {
			if pair[0] != pair[1] {
				return pair[0] < pair[1]
			}
		}
		return false
	})

	headers := []string{"STATUS", "SOURCE", "DEST", "RDMA", "IFACE", "DEST_IP", "PAYLOAD", "RESULT"}
	widths := make([]int, len(headers))
	for idx, header := range headers {
		widths[idx] = len(header)
	}
	tableRows := make([][]string, 0, len(rows))
	for _, row := range rows {
		cells := []string{
			row.Status,
			row.Source,
			row.Destination,
			row.RDMA,
			firstNonEmpty(row.SourceIface, "-"),
			firstNonEmpty(row.DestinationIP, "-"),
			row.Payload,
			firstNonEmpty(row.Result, "-"),
		}
		for idx, cell := range cells {
			if len(cell) > widths[idx] {
				widths[idx] = len(cell)
			}
		}
		tableRows = append(tableRows, cells)
	}

	fmt.Fprintln(output, "RDMA ping result summary:")
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

func rdmaPingItems(bundle spec.Bundle, source Target, destination Target) ([]rdmaPingItem, error) {
	maxItems := maxInt(len(source.RDMA), len(destination.RDMA), len(bundle.Defaults.RDMAInterfaces))
	items := make([]rdmaPingItem, 0, maxItems)
	var missing []string
	for idx := 0; idx < maxItems; idx++ {
		dstIP := ""
		if idx < len(destination.RDMA) {
			dstIP = strings.TrimSpace(destination.RDMA[idx].IP)
		}
		sourceName := ""
		if idx < len(source.RDMA) {
			sourceName = strings.TrimSpace(source.RDMA[idx].Name)
		}
		if sourceName == "" && idx < len(bundle.Defaults.RDMAInterfaces) {
			sourceName = strings.TrimSpace(bundle.Defaults.RDMAInterfaces[idx].Name)
		}
		if sourceName == "" && idx < len(source.RDMA) {
			sourceName = strings.TrimSpace(source.RDMA[idx].IP)
		}
		if dstIP == "" {
			missing = append(missing, fmt.Sprintf("%s rdma%d_ip", destination.Name, idx+1))
			continue
		}
		if sourceName == "" {
			missing = append(missing, fmt.Sprintf("%s rdma%d_name", source.Name, idx+1))
			continue
		}
		items = append(items, rdmaPingItem{
			Index:         idx,
			SourceName:    sourceName,
			DestinationIP: dstIP,
		})
	}
	if len(items) == 0 {
		if len(missing) == 0 {
			return nil, fmt.Errorf("no RDMA ping targets for %s -> %s", source.Name, destination.Name)
		}
		return nil, fmt.Errorf("no RDMA ping targets for %s -> %s; fill inventory fields: %s", source.Name, destination.Name, strings.Join(missing, ", "))
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("incomplete RDMA ping inventory for %s -> %s; fill fields: %s", source.Name, destination.Name, strings.Join(missing, ", "))
	}
	return items, nil
}

func runRDMAPingOne(opts Options, source Target, destination Target, item rdmaPingItem) error {
	args := rdmaPingArgs(opts.Bundle.Check, item)
	if opts.DryRun {
		fmt.Fprintf(opts.Output, "dry-run rdma-ping %s: %s\n", source.Name, shellJoin(args))
		return nil
	}
	output, err := runCommand(opts.Bundle.Check, source, shellJoin(args))
	if err != nil {
		return fmt.Errorf("ping from %s to %s: %w", source.Name, destination.Name, err)
	}
	if strings.Contains(output, " 0% packet loss") {
		return nil
	}
	return fmt.Errorf("ping from %s to %s reported packet loss:\n%s", source.Name, destination.Name, strings.TrimSpace(output))
}

func rdmaPingArgs(cfg spec.CheckConfig, item rdmaPingItem) []string {
	return []string{
		"ping",
		"-c", strconv.Itoa(cfg.RDMAPingCount),
		"-W", strconv.Itoa(cfg.RDMAPingTimeout),
		"-M", "do",
		"-s", strconv.Itoa(cfg.RDMAPingPayloadSize),
		"-I", item.SourceName,
		item.DestinationIP,
	}
}

func validateGroup(cfg spec.CheckConfig, group spec.CheckRDMAGroup) error {
	if strings.TrimSpace(group.IBDevice) == "" {
		return errors.New("check.rdma_groups[].ib_device is required")
	}
	if strings.TrimSpace(cfg.MmapDevice) == "" {
		return nil
	}
	if len(group.XPUOffsets) < 1 {
		return fmt.Errorf("check.rdma_groups[%s].xpu_offsets requires at least 1 offset when bandwidth mmap is enabled", group.IBDevice)
	}
	return nil
}

func runParallel(opts Options, groupsByTarget resolvedRDMAGroups, server Target, client Target) ([]Result, []error) {
	batches := bandwidthStreamBatches(opts.Bundle.Check)
	if opts.DryRun {
		results := make([]Result, 0)
		for batchIndex, batch := range batches {
			fmt.Fprintf(opts.Output, "dry-run bandwidth batch %d %s -> %s: %d stream(s)\n", batchIndex+1, client.Name, server.Name, len(batch))
			for _, stream := range batch {
				stream = resolveStreamGroups(groupsByTarget, server, client, stream)
				serverArgs := ibWriteBWArgs(opts.Bundle.Check, stream.ServerGroup, stream.ServerOffset, "", stream.Port)
				clientArgs := ibWriteBWArgs(opts.Bundle.Check, stream.ClientGroup, stream.ClientOffset, bandwidthPeerAddress(server, stream), stream.Port)
				fmt.Fprintf(opts.Output, "dry-run server %s: %s\n", server.Name, shellJoin(serverArgs))
				fmt.Fprintf(opts.Output, "dry-run client %s: %s\n", client.Name, shellJoin(clientArgs))
				results = append(results, resultFromOutput(opts.Bundle.Check, server, client, stream, ""))
			}
		}
		return results, nil
	}

	var results []Result
	var errs []error
	for _, batch := range batches {
		for idx := range batch {
			batch[idx] = resolveStreamGroups(groupsByTarget, server, client, batch[idx])
		}
		batchResults, batchErrs := runParallelBatch(opts, server, client, batch)
		results = append(results, batchResults...)
		errs = append(errs, batchErrs...)
	}
	return results, errs
}

func resolveStreamGroups(groupsByTarget resolvedRDMAGroups, server Target, client Target, stream checkStream) checkStream {
	if groups := groupsByTarget[server.Name]; stream.ServerRDMAIndex >= 0 && stream.ServerRDMAIndex < len(groups) {
		stream.ServerGroup = groups[stream.ServerRDMAIndex]
	}
	if groups := groupsByTarget[client.Name]; stream.ClientRDMAIndex >= 0 && stream.ClientRDMAIndex < len(groups) {
		stream.ClientGroup = groups[stream.ClientRDMAIndex]
	}
	return stream
}

func runParallelBatch(opts Options, server Target, client Target, streams []checkStream) ([]Result, []error) {
	type serverProcess struct {
		stream checkStream
		pid    string
	}
	processes := make([]serverProcess, 0, len(streams))
	var errs []error
	for _, stream := range streams {
		serverArgs := ibWriteBWArgs(opts.Bundle.Check, stream.ServerGroup, stream.ServerOffset, "", stream.Port)
		logPath := fmt.Sprintf("/tmp/envinit-check-%s-%d-%d.log", sanitizeName(stream.ServerGroup.IBDevice), stream.Port, time.Now().UnixNano())
		serverCmd := fmt.Sprintf("nohup %s > %s 2>&1 & echo $!", shellJoin(serverArgs), shellQuote(logPath))
		pid, err := runCommand(opts.Bundle.Check, server, serverCmd)
		if err != nil {
			errs = append(errs, fmt.Errorf("start server on %s %s: %w", server.Name, streamLabel(stream), err))
			continue
		}
		processes = append(processes, serverProcess{stream: stream, pid: strings.TrimSpace(pid)})
	}
	defer func() {
		for _, process := range processes {
			_, _ = runCommand(opts.Bundle.Check, server, fmt.Sprintf("kill %s >/dev/null 2>&1 || true", shellQuote(process.pid)))
		}
	}()
	if len(processes) == 0 {
		return nil, errs
	}
	time.Sleep(800 * time.Millisecond)

	type streamResult struct {
		index  int
		result Result
		err    error
	}
	ch := make(chan streamResult, len(processes))
	var wg sync.WaitGroup
	for index, process := range processes {
		index := index
		process := process
		wg.Add(1)
		go func() {
			defer wg.Done()
			clientArgs := ibWriteBWArgs(opts.Bundle.Check, process.stream.ClientGroup, process.stream.ClientOffset, bandwidthPeerAddress(server, process.stream), process.stream.Port)
			output, err := runCommand(opts.Bundle.Check, client, shellJoin(clientArgs))
			if err != nil {
				ch <- streamResult{index: index, err: fmt.Errorf("run client on %s against %s %s: %w", client.Name, server.Name, streamLabel(process.stream), err)}
				return
			}
			ch <- streamResult{index: index, result: resultFromOutput(opts.Bundle.Check, server, client, process.stream, output)}
		}()
	}
	wg.Wait()
	close(ch)

	ordered := make([]*Result, len(processes))
	for item := range ch {
		if item.err != nil {
			errs = append(errs, item.err)
			continue
		}
		ordered[item.index] = &item.result
	}
	results := make([]Result, 0, len(processes))
	for _, result := range ordered {
		if result != nil {
			results = append(results, *result)
		}
	}
	return results, errs
}

func runStream(opts Options, server Target, client Target, stream checkStream) (Result, error) {
	serverArgs := ibWriteBWArgs(opts.Bundle.Check, stream.ServerGroup, stream.ServerOffset, "", stream.Port)
	clientArgs := ibWriteBWArgs(opts.Bundle.Check, stream.ClientGroup, stream.ClientOffset, bandwidthPeerAddress(server, stream), stream.Port)

	if opts.DryRun {
		fmt.Fprintf(opts.Output, "dry-run server %s: %s\n", server.Name, shellJoin(serverArgs))
		fmt.Fprintf(opts.Output, "dry-run client %s: %s\n", client.Name, shellJoin(clientArgs))
		return resultFromOutput(opts.Bundle.Check, server, client, stream, ""), nil
	}

	logPath := fmt.Sprintf("/tmp/envinit-check-%s-%d-%d.log", sanitizeName(stream.ServerGroup.IBDevice), stream.Port, time.Now().UnixNano())
	serverCmd := fmt.Sprintf("nohup %s > %s 2>&1 & echo $!", shellJoin(serverArgs), shellQuote(logPath))
	pid, err := runCommand(opts.Bundle.Check, server, serverCmd)
	if err != nil {
		return Result{}, fmt.Errorf("start server on %s: %w", server.Name, err)
	}
	pid = strings.TrimSpace(pid)
	defer func() {
		_, _ = runCommand(opts.Bundle.Check, server, fmt.Sprintf("kill %s >/dev/null 2>&1 || true", shellQuote(pid)))
	}()
	time.Sleep(800 * time.Millisecond)

	output, err := runCommand(opts.Bundle.Check, client, shellJoin(clientArgs))
	if err != nil {
		return Result{}, fmt.Errorf("run client on %s against %s: %w", client.Name, server.Name, err)
	}
	return resultFromOutput(opts.Bundle.Check, server, client, stream, output), nil
}

func resultFromOutput(cfg spec.CheckConfig, server Target, client Target, stream checkStream, output string) Result {
	gbits, ok := ParseBandwidthGBits(output)
	if !ok {
		gbits = math.NaN()
	}
	passed := true
	if cfg.MinGBits > 0 {
		passed = ok && gbits >= cfg.MinGBits
	}
	return Result{
		Server:      server,
		Client:      client,
		ServerGroup: stream.ServerGroup,
		ClientGroup: stream.ClientGroup,
		ServerXP:    stream.ServerOffset,
		ClientXP:    stream.ClientOffset,
		Port:        stream.Port,
		GBits:       gbits,
		Passed:      passed,
		Output:      output,
	}
}

func bandwidthStreams(cfg spec.CheckConfig) []checkStream {
	streams := make([]checkStream, 0)
	port := bandwidthBasePort(cfg)
	mmapEnabled := strings.TrimSpace(cfg.MmapDevice) != ""
	for clientGroupIndex, clientGroup := range cfg.RDMAGroups {
		clientOffsets := []string{""}
		if mmapEnabled {
			clientOffsets = clientGroup.XPUOffsets
		}
		for serverGroupIndex, serverGroup := range cfg.RDMAGroups {
			serverOffsets := []string{""}
			if mmapEnabled {
				serverOffsets = serverGroup.XPUOffsets
			}
			for _, clientOffset := range clientOffsets {
				for _, serverOffset := range serverOffsets {
					streams = append(streams, checkStream{
						ServerGroup:     serverGroup,
						ServerRDMAIndex: serverGroupIndex,
						ClientGroup:     clientGroup,
						ClientRDMAIndex: clientGroupIndex,
						ServerOffset:    strings.TrimSpace(serverOffset),
						ClientOffset:    strings.TrimSpace(clientOffset),
						Port:            port,
					})
					port++
				}
			}
		}
	}
	return streams
}

func bandwidthStreamBatches(cfg spec.CheckConfig) [][]checkStream {
	streams := bandwidthStreams(cfg)
	batches := make([][]checkStream, 0)
	remaining := append([]checkStream(nil), streams...)
	for len(remaining) > 0 {
		usedClients := make(map[int]bool)
		usedServers := make(map[int]bool)
		batch := make([]checkStream, 0, len(cfg.RDMAGroups))
		nextRemaining := make([]checkStream, 0, len(remaining))
		for _, stream := range remaining {
			if usedClients[stream.ClientRDMAIndex] || usedServers[stream.ServerRDMAIndex] {
				nextRemaining = append(nextRemaining, stream)
				continue
			}
			batch = append(batch, stream)
			usedClients[stream.ClientRDMAIndex] = true
			usedServers[stream.ServerRDMAIndex] = true
		}
		if len(batch) == 0 {
			return append(batches, remaining)
		}
		batches = append(batches, batch)
		remaining = nextRemaining
	}
	return batches
}

func bandwidthBasePort(cfg spec.CheckConfig) int {
	if cfg.BasePort > 0 {
		return cfg.BasePort
	}
	return 18515
}

func bandwidthPeerAddress(server Target, stream checkStream) string {
	if stream.ServerRDMAIndex >= 0 && stream.ServerRDMAIndex < len(server.RDMA) {
		if address := strings.TrimSpace(server.RDMA[stream.ServerRDMAIndex].IP); address != "" {
			return address
		}
	}
	return server.Address
}

func ibWriteBWArgs(cfg spec.CheckConfig, group spec.CheckRDMAGroup, offset string, serverAddress string, port int) []string {
	iterations := cfg.Iterations
	if iterations == 0 {
		iterations = 100
	}
	args := []string{
		"ib_write_bw",
		"-n", strconv.Itoa(iterations),
		"-d", group.IBDevice,
	}
	if cfg.MessageSize > 0 {
		args = append(args, "-s", strconv.Itoa(cfg.MessageSize))
	}
	if cfg.ReportGBits {
		args = append(args, "--report_gbits")
	}
	if strings.TrimSpace(cfg.MmapDevice) != "" {
		args = append(args, "--mmap="+cfg.MmapDevice)
	}
	if strings.TrimSpace(offset) != "" {
		args = append(args, "--mmap-offset="+offset)
	}
	args = append(args, "-F", "-R")
	if port > 0 {
		args = append(args, "-p", strconv.Itoa(port))
	}
	if strings.TrimSpace(serverAddress) != "" {
		args = append(args, serverAddress)
	}
	return args
}

func runCommand(cfg spec.CheckConfig, target Target, remoteCommand string) (string, error) {
	if target.Local {
		cmd := exec.Command("sh", "-c", remoteCommand)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return stdout.String(), fmt.Errorf("local command on %s: %w\n%s", target.Name, err, stderr.String())
		}
		return stdout.String(), nil
	}

	args := append([]string{}, cfg.SSHOptions...)
	destination := target.Address
	if strings.TrimSpace(cfg.SSHUser) != "" {
		destination = cfg.SSHUser + "@" + destination
	}
	args = append(args, destination, remoteCommand)
	cmd := exec.Command("ssh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("ssh %s: %w\n%s", destination, err, stderr.String())
	}
	return stdout.String(), nil
}

func ParseBandwidthGBits(output string) (float64, bool) {
	var bandwidth float64
	ok := false
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "---") {
			continue
		}
		values := numericFields(line)
		if len(values) < 4 {
			continue
		}
		// perftest bandwidth rows are:
		// #bytes #iterations BW peak[Gb/sec] BW average[Gb/sec] ...
		// Any trailing MsgRate column is not bandwidth.
		bandwidth = values[3]
		ok = true
	}
	return bandwidth, ok
}

func numericFields(line string) []float64 {
	var values []float64
	for _, field := range strings.Fields(line) {
		value, err := strconv.ParseFloat(strings.Trim(field, "[],"), 64)
		if err != nil {
			continue
		}
		values = append(values, value)
	}
	return values
}

func shellJoin(args []string) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func sanitizeName(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "rdma"
	}
	return b.String()
}

func maxInt(values ...int) int {
	max := 0
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}

func streamLabel(stream checkStream) string {
	return resultLabel(Result{ClientGroup: stream.ClientGroup, ServerGroup: stream.ServerGroup, Port: stream.Port})
}

func resultLabel(result Result) string {
	label := result.ClientGroup.IBDevice
	if result.ServerGroup.IBDevice != "" && result.ServerGroup.IBDevice != result.ClientGroup.IBDevice {
		label = result.ClientGroup.IBDevice + "->" + result.ServerGroup.IBDevice
	}
	if result.Port > 0 {
		return fmt.Sprintf("%s port=%d", label, result.Port)
	}
	return label
}
