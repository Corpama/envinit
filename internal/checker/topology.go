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
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(headers) == 0 && strings.HasPrefix(fields[0], "XPU") && len(fields) > 1 {
			for _, field := range fields {
				if !strings.HasPrefix(field, "XPU") && !strings.HasPrefix(field, "NIC") {
					break
				}
				headers = append(headers, field)
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
				if strings.HasPrefix(header, "NIC") {
					links[header] = strings.ToUpper(fields[column+1])
				}
			}
			topology.Links[xpuIndex] = links
			continue
		}
		if len(fields) >= 2 && strings.HasPrefix(fields[0], "NIC") && strings.HasSuffix(fields[0], ":") {
			nic := strings.TrimSuffix(fields[0], ":")
			if _, ok := parseIndexedName(nic, "NIC"); ok {
				topology.NICDevices[nic] = fields[1]
			}
		}
	}
	if len(headers) == 0 || len(topology.Links) == 0 {
		return xpuTopology{}, fmt.Errorf("cannot find XPU/NIC topology matrix in xpu-smi output")
	}
	if len(topology.NICDevices) == 0 {
		return xpuTopology{}, fmt.Errorf("cannot find NIC legend in xpu-smi output")
	}
	return topology, nil
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
			return nil, nil, fmt.Errorf("ib device %s is absent from xpu-smi NIC legend", device)
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
