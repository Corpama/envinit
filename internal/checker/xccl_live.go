package checker

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"envinit/internal/spec"
)

type xcclLiveTracker struct {
	table          *liveResultTable
	rowByID        map[string]int
	cfg            spec.CheckXCCLConfig
	hosts          []string
	xpuOrders      []string
	orderingMode   string
	orderingReason string
	ranks          int
	topology       string
	finalized      bool
	finalStatus    string
}

func showXCCLTUIInitialFailure(opts Options, cfg spec.CheckXCCLConfig, plans []xcclTargetPlan, targets []Target, err error) {
	if opts.checkTUI == nil || err == nil {
		return
	}
	hosts := make([]string, 0, maxInt(len(plans), len(targets)))
	ranks := 0
	for _, plan := range plans {
		hosts = append(hosts, plan.Target.Name)
		ranks += plan.XPUCount
	}
	if len(hosts) == 0 {
		for _, target := range targets {
			hosts = append(hosts, target.Name)
		}
	}
	topology := "UNKNOWN"
	if len(plans) > 0 {
		topology = "PIX"
		if xcclPlansDegraded(plans) {
			topology = "DEGRADED"
		}
	}
	orderingMode, orderingReason := xcclResolvedOrderingDisplay(cfg, plans)
	tracker := &xcclLiveTracker{cfg: cfg, hosts: hosts, xpuOrders: xcclPlanOrderLabels(plans), orderingMode: orderingMode, orderingReason: orderingReason, ranks: ranks, topology: topology}
	opts.checkTUI.SetResults(checkTUIStageXCCL, xcclTUIHeaders(), []checkTUIItem{tracker.failureItem(err)})
}

func newXCCLLiveTracker(output io.Writer, enabled bool, cfg spec.CheckXCCLConfig, plans []xcclTargetPlan, totalRanks int) *xcclLiveTracker {
	sizes, ok := xcclConfiguredSizes(cfg)
	if !enabled || !ok {
		return nil
	}
	hosts := make([]string, 0, len(plans))
	for _, plan := range plans {
		hosts = append(hosts, plan.Target.Name)
	}
	topology := "PIX"
	if xcclPlansDegraded(plans) {
		topology = "DEGRADED"
	}
	orderingMode, orderingReason := xcclResolvedOrderingDisplay(cfg, plans)
	tracker := &xcclLiveTracker{rowByID: map[string]int{}, cfg: cfg, hosts: hosts, xpuOrders: xcclPlanOrderLabels(plans), orderingMode: orderingMode, orderingReason: orderingReason, ranks: totalRanks, topology: topology}
	var rows [][]string
	var tuiItems []checkTUIItem
	for _, mode := range xcclPlotModes {
		for _, size := range sizes {
			tracker.rowByID[xcclLiveRowID(size, mode)] = len(rows)
			rows = append(rows, []string{"WAIT", strconv.FormatInt(size, 10), mode, "-", "-", "-"})
			tuiItems = append(tuiItems, tracker.sizeItem(xcclPerformanceRow{SizeBytes: size, Mode: mode, DataType: cfg.DataType, Operation: cfg.Test}, "WAIT", false))
		}
	}
	_, tuiEnabled := output.(*checkTUIController)
	tracker.table = newLiveResultTable(output, !tuiEnabled, "XCCL live results:", []string{"STATUS", "SIZE(B)", "MODE", "TIME(us)", "ALGBW(GB/s)", "BUSBW(GB/s)"}, rows)
	if controller, ok := output.(*checkTUIController); ok {
		controller.SetResults(checkTUIStageXCCL, xcclTUIHeaders(), tuiItems)
	}
	return tracker
}

func (t *xcclLiveTracker) ConsumeLine(line string) {
	if t == nil {
		return
	}
	for _, row := range parseXCCLPerformanceRows(line) {
		index, ok := t.rowByID[xcclLiveRowID(row.SizeBytes, row.Mode)]
		if !ok {
			continue
		}
		t.table.Update(index, []string{
			"DONE", strconv.FormatInt(row.SizeBytes, 10), row.Mode,
			fmt.Sprintf("%.2f", row.TimeUS), fmt.Sprintf("%.2f", row.AlgGBs), fmt.Sprintf("%.2f", row.BusGBs),
		})
		if controller, ok := t.table.output.(*checkTUIController); ok {
			controller.Update(t.sizeItem(row, "DONE", false))
		}
	}
}

func (t *xcclLiveTracker) Finalize(evaluation xcclEvaluation) {
	if t == nil || t.finalized {
		return
	}
	t.finalized = true
	t.finalStatus = evaluation.Status
	controller, ok := t.table.output.(*checkTUIController)
	if !ok {
		return
	}
	controller.Update(t.sizeItem(evaluation.Selected, evaluation.Status, true))
}

func (t *xcclLiveTracker) Fail(err error) {
	if t == nil || err == nil {
		return
	}
	if t.finalized && t.finalStatus == "FAIL" {
		return
	}
	if controller, ok := t.table.output.(*checkTUIController); ok {
		controller.Update(t.failureItem(err))
	}
}

func xcclTUIHeaders() []string {
	return []string{"STATUS", "EVAL", "LAYOUT", "MODE", "SIZE(B)", "TYPE", "OP", "TIME(us)", "ALGBW(GB/s)", "BUSBW(GB/s)"}
}

func (t *xcclLiveTracker) failureItem(err error) checkTUIItem {
	detail := fmt.Sprintf("XCCL failure\nTest: %s\nLayout: %s\nRequested ordering: %s\nResolved ordering: %s\nOrdering reason: %s\nXPU order: %s\nHosts: %s\nRanks: %d (%s)\nTopology: %s\nEvaluation: %s\n\nFailure:\n%s",
		t.cfg.Test, normalizedXCCLLayout(t.cfg.Layout), normalizedXCCLXPUOrdering(t.cfg.XPUOrdering), firstNonEmpty(t.orderingMode, "unknown"), firstNonEmpty(t.orderingReason, "not recorded"), strings.Join(t.xpuOrders, "; "), strings.Join(t.hosts, ","), t.ranks, xcclRankSource(t.cfg), t.topology, xcclEvaluationReview(t.cfg), err)
	return checkTUIItem{
		ID: "xccl-error", Section: checkTUIStageXCCL, Status: "FAIL",
		Cells:  []string{"FAIL", "", normalizedXCCLLayout(t.cfg.Layout), "-", "-", "-", "-", "-", "-", "-"},
		Detail: detail,
	}
}

func (t *xcclLiveTracker) sizeItem(row xcclPerformanceRow, status string, evaluated bool) checkTUIItem {
	eval := ""
	if evaluated {
		eval = "*"
	}
	detail := fmt.Sprintf("XCCL size result\nTest: %s\nLayout: %s\nRequested ordering: %s\nResolved ordering: %s\nOrdering reason: %s\nXPU order: %s\nHosts: %s\nRanks: %d (%s)\nTopology: %s\nEvaluation: %s\n\nEvaluation row: %s\nSize: %d bytes\nMode: %s\nData type: %s\nOperation: %s\nStatus: %s\n\nTime: %.2f us\nAlgorithm bandwidth: %.2f GB/s\nBus bandwidth: %.2f GB/s",
		t.cfg.Test, normalizedXCCLLayout(t.cfg.Layout), normalizedXCCLXPUOrdering(t.cfg.XPUOrdering), firstNonEmpty(t.orderingMode, "unknown"), firstNonEmpty(t.orderingReason, "not recorded"), strings.Join(t.xpuOrders, "; "), strings.Join(t.hosts, ","), t.ranks, xcclRankSource(t.cfg), t.topology, xcclEvaluationReview(t.cfg),
		firstNonEmpty(eval, "no"), row.SizeBytes, row.Mode, firstNonEmpty(row.DataType, t.cfg.DataType), firstNonEmpty(row.Operation, t.cfg.Test), status, row.TimeUS, row.AlgGBs, row.BusGBs)
	if evaluated && normalizedXCCLEvaluationMode(t.cfg.EvaluationMode) == "auto" {
		baseline, required := xcclAutomaticEvaluationRule(t.cfg)
		detail += fmt.Sprintf("\nBaseline: %.2f GB/s\nUtilization: %.3f%%\nRequired: >%.2f%%", baseline, row.BusGBs/baseline*100, required*100)
	}
	cells := []string{status, eval, normalizedXCCLLayout(t.cfg.Layout), row.Mode, strconv.FormatInt(row.SizeBytes, 10), firstNonEmpty(row.DataType, t.cfg.DataType), firstNonEmpty(row.Operation, t.cfg.Test)}
	cells = append(cells, xcclMetricCells(row, status)...)
	ready := status == "DONE" || status == "PASS" || status == "FAIL" || status == "WARN"
	return checkTUIItem{
		ID: "xccl-" + xcclLiveRowID(row.SizeBytes, row.Mode), Section: checkTUIStageXCCL, Status: status,
		Cells:  cells,
		Detail: detail,
		Plot:   &checkTUIPlotPoint{Mode: row.Mode, Size: row.SizeBytes, AlgBW: row.AlgGBs, BusBW: row.BusGBs, Ready: ready},
	}
}

func xcclPlanOrderLabels(plans []xcclTargetPlan) []string {
	labels := make([]string, 0, len(plans))
	for _, plan := range plans {
		labels = append(labels, plan.Target.Name+"="+xcclPlanVisibleDevices(plan))
	}
	return labels
}

func xcclResolvedOrderingDisplay(cfg spec.CheckXCCLConfig, plans []xcclTargetPlan) (string, string) {
	requested := normalizedXCCLXPUOrdering(cfg.XPUOrdering)
	layout := normalizedXCCLLayout(cfg.Layout)
	if len(plans) == 0 {
		switch {
		case requested != "auto":
			return requested, "configured explicitly; topology result unavailable"
		case layout == "same_index":
			return "physical", "same_index preserves physical XPU indices"
		case layout == "single_host":
			return "physical", "single-host execution preserves physical XPU indices"
		default:
			return "auto", "topology resolution did not complete"
		}
	}

	physical := xcclPlansUsePhysicalXPUOrder(plans)
	if requested == "physical" {
		return "physical", "configured explicitly"
	}
	if requested == "rail_aligned" {
		if physical {
			return "physical", "rail alignment requested; physical rail order already matches"
		}
		return "rail_aligned", "configured rail alignment reordered at least one host"
	}
	if layout == "same_index" {
		return "physical", "same_index preserves physical XPU indices"
	}
	if layout == "single_host" {
		return "physical", "single-host execution preserves physical XPU indices"
	}
	if physical {
		return "physical", "physical rail order already matches across hosts"
	}
	return "rail_aligned", "cross-host physical rail order differs"
}

func xcclPlansUsePhysicalXPUOrder(plans []xcclTargetPlan) bool {
	for _, plan := range plans {
		if len(plan.XPUOrder) == 0 {
			continue
		}
		if len(plan.XPUOrder) != plan.XPUCount {
			return false
		}
		for logicalIndex, physicalIndex := range plan.XPUOrder {
			if logicalIndex != physicalIndex {
				return false
			}
		}
	}
	return true
}

func xcclRankSource(cfg spec.CheckXCCLConfig) string {
	if cfg.Ranks > 0 {
		return "manual"
	}
	return "auto"
}

func xcclMetricCells(row xcclPerformanceRow, status string) []string {
	timeUS, algGBs, busGBs := "-", "-", "-"
	if status == "DONE" || status == "PASS" || status == "FAIL" || status == "WARN" {
		timeUS = fmt.Sprintf("%.2f", row.TimeUS)
		algGBs = fmt.Sprintf("%.2f", row.AlgGBs)
		busGBs = fmt.Sprintf("%.2f", row.BusGBs)
	}
	return []string{timeUS, algGBs, busGBs}
}

func xcclLiveRowID(size int64, mode string) string {
	return strconv.FormatInt(size, 10) + "\x00" + mode
}

func xcclConfiguredSizes(cfg spec.CheckXCCLConfig) ([]int64, bool) {
	minimum, ok := parseXCCLSize(cfg.MinBytes)
	if !ok {
		return nil, false
	}
	maximum, ok := parseXCCLSize(cfg.MaxBytes)
	if !ok || maximum < minimum || cfg.StepFactor <= 1 {
		return nil, false
	}
	var sizes []int64
	for value := minimum; value <= maximum; {
		sizes = append(sizes, value)
		if len(sizes) > 256 || value > maximum/int64(cfg.StepFactor) {
			break
		}
		value *= int64(cfg.StepFactor)
	}
	return sizes, len(sizes) > 0
}

func parseXCCLSize(raw string) (int64, bool) {
	value := strings.ToLower(strings.TrimSpace(raw))
	value = strings.TrimSuffix(value, "b")
	multiplier := int64(1)
	if len(value) > 0 {
		switch value[len(value)-1] {
		case 'k':
			multiplier = 1 << 10
			value = value[:len(value)-1]
		case 'm':
			multiplier = 1 << 20
			value = value[:len(value)-1]
		case 'g':
			multiplier = 1 << 30
			value = value[:len(value)-1]
		}
	}
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil || number <= 0 || number > (1<<63-1)/multiplier {
		return 0, false
	}
	return number * multiplier, true
}

type lineCallbackWriter struct {
	mu       sync.Mutex
	pending  string
	callback func(string)
}

func (w *lineCallbackWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending += string(p)
	for {
		index := strings.IndexByte(w.pending, '\n')
		if index < 0 {
			break
		}
		line := w.pending[:index]
		w.pending = w.pending[index+1:]
		w.callback(line)
	}
	return len(p), nil
}

func (w *lineCallbackWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pending != "" {
		w.callback(w.pending)
		w.pending = ""
	}
}
