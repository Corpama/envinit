package checker

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	checkTUIStagePing      = "Ping"
	checkTUIStageBandwidth = "Bandwidth"
	checkTUIStageXCCL      = "XCCL"

	checkTUITabResults  = 0
	checkTUITabCounters = 1
	checkTUITabLogs     = 2
)

var checkTUITabNames = []string{"Results", "Counter Delta", "Raw Logs"}
var xcclPlotModes = []string{"out-of-place", "in-place"}

func isBandwidthTUIStage(stage string) bool {
	return stage == checkTUIStageBandwidth || strings.HasPrefix(stage, "BW ")
}

type checkTUIItem struct {
	ID      string
	Section string
	Status  string
	Cells   []string
	Detail  string
	Plot    *checkTUIPlotPoint
	Heatmap *checkTUIHeatmapPoint
	Retest  bool
}

type checkTUIHeatmapPoint struct {
	Direction      string
	ClientAxis     string
	ServerAxis     string
	MeasuredGBits  float64
	BaselineGBits  float64
	ThresholdGBits float64
	ThresholdMode  string
	ThresholdKnown bool
	Status         string
	Ready          bool
}

type checkTUIPlotPoint struct {
	Mode  string
	Size  int64
	AlgBW float64
	BusBW float64
	Ready bool
}

type checkTUITable struct {
	Headers []string
	Items   []checkTUIItem
}

type checkTUIController struct {
	program      *tea.Program
	done         chan struct{}
	mu           sync.Mutex
	pending      string
	currentStage string
	stages       []string
	retestCtx    context.Context
	retestCancel context.CancelFunc
	retestMu     sync.Mutex
	retestRuns   map[string]func(context.Context)
	retestActive map[string]bool
	retestWG     sync.WaitGroup
}

type checkTUIStagesMsg struct{ Stages []string }
type checkTUIResultsMsg struct {
	Stage   string
	Headers []string
	Items   []checkTUIItem
}
type checkTUIUpdateMsg struct{ Item checkTUIItem }
type checkTUITextMsg struct {
	Stage string
	Tab   int
	Text  string
}
type checkTUIActivateMsg struct{ Stage string }
type checkTUIFinishMsg struct{ Error string }

func startCheckTUI(input *os.File, output *os.File, stages []string, checkCtx context.Context, cancel context.CancelFunc, aborts *checkAbortManager) *checkTUIController {
	retestCtx, retestCancel := context.WithCancel(checkCtx)
	controller := &checkTUIController{
		done: make(chan struct{}), stages: append([]string(nil), stages...),
		retestCtx: retestCtx, retestCancel: retestCancel,
		retestRuns: map[string]func(context.Context){}, retestActive: map[string]bool{},
	}
	if len(stages) > 0 {
		controller.currentStage = stages[0]
	}
	model := newCheckTUIModel()
	model.cancelCheck = cancel
	model.abortItem = aborts.abortItem
	model.abortStage = aborts.abortStage
	model.retestItem = controller.requestRetest
	controller.program = tea.NewProgram(
		model,
		tea.WithInput(input),
		tea.WithOutput(output),
		tea.WithAltScreen(),
	)
	go func() {
		_, _ = controller.program.Run()
		close(controller.done)
	}()
	controller.program.Send(checkTUIStagesMsg{Stages: append([]string(nil), stages...)})
	return controller
}

func (c *checkTUIController) SetResults(stage string, headers []string, items []checkTUIItem) {
	if c == nil {
		return
	}
	cloned := make([]checkTUIItem, len(items))
	for idx, item := range items {
		item.Section = stage
		item.Cells = append([]string(nil), item.Cells...)
		cloned[idx] = item
	}
	c.program.Send(checkTUIResultsMsg{Stage: stage, Headers: append([]string(nil), headers...), Items: cloned})
}

func (c *checkTUIController) Update(item checkTUIItem) {
	if c == nil {
		return
	}
	item.Cells = append([]string(nil), item.Cells...)
	c.program.Send(checkTUIUpdateMsg{Item: item})
}

func checkTUIRetestKey(stage, id string) string { return stage + "\x00" + id }

func (c *checkTUIController) RegisterRetest(stage, id string, run func(context.Context)) {
	if c == nil || stage == "" || id == "" || run == nil {
		return
	}
	c.retestMu.Lock()
	c.retestRuns[checkTUIRetestKey(stage, id)] = run
	c.retestMu.Unlock()
}

func (c *checkTUIController) requestRetest(stage, id string) string {
	if c == nil {
		return "Retest is unavailable"
	}
	key := checkTUIRetestKey(stage, id)
	c.retestMu.Lock()
	run := c.retestRuns[key]
	if run == nil {
		c.retestMu.Unlock()
		return fmt.Sprintf("The selected %s item cannot be retested", stage)
	}
	if c.retestActive[key] {
		c.retestMu.Unlock()
		return fmt.Sprintf("Retest is already running for the selected %s item", stage)
	}
	c.retestActive[key] = true
	c.retestWG.Add(1)
	c.retestMu.Unlock()
	go func() {
		defer c.retestWG.Done()
		defer func() {
			c.retestMu.Lock()
			delete(c.retestActive, key)
			c.retestMu.Unlock()
		}()
		run(c.retestCtx)
	}()
	return fmt.Sprintf("Retest started for the selected %s item; q exits and cancels an active retest", stage)
}

func (c *checkTUIController) SetActiveStage(stage string) {
	if c == nil || stage == "" {
		return
	}
	c.flushPending()
	c.mu.Lock()
	c.currentStage = stage
	c.mu.Unlock()
	c.program.Send(checkTUIActivateMsg{Stage: stage})
}

func (c *checkTUIController) AppendLog(stage, text string) {
	if c == nil || strings.TrimSpace(text) == "" {
		return
	}
	c.program.Send(checkTUITextMsg{Stage: stage, Tab: checkTUITabLogs, Text: strings.TrimRight(text, "\n")})
}

func (c *checkTUIController) AppendCounterSummary(kind, text string) {
	if c == nil || strings.TrimSpace(text) == "" {
		return
	}
	for _, stage := range c.counterStages(kind) {
		c.program.Send(checkTUITextMsg{Stage: stage, Tab: checkTUITabCounters, Text: strings.TrimRight(text, "\n")})
	}
}

func (c *checkTUIController) AppendCounterSummaryForStage(stage, text string) {
	if c == nil || stage == "" || strings.TrimSpace(text) == "" {
		return
	}
	c.program.Send(checkTUITextMsg{Stage: stage, Tab: checkTUITabCounters, Text: strings.TrimRight(text, "\n")})
}

func (c *checkTUIController) counterStages(kind string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var stages []string
	for _, stage := range c.stages {
		switch kind {
		case "nic":
			stages = append(stages, stage)
		case "rdma":
			if isBandwidthTUIStage(stage) || stage == checkTUIStageXCCL {
				stages = append(stages, stage)
			}
		}
	}
	return stages
}

func (c *checkTUIController) Write(p []byte) (int, error) {
	if c == nil {
		return len(p), nil
	}
	c.mu.Lock()
	c.pending += string(p)
	stage := c.currentStage
	var chunks []string
	for {
		idx := strings.IndexByte(c.pending, '\n')
		if idx < 0 {
			break
		}
		chunks = append(chunks, c.pending[:idx])
		c.pending = c.pending[idx+1:]
	}
	c.mu.Unlock()
	for _, chunk := range chunks {
		if chunk != "" {
			c.program.Send(checkTUITextMsg{Stage: stage, Tab: checkTUITabLogs, Text: chunk})
		}
	}
	return len(p), nil
}

func (c *checkTUIController) flushPending() {
	if c == nil {
		return
	}
	c.mu.Lock()
	pending := c.pending
	stage := c.currentStage
	c.pending = ""
	c.mu.Unlock()
	if pending != "" {
		c.program.Send(checkTUITextMsg{Stage: stage, Tab: checkTUITabLogs, Text: pending})
	}
}

func (c *checkTUIController) Finish(err error) {
	if c == nil {
		return
	}
	c.flushPending()
	errorText := ""
	if err != nil {
		errorText = err.Error()
	}
	c.program.Send(checkTUIFinishMsg{Error: errorText})
	<-c.done
	c.retestCancel()
	c.retestWG.Wait()
}

type checkTUIModel struct {
	stages           []string
	tables           map[string]checkTUITable
	itemIndex        map[string]struct{ stage, index int }
	counterLines     map[string][]string
	logLines         map[string][]string
	stage            int
	tab              int
	selected         int
	listOffset       int
	detailOffset     int
	textOffset       int
	horizontalOffset int
	detailOpen       bool
	plotMode         bool
	plotModeIndex    int
	plotCursor       int
	width            int
	height           int
	finished         bool
	finalError       string
	cancelCheck      context.CancelFunc
	abortItem        func(string, string) bool
	abortStage       func(string) bool
	retestItem       func(string, string) string
	abortedStages    map[string]bool
	lastEsc          time.Time
	notice           string
}

func newCheckTUIModel() checkTUIModel {
	return checkTUIModel{
		tables:        map[string]checkTUITable{},
		itemIndex:     map[string]struct{ stage, index int }{},
		counterLines:  map[string][]string{},
		logLines:      map[string][]string{},
		abortedStages: map[string]bool{},
		width:         120,
		height:        36,
	}
}

func (m checkTUIModel) Init() tea.Cmd { return nil }

func (m checkTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case checkTUIStagesMsg:
		m.setStages(msg.Stages)
	case checkTUIResultsMsg:
		m.setResults(msg.Stage, msg.Headers, msg.Items)
	case checkTUIUpdateMsg:
		m.updateItem(msg.Item)
	case checkTUITextMsg:
		m.appendText(msg.Stage, msg.Tab, msg.Text)
	case checkTUIActivateMsg:
		m.activateStage(msg.Stage)
	case checkTUIFinishMsg:
		m.finished = true
		m.finalError = msg.Error
		if msg.Error != "" {
			m.appendText(m.currentStage(), checkTUITabLogs, "FINAL ERROR: "+msg.Error)
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			if !m.finished && m.cancelCheck != nil {
				m.cancelCheck()
			}
			return m, tea.Quit
		case "enter":
			if m.finished {
				if m.requestSelectedRetest() {
					break
				}
				return m, tea.Quit
			}
			m.notice = "Retest is available after the full check completes"
		case "q", "Q":
			if m.finished {
				return m, tea.Quit
			}
			m.abortSelectedItem()
		case "esc":
			if m.finished {
				return m, tea.Quit
			}
			m.handleEscape()
		case "tab":
			m.moveTab(1)
		case "shift+tab":
			m.moveTab(-1)
		case "[":
			m.moveStage(-1)
		case "]":
			m.moveStage(1)
		case " ":
			if m.tab == checkTUITabResults && len(m.currentTable().Items) > 0 {
				m.detailOpen = !m.detailOpen
				m.detailOffset = 0
			}
		case "p", "P":
			if (m.currentStage() == checkTUIStageXCCL || isBandwidthTUIStage(m.currentStage())) && m.tab == checkTUITabResults {
				m.plotMode = !m.plotMode
				if m.currentStage() == checkTUIStageXCCL {
					m.plotCursor = maxInt(0, len(m.currentPlotPoints())-1)
				} else if m.plotMode {
					m.selectBandwidthHeatmapPoint(0)
				}
			}
		case "m", "M":
			if m.plotMode && m.currentStage() == checkTUIStageXCCL {
				m.plotModeIndex = (m.plotModeIndex + 1) % len(xcclPlotModes)
				m.plotCursor = maxInt(0, len(m.currentPlotPoints())-1)
			} else if m.plotMode && isBandwidthTUIStage(m.currentStage()) {
				directions := m.bandwidthHeatmapDirections()
				if len(directions) > 0 {
					m.plotModeIndex = (m.plotModeIndex + 1) % len(directions)
					m.selectBandwidthHeatmapPoint(0)
				}
			}
		case "up", "k":
			if m.plotMode && isBandwidthTUIStage(m.currentStage()) {
				m.moveBandwidthHeatmapCursor(-1, 0)
			} else {
				m.moveVertical(-1)
			}
		case "down", "j":
			if m.plotMode && isBandwidthTUIStage(m.currentStage()) {
				m.moveBandwidthHeatmapCursor(1, 0)
			} else {
				m.moveVertical(1)
			}
		case "left", "h":
			if m.plotMode && isBandwidthTUIStage(m.currentStage()) {
				m.moveBandwidthHeatmapCursor(0, -1)
			} else if m.plotMode {
				m.movePlotCursor(-1)
			} else {
				m.horizontalOffset = maxInt(0, m.horizontalOffset-8)
			}
		case "right", "l":
			if m.plotMode && isBandwidthTUIStage(m.currentStage()) {
				m.moveBandwidthHeatmapCursor(0, 1)
			} else if m.plotMode {
				m.movePlotCursor(1)
			} else {
				m.horizontalOffset += 8
			}
		case "pgup":
			m.movePage(-1)
		case "pgdown":
			m.movePage(1)
		case "ctrl+u":
			m.moveDetailPage(-1)
		case "ctrl+d":
			m.moveDetailPage(1)
		}
	}
	return m, nil
}

func (m checkTUIModel) View() string {
	width := maxInt(20, m.width)
	var b strings.Builder
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39")).Render("envinit check")
	if width >= 72 {
		fmt.Fprintf(&b, "%s  %s\n", title, m.stageBar(width-lipgloss.Width(title)-2))
	} else {
		fmt.Fprintln(&b, title)
		fmt.Fprintln(&b, m.stageBar(width))
	}
	fmt.Fprintln(&b, m.tabBar(width))
	fmt.Fprintln(&b)
	switch m.tab {
	case checkTUITabCounters:
		fmt.Fprintln(&b, m.renderTextPage(width, m.counterLines[m.currentStage()], "Counter delta summary is not available yet."))
	case checkTUITabLogs:
		fmt.Fprintln(&b, m.renderTextPage(width, m.logLines[m.currentStage()], "Waiting for raw check output..."))
	default:
		if m.plotMode && m.currentStage() == checkTUIStageXCCL {
			fmt.Fprintln(&b, m.renderXCCLPlots(width))
		} else if m.plotMode && isBandwidthTUIStage(m.currentStage()) {
			fmt.Fprintln(&b, m.renderBandwidthHeatmapView(width))
		} else {
			fmt.Fprintln(&b, m.renderResults(width))
		}
	}
	footer := ""
	if m.finished {
		status := "Checks completed"
		if m.finalError != "" {
			status = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("Checks failed")
		}
		if m.plotMode && m.currentStage() == checkTUIStageXCCL {
			footer = fmt.Sprintf("%s | p table | m mode | Left/Right point | Enter/q exit", status)
		} else if m.plotMode && isBandwidthTUIStage(m.currentStage()) {
			footer = fmt.Sprintf("%s | [/]: switch stage | p: table | m: active direction | Arrows: cell | PgUp/PgDown: jump rows | Space: details | Ctrl+U/Ctrl+D: detail page | Enter: retest | q: exit", status)
		} else if m.tab == checkTUITabResults && (m.currentStage() == checkTUIStagePing || isBandwidthTUIStage(m.currentStage())) {
			footer = fmt.Sprintf("%s | [/]: switch stage | Tab: tab | Up/Down: select | PgUp/PgDown: list page | Space: details | Ctrl+U/Ctrl+D: detail page | Enter: retest | q: exit", status)
		} else {
			footer = fmt.Sprintf("%s | [/]: switch stage | Tab: tab | Up/Down: select | PgUp/PgDown: page | Space: details | Ctrl+U/Ctrl+D: detail page | Enter/q: exit", status)
		}
	} else {
		if m.plotMode && m.currentStage() == checkTUIStageXCCL {
			footer = "Checks running | [/]: switch stage | q: abort XCCL stage | Esc: abort stage (twice: all) | p: table | m: mode | Left/Right: point"
		} else if m.plotMode && isBandwidthTUIStage(m.currentStage()) {
			footer = "Checks running | [/]: switch stage | p: table | m: active direction | Arrows: cell | PgUp/PgDown: jump rows | Space: details | Ctrl+U/Ctrl+D: detail page | q: abort item | Esc: abort stage (twice: all)"
		} else {
			footer = "Checks running | [/]: switch stage | Tab: tab | Up/Down: select | PgUp/PgDown: page | Space: details | Ctrl+U/Ctrl+D: detail page | q: abort item | Esc: abort stage (twice: all)"
		}
		if m.tab == checkTUITabResults && !m.plotMode {
			if m.currentStage() == checkTUIStageXCCL {
				footer += " | p chart"
			} else if isBandwidthTUIStage(m.currentStage()) {
				footer += " | p heatmap"
			}
		}
	}
	if m.notice != "" {
		footer = m.notice + "\n" + footer
	}
	return pinCheckTUIFooter(strings.TrimRight(b.String(), "\n"), adaptiveCheckTUIFooter(footer, width), maxInt(1, m.height))
}

func (m *checkTUIModel) requestSelectedRetest() bool {
	if m.tab != checkTUITabResults {
		return false
	}
	stage := m.currentStage()
	if stage != checkTUIStagePing && !isBandwidthTUIStage(stage) {
		return false
	}
	table := m.currentTable()
	if m.selected < 0 || m.selected >= len(table.Items) {
		m.notice = "No test item is selected"
		return true
	}
	item := table.Items[m.selected]
	if checkTUIItemCanAbort(item) {
		m.notice = fmt.Sprintf("The selected %s item is still running and cannot be retested yet", stage)
		return true
	}
	if m.retestItem == nil {
		m.notice = "Retest is unavailable"
		return true
	}
	m.notice = m.retestItem(stage, item.ID)
	return true
}

func (m *checkTUIModel) setStages(stages []string) {
	m.stages = append([]string(nil), stages...)
	for _, stage := range stages {
		if _, ok := m.tables[stage]; !ok {
			m.tables[stage] = checkTUITable{}
		}
	}
}

func (m *checkTUIModel) setResults(stage string, headers []string, items []checkTUIItem) {
	if !containsString(m.stages, stage) {
		m.stages = append(m.stages, stage)
	}
	if m.abortedStages[stage] {
		for index := range items {
			if checkTUIItemCanAbort(items[index]) {
				items[index] = abortedCheckTUIItem(items[index], "stage aborted by user")
			}
		}
	}
	for idx, item := range items {
		item.Section = stage
		items[idx] = item
		m.itemIndex[item.ID] = struct{ stage, index int }{stage: m.stageIndex(stage), index: idx}
	}
	m.tables[stage] = checkTUITable{Headers: append([]string(nil), headers...), Items: append([]checkTUIItem(nil), items...)}
}

func (m *checkTUIModel) abortSelectedItem() {
	if m.tab != checkTUITabResults {
		m.notice = "q aborts the selected Results row; switch to Results first"
		return
	}
	stage := m.currentStage()
	if stage == checkTUIStageXCCL {
		if m.requestStageAbort(stage) {
			m.notice = "XCCL uses one shared mpirun process; the XCCL stage is being aborted"
		}
		return
	}
	table := m.currentTable()
	if m.selected < 0 || m.selected >= len(table.Items) {
		m.notice = "No test item is selected"
		return
	}
	item := table.Items[m.selected]
	if !checkTUIItemCanAbort(item) {
		m.notice = fmt.Sprintf("%s is already complete and cannot be aborted", item.ID)
		return
	}
	if m.abortItem == nil || !m.abortItem(stage, item.ID) {
		m.notice = fmt.Sprintf("%s is no longer running", item.ID)
		return
	}
	item.Status = "ABORTING"
	if item.Heatmap != nil {
		point := *item.Heatmap
		point.Status = "ABORTING"
		item.Heatmap = &point
	}
	if len(item.Cells) > 0 {
		item.Cells[0] = "ABORTING"
	}
	item.Detail += "\n\nAbort requested by user; waiting for process cleanup."
	m.updateItem(item)
	m.notice = fmt.Sprintf("Abort requested for selected item %s", item.ID)
}

func (m *checkTUIModel) handleEscape() {
	now := time.Now()
	if !m.lastEsc.IsZero() && now.Sub(m.lastEsc) <= 1500*time.Millisecond {
		m.lastEsc = time.Time{}
		if m.cancelCheck != nil {
			m.cancelCheck()
		}
		for _, stage := range m.stages {
			m.markStageAborted(stage)
		}
		m.notice = "Entire check abort requested; waiting for cleanup"
		return
	}
	stage := m.currentStage()
	if !m.requestStageAbort(stage) {
		m.lastEsc = time.Time{}
		m.notice = fmt.Sprintf("%s stage is already complete", stage)
		return
	}
	if len(m.stages) > 1 {
		m.lastEsc = now
		m.notice = fmt.Sprintf("%s stage abort requested; press Esc again within 1.5s to abort the entire check", stage)
	} else {
		m.lastEsc = time.Time{}
		m.notice = fmt.Sprintf("%s stage abort requested; this is the only stage, so the check will end after cleanup", stage)
	}
}

func (m *checkTUIModel) requestStageAbort(stage string) bool {
	if stage == "" || m.abortStage == nil || !m.abortStage(stage) {
		return false
	}
	m.markStageAborted(stage)
	return true
}

func (m *checkTUIModel) markStageAborted(stage string) {
	m.abortedStages[stage] = true
	table := m.tables[stage]
	for index := range table.Items {
		if checkTUIItemCanAbort(table.Items[index]) {
			table.Items[index] = abortedCheckTUIItem(table.Items[index], "stage aborted by user")
		}
	}
	m.tables[stage] = table
}

func checkTUIItemCanAbort(item checkTUIItem) bool {
	return item.Status == "WAIT" || item.Status == "RUNNING" || item.Status == "ABORTING"
}

func abortedCheckTUIItem(item checkTUIItem, reason string) checkTUIItem {
	item.Status = "ABORT"
	if item.Heatmap != nil {
		point := *item.Heatmap
		point.Status = "ABORT"
		item.Heatmap = &point
	}
	if len(item.Cells) > 0 {
		item.Cells[0] = "ABORT"
	}
	item.Detail += "\n\nAborted: " + reason
	return item
}

func (m *checkTUIModel) updateItem(item checkTUIItem) {
	location, ok := m.itemIndex[item.ID]
	if !ok {
		stageIndex := m.stageIndex(item.Section)
		if stageIndex < 0 {
			return
		}
		table := m.tables[item.Section]
		location = struct{ stage, index int }{stage: stageIndex, index: len(table.Items)}
		table.Items = append(table.Items, item)
		m.tables[item.Section] = table
		m.itemIndex[item.ID] = location
		return
	}
	if location.stage < 0 || location.stage >= len(m.stages) {
		return
	}
	stage := m.stages[location.stage]
	table := m.tables[stage]
	if location.index < 0 || location.index >= len(table.Items) {
		return
	}
	currentStatus := table.Items[location.index].Status
	if !item.Retest && (currentStatus == "ABORT" || currentStatus == "ABORTING") && (item.Status == "WAIT" || item.Status == "RUNNING") {
		return
	}
	item.Section = stage
	table.Items[location.index] = item
	m.tables[stage] = table
}

func (m *checkTUIModel) appendText(stage string, tab int, text string) {
	if stage == "" || text == "" {
		return
	}
	lines := strings.Split(strings.ReplaceAll(strings.TrimRight(text, "\n"), "\r", ""), "\n")
	if tab == checkTUITabCounters {
		if len(m.counterLines[stage]) > 0 {
			m.counterLines[stage] = append(m.counterLines[stage], "")
		}
		m.counterLines[stage] = append(m.counterLines[stage], lines...)
		return
	}
	m.logLines[stage] = append(m.logLines[stage], lines...)
}

func (m *checkTUIModel) activateStage(stage string) {
	idx := m.stageIndex(stage)
	if idx >= 0 {
		m.stage = idx
		m.resetViewport()
	}
}

func (m *checkTUIModel) moveStage(delta int) {
	if len(m.stages) == 0 {
		return
	}
	m.stage = (m.stage + delta + len(m.stages)) % len(m.stages)
	m.resetViewport()
}

func (m *checkTUIModel) moveTab(delta int) {
	m.tab = (m.tab + delta + len(checkTUITabNames)) % len(checkTUITabNames)
	m.resetViewport()
}

func (m *checkTUIModel) moveVertical(delta int) {
	if m.tab != checkTUITabResults {
		lines := m.currentTextLines()
		m.textOffset = minInt(maxInt(0, len(lines)-1), maxInt(0, m.textOffset+delta))
		return
	}
	items := m.currentTable().Items
	if len(items) == 0 {
		return
	}
	m.selected = (m.selected + delta + len(items)) % len(items)
	m.detailOffset = 0
	visible := m.resultListHeight()
	if m.selected < m.listOffset {
		m.listOffset = m.selected
	} else if m.selected >= m.listOffset+visible {
		m.listOffset = m.selected - visible + 1
	}
}

func (m *checkTUIModel) movePage(delta int) {
	if m.tab == checkTUITabResults {
		if m.plotMode && m.currentStage() == checkTUIStageXCCL {
			m.movePlotCursor(delta * maxInt(1, m.contentHeight()/2))
			return
		}
		if m.plotMode && isBandwidthTUIStage(m.currentStage()) {
			m.moveBandwidthHeatmapCursor(delta*maxInt(1, m.resultListHeight()), 0)
			return
		}
		items := m.currentTable().Items
		if len(items) == 0 {
			return
		}
		visible := maxInt(1, m.resultListHeight())
		m.selected = minInt(len(items)-1, maxInt(0, m.selected+delta*visible))
		maxOffset := maxInt(0, len(items)-visible)
		m.listOffset = minInt(maxOffset, maxInt(0, m.listOffset+delta*visible))
		if m.selected < m.listOffset {
			m.listOffset = m.selected
		} else if m.selected >= m.listOffset+visible {
			m.listOffset = minInt(maxOffset, m.selected-visible+1)
		}
		m.detailOffset = 0
		return
	}
	lines := m.currentTextLines()
	step := maxInt(3, m.contentHeight()-1)
	m.textOffset = minInt(maxInt(0, len(lines)-1), maxInt(0, m.textOffset+delta*step))
}

func (m *checkTUIModel) moveDetailPage(delta int) {
	if m.tab != checkTUITabResults || !m.detailOpen {
		m.notice = "Open details with Space before paging the detail panel"
		return
	}
	step := maxInt(3, m.contentHeight()/2)
	m.detailOffset = maxInt(0, m.detailOffset+delta*step)
}

func (m *checkTUIModel) resetViewport() {
	m.selected, m.listOffset, m.detailOffset, m.textOffset, m.horizontalOffset = 0, 0, 0, 0, 0
	m.detailOpen = false
	m.plotMode = false
	m.plotModeIndex = 0
}

func (m checkTUIModel) renderResults(width int) string {
	if !m.detailOpen {
		return m.renderResultTable(width, m.contentHeight())
	}
	if width >= 140 {
		leftWidth := width * 64 / 100
		rightWidth := width - leftWidth - 3
		return lipgloss.JoinHorizontal(lipgloss.Top,
			m.renderResultTable(leftWidth, m.contentHeight()), " │ ", m.renderDetails(rightWidth, m.contentHeight()))
	}
	if m.contentHeight() < 7 {
		return m.renderDetails(width, m.contentHeight())
	}
	tableHeight := maxInt(3, m.contentHeight()/2)
	detailHeight := maxInt(2, m.contentHeight()-tableHeight-1)
	return m.renderResultTable(width, tableHeight) + "\n" + strings.Repeat("─", width) + "\n" + m.renderDetails(width, detailHeight)
}

func (m checkTUIModel) currentPlotPoints() []checkTUIPlotPoint {
	mode := xcclPlotModes[m.plotModeIndex%len(xcclPlotModes)]
	var points []checkTUIPlotPoint
	for _, item := range m.currentTable().Items {
		if item.Plot == nil || !item.Plot.Ready || item.Plot.Mode != mode {
			continue
		}
		points = append(points, *item.Plot)
	}
	return points
}

func (m *checkTUIModel) movePlotCursor(delta int) {
	points := m.currentPlotPoints()
	if len(points) == 0 {
		m.plotCursor = 0
		return
	}
	m.plotCursor = minInt(len(points)-1, maxInt(0, m.plotCursor+delta))
}

func (m checkTUIModel) renderXCCLPlots(width int) string {
	height := m.contentHeight()
	mode := xcclPlotModes[m.plotModeIndex%len(xcclPlotModes)]
	points := m.currentPlotPoints()
	if len(points) == 0 {
		return fmt.Sprintf("XCCL bandwidth charts [%s]\nWaiting for performance points...", mode)
	}
	cursor := minInt(maxInt(0, m.plotCursor), len(points)-1)
	point := points[cursor]
	info := fmt.Sprintf("XCCL bandwidth charts [%s]  point %d/%d  size=%s (%d B)  AlgBW=%.2f GB/s  BusBW=%.2f GB/s",
		mode, cursor+1, len(points), formatByteSize(point.Size), point.Size, point.AlgBW, point.BusBW)
	if height < 9 || width < 32 {
		return ansi.Truncate(info+"\nResize the terminal to show both line charts.", width, "...")
	}
	chartHeight := maxInt(4, (height-1)/2)
	if 1+chartHeight*2 > height {
		chartHeight = maxInt(3, (height-1)/2)
	}
	alg := renderCheckTUILineChart("AlgBW (GB/s)", points, cursor, width, chartHeight, func(point checkTUIPlotPoint) float64 { return point.AlgBW })
	bus := renderCheckTUILineChart("BusBW (GB/s)", points, cursor, width, chartHeight, func(point checkTUIPlotPoint) float64 { return point.BusBW })
	return info + "\n" + alg + "\n" + bus
}

type bandwidthHeatmapEntry struct {
	index int
	item  checkTUIItem
	point checkTUIHeatmapPoint
}

func (m checkTUIModel) bandwidthHeatmapDirections() []string {
	var directions []string
	for _, item := range m.tables[m.currentStage()].Items {
		if item.Heatmap != nil && !containsString(directions, item.Heatmap.Direction) {
			directions = append(directions, item.Heatmap.Direction)
		}
	}
	return directions
}

func (m checkTUIModel) currentBandwidthHeatmapEntries() []bandwidthHeatmapEntry {
	directions := m.bandwidthHeatmapDirections()
	if len(directions) == 0 {
		return nil
	}
	direction := directions[m.plotModeIndex%len(directions)]
	return m.bandwidthHeatmapEntriesForDirection(direction)
}

func (m checkTUIModel) bandwidthHeatmapEntriesForDirection(direction string) []bandwidthHeatmapEntry {
	var entries []bandwidthHeatmapEntry
	for index, item := range m.tables[m.currentStage()].Items {
		if item.Heatmap != nil && item.Heatmap.Direction == direction {
			entries = append(entries, bandwidthHeatmapEntry{index: index, item: item, point: *item.Heatmap})
		}
	}
	return entries
}

func (m *checkTUIModel) selectBandwidthHeatmapPoint(fallback int) {
	entries := m.currentBandwidthHeatmapEntries()
	if len(entries) == 0 {
		return
	}
	for _, entry := range entries {
		if entry.index == m.selected {
			return
		}
	}
	m.selected = entries[minInt(maxInt(0, fallback), len(entries)-1)].index
}

func (m *checkTUIModel) moveBandwidthHeatmapCursor(rowDelta, columnDelta int) {
	entries := m.currentBandwidthHeatmapEntries()
	if len(entries) == 0 {
		return
	}
	rows, columns := bandwidthHeatmapAxes(entries)
	current := entries[0]
	for _, entry := range entries {
		if entry.index == m.selected {
			current = entry
			break
		}
	}
	row := stringIndex(rows, current.point.ClientAxis)
	column := stringIndex(columns, current.point.ServerAxis)
	row = (row + rowDelta + len(rows)) % len(rows)
	column = (column + columnDelta + len(columns)) % len(columns)
	for _, entry := range entries {
		if entry.point.ClientAxis == rows[row] && entry.point.ServerAxis == columns[column] {
			m.selected = entry.index
			m.detailOffset = 0
			return
		}
	}
}

func bandwidthHeatmapAxes(entries []bandwidthHeatmapEntry) ([]string, []string) {
	var rows, columns []string
	for _, entry := range entries {
		if !containsString(rows, entry.point.ClientAxis) {
			rows = append(rows, entry.point.ClientAxis)
		}
		if !containsString(columns, entry.point.ServerAxis) {
			columns = append(columns, entry.point.ServerAxis)
		}
	}
	return rows, columns
}

func stringIndex(values []string, target string) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return 0
}

func (m checkTUIModel) renderBandwidthHeatmapView(width int) string {
	heatmap := m.renderBandwidthHeatmap(width)
	if !m.detailOpen {
		return heatmap
	}
	heatmapLines := strings.Split(heatmap, "\n")
	remaining := maxInt(2, m.contentHeight()-len(heatmapLines)-1)
	return heatmap + "\n" + strings.Repeat("─", width) + "\n" + m.renderDetails(width, remaining)
}

func (m checkTUIModel) renderBandwidthHeatmap(width int) string {
	directions := m.bandwidthHeatmapDirections()
	activeEntries := m.currentBandwidthHeatmapEntries()
	if len(directions) == 0 || len(activeEntries) == 0 {
		return "Bandwidth heatmap\nWaiting for bandwidth result plan..."
	}
	activeDirectionIndex := m.plotModeIndex % len(directions)
	activeDirection := directions[activeDirectionIndex]
	selected := activeEntries[0]
	for _, entry := range activeEntries {
		if entry.index == m.selected {
			selected = entry
		}
	}
	var b strings.Builder
	b.WriteString(ansi.Truncate(fmt.Sprintf("Bandwidth heatmap  both directions shown  active direction %d/%d [%s]", activeDirectionIndex+1, len(directions), activeDirection), width, "...") + "\n")
	b.WriteString(ansi.Truncate(fmt.Sprintf("Selected link: CLIENT %s -> SERVER %s  measured=%s",
		selected.point.ClientAxis, selected.point.ServerAxis,
		formatHeatmapGBits(selected.point.MeasuredGBits, selected.point.Ready)), width, "...") + "\n")
	b.WriteString(ansi.Truncate(fmt.Sprintf("Selected metrics: baseline=%s  threshold=%s  utilization=%s  mode=%s",
		formatHeatmapGBits(selected.point.BaselineGBits, selected.point.BaselineGBits > 0),
		formatHeatmapGBits(selected.point.ThresholdGBits, selected.point.ThresholdKnown),
		bandwidthHeatmapUtilization(selected.point), selected.point.ThresholdMode), width, "...") + "\n")
	b.WriteString(ansi.Truncate("Legend: P=PASS  F=FAIL  W=WARN  R=RUNNING  .=WAIT; rows are CLIENT NICs, columns are SERVER NICs", width, "...") + "\n")
	rowsPerDirection := maxInt(1, (m.contentHeight()-4-len(directions)*3)/maxInt(1, len(directions)))
	if m.detailOpen {
		rowsPerDirection = maxInt(1, rowsPerDirection/2)
	}
	for directionIndex, direction := range directions {
		if directionIndex > 0 {
			b.WriteByte('\n')
		}
		entries := m.bandwidthHeatmapEntriesForDirection(direction)
		active := directionIndex == activeDirectionIndex
		b.WriteString(m.renderBandwidthHeatmapDirection(width, directionIndex+1, direction, entries, active, rowsPerDirection))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m checkTUIModel) renderBandwidthHeatmapDirection(width, directionIndex int, direction string, entries []bandwidthHeatmapEntry, active bool, visibleRows int) string {
	if len(entries) == 0 {
		return fmt.Sprintf("Direction %d: %s  no planned links", directionIndex, direction)
	}
	allRows, allColumns := bandwidthHeatmapAxes(entries)
	selected := entries[0]
	for _, entry := range entries {
		if entry.index == m.selected {
			selected = entry
		}
	}
	rowStart := 0
	if visibleRows < len(allRows) {
		selectedRow := 0
		if active {
			selectedRow = stringIndex(allRows, selected.point.ClientAxis)
		}
		rowStart = minInt(len(allRows)-visibleRows, maxInt(0, selectedRow-visibleRows/2))
	}
	rows := allRows[rowStart:minInt(len(allRows), rowStart+visibleRows)]
	rowWidth := len("CLIENT\\SERVER")
	for _, row := range allRows {
		rowWidth = maxInt(rowWidth, ansi.StringWidth(row))
	}
	rowWidth = minInt(rowWidth, 20)
	cellWidth := 12
	visibleColumns := maxInt(1, (width-rowWidth-3)/cellWidth)
	columnStart := 0
	if visibleColumns < len(allColumns) {
		selectedColumn := 0
		if active {
			selectedColumn = stringIndex(allColumns, selected.point.ServerAxis)
		}
		columnStart = minInt(len(allColumns)-visibleColumns, maxInt(0, selectedColumn-visibleColumns/2))
	}
	columns := allColumns[columnStart:minInt(len(allColumns), columnStart+visibleColumns)]
	byCell := map[string]bandwidthHeatmapEntry{}
	for _, entry := range entries {
		byCell[entry.point.ClientAxis+"\x00"+entry.point.ServerAxis] = entry
	}
	marker := " "
	if active {
		marker = ">"
	}
	var b strings.Builder
	b.WriteString(ansi.Truncate(fmt.Sprintf("%s Direction %d: CLIENT %s  ->  SERVER %s  rows %d-%d/%d cols %d-%d/%d", marker, directionIndex, heatmapDirectionEndpoint(direction, true), heatmapDirectionEndpoint(direction, false), rowStart+1, rowStart+len(rows), len(allRows), columnStart+1, columnStart+len(columns), len(allColumns)), width, "...") + "\n")
	fmt.Fprintf(&b, "%-*s", rowWidth, "CLIENT\\SERVER")
	for _, column := range columns {
		fmt.Fprintf(&b, "  %-*s", cellWidth-2, fitCheckTUIText(column, cellWidth-2))
	}
	b.WriteByte('\n')
	for _, row := range rows {
		fmt.Fprintf(&b, "%-*s", rowWidth, fitCheckTUIText(row, rowWidth))
		for _, column := range columns {
			entry, ok := byCell[row+"\x00"+column]
			if !ok {
				fmt.Fprintf(&b, "  %-*s", cellWidth-2, "-")
				continue
			}
			cell := bandwidthHeatmapCell(entry.point, entry.index == m.selected)
			fmt.Fprintf(&b, "  %s", lipgloss.NewStyle().Width(cellWidth-2).Render(cell))
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func heatmapDirectionEndpoint(direction string, client bool) string {
	parts := strings.SplitN(direction, " -> ", 2)
	if len(parts) != 2 {
		return direction
	}
	if client {
		return parts[0]
	}
	return parts[1]
}

func bandwidthHeatmapCell(point checkTUIHeatmapPoint, selected bool) string {
	prefix := "."
	switch point.Status {
	case "PASS":
		prefix = "P"
	case "FAIL", "ABORT":
		prefix = "F"
	case "WARN":
		prefix = "W"
	case "RUNNING", "ABORTING":
		prefix = "R"
	}
	value := prefix
	if point.Ready && !math.IsNaN(point.MeasuredGBits) {
		value = fmt.Sprintf("%s %.1f", prefix, point.MeasuredGBits)
	}
	if selected {
		value = ">" + value
	} else {
		value = " " + value
	}
	color := lipgloss.Color("244")
	switch {
	case point.Status == "FAIL" || point.Status == "ABORT":
		color = lipgloss.Color("196")
	case point.Status == "RUNNING" || point.Status == "ABORTING":
		color = lipgloss.Color("214")
	case point.Ready && point.ThresholdMode == "auto" && point.BaselineGBits > 0 && point.MeasuredGBits/point.BaselineGBits < 0.70:
		color = lipgloss.Color("196")
	case point.Ready && point.ThresholdMode == "auto" && point.BaselineGBits > 0 && point.MeasuredGBits/point.BaselineGBits < 0.90:
		color = lipgloss.Color("226")
	case point.Ready && point.ThresholdMode == "disabled":
		color = lipgloss.Color("45")
	case point.Ready:
		color = lipgloss.Color("42")
	}
	return lipgloss.NewStyle().Foreground(color).Render(fitCheckTUIText(value, 10))
}

func formatHeatmapGBits(value float64, available bool) string {
	if !available || math.IsNaN(value) {
		return "-"
	}
	return fmt.Sprintf("%.2f Gbps", value)
}

func bandwidthHeatmapUtilization(point checkTUIHeatmapPoint) string {
	if !point.Ready || point.BaselineGBits <= 0 || math.IsNaN(point.MeasuredGBits) {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", point.MeasuredGBits/point.BaselineGBits*100)
}

func renderCheckTUILineChart(title string, points []checkTUIPlotPoint, cursor, width, height int, value func(checkTUIPlotPoint) float64) string {
	const labelWidth = 10
	plotWidth := maxInt(8, width-labelWidth)
	plotHeight := maxInt(1, height-2)
	canvas := make([][]rune, plotHeight)
	for row := range canvas {
		canvas[row] = []rune(strings.Repeat(" ", plotWidth))
	}
	maximum := 0.0
	for _, point := range points {
		maximum = math.Max(maximum, value(point))
	}
	if maximum <= 0 {
		maximum = 1
	}
	xs := make([]int, len(points))
	ys := make([]int, len(points))
	for idx, point := range points {
		if len(points) == 1 {
			xs[idx] = plotWidth / 2
		} else {
			xs[idx] = int(math.Round(float64(idx) * float64(plotWidth-1) / float64(len(points)-1)))
		}
		ys[idx] = plotHeight - 1 - int(math.Round(value(point)/maximum*float64(plotHeight-1)))
		if idx > 0 {
			drawCheckTUILine(canvas, xs[idx-1], ys[idx-1], xs[idx], ys[idx])
		}
	}
	for idx := range points {
		marker := '●'
		if idx == cursor {
			marker = '◆'
		}
		canvas[ys[idx]][xs[idx]] = marker
	}
	lines := make([]string, 0, height)
	lines = append(lines, ansi.Truncate(fmt.Sprintf("%s  max=%.2f", title, maximum), width, "..."))
	for row := range canvas {
		label := "        "
		if row == 0 {
			label = fmt.Sprintf("%7.2f ", maximum)
		} else if row == len(canvas)-1 {
			label = "   0.00 "
		}
		lines = append(lines, ansi.Truncate(label+"│ "+string(canvas[row]), width, ""))
	}
	lines = append(lines, ansi.Truncate("        └"+strings.Repeat("─", plotWidth), width, ""))
	return strings.Join(lines, "\n")
}

func drawCheckTUILine(canvas [][]rune, x0, y0, x1, y1 int) {
	dx := absInt(x1 - x0)
	sx := -1
	if x0 < x1 {
		sx = 1
	}
	dy := -absInt(y1 - y0)
	sy := -1
	if y0 < y1 {
		sy = 1
	}
	err := dx + dy
	for {
		if y0 >= 0 && y0 < len(canvas) && x0 >= 0 && x0 < len(canvas[y0]) && canvas[y0][x0] == ' ' {
			canvas[y0][x0] = '·'
		}
		if x0 == x1 && y0 == y1 {
			break
		}
		twice := 2 * err
		if twice >= dy {
			err += dy
			x0 += sx
		}
		if twice <= dx {
			err += dx
			y0 += sy
		}
	}
}

func formatByteSize(size int64) string {
	for _, unit := range []struct {
		name  string
		value int64
	}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}} {
		if size >= unit.value && size%unit.value == 0 {
			return fmt.Sprintf("%d %s", size/unit.value, unit.name)
		}
	}
	return fmt.Sprintf("%d B", size)
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func (m checkTUIModel) renderResultTable(width, height int) string {
	table := m.currentTable()
	if len(table.Headers) == 0 {
		return lipgloss.NewStyle().Width(width).Height(height).Render("Waiting for result plan...")
	}
	widths := tableColumnWidths(table)
	var lines []string
	lines = append(lines, "  "+formatTableLine(table.Headers, widths), "  "+formatTableSeparator(widths))
	start := minInt(m.listOffset, len(table.Items))
	end := minInt(len(table.Items), start+maxInt(1, height-2))
	for idx := start; idx < end; idx++ {
		marker := "  "
		if idx == m.selected {
			marker = "> "
		}
		line := marker + formatTableLine(table.Items[idx].Cells, widths)
		line = horizontalWindow(line, m.horizontalOffset, width)
		if table.Items[idx].Status == "FAIL" || table.Items[idx].Status == "ABORT" {
			line = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render(line)
		} else if table.Items[idx].Status == "ABORTING" {
			line = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render(line)
		} else if table.Items[idx].Status == "PASS" || table.Items[idx].Status == "DONE" {
			line = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render(line)
		}
		lines = append(lines, line)
	}
	for idx := 0; idx < minInt(2, len(lines)); idx++ {
		lines[idx] = horizontalWindow(lines[idx], m.horizontalOffset, width)
	}
	renderHeight := minInt(height, maxInt(1, len(lines)))
	return lipgloss.NewStyle().Width(width).Height(renderHeight).Render(strings.Join(lines, "\n"))
}

func (m checkTUIModel) renderDetails(width, height int) string {
	items := m.currentTable().Items
	if len(items) == 0 || m.selected >= len(items) {
		return lipgloss.NewStyle().Width(width).Height(height).Render("Details\nNo result selected.")
	}
	lines := wrapCheckTUIText(items[m.selected].Detail, maxInt(20, width))
	start := minInt(m.detailOffset, maxInt(0, len(lines)-1))
	end := minInt(len(lines), start+maxInt(1, height-1))
	body := "Details\n" + strings.Join(lines[start:end], "\n")
	return lipgloss.NewStyle().Width(width).Height(height).Render(body)
}

func (m checkTUIModel) renderTextPage(width int, lines []string, empty string) string {
	height := m.contentHeight()
	if len(lines) == 0 {
		return lipgloss.NewStyle().Width(width).Render(empty)
	}
	start := minInt(m.textOffset, maxInt(0, len(lines)-1))
	end := minInt(len(lines), start+height)
	visible := make([]string, 0, end-start)
	for _, line := range lines[start:end] {
		visible = append(visible, horizontalWindow(line, m.horizontalOffset, width))
	}
	renderHeight := minInt(height, maxInt(1, len(visible)))
	return lipgloss.NewStyle().Width(width).Height(renderHeight).Render(strings.Join(visible, "\n"))
}

func (m checkTUIModel) stageBar(width int) string {
	if len(m.stages) == 0 {
		return "Waiting for checks..."
	}
	parts := make([]string, 0, len(m.stages))
	for idx, stage := range m.stages {
		complete, total, failed := sectionProgress(m.tables[stage].Items)
		label := fmt.Sprintf("%s %d/%d", stage, complete, total)
		if failed > 0 {
			label += fmt.Sprintf(" !%d", failed)
		}
		style := lipgloss.NewStyle().Padding(0, 1)
		if idx == m.stage {
			style = style.Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("62"))
		}
		parts = append(parts, style.Render(label))
	}
	return fitCheckTUIText(strings.Join(parts, " "), width)
}

func (m checkTUIModel) tabBar(width int) string {
	parts := make([]string, 0, len(checkTUITabNames))
	for idx, name := range checkTUITabNames {
		style := lipgloss.NewStyle().Padding(0, 1)
		if idx == m.tab {
			style = style.Bold(true).Underline(true).Foreground(lipgloss.Color("39"))
		}
		parts = append(parts, style.Render(name))
	}
	return fitCheckTUIText(strings.Join(parts, "  "), width)
}

func (m checkTUIModel) currentStage() string {
	if m.stage < 0 || m.stage >= len(m.stages) {
		return ""
	}
	return m.stages[m.stage]
}

func (m checkTUIModel) currentTable() checkTUITable { return m.tables[m.currentStage()] }

func (m checkTUIModel) currentTextLines() []string {
	if m.tab == checkTUITabCounters {
		return m.counterLines[m.currentStage()]
	}
	return m.logLines[m.currentStage()]
}

func (m checkTUIModel) stageIndex(stage string) int {
	for idx, current := range m.stages {
		if current == stage {
			return idx
		}
	}
	return -1
}

func (m checkTUIModel) contentHeight() int {
	headerAndFooter := 7
	if m.width < 72 {
		headerAndFooter++
	}
	return maxInt(3, m.height-headerAndFooter)
}

func (m checkTUIModel) resultListHeight() int {
	if !m.detailOpen || m.width >= 140 {
		return maxInt(1, m.contentHeight()-2)
	}
	return maxInt(1, m.contentHeight()/2-2)
}

func tableColumnWidths(table checkTUITable) []int {
	widths := make([]int, len(table.Headers))
	for idx, header := range table.Headers {
		widths[idx] = len(header)
	}
	for _, item := range table.Items {
		for idx, cell := range item.Cells {
			if idx < len(widths) && len(cell) > widths[idx] {
				widths[idx] = len(cell)
			}
		}
	}
	return widths
}

func sectionProgress(items []checkTUIItem) (complete, total, failed int) {
	for _, item := range items {
		if item.Status == "PASS" || item.Status == "FAIL" || item.Status == "WARN" || item.Status == "DONE" || item.Status == "INFO" || item.Status == "SAME" || item.Status == "ABORT" {
			complete++
		}
		if item.Status == "FAIL" || item.Status == "ABORT" {
			failed++
		}
	}
	return complete, len(items), failed
}

func horizontalWindow(value string, offset, width int) string {
	if width <= 0 || offset >= ansi.StringWidth(value) {
		return ""
	}
	return ansi.Cut(value, maxInt(0, offset), maxInt(0, offset)+width)
}

func fitCheckTUIText(value string, width int) string {
	if width <= 3 || ansi.StringWidth(value) <= width {
		return value
	}
	return ansi.Truncate(value, width, "...")
}

func adaptiveCheckTUIFooter(value string, width int) string {
	lines := strings.Split(value, "\n")
	for index, line := range lines {
		if ansi.StringWidth(line) <= width {
			continue
		}
		short := line
		switch {
		case strings.Contains(line, "q: abort XCCL stage"):
			short = "[/] stage | q XCCL-stage | Esc stage (x2 all) | p table | m mode | Left/Right point"
		case strings.Contains(line, "m: active direction") && strings.Contains(line, "q: abort item"):
			short = "[/] stage | p table | m active | Arrows | Pg rows | Space detail | Ctrl+U/D detail page | q item | Esc stage (x2 all)"
		case strings.Contains(line, "m: active direction"):
			short = "[/] stage | p table | m active | Arrows | Pg rows | Space detail | Ctrl+U/D detail page | Enter retest | q exit"
		case strings.Contains(line, "Enter: retest"):
			short = "PgUp/PgDown list | Ctrl+U/Ctrl+D detail\n[/] stage | Tab | Up/Down | Space | Enter retest | q exit"
		case strings.Contains(line, "q: abort item"):
			short = "PgUp/PgDown page | Ctrl+U/Ctrl+D detail\n[/] stage | Tab | Up/Down | Space | q item | Esc stage (x2 all)"
		case strings.Contains(line, "Enter/q: exit") && strings.Contains(line, "switch stage"):
			short = "PgUp/PgDown page | Ctrl+U/Ctrl+D detail\n[/] stage | Tab | Up/Down | Space | Enter/q exit"
		case strings.Contains(line, "Enter/q exit"):
			short = "p table | m mode | Left/Right point | Enter/q exit"
		}
		shortLines := strings.Split(short, "\n")
		for shortIndex := range shortLines {
			shortLines[shortIndex] = ansi.Truncate(shortLines[shortIndex], width, "...")
		}
		lines[index] = strings.Join(shortLines, "\n")
	}
	return strings.Join(lines, "\n")
}

func pinCheckTUIFooter(body, footer string, height int) string {
	bodyLines := []string{}
	if body != "" {
		bodyLines = strings.Split(body, "\n")
	}
	footerLines := strings.Split(footer, "\n")
	padding := height - len(bodyLines) - len(footerLines)
	if padding < 0 {
		padding = 0
	}
	lines := make([]string, 0, len(bodyLines)+padding+len(footerLines))
	lines = append(lines, bodyLines...)
	lines = append(lines, make([]string, padding)...)
	lines = append(lines, footerLines...)
	return strings.Join(lines, "\n")
}

func wrapCheckTUIText(value string, width int) []string {
	if width <= 0 {
		return strings.Split(value, "\n")
	}
	var lines []string
	for _, raw := range strings.Split(value, "\n") {
		if raw == "" {
			lines = append(lines, "")
			continue
		}
		for len(raw) > width {
			lines = append(lines, raw[:width])
			raw = raw[width:]
		}
		lines = append(lines, raw)
	}
	return lines
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

var _ io.Writer = (*checkTUIController)(nil)
