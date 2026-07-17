package checker

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"envinit/internal/spec"
)

type discoveredRDMAInterface struct {
	Name     string
	IP       string
	IBDevice string
}

func DiscoverNetwork(opts DiscoverOptions) error {
	if opts.Output == nil {
		opts.Output = os.Stdout
	}
	targets, err := ResolveDiscoveryTargets(opts.Records, opts.Hosts)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return fmt.Errorf("discover requires at least one host")
	}
	targets = markLocalTargets(targets)
	_, err = autoDiscoverNetworkTargets(opts, targets)
	return err
}

func autoDiscoverNetworkTargets(opts DiscoverOptions, targets []Target) ([]Target, error) {
	updated := make([]Target, len(targets))
	copy(updated, targets)
	mgmtDiscovered := map[string][]discoveredIPv4Address{}
	discovered := map[string][]discoveredRDMAInterface{}
	for idx := range updated {
		mgmtItems, err := discoverTargetManagementInterfaces(opts, updated[idx])
		if err != nil {
			return nil, err
		}
		items, err := discoverTargetRDMAInterfaces(opts, updated[idx])
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			return nil, fmt.Errorf("discover found no IPv4 show_gids RDMA entries for %s", updated[idx].Name)
		}
		mgmtItems = filterRDMAFromManagementCandidates(mgmtItems, items)
		mgmtDiscovered[updated[idx].Name] = mgmtItems
		mgmtIP := preferredManagementIP(mgmtItems)
		if mgmtIP != "" {
			updated[idx].Address = mgmtIP
			fmt.Fprintf(opts.Output, "INFO discover management %s: mgmt_ip=%s\n", updated[idx].Name, mgmtIP)
		}
		discovered[updated[idx].Name] = items
		updated[idx].RDMA = discoveredToRDMARecords(items)
		fmt.Fprintf(opts.Output, "INFO discover RDMA %s: %s\n", updated[idx].Name, describeDiscoveredRDMA(items))
	}
	if opts.Confirm {
		reviewed, err := confirmDiscoveredNetwork(opts, updated, mgmtDiscovered, discovered)
		if err != nil {
			return nil, err
		}
		updated = reviewed
	}
	if opts.DryRun {
		fmt.Fprintf(opts.Output, "dry-run: would update inventory network fields: %s\n", opts.InventoryPath)
		fmt.Fprintln(opts.Output, "dry-run: bundle remains unchanged")
		return updated, nil
	}
	if strings.TrimSpace(opts.InventoryPath) != "" {
		if err := updateDelimitedInventoryRDMA(opts.InventoryPath, updated); err != nil {
			return nil, err
		}
		fmt.Fprintf(opts.Output, "INFO updated inventory network fields: %s\n", opts.InventoryPath)
		fmt.Fprintln(opts.Output, "INFO bundle remains unchanged")
	}
	return updated, nil
}

func discoverTargetManagementInterfaces(opts DiscoverOptions, target Target) ([]discoveredIPv4Address, error) {
	command := strings.Join([]string{
		"ip -o -4 route show default 2>/dev/null || true",
		"printf '\\n--ADDR--\\n'",
		"ip -o -4 addr show scope global 2>/dev/null || true",
	}, "; ")
	output, err := runCommand(opts.Bundle.Check, target, command)
	if err != nil {
		return nil, fmt.Errorf("discover management IP for %s: %w", target.Name, err)
	}
	return parseManagementIPCandidates(output), nil
}

type discoveredIPv4Address struct {
	Iface     string
	IP        string
	Preferred bool
}

func parseManagementIPDiscovery(output string) string {
	return preferredManagementIP(parseManagementIPCandidates(output))
}

func parseManagementIPCandidates(output string) []discoveredIPv4Address {
	defaultIfaces := map[string]bool{}
	var addrs []discoveredIPv4Address
	inAddr := false
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "--ADDR--" {
			inAddr = true
			continue
		}
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if !inAddr {
			for idx, field := range fields {
				if field == "dev" && idx+1 < len(fields) {
					defaultIfaces[fields[idx+1]] = true
				}
			}
			continue
		}
		if len(fields) < 4 {
			continue
		}
		iface := strings.TrimSuffix(fields[1], ":")
		if shouldIgnoreManagementIface(iface) {
			continue
		}
		ip := ""
		for idx, field := range fields {
			if field == "inet" && idx+1 < len(fields) {
				ip = strings.SplitN(fields[idx+1], "/", 2)[0]
				break
			}
		}
		if net.ParseIP(ip).To4() == nil {
			continue
		}
		addrs = append(addrs, discoveredIPv4Address{Iface: iface, IP: ip, Preferred: defaultIfaces[iface]})
	}
	if len(addrs) > 0 {
		sort.Slice(addrs, func(i, j int) bool {
			if addrs[i].Preferred != addrs[j].Preferred {
				return addrs[i].Preferred
			}
			leftBond := strings.HasPrefix(strings.ToLower(addrs[i].Iface), "bond")
			rightBond := strings.HasPrefix(strings.ToLower(addrs[j].Iface), "bond")
			if leftBond != rightBond {
				return leftBond
			}
			left := ipv4SortKey(addrs[i].IP)
			right := ipv4SortKey(addrs[j].IP)
			if left != right {
				return left < right
			}
			return addrs[i].Iface < addrs[j].Iface
		})
	}
	return addrs
}

func preferredManagementIP(addrs []discoveredIPv4Address) string {
	if len(addrs) == 0 {
		return ""
	}
	return addrs[0].IP
}

func shouldIgnoreManagementIface(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || name == "lo" {
		return true
	}
	for _, prefix := range []string{"ib", "rdma", "vxlan.", "cali", "flannel", "docker", "veth", "br-", "virbr", "kube"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func filterRDMAFromManagementCandidates(mgmt []discoveredIPv4Address, rdma []discoveredRDMAInterface) []discoveredIPv4Address {
	if len(mgmt) == 0 || len(rdma) == 0 {
		return mgmt
	}
	rdmaNames := map[string]bool{}
	rdmaIPs := map[string]bool{}
	for _, item := range rdma {
		rdmaNames[strings.ToLower(strings.TrimSpace(item.Name))] = true
		rdmaIPs[strings.TrimSpace(item.IP)] = true
	}
	out := make([]discoveredIPv4Address, 0, len(mgmt))
	for _, item := range mgmt {
		if rdmaNames[strings.ToLower(strings.TrimSpace(item.Iface))] || rdmaIPs[strings.TrimSpace(item.IP)] {
			continue
		}
		out = append(out, item)
	}
	return out
}

func discoverTargetRDMAInterfaces(opts DiscoverOptions, target Target) ([]discoveredRDMAInterface, error) {
	output, err := runCommand(opts.Bundle.Check, target, "show_gids")
	if err != nil {
		return nil, fmt.Errorf("run show_gids on %s: %w", target.Name, err)
	}
	return parseShowGIDs(output), nil
}

func parseShowGIDs(output string) []discoveredRDMAInterface {
	type candidate struct {
		item discoveredRDMAInterface
		rank int
	}
	byName := map[string]candidate{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || strings.EqualFold(fields[0], "DEV") || strings.HasPrefix(fields[0], "---") {
			continue
		}
		ip := ""
		ipIndex := -1
		for idx, field := range fields {
			if parsed := net.ParseIP(field); parsed != nil && parsed.To4() != nil {
				ip = parsed.String()
				ipIndex = idx
				break
			}
		}
		if ip == "" || ipIndex < 0 || len(fields) == 0 {
			continue
		}
		iface := fields[len(fields)-1]
		if shouldIgnoreDiscoveredIface(iface) {
			continue
		}
		version := ""
		if ipIndex+1 < len(fields) {
			version = fields[ipIndex+1]
		}
		rank := 2
		if strings.EqualFold(version, "v1") {
			rank = 1
		}
		current, ok := byName[iface]
		if ok && current.rank <= rank {
			continue
		}
		byName[iface] = candidate{
			item: discoveredRDMAInterface{
				Name:     iface,
				IP:       ip,
				IBDevice: fields[0],
			},
			rank: rank,
		}
	}
	items := make([]discoveredRDMAInterface, 0, len(byName))
	for _, item := range byName {
		items = append(items, item.item)
	}
	sort.Slice(items, func(i, j int) bool {
		left := ipv4SortKey(items[i].IP)
		right := ipv4SortKey(items[j].IP)
		if left != right {
			return left < right
		}
		return items[i].Name < items[j].Name
	})
	return items
}

func shouldIgnoreDiscoveredIface(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || name == "lo" || name == "bond0" || strings.HasPrefix(name, "bond") {
		return true
	}
	for _, prefix := range []string{"vxlan.", "cali", "flannel", "docker", "veth", "br-", "virbr", "kube"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func ipv4SortKey(value string) uint32 {
	ip := net.ParseIP(strings.TrimSpace(value)).To4()
	if ip == nil {
		return 0
	}
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}

func discoveredToRDMARecords(items []discoveredRDMAInterface) []spec.RDMARecord {
	records := make([]spec.RDMARecord, 0, len(items))
	for _, item := range items {
		records = append(records, spec.RDMARecord{
			Name: item.Name,
			IP:   item.IP,
		})
	}
	return records
}

func describeDiscoveredRDMA(items []discoveredRDMAInterface) string {
	parts := make([]string, 0, len(items))
	for idx, item := range items {
		parts = append(parts, fmt.Sprintf("rdma%d=%s/%s(%s)", idx+1, item.Name, item.IP, item.IBDevice))
	}
	return strings.Join(parts, ", ")
}

func confirmDiscoveredNetwork(opts DiscoverOptions, targets []Target, mgmt map[string][]discoveredIPv4Address, discovered map[string][]discoveredRDMAInterface) ([]Target, error) {
	fmt.Fprintln(opts.Output, "Auto-discovered network inventory:")
	for _, target := range targets {
		fmt.Fprintf(opts.Output, "  %s mgmt_ip=%s\n", target.Name, target.Address)
		for idx, item := range discovered[target.Name] {
			fmt.Fprintf(opts.Output, "  %s rdma%d name=%s ip=%s ib_device=%s\n", target.Name, idx+1, item.Name, item.IP, item.IBDevice)
		}
	}
	if !terminalFile(os.Stdin) || !terminalFile(os.Stdout) {
		return nil, fmt.Errorf("interactive RDMA discovery confirmation requires a terminal; rerun with --yes to accept auto-discovered fields or --dry-run to preview without writing")
	}
	return runNetworkDiscoveryReview(targets, mgmt, discovered)
}

func updateDelimitedInventoryRDMA(path string, targets []Target) error {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".csv" && ext != ".tsv" && ext != ".txt" {
		return fmt.Errorf("discover inventory write-back supports only .csv/.tsv/.txt, got %s", ext)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read inventory for RDMA update: %w", err)
	}
	delimiter := detectDelimitedInventoryDelimiter(data)
	reader := csv.NewReader(bytes.NewReader(data))
	reader.Comma = delimiter
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	rows, err := reader.ReadAll()
	if err != nil {
		return fmt.Errorf("parse inventory for RDMA update: %w", err)
	}
	if len(rows) == 0 {
		return fmt.Errorf("inventory is empty")
	}
	targetByKey, err := targetIndexByInventoryKey(targets)
	if err != nil {
		return err
	}
	header := append([]string{}, rows[0]...)
	headerIndex := map[string]int{}
	for idx, value := range header {
		headerIndex[canonicalInventoryHeader(value)] = idx
	}
	for _, key := range []string{"mgmt_ip"} {
		if _, ok := headerIndex[key]; ok {
			continue
		}
		headerIndex[key] = len(header)
		header = append(header, key)
	}
	maxRDMA := 0
	for _, target := range targets {
		if len(target.RDMA) > maxRDMA {
			maxRDMA = len(target.RDMA)
		}
	}
	for idx := 1; idx <= maxRDMA; idx++ {
		for _, field := range []string{"name", "ip"} {
			key := fmt.Sprintf("rdma%d_%s", idx, field)
			if _, ok := headerIndex[key]; ok {
				continue
			}
			headerIndex[key] = len(header)
			header = append(header, key)
		}
	}
	rows[0] = header
	matchedTargets := map[int]int{}
	for rowIdx := 1; rowIdx < len(rows); rowIdx++ {
		row := rows[rowIdx]
		if len(row) < len(header) {
			row = append(row, make([]string, len(header)-len(row))...)
		}
		targetIndex, ok := targetIndexForInventoryRow(row, headerIndex, targetByKey)
		if !ok {
			rows[rowIdx] = row
			continue
		}
		if previousRow, exists := matchedTargets[targetIndex]; exists {
			return fmt.Errorf("inventory conflict: target %s matches multiple rows (%d and %d); remove the duplicate row before discover write-back", targets[targetIndex].Name, previousRow+1, rowIdx+1)
		}
		matchedTargets[targetIndex] = rowIdx
		writeTargetInventoryFields(row, headerIndex, targets[targetIndex])
		rows[rowIdx] = row
	}
	for targetIndex, target := range targets {
		if _, ok := matchedTargets[targetIndex]; ok {
			continue
		}
		row := make([]string, len(header))
		writeNewTargetIdentity(row, headerIndex, target)
		writeTargetInventoryFields(row, headerIndex, target)
		rows = append(rows, row)
	}
	var out bytes.Buffer
	writer := csv.NewWriter(&out)
	writer.Comma = delimiter
	if err := writer.WriteAll(rows); err != nil {
		return fmt.Errorf("encode inventory RDMA update: %w", err)
	}
	return os.WriteFile(path, out.Bytes(), 0o644)
}

func targetIndexByInventoryKey(targets []Target) (map[string]int, error) {
	out := map[string]int{}
	for idx, target := range targets {
		for _, key := range []string{target.Input, target.Name, target.Address, target.ExpectedHostname} {
			key = strings.ToLower(strings.TrimSpace(key))
			if key == "" {
				continue
			}
			if existing, ok := out[key]; ok && existing != idx {
				return nil, fmt.Errorf("discover target conflict: %q matches both %s and %s", key, targets[existing].Name, target.Name)
			}
			out[key] = idx
		}
	}
	return out, nil
}

func writeNewTargetIdentity(row []string, headerIndex map[string]int, target Target) {
	hostID := firstNonEmpty(target.Input, target.Name, target.ExpectedHostname, target.Address)
	hostname := firstNonEmpty(target.Name, target.ExpectedHostname, target.Input)
	setFirstExistingInventoryField(row, headerIndex, []string{"host_id", "host", "node_id", "asset_tag"}, hostID)
	setFirstExistingInventoryField(row, headerIndex, []string{"hostname", "node", "machine"}, hostname)
}

func writeTargetInventoryFields(row []string, headerIndex map[string]int, target Target) {
	managementIP := strings.TrimSpace(target.Address)
	if parsed := net.ParseIP(managementIP); parsed != nil && parsed.To4() != nil {
		row[headerIndex["mgmt_ip"]] = managementIP
	}
	for key, column := range headerIndex {
		slot, ok := inventoryRDMASlot(key)
		if ok && slot > len(target.RDMA) && column < len(row) {
			row[column] = ""
		}
	}
	for idx, item := range target.RDMA {
		row[headerIndex[fmt.Sprintf("rdma%d_name", idx+1)]] = strings.TrimSpace(item.Name)
		row[headerIndex[fmt.Sprintf("rdma%d_ip", idx+1)]] = strings.TrimSpace(item.IP)
	}
}

func inventoryRDMASlot(key string) (int, bool) {
	if !strings.HasPrefix(key, "rdma") {
		return 0, false
	}
	rest := strings.TrimPrefix(key, "rdma")
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 || end >= len(rest) || rest[end] != '_' {
		return 0, false
	}
	slot, err := strconv.Atoi(rest[:end])
	return slot, err == nil && slot > 0
}

func setFirstExistingInventoryField(row []string, headerIndex map[string]int, keys []string, value string) {
	value = strings.TrimSpace(value)
	for _, key := range keys {
		idx, ok := headerIndex[key]
		if ok && idx < len(row) {
			row[idx] = value
			return
		}
	}
}

func detectDelimitedInventoryDelimiter(data []byte) rune {
	firstLine := string(data)
	if idx := strings.IndexByte(firstLine, '\n'); idx >= 0 {
		firstLine = firstLine[:idx]
	}
	best := ','
	bestCount := -1
	for _, candidate := range []rune{',', '\t', ';'} {
		count := strings.Count(firstLine, string(candidate))
		if count > bestCount {
			best = candidate
			bestCount = count
		}
	}
	return best
}

func canonicalInventoryHeader(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	replacer := strings.NewReplacer("-", "_", " ", "_", "/", "_", ".", "_", "(", "", ")", "")
	raw = replacer.Replace(raw)
	return strings.Trim(raw, "_")
}

func targetIndexForInventoryRow(row []string, headerIndex map[string]int, targets map[string]int) (int, bool) {
	for _, header := range []string{"host_id", "host", "hostname", "node", "machine", "mgmt_ip", "bond_ip", "management_ip"} {
		idx, ok := headerIndex[header]
		if !ok || idx >= len(row) {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(row[idx]))
		if key == "" {
			continue
		}
		if targetIndex, ok := targets[key]; ok {
			return targetIndex, true
		}
	}
	return 0, false
}

func terminalFile(file *os.File) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}
