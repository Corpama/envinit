package nicdetect

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

type Role string

const (
	RoleManagement Role = "mgmt"
	RoleRDMA       Role = "rdma"

	ConfidenceExact     = "exact"
	ConfidenceStrong    = "strong"
	ConfidenceWeak      = "weak"
	ConfidenceAmbiguous = "ambiguous"
	ConfidenceConflict  = "conflict"
)

type Address struct {
	IP     string
	Prefix int
}

type Facts struct {
	Name                      string
	MAC                       string
	PCI                       string
	Driver                    string
	VendorID                  string
	DeviceID                  string
	Model                     string
	CurrentSpeedMbps          int
	MaxSpeedMbps              int
	MTU                       int
	LinkUp                    bool
	LinkKnown                 bool
	HasRDMA                   bool
	IBDevice                  string
	PhysPortName              string
	DevPort                   int
	HasDevPort                bool
	Addresses                 []Address
	DefaultRoute              bool
	ControlAddress            bool
	ManagementNetworkEvidence bool
	RDMANetworkEvidence       bool
}

type SlotHint struct {
	Index  int
	Name   string
	MAC    string
	IP     string
	Prefix int
}

type Plan struct {
	ManagementCount    int
	RDMACount          int
	ManagementHints    []SlotHint
	RDMAHints          []SlotHint
	AllowRDMAExpansion bool
}

type Binding struct {
	Role       Role
	Slot       int
	NIC        Facts
	Reason     string
	Confidence string
}

type Decision struct {
	Management []Binding
	RDMA       []Binding
	Unassigned []Facts
	Confidence string
	Reasons    []string
	Conflicts  []string
}

func Recommend(plan Plan, input []Facts) Decision {
	facts := normalizeFacts(input)
	annotateNetworkEvidence(plan, facts)
	decision := Decision{Confidence: ConfidenceStrong}
	used := map[string]Role{}

	managementCount := plan.ManagementCount
	if managementCount < 0 {
		managementCount = 0
	}
	rdmaCount := plan.RDMACount
	if rdmaCount < 0 {
		rdmaCount = 0
	}

	decision.Management = exactBindings(RoleManagement, managementCount, plan.ManagementHints, facts, used, &decision)
	decision.RDMA = exactBindings(RoleRDMA, rdmaCount, plan.RDMAHints, facts, used, &decision)
	if len(decision.Conflicts) > 0 {
		decision.Confidence = ConfidenceConflict
	}

	remaining := unusedFacts(facts, used)
	rdmaNeed := rdmaCount - len(decision.RDMA)
	if rdmaNeed < 0 {
		rdmaNeed = 0
	}
	mgmtNeed := managementCount - len(decision.Management)
	if mgmtNeed < 0 {
		mgmtNeed = 0
	}

	selectedRDMA, rdmaReason, rdmaAmbiguous := selectRDMAGroup(remaining, rdmaNeed, mgmtNeed, plan.AllowRDMAExpansion)
	if rdmaAmbiguous {
		decision.Reasons = append(decision.Reasons, rdmaReason)
		if decision.Confidence != ConfidenceConflict {
			decision.Confidence = ConfidenceAmbiguous
		}
	} else if len(selectedRDMA) > 0 {
		slots := openSlots(rdmaCount, plan.RDMAHints, decision.RDMA)
		if plan.AllowRDMAExpansion {
			for len(slots) < len(selectedRDMA) {
				slots = append(slots, len(slots)+len(decision.RDMA))
			}
		}
		for idx, item := range selectedRDMA {
			if idx >= len(slots) {
				break
			}
			decision.RDMA = append(decision.RDMA, Binding{Role: RoleRDMA, Slot: slots[idx], NIC: item, Reason: rdmaReason, Confidence: ConfidenceStrong})
			used[canonicalName(item.Name)] = RoleRDMA
		}
		remaining = unusedFacts(facts, used)
		decision.Reasons = append(decision.Reasons, rdmaReason)
	}

	selectedMgmt, mgmtReason, mgmtAmbiguous := selectManagementGroup(remaining, mgmtNeed)
	if mgmtAmbiguous {
		decision.Reasons = append(decision.Reasons, mgmtReason)
		if decision.Confidence != ConfidenceConflict {
			decision.Confidence = ConfidenceAmbiguous
		}
	} else if len(selectedMgmt) > 0 {
		slots := openSlots(managementCount, plan.ManagementHints, decision.Management)
		for idx, item := range selectedMgmt {
			if idx >= len(slots) {
				break
			}
			decision.Management = append(decision.Management, Binding{Role: RoleManagement, Slot: slots[idx], NIC: item, Reason: mgmtReason, Confidence: ConfidenceStrong})
			used[canonicalName(item.Name)] = RoleManagement
		}
		decision.Reasons = append(decision.Reasons, mgmtReason)
	}

	sortBindings(decision.Management)
	sortBindings(decision.RDMA)
	decision.Unassigned = unusedFacts(facts, used)
	if decision.Confidence == "" {
		decision.Confidence = ConfidenceStrong
	}
	return decision
}

func exactBindings(role Role, count int, hints []SlotHint, facts []Facts, used map[string]Role, decision *Decision) []Binding {
	var out []Binding
	for _, hint := range hints {
		if hint.Index < 0 || (count > 0 && hint.Index >= count) {
			continue
		}
		item, reason, ok := matchHint(hint, facts)
		if !ok {
			continue
		}
		key := canonicalName(item.Name)
		if previous, exists := used[key]; exists {
			decision.Conflicts = append(decision.Conflicts, fmt.Sprintf("%s is matched to both %s and %s", item.Name, previous, role))
			continue
		}
		used[key] = role
		out = append(out, Binding{Role: role, Slot: hint.Index, NIC: item, Reason: reason, Confidence: ConfidenceExact})
	}
	return out
}

func matchHint(hint SlotHint, facts []Facts) (Facts, string, bool) {
	if value := canonicalMAC(hint.MAC); value != "" {
		for _, item := range facts {
			if canonicalMAC(item.MAC) == value {
				return item, "MAC exact", true
			}
		}
	}
	if value := canonicalName(hint.Name); value != "" {
		for _, item := range facts {
			if canonicalName(item.Name) == value {
				return item, "name exact", true
			}
		}
	}
	if value := normalizedIP(hint.IP); value != "" {
		for _, item := range facts {
			for _, address := range item.Addresses {
				if normalizedIP(address.IP) == value {
					return item, "IP exact", true
				}
			}
		}
	}
	return Facts{}, "", false
}

type factGroup struct {
	Key      string
	Facts    []Facts
	Speed    int
	MTU      int
	Links    int
	Evidence int
}

func selectRDMAGroup(facts []Facts, need int, mgmtNeed int, allowExpansion bool) ([]Facts, string, bool) {
	if need == 0 && !allowExpansion {
		return nil, "", false
	}
	groups := groupFacts(facts, true)
	// A freshly installed host may not expose an RDMA device until OFED is
	// installed and loaded. In that state the hardware model and maximum link
	// speed still provide useful grouping evidence, so do not make the runtime
	// RDMA capability bit a prerequisite.
	if len(groups) == 0 {
		groups = groupFacts(facts, false)
	}
	if need > 0 && !hasExactSizedGroup(groups, need, mgmtNeed) {
		if refined := supportingRDMAGroupCandidates(groups, need, mgmtNeed); len(refined) > 0 {
			groups = refined
			allowExpansion = false
		}
	}
	var candidates []factGroup
	for _, group := range groups {
		if len(group.Facts) == 0 || len(facts)-len(group.Facts) < mgmtNeed {
			continue
		}
		if allowExpansion {
			if need > 0 && len(group.Facts) < need {
				continue
			}
		} else if len(group.Facts) != need {
			continue
		}
		candidates = append(candidates, group)
	}
	if len(candidates) == 0 {
		if need > 0 {
			return nil, fmt.Sprintf("no unique hardware group matches %d RDMA slot(s)", need), true
		}
		return nil, "", false
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Speed != candidates[j].Speed {
			return candidates[i].Speed > candidates[j].Speed
		}
		if candidates[i].Evidence != candidates[j].Evidence {
			return candidates[i].Evidence > candidates[j].Evidence
		}
		if candidates[i].Links != candidates[j].Links {
			return candidates[i].Links > candidates[j].Links
		}
		if len(candidates[i].Facts) != len(candidates[j].Facts) {
			return len(candidates[i].Facts) > len(candidates[j].Facts)
		}
		return candidates[i].Key < candidates[j].Key
	})
	if len(candidates) > 1 && candidates[0].Speed == candidates[1].Speed && candidates[0].Evidence == candidates[1].Evidence && candidates[0].Links == candidates[1].Links && len(candidates[0].Facts) == len(candidates[1].Facts) {
		return nil, fmt.Sprintf("multiple equivalent RDMA groups match %d slot(s)", need), true
	}
	selected := append([]Facts(nil), candidates[0].Facts...)
	sortFactsPhysical(selected)
	reason := fmt.Sprintf("%dx%s exact group", len(selected), speedLabel(candidates[0].Speed))
	return selected, reason, false
}

func hasExactSizedGroup(groups []factGroup, need int, mgmtNeed int) bool {
	total := 0
	for _, group := range groups {
		total += len(group.Facts)
	}
	for _, group := range groups {
		if len(group.Facts) == need && total-len(group.Facts) >= mgmtNeed {
			return true
		}
	}
	return false
}

func selectManagementGroup(facts []Facts, need int) ([]Facts, string, bool) {
	if need == 0 {
		return nil, "", false
	}
	if len(facts) == need {
		selected := append([]Facts(nil), facts...)
		sortFactsPhysical(selected)
		return selected, fmt.Sprintf("%d NIC(s) remaining", need), false
	}
	groups := groupFacts(facts, false)
	var candidates []factGroup
	for _, group := range groups {
		if len(group.Facts) == need {
			candidates = append(candidates, group)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Sprintf("no unique hardware group matches %d management slot(s)", need), true
	}
	sort.Slice(candidates, func(i, j int) bool {
		leftEvidence := managementEvidence(candidates[i])
		rightEvidence := managementEvidence(candidates[j])
		if leftEvidence != rightEvidence {
			return leftEvidence > rightEvidence
		}
		if candidates[i].Speed != candidates[j].Speed {
			return candidates[i].Speed < candidates[j].Speed
		}
		if candidates[i].MTU != candidates[j].MTU {
			return candidates[i].MTU < candidates[j].MTU
		}
		if candidates[i].Links != candidates[j].Links {
			return candidates[i].Links > candidates[j].Links
		}
		return candidates[i].Key < candidates[j].Key
	})
	if len(candidates) > 1 && managementEvidence(candidates[0]) == managementEvidence(candidates[1]) && candidates[0].Speed == candidates[1].Speed && candidates[0].MTU == candidates[1].MTU && candidates[0].Links == candidates[1].Links {
		return nil, fmt.Sprintf("multiple equivalent management groups match %d slot(s)", need), true
	}
	selected := append([]Facts(nil), candidates[0].Facts...)
	sortFactsPhysical(selected)
	reason := fmt.Sprintf("%dx%s management group", len(selected), speedLabel(candidates[0].Speed))
	if managementEvidence(candidates[0]) > 0 {
		reason = "existing management evidence"
	}
	return selected, reason, false
}

func managementEvidence(group factGroup) int {
	score := 0
	for _, item := range group.Facts {
		if item.ControlAddress {
			score += 2
		}
		if item.DefaultRoute {
			score++
		}
		if item.ManagementNetworkEvidence {
			score++
		}
	}
	return score
}

func groupFacts(facts []Facts, requireRDMA bool) []factGroup {
	byKey := map[string][]Facts{}
	for _, item := range facts {
		if requireRDMA && !item.HasRDMA {
			continue
		}
		key := hardwareKey(item)
		byKey[key] = append(byKey[key], item)
	}
	groups := make([]factGroup, 0, len(byKey))
	for key, items := range byKey {
		group := factGroup{Key: key, Facts: items}
		for _, item := range items {
			speed := effectiveMaxSpeed(item)
			if speed > group.Speed {
				group.Speed = speed
			}
			if group.MTU == 0 {
				group.MTU = item.MTU
			} else if item.MTU != group.MTU {
				group.MTU = -1
			}
			if item.LinkKnown && item.LinkUp {
				group.Links++
			}
			if item.MTU > 1500 {
				group.Evidence++
			}
			if item.RDMANetworkEvidence || (len(item.Addresses) > 0 && !item.DefaultRoute && !item.ControlAddress) {
				group.Evidence++
			}
		}
		groups = append(groups, group)
	}
	return groups
}

func supportingRDMAGroupCandidates(groups []factGroup, need int, mgmtNeed int) []factGroup {
	var candidates []factGroup
	for _, group := range groups {
		if len(group.Facts) <= need {
			continue
		}
		var withoutManagementEvidence []Facts
		for _, item := range group.Facts {
			if item.DefaultRoute || item.ControlAddress || item.ManagementNetworkEvidence {
				continue
			}
			withoutManagementEvidence = append(withoutManagementEvidence, item)
		}
		if len(withoutManagementEvidence) == need && len(group.Facts)-len(withoutManagementEvidence) >= mgmtNeed {
			candidates = append(candidates, makeFactGroup(group.Key+"|non-management-address", withoutManagementEvidence))
		}

		byMTU := map[int][]Facts{}
		allMTUKnown := true
		for _, item := range group.Facts {
			if item.MTU <= 0 {
				allMTUKnown = false
				break
			}
			byMTU[item.MTU] = append(byMTU[item.MTU], item)
		}
		if !allMTUKnown || len(byMTU) < 2 {
			continue
		}
		for mtu, items := range byMTU {
			if len(items) == need && len(group.Facts)-len(items) >= mgmtNeed {
				candidates = append(candidates, makeFactGroup(fmt.Sprintf("%s|mtu=%d", group.Key, mtu), items))
			}
		}
	}
	seen := map[string]bool{}
	unique := make([]factGroup, 0, len(candidates))
	for _, candidate := range candidates {
		names := make([]string, 0, len(candidate.Facts))
		for _, item := range candidate.Facts {
			names = append(names, canonicalName(item.Name))
		}
		sort.Strings(names)
		key := strings.Join(names, "|")
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, candidate)
	}
	return unique
}

func makeFactGroup(key string, items []Facts) factGroup {
	group := factGroup{Key: key, Facts: append([]Facts(nil), items...)}
	for _, item := range items {
		speed := effectiveMaxSpeed(item)
		if speed > group.Speed {
			group.Speed = speed
		}
		if group.MTU == 0 {
			group.MTU = item.MTU
		} else if item.MTU != group.MTU {
			group.MTU = -1
		}
		if item.LinkKnown && item.LinkUp {
			group.Links++
		}
		if item.MTU > 1500 {
			group.Evidence++
		}
		if item.RDMANetworkEvidence || (len(item.Addresses) > 0 && !item.DefaultRoute && !item.ControlAddress) {
			group.Evidence++
		}
	}
	return group
}

func hardwareKey(item Facts) string {
	model := strings.ToLower(strings.TrimSpace(item.Model))
	if model == "" {
		vendor := strings.ToLower(strings.TrimSpace(item.VendorID))
		device := strings.ToLower(strings.TrimSpace(item.DeviceID))
		if vendor != "" || device != "" {
			model = vendor + ":" + device
		}
	}
	if model == "" {
		model = "driver:" + strings.ToLower(strings.TrimSpace(item.Driver))
	}
	return fmt.Sprintf("%s|%d|rdma=%t", model, effectiveMaxSpeed(item), item.HasRDMA)
}

func effectiveMaxSpeed(item Facts) int {
	if item.MaxSpeedMbps > 0 {
		return item.MaxSpeedMbps
	}
	return item.CurrentSpeedMbps
}

func normalizeFacts(input []Facts) []Facts {
	seen := map[string]bool{}
	out := make([]Facts, 0, len(input))
	for _, item := range input {
		item.Name = strings.TrimSpace(item.Name)
		if item.Name == "" || seen[canonicalName(item.Name)] {
			continue
		}
		seen[canonicalName(item.Name)] = true
		out = append(out, item)
	}
	sortFactsPhysical(out)
	return out
}

func unusedFacts(facts []Facts, used map[string]Role) []Facts {
	out := make([]Facts, 0, len(facts))
	for _, item := range facts {
		if _, ok := used[canonicalName(item.Name)]; !ok {
			out = append(out, item)
		}
	}
	return out
}

func openSlots(count int, hints []SlotHint, existing []Binding) []int {
	used := map[int]bool{}
	for _, binding := range existing {
		used[binding.Slot] = true
	}
	maxIndex := count
	for _, hint := range hints {
		if hint.Index+1 > maxIndex {
			maxIndex = hint.Index + 1
		}
	}
	var out []int
	for idx := 0; idx < maxIndex; idx++ {
		if !used[idx] {
			out = append(out, idx)
		}
	}
	return out
}

func sortFactsPhysical(facts []Facts) {
	sort.SliceStable(facts, func(i, j int) bool {
		left, right := facts[i], facts[j]
		if left.PCI != right.PCI {
			return left.PCI < right.PCI
		}
		if left.HasDevPort != right.HasDevPort {
			return left.HasDevPort
		}
		if left.DevPort != right.DevPort {
			return left.DevPort < right.DevPort
		}
		if left.PhysPortName != right.PhysPortName {
			return left.PhysPortName < right.PhysPortName
		}
		return left.Name < right.Name
	})
}

func sortBindings(bindings []Binding) {
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].Slot < bindings[j].Slot })
}

func canonicalName(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
func canonicalMAC(value string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "-", ":"))
}

func normalizedIP(value string) string {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil {
		return ""
	}
	return ip.String()
}

func annotateNetworkEvidence(plan Plan, facts []Facts) {
	for idx := range facts {
		for _, address := range facts[idx].Addresses {
			for _, hint := range plan.ManagementHints {
				if sameIPv4Network(address, hint) {
					facts[idx].ManagementNetworkEvidence = true
				}
			}
			for _, hint := range plan.RDMAHints {
				if sameIPv4Network(address, hint) {
					facts[idx].RDMANetworkEvidence = true
				}
			}
		}
	}
}

func sameIPv4Network(address Address, hint SlotHint) bool {
	left := net.ParseIP(strings.TrimSpace(address.IP)).To4()
	right := net.ParseIP(strings.TrimSpace(hint.IP)).To4()
	if left == nil || right == nil {
		return false
	}
	prefix := hint.Prefix
	if prefix <= 0 || prefix > 32 {
		prefix = address.Prefix
	}
	if prefix <= 0 || prefix > 32 {
		return left.String() == right.String()
	}
	mask := net.CIDRMask(prefix, 32)
	return left.Mask(mask).String() == right.Mask(mask).String()
}

func speedLabel(speed int) string {
	if speed <= 0 {
		return "unknown-speed"
	}
	if speed%1000 == 0 {
		return fmt.Sprintf("%dG", speed/1000)
	}
	return fmt.Sprintf("%dM", speed)
}
