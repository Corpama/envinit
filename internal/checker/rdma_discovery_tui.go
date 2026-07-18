package checker

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"envinit/internal/spec"
)

type networkDiscoveryReview struct {
	Targets []targetNetworkDiscovery
	Target  int
	Slot    int
	Message string
}

type targetNetworkDiscovery struct {
	Original Target
	Mgmt     []discoveredIPv4Address
	RDMA     []discoveredRDMAInterface

	MgmtChoice         int
	RDMAChoices        []int
	MgmtManual         bool
	RDMAManual         []bool
	DefaultMgmtChoice  int
	DefaultRDMAChoices []int
}

func runNetworkDiscoveryReview(targets []Target, mgmt map[string][]discoveredIPv4Address, rdma map[string][]discoveredRDMAInterface) ([]Target, error) {
	review := newNetworkDiscoveryReview(targets, mgmt, rdma)
	program := tea.NewProgram(newNetworkDiscoveryModel(review), tea.WithAltScreen())
	finalModel, err := program.Run()
	if err != nil {
		return nil, err
	}
	model, ok := finalModel.(networkDiscoveryModel)
	if !ok {
		return nil, errors.New("network discovery review returned unexpected model")
	}
	if model.aborted {
		return nil, errors.New("network discovery review aborted")
	}
	return reviewTargets(review), nil
}

func newNetworkDiscoveryReview(targets []Target, mgmt map[string][]discoveredIPv4Address, rdma map[string][]discoveredRDMAInterface) *networkDiscoveryReview {
	out := &networkDiscoveryReview{
		Targets: make([]targetNetworkDiscovery, 0, len(targets)),
	}
	for _, target := range targets {
		item := targetNetworkDiscovery{
			Original:   target,
			Mgmt:       append([]discoveredIPv4Address(nil), mgmt[target.Name]...),
			RDMA:       append([]discoveredRDMAInterface(nil), rdma[target.Name]...),
			MgmtChoice: -1,
		}
		for idx, candidate := range item.Mgmt {
			if candidate.IP == target.Address {
				item.MgmtChoice = idx
				break
			}
		}
		if item.MgmtChoice < 0 && len(item.Mgmt) > 0 && strongDiscoveryCandidate(item.Mgmt[0].Confidence) {
			item.MgmtChoice = 0
		}
		slotCount := len(target.RDMA)
		if slotCount == 0 {
			slotCount = len(item.RDMA)
		}
		item.RDMAChoices = make([]int, slotCount)
		item.RDMAManual = make([]bool, slotCount)
		for idx := range item.RDMAChoices {
			item.RDMAChoices[idx] = -1
		}
		for slot, existing := range target.RDMA {
			if slot >= len(item.RDMAChoices) {
				break
			}
			for idx, candidate := range item.RDMA {
				if strings.TrimSpace(existing.Name) != "" && existing.Name == candidate.Name {
					item.RDMAChoices[slot] = idx
					break
				}
				if strings.TrimSpace(existing.IP) != "" && existing.IP == candidate.IP {
					item.RDMAChoices[slot] = idx
					break
				}
			}
		}
		item.DefaultMgmtChoice = item.MgmtChoice
		item.DefaultRDMAChoices = append([]int(nil), item.RDMAChoices...)
		out.Targets = append(out.Targets, item)
	}
	return out
}

func reviewTargets(review *networkDiscoveryReview) []Target {
	out := make([]Target, 0, len(review.Targets))
	for _, item := range review.Targets {
		target := item.Original
		if item.MgmtChoice >= 0 && item.MgmtChoice < len(item.Mgmt) {
			target.Address = item.Mgmt[item.MgmtChoice].IP
		}
		target.RDMA = nil
		for _, choice := range item.RDMAChoices {
			if choice < 0 || choice >= len(item.RDMA) {
				continue
			}
			candidate := item.RDMA[choice]
			target.RDMA = append(target.RDMA, spec.RDMARecord{
				Name:   candidate.Name,
				MAC:    candidate.MAC,
				IP:     candidate.IP,
				Prefix: prefixString(candidate.Prefix),
			})
		}
		out = append(out, target)
	}
	return out
}

type networkDiscoveryModel struct {
	review  *networkDiscoveryReview
	width   int
	height  int
	aborted bool
}

func newNetworkDiscoveryModel(review *networkDiscoveryReview) networkDiscoveryModel {
	return networkDiscoveryModel{review: review, width: 120, height: 32}
}

func (m networkDiscoveryModel) Init() tea.Cmd {
	return nil
}

func (m networkDiscoveryModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "Q":
			m.aborted = true
			return m, tea.Quit
		case "enter":
			if err := validateNetworkDiscoveryReview(m.review); err != nil {
				m.review.Message = err.Error()
				return m, nil
			}
			return m, tea.Quit
		case "tab", "right", "l":
			m.review.moveTarget(1)
		case "shift+tab", "left", "h":
			m.review.moveTarget(-1)
		case "up", "k":
			m.review.moveSlot(-1)
		case "down", "j":
			m.review.moveSlot(1)
		case " ", "space", "backspace", "delete":
			m.review.clearSlot()
		case "r":
			m.review.resetTargetDefaults()
		case "+", "=":
			m.review.addRDMASlot()
		case "-", "_":
			m.review.removeRDMASlot()
		default:
			if idx, ok := candidateKeyIndex(msg.String()); ok {
				m.review.bindSlot(idx)
			}
		}
	}
	return m, nil
}

func (m networkDiscoveryModel) View() string {
	if len(m.review.Targets) == 0 {
		return "Network Discovery Review\n\nNo targets discovered.\n"
	}
	var b strings.Builder
	target := &m.review.Targets[m.review.Target]
	fmt.Fprintf(&b, "Network Discovery Review  target %d/%d: %s\n\n", m.review.Target+1, len(m.review.Targets), target.Original.Name)
	fmt.Fprintln(&b, "Bind inventory fields to discovered network candidates.")
	fmt.Fprintln(&b)
	renderDiscoverySlots(&b, target, m.review.Slot)
	fmt.Fprintln(&b)
	renderDiscoveryCandidates(&b, target, m.review.Slot)
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Keys: Up/Down slot | 1-9/0 bind | +/- RDMA slots | Space clear | r reset | Tab target | Enter accept | q abort")
	if m.review.Message != "" {
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, m.review.Message)
	}
	return b.String()
}

func renderDiscoverySlots(b *strings.Builder, target *targetNetworkDiscovery, selected int) {
	fmt.Fprintln(b, "Planned inventory fields")
	fmt.Fprintln(b, "  Slot    Value                                      Why")
	fmt.Fprintln(b, "  ----    -----------------------------------------  ------------------------")
	marker := " "
	if selected == 0 {
		marker = ">"
	}
	fmt.Fprintf(b, "%s mgmt   %-41s  %s\n", marker, mgmtChoiceLabel(target), mgmtChoiceReason(target))
	for idx := range target.RDMAChoices {
		marker = " "
		if selected == idx+1 {
			marker = ">"
		}
		fmt.Fprintf(b, "%s rdma%-2d %-41s  %s\n", marker, idx+1, rdmaChoiceLabel(target, idx), rdmaChoiceReason(target, idx))
	}
}

func renderDiscoveryCandidates(b *strings.Builder, target *targetNetworkDiscovery, slot int) {
	if slot == 0 {
		fmt.Fprintln(b, "Management candidates")
		fmt.Fprintln(b, "  Key  Iface           IP               Max/Now      MTU   Link  Why")
		fmt.Fprintln(b, "  ---  --------------  ---------------  -----------  ----  ----  ------------------------")
		for idx, candidate := range target.Mgmt {
			fmt.Fprintf(b, "  %-3s  %-14s  %-15s  %-11s  %-4s  %-4s  %s\n", candidateKeyLabel(idx), candidate.Iface, candidate.IP, discoveredSpeedLabel(candidate.MaxSpeedMbps, candidate.CurrentSpeedMbps), discoveredMTULabel(candidate.MTU), discoveredLinkLabel(candidate.LinkKnown, candidate.LinkUp), discoveryReasonLabel(candidate.Reason, candidate.Confidence, candidate.Preferred))
		}
		return
	}
	fmt.Fprintln(b, "RDMA candidates")
	fmt.Fprintln(b, "  Key  NIC             IP              IB        Max/Now      MTU   Link  Why")
	fmt.Fprintln(b, "  ---  --------------  --------------  --------  -----------  ----  ----  ------------------------")
	for idx, candidate := range target.RDMA {
		fmt.Fprintf(b, "  %-3s  %-14s  %-14s  %-8s  %-11s  %-4s  %-4s  %s\n", candidateKeyLabel(idx), candidate.Name, candidate.IP, candidate.IBDevice, discoveredSpeedLabel(candidate.MaxSpeedMbps, candidate.CurrentSpeedMbps), discoveredMTULabel(candidate.MTU), discoveredLinkLabel(candidate.LinkKnown, candidate.LinkUp), discoveryReasonLabel(candidate.Reason, candidate.Confidence, false))
	}
}

func mgmtChoiceLabel(target *targetNetworkDiscovery) string {
	if target.MgmtChoice < 0 || target.MgmtChoice >= len(target.Mgmt) {
		return "-"
	}
	item := target.Mgmt[target.MgmtChoice]
	return fmt.Sprintf("%s %s", item.Iface, item.IP)
}

func rdmaChoiceLabel(target *targetNetworkDiscovery, slot int) string {
	if slot < 0 || slot >= len(target.RDMAChoices) {
		return "-"
	}
	choice := target.RDMAChoices[slot]
	if choice < 0 || choice >= len(target.RDMA) {
		return "-"
	}
	item := target.RDMA[choice]
	return fmt.Sprintf("%s %s %s", item.Name, item.IP, item.IBDevice)
}

func mgmtChoiceReason(target *targetNetworkDiscovery) string {
	if target.MgmtChoice < 0 || target.MgmtChoice >= len(target.Mgmt) {
		return "manual selection required"
	}
	item := target.Mgmt[target.MgmtChoice]
	if target.MgmtManual {
		return "user selected [manual]"
	}
	return discoveryReasonLabel(item.Reason, item.Confidence, item.Preferred)
}

func rdmaChoiceReason(target *targetNetworkDiscovery, slot int) string {
	if slot < 0 || slot >= len(target.RDMAChoices) {
		return "manual selection required"
	}
	choice := target.RDMAChoices[slot]
	if choice < 0 || choice >= len(target.RDMA) {
		return "manual selection required"
	}
	item := target.RDMA[choice]
	if slot < len(target.RDMAManual) && target.RDMAManual[slot] {
		return "user selected [manual]"
	}
	return discoveryReasonLabel(item.Reason, item.Confidence, false)
}

func (review *networkDiscoveryReview) moveTarget(delta int) {
	if len(review.Targets) == 0 {
		return
	}
	review.Target += delta
	if review.Target < 0 {
		review.Target = len(review.Targets) - 1
	}
	if review.Target >= len(review.Targets) {
		review.Target = 0
	}
	review.Slot = 0
	review.Message = ""
}

func (review *networkDiscoveryReview) moveSlot(delta int) {
	target := &review.Targets[review.Target]
	maxSlot := len(target.RDMAChoices)
	review.Slot += delta
	if review.Slot < 0 {
		review.Slot = maxSlot
	}
	if review.Slot > maxSlot {
		review.Slot = 0
	}
	review.Message = ""
}

func (review *networkDiscoveryReview) clearSlot() {
	target := &review.Targets[review.Target]
	if review.Slot == 0 {
		target.MgmtChoice = -1
		target.MgmtManual = false
	} else {
		idx := review.Slot - 1
		if idx >= 0 && idx < len(target.RDMAChoices) {
			target.RDMAChoices[idx] = -1
			target.RDMAManual[idx] = false
		}
	}
	review.Message = ""
}

func (review *networkDiscoveryReview) bindSlot(candidate int) {
	target := &review.Targets[review.Target]
	if review.Slot == 0 {
		if candidate >= 0 && candidate < len(target.Mgmt) {
			target.MgmtChoice = candidate
			target.MgmtManual = true
			review.Message = ""
			return
		}
		review.Message = "management candidate key is out of range"
		return
	}
	idx := review.Slot - 1
	if candidate >= 0 && candidate < len(target.RDMA) && idx >= 0 && idx < len(target.RDMAChoices) {
		target.RDMAChoices[idx] = candidate
		target.RDMAManual[idx] = true
		review.Message = ""
		return
	}
	review.Message = "RDMA candidate key is out of range"
}

func (review *networkDiscoveryReview) resetTargetDefaults() {
	target := &review.Targets[review.Target]
	target.MgmtChoice = target.DefaultMgmtChoice
	target.RDMAChoices = append([]int(nil), target.DefaultRDMAChoices...)
	target.MgmtManual = false
	target.RDMAManual = make([]bool, len(target.RDMAChoices))
	review.Message = "restored discovered defaults for current target"
}

func (review *networkDiscoveryReview) addRDMASlot() {
	target := &review.Targets[review.Target]
	if len(target.RDMAChoices) >= len(target.RDMA) {
		review.Message = "all discovered RDMA candidates already have a slot"
		return
	}
	target.RDMAChoices = append(target.RDMAChoices, -1)
	target.RDMAManual = append(target.RDMAManual, false)
	review.Slot = len(target.RDMAChoices)
	review.Message = "added RDMA inventory slot; bind a candidate before accepting"
}

func (review *networkDiscoveryReview) removeRDMASlot() {
	target := &review.Targets[review.Target]
	if len(target.RDMAChoices) == 0 {
		review.Message = "no RDMA slot to remove"
		return
	}
	target.RDMAChoices = target.RDMAChoices[:len(target.RDMAChoices)-1]
	target.RDMAManual = target.RDMAManual[:len(target.RDMAManual)-1]
	if review.Slot > len(target.RDMAChoices) {
		review.Slot = len(target.RDMAChoices)
	}
	review.Message = "removed the last RDMA inventory slot"
}

func validateNetworkDiscoveryReview(review *networkDiscoveryReview) error {
	for _, target := range review.Targets {
		if target.MgmtChoice < 0 || target.MgmtChoice >= len(target.Mgmt) {
			return fmt.Errorf("%s mgmt is not selected", target.Original.Name)
		}
		seen := map[int]bool{}
		mgmtIface := ""
		mgmtIP := ""
		if target.MgmtChoice >= 0 && target.MgmtChoice < len(target.Mgmt) {
			mgmtIface = target.Mgmt[target.MgmtChoice].Iface
			mgmtIP = target.Mgmt[target.MgmtChoice].IP
		}
		for idx, choice := range target.RDMAChoices {
			if choice < 0 {
				return fmt.Errorf("%s rdma%d is not selected", target.Original.Name, idx+1)
			}
			if choice >= len(target.RDMA) {
				return fmt.Errorf("%s rdma%d candidate is out of range", target.Original.Name, idx+1)
			}
			if seen[choice] {
				return fmt.Errorf("%s candidate %s is assigned more than once", target.Original.Name, target.RDMA[choice].Name)
			}
			if strings.EqualFold(target.RDMA[choice].Name, mgmtIface) || target.RDMA[choice].IP == mgmtIP {
				return fmt.Errorf("%s interface %s cannot be both management and RDMA", target.Original.Name, target.RDMA[choice].Name)
			}
			seen[choice] = true
		}
	}
	return nil
}

func candidateKeyIndex(key string) (int, bool) {
	switch key {
	case "1":
		return 0, true
	case "2":
		return 1, true
	case "3":
		return 2, true
	case "4":
		return 3, true
	case "5":
		return 4, true
	case "6":
		return 5, true
	case "7":
		return 6, true
	case "8":
		return 7, true
	case "9":
		return 8, true
	case "0":
		return 9, true
	default:
		return 0, false
	}
}

func candidateKeyLabel(idx int) string {
	if idx == 9 {
		return "0"
	}
	if idx >= 0 && idx < 9 {
		return fmt.Sprintf("%d", idx+1)
	}
	return "-"
}

func strongDiscoveryCandidate(confidence string) bool {
	return confidence == "exact" || confidence == "strong"
}

func discoveredSpeedLabel(maximum int, current int) string {
	maxLabel := discoveredSingleSpeedLabel(maximum)
	currentLabel := discoveredSingleSpeedLabel(current)
	if maxLabel == currentLabel {
		return maxLabel
	}
	return maxLabel + "/" + currentLabel
}

func discoveredSingleSpeedLabel(speed int) string {
	if speed <= 0 {
		return "-"
	}
	if speed%1000 == 0 {
		return fmt.Sprintf("%dG", speed/1000)
	}
	return fmt.Sprintf("%dM", speed)
}

func discoveredMTULabel(mtu int) string {
	if mtu <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d", mtu)
}

func discoveredLinkLabel(known bool, up bool) string {
	if !known {
		return "-"
	}
	if up {
		return "up"
	}
	return "down"
}

func discoveryReasonLabel(reason string, confidence string, defaultRoute bool) string {
	parts := make([]string, 0, 2)
	if strings.TrimSpace(reason) != "" {
		parts = append(parts, strings.TrimSpace(reason))
	}
	if defaultRoute {
		parts = append(parts, "default-route")
	}
	label := strings.Join(parts, ", ")
	if label == "" {
		label = "candidate"
	}
	if strings.TrimSpace(confidence) != "" {
		label += " [" + strings.TrimSpace(confidence) + "]"
	}
	return label
}
