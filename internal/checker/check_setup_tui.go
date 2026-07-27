package checker

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"envinit/internal/spec"
)

type CheckWizardSelection struct {
	Bundle         spec.Bundle
	Hosts          []string
	RunPing        bool
	RunBandwidth   bool
	RunXCCL        bool
	BandwidthModes []string
	Canceled       bool
}

type checkWizardModel struct {
	records       []spec.MachineRecord
	bundle        spec.Bundle
	selectedHosts []bool
	runPing       bool
	runBandwidth  bool
	runXCCL       bool
	verbs         bool
	rdmaCM        bool
	page          int
	cursor        int
	width         int
	height        int
	editing       bool
	editKey       string
	editBuffer    string
	notice        string
	accepted      bool
	canceled      bool
}

const (
	checkWizardPageHosts = iota
	checkWizardPageChecks
	checkWizardPageParameters
	checkWizardPageEnvironment
	checkWizardPageReview
)

func RunCheckWizard(records []spec.MachineRecord, bundle spec.Bundle, runPing, runBandwidth, runXCCL bool) (CheckWizardSelection, error) {
	model := newCheckWizardModel(records, bundle, runPing, runBandwidth, runXCCL)
	program := tea.NewProgram(model, tea.WithAltScreen())
	result, err := program.Run()
	if err != nil {
		return CheckWizardSelection{}, err
	}
	final, ok := result.(checkWizardModel)
	if !ok {
		return CheckWizardSelection{}, fmt.Errorf("check setup returned an unexpected model")
	}
	selection := CheckWizardSelection{Bundle: final.bundle, RunPing: final.runPing, RunBandwidth: final.runBandwidth, RunXCCL: final.runXCCL, Canceled: final.canceled || !final.accepted}
	for index, selected := range final.selectedHosts {
		if selected {
			selection.Hosts = append(selection.Hosts, checkWizardHostIdentity(final.records[index]))
		}
	}
	if final.verbs {
		selection.BandwidthModes = append(selection.BandwidthModes, BandwidthModeVerbs)
	}
	if final.rdmaCM {
		selection.BandwidthModes = append(selection.BandwidthModes, BandwidthModeRDMACM)
	}
	return selection, nil
}

func newCheckWizardModel(records []spec.MachineRecord, bundle spec.Bundle, runPing, runBandwidth, runXCCL bool) checkWizardModel {
	if bundle.Check.Bandwidth.Duration <= 1 {
		bundle.Check.Bandwidth.Duration = 10
	}
	if bundle.Check.Bandwidth.MessageSize == 0 {
		bundle.Check.Bandwidth.MessageSize = 1048576
	}
	bundle.Check.Bandwidth.RunByDuration = true
	return checkWizardModel{
		records: records, bundle: bundle, selectedHosts: make([]bool, len(records)),
		runPing: runPing, runBandwidth: runBandwidth, runXCCL: runXCCL,
		verbs: true, rdmaCM: true, width: 100, height: 32,
	}
}

func (m checkWizardModel) Init() tea.Cmd { return nil }

func (m checkWizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		if m.editing {
			return m.updateEditor(msg)
		}
		switch msg.String() {
		case "ctrl+c", "q", "Q":
			m.canceled = true
			return m, tea.Quit
		case "esc", "b", "B":
			if m.page == checkWizardPageHosts {
				m.canceled = true
				return m, tea.Quit
			}
			if m.page == checkWizardPageReview && !m.runXCCL {
				m.page = checkWizardPageParameters
			} else {
				m.page--
			}
			m.cursor = 0
			m.notice = ""
		case "up", "k":
			m.moveCursor(-1)
		case "down", "j":
			m.moveCursor(1)
		case " ":
			m.toggleCurrent()
		case "left", "h":
			m.adjustCurrent(-1)
		case "right", "l":
			m.adjustCurrent(1)
		case "a", "A":
			if m.page == checkWizardPageHosts {
				all := m.selectedHostCount() != len(m.selectedHosts)
				for index := range m.selectedHosts {
					m.selectedHosts[index] = all
				}
			}
		case "e", "E":
			if m.page == checkWizardPageParameters && m.currentParamEditable() {
				m.startEditor()
			}
		case "enter":
			if m.page == checkWizardPageReview {
				if err := m.validate(); err != nil {
					m.notice = err.Error()
					break
				}
				m.accepted = true
				return m, tea.Quit
			}
			if err := m.validatePage(); err != nil {
				m.notice = err.Error()
				break
			}
			if m.page == checkWizardPageHosts {
				m.applySelectedHostScope()
			}
			if m.page == checkWizardPageParameters && !m.runXCCL {
				m.page = checkWizardPageReview
			} else {
				m.page++
			}
			m.cursor = 0
			m.notice = ""
		}
	}
	return m, nil
}

func (m checkWizardModel) updateEditor(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.editing = false
		m.editKey, m.editBuffer = "", ""
	case "enter":
		if err := m.applyEditedValue(); err != nil {
			m.notice = err.Error()
			return m, nil
		}
		m.editing = false
		m.editKey, m.editBuffer = "", ""
		m.notice = "Value updated for this run"
	case "backspace", "ctrl+h":
		if len(m.editBuffer) > 0 {
			_, size := utf8.DecodeLastRuneInString(m.editBuffer)
			m.editBuffer = m.editBuffer[:len(m.editBuffer)-size]
		}
	default:
		for _, r := range msg.Runes {
			if r >= 32 && r != 127 {
				m.editBuffer += string(r)
			}
		}
	}
	return m, nil
}

func (m checkWizardModel) View() string {
	width := maxIntMain(40, m.width)
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39")).Render("envinit check setup")
	steps := []string{"1 Hosts", "2 Checks", "3 Parameters", "4 Environment", "5 Review"}
	for index := range steps {
		if index == m.page {
			steps[index] = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("45")).Render("[" + steps[index] + "]")
		}
	}
	var body string
	switch m.page {
	case checkWizardPageHosts:
		body = m.renderHosts(width)
	case checkWizardPageChecks:
		body = m.renderChecks(width)
	case checkWizardPageParameters:
		body = m.renderParameters(width)
	case checkWizardPageEnvironment:
		body = m.renderEnvironment(width)
	default:
		body = m.renderReview(width)
	}
	footer := "Up/Down select | Space toggle | Enter continue | b back | q exit"
	if m.page == checkWizardPageHosts {
		footer = "Up/Down select | Space toggle | a all/none | Enter continue | q exit"
	} else if m.page == checkWizardPageParameters {
		footer = "Up/Down select | Space toggle | Left/Right choose | e edit | Enter environment | b back | q exit"
	} else if m.page == checkWizardPageEnvironment {
		footer = "Up/Down scroll environment | Enter review | b back | q exit"
	} else if m.page == checkWizardPageReview {
		footer = "Enter run | b back | q exit"
	}
	if m.editing {
		footer = "Editing " + m.editKey + ": " + m.editBuffer + "_  | Enter save | Esc cancel"
	}
	if m.notice != "" {
		footer = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render(m.notice) + "\n" + footer
	}
	content := fmt.Sprintf("%s  %s\n\n%s", title, strings.Join(steps, "  "), body)
	return pinWizardFooter(content, footer, width, maxIntMain(8, m.height))
}

func (m checkWizardModel) renderHosts(width int) string {
	var b strings.Builder
	b.WriteString("Select inventory hosts for this run. No remote command is executed on this page.\n\n")
	b.WriteString("  SELECT  HOST                         MGMT IP             RDMA\n")
	b.WriteString("  ------  ---------------------------  ------------------  ----\n")
	for index, record := range m.records {
		cursor := " "
		if index == m.cursor {
			cursor = ">"
		}
		mark := "[ ]"
		if m.selectedHosts[index] {
			mark = "[x]"
		}
		fmt.Fprintf(&b, "%s %-6s  %-27s  %-18s  %d\n", cursor, mark, truncateWizard(checkWizardHostIdentity(record), 27), truncateWizard(valueOrDash(record.MgmtIP), 18), len(record.RDMA))
	}
	if len(m.records) == 0 {
		b.WriteString("  Inventory contains no hosts.\n")
	}
	return b.String()
}

func (m checkWizardModel) renderChecks(width int) string {
	rows := m.checkRows()
	var b strings.Builder
	if m.selectedHostCount() == 1 {
		b.WriteString("Single-host mode supports XCCL only. Ping and Bandwidth require at least two hosts.\n\n")
	} else {
		b.WriteString("Choose one or more checks. Bundle values are used as defaults.\n\n")
	}
	for index, row := range rows {
		cursor := " "
		if index == m.cursor {
			cursor = ">"
		}
		mark := "[ ]"
		if row.enabled {
			mark = "[x]"
		}
		fmt.Fprintf(&b, "%s %s %-12s %s\n", cursor, mark, row.name, row.description)
	}
	return b.String()
}

type wizardCheckRow struct {
	key, name, description string
	enabled                bool
}

func (m checkWizardModel) checkRows() []wizardCheckRow {
	if m.selectedHostCount() == 1 {
		return []wizardCheckRow{{"xccl", "XCCL", "XPU collective communication performance", m.runXCCL}}
	}
	return []wizardCheckRow{
		{"ping", "Ping", "RoCE IPv4, MTU and full cross-matrix reachability", m.runPing},
		{"bandwidth", "Bandwidth", "ib_write_bw using Verbs and/or RDMA-CM", m.runBandwidth},
		{"xccl", "XCCL", "XPU collective communication performance", m.runXCCL},
	}
}

type wizardParamRow struct {
	key, label, value string
	toggle, cycle     bool
}

func (m checkWizardModel) parameterRows() []wizardParamRow {
	var rows []wizardParamRow
	if m.runPing {
		rows = append(rows,
			wizardParamRow{"ping_count", "Ping / packet count", strconv.Itoa(m.bundle.Check.RDMAPing.Count), false, false},
			wizardParamRow{"ping_payload", "Ping / payload bytes", strconv.Itoa(m.bundle.Check.RDMAPing.PayloadSize), false, false},
			wizardParamRow{"ping_timeout", "Ping / timeout seconds", strconv.Itoa(m.bundle.Check.RDMAPing.Timeout), false, false})
	}
	if m.runBandwidth {
		rows = append(rows,
			wizardParamRow{"bw_verbs", "Bandwidth / Verbs", boolLabel(m.verbs), true, false},
			wizardParamRow{"bw_cm", "Bandwidth / RDMA-CM", boolLabel(m.rdmaCM), true, false},
			wizardParamRow{"bw_run_mode", "Bandwidth / run mode", bandwidthRunModeLabel(m.bundle.Check.Bandwidth), false, true})
		if m.bundle.Check.Bandwidth.RunByDuration {
			rows = append(rows, wizardParamRow{"bw_duration", "Bandwidth / duration seconds", strconv.Itoa(m.bundle.Check.Bandwidth.Duration), false, false})
		} else {
			rows = append(rows, wizardParamRow{"bw_iterations", "Bandwidth / iterations", strconv.Itoa(m.bundle.Check.Bandwidth.Iterations), false, false})
		}
		rows = append(rows,
			wizardParamRow{"bw_size", "Bandwidth / message bytes", strconv.Itoa(m.bundle.Check.Bandwidth.MessageSize), false, false},
			wizardParamRow{"bw_qps", "Bandwidth / QPs (0=perftest default)", strconv.Itoa(m.bundle.Check.Bandwidth.BandwidthQPs), false, false},
			wizardParamRow{"bw_gid", "Bandwidth / Verbs GID index", strconv.Itoa(m.bundle.Check.Bandwidth.GIDIndex), false, false},
			wizardParamRow{"bw_parallel", "Bandwidth / parallel batches", boolLabel(m.bundle.Check.Bandwidth.Parallel), true, false},
			wizardParamRow{"bw_port", "Bandwidth / base port", strconv.Itoa(m.bundle.Check.Bandwidth.BasePort), false, false},
			wizardParamRow{"bw_memory", "Bandwidth / memory backend", bandwidthMemoryLabel(m.bundle.Check.Bandwidth), false, true},
			wizardParamRow{"bw_threshold", "Bandwidth / threshold mode", m.bundle.Check.Bandwidth.MinGBitsMode(), false, true})
		if m.bundle.Check.Bandwidth.MinGBitsMode() == "manual" {
			rows = append(rows, wizardParamRow{"bw_min_gbits", "Bandwidth / minimum Gbps", strconv.FormatFloat(m.bundle.Check.Bandwidth.MinGBits, 'f', -1, 64), false, false})
		}
	}
	if m.runXCCL {
		multiHost := m.selectedHostCount() > 1
		scope := "single_host"
		if multiHost {
			scope = "multi_host"
			rows = append(rows,
				wizardParamRow{"xccl_scope", "XCCL / execution scope", scope, false, false},
				wizardParamRow{"xccl_layout", "XCCL / multi-host layout", normalizedXCCLLayout(m.bundle.Check.XCCL.Layout), false, true},
				wizardParamRow{"xccl_xpu_ordering", "XCCL / XPU ordering", xcclXPUOrderingLabel(m.bundle.Check.XCCL), false, true})
		} else {
			rows = append(rows, wizardParamRow{"xccl_scope", "XCCL / execution scope", scope, false, false})
		}
		evaluationValue := xcclWizardEvaluationLabel(m.bundle.Check.XCCL, multiHost)
		evaluationCycle := multiHost
		rows = append(rows,
			wizardParamRow{"xccl_ranks", "XCCL / MPI ranks (0=auto)", strconv.Itoa(m.bundle.Check.XCCL.Ranks), false, false},
			wizardParamRow{"xccl_evaluation", "XCCL / evaluation mode", evaluationValue, false, evaluationCycle})
		if multiHost {
			if normalizedXCCLLayout(m.bundle.Check.XCCL.Layout) == "full_ring" && normalizedXCCLEvaluationMode(m.bundle.Check.XCCL.EvaluationMode) == "auto" {
				rows = append(rows, wizardParamRow{"xccl_machine", "XCCL / full-ring machine class", xcclMachineClassLabel(m.bundle.Check.XCCL.MachineClass), false, true})
			}
			if normalizedXCCLLayout(m.bundle.Check.XCCL.Layout) == "same_index" {
				rows = append(rows,
					wizardParamRow{"xccl_split_step", "XCCL / same-index split step", strconv.Itoa(m.bundle.Check.XCCL.SplitStep), false, false},
					wizardParamRow{"xccl_split_op", "XCCL / same-index split operation", strconv.Itoa(m.bundle.Check.XCCL.SplitOperation), false, false})
			}
		}
		if normalizedXCCLEvaluationMode(m.bundle.Check.XCCL.EvaluationMode) == "manual" {
			rows = append(rows, wizardParamRow{"xccl_min_bus", "XCCL / manual minimum bus BW GB/s", strconv.FormatFloat(m.bundle.Check.XCCL.MinBusBandwidthGBs, 'f', -1, 64), false, false})
		}
		rows = append(rows,
			wizardParamRow{"xccl_test", "XCCL / collective", m.bundle.Check.XCCL.Test, false, true},
			wizardParamRow{"xccl_min", "XCCL / minimum bytes", m.bundle.Check.XCCL.MinBytes, false, false},
			wizardParamRow{"xccl_max", "XCCL / maximum bytes", m.bundle.Check.XCCL.MaxBytes, false, false},
			wizardParamRow{"xccl_step", "XCCL / size step factor", strconv.Itoa(m.bundle.Check.XCCL.StepFactor), false, false},
			wizardParamRow{"xccl_warmup", "XCCL / warmup iterations", strconv.Itoa(m.bundle.Check.XCCL.WarmupIterations), false, false},
			wizardParamRow{"xccl_iters", "XCCL / iterations", strconv.Itoa(m.bundle.Check.XCCL.Iterations), false, false},
			wizardParamRow{"xccl_dtype", "XCCL / data type", m.bundle.Check.XCCL.DataType, false, false},
			wizardParamRow{"xccl_timeout", "XCCL / timeout seconds", strconv.Itoa(m.bundle.Check.XCCL.Timeout), false, false},
			wizardParamRow{"xccl_xdr", "XCCL / XDR", boolLabel(boolPointerValue(m.bundle.Check.XCCL.EnableXDR)), true, false},
			wizardParamRow{"xccl_supernode", "XCCL / supernode profile", boolLabel(m.bundle.Check.XCCL.Supernode), true, false},
			wizardParamRow{"xccl_socket", "XCCL / socket interface (-=first RDMA)", valueOrDash(m.bundle.Check.XCCL.SocketInterface), false, false})
		if multiHost {
			rows = append(rows, wizardParamRow{"xccl_validate_topology", "XCCL / cross-host topology validation", boolLabel(m.bundle.Check.XCCL.TopologyValidationEnabled()), true, false})
		}
	}
	return rows
}

func (m checkWizardModel) renderParameters(width int) string {
	rows := m.parameterRows()
	var b strings.Builder
	visible := maxIntMain(3, m.height-9)
	start := 0
	if len(rows) > visible {
		start = minIntMain(len(rows)-visible, maxIntMain(0, m.cursor-visible/2))
	}
	end := minIntMain(len(rows), start+visible)
	fmt.Fprintf(&b, "Configure this run. Changes are temporary and do not rewrite bundle.json.  rows %d-%d/%d\n\n", start+1, end, len(rows))
	for index := start; index < end; index++ {
		row := rows[index]
		cursor := " "
		if index == m.cursor {
			cursor = ">"
		}
		fmt.Fprintf(&b, "%s %-38s %s\n", cursor, row.label, row.value)
	}
	b.WriteString("\nSpace toggles booleans; Left/Right cycles modes; e edits the selected value.")
	return b.String()
}

func (m checkWizardModel) environmentRows() []wizardParamRow {
	if !m.runXCCL {
		return nil
	}
	cfg := effectiveXCCLConfig(m.bundle.Check.XCCL, m.selectedHostCount() > 1)
	env := xcclPreviewRankEnvironment(cfg, m.selectedHostCount() > 1)
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rows := make([]wizardParamRow, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, wizardParamRow{"env_" + key, key, env[key], false, false})
	}
	return rows
}

func (m checkWizardModel) renderEnvironment(width int) string {
	rows := m.environmentRows()
	var b strings.Builder
	if len(rows) == 0 {
		return "No XCCL environment is configured for this run."
	}
	visible := maxIntMain(3, m.height-9)
	start := 0
	if len(rows) > visible {
		start = minIntMain(len(rows)-visible, maxIntMain(0, m.cursor-visible/2))
	}
	end := minIntMain(len(rows), start+visible)
	scope := "single_host"
	if m.selectedHostCount() > 1 {
		scope = "multi_host"
	}
	fmt.Fprintf(&b, "XCCL rank environment. scope=%s; per-host topology values are resolved before launch.  rows %d-%d/%d\n\n", scope, start+1, end, len(rows))
	for index := start; index < end; index++ {
		cursor := " "
		if index == m.cursor {
			cursor = ">"
		}
		fmt.Fprintf(&b, "%s %-34s %s\n", cursor, rows[index].label, truncateWizard(rows[index].value, maxIntMain(20, width-40)))
	}
	return b.String()
}

func (m checkWizardModel) renderReview(width int) string {
	selected := m.selectedRecords()
	var names []string
	for _, record := range selected {
		names = append(names, checkWizardHostIdentity(record))
	}
	paths := wizardPathCount(selected)
	var b strings.Builder
	fmt.Fprintf(&b, "Hosts: %d  %s\n\n", len(selected), strings.Join(names, ", "))
	if m.runPing {
		fmt.Fprintf(&b, "[x] Ping       estimated paths: %d  count=%d payload=%d timeout=%ds\n", paths, m.bundle.Check.RDMAPing.Count, m.bundle.Check.RDMAPing.PayloadSize, m.bundle.Check.RDMAPing.Timeout)
	}
	if m.runBandwidth {
		var modes []string
		if m.verbs {
			modes = append(modes, "Verbs")
		}
		if m.rdmaCM {
			modes = append(modes, "RDMA-CM")
		}
		qps := strconv.Itoa(m.bundle.Check.Bandwidth.BandwidthQPs)
		if m.bundle.Check.Bandwidth.BandwidthQPs == 0 {
			qps = "perftest-default"
		}
		runValue := fmt.Sprintf("duration=%ds", m.bundle.Check.Bandwidth.Duration)
		if !m.bundle.Check.Bandwidth.RunByDuration {
			runValue = fmt.Sprintf("iterations=%d", m.bundle.Check.Bandwidth.Iterations)
		}
		fmt.Fprintf(&b, "[x] Bandwidth  modes=%s paths/mode=%d %s size=%d QPs=%s parallel=%v threshold=%s memory=%s\n", strings.Join(modes, ","), paths, runValue, m.bundle.Check.Bandwidth.MessageSize, qps, m.bundle.Check.Bandwidth.Parallel, bandwidthThresholdReview(m.bundle.Check.Bandwidth), bandwidthMemoryLabel(m.bundle.Check.Bandwidth))
	}
	if m.runXCCL {
		multiHost := len(selected) > 1
		xcclConfig := effectiveXCCLConfig(m.bundle.Check.XCCL, multiHost)
		ranks := "auto"
		if m.bundle.Check.XCCL.Ranks > 0 {
			ranks = strconv.Itoa(m.bundle.Check.XCCL.Ranks) + " (manual)"
		}
		scope := "single_host"
		if multiHost {
			scope = "multi_host"
		}
		fmt.Fprintf(&b, "[x] XCCL       scope=%s layout=%s ordering=%s ranks=%s collective=%s range=%s..%s iterations=%d dtype=%s evaluation=%s XDR=%v", scope, normalizedXCCLLayout(xcclConfig.Layout), xcclXPUOrderingLabel(xcclConfig), ranks, xcclConfig.Test, xcclConfig.MinBytes, xcclConfig.MaxBytes, xcclConfig.Iterations, xcclConfig.DataType, xcclEvaluationReview(xcclConfig), boolPointerValue(xcclConfig.EnableXDR))
		if multiHost {
			fmt.Fprintf(&b, " topology-validation=%v", xcclConfig.TopologyValidationEnabled())
		}
		b.WriteByte('\n')
		fmt.Fprintf(&b, "    Environment: %d variables; see the Environment page for common and per-host-resolved values.\n", len(m.environmentRows()))
	}
	b.WriteString("\nPress Enter to build the execution plan and start read-only discovery/testing.")
	return b.String()
}

func (m *checkWizardModel) moveCursor(delta int) {
	count := m.rowCount()
	if count == 0 {
		m.cursor = 0
		return
	}
	m.cursor = (m.cursor + delta + count) % count
}

func (m checkWizardModel) rowCount() int {
	switch m.page {
	case checkWizardPageHosts:
		return len(m.records)
	case checkWizardPageChecks:
		return len(m.checkRows())
	case checkWizardPageParameters:
		return len(m.parameterRows())
	case checkWizardPageEnvironment:
		return len(m.environmentRows())
	default:
		return 1
	}
}

func (m *checkWizardModel) toggleCurrent() {
	switch m.page {
	case checkWizardPageHosts:
		if m.cursor < len(m.selectedHosts) {
			m.selectedHosts[m.cursor] = !m.selectedHosts[m.cursor]
		}
	case checkWizardPageChecks:
		rows := m.checkRows()
		if m.cursor >= len(rows) {
			return
		}
		switch rows[m.cursor].key {
		case "ping":
			m.runPing = !m.runPing
		case "bandwidth":
			m.runBandwidth = !m.runBandwidth
		case "xccl":
			m.runXCCL = !m.runXCCL
		}
	case checkWizardPageParameters:
		rows := m.parameterRows()
		if m.cursor >= len(rows) {
			return
		}
		switch rows[m.cursor].key {
		case "bw_verbs":
			m.verbs = !m.verbs
		case "bw_cm":
			m.rdmaCM = !m.rdmaCM
		case "bw_parallel":
			m.bundle.Check.Bandwidth.Parallel = !m.bundle.Check.Bandwidth.Parallel
		case "xccl_xdr":
			value := !boolPointerValue(m.bundle.Check.XCCL.EnableXDR)
			m.bundle.Check.XCCL.EnableXDR = &value
		case "xccl_validate_topology":
			value := !m.bundle.Check.XCCL.TopologyValidationEnabled()
			m.bundle.Check.XCCL.ValidateTopology = &value
		case "xccl_supernode":
			m.bundle.Check.XCCL.Supernode = !m.bundle.Check.XCCL.Supernode
		}
	}
}

func (m *checkWizardModel) adjustCurrent(delta int) {
	if m.page != checkWizardPageParameters {
		return
	}
	rows := m.parameterRows()
	if m.cursor >= len(rows) {
		return
	}
	row := rows[m.cursor]
	if row.toggle {
		m.toggleCurrent()
		return
	}
	switch row.key {
	case "xccl_layout":
		values := []string{"full_ring", "same_index"}
		index := stringIndex(values, normalizedXCCLLayout(m.bundle.Check.XCCL.Layout))
		m.bundle.Check.XCCL.Layout = values[(index+delta+len(values))%len(values)]
	case "xccl_xpu_ordering":
		values := []string{"auto", "rail_aligned", "physical"}
		index := stringIndex(values, normalizedXCCLXPUOrdering(m.bundle.Check.XCCL.XPUOrdering))
		m.bundle.Check.XCCL.XPUOrdering = values[(index+delta+len(values))%len(values)]
	case "xccl_machine":
		values := []string{"auto", "vc", "vd"}
		index := stringIndex(values, normalizedXCCLMachineClass(m.bundle.Check.XCCL.MachineClass))
		m.bundle.Check.XCCL.MachineClass = values[(index+delta+len(values))%len(values)]
	case "xccl_evaluation":
		values := []string{"auto", "manual", "disabled"}
		index := stringIndex(values, normalizedXCCLEvaluationMode(m.bundle.Check.XCCL.EvaluationMode))
		m.bundle.Check.XCCL.EvaluationMode = values[(index+delta+len(values))%len(values)]
		if m.bundle.Check.XCCL.EvaluationMode == "manual" && m.bundle.Check.XCCL.MinBusBandwidthGBs <= 0 {
			m.bundle.Check.XCCL.MinBusBandwidthGBs = 1
		}
	case "bw_run_mode":
		m.bundle.Check.Bandwidth.RunByDuration = !m.bundle.Check.Bandwidth.RunByDuration
	case "bw_memory":
		if strings.TrimSpace(m.bundle.Check.Bandwidth.MmapDevice) == "" {
			m.bundle.Check.Bandwidth.MmapDevice = "/dev/xdrdrv"
		} else {
			m.bundle.Check.Bandwidth.MmapDevice = ""
		}
	case "bw_threshold":
		modes := []string{"auto", "disabled", "manual"}
		index := stringIndex(modes, m.bundle.Check.Bandwidth.MinGBitsMode())
		next := modes[(index+delta+len(modes))%len(modes)]
		m.bundle.Check.Bandwidth.MinGBitsSet = true
		switch next {
		case "auto":
			m.bundle.Check.Bandwidth.MinGBitsAuto = true
			m.bundle.Check.Bandwidth.MinGBits = 0
		case "disabled":
			m.bundle.Check.Bandwidth.MinGBitsAuto = false
			m.bundle.Check.Bandwidth.MinGBits = 0
		case "manual":
			m.bundle.Check.Bandwidth.MinGBitsAuto = false
			if m.bundle.Check.Bandwidth.MinGBits <= 0 {
				m.bundle.Check.Bandwidth.MinGBits = 1
			}
		}
	case "xccl_test":
		values := []string{"all_reduce", "all_gather", "reduce_scatter", "all_to_all", "broadcast", "reduce", "sendrecv"}
		index := 0
		for i, value := range values {
			if value == m.bundle.Check.XCCL.Test {
				index = i
			}
		}
		m.bundle.Check.XCCL.Test = values[(index+delta+len(values))%len(values)]
	}
}

func (m checkWizardModel) currentParamEditable() bool {
	rows := m.parameterRows()
	return m.cursor < len(rows) && !rows[m.cursor].toggle && !rows[m.cursor].cycle
}

func (m *checkWizardModel) startEditor() {
	rows := m.parameterRows()
	if m.cursor >= len(rows) {
		return
	}
	m.editing, m.editKey, m.editBuffer = true, rows[m.cursor].key, rows[m.cursor].value
	m.notice = ""
}

func (m *checkWizardModel) applyEditedValue() error {
	value := strings.TrimSpace(m.editBuffer)
	positive := func(label string, allowZero bool) (int, error) {
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 || (!allowZero && n == 0) {
			return 0, fmt.Errorf("%s must be %s", label, map[bool]string{true: "zero or a positive integer", false: "a positive integer"}[allowZero])
		}
		return n, nil
	}
	nonNegativeFloat := func(label string) (float64, error) {
		n, err := strconv.ParseFloat(value, 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("%s must be zero or a positive number", label)
		}
		return n, nil
	}
	switch m.editKey {
	case "ping_count":
		n, err := positive("ping count", false)
		if err != nil {
			return err
		}
		m.bundle.Check.RDMAPing.Count = n
	case "ping_payload":
		n, err := positive("ping payload", false)
		if err != nil {
			return err
		}
		m.bundle.Check.RDMAPing.PayloadSize = n
	case "ping_timeout":
		n, err := positive("ping timeout", false)
		if err != nil {
			return err
		}
		m.bundle.Check.RDMAPing.Timeout = n
	case "bw_duration":
		n, err := positive("bandwidth duration", false)
		if err != nil {
			return err
		}
		m.bundle.Check.Bandwidth.Duration = n
	case "bw_iterations":
		n, err := positive("bandwidth iterations", false)
		if err != nil {
			return err
		}
		m.bundle.Check.Bandwidth.Iterations = n
	case "bw_size":
		n, err := positive("bandwidth message size", false)
		if err != nil {
			return err
		}
		m.bundle.Check.Bandwidth.MessageSize = n
	case "bw_qps":
		n, err := positive("bandwidth QPs", true)
		if err != nil {
			return err
		}
		m.bundle.Check.Bandwidth.BandwidthQPs = n
	case "bw_gid":
		n, err := positive("bandwidth GID index", true)
		if err != nil {
			return err
		}
		m.bundle.Check.Bandwidth.GIDIndex = n
	case "bw_port":
		n, err := positive("bandwidth base port", false)
		if err != nil || n > 65535 {
			return fmt.Errorf("bandwidth base port must be between 1 and 65535")
		}
		m.bundle.Check.Bandwidth.BasePort = n
	case "bw_min_gbits":
		n, err := nonNegativeFloat("bandwidth minimum Gbps")
		if err != nil || n <= 0 {
			return fmt.Errorf("bandwidth minimum Gbps must be positive in manual mode")
		}
		m.bundle.Check.Bandwidth.MinGBits = n
	case "xccl_min":
		if value == "" {
			return fmt.Errorf("XCCL minimum bytes is required")
		}
		m.bundle.Check.XCCL.MinBytes = value
	case "xccl_ranks":
		n, err := positive("XCCL MPI ranks", true)
		if err != nil {
			return err
		}
		m.bundle.Check.XCCL.Ranks = n
	case "xccl_max":
		if value == "" {
			return fmt.Errorf("XCCL maximum bytes is required")
		}
		m.bundle.Check.XCCL.MaxBytes = value
	case "xccl_step":
		n, err := positive("XCCL size step factor", false)
		if err != nil || n < 2 {
			return fmt.Errorf("XCCL size step factor must be at least 2")
		}
		m.bundle.Check.XCCL.StepFactor = n
	case "xccl_warmup":
		n, err := positive("XCCL warmup iterations", true)
		if err != nil {
			return err
		}
		m.bundle.Check.XCCL.WarmupIterations = n
	case "xccl_iters":
		n, err := positive("XCCL iterations", false)
		if err != nil {
			return err
		}
		m.bundle.Check.XCCL.Iterations = n
	case "xccl_dtype":
		if value == "" {
			return fmt.Errorf("XCCL data type is required")
		}
		m.bundle.Check.XCCL.DataType = value
	case "xccl_timeout":
		n, err := positive("XCCL timeout", false)
		if err != nil {
			return err
		}
		m.bundle.Check.XCCL.Timeout = n
	case "xccl_split_step":
		n, err := positive("XCCL split step", false)
		if err != nil {
			return err
		}
		m.bundle.Check.XCCL.SplitStep = n
	case "xccl_split_op":
		n, err := positive("XCCL split operation", true)
		if err != nil {
			return err
		}
		m.bundle.Check.XCCL.SplitOperation = n
	case "xccl_socket":
		if value == "-" || strings.EqualFold(value, "auto") {
			value = ""
		}
		m.bundle.Check.XCCL.SocketInterface = value
	case "xccl_min_bus":
		n, err := nonNegativeFloat("XCCL minimum bus bandwidth")
		if err != nil {
			return err
		}
		m.bundle.Check.XCCL.MinBusBandwidthGBs = n
	}
	return nil
}

func (m checkWizardModel) validatePage() error {
	if m.page == checkWizardPageHosts && m.selectedHostCount() == 0 {
		return fmt.Errorf("select at least one host")
	}
	if m.page == checkWizardPageChecks && !m.runPing && !m.runBandwidth && !m.runXCCL {
		return fmt.Errorf("select at least one check")
	}
	if m.page == checkWizardPageParameters && m.runBandwidth && !m.verbs && !m.rdmaCM {
		return fmt.Errorf("Bandwidth requires Verbs, RDMA-CM, or both")
	}
	if m.page == checkWizardPageParameters && m.runXCCL && m.selectedHostCount() > 1 && normalizedXCCLEvaluationMode(m.bundle.Check.XCCL.EvaluationMode) == "auto" && m.bundle.Check.XCCL.Test != "all_reduce" {
		return fmt.Errorf("XCCL automatic evaluation is defined only for all_reduce; choose manual or disabled for %s", m.bundle.Check.XCCL.Test)
	}
	return nil
}

func (m *checkWizardModel) applySelectedHostScope() {
	if m.selectedHostCount() != 1 {
		return
	}
	m.runPing = false
	m.runBandwidth = false
	m.runXCCL = true
	m.bundle.Check.XCCL.EvaluationMode = "disabled"
}

func (m checkWizardModel) validate() error {
	if err := m.validatePage(); err != nil {
		return err
	}
	count := m.selectedHostCount()
	if count < 2 && (m.runPing || m.runBandwidth) {
		return fmt.Errorf("Ping and Bandwidth require at least two hosts")
	}
	return nil
}

func (m checkWizardModel) selectedHostCount() int {
	count := 0
	for _, selected := range m.selectedHosts {
		if selected {
			count++
		}
	}
	return count
}
func (m checkWizardModel) selectedRecords() []spec.MachineRecord {
	var out []spec.MachineRecord
	for i, selected := range m.selectedHosts {
		if selected {
			out = append(out, m.records[i])
		}
	}
	return out
}

func wizardPathCount(records []spec.MachineRecord) int {
	total := 0
	for i := 0; i < len(records); i++ {
		for j := i + 1; j < len(records); j++ {
			total += 2 * len(records[i].RDMA) * len(records[j].RDMA)
		}
	}
	return total
}

func checkWizardHostIdentity(record spec.MachineRecord) string {
	for _, value := range []string{record.HostID, record.Hostname, record.MgmtIP} {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return "<unnamed>"
}

func boolLabel(value bool) string {
	if value {
		return "enabled"
	}
	return "disabled"
}

func boolPointerValue(value *bool) bool {
	return value != nil && *value
}

func xcclEvaluationReview(config spec.CheckXCCLConfig) string {
	switch normalizedXCCLEvaluationMode(config.EvaluationMode) {
	case "auto":
		baseline, required := xcclAutomaticEvaluationRule(config)
		if normalizedXCCLLayout(config.Layout) == "full_ring" {
			if normalizedXCCLMachineClass(config.MachineClass) == "auto" {
				return "auto(detect VC/VD per host; weakest-link)"
			}
			return fmt.Sprintf("auto(%s %.0fGB/s >%.0f%%)", strings.ToUpper(normalizedXCCLMachineClass(config.MachineClass)), baseline, required*100)
		}
		return fmt.Sprintf("auto(%.0fGB/s >%.0f%%)", baseline, required*100)
	case "manual":
		return fmt.Sprintf("manual(>=%.2fGB/s)", config.MinBusBandwidthGBs)
	default:
		return "disabled"
	}
}

func xcclXPUOrderingLabel(config spec.CheckXCCLConfig) string {
	configured := normalizedXCCLXPUOrdering(config.XPUOrdering)
	if configured != "auto" {
		return configured
	}
	if normalizedXCCLLayout(config.Layout) == "same_index" {
		return "auto (physical for same_index)"
	}
	return "auto (physical first; rail fallback)"
}

func xcclWizardEvaluationLabel(config spec.CheckXCCLConfig, multiHost bool) string {
	if !multiHost {
		return "disabled"
	}
	return normalizedXCCLEvaluationMode(config.EvaluationMode)
}

func xcclMachineClassLabel(value string) string {
	normalized := normalizedXCCLMachineClass(value)
	if normalized == "auto" {
		return "AUTO (detect per host)"
	}
	return strings.ToUpper(normalized)
}

func bandwidthRunModeLabel(config spec.CheckBandwidthConfig) string {
	if config.RunByDuration {
		return "duration"
	}
	return "iterations"
}

func bandwidthMemoryLabel(config spec.CheckBandwidthConfig) string {
	if strings.TrimSpace(config.MmapDevice) != "" {
		return "XDR mmap"
	}
	return "host memory"
}

func bandwidthThresholdReview(config spec.CheckBandwidthConfig) string {
	if config.MinGBitsMode() == "manual" {
		return fmt.Sprintf("manual(%.2fGbps)", config.MinGBits)
	}
	return config.MinGBitsMode()
}
func valueOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return strings.TrimSpace(value)
}
func truncateWizard(value string, width int) string {
	if utf8.RuneCountInString(value) <= width {
		return value
	}
	runes := []rune(value)
	if width <= 1 {
		return string(runes[:width])
	}
	return string(runes[:width-1]) + "…"
}
func maxIntMain(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minIntMain(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func pinWizardFooter(content, footer string, width, height int) string {
	contentLines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	footerLines := strings.Split(footer, "\n")
	for index := range contentLines {
		contentLines[index] = ansi.Truncate(contentLines[index], width, "…")
	}
	for index := range footerLines {
		footerLines[index] = ansi.Truncate(footerLines[index], width, "…")
	}
	available := maxIntMain(1, height-len(footerLines))
	if len(contentLines) > available {
		contentLines = contentLines[:available]
	}
	for len(contentLines) < available {
		contentLines = append(contentLines, "")
	}
	return strings.Join(contentLines, "\n") + "\n" + strings.Join(footerLines, "\n")
}
