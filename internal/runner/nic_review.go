package runner

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type nicBindingReview struct {
	Original      []interfaceBinding
	Bindings      []interfaceBinding
	Devices       []netDevice
	Selected      int
	DropdownOpen  bool
	DropdownMode  string
	DropdownIndex int
	Message       string

	blinkCmd   *exec.Cmd
	blinkIface string
}

func newNICBindingReview(bindings []interfaceBinding, devices []netDevice) *nicBindingReview {
	return &nicBindingReview{
		Original: cloneInterfaceBindings(bindings),
		Bindings: cloneInterfaceBindings(bindings),
		Devices:  append([]netDevice(nil), devices...),
	}
}

func cloneInterfaceBindings(bindings []interfaceBinding) []interfaceBinding {
	out := make([]interfaceBinding, len(bindings))
	copy(out, bindings)
	return out
}

func runNICBindingReview(tty *os.File, review *nicBindingReview) ([]interfaceBinding, error) {
	if len(review.Bindings) == 0 {
		return review.Bindings, nil
	}
	defer review.stopBlink()
	program := tea.NewProgram(
		newNICBindingModel(review),
		tea.WithInput(tty),
		tea.WithOutput(tty),
		tea.WithAltScreen(),
	)
	finalModel, err := program.Run()
	if err != nil {
		return nil, err
	}
	model, ok := finalModel.(nicBindingModel)
	if !ok {
		return nil, errors.New("NIC binding review returned unexpected model")
	}
	if model.aborted {
		return nil, errors.New("NIC binding review aborted")
	}
	return review.Bindings, nil
}

func renderNICBindingReview(tty io.Writer, review *nicBindingReview) {
	width := terminalColumns(tty)
	model := newNICBindingModel(review)
	model.width = width
	model.height = 30
	fmt.Fprint(tty, "\033[2J\033[H")
	fmt.Fprint(tty, model.View())
}

type nicBindingModel struct {
	review  *nicBindingReview
	width   int
	height  int
	aborted bool
}

func newNICBindingModel(review *nicBindingReview) nicBindingModel {
	return nicBindingModel{
		review: review,
		width:  100,
		height: 30,
	}
}

func (model nicBindingModel) Init() tea.Cmd {
	return nil
}

func (model nicBindingModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		model.width = msg.Width
		model.height = msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "Q":
			model.aborted = true
			return model, tea.Quit
		case "up", "k":
			model.review.moveSelection(-1)
		case "down", "j":
			model.review.moveSelection(1)
		case " ", "space":
			if model.review.DropdownOpen {
				model.review.applyDropdown()
				return model, nil
			}
			model.review.openNICDropdown()
		case "n":
			model.review.openNICDropdown()
		case "t":
			model.review.openTargetDropdown()
		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			model.review.applyNICByNumber(msg.String())
		case "r":
			model.review.Bindings = cloneInterfaceBindings(model.review.Original)
			model.review.DropdownOpen = false
			model.review.DropdownMode = ""
			model.review.Message = "restored automatic mapping"
		case "p":
			model.review.toggleBlink()
		case "enter":
			if model.review.DropdownOpen {
				model.review.applyDropdown()
				return model, nil
			}
			if err := validateReviewBindings(model.review.Bindings); err != nil {
				model.review.Message = err.Error()
				return model, nil
			}
			return model, tea.Quit
		case "esc":
			if model.review.DropdownOpen {
				model.review.DropdownOpen = false
				model.review.DropdownMode = ""
				model.review.Message = "selection cancelled"
			}
		}
	}
	return model, nil
}

func (model nicBindingModel) View() string {
	width := model.width
	if width <= 0 {
		width = 100
	}
	var b strings.Builder
	title := lipgloss.NewStyle().Bold(true).Render("NIC Binding Review")
	fmt.Fprintln(&b, fitText(title, width))
	fmt.Fprintln(&b)
	if width >= 120 {
		renderNICBindingReviewWide(&b, model.review, width, model.deviceLimit())
	} else {
		renderNICBindingReviewCompact(&b, model.review, width, model.deviceLimit())
	}
	renderReviewDetails(&b, model.review, width)
	fmt.Fprintln(&b)
	if model.review.DropdownOpen {
		fmt.Fprintln(&b, fitText("Keys: Up/Down choose option | Space/Enter confirm | Esc cancel", width))
	} else if width >= 100 {
		fmt.Fprintln(&b, fitText("Keys: Up/Down move | Space/n choose NIC | 1-9 bind NIC | t swap target | p blink selected NIC | r reset | Enter accept | q abort", width))
	} else {
		fmt.Fprintln(&b, fitText("Keys: Up/Down move | Space/n NIC | 1-9 bind | t target | p blink | r reset | Enter accept | q abort", width))
	}
	if model.review.Message != "" {
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, fitText(model.review.Message, width))
	}
	return b.String()
}

func (model nicBindingModel) deviceLimit() int {
	limit := model.height - len(model.review.Bindings) - 14
	if model.review.DropdownOpen {
		limit -= 4
	}
	if limit < 3 {
		return 3
	}
	if limit > len(model.review.Devices) {
		return len(model.review.Devices)
	}
	return limit
}

func renderNICBindingReviewWide(tty io.Writer, review *nicBindingReview, width int, deviceLimit int) {
	fmt.Fprintln(tty, sectionHeader("Planned Bindings", width))
	fmt.Fprintln(tty, "    Slot    Target Name        Planned IP          Current NIC        MAC                Why")
	fmt.Fprintln(tty, "    ------  -----------------  ------------------  -----------------  -----------------  ------------------")
	for idx, binding := range review.Bindings {
		prefix := "  "
		if idx == review.Selected {
			prefix = "> "
		}
		fmt.Fprintf(tty, "%s  %-6s  %-17s  %-18s  %-17s  %-17s  %-18s\n",
			prefix,
			fitText(bindingSlotLabel(review.Bindings, idx), 6),
			fitText(binding.Name, 17),
			fitText(valueOrDash(binding.Address), 18),
			fitText(valueOrDash(binding.CurrentName), 17),
			fitText(valueOrDash(binding.MAC), 17),
			fitText(bindingReasonLabel(binding), 18),
		)
		if idx == review.Selected && review.DropdownOpen {
			renderReviewDropdown(tty, review, width)
		}
	}
	fmt.Fprintln(tty)
	fmt.Fprintln(tty, sectionHeader("Detected NICs", width))
	fmt.Fprintln(tty, "    ID  Current Name       MAC                Max/Now      MTU    Link   Driver      Model        PCI")
	fmt.Fprintln(tty, "    --  -----------------  -----------------  -----------  -----  -----  ----------  -----------  -------------")
	for idx, device := range visibleDevices(review.Devices, deviceLimit) {
		prefix := "  "
		blink := " "
		if review.blinkIface == device.Name {
			blink = "*"
		}
		fmt.Fprintf(tty, "%s%s%-2d  %-17s  %-17s  %-11s  %-5s  %-5s  %-10s  %-11s  %-13s\n",
			prefix,
			blink,
			idx+1,
			fitText(device.Name, 17),
			fitText(valueOrDash(device.MAC), 17),
			fitText(deviceCapacityLabel(device), 11),
			fitText(deviceMTULabel(device), 5),
			fitText(deviceLinkLabel(device), 5),
			fitText(device.Driver, 10),
			fitText(deviceModelLabel(device), 11),
			fitText(shortPCI(device.PCI), 13),
		)
	}
	renderMoreDevicesLine(tty, len(review.Devices), deviceLimit)
}

func renderNICBindingReviewCompact(tty io.Writer, review *nicBindingReview, width int, deviceLimit int) {
	nameWidth := 12
	if width < 90 {
		nameWidth = 10
	}
	fmt.Fprintln(tty, sectionHeader("Planned Bindings", width))
	fmt.Fprintf(tty, "    %-6s  %-*s  %-15s  %-*s  %-17s\n", "Slot", nameWidth, "Target", "IP", nameWidth, "NIC", "MAC")
	fmt.Fprintf(tty, "    %-6s  %-*s  %-15s  %-*s  %-17s\n", "------", nameWidth, strings.Repeat("-", nameWidth), "---------------", nameWidth, strings.Repeat("-", nameWidth), "-----------------")
	for idx, binding := range review.Bindings {
		prefix := "  "
		if idx == review.Selected {
			prefix = "> "
		}
		fmt.Fprintf(tty, "%s  %-6s  %-*s  %-15s  %-*s  %-17s\n",
			prefix,
			fitText(bindingSlotLabel(review.Bindings, idx), 6),
			nameWidth,
			fitText(binding.Name, nameWidth),
			fitText(valueOrDash(binding.Address), 15),
			nameWidth,
			fitText(valueOrDash(binding.CurrentName), nameWidth),
			fitText(valueOrDash(binding.MAC), 17),
		)
		if idx == review.Selected {
			apply := afterApplyLabel(binding)
			if apply != "-" {
				fmt.Fprintf(tty, "    apply: %s\n", fitText(apply, maxInt(10, width-11)))
			}
			fmt.Fprintf(tty, "    why:   %s\n", fitText(bindingReasonLabel(binding), maxInt(10, width-11)))
		}
		if idx == review.Selected && review.DropdownOpen {
			renderReviewDropdown(tty, review, width)
		}
	}
	fmt.Fprintln(tty)
	fmt.Fprintln(tty, sectionHeader("Detected NICs", width))
	fmt.Fprintf(tty, "    %-2s  %-*s  %-17s  %-5s  %-4s  %-8s  %-10s\n", "ID", nameWidth, "Name", "MAC", "Speed", "Link", "Driver", "Model")
	fmt.Fprintf(tty, "    %-2s  %-*s  %-17s  %-5s  %-4s  %-8s  %-10s\n", "--", nameWidth, strings.Repeat("-", nameWidth), "-----------------", "-----", "----", "--------", "----------")
	for idx, device := range visibleDevices(review.Devices, deviceLimit) {
		blink := " "
		if review.blinkIface == device.Name {
			blink = "*"
		}
		fmt.Fprintf(tty, "  %s%-2d  %-*s  %-17s  %-5s  %-4s  %-8s  %-10s\n",
			blink,
			idx+1,
			nameWidth,
			fitText(device.Name, nameWidth),
			fitText(valueOrDash(device.MAC), 17),
			fitText(deviceSpeedLabel(device), 5),
			fitText(deviceLinkLabel(device), 4),
			fitText(device.Driver, 8),
			fitText(deviceModelLabel(device), 10),
		)
	}
	renderMoreDevicesLine(tty, len(review.Devices), deviceLimit)
}

func renderReviewDetails(tty io.Writer, review *nicBindingReview, width int) {
	fmt.Fprintln(tty)
	fmt.Fprintln(tty, sectionHeader("Details", width))
	if review.Selected >= 0 && review.Selected < len(review.Bindings) {
		binding := review.Bindings[review.Selected]
		fmt.Fprintf(tty, "    Plan: %s  target=%s  ip=%s  after=%s\n",
			fitText(bindingSlotLabel(review.Bindings, review.Selected), 6),
			fitText(binding.Name, 18),
			fitText(valueOrDash(binding.Address), 18),
			fitText(afterApplyLabel(binding), maxInt(12, width-62)),
		)
		fmt.Fprintf(tty, "    Why: %s\n", fitText(bindingReasonLabel(binding), maxInt(10, width-10)))
	}
	device := activeReviewDevice(review)
	if device == nil {
		fmt.Fprintln(tty, "    NIC:  -")
		return
	}
	detail := fmt.Sprintf("NIC: %s  mac=%s  max/current=%s  mtu=%s  link=%s  driver=%s  model=%s  pci=%s  port=%s",
		device.Name,
		valueOrDash(device.MAC),
		deviceCapacityLabel(*device),
		deviceMTULabel(*device),
		deviceLinkLabel(*device),
		valueOrDash(device.Driver),
		deviceModelLabel(*device),
		valueOrDash(device.PCI),
		devicePortLabel(*device),
	)
	fmt.Fprintf(tty, "    %s\n", fitText(detail, maxInt(10, width-4)))
}

func activeReviewDevice(review *nicBindingReview) *netDevice {
	if review.DropdownOpen && review.DropdownMode == "nic" && review.DropdownIndex >= 0 && review.DropdownIndex < len(review.Devices) {
		return &review.Devices[review.DropdownIndex]
	}
	return selectedReviewDevice(review)
}

func visibleDevices(devices []netDevice, limit int) []netDevice {
	if limit <= 0 || limit >= len(devices) {
		return devices
	}
	return devices[:limit]
}

func renderMoreDevicesLine(tty io.Writer, total int, limit int) {
	if limit > 0 && total > limit {
		fmt.Fprintf(tty, "    ... %d more NIC(s); enlarge terminal to show more\n", total-limit)
	}
}

func sectionHeader(title string, width int) string {
	if width < 20 {
		return title
	}
	text := " " + title + " "
	lineWidth := maxInt(0, width-len(text)-2)
	if lineWidth > 12 {
		lineWidth = 12
	}
	return text + strings.Repeat("-", lineWidth)
}

func renderReviewDropdown(tty io.Writer, review *nicBindingReview, width int) {
	switch review.DropdownMode {
	case "nic":
		renderNICDropdown(tty, review, width)
	default:
		renderTargetDropdown(tty, review, width)
	}
}

func renderTargetDropdown(tty io.Writer, review *nicBindingReview, width int) {
	fmt.Fprintln(tty, "          Target Plan Options")
	detailWidth := maxInt(10, width-50)
	for idx, option := range review.Original {
		cursor := "  "
		if idx == review.DropdownIndex {
			cursor = "> "
		}
		fmt.Fprintf(tty, "          %s%-17s  %-18s  %s\n",
			cursor,
			fitText(option.Name, 17),
			fitText(valueOrDash(option.Address), 18),
			fitText(plannedTargetDetail(option), detailWidth),
		)
	}
}

func renderNICDropdown(tty io.Writer, review *nicBindingReview, width int) {
	fmt.Fprintln(tty, "          NIC Options")
	nameWidth := 17
	if width < 100 {
		nameWidth = 12
	}
	fmt.Fprintf(tty, "          %-3s  %-*s  %-17s  %-6s  %-5s  %-10s  %-11s\n",
		"ID",
		nameWidth,
		"Current NIC",
		"MAC",
		"Speed",
		"Link",
		"Driver",
		"Model",
	)
	for idx, device := range review.Devices {
		cursor := "  "
		if idx == review.DropdownIndex {
			cursor = "> "
		}
		fmt.Fprintf(tty, "          %s%-3d  %-*s  %-17s  %-6s  %-5s  %-10s  %-11s\n",
			cursor,
			idx+1,
			nameWidth,
			fitText(device.Name, nameWidth),
			fitText(valueOrDash(device.MAC), 17),
			fitText(deviceSpeedLabel(device), 6),
			fitText(deviceLinkLabel(device), 5),
			fitText(device.Driver, 10),
			fitText(deviceModelLabel(device), 11),
		)
	}
}

func terminalColumns(tty io.Writer) int {
	file, ok := tty.(*os.File)
	if ok {
		cmd := exec.Command("stty", "size")
		cmd.Stdin = file
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err == nil {
			fields := strings.Fields(out.String())
			if len(fields) == 2 {
				if cols, err := strconv.Atoi(fields[1]); err == nil && cols > 0 {
					return cols
				}
			}
		}
	}
	if raw := strings.TrimSpace(os.Getenv("COLUMNS")); raw != "" {
		if cols, err := strconv.Atoi(raw); err == nil && cols > 0 {
			return cols
		}
	}
	return 100
}

func withRawTerminal(tty *os.File, fn func() error) error {
	stateCmd := exec.Command("stty", "-g")
	stateCmd.Stdin = tty
	var stateOut bytes.Buffer
	stateCmd.Stdout = &stateOut
	if err := stateCmd.Run(); err != nil {
		return fmt.Errorf("read terminal state: %w", err)
	}
	state := strings.TrimSpace(stateOut.String())
	rawCmd := exec.Command("stty", "raw", "-echo")
	rawCmd.Stdin = tty
	if err := rawCmd.Run(); err != nil {
		return fmt.Errorf("enable raw terminal mode: %w", err)
	}
	defer func() {
		restoreCmd := exec.Command("stty", state)
		restoreCmd.Stdin = tty
		_ = restoreCmd.Run()
	}()
	return fn()
}

func readReviewKey(tty *os.File) (string, error) {
	buf := make([]byte, 3)
	n, err := tty.Read(buf[:1])
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", nil
	}
	switch buf[0] {
	case ' ':
		return "toggle", nil
	case '\r', '\n':
		return "accept", nil
	case 'q', 'Q':
		return "abort", nil
	case 'r', 'R':
		return "reset", nil
	case 'p', 'P':
		return "blink", nil
	case 'n', 'N':
		return "nic", nil
	case 'k', 'K':
		return "up", nil
	case 'j', 'J':
		return "down", nil
	case 'h', 'H':
		return "left", nil
	case 'l', 'L':
		return "right", nil
	case 0x03:
		return "abort", nil
	case 0x1b:
		restore, err := setTerminalReadTimeout(tty)
		if err != nil {
			return "", err
		}
		defer restore()
		n, err := tty.Read(buf[1:2])
		if err != nil {
			return "", err
		}
		if n == 0 {
			return "cancel", nil
		}
		if buf[1] != '[' {
			return "cancel", nil
		}
		n, err = tty.Read(buf[2:3])
		if err != nil {
			return "", err
		}
		if n == 0 {
			return "cancel", nil
		}
		switch buf[2] {
		case 'A':
			return "up", nil
		case 'B':
			return "down", nil
		case 'C':
			return "right", nil
		case 'D':
			return "left", nil
		}
	}
	return "", nil
}

func setTerminalReadTimeout(tty *os.File) (func(), error) {
	timeoutCmd := exec.Command("stty", "min", "0", "time", "1")
	timeoutCmd.Stdin = tty
	if err := timeoutCmd.Run(); err != nil {
		return nil, fmt.Errorf("set terminal escape timeout: %w", err)
	}
	return func() {
		restoreCmd := exec.Command("stty", "min", "1", "time", "0")
		restoreCmd.Stdin = tty
		_ = restoreCmd.Run()
	}, nil
}

func renderNICBindingAccepted(tty io.Writer) {
	fmt.Fprint(tty, "\033[2J\033[H")
}

func fitText(value string, width int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "-"
	}
	if width <= 0 {
		return ""
	}
	if len(value) <= width {
		return value
	}
	if width == 1 {
		return "~"
	}
	return value[:width-1] + "~"
}

func maxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

func (review *nicBindingReview) toggleBlink() {
	device := selectedReviewDevice(review)
	if device == nil {
		review.Message = "no selected NIC to blink"
		return
	}
	if review.blinkCmd != nil {
		if review.blinkIface == device.Name {
			review.stopBlink()
			review.Message = fmt.Sprintf("stopped blinking %s", device.Name)
			return
		}
		review.stopBlink()
	}
	cmd := exec.Command("ethtool", "-p", device.Name)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		review.Message = fmt.Sprintf("failed to blink %s with ethtool -p: %v", device.Name, err)
		return
	}
	review.blinkCmd = cmd
	review.blinkIface = device.Name
	review.Message = fmt.Sprintf("blinking %s; press p again to stop", device.Name)
}

func (review *nicBindingReview) moveSelection(delta int) {
	if review.DropdownOpen {
		review.DropdownIndex = clampIndex(review.DropdownIndex+delta, review.dropdownLength())
		review.Message = ""
		return
	}
	if len(review.Bindings) == 0 {
		return
	}
	review.Selected = clampIndex(review.Selected+delta, len(review.Bindings))
	review.Message = ""
}

func (review *nicBindingReview) openTargetDropdown() {
	if review.Selected < 0 || review.Selected >= len(review.Bindings) {
		review.Message = "select a row before choosing target name"
		return
	}
	review.DropdownIndex = originalBindingIndex(review.Original, review.Bindings[review.Selected])
	review.DropdownOpen = true
	review.DropdownMode = "target"
	review.Message = ""
}

func (review *nicBindingReview) openNICDropdown() {
	if review.Selected < 0 || review.Selected >= len(review.Bindings) {
		review.Message = "select a row before choosing NIC"
		return
	}
	if len(review.Devices) == 0 {
		review.Message = "no detected NICs available"
		return
	}
	review.DropdownIndex = reviewDeviceIndex(review.Devices, review.Bindings[review.Selected])
	if review.DropdownIndex < 0 {
		review.DropdownIndex = 0
	}
	review.DropdownOpen = true
	review.DropdownMode = "nic"
	review.Message = ""
}

func (review *nicBindingReview) dropdownLength() int {
	if review.DropdownMode == "nic" {
		return len(review.Devices)
	}
	return len(review.Original)
}

func (review *nicBindingReview) applyDropdown() {
	if review.DropdownMode == "nic" {
		review.applyNICDropdown()
		return
	}
	review.applyTargetDropdown()
}

func (review *nicBindingReview) applyTargetDropdown() {
	if !review.DropdownOpen {
		return
	}
	if review.Selected < 0 || review.Selected >= len(review.Bindings) || review.DropdownIndex < 0 || review.DropdownIndex >= len(review.Original) {
		review.Message = "select a target name before confirming"
		return
	}
	selectedPlan := review.Original[review.DropdownIndex]
	selectedCurrent := physicalBindingFields(review.Bindings[review.Selected])
	otherIdx := review.bindingIndexByPlanName(selectedPlan.Name)
	if otherIdx >= 0 && otherIdx != review.Selected {
		otherCurrent := physicalBindingFields(review.Bindings[otherIdx])
		review.Bindings[otherIdx] = copyPlanFields(review.Bindings[otherIdx], review.Bindings[review.Selected])
		applyPhysicalBindingFields(&review.Bindings[otherIdx], otherCurrent)
		markBindingUserSelected(&review.Bindings[otherIdx])
	}
	review.Bindings[review.Selected] = copyPlanFields(review.Bindings[review.Selected], selectedPlan)
	applyPhysicalBindingFields(&review.Bindings[review.Selected], selectedCurrent)
	markBindingUserSelected(&review.Bindings[review.Selected])
	review.DropdownOpen = false
	review.DropdownMode = ""
	review.Message = fmt.Sprintf("selected target %s for %s", selectedPlan.Name, valueOrDash(review.Bindings[review.Selected].CurrentName))
}

func (review *nicBindingReview) applyNICDropdown() {
	if !review.DropdownOpen {
		return
	}
	if review.Selected < 0 || review.Selected >= len(review.Bindings) || review.DropdownIndex < 0 || review.DropdownIndex >= len(review.Devices) {
		review.Message = "select a NIC before confirming"
		return
	}
	selected := review.Devices[review.DropdownIndex]
	selectedCurrent := physicalBindingFields(review.Bindings[review.Selected])
	otherIdx := review.bindingIndexByNIC(selected)
	if otherIdx >= 0 && otherIdx != review.Selected {
		otherCurrent := physicalBindingFields(review.Bindings[otherIdx])
		applyPhysicalBindingFields(&review.Bindings[otherIdx], selectedCurrent)
		if otherCurrent.MAC == "" && otherCurrent.CurrentName == "" {
			review.Bindings[otherIdx].NeedsReview = true
		}
		markBindingUserSelected(&review.Bindings[otherIdx])
	}
	applyPhysicalBindingFields(&review.Bindings[review.Selected], physicalBinding{MAC: selected.MAC, CurrentName: selected.Name})
	review.Bindings[review.Selected].NeedsReview = false
	markBindingUserSelected(&review.Bindings[review.Selected])
	review.DropdownOpen = false
	review.DropdownMode = ""
	review.Message = fmt.Sprintf("selected NIC %s for %s", selected.Name, review.Bindings[review.Selected].Name)
}

func (review *nicBindingReview) applyNICByNumber(raw string) {
	idx, err := strconv.Atoi(raw)
	if err != nil {
		review.Message = "select a NIC number"
		return
	}
	idx--
	if idx < 0 || idx >= len(review.Devices) {
		review.Message = fmt.Sprintf("NIC %s is not available", raw)
		return
	}
	if review.Selected < 0 || review.Selected >= len(review.Bindings) {
		review.Message = "select a row before choosing NIC"
		return
	}
	review.DropdownOpen = true
	review.DropdownMode = "nic"
	review.DropdownIndex = idx
	review.applyNICDropdown()
}

func (review *nicBindingReview) bindingIndexByPlanName(name string) int {
	for idx, binding := range review.Bindings {
		if binding.Name == name {
			return idx
		}
	}
	return -1
}

func (review *nicBindingReview) bindingIndexByNIC(device netDevice) int {
	for idx, binding := range review.Bindings {
		if binding.MAC != "" && strings.EqualFold(binding.MAC, device.MAC) {
			return idx
		}
		if binding.CurrentName != "" && binding.CurrentName == device.Name {
			return idx
		}
	}
	return -1
}

func originalBindingIndex(original []interfaceBinding, binding interfaceBinding) int {
	for idx, item := range original {
		if item.Name == binding.Name {
			return idx
		}
	}
	return 0
}

type physicalBinding struct {
	MAC         string
	CurrentName string
}

func physicalBindingFields(binding interfaceBinding) physicalBinding {
	return physicalBinding{
		MAC:         binding.MAC,
		CurrentName: binding.CurrentName,
	}
}

func applyPhysicalBindingFields(binding *interfaceBinding, fields physicalBinding) {
	binding.MAC = fields.MAC
	binding.CurrentName = fields.CurrentName
}

func markBindingUserSelected(binding *interfaceBinding) {
	binding.Reason = "user selected"
	binding.Confidence = "manual"
}

func copyPlanFields(binding interfaceBinding, plan interfaceBinding) interfaceBinding {
	binding.Kind = plan.Kind
	binding.Name = plan.Name
	binding.Address = plan.Address
	binding.Gateway = plan.Gateway
	binding.Table = plan.Table
	return binding
}

func (review *nicBindingReview) stopBlink() {
	if review.blinkCmd == nil {
		review.blinkIface = ""
		return
	}
	_ = review.blinkCmd.Process.Kill()
	_ = review.blinkCmd.Wait()
	review.blinkCmd = nil
	review.blinkIface = ""
}

func selectedReviewDevice(review *nicBindingReview) *netDevice {
	if review.Selected < 0 || review.Selected >= len(review.Bindings) {
		return nil
	}
	return deviceForBinding(review.Devices, review.Bindings[review.Selected])
}

func clampIndex(idx int, length int) int {
	if length <= 0 {
		return 0
	}
	if idx < 0 {
		return 0
	}
	if idx >= length {
		return length - 1
	}
	return idx
}

func reviewDeviceIndex(devices []netDevice, binding interfaceBinding) int {
	for idx, device := range devices {
		if binding.MAC != "" && strings.EqualFold(binding.MAC, device.MAC) {
			return idx
		}
		if binding.CurrentName != "" && binding.CurrentName == device.Name {
			return idx
		}
	}
	return -1
}

func validateReviewBindings(bindings []interfaceBinding) error {
	seen := map[string]string{}
	for idx, binding := range bindings {
		if strings.TrimSpace(binding.MAC) == "" {
			return fmt.Errorf("%s %s has no selected NIC", bindingSlotLabel(bindings, idx), binding.Name)
		}
		key := strings.ToLower(strings.TrimSpace(binding.MAC))
		if other := seen[key]; other != "" {
			return fmt.Errorf("%s %s uses the same NIC as %s", bindingSlotLabel(bindings, idx), binding.Name, other)
		}
		seen[key] = bindingSlotLabel(bindings, idx) + " " + binding.Name
	}
	return nil
}

func deviceForBinding(devices []netDevice, binding interfaceBinding) *netDevice {
	idx := reviewDeviceIndex(devices, binding)
	if idx < 0 {
		return nil
	}
	return &devices[idx]
}

func bindingSlotLabel(bindings []interfaceBinding, target int) string {
	if target < 0 || target >= len(bindings) {
		return "-"
	}
	count := 0
	for idx := 0; idx <= target; idx++ {
		if bindings[idx].Kind == bindings[target].Kind {
			count++
		}
	}
	return fmt.Sprintf("%s%d", bindings[target].Kind, count)
}

func valueOrDash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}

func afterApplyLabel(binding interfaceBinding) string {
	address := valueOrDash(binding.Address)
	if address == "-" {
		return binding.Name
	}
	return binding.Name + " " + address
}

func plannedTargetDetail(binding interfaceBinding) string {
	parts := []string{}
	if strings.TrimSpace(binding.Gateway) != "" {
		parts = append(parts, "via "+strings.TrimSpace(binding.Gateway))
	}
	if binding.Table > 0 {
		parts = append(parts, fmt.Sprintf("table %d", binding.Table))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

func shortPCI(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}

func devicePortLabel(device netDevice) string {
	if strings.TrimSpace(device.PhysPortName) != "" {
		return strings.TrimSpace(device.PhysPortName)
	}
	if device.HasDevPort {
		return strconv.Itoa(device.DevPort)
	}
	return "-"
}

func deviceSpeedLabel(device netDevice) string {
	if device.SpeedMbps <= 0 {
		return "-"
	}
	if device.SpeedMbps >= 1000 && device.SpeedMbps%1000 == 0 {
		return fmt.Sprintf("%dG", device.SpeedMbps/1000)
	}
	return fmt.Sprintf("%dM", device.SpeedMbps)
}

func deviceCapacityLabel(device netDevice) string {
	maxSpeed := device.MaxSpeedMbps
	if maxSpeed <= 0 {
		maxSpeed = device.SpeedMbps
	}
	maxLabel := speedMbpsLabel(maxSpeed)
	currentLabel := speedMbpsLabel(device.SpeedMbps)
	if maxLabel == currentLabel {
		return maxLabel
	}
	return maxLabel + "/" + currentLabel
}

func speedMbpsLabel(speed int) string {
	if speed <= 0 {
		return "-"
	}
	if speed >= 1000 && speed%1000 == 0 {
		return fmt.Sprintf("%dG", speed/1000)
	}
	return fmt.Sprintf("%dM", speed)
}

func deviceMTULabel(device netDevice) string {
	if device.MTU <= 0 {
		return "-"
	}
	return strconv.Itoa(device.MTU)
}

func bindingReasonLabel(binding interfaceBinding) string {
	reason := strings.TrimSpace(binding.Reason)
	confidence := strings.TrimSpace(binding.Confidence)
	if reason == "" {
		reason = "existing binding"
	}
	if confidence == "" {
		return reason
	}
	return reason + " [" + confidence + "]"
}

func deviceLinkLabel(device netDevice) string {
	if device.CarrierKnown {
		if device.CarrierUp {
			return "up"
		}
		return "down"
	}
	state := strings.TrimSpace(device.OperState)
	if state == "" {
		return "-"
	}
	return state
}

func deviceModelLabel(device netDevice) string {
	vendor := strings.TrimSpace(device.VendorID)
	id := strings.TrimSpace(device.DeviceID)
	switch {
	case vendor != "" && id != "":
		return vendor + ":" + id
	case id != "":
		return id
	case vendor != "":
		return vendor
	default:
		return "-"
	}
}
