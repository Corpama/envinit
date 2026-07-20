package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"envinit/internal/bundle"
	"envinit/internal/checker"
	"envinit/internal/inventory"
	"envinit/internal/runner"
	"envinit/internal/spec"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "plan":
		if err := run(true, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "apply":
		if err := run(false, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "check":
		if err := runCheck(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "discover":
		if err := runDiscover(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintln(os.Stderr, "unknown subcommand:", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func runCheck(args []string) error {
	fs := flag.NewFlagSet("envinit check", flag.ContinueOnError)
	inventoryPath := fs.String("inventory", "", "Path to the inventory file (.csv/.tsv/.txt/.xlsx)")
	bundlePath := fs.String("bundle", "", "Path to the offline installation bundle JSON")
	hostsRaw := fs.String("hosts", "", "Inventory identities or SSH endpoints; identity=endpoint overrides the control address")
	sheet := fs.String("sheet", "", "Worksheet name for .xlsx inventories, defaults to the first sheet")
	checkStageRaw := fs.String("check-stage", "all", "Check stages to run: bandwidth, rdma-ping, xccl, or all")
	checksRaw := fs.String("checks", "", "Deprecated alias for --check-stage")
	emuKVTransfer := fs.Bool("emu-kv-transfer", false, "Enable 8MiB ib_write_bw message size to emulate KV cache transfer")
	bandwidthMmap := fs.String("bandwidth-mmap", "", "Enable bandwidth mmap mode; supported value: xdr")
	bandwidthQPs := fs.Int("bandwidth-qps", 0, "Override ib_write_bw queue pair count (-q)")
	rdmaPingCount := fs.Int("rdma-ping-count", 0, "Override RDMA ping packet count")
	rdmaPingMTU := fs.Int("rdma-ping-mtu", 0, "Override RDMA ping MTU; payload is calculated as MTU-28 for IPv4")
	rdmaPingTimeout := fs.Int("rdma-ping-timeout", 0, "Override RDMA ping timeout in seconds")
	dryRun := fs.Bool("dry-run", false, "Preview check commands without running traffic; XDR mmap performs read-only IB/topology discovery")
	noTUI := fs.Bool("no-tui", false, "Disable the interactive check TUI and print plain text results")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*inventoryPath) == "" {
		return fmt.Errorf("--inventory is required")
	}
	if strings.TrimSpace(*bundlePath) == "" {
		return fmt.Errorf("--bundle is required")
	}
	rawCheckStage := *checkStageRaw
	if strings.TrimSpace(*checksRaw) != "" {
		rawCheckStage = *checksRaw
	}
	runBandwidth, runRDMAPing, runXCCL, err := parseCheckStages(rawCheckStage)
	if err != nil {
		return err
	}
	b, err := bundle.Load(*bundlePath)
	if err != nil {
		return err
	}
	if err := applyCheckOverrides(&b, checkOverrideOptions{
		emuKVTransfer:   *emuKVTransfer,
		bandwidthMmap:   *bandwidthMmap,
		bandwidthQPs:    *bandwidthQPs,
		rdmaPingCount:   *rdmaPingCount,
		rdmaPingMTU:     *rdmaPingMTU,
		rdmaPingTimeout: *rdmaPingTimeout,
	}); err != nil {
		return err
	}
	if runXCCL && !b.Check.XCCL.Enabled && !checkStageExplicitlyRequestsXCCL(rawCheckStage) {
		runXCCL = false
	}
	records, err := inventory.Load(*inventoryPath, *sheet)
	if err != nil {
		return err
	}
	hosts := []string{*hostsRaw}
	var bandwidthModes []string
	if strings.TrimSpace(*hostsRaw) == "" {
		if *noTUI || *dryRun || !stdinIsTerminal() || !stdoutIsTerminal() {
			return fmt.Errorf("--hosts is required in non-interactive mode; omit it in an interactive terminal to select hosts in the check setup TUI")
		}
		selection, err := checker.RunCheckWizard(records, b, runRDMAPing, runBandwidth, runXCCL)
		if err != nil {
			return err
		}
		if selection.Canceled {
			return nil
		}
		b = selection.Bundle
		hosts = selection.Hosts
		runRDMAPing = selection.RunPing
		runBandwidth = selection.RunBandwidth
		runXCCL = selection.RunXCCL
		bandwidthModes = selection.BandwidthModes
	}
	return checker.Run(checker.Options{
		Bundle:         b,
		Records:        records,
		Hosts:          hosts,
		RunBandwidth:   runBandwidth,
		RunRDMAPing:    runRDMAPing,
		RunXCCL:        runXCCL,
		BandwidthModes: bandwidthModes,
		DryRun:         *dryRun,
		LiveOutput:     stdoutIsTerminal() && !*noTUI,
		Output:         os.Stdout,
	})
}

func runDiscover(args []string) error {
	fs := flag.NewFlagSet("envinit discover", flag.ContinueOnError)
	inventoryPath := fs.String("inventory", "", "Path to the inventory file (.csv/.tsv/.txt/.xlsx)")
	bundlePath := fs.String("bundle", "", "Path to the offline installation bundle JSON")
	hostsRaw := fs.String("hosts", "", "Inventory identities, SSH endpoints, or identity=endpoint mappings separated by commas or spaces")
	sheet := fs.String("sheet", "", "Worksheet name for .xlsx inventories, defaults to the first sheet")
	yes := fs.Bool("yes", false, "Accept only exact/strong discovered network mappings without interactive confirmation")
	dryRun := fs.Bool("dry-run", false, "Discover and print planned inventory updates without writing")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*inventoryPath) == "" {
		return fmt.Errorf("--inventory is required")
	}
	if strings.TrimSpace(*bundlePath) == "" {
		return fmt.Errorf("--bundle is required")
	}
	if strings.TrimSpace(*hostsRaw) == "" {
		return fmt.Errorf("--hosts is required")
	}
	b, err := bundle.Load(*bundlePath)
	if err != nil {
		return err
	}
	records, err := inventory.Load(*inventoryPath, *sheet)
	if err != nil {
		return err
	}
	return checker.DiscoverNetwork(checker.DiscoverOptions{
		Bundle:        b,
		Records:       records,
		Hosts:         []string{*hostsRaw},
		InventoryPath: strings.TrimSpace(*inventoryPath),
		Confirm:       !*yes,
		DryRun:        *dryRun,
		Output:        os.Stdout,
	})
}

type checkOverrideOptions struct {
	emuKVTransfer   bool
	bandwidthMmap   string
	bandwidthQPs    int
	rdmaPingCount   int
	rdmaPingMTU     int
	rdmaPingTimeout int
}

func applyCheckOverrides(b *spec.Bundle, opts checkOverrideOptions) error {
	if opts.bandwidthQPs < 0 {
		return fmt.Errorf("--bandwidth-qps must not be negative")
	}
	if opts.rdmaPingCount < 0 {
		return fmt.Errorf("--rdma-ping-count must be greater than 0")
	}
	if opts.rdmaPingTimeout < 0 {
		return fmt.Errorf("--rdma-ping-timeout must be greater than 0")
	}
	if opts.rdmaPingMTU < 0 {
		return fmt.Errorf("--rdma-ping-mtu must be greater than 28")
	}

	b.Check.Bandwidth.MessageSize = 0
	b.Check.Bandwidth.MmapDevice = ""
	if opts.emuKVTransfer {
		b.Check.Bandwidth.MessageSize = 8388608
	}
	switch strings.ToLower(strings.TrimSpace(opts.bandwidthMmap)) {
	case "":
	case "none", "off", "false":
	case "xdr":
		b.Check.Bandwidth.MmapDevice = "/dev/xdrdrv"
	default:
		return fmt.Errorf("--bandwidth-mmap supports only xdr")
	}
	if opts.bandwidthQPs > 0 {
		b.Check.Bandwidth.BandwidthQPs = opts.bandwidthQPs
	}

	if opts.rdmaPingCount > 0 {
		b.Check.RDMAPing.Count = opts.rdmaPingCount
	}
	if opts.rdmaPingTimeout > 0 {
		b.Check.RDMAPing.Timeout = opts.rdmaPingTimeout
	}
	if opts.rdmaPingMTU > 0 {
		if opts.rdmaPingMTU <= 28 {
			return fmt.Errorf("--rdma-ping-mtu must be greater than 28")
		}
		b.Check.RDMAPing.PayloadSize = opts.rdmaPingMTU - 28
	}
	return nil
}

func parseCheckStages(raw string) (bool, bool, bool, error) {
	runBandwidth := false
	runRDMAPing := false
	runXCCL := false
	normalized := strings.NewReplacer(",", " ", ";", " ", "|", " ").Replace(raw)
	for _, item := range strings.Fields(normalized) {
		switch strings.ToLower(strings.TrimSpace(item)) {
		case "all":
			runBandwidth = true
			runRDMAPing = true
			runXCCL = true
		case "bandwidth", "bw", "ib", "ib_write_bw":
			runBandwidth = true
		case "rdma-ping", "rdma_ping", "ping":
			runRDMAPing = true
		case "xccl", "xccl-perf", "xccl_perf", "collective":
			runXCCL = true
		default:
			return false, false, false, fmt.Errorf("unknown check-stage %q; use bandwidth, rdma-ping, xccl, or all", item)
		}
	}
	if !runBandwidth && !runRDMAPing && !runXCCL {
		return false, false, false, fmt.Errorf("--check-stage requires at least one value")
	}
	return runBandwidth, runRDMAPing, runXCCL, nil
}

func checkStageExplicitlyRequestsXCCL(raw string) bool {
	normalized := strings.NewReplacer(",", " ", ";", " ", "|", " ").Replace(raw)
	for _, item := range strings.Fields(normalized) {
		switch strings.ToLower(strings.TrimSpace(item)) {
		case "xccl", "xccl-perf", "xccl_perf", "collective":
			return true
		}
	}
	return false
}

func run(dryRun bool, args []string) error {
	normalizedArgs, err := normalizeArgsForStages(args)
	if err != nil {
		return err
	}

	fs := flag.NewFlagSet("envinit", flag.ContinueOnError)
	inventoryPath := fs.String("inventory", "", "Path to the inventory file (.csv/.tsv/.txt/.xlsx)")
	bundlePath := fs.String("bundle", "", "Path to the offline installation bundle JSON")
	host := fs.String("host", "", "host_id/hostname/mgmt_ip from the inventory; auto-detect the current machine when omitted")
	root := fs.String("root", "/", "Alternate filesystem root for testing, defaults to /")
	sheet := fs.String("sheet", "", "Worksheet name for .xlsx inventories, defaults to the first sheet")
	stagesRaw := fs.String("stages", "all", "Stages separated by commas or spaces: all,software,ofed,network,udev,xre,xdr,firmware,container,mlxconfig,sysctl,kernel,post")
	restart := fs.Bool("restart", false, "Clear saved apply progress and rerun all stages from the beginning")
	plainPlan := fs.Bool("plain", false, "Print plan as plain text instead of opening the interactive plan preview")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(normalizedArgs); err != nil {
		return err
	}
	if strings.TrimSpace(*inventoryPath) == "" {
		return fmt.Errorf("--inventory is required")
	}
	if strings.TrimSpace(*bundlePath) == "" {
		return fmt.Errorf("--bundle is required")
	}

	b, err := bundle.Load(*bundlePath)
	if err != nil {
		return err
	}
	records, err := inventory.Load(*inventoryPath, *sheet)
	if err != nil {
		return err
	}

	stages, err := parseStages(*stagesRaw)
	if err != nil {
		return err
	}
	if *restart && !stages["all"] {
		return fmt.Errorf("--restart can only be used with --stages all")
	}
	if dryRun && *restart {
		return fmt.Errorf("--restart is supported only by apply, not plan")
	}
	out := io.Writer(os.Stdout)
	var dryRunLog bytes.Buffer
	if dryRun {
		out = &dryRunLog
	}
	app, err := runner.New(b, records, *host, *root, dryRun, stages, out)
	if err != nil {
		return err
	}
	app.ResumeApply = !dryRun && stages["all"]
	app.ResetApplyProgress = !dryRun && *restart
	interactivePlan := dryRun && !*plainPlan && stdinIsTerminal() && stdoutIsTerminal()
	if interactivePlan {
		app.InteractiveDryRunReview = true
	}

	if dryRun {
		if err := app.Apply(); err != nil {
			return err
		}
		preview := buildPlanPreview(app, dryRunLog.String())
		if interactivePlan {
			if err := runPlanPreviewTUI(preview); err != nil {
				return err
			}
			return nil
		}
		fmt.Print(renderPlanPreviewPlain(preview))
		return nil
	}

	description, err := app.Describe()
	if err != nil {
		return err
	}
	fmt.Print(description)
	if err := app.Apply(); err != nil {
		return err
	}
	fmt.Println("Initialization completed.")
	return nil
}

func renderPlanPreview(app *runner.App, log string) string {
	return renderPlanPreviewPlain(buildPlanPreview(app, log))
}

type planPreview struct {
	Target     string
	Hostname   string
	Platform   string
	Management string
	RDMA       []string
	Groups     dryRunLogGroups
}

func buildPlanPreview(app *runner.App, log string) planPreview {
	groups := groupDryRunLogByStage(log)
	preview := planPreview{
		Target: app.Machine.HostID,
		Platform: fmt.Sprintf("os_family=%s package_manager=%s network_backend=%s",
			valueOrAuto(app.Bundle.Platform.OSFamily),
			valueOrAuto(app.Bundle.Platform.PackageManager),
			valueOrAuto(app.Bundle.Platform.NetworkBackend),
		),
		Groups: groups,
	}
	if strings.TrimSpace(app.Machine.Hostname) != "" {
		preview.Hostname = app.Machine.Hostname
	}
	if app.Bundle.ConfigureManagementNetwork() && strings.TrimSpace(app.Machine.MgmtIP) != "" {
		preview.Management = fmt.Sprintf("%s/%d via %s, uplink=%s, members=%s",
			app.Machine.MgmtIP,
			app.Machine.MgmtPrefix,
			app.Machine.MgmtGateway,
			managementPlanName(app.Machine.MgmtBondName, app.Machine.MgmtIfaces),
			strings.Join(app.Machine.MgmtIfaces, ","),
		)
	} else {
		preview.Management = "skipped (mgmt_ip is empty or configure_management_network=false)"
	}
	for _, item := range app.Machine.RDMA {
		preview.RDMA = append(preview.RDMA, fmt.Sprintf("%s -> %s/%d via %s table %d", item.Name, item.IP, item.Prefix, item.Gateway, item.Table))
	}
	return preview
}

func renderPlanPreviewPlain(preview planPreview) string {
	var b strings.Builder
	fmt.Fprintln(&b, "Plan preview (dry-run; no changes have been made)")
	fmt.Fprintf(&b, "Target machine: %s\n", preview.Target)
	if preview.Hostname != "" {
		fmt.Fprintf(&b, "Hostname: %s\n", preview.Hostname)
	}
	fmt.Fprintf(&b, "Platform: %s\n", preview.Platform)
	fmt.Fprintf(&b, "Management network: %s\n", preview.Management)
	for _, item := range preview.RDMA {
		fmt.Fprintf(&b, "RDMA: %s\n", item)
	}
	if len(preview.Groups.Prelude) > 0 {
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "Preflight")
		for _, line := range preview.Groups.Prelude {
			fmt.Fprintf(&b, "  - %s\n", line)
		}
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Stages")
	for _, stage := range preview.Groups.Order {
		fmt.Fprintf(&b, "  - %s (%d actions)\n", stage, len(preview.Groups.ByStage[stage]))
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Stage details")
	for _, stage := range preview.Groups.Order {
		fmt.Fprintf(&b, "[%s]\n", stage)
		actions := preview.Groups.ByStage[stage]
		if len(actions) == 0 {
			fmt.Fprintln(&b, "  - no actions")
			continue
		}
		for _, line := range actions {
			fmt.Fprintf(&b, "  - %s\n", line)
		}
	}
	return b.String()
}

type planStageView struct {
	Name    string
	Actions []string
}

type planPreviewModel struct {
	preview planPreview
	stages  []planStageView
	width   int
	height  int
	stage   int
	scroll  int
}

func runPlanPreviewTUI(preview planPreview) error {
	_, err := tea.NewProgram(
		newPlanPreviewModel(preview),
		tea.WithInput(os.Stdin),
		tea.WithOutput(os.Stdout),
		tea.WithAltScreen(),
	).Run()
	return err
}

func newPlanPreviewModel(preview planPreview) planPreviewModel {
	return planPreviewModel{
		preview: preview,
		stages:  planStageViews(preview.Groups),
		width:   100,
		height:  30,
	}
}

func planStageViews(groups dryRunLogGroups) []planStageView {
	stages := []planStageView{}
	if len(groups.Prelude) > 0 {
		stages = append(stages, planStageView{Name: "preflight", Actions: groups.Prelude})
	}
	for _, stage := range groups.Order {
		actions := groups.ByStage[stage]
		if len(actions) == 0 {
			actions = []string{"no actions"}
		}
		stages = append(stages, planStageView{Name: stage, Actions: actions})
	}
	if len(stages) == 0 {
		stages = append(stages, planStageView{Name: "plan", Actions: []string{"no actions"}})
	}
	return stages
}

func (model planPreviewModel) Init() tea.Cmd {
	return nil
}

func (model planPreviewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		model.width = msg.Width
		model.height = msg.Height
		model.clampScroll()
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "Q", "esc":
			return model, tea.Quit
		case "up", "k":
			model.moveStage(-1)
		case "down", "j":
			model.moveStage(1)
		case "pgup", "b":
			model.scroll -= model.pageSize()
			model.clampScroll()
		case "pgdown", "f", " ":
			model.scroll += model.pageSize()
			model.clampScroll()
		case "home":
			model.scroll = 0
		case "end":
			model.scroll = len(model.currentActions())
			model.clampScroll()
		}
	}
	return model, nil
}

func (model planPreviewModel) View() string {
	width := maxInt(model.width, 60)
	height := maxInt(model.height, 18)
	sidebarWidth := 26
	if width < 90 {
		sidebarWidth = 20
	}
	detailWidth := maxInt(width-sidebarWidth-4, 30)

	title := lipgloss.NewStyle().Bold(true).Render("Plan Preview")
	lines := []string{
		title + " (dry-run; no changes have been made)",
		fmt.Sprintf("Target: %s", emptyAsDash(model.preview.Target)),
	}
	if model.preview.Hostname != "" {
		lines = append(lines, fmt.Sprintf("Hostname: %s", model.preview.Hostname))
	}
	lines = append(lines,
		fmt.Sprintf("Platform: %s", model.preview.Platform),
		fmt.Sprintf("Management: %s", model.preview.Management),
	)
	for _, item := range model.preview.RDMA {
		lines = append(lines, fmt.Sprintf("RDMA: %s", item))
	}
	lines = append(lines, "")

	bodyHeight := maxInt(height-len(lines)-3, 8)
	left := model.renderPlanStageList(sidebarWidth, bodyHeight)
	right := model.renderPlanStageDetails(detailWidth, bodyHeight)
	for idx := 0; idx < bodyHeight; idx++ {
		lines = append(lines, fmt.Sprintf("%-*s  %s", sidebarWidth, left[idx], right[idx]))
	}
	lines = append(lines, "")
	lines = append(lines, "Keys: Up/Down stage | PgUp/PgDn scroll | q/Esc close | --plain for text output")
	return strings.Join(lines, "\n")
}

func (model planPreviewModel) renderPlanStageList(width int, height int) []string {
	lines := make([]string, 0, height)
	lines = append(lines, truncateText("Stages", width))
	for idx, stage := range model.stages {
		prefix := "  "
		if idx == model.stage {
			prefix = "> "
		}
		line := fmt.Sprintf("%s%s (%d)", prefix, stage.Name, len(stage.Actions))
		lines = append(lines, truncateText(line, width))
		if len(lines) == height {
			return lines
		}
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines
}

func (model planPreviewModel) renderPlanStageDetails(width int, height int) []string {
	stage := model.stages[model.stage]
	actions := stage.Actions
	total := len(actions)
	start := model.scroll
	end := minInt(start+height-2, total)
	lines := []string{
		truncateText(fmt.Sprintf("[%s] %d action(s)", stage.Name, total), width),
		truncateText(strings.Repeat("-", minInt(width, 32)), width),
	}
	for _, action := range actions[start:end] {
		lines = append(lines, truncateText("- "+action, width))
	}
	if end < total && len(lines) < height {
		lines = append(lines, truncateText(fmt.Sprintf("... %d more action(s)", total-end), width))
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines
}

func (model *planPreviewModel) moveStage(delta int) {
	model.stage += delta
	if model.stage < 0 {
		model.stage = 0
	}
	if model.stage >= len(model.stages) {
		model.stage = len(model.stages) - 1
	}
	model.scroll = 0
}

func (model *planPreviewModel) clampScroll() {
	maxScroll := len(model.currentActions()) - model.pageSize()
	if maxScroll < 0 {
		maxScroll = 0
	}
	if model.scroll < 0 {
		model.scroll = 0
	}
	if model.scroll > maxScroll {
		model.scroll = maxScroll
	}
}

func (model planPreviewModel) currentActions() []string {
	if len(model.stages) == 0 {
		return nil
	}
	return model.stages[model.stage].Actions
}

func (model planPreviewModel) pageSize() int {
	return maxInt(model.height-12, 4)
}

type dryRunLogGroups struct {
	Prelude []string
	Order   []string
	ByStage map[string][]string
}

func groupDryRunLogByStage(log string) dryRunLogGroups {
	groups := dryRunLogGroups{ByStage: map[string][]string{}}
	current := ""
	for _, raw := range strings.Split(log, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if stage, ok := strings.CutPrefix(line, "==> stage: "); ok {
			current = strings.TrimSpace(stage)
			if _, exists := groups.ByStage[current]; !exists {
				groups.Order = append(groups.Order, current)
				groups.ByStage[current] = nil
			}
			continue
		}
		if current == "" {
			groups.Prelude = append(groups.Prelude, line)
			continue
		}
		groups.ByStage[current] = append(groups.ByStage[current], line)
	}
	return groups
}

func valueOrAuto(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "auto"
	}
	return value
}

func stdinIsTerminal() bool {
	return fileIsTerminal(os.Stdin)
}

func stdoutIsTerminal() bool {
	return fileIsTerminal(os.Stdout)
}

func fileIsTerminal(file *os.File) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func managementPlanName(bondName string, ifaces []string) string {
	if len(ifaces) <= 1 {
		if len(ifaces) == 1 {
			return ifaces[0]
		}
		return "-"
	}
	if strings.TrimSpace(bondName) == "" {
		return "bond0"
	}
	return bondName
}

func truncateText(value string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width == 1 {
		return string(runes[:1])
	}
	return string(runes[:width-1]) + "~"
}

func emptyAsDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

func parseStages(raw string) (map[string]bool, error) {
	out := map[string]bool{}
	normalized := strings.ReplaceAll(raw, ",", " ")
	for _, item := range strings.Fields(normalized) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		stage, ok := runner.CanonicalStage(item)
		if !ok {
			return nil, fmt.Errorf("unknown stage %q", item)
		}
		out[stage] = true
	}
	if len(out) == 0 {
		out["all"] = true
	}
	return out, nil
}

func normalizeArgsForStages(args []string) ([]string, error) {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--stages" || arg == "-stages":
			values, next, err := collectStageValues(args, i+1)
			if err != nil {
				return nil, err
			}
			out = append(out, arg, strings.Join(values, ","))
			i = next - 1
		case strings.HasPrefix(arg, "--stages=") || strings.HasPrefix(arg, "-stages="):
			idx := strings.IndexByte(arg, '=')
			base := arg[:idx+1]
			initial := arg[idx+1:]
			values := []string{}
			if strings.TrimSpace(initial) != "" {
				values = append(values, initial)
			}
			collected, next, err := collectStageValues(args, i+1)
			if err != nil && len(values) == 0 {
				return nil, err
			}
			values = append(values, collected...)
			if len(values) == 0 {
				return nil, fmt.Errorf("--stages requires at least one value")
			}
			out = append(out, base+strings.Join(values, ","))
			i = next - 1
		default:
			out = append(out, arg)
		}
	}
	return out, nil
}

func collectStageValues(args []string, start int) ([]string, int, error) {
	values := make([]string, 0, 2)
	i := start
	for ; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			break
		}
		values = append(values, args[i])
	}
	if len(values) == 0 {
		return nil, start, fmt.Errorf("--stages requires at least one value")
	}
	return values, i, nil
}

func usage() {
	fmt.Fprintf(os.Stderr, `envinit

Usage:
  envinit plan  --inventory ./machines.xlsx --bundle ./bundle.json [--host xpu11] [--plain]
  envinit apply --inventory ./machines.xlsx --bundle ./bundle.json [--host xpu11] [--restart]
  envinit discover --inventory ./machines.csv --bundle ./bundle.json --hosts xpu11=192.168.32.11 [--yes]
  envinit check --inventory ./machines.csv --bundle ./bundle.json [--hosts xpu11,xpu12] [--check-stage bandwidth|rdma-ping|xccl|all]

Notes:
  plan   Parse the inventory and preview planned actions in a stage-by-stage TUI; use --plain for text output
  apply  Write files and execute commands; root privileges are required; default all-stage runs resume from saved progress
  discover  Discover mgmt_ip plus rdmaN_name/rdmaN_ip for one or more hosts and write them back to CSV/TSV/TXT inventory
  check  Open an interactive host/check/parameter setup TUI when --hosts is omitted; retain flags for automation and --no-tui

Discover options:
  --hosts    inventory identity, SSH endpoint, or an explicit identity=endpoint mapping
  --yes      accept discovered network fields without interactive confirmation
  --dry-run  preview discovered fields without writing inventory
`)
}
