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

	"envinit/internal/nicdetect"
	"envinit/internal/spec"
)

type discoveredRDMAInterface struct {
	Name             string
	IP               string
	Prefix           int
	MAC              string
	IBDevice         string
	MaxSpeedMbps     int
	CurrentSpeedMbps int
	MTU              int
	Model            string
	LinkUp           bool
	LinkKnown        bool
	Reason           string
	Confidence       string
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
	targets, err = bindDiscoveryTargetIdentities(opts, targets)
	if err != nil {
		return err
	}
	_, err = autoDiscoverNetworkTargets(opts, targets)
	return err
}

func bindDiscoveryTargetIdentities(opts DiscoverOptions, targets []Target) ([]Target, error) {
	bound := append([]Target(nil), targets...)
	for idx := range bound {
		target := bound[idx]
		if target.InventoryMatched && !target.ExplicitIdentity {
			continue
		}
		output, err := runNetworkDiscoveryCommand(opts, target, "hostnamectl --static 2>/dev/null || hostname")
		if err != nil {
			return nil, fmt.Errorf("discover hostname through %s: %w", targetControlAddress(target), err)
		}
		hostname := firstNonEmptyLine(output)
		if hostname == "" {
			return nil, fmt.Errorf("discover hostname through %s: remote command returned an empty hostname", targetControlAddress(target))
		}
		target.DiscoveredHostname = hostname

		if target.ExplicitIdentity {
			if !target.InventoryMatched {
				target.Name = hostname
				target.ExpectedHostname = hostname
			}
			if target.InventoryMatched && target.ExpectedHostname != "" && !hostnameEquivalent(target.ExpectedHostname, hostname) {
				fmt.Fprintf(opts.Output, "WARN discover target bind: explicit inventory=%s control=%s reports hostname=%s, expected=%s; keeping explicit mapping\n",
					target.InventoryIdentity, targetControlAddress(target), hostname, target.ExpectedHostname)
			} else {
				fmt.Fprintf(opts.Output, "INFO discover target bind: explicit inventory=%s control=%s hostname=%s\n",
					target.InventoryIdentity, targetControlAddress(target), hostname)
			}
			bound[idx] = target
			continue
		}

		record, err := uniqueInventoryRecordForHostname(opts.Records, hostname)
		if err != nil {
			return nil, fmt.Errorf("bind SSH endpoint %s reporting hostname=%s: %w; use --hosts <inventory-id>=%s to select the intended row explicitly", targetControlAddress(target), hostname, err, targetControlAddress(target))
		}
		if record == nil {
			target.Name = hostname
			target.ExpectedHostname = hostname
			target.InventoryIdentity = hostname
			target.InventoryMatched = false
			bound[idx] = target
			fmt.Fprintf(opts.Output, "INFO discover target bind: control=%s hostname=%s inventory=new-row\n", targetControlAddress(target), hostname)
			continue
		}
		controlAddress := targetControlAddress(target)
		target.Name = firstNonEmpty(record.Hostname, record.HostID, record.MgmtIP)
		target.ExpectedHostname = firstNonEmpty(record.Hostname, record.HostID)
		target.InventoryIdentity = firstNonEmpty(record.HostID, record.Hostname, record.MgmtIP)
		target.InventoryMatched = true
		target.ControlAddress = controlAddress
		if strings.TrimSpace(record.MgmtIP) != "" {
			target.Address = strings.TrimSpace(record.MgmtIP)
		}
		target.RDMA = append([]spec.RDMARecord(nil), record.RDMA...)
		bound[idx] = target
		fmt.Fprintf(opts.Output, "INFO discover target bind: control=%s hostname=%s inventory=%s\n", controlAddress, hostname, target.InventoryIdentity)
	}
	return bound, nil
}

func firstNonEmptyLine(output string) string {
	for _, rawLine := range strings.Split(output, "\n") {
		if line := strings.TrimSpace(rawLine); line != "" {
			return line
		}
	}
	return ""
}

func uniqueInventoryRecordForHostname(records []spec.MachineRecord, hostname string) (*spec.MachineRecord, error) {
	var hostnameMatches []spec.MachineRecord
	for _, record := range records {
		if strings.TrimSpace(record.Hostname) != "" && hostnameEquivalent(record.Hostname, hostname) {
			hostnameMatches = append(hostnameMatches, record)
		}
	}
	if len(hostnameMatches) > 1 {
		return nil, fmt.Errorf("hostname matches %d inventory rows", len(hostnameMatches))
	}
	if len(hostnameMatches) == 1 {
		return &hostnameMatches[0], nil
	}
	var hostIDMatches []spec.MachineRecord
	for _, record := range records {
		if strings.TrimSpace(record.HostID) != "" && hostnameEquivalent(record.HostID, hostname) {
			hostIDMatches = append(hostIDMatches, record)
		}
	}
	if len(hostIDMatches) > 1 {
		return nil, fmt.Errorf("hostname matches %d inventory host_id values", len(hostIDMatches))
	}
	if len(hostIDMatches) == 1 {
		return &hostIDMatches[0], nil
	}
	return nil, nil
}

func hostnameEquivalent(left, right string) bool {
	normalize := func(value string) string {
		return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
	}
	short := func(value string) string {
		if idx := strings.IndexByte(value, '.'); idx > 0 {
			return value[:idx]
		}
		return value
	}
	left = normalize(left)
	right = normalize(right)
	return left != "" && right != "" && (left == right || short(left) == short(right))
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
		facts, err := discoverTargetNICFacts(opts, updated[idx], mgmtItems, items)
		if err != nil {
			return nil, err
		}
		decision := recommendDiscoveredNetwork(updated[idx], facts)
		mgmtItems, items = enrichDiscoveryCandidates(mgmtItems, items, facts, decision)
		mgmtDiscovered[updated[idx].Name] = mgmtItems
		discovered[updated[idx].Name] = items
		applyDiscoveryDecision(&updated[idx], decision)
		fmt.Fprintf(opts.Output, "INFO discover network %s: confidence=%s, %s\n", updated[idx].Name, decision.Confidence, strings.Join(decision.Reasons, "; "))
		fmt.Fprintf(opts.Output, "INFO discover management %s: mgmt_ip=%s\n", updated[idx].Name, updated[idx].Address)
		fmt.Fprintf(opts.Output, "INFO discover RDMA %s: %s\n", updated[idx].Name, describeDiscoveredRDMARecords(updated[idx].RDMA, items))
		if !opts.Confirm && !acceptableAutomaticDecision(decision) {
			return nil, fmt.Errorf("discover network classification for %s is %s (%s); --yes cannot accept an uncertain mapping, rerun interactively and select the management/RDMA bindings", updated[idx].Name, decision.Confidence, strings.Join(decision.Reasons, "; "))
		}
	}
	if opts.Confirm {
		reviewed, err := confirmDiscoveredNetwork(opts, updated, mgmtDiscovered, discovered)
		if err != nil {
			return nil, err
		}
		updated = reviewed
	}
	if opts.DryRun {
		if strings.TrimSpace(opts.InventoryPath) != "" {
			before, after, err := plannedInventoryRDMASlotChange(opts.InventoryPath, updated)
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(opts.Output, "dry-run: inventory RDMA slots: %d -> %d\n", before, after)
		}
		fmt.Fprintf(opts.Output, "dry-run: would update inventory network fields: %s\n", opts.InventoryPath)
		fmt.Fprintln(opts.Output, "dry-run: bundle remains unchanged")
		return updated, nil
	}
	if strings.TrimSpace(opts.InventoryPath) != "" {
		before, after, err := updateDelimitedInventoryRDMAWithChange(opts.InventoryPath, updated)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(opts.Output, "INFO inventory RDMA slots: %d -> %d\n", before, after)
		fmt.Fprintf(opts.Output, "INFO updated inventory network fields: %s\n", opts.InventoryPath)
		fmt.Fprintln(opts.Output, "INFO bundle remains unchanged")
	}
	return updated, nil
}

func plannedInventoryRDMASlotChange(path string, targets []Target) (int, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, fmt.Errorf("read inventory for RDMA preview: %w", err)
	}
	reader := csv.NewReader(bytes.NewReader(data))
	reader.Comma = detectDelimitedInventoryDelimiter(data)
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return 0, 0, fmt.Errorf("parse inventory for RDMA preview: %w", err)
	}
	before := inventoryRDMAHeaderSlots(header)
	after := before
	for _, target := range targets {
		if len(target.RDMA) > after {
			after = len(target.RDMA)
		}
	}
	return before, after, nil
}

func discoverTargetManagementInterfaces(opts DiscoverOptions, target Target) ([]discoveredIPv4Address, error) {
	command := strings.Join([]string{
		"ip -o -4 route show default 2>/dev/null || true",
		"printf '\\n--ADDR--\\n'",
		"ip -o -4 addr show scope global 2>/dev/null || true",
	}, "; ")
	output, err := runNetworkDiscoveryCommand(opts, target, command)
	if err != nil {
		return nil, fmt.Errorf("discover management IP for %s: %w", target.Name, err)
	}
	return parseManagementIPCandidates(output), nil
}

type discoveredIPv4Address struct {
	Iface            string
	IP               string
	Prefix           int
	MAC              string
	Preferred        bool
	Reason           string
	Confidence       string
	MaxSpeedMbps     int
	CurrentSpeedMbps int
	MTU              int
	Model            string
	LinkUp           bool
	LinkKnown        bool
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
		prefix := 0
		for idx, field := range fields {
			if field == "inet" && idx+1 < len(fields) {
				parts := strings.SplitN(fields[idx+1], "/", 2)
				ip = parts[0]
				if len(parts) == 2 {
					prefix, _ = strconv.Atoi(parts[1])
				}
				break
			}
		}
		if !usableDiscoveredIPv4(ip) {
			continue
		}
		addrs = append(addrs, discoveredIPv4Address{Iface: iface, IP: ip, Prefix: prefix, Preferred: defaultIfaces[iface]})
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
	for _, prefix := range []string{"ib", "rdma"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return isVirtualOverlayIface(name)
}

func isVirtualOverlayIface(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, prefix := range []string{"vxlan.", "cali", "flannel", "docker", "veth", "br-", "virbr", "kube", "ovn", "ovs"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func usableDiscoveredIPv4(value string) bool {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil {
		return false
	}
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	return !v4.IsUnspecified() &&
		!v4.IsLoopback() &&
		!v4.IsLinkLocalUnicast() &&
		!v4.IsMulticast() &&
		!v4.Equal(net.IPv4bcast)
}

func discoverTargetRDMAInterfaces(opts DiscoverOptions, target Target) ([]discoveredRDMAInterface, error) {
	output, err := runNetworkDiscoveryCommand(opts, target, "show_gids")
	if err != nil {
		return nil, fmt.Errorf("run show_gids on %s: %w", target.Name, err)
	}
	return parseShowGIDs(output), nil
}

func runNetworkDiscoveryCommand(opts DiscoverOptions, target Target, command string) (string, error) {
	if opts.CommandRunner != nil {
		return opts.CommandRunner(opts.Bundle.Check, target, command)
	}
	return runCommand(opts.Bundle.Check, target, command)
}

func discoverTargetNICFacts(opts DiscoverOptions, target Target, mgmt []discoveredIPv4Address, rdma []discoveredRDMAInterface) ([]nicdetect.Facts, error) {
	output, err := runNetworkDiscoveryCommand(opts, target, nicFactDiscoveryCommand())
	if err != nil {
		return nil, fmt.Errorf("discover NIC hardware facts for %s: %w", target.Name, err)
	}
	facts := parseNICFactDiscovery(output)
	byName := map[string]int{}
	for idx := range facts {
		byName[strings.ToLower(facts[idx].Name)] = idx
	}
	ensure := func(name string) *nicdetect.Facts {
		key := strings.ToLower(strings.TrimSpace(name))
		if idx, ok := byName[key]; ok {
			return &facts[idx]
		}
		facts = append(facts, nicdetect.Facts{Name: strings.TrimSpace(name)})
		byName[key] = len(facts) - 1
		return &facts[len(facts)-1]
	}
	for _, item := range mgmt {
		fact := ensure(item.Iface)
		fact.Addresses = appendUniqueNICAddress(fact.Addresses, nicdetect.Address{IP: item.IP, Prefix: item.Prefix})
		fact.DefaultRoute = fact.DefaultRoute || item.Preferred
		fact.ControlAddress = fact.ControlAddress || normalizedIPv4(targetControlAddress(target)) == normalizedIPv4(item.IP)
	}
	for _, item := range rdma {
		fact := ensure(item.Name)
		fact.HasRDMA = true
		fact.IBDevice = item.IBDevice
		fact.Addresses = appendUniqueNICAddress(fact.Addresses, nicdetect.Address{IP: item.IP, Prefix: item.Prefix})
	}
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].PCI != facts[j].PCI {
			return facts[i].PCI < facts[j].PCI
		}
		return facts[i].Name < facts[j].Name
	})
	return facts, nil
}

func nicFactDiscoveryCommand() string {
	return `for p in /sys/class/net/*; do
  n=${p##*/}
  [ "$n" = "lo" ] && continue
  mac=$(cat "$p/address" 2>/dev/null || true)
  dev=$(readlink -f "$p/device" 2>/dev/null || true)
  pci=""; driver=""; vendor=""; device=""
  if [ -n "$dev" ]; then
    pci=${dev##*/}
    driver_path=$(readlink -f "$dev/driver" 2>/dev/null || true)
    [ -n "$driver_path" ] && driver=${driver_path##*/}
    vendor=$(cat "$dev/vendor" 2>/dev/null || true)
    device=$(cat "$dev/device" 2>/dev/null || true)
  fi
  current=$(cat "$p/speed" 2>/dev/null || true)
  mtu=$(cat "$p/mtu" 2>/dev/null || true)
  carrier=$(cat "$p/carrier" 2>/dev/null || true)
  oper=$(cat "$p/operstate" 2>/dev/null || true)
  phys=$(cat "$p/phys_port_name" 2>/dev/null || true)
  devport=$(cat "$p/dev_port" 2>/dev/null || true)
  max=$(ethtool "$n" 2>/dev/null | awk '/Supported link modes:/{seen=1} seen{for(i=1;i<=NF;i++){if(match($i,/^[0-9]+base/)){v=substr($i,RSTART,RLENGTH-4)+0;if(v>m)m=v}}} /^Supported pause frame use:/{seen=0} END{print m+0}')
  printf 'NIC|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s\n' "$n" "$mac" "$pci" "$driver" "$vendor" "$device" "$current" "$max" "$mtu" "$carrier" "$oper" "$phys" "$devport"
done`
}

func parseNICFactDiscovery(output string) []nicdetect.Facts {
	var facts []nicdetect.Facts
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "|")
		if len(fields) != 14 || fields[0] != "NIC" || strings.TrimSpace(fields[1]) == "" {
			continue
		}
		current, _ := positiveInt(fields[7])
		maximum, _ := positiveInt(fields[8])
		mtu, _ := positiveInt(fields[9])
		devPort, hasDevPort := nonNegativeInt(fields[13])
		linkKnown := strings.TrimSpace(fields[10]) == "0" || strings.TrimSpace(fields[10]) == "1"
		linkUp := strings.TrimSpace(fields[10]) == "1"
		vendor := strings.TrimSpace(fields[5])
		device := strings.TrimSpace(fields[6])
		model := strings.Trim(strings.Join([]string{vendor, device}, ":"), ":")
		facts = append(facts, nicdetect.Facts{
			Name:             strings.TrimSpace(fields[1]),
			MAC:              strings.TrimSpace(fields[2]),
			PCI:              strings.TrimSpace(fields[3]),
			Driver:           strings.TrimSpace(fields[4]),
			VendorID:         vendor,
			DeviceID:         device,
			Model:            model,
			CurrentSpeedMbps: current,
			MaxSpeedMbps:     maximum,
			MTU:              mtu,
			LinkKnown:        linkKnown,
			LinkUp:           linkUp,
			PhysPortName:     strings.TrimSpace(fields[12]),
			DevPort:          devPort,
			HasDevPort:       hasDevPort,
		})
	}
	return facts
}

func positiveInt(raw string) (int, bool) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}

func nonNegativeInt(raw string) (int, bool) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return 0, false
	}
	return value, true
}

func appendUniqueNICAddress(items []nicdetect.Address, candidate nicdetect.Address) []nicdetect.Address {
	for _, item := range items {
		if normalizedIPv4(item.IP) == normalizedIPv4(candidate.IP) {
			return items
		}
	}
	return append(items, candidate)
}

func normalizedIPv4(value string) string {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil || ip.To4() == nil {
		return ""
	}
	return ip.To4().String()
}

func recommendDiscoveredNetwork(target Target, facts []nicdetect.Facts) nicdetect.Decision {
	plan := nicdetect.Plan{
		ManagementCount:    1,
		RDMACount:          len(target.RDMA),
		AllowRDMAExpansion: true,
		ManagementHints:    []nicdetect.SlotHint{{Index: 0, IP: target.Address}},
	}
	for idx, item := range target.RDMA {
		prefix, _ := strconv.Atoi(strings.TrimSpace(item.Prefix))
		plan.RDMAHints = append(plan.RDMAHints, nicdetect.SlotHint{Index: idx, Name: item.Name, MAC: item.MAC, IP: item.IP, Prefix: prefix})
	}
	return nicdetect.Recommend(plan, facts)
}

func acceptableAutomaticDecision(decision nicdetect.Decision) bool {
	return (decision.Confidence == nicdetect.ConfidenceExact || decision.Confidence == nicdetect.ConfidenceStrong) && len(decision.Management) == 1 && len(decision.RDMA) > 0
}

func applyDiscoveryDecision(target *Target, decision nicdetect.Decision) {
	if len(decision.Management) == 1 {
		if address, ok := preferredFactAddress(decision.Management[0].NIC); ok {
			target.Address = address.IP
		}
	}
	if len(decision.RDMA) == 0 {
		return
	}
	records := make([]spec.RDMARecord, 0, len(decision.RDMA))
	for _, binding := range decision.RDMA {
		address, ok := preferredFactAddress(binding.NIC)
		if !ok {
			continue
		}
		records = append(records, spec.RDMARecord{Name: binding.NIC.Name, MAC: binding.NIC.MAC, IP: address.IP, Prefix: strconv.Itoa(address.Prefix)})
	}
	if len(records) == len(decision.RDMA) {
		target.RDMA = records
	}
}

func preferredFactAddress(fact nicdetect.Facts) (nicdetect.Address, bool) {
	for _, address := range fact.Addresses {
		if normalizedIPv4(address.IP) != "" {
			return address, true
		}
	}
	return nicdetect.Address{}, false
}

func enrichDiscoveryCandidates(mgmt []discoveredIPv4Address, rdma []discoveredRDMAInterface, facts []nicdetect.Facts, decision nicdetect.Decision) ([]discoveredIPv4Address, []discoveredRDMAInterface) {
	factByName := map[string]nicdetect.Facts{}
	for _, fact := range facts {
		factByName[strings.ToLower(fact.Name)] = fact
	}
	mgmtBinding := map[string]nicdetect.Binding{}
	for _, binding := range decision.Management {
		mgmtBinding[strings.ToLower(binding.NIC.Name)] = binding
	}
	rdmaBinding := map[string]nicdetect.Binding{}
	for _, binding := range decision.RDMA {
		rdmaBinding[strings.ToLower(binding.NIC.Name)] = binding
	}
	for idx := range mgmt {
		fact := factByName[strings.ToLower(mgmt[idx].Iface)]
		copyFactToManagementCandidate(&mgmt[idx], fact)
		if binding, ok := mgmtBinding[strings.ToLower(mgmt[idx].Iface)]; ok {
			mgmt[idx].Reason = binding.Reason
			mgmt[idx].Confidence = binding.Confidence
		} else {
			mgmt[idx].Reason = "available IPv4 interface"
			mgmt[idx].Confidence = decision.Confidence
		}
	}
	for idx := range rdma {
		fact := factByName[strings.ToLower(rdma[idx].Name)]
		copyFactToRDMACandidate(&rdma[idx], fact)
		if binding, ok := rdmaBinding[strings.ToLower(rdma[idx].Name)]; ok {
			rdma[idx].Reason = binding.Reason
			rdma[idx].Confidence = binding.Confidence
		} else {
			rdma[idx].Reason = "RDMA-capable candidate"
			rdma[idx].Confidence = decision.Confidence
		}
	}
	return mgmt, rdma
}

func copyFactToManagementCandidate(candidate *discoveredIPv4Address, fact nicdetect.Facts) {
	candidate.MAC = fact.MAC
	candidate.MaxSpeedMbps = fact.MaxSpeedMbps
	candidate.CurrentSpeedMbps = fact.CurrentSpeedMbps
	candidate.MTU = fact.MTU
	candidate.Model = fact.Model
	candidate.LinkUp = fact.LinkUp
	candidate.LinkKnown = fact.LinkKnown
}

func copyFactToRDMACandidate(candidate *discoveredRDMAInterface, fact nicdetect.Facts) {
	candidate.MAC = fact.MAC
	candidate.MaxSpeedMbps = fact.MaxSpeedMbps
	candidate.CurrentSpeedMbps = fact.CurrentSpeedMbps
	candidate.MTU = fact.MTU
	candidate.Model = fact.Model
	candidate.LinkUp = fact.LinkUp
	candidate.LinkKnown = fact.LinkKnown
	for _, address := range fact.Addresses {
		if normalizedIPv4(address.IP) == normalizedIPv4(candidate.IP) {
			candidate.Prefix = address.Prefix
			break
		}
	}
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
			if parsed := net.ParseIP(field); parsed != nil && usableDiscoveredIPv4(field) {
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
	return isVirtualOverlayIface(name)
}

func ipv4SortKey(value string) uint32 {
	ip := net.ParseIP(strings.TrimSpace(value)).To4()
	if ip == nil {
		return 0
	}
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}

func describeDiscoveredRDMARecords(records []spec.RDMARecord, candidates []discoveredRDMAInterface) string {
	byName := map[string]discoveredRDMAInterface{}
	for _, item := range candidates {
		byName[item.Name] = item
	}
	parts := make([]string, 0, len(records))
	for idx, record := range records {
		candidate := byName[record.Name]
		parts = append(parts, fmt.Sprintf("rdma%d=%s/%s(%s,%s)", idx+1, record.Name, record.IP, candidate.IBDevice, candidate.Reason))
	}
	return strings.Join(parts, ", ")
}

func prefixString(prefix int) string {
	if prefix <= 0 {
		return ""
	}
	return strconv.Itoa(prefix)
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
	_, _, err := updateDelimitedInventoryRDMAWithChange(path, targets)
	return err
}

func updateDelimitedInventoryRDMAWithChange(path string, targets []Target) (int, int, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".csv" && ext != ".tsv" && ext != ".txt" {
		return 0, 0, fmt.Errorf("discover inventory write-back supports only .csv/.tsv/.txt, got %s", ext)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, fmt.Errorf("read inventory for RDMA update: %w", err)
	}
	delimiter := detectDelimitedInventoryDelimiter(data)
	reader := csv.NewReader(bytes.NewReader(data))
	reader.Comma = delimiter
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	rows, err := reader.ReadAll()
	if err != nil {
		return 0, 0, fmt.Errorf("parse inventory for RDMA update: %w", err)
	}
	if len(rows) == 0 {
		return 0, 0, fmt.Errorf("inventory is empty")
	}
	targetByKey, err := targetIndexByInventoryKey(targets)
	if err != nil {
		return 0, 0, err
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
	beforeSlots := inventoryRDMAHeaderSlots(header)
	maxRDMA := 0
	for _, target := range targets {
		if len(target.RDMA) > maxRDMA {
			maxRDMA = len(target.RDMA)
		}
	}
	fieldLayout := inventoryRDMAFieldLayout(header)
	for idx := 1; idx <= maxRDMA; idx++ {
		for _, field := range fieldLayout {
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
			return 0, 0, fmt.Errorf("inventory conflict: target %s matches multiple rows (%d and %d); remove the duplicate row before discover write-back", targets[targetIndex].Name, previousRow+1, rowIdx+1)
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
		return 0, 0, fmt.Errorf("encode inventory RDMA update: %w", err)
	}
	if err := atomicWriteInventory(path, out.Bytes()); err != nil {
		return 0, 0, err
	}
	afterSlots := beforeSlots
	if maxRDMA > afterSlots {
		afterSlots = maxRDMA
	}
	return beforeSlots, afterSlots, nil
}

func inventoryRDMAHeaderSlots(header []string) int {
	maxSlot := 0
	for _, raw := range header {
		slot, ok := inventoryRDMASlot(canonicalInventoryHeader(raw))
		if ok && slot > maxSlot {
			maxSlot = slot
		}
	}
	return maxSlot
}

func inventoryRDMAFieldLayout(header []string) []string {
	fields := []string{"name", "ip"}
	seen := map[string]bool{"name": true, "ip": true}
	firstSlot := 0
	for _, raw := range header {
		key := canonicalInventoryHeader(raw)
		slot, field, ok := inventoryRDMAField(key)
		if !ok {
			continue
		}
		if firstSlot == 0 {
			firstSlot = slot
		}
		if slot != firstSlot || seen[field] {
			continue
		}
		if isSupportedInventoryRDMAField(field) {
			fields = append(fields, field)
			seen[field] = true
		}
	}
	return fields
}

func inventoryRDMAField(key string) (int, string, bool) {
	slot, ok := inventoryRDMASlot(key)
	if !ok {
		return 0, "", false
	}
	prefix := fmt.Sprintf("rdma%d_", slot)
	field := strings.TrimPrefix(key, prefix)
	return slot, field, field != "" && field != key
}

func isSupportedInventoryRDMAField(field string) bool {
	switch field {
	case "name", "ip", "prefix", "gateway", "mac", "table", "route_cidr":
		return true
	default:
		return false
	}
}

func atomicWriteInventory(path string, data []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat inventory before write-back: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".envinit-inventory-*")
	if err != nil {
		return fmt.Errorf("create temporary inventory: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(info.Mode().Perm()); err != nil {
		_ = temp.Close()
		return fmt.Errorf("set temporary inventory permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write temporary inventory: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync temporary inventory: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary inventory: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace inventory atomically: %w", err)
	}
	return nil
}

func targetIndexByInventoryKey(targets []Target) (map[string]int, error) {
	out := map[string]int{}
	for idx, target := range targets {
		keys := []string{target.InventoryIdentity, target.Input}
		if !target.ExplicitIdentity && target.InventoryMatched {
			keys = append(keys, target.Name, target.Address, target.ExpectedHostname, target.DiscoveredHostname)
		} else if !target.ExplicitIdentity {
			// A bare SSH endpoint whose remote hostname did not match inventory is
			// intentionally a new row. Do not let a newly discovered management IP
			// redirect write-back into an unrelated existing row.
			keys = []string{target.InventoryIdentity, target.DiscoveredHostname, target.Name}
		}
		for _, key := range keys {
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
	hostID := firstNonEmpty(target.InventoryIdentity, target.Input, target.Name, target.ExpectedHostname, target.Address)
	hostname := firstNonEmpty(target.DiscoveredHostname, target.ExpectedHostname, target.Name, target.Input)
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
		values := map[string]string{
			"name":       item.Name,
			"ip":         item.IP,
			"prefix":     item.Prefix,
			"gateway":    item.Gateway,
			"mac":        item.MAC,
			"table":      item.Table,
			"route_cidr": item.RouteCIDR,
		}
		for field, value := range values {
			column, ok := headerIndex[fmt.Sprintf("rdma%d_%s", idx+1, field)]
			if ok && column < len(row) && strings.TrimSpace(value) != "" {
				row[column] = strings.TrimSpace(value)
			}
		}
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
