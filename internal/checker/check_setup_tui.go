package checker

import (
	"fmt"
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
			if m.page == 0 {
				m.canceled = true
				return m, tea.Quit
			}
			m.page--
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
			if m.page == 0 {
				all := m.selectedHostCount() != len(m.selectedHosts)
				for index := range m.selectedHosts {
					m.selectedHosts[index] = all
				}
			}
		case "e", "E":
			if m.page == 2 && m.currentParamEditable() {
				m.startEditor()
			}
		case "enter":
			if m.page == 3 {
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
			m.page++
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
	steps := []string{"1 Hosts", "2 Checks", "3 Parameters", "4 Review"}
	for index := range steps {
		if index == m.page {
			steps[index] = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("45")).Render("[" + steps[index] + "]")
		}
	}
	var body string
	switch m.page {
	case 0:
		body = m.renderHosts(width)
	case 1:
		body = m.renderChecks(width)
	case 2:
		body = m.renderParameters(width)
	default:
		body = m.renderReview(width)
	}
	footer := "Up/Down select | Space toggle | Enter continue | b back | q exit"
	if m.page == 0 {
		footer = "Up/Down select | Space toggle | a all/none | Enter continue | q exit"
	} else if m.page == 2 {
		footer = "Up/Down select | Space toggle | Left/Right choose | e edit | Enter review | b back | q exit"
	} else if m.page == 3 {
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
	rows := []struct {
		name, description string
		enabled           bool
	}{
		{"Ping", "RoCE IPv4, MTU and full cross-matrix reachability", m.runPing},
		{"Bandwidth", "ib_write_bw using Verbs and/or RDMA-CM", m.runBandwidth},
		{"XCCL", "XPU collective communication performance", m.runXCCL},
	}
	var b strings.Builder
	b.WriteString("Choose one or more checks. Bundle values are used as defaults.\n\n")
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
			wizardParamRow{"xccl_socket", "XCCL / socket interface (-=auto)", valueOrDash(m.bundle.Check.XCCL.SocketInterface), false, false},
			wizardParamRow{"xccl_min_bus", "XCCL / minimum bus BW GB/s", strconv.FormatFloat(m.bundle.Check.XCCL.MinBusBandwidthGBs, 'f', -1, 64), false, false})
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
		fmt.Fprintf(&b, "[x] XCCL       collective=%s range=%s..%s step=%d warmup=%d iterations=%d dtype=%s XDR=%v supernode=%v\n", m.bundle.Check.XCCL.Test, m.bundle.Check.XCCL.MinBytes, m.bundle.Check.XCCL.MaxBytes, m.bundle.Check.XCCL.StepFactor, m.bundle.Check.XCCL.WarmupIterations, m.bundle.Check.XCCL.Iterations, m.bundle.Check.XCCL.DataType, boolPointerValue(m.bundle.Check.XCCL.EnableXDR), m.bundle.Check.XCCL.Supernode)
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
	case 0:
		return len(m.records)
	case 1:
		return 3
	case 2:
		return len(m.parameterRows())
	default:
		return 1
	}
}

func (m *checkWizardModel) toggleCurrent() {
	switch m.page {
	case 0:
		if m.cursor < len(m.selectedHosts) {
			m.selectedHosts[m.cursor] = !m.selectedHosts[m.cursor]
		}
	case 1:
		switch m.cursor {
		case 0:
			m.runPing = !m.runPing
		case 1:
			m.runBandwidth = !m.runBandwidth
		case 2:
			m.runXCCL = !m.runXCCL
		}
	case 2:
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
		case "xccl_supernode":
			m.bundle.Check.XCCL.Supernode = !m.bundle.Check.XCCL.Supernode
		}
	}
}

func (m *checkWizardModel) adjustCurrent(delta int) {
	if m.page != 2 {
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
	if m.page == 0 && m.selectedHostCount() == 0 {
		return fmt.Errorf("select at least one host")
	}
	if m.page == 1 && !m.runPing && !m.runBandwidth && !m.runXCCL {
		return fmt.Errorf("select at least one check")
	}
	if m.page == 2 && m.runBandwidth && !m.verbs && !m.rdmaCM {
		return fmt.Errorf("Bandwidth requires Verbs, RDMA-CM, or both")
	}
	return nil
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
