package checker

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"

	"envinit/internal/spec"
)

type bandwidthLiveTracker struct {
	mu      sync.Mutex
	table   *liveResultTable
	rowByID map[string]int
	cfg     spec.CheckBandwidthConfig
	stage   string
	mode    string
}

func newBandwidthLiveTracker(opts Options, targets []Target, groups resolvedRDMAGroups) *bandwidthLiveTracker {
	tracker := &bandwidthLiveTracker{rowByID: map[string]int{}, cfg: opts.Bundle.Check.Bandwidth, stage: opts.currentBandwidthStage(), mode: opts.currentBandwidthMode()}
	var rows [][]string
	var tuiItems []checkTUIItem
	for i := 0; i < len(targets); i++ {
		for j := i + 1; j < len(targets); j++ {
			for _, pair := range [][2]Target{{targets[i], targets[j]}, {targets[j], targets[i]}} {
				for _, stream := range bandwidthStreamsForTargets(opts, pair[0], pair[1], groups) {
					result := pendingBandwidthResultForConfig(opts.Bundle.Check.Bandwidth, pair[0], pair[1], stream)
					tracker.rowByID[bandwidthResultID(result)] = len(rows)
					rows = append(rows, bandwidthResultCells(result, "WAIT", true))
					tuiItems = append(tuiItems, bandwidthTUIItemForStage(result, "WAIT", tracker.stage))
				}
			}
		}
	}
	tracker.table = newLiveResultTable(opts.Output, opts.LiveOutput && !opts.DryRun && opts.checkTUI == nil, "Bandwidth live results:", bandwidthResultHeaders(), rows)
	if opts.checkTUI != nil {
		for index := range tuiItems {
			tuiItems[index].Section = tracker.stage
		}
		opts.checkTUI.SetResults(tracker.stage, bandwidthResultHeaders(), tuiItems)
	}
	return tracker
}

func (t *bandwidthLiveTracker) MarkRunning(server, client Target, streams []checkStream) {
	if t == nil {
		return
	}
	updates := make(map[int][]string, len(streams))
	for _, stream := range streams {
		result := pendingBandwidthResultForConfig(t.cfg, server, client, stream)
		t.mu.Lock()
		index, ok := t.rowByID[bandwidthResultID(result)]
		t.mu.Unlock()
		if ok {
			updates[index] = bandwidthResultCells(result, "RUNNING", true)
		}
	}
	t.table.UpdateRows(updates)
	for _, stream := range streams {
		result := pendingBandwidthResultForConfig(t.cfg, server, client, stream)
		if controller, ok := t.table.output.(*checkTUIController); ok {
			controller.Update(bandwidthTUIItemForStage(result, "RUNNING", t.stage))
		}
	}
}

func (t *bandwidthLiveTracker) Complete(result Result) {
	t.CompleteWithError(result, nil)
}

func (t *bandwidthLiveTracker) CompleteWithError(result Result, err error) {
	if t == nil {
		return
	}
	status := "PASS"
	if errors.Is(err, errCheckItemAborted) || errors.Is(err, errCheckStageAborted) {
		status = "ABORT"
	} else if !result.Passed || result.ClientError != "" || result.ServerError != "" {
		status = "FAIL"
	} else if result.Degraded {
		status = "WARN"
	}
	t.update(result, status)
}

func (t *bandwidthLiveTracker) update(result Result, status string) {
	t.mu.Lock()
	index, ok := t.rowByID[bandwidthResultID(result)]
	t.mu.Unlock()
	if ok {
		t.table.Update(index, bandwidthResultCells(result, status, true))
		if controller, ok := t.table.output.(*checkTUIController); ok {
			controller.Update(bandwidthTUIItemForStage(result, status, t.stage))
			if status != "RUNNING" && status != "WAIT" {
				controller.AppendLog(t.stage, strings.ToUpper(t.mode)+" "+bandwidthRawLog(result, status))
			}
		}
	}
}

func pendingBandwidthResult(server, client Target, stream checkStream) Result {
	result := Result{
		Server:          server,
		Client:          client,
		ServerGroup:     stream.ServerGroup,
		ClientGroup:     stream.ClientGroup,
		ServerRDMAIndex: stream.ServerRDMAIndex,
		ClientRDMAIndex: stream.ClientRDMAIndex,
		ServerXP:        stream.ServerOffset,
		ClientXP:        stream.ClientOffset,
		ServerTopology:  groupTopologyLink(stream.ServerGroup, stream.ServerOffset),
		ClientTopology:  groupTopologyLink(stream.ClientGroup, stream.ClientOffset),
		Port:            stream.Port,
		GBits:           math.NaN(),
	}
	result.Degraded = topologyLinkDegraded(result.ServerTopology) || topologyLinkDegraded(result.ClientTopology)
	result.ClientMaxMbps = stream.ClientSpeed.MaximumMbps
	result.ServerMaxMbps = stream.ServerSpeed.MaximumMbps
	result.ClientNowMbps = stream.ClientSpeed.CurrentMbps
	result.ServerNowMbps = stream.ServerSpeed.CurrentMbps
	result.ClientSpeedError = stream.ClientSpeed.Error
	result.ServerSpeedError = stream.ServerSpeed.Error
	return result
}

func pendingBandwidthResultForConfig(cfg spec.CheckBandwidthConfig, server, client Target, stream checkStream) Result {
	result := pendingBandwidthResult(server, client, stream)
	applyBandwidthThreshold(cfg, &result, false)
	return result
}

func bandwidthResultID(result Result) string {
	return result.Client.Name + "\x00" + result.Server.Name + "\x00" +
		rdmaLabel(result.ClientRDMAIndex) + "\x00" + rdmaLabel(result.ServerRDMAIndex) + "\x00" +
		result.ClientXP + "\x00" + result.ServerXP + "\x00" + strconv.Itoa(result.Port)
}

func bandwidthStreamTUIID(server, client Target, stream checkStream) string {
	return "bandwidth-" + bandwidthResultID(pendingBandwidthResult(server, client, stream))
}

func bandwidthStreamTUIIDForStage(stage string, server, client Target, stream checkStream) string {
	return stage + "\x00" + bandwidthStreamTUIID(server, client, stream)
}

func bandwidthTUIItem(result Result, status string) checkTUIItem {
	bandwidth := "-"
	if !math.IsNaN(result.GBits) {
		bandwidth = fmt.Sprintf("%.2f Gbps", result.GBits)
	}
	if status == "FAIL" && (result.ClientError != "" || result.ServerError != "") {
		bandwidth = "error"
	}
	detail := fmt.Sprintf("Client: %s\nClient NIC: %s\nClient IP: %s\nClient IB device: %s\nClient XPU: %s\nClient topology: %s\nClient maximum/current speed: %s / %s\nClient speed probe: %s\n\nServer: %s\nServer NIC: %s\nServer IP: %s\nServer IB device: %s\nServer XPU: %s\nServer topology: %s\nServer maximum/current speed: %s / %s\nServer speed probe: %s\n\nPort: %d\nStatus: %s\nBandwidth: %s\nThreshold mode: %s\nBaseline: %s\nPass threshold: %s\nUtilization: %s\nThreshold error: %s\n\nCLIENT ERROR:\n%s\n\nCLIENT OUTPUT:\n%s\n\nSERVER ERROR:\n%s\n\nSERVER OUTPUT:\n%s",
		result.Client.Name, rdmaNICLabel(result.Client, result.ClientRDMAIndex), rdmaIPLabel(result.Client, result.ClientRDMAIndex), result.ClientGroup.IBDevice, firstNonEmpty(result.ClientXP, "-"), topologyResultLabel(result.ClientTopology),
		bandwidthSpeedLabel(result.ClientMaxMbps), bandwidthSpeedLabel(result.ClientNowMbps), firstNonEmpty(result.ClientSpeedError, "ok"),
		result.Server.Name, rdmaNICLabel(result.Server, result.ServerRDMAIndex), bandwidthPeerAddress(result.Server, checkStream{ServerRDMAIndex: result.ServerRDMAIndex}), result.ServerGroup.IBDevice, firstNonEmpty(result.ServerXP, "-"), topologyResultLabel(result.ServerTopology),
		bandwidthSpeedLabel(result.ServerMaxMbps), bandwidthSpeedLabel(result.ServerNowMbps), firstNonEmpty(result.ServerSpeedError, "ok"),
		result.Port, status, bandwidth,
		result.ThresholdMode, formatOptionalGBits(result.BaselineGBits, result.BaselineGBits > 0), formatOptionalGBits(result.ThresholdGBits, result.ThresholdKnown), bandwidthUtilizationLabel(result), firstNonEmpty(result.ThresholdError, "-"),
		firstNonEmpty(strings.TrimSpace(result.ClientError), "-"), firstNonEmpty(strings.TrimSpace(result.ClientOutput), "-"),
		firstNonEmpty(strings.TrimSpace(result.ServerError), "-"), firstNonEmpty(strings.TrimSpace(result.ServerOutput), "-"))
	return checkTUIItem{
		ID: "bandwidth-" + bandwidthResultID(result), Section: checkTUIStageBandwidth, Status: status, Cells: bandwidthResultCells(result, status, true), Detail: detail,
		Heatmap: &checkTUIHeatmapPoint{
			Direction:     result.Client.Name + " -> " + result.Server.Name,
			ClientAxis:    bandwidthHeatmapAxis(rdmaNICLabel(result.Client, result.ClientRDMAIndex), result.ClientXP),
			ServerAxis:    bandwidthHeatmapAxis(rdmaNICLabel(result.Server, result.ServerRDMAIndex), result.ServerXP),
			MeasuredGBits: result.GBits, BaselineGBits: result.BaselineGBits, ThresholdGBits: result.ThresholdGBits,
			ThresholdMode: result.ThresholdMode, ThresholdKnown: result.ThresholdKnown, Status: status,
			Ready: status == "PASS" || status == "FAIL" || status == "WARN",
		},
	}
}

func bandwidthTUIItemForStage(result Result, status, stage string) checkTUIItem {
	item := bandwidthTUIItem(result, status)
	item.ID = stage + "\x00" + item.ID
	item.Section = stage
	return item
}

func bandwidthHeatmapAxis(nic, xpu string) string {
	if strings.TrimSpace(xpu) == "" {
		return nic
	}
	return nic + "/" + xpu
}

func formatOptionalGBits(value float64, available bool) string {
	if !available {
		return "-"
	}
	return fmt.Sprintf("%.2f Gbps", value)
}

func bandwidthUtilizationLabel(result Result) string {
	if result.BaselineGBits <= 0 || math.IsNaN(result.GBits) {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", result.GBits/result.BaselineGBits*100)
}

func bandwidthRawLog(result Result, status string) string {
	return fmt.Sprintf("%s bandwidth %s/%s(%s) -> %s/%s(%s) port=%d mode=%s baseline=%s threshold=%s utilization=%s threshold_error=%s\nCLIENT %s ERROR:\n%s\nCLIENT %s OUTPUT:\n%s\nSERVER %s ERROR:\n%s\nSERVER %s OUTPUT:\n%s",
		status,
		result.Client.Name, rdmaNICLabel(result.Client, result.ClientRDMAIndex), rdmaIPLabel(result.Client, result.ClientRDMAIndex),
		result.Server.Name, rdmaNICLabel(result.Server, result.ServerRDMAIndex), bandwidthPeerAddress(result.Server, checkStream{ServerRDMAIndex: result.ServerRDMAIndex}), result.Port,
		result.ThresholdMode, formatOptionalGBits(result.BaselineGBits, result.BaselineGBits > 0), formatOptionalGBits(result.ThresholdGBits, result.ThresholdKnown), bandwidthUtilizationLabel(result), firstNonEmpty(result.ThresholdError, "-"),
		result.Client.Name, firstNonEmpty(strings.TrimSpace(result.ClientError), "-"),
		result.Client.Name, firstNonEmpty(strings.TrimSpace(result.ClientOutput), "-"),
		result.Server.Name, firstNonEmpty(strings.TrimSpace(result.ServerError), "-"),
		result.Server.Name, firstNonEmpty(strings.TrimSpace(result.ServerOutput), "-"))
}

func registerBandwidthRetests(opts Options, targets []Target, groups resolvedRDMAGroups) {
	if opts.checkTUI == nil {
		return
	}
	for i := 0; i < len(targets); i++ {
		for j := i + 1; j < len(targets); j++ {
			for _, pair := range [][2]Target{{targets[i], targets[j]}, {targets[j], targets[i]}} {
				server := pair[0]
				client := pair[1]
				for _, plannedStream := range bandwidthStreamsForTargets(opts, server, client, groups) {
					stream := plannedStream
					itemID := bandwidthStreamTUIIDForStage(opts.currentBandwidthStage(), server, client, stream)
					stage := opts.currentBandwidthStage()
					opts.checkTUI.RegisterRetest(stage, itemID, func(ctx context.Context) {
						retestOpts := opts
						retestOpts.Context = ctx
						itemOpts, finishItem := beginRetestCheckItem(retestOpts, stage, itemID)

						runningResult := pendingBandwidthResultForConfig(retestOpts.Bundle.Check.Bandwidth, server, client, stream)
						runningItem := bandwidthTUIItemForStage(runningResult, "RUNNING", stage)
						runningItem.Retest = true
						runningItem.Detail += "\n\nManual retest is running; the original check verdict and counter summary are preserved."
						retestOpts.checkTUI.Update(runningItem)

						result, err := runStream(itemOpts, server, client, stream)
						finishItem()
						status := bandwidthResultStatus(result)
						if errors.Is(err, errCheckItemAborted) || errors.Is(err, errCheckStageAborted) || errors.Is(err, errCheckCanceled) {
							status = "ABORT"
						}
						resultItem := bandwidthTUIItemForStage(result, status, stage)
						resultItem.Retest = true
						resultItem.Detail += "\n\nThis is the latest manual retest result; the original check verdict and counter summary are preserved."
						retestOpts.checkTUI.Update(resultItem)
						retestOpts.checkTUI.AppendLog(stage, "RETEST "+strings.ToUpper(retestOpts.currentBandwidthMode())+" "+bandwidthRawLog(result, status))
					})
				}
			}
		}
	}
}
