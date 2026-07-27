package checker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"envinit/internal/spec"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type streamingProbeWriter struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	writes chan string
}

func (w *streamingProbeWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.buffer.Write(p)
	w.mu.Unlock()
	select {
	case w.writes <- string(p):
	default:
	}
	return n, err
}

func (w *streamingProbeWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

func TestRunCommandStreamingPublishesOutputBeforeCompletion(t *testing.T) {
	unblock := filepath.Join(t.TempDir(), "continue")
	live := &streamingProbeWriter{writes: make(chan string, 8)}
	type commandResult struct {
		output string
		err    error
	}
	done := make(chan commandResult, 1)
	command := fmt.Sprintf("printf 'first\\n'; while [ ! -f %s ]; do sleep 0.01; done; printf 'second\\n'", shellQuote(unblock))
	go func() {
		output, err := runCommandStreaming(spec.CheckConfig{}, Target{Name: "self", Local: true}, command, live, live)
		done <- commandResult{output: output, err: err}
	}()

	deadline := time.After(2 * time.Second)
	seenFirst := false
	for !seenFirst {
		select {
		case chunk := <-live.writes:
			seenFirst = strings.Contains(chunk, "first") || strings.Contains(live.String(), "first")
		case result := <-done:
			t.Fatalf("command completed before it was unblocked: output=%q err=%v", result.output, result.err)
		case <-deadline:
			t.Fatal("timed out waiting for streamed command output")
		}
	}
	if err := os.WriteFile(unblock, []byte("ok"), 0o600); err != nil {
		t.Fatalf("unblock command: %v", err)
	}

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("run streaming command: %v", result.err)
		}
		if result.output != "first\nsecond\n" {
			t.Fatalf("unexpected captured output: %q", result.output)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for streaming command to complete")
	}
	if got := live.String(); got != "first\nsecond\n" {
		t.Fatalf("unexpected live output: %q", got)
	}
}

func TestRunCheckCommandCancellationStopsCurrentProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	started := time.Now()
	_, err := runCheckCommand(Options{Context: ctx}, Target{Name: "self", Local: true}, "sleep 30")
	if !errors.Is(err, errCheckCanceled) {
		t.Fatalf("canceled command error = %v, want errCheckCanceled", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("canceled command took %v", elapsed)
	}
}

func TestLiveResultTableUpdatesRowsInPlace(t *testing.T) {
	var output bytes.Buffer
	table := newLiveResultTable(&output, true, "Live results:", []string{"STATUS", "COMBINATION", "RESULT"}, [][]string{
		{"WAIT", "node1/eth1 -> node2/eth2", "-"},
		{"WAIT", "node1/eth2 -> node2/eth1", "-"},
	})
	table.Update(0, []string{"RUNNING", "node1/eth1 -> node2/eth2", "-"})
	table.Update(0, []string{"PASS", "node1/eth1 -> node2/eth2", "387.42 Gbps"})
	got := output.String()
	for _, want := range []string{
		"WAIT",
		"RUNNING",
		"\033[5A",
		"PASS",
		"387.42 Gbps",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in live table output:\n%q", want, got)
		}
	}
}

func TestBandwidthLiveTrackerRendersFullMatrixBeforeUpdates(t *testing.T) {
	var output bytes.Buffer
	targets := []Target{
		{Name: "node1", RDMA: []spec.RDMARecord{{Name: "eth1", IP: "10.0.1.1"}}},
		{Name: "node2", RDMA: []spec.RDMARecord{{Name: "eth1", IP: "10.0.1.2"}}},
	}
	groups := resolvedRDMAGroups{
		"node1": {{IBDevice: "mlx5_1"}},
		"node2": {{IBDevice: "mlx5_2"}},
	}
	tracker := newBandwidthLiveTracker(Options{
		Bundle: spec.Bundle{Check: spec.CheckConfig{Bandwidth: spec.CheckBandwidthConfig{BasePort: 18515}}},
		Output: &output, LiveOutput: true,
	}, targets, groups)
	initial := output.String()
	if got := strings.Count(initial, "WAIT"); got != 2 {
		t.Fatalf("initial bandwidth matrix contains %d WAIT rows, want both directions:\n%q", got, initial)
	}
	for _, want := range []string{"node1", "node2", "mlx5_1", "mlx5_2", "18515"} {
		if !strings.Contains(initial, want) {
			t.Fatalf("initial bandwidth matrix missing %q:\n%q", want, initial)
		}
	}
	stream := bandwidthStreamsForGroups(spec.CheckBandwidthConfig{BasePort: 18515}, groups["node1"], groups["node2"])[0]
	tracker.MarkRunning(targets[0], targets[1], []checkStream{stream})
	result := resultFromOutput(spec.CheckBandwidthConfig{}, targets[0], targets[1], stream, "65536 100 400.00 387.42")
	tracker.Complete(result)
	got := output.String()
	for _, want := range []string{"RUNNING", "PASS", "387.42 Gbps"} {
		if !strings.Contains(got, want) {
			t.Fatalf("updated bandwidth matrix missing %q:\n%q", want, got)
		}
	}
}

func TestBandwidthAutoThresholdUsesSlowerEndpointAndThirtyPercentTolerance(t *testing.T) {
	cfg := spec.CheckBandwidthConfig{MinGBitsAuto: true}
	server := Target{Name: "server", RDMA: []spec.RDMARecord{{Name: "eth1", IP: "10.0.0.1"}}}
	client := Target{Name: "client", RDMA: []spec.RDMARecord{{Name: "eth1", IP: "10.0.0.2"}}}
	stream := checkStream{
		ServerRDMAIndex: 0, ClientRDMAIndex: 0,
		ServerGroup: spec.CheckRDMAGroup{IBDevice: "mlx5_1"}, ClientGroup: spec.CheckRDMAGroup{IBDevice: "mlx5_2"},
		ServerSpeed: bandwidthNICSpeed{MaximumMbps: 400000, CurrentMbps: 400000},
		ClientSpeed: bandwidthNICSpeed{MaximumMbps: 200000, CurrentMbps: 200000},
	}
	passed := resultFromOutput(cfg, server, client, stream, "65536 100 150.00 150.00")
	if !passed.Passed || passed.BaselineGBits != 200 || passed.ThresholdGBits != 140 || passed.ThresholdMode != "auto" {
		t.Fatalf("unexpected auto threshold result: %#v", passed)
	}
	failed := resultFromOutput(cfg, server, client, stream, "65536 100 130.00 130.00")
	if failed.Passed {
		t.Fatalf("130 Gbps unexpectedly passed 140 Gbps threshold: %#v", failed)
	}
}

func TestBandwidthAutoThresholdFailsWhenEndpointMaximumIsUnknown(t *testing.T) {
	cfg := spec.CheckBandwidthConfig{MinGBitsAuto: true}
	stream := checkStream{ServerSpeed: bandwidthNICSpeed{MaximumMbps: 400000}}
	result := resultFromOutput(cfg, Target{Name: "server"}, Target{Name: "client"}, stream, "65536 100 390.00 390.00")
	if result.Passed || !strings.Contains(result.ThresholdError, "client max=unknown") {
		t.Fatalf("unknown auto baseline was not rejected: %#v", result)
	}
	cfg = spec.CheckBandwidthConfig{MinGBits: 0, MinGBitsSet: true}
	result = resultFromOutput(cfg, Target{Name: "server"}, Target{Name: "client"}, stream, "65536 100 1.00 1.00")
	if !result.Passed || result.ThresholdMode != "disabled" {
		t.Fatalf("explicit zero should only record bandwidth: %#v", result)
	}
}

func TestParseBandwidthSpeedProbe(t *testing.T) {
	got := parseBandwidthSpeedProbe("SPEED|0|eth1|400000|400000\nSPEED|1|eth2|100000|200000\n")
	if got[0].MaximumMbps != 400000 || got[0].CurrentMbps != 400000 || got[1].MaximumMbps != 200000 || got[1].CurrentMbps != 100000 {
		t.Fatalf("unexpected speed probe parse: %#v", got)
	}
}

func TestGeneratedBandwidthSpeedProbeShellIsPortable(t *testing.T) {
	command := bandwidthSpeedProbeCommand(Target{RDMA: []spec.RDMARecord{{Name: "eth1"}, {Name: "eth2"}}})
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(command)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated speed probe is invalid shell: %v\n%s\n%s", err, output, command)
	}
}

func TestBandwidthTUIHeatmapSwitchesDirectionAndMovesCells(t *testing.T) {
	cfg := spec.CheckBandwidthConfig{MinGBitsAuto: true}
	nodeA := Target{Name: "node-a", RDMA: []spec.RDMARecord{{Name: "eth1"}, {Name: "eth2"}}}
	nodeB := Target{Name: "node-b", RDMA: []spec.RDMARecord{{Name: "eth1"}, {Name: "eth2"}}}
	var items []checkTUIItem
	for _, pair := range [][2]Target{{nodeA, nodeB}, {nodeB, nodeA}} {
		for clientIndex := 0; clientIndex < 2; clientIndex++ {
			for serverIndex := 0; serverIndex < 2; serverIndex++ {
				stream := checkStream{ClientRDMAIndex: clientIndex, ServerRDMAIndex: serverIndex, ClientSpeed: bandwidthNICSpeed{MaximumMbps: 400000}, ServerSpeed: bandwidthNICSpeed{MaximumMbps: 400000}}
				result := resultFromOutput(cfg, pair[0], pair[1], stream, fmt.Sprintf("65536 100 %.2f %.2f", 385+float64(clientIndex+serverIndex), 385+float64(clientIndex+serverIndex)))
				items = append(items, bandwidthTUIItem(result, "PASS"))
			}
		}
	}
	model := newCheckTUIModel()
	model.width, model.height = 120, 28
	model.finished = true
	model.setStages([]string{checkTUIStageBandwidth})
	model.setResults(checkTUIStageBandwidth, bandwidthResultHeaders(), items)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	model = updated.(checkTUIModel)
	for _, want := range []string{"both directions shown", "> Direction 1: CLIENT node-b  ->  SERVER node-a", "Direction 2: CLIENT node-a  ->  SERVER node-b", "CLIENT\\SERVER", "baseline=400.00 Gbps", "threshold=280.00 Gbps", "utilization=96.2%", "p table", "m active"} {
		if view := model.View(); !strings.Contains(view, want) {
			t.Fatalf("bandwidth heatmap missing %q:\n%s", want, view)
		}
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRight})
	model = updated.(checkTUIModel)
	if got := model.currentTable().Items[model.selected].Heatmap.ServerAxis; got != "eth2" {
		t.Fatalf("right selected server axis %q, want eth2", got)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	model = updated.(checkTUIModel)
	if got := model.View(); !strings.Contains(got, "active direction 2/2 [node-a -> node-b]") || !strings.Contains(got, "> Direction 2: CLIENT node-a  ->  SERVER node-b") {
		t.Fatalf("m did not switch heatmap direction:\n%s", got)
	}
	lines := strings.Split(model.View(), "\n")
	if len(lines) != 28 {
		t.Fatalf("heatmap footer is not pinned to terminal bottom: lines=%d\n%s", len(lines), model.View())
	}
	for _, line := range lines {
		if width := ansi.StringWidth(line); width > 120 {
			t.Fatalf("heatmap line exceeds terminal width: width=%d line=%q", width, line)
		}
	}
}

func TestBandwidthFailureDetailsPrintBothSides(t *testing.T) {
	var output bytes.Buffer
	printBandwidthFailureDetails(&output, []Result{{
		Client: Target{Name: "node1"}, Server: Target{Name: "node2"},
		ClientGroup: spec.CheckRDMAGroup{IBDevice: "mlx5_1"}, ServerGroup: spec.CheckRDMAGroup{IBDevice: "mlx5_2"},
		Port: 18515, ClientError: "connection refused", ClientOutput: "client diagnostic",
		ServerOutput: "Couldn't listen to port 18515",
	}})
	got := output.String()
	for _, want := range []string{
		"Bandwidth failure details:",
		"CLIENT node1 error: connection refused",
		"client diagnostic",
		"SERVER node2 error: no command error reported",
		"Couldn't listen to port 18515",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("failure details missing %q:\n%s", want, got)
		}
	}
}

func TestRDMAPingFailureDetailsPreserveFullError(t *testing.T) {
	var output bytes.Buffer
	printRDMAPingFailureDetails(&output, []rdmaPingResultRow{{
		Status: "FAIL", Failure: true,
		Source: "node1", SourceNIC: "eth1", SourceIP: "10.0.1.1",
		Destination: "node2", DestinationNIC: "eth2", DestinationIP: "10.0.2.2",
		Result: "3 packets transmitted, 0 received, 100% packet loss",
	}})
	got := output.String()
	for _, want := range []string{"RDMA ping failure details:", "node1 eth1(10.0.1.1) -> node2 eth2(10.0.2.2)", "100% packet loss"} {
		if !strings.Contains(got, want) {
			t.Fatalf("ping failure details missing %q:\n%s", want, got)
		}
	}
}

func TestCollectBandwidthServerOutputReadsAndRemovesLog(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "server.log")
	if err := os.WriteFile(logPath, []byte("server-side failure\n"), 0o600); err != nil {
		t.Fatalf("write server log: %v", err)
	}
	if err := os.WriteFile(logPath+".pid", []byte("99999999\n"), 0o600); err != nil {
		t.Fatalf("write server pid: %v", err)
	}
	output, err := collectBandwidthServerOutput(Options{}, Target{Name: "server", Local: true}, "99999999", logPath)
	if err != nil {
		t.Fatalf("collect server output: %v", err)
	}
	if output != "server-side failure\n" {
		t.Fatalf("unexpected server output: %q", output)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("server log was not removed: %v", err)
	}
	if _, err := os.Stat(logPath + ".pid"); !os.IsNotExist(err) {
		t.Fatalf("server pid file was not removed: %v", err)
	}
}

func TestXCCLLiveTrackerPreRendersSizesAndConsumesRows(t *testing.T) {
	var output bytes.Buffer
	tracker := newXCCLLiveTracker(&output, true, spec.CheckXCCLConfig{MinBytes: "1k", MaxBytes: "4k", StepFactor: 2}, nil, 0)
	if tracker == nil {
		t.Fatal("expected live XCCL tracker")
	}
	initial := output.String()
	if got := strings.Count(initial, "WAIT"); got != 6 {
		t.Fatalf("initial XCCL matrix contains %d WAIT rows, want 3 sizes x 2 modes:\n%q", got, initial)
	}
	if outLast, inFirst := strings.LastIndex(initial, "out-of-place"), strings.Index(initial, "in-place"); outLast < 0 || inFirst < 0 || outLast > inFirst {
		t.Fatalf("XCCL modes are interleaved instead of grouped:\n%s", initial)
	}
	tracker.ConsumeLine("1024 256 float sum -1 12.50 8.00 7.00 0 11.50 8.50 7.50 0")
	got := output.String()
	for _, want := range []string{"DONE", "1024", "out-of-place", "in-place", "7.00", "7.50"} {
		if !strings.Contains(got, want) {
			t.Fatalf("updated XCCL matrix missing %q:\n%q", want, got)
		}
	}
}

func TestParseXCCLSize(t *testing.T) {
	for raw, want := range map[string]int64{"1024": 1024, "1k": 1024, "128m": 128 << 20, "1GB": 1 << 30} {
		got, ok := parseXCCLSize(raw)
		if !ok || got != want {
			t.Fatalf("parseXCCLSize(%q) = %d,%v want %d,true", raw, got, ok, want)
		}
	}
}

func TestXCCLTUIUsesSelectedSizeRowForFinalThresholdEvaluation(t *testing.T) {
	cfg := spec.CheckXCCLConfig{Test: "all_reduce_perf", DataType: "float32", EvaluationMode: "manual", MinBusBandwidthGBs: 60}
	tracker := &xcclLiveTracker{cfg: cfg, hosts: []string{"node1", "node2"}, orderingMode: "physical", orderingReason: "physical rail order already matches across hosts", ranks: 16, topology: "PIX"}
	selected := xcclPerformanceRow{SizeBytes: 134217728, Count: 33554432, DataType: "float32", Operation: "sum", Mode: "in-place", TimeUS: 2200, AlgGBs: 58, BusGBs: 55}
	evaluation := xcclEvaluation{Status: "FAIL", Selected: selected, Hosts: tracker.hosts, Topology: tracker.topology}

	selectedItem := tracker.sizeItem(selected, evaluation.Status, true)
	if selectedItem.Status != "FAIL" || !containsString(selectedItem.Cells, "*") {
		t.Fatalf("selected SOP row was not marked failed/evaluated: %#v", selectedItem)
	}
	for _, want := range []string{"FAIL", "*", "in-place", "134217728", "float32", "sum", "2200.00", "58.00", "55.00"} {
		if !containsString(selectedItem.Cells, want) {
			t.Fatalf("selected XCCL result cells missing %q: %#v", want, selectedItem.Cells)
		}
	}
	for _, want := range []string{"Test: all_reduce_perf", "Layout: full_ring", "Requested ordering: auto", "Resolved ordering: physical", "Ordering reason: physical rail order already matches across hosts", "Hosts: node1,node2", "Ranks: 16 (auto)", "Topology: PIX", "Evaluation: manual(>=60.00GB/s)"} {
		if !strings.Contains(selectedItem.Detail, want) {
			t.Fatalf("selected XCCL detail missing %q:\n%s", want, selectedItem.Detail)
		}
	}
}

func TestXCCLTUIFailureRowIsOnlyAddedForAnError(t *testing.T) {
	tracker := &xcclLiveTracker{cfg: spec.CheckXCCLConfig{Test: "all_reduce_perf"}, hosts: []string{"node1", "node2"}, ranks: 16, topology: "PIX"}
	item := tracker.failureItem(errors.New("mpirun failed"))
	if len(item.Cells) != len(xcclTUIHeaders()) || item.ID != "xccl-error" || item.Status != "FAIL" {
		t.Fatalf("unexpected XCCL failure item: %#v headers=%#v", item, xcclTUIHeaders())
	}
	if !strings.Contains(item.Detail, "mpirun failed") {
		t.Fatalf("failure detail lost original error: %s", item.Detail)
	}
	model := newCheckTUIModel()
	model.setStages([]string{checkTUIStageXCCL})
	model.setResults(checkTUIStageXCCL, xcclTUIHeaders(), nil)
	model.updateItem(item)
	if got := model.tables[checkTUIStageXCCL].Items; len(got) != 1 || got[0].ID != "xccl-error" {
		t.Fatalf("new XCCL failure row was not appended: %#v", got)
	}
}

func TestXCCLTUIPlotModeRendersTwoChartsAndMovesCursor(t *testing.T) {
	tracker := &xcclLiveTracker{cfg: spec.CheckXCCLConfig{Test: "all_reduce", DataType: "float"}, topology: "PIX"}
	var items []checkTUIItem
	for _, row := range []xcclPerformanceRow{
		{SizeBytes: 1024, Mode: "out-of-place", AlgGBs: 1.25, BusGBs: 2.10},
		{SizeBytes: 2048, Mode: "out-of-place", AlgGBs: 2.50, BusGBs: 4.20},
		{SizeBytes: 1024, Mode: "in-place", AlgGBs: 1.10, BusGBs: 1.90},
		{SizeBytes: 2048, Mode: "in-place", AlgGBs: 2.20, BusGBs: 3.80},
	} {
		items = append(items, tracker.sizeItem(row, "DONE", false))
	}
	model := newCheckTUIModel()
	model.width, model.height = 100, 28
	model.finished = true
	model.setStages([]string{checkTUIStageXCCL})
	model.setResults(checkTUIStageXCCL, xcclTUIHeaders(), items)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	model = updated.(checkTUIModel)
	view := model.View()
	for _, want := range []string{"XCCL bandwidth charts [out-of-place]", "AlgBW (GB/s)", "BusBW (GB/s)", "size=2 KiB (2048 B)", "AlgBW=2.50", "BusBW=4.20", "◆", "p table", "m mode"} {
		if !strings.Contains(view, want) {
			t.Fatalf("XCCL plot view missing %q:\n%s", want, view)
		}
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyLeft})
	model = updated.(checkTUIModel)
	if got := model.View(); !strings.Contains(got, "size=1 KiB (1024 B)") {
		t.Fatalf("left did not move the plot cursor:\n%s", got)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	model = updated.(checkTUIModel)
	if got := model.View(); !strings.Contains(got, "[in-place]") || !strings.Contains(got, "AlgBW=2.20") {
		t.Fatalf("m did not switch the plot mode:\n%s", got)
	}
	lines := strings.Split(model.View(), "\n")
	if len(lines) != 28 || !strings.Contains(lines[len(lines)-1], "p table") {
		t.Fatalf("plot footer is not pinned: lines=%d last=%q", len(lines), lines[len(lines)-1])
	}
	for _, line := range lines {
		if width := ansi.StringWidth(line); width > 100 {
			t.Fatalf("plot line exceeds terminal width: width=%d line=%q", width, line)
		}
	}
}

func TestCheckTUIModelShowsSelectableFullDetails(t *testing.T) {
	model := newCheckTUIModel()
	model.width = 140
	model.height = 30
	model.setStages([]string{checkTUIStageBandwidth})
	model.setResults(checkTUIStageBandwidth, []string{"STATUS", "CLIENT", "SERVER", "CLIENT_NIC", "SERVER_NIC", "BANDWIDTH"}, []checkTUIItem{
		{ID: "bw-1", Status: "WAIT", Cells: []string{"WAIT", "node1", "node2", "eth1", "eth1", "-"}, Detail: "waiting"},
		{ID: "bw-2", Status: "FAIL", Cells: []string{"FAIL", "node1", "node2", "eth2", "eth2", "error"}, Detail: "CLIENT ERROR:\nconnection refused\n\nSERVER OUTPUT:\naddress already in use"},
	})
	model.moveVertical(1)
	model.detailOpen = true
	view := model.View()
	for _, want := range []string{"Bandwidth", "CLIENT_NIC", "node1", "eth2", "CLIENT ERROR:", "connection refused", "SERVER OUTPUT:", "address already in use"} {
		if !strings.Contains(view, want) {
			t.Fatalf("TUI view missing %q:\n%s", want, view)
		}
	}
	model.updateItem(checkTUIItem{ID: "bw-1", Section: checkTUIStageBandwidth, Status: "PASS", Cells: []string{"PASS", "node1", "node2", "eth1", "eth1", "387.42 Gbps"}, Detail: "complete"})
	complete, total, failed := sectionProgress(model.tables[checkTUIStageBandwidth].Items)
	if complete != 2 || total != 2 || failed != 1 {
		t.Fatalf("unexpected progress: complete=%d total=%d failed=%d", complete, total, failed)
	}
}

func TestCheckTUISeparatesMainAndDetailPaging(t *testing.T) {
	model := newCheckTUIModel()
	model.width, model.height = 100, 18
	model.setStages([]string{checkTUIStagePing})
	items := make([]checkTUIItem, 40)
	for index := range items {
		items[index] = checkTUIItem{
			ID:     fmt.Sprintf("ping-%d", index),
			Status: "PASS",
			Cells:  []string{"PASS", fmt.Sprintf("source-%d", index), "dest", "eth1", "eth2", "10.0.0.1", "10.0.0.2", "4172", "ok"},
			Detail: strings.Repeat("detail line\n", 40),
		}
	}
	model.setResults(checkTUIStagePing, rdmaPingResultHeaders(), items)
	model.detailOpen = true

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	model = updated.(checkTUIModel)
	if model.selected == 0 || model.listOffset == 0 {
		t.Fatalf("PgDown did not page the main result list: selected=%d offset=%d", model.selected, model.listOffset)
	}
	if model.detailOffset != 0 {
		t.Fatalf("main list paging unexpectedly moved detail offset to %d", model.detailOffset)
	}
	selected, listOffset := model.selected, model.listOffset
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	model = updated.(checkTUIModel)
	if model.detailOffset == 0 {
		t.Fatal("Ctrl+D detail paging did not move the detail panel")
	}
	if model.selected != selected || model.listOffset != listOffset {
		t.Fatalf("detail paging moved main list: selected=%d offset=%d", model.selected, model.listOffset)
	}

	model.tab = checkTUITabLogs
	model.logLines[checkTUIStagePing] = make([]string, 80)
	model.textOffset = 0
	model.movePage(1)
	if model.textOffset == 0 {
		t.Fatal("PgDown did not page raw logs")
	}
}

func TestCheckTUIDetailPagingRequiresOpenDetails(t *testing.T) {
	model := newCheckTUIModel()
	model.setStages([]string{checkTUIStagePing})
	model.moveDetailPage(1)
	if !strings.Contains(model.notice, "Open details with Space") {
		t.Fatalf("missing detail paging hint: %q", model.notice)
	}
}

func TestCheckTUIModelProvidesThreePagesPerCheck(t *testing.T) {
	model := newCheckTUIModel()
	model.setStages([]string{checkTUIStagePing, checkTUIStageBandwidth})
	model.setResults(checkTUIStagePing, rdmaPingResultHeaders(), []checkTUIItem{{
		ID: "ping-0", Status: "PASS", Cells: []string{"PASS", "node1", "node2", "eth1", "eth2", "10.0.0.1", "10.0.0.2", "4172", "ok"}, Detail: "full ping detail",
	}})
	model.appendText(checkTUIStagePing, checkTUITabCounters, "NIC counter delta summary:\nSTATUS NODE IFACE COUNTER BEFORE AFTER DELTA")
	model.appendText(checkTUIStagePing, checkTUITabLogs, "PASS rdma-ping node1 eth1 -> node2 eth2: ok")

	results := model.View()
	for _, want := range rdmaPingResultHeaders() {
		if !strings.Contains(results, want) {
			t.Fatalf("results page missing column %q:\n%s", want, results)
		}
	}
	model.moveTab(1)
	if got := model.View(); !strings.Contains(got, "NIC counter delta summary:") {
		t.Fatalf("counter page missing original summary:\n%s", got)
	}
	model.moveTab(1)
	if got := model.View(); !strings.Contains(got, "PASS rdma-ping node1") {
		t.Fatalf("raw log page missing ping output:\n%s", got)
	}
}

func TestCheckTUIModelAdaptsToTerminalResizeWithoutTruncatingTabs(t *testing.T) {
	model := newCheckTUIModel()
	model.setStages([]string{checkTUIStagePing})
	model.setResults(checkTUIStagePing, rdmaPingResultHeaders(), []checkTUIItem{{
		ID: "ping-0", Status: "FAIL", Cells: []string{"FAIL", "source", "dest", "eth1", "eth2", "10.0.0.1", "10.0.0.2", "4172", "packet loss"}, Detail: "full detail",
	}})
	model.appendText(checkTUIStagePing, checkTUITabCounters, "NIC counter delta summary:\nSTATUS NODE IFACE COUNTER BEFORE AFTER DELTA")
	model.finished = true
	model.finalError = "ping failed"

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 64, Height: 18})
	model = updated.(checkTUIModel)
	for _, tab := range checkTUITabNames {
		if got := model.View(); !strings.Contains(got, tab) {
			t.Fatalf("64-column view truncated tab %q:\n%s", tab, got)
		}
	}
	for _, line := range strings.Split(model.View(), "\n") {
		if width := ansi.StringWidth(line); width > 64 {
			t.Fatalf("64-column view rendered a %d-cell line:\n%q", width, line)
		}
	}

	model.moveTab(1)
	compact := model.View()
	compactLines := strings.Split(compact, "\n")
	if len(compactLines) != 18 {
		t.Fatalf("18-row view should pin its footer on row 18; got %d lines:\n%s", len(compactLines), compact)
	}
	if last := compactLines[len(compactLines)-1]; !strings.Contains(last, "Enter/q") {
		t.Fatalf("footer is not pinned to the last row: %q\n%s", last, compact)
	}
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 180, Height: 50})
	model = updated.(checkTUIModel)
	if model.width != 180 || model.height != 50 {
		t.Fatalf("resize was not applied: width=%d height=%d", model.width, model.height)
	}
	wideLines := strings.Split(model.View(), "\n")
	if len(wideLines) != 50 || !strings.Contains(wideLines[len(wideLines)-1], "Enter/q") {
		t.Fatalf("resized footer is not pinned to row 50: lines=%d last=%q", len(wideLines), wideLines[len(wideLines)-1])
	}
	model.moveTab(-1)
	model.detailOpen = true
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 64, Height: 12})
	model = updated.(checkTUIModel)
	detailLines := strings.Split(model.View(), "\n")
	if len(detailLines) != 12 || !strings.Contains(detailLines[len(detailLines)-1], "Enter retest") || !strings.Contains(detailLines[len(detailLines)-1], "q exit") {
		t.Fatalf("small detail layout did not preserve footer row: lines=%d last=%q", len(detailLines), detailLines[len(detailLines)-1])
	}
}

func TestCheckTUIEnterRetestsSelectedCompletedPing(t *testing.T) {
	requested := ""
	model := newCheckTUIModel()
	model.finished = true
	model.retestItem = func(stage, id string) string {
		requested = stage + "/" + id
		return "retest accepted"
	}
	model.setStages([]string{checkTUIStagePing})
	model.setResults(checkTUIStagePing, []string{"STATUS"}, []checkTUIItem{{ID: "ping-0", Status: "FAIL", Cells: []string{"FAIL"}}})
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(checkTUIModel)
	if cmd != nil || requested != "Ping/ping-0" || model.notice != "retest accepted" {
		t.Fatalf("Enter did not request selected ping retest: requested=%q notice=%q cmd=%v", requested, model.notice, cmd)
	}
	for _, want := range []string{"Enter retest", "q exit"} {
		if view := model.View(); !strings.Contains(view, want) {
			t.Fatalf("completed ping footer missing %q:\n%s", want, view)
		}
	}
}

func TestCheckTUIEnterDefersRetestUntilCheckCompletes(t *testing.T) {
	called := false
	model := newCheckTUIModel()
	model.retestItem = func(stage, id string) string {
		called = true
		return "unexpected"
	}
	model.setStages([]string{checkTUIStageBandwidth})
	model.setResults(checkTUIStageBandwidth, []string{"STATUS"}, []checkTUIItem{{ID: "bandwidth-0", Status: "PASS", Cells: []string{"PASS"}}})
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(checkTUIModel)
	if cmd != nil || called || !strings.Contains(model.notice, "after the full check completes") {
		t.Fatalf("running check unexpectedly launched retest: called=%v notice=%q cmd=%v", called, model.notice, cmd)
	}
}

func TestCheckTUIRetestCanReplaceAbortedRow(t *testing.T) {
	model := newCheckTUIModel()
	model.setStages([]string{checkTUIStagePing})
	model.setResults(checkTUIStagePing, []string{"STATUS"}, []checkTUIItem{{ID: "ping-0", Status: "ABORT", Cells: []string{"ABORT"}}})
	model.updateItem(checkTUIItem{ID: "ping-0", Section: checkTUIStagePing, Status: "RUNNING", Cells: []string{"RUNNING"}, Retest: true})
	if got := model.currentTable().Items[0].Status; got != "RUNNING" {
		t.Fatalf("manual retest did not replace aborted row: %s", got)
	}
}

func TestCheckTUIControllerDeduplicatesConcurrentRetest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	controller := &checkTUIController{
		retestCtx: ctx, retestCancel: cancel,
		retestRuns: map[string]func(context.Context){}, retestActive: map[string]bool{},
	}
	started := make(chan struct{})
	release := make(chan struct{})
	controller.RegisterRetest(checkTUIStagePing, "ping-0", func(context.Context) {
		close(started)
		<-release
	})
	if notice := controller.requestRetest(checkTUIStagePing, "ping-0"); !strings.Contains(notice, "Retest started") {
		t.Fatalf("first retest request was rejected: %q", notice)
	}
	<-started
	if notice := controller.requestRetest(checkTUIStagePing, "ping-0"); !strings.Contains(notice, "already running") {
		t.Fatalf("duplicate retest request was not rejected: %q", notice)
	}
	close(release)
	controller.retestWG.Wait()
}

func TestCheckTUIQWhileRunningAbortsOnlySelectedItem(t *testing.T) {
	manager := newCheckAbortManager([]string{checkTUIStagePing})
	stageOpts, finishStage := manager.beginStage(Options{Context: context.Background()}, checkTUIStagePing)
	defer finishStage()
	itemOpts, finishItem := manager.beginItem(stageOpts, checkTUIStagePing, "ping-0")
	defer finishItem()
	model := newCheckTUIModel()
	model.abortItem = manager.abortItem
	model.abortStage = manager.abortStage
	model.width = 180
	model.height = 24
	model.setStages([]string{checkTUIStagePing})
	model.setResults(checkTUIStagePing, []string{"STATUS"}, []checkTUIItem{{ID: "ping-0", Section: checkTUIStagePing, Status: "RUNNING", Cells: []string{"RUNNING"}}})
	for _, want := range []string{"[/]: switch stage", "q: abort item", "Esc: abort stage (twice: all)"} {
		if view := model.View(); !strings.Contains(view, want) {
			t.Fatalf("running footer missing %q:\n%s", want, view)
		}
	}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd != nil {
		t.Fatal("q item abort must keep the TUI open")
	}
	updatedModel := updated.(checkTUIModel)
	if got := updatedModel.tables[checkTUIStagePing].Items[0].Status; got != "ABORTING" {
		t.Fatalf("selected item status = %q, want ABORTING", got)
	}
	if !errors.Is(checkCancellationError(itemOpts), errCheckItemAborted) {
		t.Fatalf("selected item context was not aborted: %v", checkCancellationError(itemOpts))
	}
	if err := checkCancellationError(stageOpts); err != nil {
		t.Fatalf("q item abort leaked into stage context: %v", err)
	}
}

func TestCheckAbortManagerCanAbortRetestAfterStageCompletes(t *testing.T) {
	manager := newCheckAbortManager([]string{checkTUIStagePing})
	baseOpts := Options{Context: context.Background()}
	_, finishStage := manager.beginStage(baseOpts, checkTUIStagePing)
	finishStage()
	retestOpts, finishRetest := manager.beginRetestItem(baseOpts, checkTUIStagePing, "ping-0")
	defer finishRetest()
	if !manager.abortItem(checkTUIStagePing, "ping-0") {
		t.Fatal("active retest could not be aborted after its stage completed")
	}
	if err := checkCancellationError(retestOpts); !errors.Is(err, errCheckItemAborted) {
		t.Fatalf("retest cancellation error = %v, want errCheckItemAborted", err)
	}
}

func TestCheckTUIEscapeAbortsStageAndSecondEscapeAbortsCheck(t *testing.T) {
	checkCtx, cancelCheck := context.WithCancel(context.Background())
	manager := newCheckAbortManager([]string{checkTUIStagePing, checkTUIStageBandwidth})
	stageOpts, finishStage := manager.beginStage(Options{Context: checkCtx}, checkTUIStagePing)
	defer finishStage()
	model := newCheckTUIModel()
	model.cancelCheck = cancelCheck
	model.abortItem = manager.abortItem
	model.abortStage = manager.abortStage
	model.setStages([]string{checkTUIStagePing, checkTUIStageBandwidth})
	model.setResults(checkTUIStagePing, []string{"STATUS"}, []checkTUIItem{{ID: "ping-0", Section: checkTUIStagePing, Status: "RUNNING", Cells: []string{"RUNNING"}}})

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(checkTUIModel)
	if !errors.Is(checkCancellationError(stageOpts), errCheckStageAborted) {
		t.Fatalf("Esc did not abort the current stage: %v", checkCancellationError(stageOpts))
	}
	if checkCtx.Err() != nil {
		t.Fatal("first Esc unexpectedly aborted the whole check")
	}
	if got := model.tables[checkTUIStagePing].Items[0].Status; got != "ABORT" {
		t.Fatalf("stage row status = %q, want ABORT", got)
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(checkTUIModel)
	if checkCtx.Err() == nil {
		t.Fatal("second consecutive Esc did not abort the whole check")
	}
	if !strings.Contains(model.notice, "Entire check abort requested") {
		t.Fatalf("missing whole-check abort notice: %q", model.notice)
	}
}

func TestCheckTUISingleStageEscapeNeedsOnlyOnePress(t *testing.T) {
	checkCtx, cancelCheck := context.WithCancel(context.Background())
	manager := newCheckAbortManager([]string{checkTUIStageXCCL})
	stageOpts, finishStage := manager.beginStage(Options{Context: checkCtx}, checkTUIStageXCCL)
	defer finishStage()
	model := newCheckTUIModel()
	model.cancelCheck = cancelCheck
	model.abortStage = manager.abortStage
	model.setStages([]string{checkTUIStageXCCL})
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(checkTUIModel)
	if !errors.Is(checkCancellationError(stageOpts), errCheckStageAborted) {
		t.Fatalf("single-stage Esc did not abort the stage: %v", checkCancellationError(stageOpts))
	}
	if checkCtx.Err() != nil {
		t.Fatal("single-stage Esc should let the runner finish cleanup instead of canceling the root context")
	}
	if !strings.Contains(model.notice, "only stage") {
		t.Fatalf("single-stage abort notice is unclear: %q", model.notice)
	}
}

func TestCheckTUIQOnXCCLAbortsSharedStage(t *testing.T) {
	manager := newCheckAbortManager([]string{checkTUIStageXCCL})
	stageOpts, finishStage := manager.beginStage(Options{Context: context.Background()}, checkTUIStageXCCL)
	defer finishStage()
	model := newCheckTUIModel()
	model.abortItem = manager.abortItem
	model.abortStage = manager.abortStage
	model.setStages([]string{checkTUIStageXCCL})
	model.setResults(checkTUIStageXCCL, xcclTUIHeaders(), []checkTUIItem{
		{ID: "xccl-1024-out", Section: checkTUIStageXCCL, Status: "RUNNING", Cells: []string{"RUNNING"}},
		{ID: "xccl-2048-out", Section: checkTUIStageXCCL, Status: "WAIT", Cells: []string{"WAIT"}},
	})
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	model = updated.(checkTUIModel)
	if !errors.Is(checkCancellationError(stageOpts), errCheckStageAborted) {
		t.Fatalf("q on XCCL did not abort the shared stage: %v", checkCancellationError(stageOpts))
	}
	for _, item := range model.tables[checkTUIStageXCCL].Items {
		if item.Status != "ABORT" {
			t.Fatalf("XCCL row %s status = %s, want ABORT", item.ID, item.Status)
		}
	}
	if !strings.Contains(model.notice, "shared mpirun") {
		t.Fatalf("XCCL q exception was not explained: %q", model.notice)
	}
}
