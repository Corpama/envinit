package runner

import "fmt"

// Order fallback candidates using stable hardware identity rather than the
// order returned by iproute. This is only a presentation/selection order: it
// must never be treated as proof that a planned address belongs to a port.
func hardwareOrderProbeCandidates(candidates []probeLink, physical map[string]netDevice) []probeLink {
	byName := make(map[string]probeLink, len(candidates))
	for _, candidate := range candidates {
		byName[candidate.Name] = candidate
	}
	orderedDevices := make([]netDevice, 0, len(candidates))
	for _, candidate := range candidates {
		if device, ok := physical[candidate.Name]; ok {
			orderedDevices = append(orderedDevices, device)
		}
	}
	sortNetDevicesPhysical(orderedDevices)
	ordered := make([]probeLink, 0, len(orderedDevices))
	for _, device := range orderedDevices {
		if candidate, ok := byName[device.Name]; ok {
			ordered = append(ordered, candidate)
		}
	}
	return ordered
}

// applyHardwareOrderProbeFallback fills review candidates without claiming
// that the planned address was observed on that NIC. It deliberately keeps
// the slot order and never reuses a physical candidate.
func applyHardwareOrderProbeFallback(bindings []interfaceBinding, slots []int, candidates []probeLink) {
	for row, idx := range slots {
		if row < len(candidates) {
			bindings[idx].MAC = candidates[row].MAC
			bindings[idx].CurrentName = candidates[row].Name
		}
		bindings[idx].NeedsReview = true
		bindings[idx].Reason = "hardware order fallback; connectivity unverified"
		bindings[idx].Confidence = "ambiguous"
	}
}

func probeFallbackWarning(edges [][]bool, allCandidates, probeCandidates int, probeEvidenceIncomplete bool) string {
	if allCandidates == 0 {
		return "RDMA gateway probe found no selectable physical NIC; fell back to PCI/hardware order where possible"
	}
	if probeCandidates == 0 {
		return "RDMA gateway probe could not test any candidate NIC (existing address/route or link state); fell back to PCI/hardware order"
	}
	if allCandidates != probeCandidates {
		return fmt.Sprintf("RDMA gateway probe evidence incomplete (%d of %d physical NICs tested); fell back to PCI/hardware order", probeCandidates, allCandidates)
	}
	if probeEvidenceIncomplete {
		return "RDMA gateway probe evidence incomplete because one or more candidates did not complete both ARP and ICMP; fell back to PCI/hardware order"
	}
	anyReachable := false
	for _, row := range edges {
		for _, reachable := range row {
			anyReachable = anyReachable || reachable
		}
	}
	if !anyReachable {
		return "RDMA gateway probe: gateway unreachable from all probe candidates; fell back to PCI/hardware order"
	}
	return "RDMA gateway probe did not produce a unique NIC mapping; multiple physical NICs share the connectivity evidence, so it fell back to PCI/hardware order"
}

// Find a complete matching, then remove each chosen edge to test uniqueness.
// Augmenting paths avoid factorial searches when many NICs share connectivity.
func uniqueProbeMapping(edges [][]bool) ([]int, bool) {
	if len(edges) == 0 {
		return nil, true
	}
	width := len(edges[0])
	for _, row := range edges {
		if len(row) != width {
			return nil, false
		}
	}
	if len(edges) > width {
		return nil, false
	}
	match := func(skipRow, skipCol int) ([]int, bool) {
		owners := make([]int, width)
		for i := range owners {
			owners[i] = -1
		}
		var augment func(int, []bool) bool
		augment = func(row int, seen []bool) bool {
			for col, ok := range edges[row] {
				if !ok || seen[col] || (row == skipRow && col == skipCol) {
					continue
				}
				seen[col] = true
				if owners[col] == -1 || augment(owners[col], seen) {
					owners[col] = row
					return true
				}
			}
			return false
		}
		for row := range edges {
			if !augment(row, make([]bool, width)) {
				return nil, false
			}
		}
		result := make([]int, len(edges))
		for col, row := range owners {
			if row >= 0 {
				result[row] = col
			}
		}
		return result, true
	}
	result, ok := match(-1, -1)
	if !ok {
		return nil, false
	}
	for row, col := range result {
		if _, ok := match(row, col); ok {
			return nil, false
		}
	}
	return result, true
}
