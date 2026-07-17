package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const mstSelectionFile = "/var/lib/envinit/mst-devices.json"

type mstDevice struct {
	Path        string `json:"mst"`
	PCI         string `json:"pci,omitempty"`
	Net         string `json:"net,omitempty"`
	Recommended bool   `json:"-"`
	Reason      string `json:"-"`
}

type mstSelection struct {
	SchemaVersion  int         `json:"schema_version,omitempty"`
	RDMAInterfaces []string    `json:"rdma_interfaces,omitempty"`
	Devices        []mstDevice `json:"mlxconfig_devices"`
}

type mstStatusDevice struct {
	PCI string
	Net string
}

func (a *App) mlxconfigDevices() ([]string, error) {
	if glob := strings.TrimSpace(a.Bundle.MlxConfig.DeviceGlob); glob != "" {
		return a.mlxconfigDevicesFromGlob(glob)
	}

	if a.DryRun {
		a.logf("ignore persisted MST mlxconfig device selection during plan; scanning current system state")
	}

	candidates, err := a.discoverMSTDevices()
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, errors.New("no MST pciconf devices discovered under /dev/mst; run mst start and verify Mellanox devices are present")
	}
	a.markRecommendedMSTDevices(candidates)

	if !a.DryRun {
		if devices, err := a.loadValidatedMSTSelection(candidates); err == nil && len(devices) > 0 {
			a.logf("use validated MST mlxconfig device selection from %s: %s", mstSelectionFile, strings.Join(devices, ", "))
			return devices, nil
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			a.logf("ignore persisted MST mlxconfig device selection: %v", err)
		}
	}

	selected, err := a.confirmMSTDevices(candidates)
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return nil, errors.New("no MST devices selected for mlxconfig")
	}
	if err := a.saveMSTSelection(selected); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(selected))
	for _, device := range selected {
		paths = append(paths, device.Path)
	}
	return paths, nil
}

func (a *App) mlxconfigDevicesFromGlob(glob string) ([]string, error) {
	devices, err := filepath.Glob(glob)
	if err != nil {
		return nil, fmt.Errorf("glob mlxconfig devices: %w", err)
	}
	filtered := filterMSTPciconfDevices(devices)
	if len(filtered) == 0 {
		return nil, fmt.Errorf("no mlxconfig devices matched %s", glob)
	}
	return filtered, nil
}

func (a *App) discoverMSTDevices() ([]mstDevice, error) {
	matches, err := a.globMSTPciconfDevices()
	if err != nil {
		return nil, err
	}
	if len(filterMSTPciconfDevices(matches)) == 0 {
		if err := a.ensureMSTStartedForDiscovery(); err != nil {
			return nil, err
		}
		matches, err = a.globMSTPciconfDevices()
		if err != nil {
			return nil, err
		}
	}
	paths := make([]string, 0, len(matches))
	for _, match := range filterMSTPciconfDevices(matches) {
		if a.Root != "" && a.Root != "/" {
			rel, err := filepath.Rel(a.Root, match)
			if err == nil {
				match = "/" + rel
			}
		}
		paths = append(paths, match)
	}
	sort.Strings(paths)
	statusDevices := a.mstStatusDevicesByDevice()
	devices := make([]mstDevice, 0, len(paths))
	for _, path := range paths {
		pci := mstPCIFromDeviceName(path)
		if pci == "" {
			pci = statusDevices[filepath.Base(path)].PCI
		}
		netName := statusDevices[filepath.Base(path)].Net
		devices = append(devices, mstDevice{
			Path: path,
			PCI:  normalizePCI(pci),
			Net:  netName,
		})
	}
	return devices, nil
}

func (a *App) globMSTPciconfDevices() ([]string, error) {
	matches, err := filepath.Glob(a.targetPath("/dev/mst/*_pciconf*"))
	if err != nil {
		return nil, fmt.Errorf("scan MST devices: %w", err)
	}
	return matches, nil
}

func (a *App) ensureMSTStartedForDiscovery() error {
	if a.Root != "" && a.Root != "/" {
		a.logf("skip mst start for alternate root %s; no MST pciconf devices found", a.Root)
		return nil
	}
	if a.DryRun {
		a.logf("run (discovery): mst start")
		data, err := exec.Command("mst", "start").CombinedOutput()
		if strings.TrimSpace(string(data)) != "" {
			a.logf("capture: %s", strings.TrimSpace(string(data)))
		}
		if err != nil {
			return fmt.Errorf("start MST for discovery: %w", err)
		}
		return nil
	}
	return a.runCmd("", nil, "mst", "start")
}

func filterMSTPciconfDevices(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		base := filepath.Base(path)
		if !strings.Contains(base, "_pciconf") {
			continue
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func mstPCIFromDeviceName(path string) string {
	base := filepath.Base(path)
	if pci := firstPCIAddress(base); pci != "" {
		return pci
	}
	return ""
}

var pciAddressPattern = regexp.MustCompile(`(?i)(?:[0-9a-f]{4}:)?[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]`)

func firstPCIAddress(value string) string {
	match := pciAddressPattern.FindString(value)
	if match == "" {
		return ""
	}
	return normalizePCI(match)
}

func normalizePCI(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	if strings.Count(value, ":") == 1 {
		value = "0000:" + value
	}
	return value
}

func (a *App) mstStatusDevicesByDevice() map[string]mstStatusDevice {
	out := map[string]mstStatusDevice{}
	var output string
	var err error
	if a.DryRun {
		a.logf("capture (read-only): mst status -v")
		data, runErr := exec.Command("mst", "status", "-v").CombinedOutput()
		output = string(data)
		err = runErr
	} else {
		output, err = a.captureCmd("", nil, "mst", "status", "-v")
	}
	if err != nil {
		a.logf("skip MST status PCI correlation: %v", err)
		return out
	}
	return parseMSTStatusDevices(output)
}

func parseMSTStatusDevices(output string) map[string]mstStatusDevice {
	out := map[string]mstStatusDevice{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		deviceName := ""
		status := mstStatusDevice{}
		for _, field := range fields {
			base := filepath.Base(field)
			if strings.Contains(base, "_pciconf") {
				deviceName = base
				continue
			}
			if pci := firstPCIAddress(field); pci != "" {
				status.PCI = pci
				continue
			}
			if strings.HasPrefix(field, "net-") {
				status.Net = strings.TrimPrefix(field, "net-")
			}
		}
		if deviceName != "" && (status.PCI != "" || status.Net != "") {
			out[deviceName] = status
		}
	}
	return out
}

func (a *App) loadValidatedMSTSelection(candidates []mstDevice) ([]string, error) {
	data, err := os.ReadFile(a.targetPath(mstSelectionFile))
	if err != nil {
		return nil, err
	}
	var selection mstSelection
	if err := json.Unmarshal(data, &selection); err != nil {
		return nil, fmt.Errorf("parse %s: %w", mstSelectionFile, err)
	}
	if err := a.validateMSTSelection(selection, candidates); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(selection.Devices))
	for _, device := range selection.Devices {
		path := strings.TrimSpace(device.Path)
		paths = append(paths, path)
	}
	return paths, nil
}

func (a *App) validateMSTSelection(selection mstSelection, candidates []mstDevice) error {
	if selection.SchemaVersion < 2 {
		return fmt.Errorf("%s uses legacy schema and cannot be validated against current MST NET/PCI state", mstSelectionFile)
	}
	if len(selection.Devices) == 0 {
		return fmt.Errorf("%s does not contain any mlxconfig devices", mstSelectionFile)
	}
	expectedRDMA := a.currentRDMAInterfaceNames()
	if len(expectedRDMA) > 0 && len(selection.RDMAInterfaces) != len(expectedRDMA) {
		return fmt.Errorf("persisted MST selection covers %d RDMA interface(s), current confirmation has %d", len(selection.RDMAInterfaces), len(expectedRDMA))
	}
	if len(expectedRDMA) > 0 && !sameStringSet(selection.RDMAInterfaces, expectedRDMA) {
		return fmt.Errorf("persisted MST RDMA interface set %s does not match current confirmation %s", strings.Join(selection.RDMAInterfaces, ","), strings.Join(expectedRDMA, ","))
	}
	if len(expectedRDMA) > 0 && len(selection.Devices) != len(expectedRDMA) {
		return fmt.Errorf("persisted MST selection has %d device(s), current RDMA confirmation has %d interface(s)", len(selection.Devices), len(expectedRDMA))
	}

	candidateByPath := map[string]mstDevice{}
	for _, candidate := range candidates {
		candidateByPath[candidate.Path] = candidate
	}
	rdmaNames := stringSliceSet(expectedRDMA)
	seen := map[string]bool{}
	for _, device := range selection.Devices {
		path := strings.TrimSpace(device.Path)
		if path == "" {
			return fmt.Errorf("persisted MST selection contains an empty device path")
		}
		if seen[path] {
			return fmt.Errorf("persisted MST selection contains duplicate device %s", path)
		}
		seen[path] = true
		if _, err := os.Stat(a.targetPath(path)); err != nil {
			return fmt.Errorf("persisted MST device %s is not available: %w", path, err)
		}
		current, ok := candidateByPath[path]
		if !ok {
			return fmt.Errorf("persisted MST device %s is not present in current /dev/mst scan", path)
		}
		if strings.TrimSpace(device.Net) == "" {
			return fmt.Errorf("persisted MST device %s does not record NET mapping", path)
		}
		if strings.TrimSpace(current.Net) == "" {
			return fmt.Errorf("current MST status does not provide NET mapping for %s", path)
		}
		if device.Net != current.Net {
			return fmt.Errorf("persisted MST device %s NET changed from %s to %s", path, device.Net, current.Net)
		}
		if strings.TrimSpace(device.PCI) == "" {
			return fmt.Errorf("persisted MST device %s does not record PCI mapping", path)
		}
		if strings.TrimSpace(current.PCI) == "" {
			return fmt.Errorf("current MST status does not provide PCI mapping for %s", path)
		}
		if normalizePCI(device.PCI) != normalizePCI(current.PCI) {
			return fmt.Errorf("persisted MST device %s PCI changed from %s to %s", path, normalizePCI(device.PCI), normalizePCI(current.PCI))
		}
		if len(rdmaNames) > 0 && !rdmaNames[device.Net] {
			return fmt.Errorf("persisted MST device %s maps to non-RDMA interface %s", path, device.Net)
		}
	}
	return nil
}

func (a *App) saveMSTSelection(devices []mstDevice) error {
	if a.DryRun {
		a.logf("dry-run: would write %s with %d MST device(s)", mstSelectionFile, len(devices))
		return nil
	}
	selection := mstSelection{
		SchemaVersion:  2,
		RDMAInterfaces: a.currentRDMAInterfaceNames(),
		Devices:        normalizeMSTDevicesForSelection(devices),
	}
	data, err := json.MarshalIndent(selection, "", "  ")
	if err != nil {
		return fmt.Errorf("encode MST selection: %w", err)
	}
	return a.writeManagedFile(mstSelectionFile, string(data)+"\n", 0o644)
}

func (a *App) confirmMSTDevices(candidates []mstDevice) ([]mstDevice, error) {
	if a.DryRun {
		if a.InteractiveDryRunReview {
			tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
			if err != nil {
				selected := defaultMSTSelection(candidates)
				a.logf("skip interactive dry-run MST device review: /dev/tty is not available; using default MST selection: %s", describeMSTDevices(selected))
				return selected, nil
			}
			defer tty.Close()
			return runMSTDeviceReview(tty, candidates)
		}
		selected := defaultMSTSelection(candidates)
		a.logf("dry-run: would review %d discovered MST device(s) for mlxconfig; default selection: %s", len(candidates), describeMSTDevices(selected))
		return selected, nil
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		selected := defaultMSTSelection(candidates)
		a.logf("skip interactive MST device review: /dev/tty is not available; using default MST selection: %s", describeMSTDevices(selected))
		return selected, nil
	}
	defer tty.Close()
	return runMSTDeviceReview(tty, candidates)
}

type mstDeviceReview struct {
	Devices  []mstDevice
	Selected map[int]bool
	Index    int
	Message  string
}

func runMSTDeviceReview(tty *os.File, candidates []mstDevice) ([]mstDevice, error) {
	review := mstDeviceReview{
		Devices:  append([]mstDevice(nil), candidates...),
		Selected: map[int]bool{},
	}
	review.resetDefaultSelection()
	review.Message = ""
	program := tea.NewProgram(
		newMSTDeviceModel(&review),
		tea.WithInput(tty),
		tea.WithOutput(tty),
		tea.WithAltScreen(),
	)
	finalModel, err := program.Run()
	if err != nil {
		return nil, err
	}
	model, ok := finalModel.(mstDeviceModel)
	if !ok {
		return nil, errors.New("MST device review returned unexpected model")
	}
	if model.aborted {
		return nil, errors.New("MST device review aborted")
	}
	return selectedMSTDevices(&review), nil
}

func defaultMSTSelection(candidates []mstDevice) []mstDevice {
	review := mstDeviceReview{
		Devices:  append([]mstDevice(nil), candidates...),
		Selected: map[int]bool{},
	}
	review.resetDefaultSelection()
	review.Message = ""
	return selectedMSTDevices(&review)
}

func selectedMSTDevices(review *mstDeviceReview) []mstDevice {
	out := make([]mstDevice, 0, len(review.Devices))
	for idx, device := range review.Devices {
		if review.Selected[idx] {
			out = append(out, device)
		}
	}
	return out
}

type mstDeviceModel struct {
	review  *mstDeviceReview
	width   int
	height  int
	aborted bool
}

func newMSTDeviceModel(review *mstDeviceReview) mstDeviceModel {
	return mstDeviceModel{review: review, width: 100, height: 24}
}

func (model mstDeviceModel) Init() tea.Cmd {
	return nil
}

func (model mstDeviceModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
			model.review.toggleSelected()
		case "r":
			model.review.resetDefaultSelection()
		case "enter":
			if len(selectedMSTDevices(model.review)) == 0 {
				model.review.Message = "select at least one MST device"
				return model, nil
			}
			return model, tea.Quit
		}
	}
	return model, nil
}

func (model mstDeviceModel) View() string {
	width := model.width
	if width <= 0 {
		width = 100
	}
	var b strings.Builder
	title := lipgloss.NewStyle().Bold(true).Render("MST Device Review")
	fmt.Fprintln(&b, fitText(title, width))
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, fitText("Select devices that should receive mlxconfig settings.", width))
	fmt.Fprintln(&b)
	if width >= 110 {
		renderMSTDeviceReviewWide(&b, model.review, width, model.deviceLimit())
	} else {
		renderMSTDeviceReviewCompact(&b, model.review, width, model.deviceLimit())
	}
	fmt.Fprintln(&b)
	renderMSTDeviceDetails(&b, model.review, width)
	fmt.Fprintln(&b)
	if width >= 90 {
		fmt.Fprintln(&b, fitText("Keys: Up/Down move | Space toggle device | r reset recommended/default | Enter accept | q abort", width))
	} else {
		fmt.Fprintln(&b, fitText("Keys: Up/Down move | Space toggle | r reset | Enter accept | q abort", width))
	}
	if model.review.Message != "" {
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, fitText(model.review.Message, width))
	}
	return b.String()
}

func (model mstDeviceModel) deviceLimit() int {
	limit := model.height - 12
	if limit < 3 {
		return 3
	}
	if limit > len(model.review.Devices) {
		return len(model.review.Devices)
	}
	return limit
}

func renderMSTDeviceReviewWide(tty *strings.Builder, review *mstDeviceReview, width int, limit int) {
	fmt.Fprintln(tty, sectionHeader("Discovered MST Devices", width))
	fmt.Fprintln(tty, "    Use  MST Device                    PCI            Source")
	fmt.Fprintln(tty, "    ---  ----------------------------  -------------  ------------------------------")
	start, end := visibleMSTDeviceRange(len(review.Devices), review.Index, limit)
	renderHiddenMSTDevicesLine(tty, start, "above")
	for idx := start; idx < end; idx++ {
		device := review.Devices[idx]
		prefix := "  "
		if idx == review.Index {
			prefix = "> "
		}
		checked := "[ ]"
		if review.Selected[idx] {
			checked = "[x]"
		}
		fmt.Fprintf(tty, "%s  %s  %-28s  %-13s  %s\n",
			prefix,
			checked,
			fitText(device.Path, 28),
			fitText(shortPCI(device.PCI), 13),
			fitText(mstDeviceReasonLabel(device), maxInt(10, width-58)),
		)
	}
	renderHiddenMSTDevicesLine(tty, len(review.Devices)-end, "below")
}

func renderMSTDeviceReviewCompact(tty *strings.Builder, review *mstDeviceReview, width int, limit int) {
	nameWidth := 28
	if width < 80 {
		nameWidth = 22
	}
	fmt.Fprintln(tty, sectionHeader("Discovered MST Devices", width))
	fmt.Fprintf(tty, "    %-3s  %-*s  %-13s\n", "Use", nameWidth, "MST Device", "PCI")
	fmt.Fprintf(tty, "    %-3s  %-*s  %-13s\n", "---", nameWidth, strings.Repeat("-", nameWidth), "-------------")
	start, end := visibleMSTDeviceRange(len(review.Devices), review.Index, limit)
	renderHiddenMSTDevicesLine(tty, start, "above")
	for idx := start; idx < end; idx++ {
		device := review.Devices[idx]
		prefix := "  "
		if idx == review.Index {
			prefix = "> "
		}
		checked := "[ ]"
		if review.Selected[idx] {
			checked = "[x]"
		}
		fmt.Fprintf(tty, "%s  %s  %-*s  %-13s\n",
			prefix,
			checked,
			nameWidth,
			fitText(device.Path, nameWidth),
			fitText(shortPCI(device.PCI), 13),
		)
		if idx == review.Index {
			fmt.Fprintf(tty, "    source: %s\n", fitText(mstDeviceReasonLabel(device), maxInt(10, width-12)))
		}
	}
	renderHiddenMSTDevicesLine(tty, len(review.Devices)-end, "below")
}

func renderMSTDeviceDetails(tty *strings.Builder, review *mstDeviceReview, width int) {
	fmt.Fprintln(tty, sectionHeader("Details", width))
	if review.Index < 0 || review.Index >= len(review.Devices) {
		fmt.Fprintln(tty, "    MST: -")
		return
	}
	device := review.Devices[review.Index]
	detail := fmt.Sprintf("MST: %s  pci=%s  selected=%t  source=%s",
		device.Path,
		valueOrDash(device.PCI),
		review.Selected[review.Index],
		mstDeviceReasonLabel(device),
	)
	fmt.Fprintf(tty, "    %s\n", fitText(detail, maxInt(10, width-4)))
}

func visibleMSTDeviceRange(total int, selected int, limit int) (int, int) {
	if total <= 0 {
		return 0, 0
	}
	if limit <= 0 || limit >= total {
		return 0, total
	}
	selected = clampIndex(selected, total)
	start := selected - limit/2
	if start < 0 {
		start = 0
	}
	if start+limit > total {
		start = total - limit
	}
	return start, start + limit
}

func renderHiddenMSTDevicesLine(tty *strings.Builder, count int, direction string) {
	if count > 0 {
		fmt.Fprintf(tty, "    ... %d more MST device(s) %s\n", count, direction)
	}
}

func (review *mstDeviceReview) moveSelection(delta int) {
	if len(review.Devices) == 0 {
		return
	}
	review.Index = clampIndex(review.Index+delta, len(review.Devices))
	review.Message = ""
}

func (review *mstDeviceReview) toggleSelected() {
	if review.Index < 0 || review.Index >= len(review.Devices) {
		review.Message = "select a row before toggling"
		return
	}
	review.Selected[review.Index] = !review.Selected[review.Index]
	review.Message = ""
}

func (review *mstDeviceReview) resetDefaultSelection() {
	review.Selected = map[int]bool{}
	for idx, device := range review.Devices {
		if device.Recommended {
			review.Selected[idx] = true
		}
	}
	if len(review.Selected) == 0 {
		for idx := range review.Devices {
			review.Selected[idx] = true
		}
		review.Message = "restored default selection: all discovered MST devices"
		return
	}
	review.Message = "restored recommended RDMA-matched MST devices"
}

func mstDeviceReasonLabel(device mstDevice) string {
	if strings.TrimSpace(device.Reason) != "" {
		return strings.TrimSpace(device.Reason)
	}
	if device.Recommended {
		return "recommended"
	}
	return "not matched to RDMA PCI"
}

func describeMSTDevices(devices []mstDevice) string {
	if len(devices) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(devices))
	for _, device := range devices {
		label := device.Path
		if strings.TrimSpace(device.PCI) != "" {
			label += "(" + strings.TrimSpace(device.PCI) + ")"
		}
		if strings.TrimSpace(device.Net) != "" {
			label += "[" + strings.TrimSpace(device.Net) + "]"
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, ", ")
}

func (a *App) markRecommendedMSTDevices(candidates []mstDevice) {
	rdmaPCIs := a.rdmaPCISet()
	rdmaNames := a.rdmaInterfaceNameSet()
	if len(rdmaPCIs) == 0 && len(rdmaNames) == 0 {
		for idx := range candidates {
			candidates[idx].Reason = "no RDMA PCI correlation available"
		}
		return
	}
	for idx := range candidates {
		netName := strings.TrimSpace(candidates[idx].Net)
		if netName != "" && rdmaNames[netName] {
			candidates[idx].Recommended = true
			candidates[idx].Reason = "matched RDMA NET " + netName
			continue
		}
		pci := normalizePCI(candidates[idx].PCI)
		if pci != "" && rdmaPCIs[pci] {
			candidates[idx].Recommended = true
			candidates[idx].Reason = "matched RDMA PCI " + pci
			continue
		}
		candidates[idx].Reason = "not matched to selected RDMA PCI"
	}
}

func (a *App) rdmaInterfaceNameSet() map[string]bool {
	return stringSliceSet(a.currentRDMAInterfaceNames())
}

func (a *App) currentRDMAInterfaceNames() []string {
	out := map[string]bool{}
	for _, binding := range a.confirmedInterfaceBindings {
		if binding.Kind != "rdma" {
			continue
		}
		addInterfaceName(out, binding.CurrentName)
		addInterfaceName(out, binding.Name)
	}
	for _, item := range a.Machine.RDMA {
		addInterfaceName(out, item.Name)
	}
	names := make([]string, 0, len(out))
	for name := range out {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (a *App) rdmaPCISet() map[string]bool {
	out := map[string]bool{}
	deviceByMAC, _ := a.netDeviceByMAC()
	for _, binding := range a.confirmedInterfaceBindings {
		if binding.Kind != "rdma" {
			continue
		}
		if device, ok := deviceByMAC[strings.ToLower(strings.TrimSpace(binding.MAC))]; ok {
			addPCI(out, device.PCI)
		}
		addPCI(out, a.interfacePCI(binding.CurrentName))
		addPCI(out, a.interfacePCI(binding.Name))
	}
	for _, item := range a.Machine.RDMA {
		if strings.TrimSpace(item.MAC) != "" {
			if device, ok := deviceByMAC[strings.ToLower(strings.TrimSpace(item.MAC))]; ok {
				addPCI(out, device.PCI)
			}
		}
		addPCI(out, a.interfacePCI(item.Name))
	}
	return out
}

func (a *App) interfacePCI(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	devicePath, err := filepath.EvalSymlinks(a.targetPath(filepath.Join("/sys/class/net", name, "device")))
	if err == nil && strings.TrimSpace(devicePath) != "" {
		return normalizePCI(filepath.Base(devicePath))
	}
	if a.DryRun {
		return ""
	}
	output, err := a.captureCmd("", nil, "ethtool", "-i", name)
	if err != nil {
		return ""
	}
	pci, err := parseEthtoolBusInfo(output)
	if err != nil {
		return ""
	}
	return normalizePCI(pci)
}

func addPCI(out map[string]bool, pci string) {
	pci = normalizePCI(pci)
	if pci != "" {
		out[pci] = true
	}
}

func addInterfaceName(out map[string]bool, name string) {
	name = strings.TrimSpace(name)
	if name != "" {
		out[name] = true
	}
}

func normalizeMSTDevicesForSelection(devices []mstDevice) []mstDevice {
	out := make([]mstDevice, 0, len(devices))
	for _, device := range devices {
		out = append(out, mstDevice{
			Path: strings.TrimSpace(device.Path),
			PCI:  normalizePCI(device.PCI),
			Net:  strings.TrimSpace(device.Net),
		})
	}
	return out
}

func stringSliceSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		addInterfaceName(out, value)
	}
	return out
}

func sameStringSet(left []string, right []string) bool {
	leftSet := stringSliceSet(left)
	rightSet := stringSliceSet(right)
	if len(leftSet) != len(rightSet) {
		return false
	}
	for value := range leftSet {
		if !rightSet[value] {
			return false
		}
	}
	return true
}
