package checker

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"envinit/internal/spec"
)

const xdrMmapOffsetBase uint64 = 0x90001000

type xpuTopology struct {
	NICDevices map[string]string
	Links      map[int]map[string]string
}

func resolveXDRTopologyGroups(opts Options, targets []Target, groupsByTarget resolvedRDMAGroups) (resolvedRDMAGroups, error) {
	if strings.TrimSpace(opts.Bundle.Check.Bandwidth.MmapDevice) == "" {
		return groupsByTarget, nil
	}
	if opts.DryRun {
		completeLegacyOffsets := true
		for _, groups := range groupsByTarget {
			for _, group := range groups {
				if len(group.XPUOffsets) == 0 {
					completeLegacyOffsets = false
				}
			}
		}
		if completeLegacyOffsets {
			fmt.Fprintln(opts.Output, "dry-run xdr topology: using compatibility xpu_offsets from bundle")
			return groupsByTarget, nil
		}
		fmt.Fprintln(opts.Output, "dry-run discovery xdr topology: executing read-only xpu-smi topo -m on each target")
	}

	resolved := resolvedRDMAGroups{}
	for _, target := range targets {
		groups := append([]spec.CheckRDMAGroup(nil), groupsByTarget[target.Name]...)
		output, err := runDiscoveryCommand(opts, target, "xpu-smi topo -m")
		if err != nil {
			return nil, fmt.Errorf("resolve xdr topology for %s: %w", target.Name, err)
		}
		topology, err := parseXPUTopology(output)
		if err != nil {
			return nil, fmt.Errorf("resolve xdr topology for %s: %w", target.Name, err)
		}
		groups, assignments, err := assignXPUOffsetsByTopology(groups, topology)
		if err != nil {
			return nil, fmt.Errorf("resolve xdr topology for %s: %w", target.Name, err)
		}
		for idx, group := range groups {
			fmt.Fprintf(opts.Output, "INFO xdr topology: %s rdma%d ib_device=%s xpus=%s offsets=%s\n",
				target.Name, idx+1, group.IBDevice, joinXPUAssignments(assignments[idx]), strings.Join(group.XPUOffsets, ","))
			for _, offset := range group.XPUOffsets {
				link := group.XPUTopologyLinks[offset]
				if topologyLinkDegraded(link) {
					fmt.Fprintf(opts.Output, "WARN xdr topology degraded: %s rdma%d ib_device=%s offset=%s link=%s; PIX is unavailable, bandwidth may be limited by the PCIe/NUMA path\n",
						target.Name, idx+1, group.IBDevice, offset, link)
				}
			}
		}
		resolved[target.Name] = groups
	}
	return resolved, nil
}

func parseXPUTopology(output string) (xpuTopology, error) {
	topology := xpuTopology{NICDevices: map[string]string{}, Links: map[int]map[string]string{}}
	var headers []string
	var nicHeaders []string
	var directDeviceRows []string
	directDeviceRowSeen := map[string]bool{}
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		rawFields := strings.Fields(line)
		fields := make([]string, len(rawFields))
		for idx, field := range rawFields {
			fields[idx] = sanitizeTopologyToken(field)
		}
		if len(fields) == 0 {
			continue
		}
		if len(headers) == 0 && strings.HasPrefix(fields[0], "XPU") && len(fields) > 1 {
			for _, field := range fields {
				if strings.EqualFold(field, "CPU") || strings.EqualFold(field, "NUMA") {
					break
				}
				headers = append(headers, field)
				if _, ok := parseIndexedName(field, "NIC"); ok {
					nicHeaders = append(nicHeaders, field)
				}
				if isDirectIBDeviceHeader(field) {
					topology.NICDevices[field] = field
				}
			}
			continue
		}
		if len(headers) > 0 && strings.HasPrefix(fields[0], "XPU") {
			xpuIndex, ok := parseIndexedName(fields[0], "XPU")
			if !ok || len(fields) < len(headers)+1 {
				continue
			}
			links := map[string]string{}
			for column, header := range headers {
				if strings.HasPrefix(header, "NIC") || isDirectIBDeviceHeader(header) {
					links[header] = strings.ToUpper(sanitizeTopologyToken(fields[column+1]))
				}
			}
			topology.Links[xpuIndex] = links
			continue
		}
		if isDirectIBDeviceHeader(fields[0]) && !directDeviceRowSeen[fields[0]] {
			directDeviceRowSeen[fields[0]] = true
			directDeviceRows = append(directDeviceRows, fields[0])
		}
		if len(fields) >= 2 && strings.HasPrefix(fields[0], "NIC") {
			nic := strings.TrimSuffix(fields[0], ":")
			if _, ok := parseIndexedName(nic, "NIC"); ok {
				device := sanitizeTopologyToken(fields[1])
				if isDirectIBDeviceHeader(device) {
					topology.NICDevices[nic] = device
				}
			}
		}
	}
	if len(topology.NICDevices) == 0 && len(nicHeaders) > 0 && len(nicHeaders) == len(directDeviceRows) {
		for idx, nic := range nicHeaders {
			topology.NICDevices[nic] = directDeviceRows[idx]
		}
	}
	if len(headers) == 0 || len(topology.Links) == 0 {
		return xpuTopology{}, fmt.Errorf("cannot find XPU/NIC topology matrix in xpu-smi output")
	}
	if len(topology.NICDevices) == 0 {
		return xpuTopology{}, fmt.Errorf("cannot find NIC columns or NIC legend in xpu-smi output; matrix_headers=%s direct_device_rows=%s", strings.Join(headers, ","), strings.Join(directDeviceRows, ","))
	}
	return topology, nil
}

func sanitizeTopologyToken(value string) string {
	var cleaned strings.Builder
	for idx := 0; idx < len(value); {
		if value[idx] != 0x1b {
			cleaned.WriteByte(value[idx])
			idx++
			continue
		}
		idx++
		if idx < len(value) && value[idx] == '[' {
			idx++
			for idx < len(value) {
				char := value[idx]
				idx++
				if char >= 0x40 && char <= 0x7e {
					break
				}
			}
		}
	}
	return strings.Trim(strings.TrimSpace(cleaned.String()), "|,:;")
}

func isDirectIBDeviceHeader(value string) bool {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "mlx5_") || len(value) <= len("mlx5_") {
		return false
	}
	hasDigit := false
	for _, char := range strings.TrimPrefix(value, "mlx5_") {
		if (char < '0' || char > '9') && char != '_' {
			return false
		}
		if char >= '0' && char <= '9' {
			hasDigit = true
		}
	}
	return hasDigit
}

func assignXPUOffsetsByTopology(groups []spec.CheckRDMAGroup, topology xpuTopology) ([]spec.CheckRDMAGroup, map[int][]int, error) {
	deviceToNIC := map[string]string{}
	for nic, device := range topology.NICDevices {
		deviceToNIC[strings.TrimSpace(device)] = nic
	}
	groupNICs := make([]string, len(groups))
	for idx, group := range groups {
		device := strings.TrimSpace(group.IBDevice)
		nic, ok := deviceToNIC[device]
		if !ok {
			return nil, nil, fmt.Errorf("ib device %s is absent from xpu-smi topology NIC columns/mapping", device)
		}
		groupNICs[idx] = nic
		groups[idx].XPUOffsets = nil
		groups[idx].XPUTopologyLinks = map[string]string{}
	}

	assignments := map[int][]int{}
	xpuIndexes := make([]int, 0, len(topology.Links))
	for xpu := range topology.Links {
		xpuIndexes = append(xpuIndexes, xpu)
	}
	sort.Ints(xpuIndexes)
	for _, xpu := range xpuIndexes {
		bestRank := int(^uint(0) >> 1)
		var closest []int
		for groupIndex, nic := range groupNICs {
			rank, ok := topologyLinkRank(topology.Links[xpu][nic])
			if !ok {
				continue
			}
			if rank < bestRank {
				bestRank = rank
				closest = []int{groupIndex}
			} else if rank == bestRank {
				closest = append(closest, groupIndex)
			}
		}
		for _, groupIndex := range closest {
			offset := xdrMmapOffsetForXPU(xpu)
			link := strings.ToUpper(strings.TrimSpace(topology.Links[xpu][groupNICs[groupIndex]]))
			groups[groupIndex].XPUOffsets = append(groups[groupIndex].XPUOffsets, offset)
			groups[groupIndex].XPUTopologyLinks[offset] = link
			assignments[groupIndex] = append(assignments[groupIndex], xpu)
		}
	}
	for _, group := range groups {
		if len(group.XPUOffsets) == 0 {
			return nil, nil, fmt.Errorf("ib device %s has no closest XPU in topology", group.IBDevice)
		}
	}
	return groups, assignments, nil
}

func topologyLinkRank(link string) (int, bool) {
	switch strings.ToUpper(strings.TrimSpace(link)) {
	case "PIX":
		return 0, true
	case "PXB":
		return 1, true
	case "PHB":
		return 2, true
	case "NODE":
		return 3, true
	case "SYS":
		return 4, true
	default:
		return 0, false
	}
}

func topologyLinkDegraded(link string) bool {
	link = strings.ToUpper(strings.TrimSpace(link))
	return link != "" && link != "PIX"
}

func groupTopologyLink(group spec.CheckRDMAGroup, offset string) string {
	if group.XPUTopologyLinks == nil {
		return ""
	}
	return strings.ToUpper(strings.TrimSpace(group.XPUTopologyLinks[strings.TrimSpace(offset)]))
}

func xdrMmapOffsetForXPU(index int) string {
	return fmt.Sprintf("0x%016x", (uint64(index)<<60)+xdrMmapOffsetBase)
}

func parseIndexedName(value, prefix string) (int, bool) {
	if !strings.HasPrefix(value, prefix) {
		return 0, false
	}
	index, err := strconv.Atoi(strings.TrimPrefix(value, prefix))
	return index, err == nil && index >= 0
}

func joinXPUAssignments(indexes []int) string {
	values := make([]string, len(indexes))
	for idx, xpu := range indexes {
		values[idx] = fmt.Sprintf("XPU%d", xpu)
	}
	return strings.Join(values, ",")
}
