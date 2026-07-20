package checker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

func Run(opts Options) error {
	baseContext := opts.Context
	if baseContext == nil {
		baseContext = context.Background()
	}
	checkCtx, cancelCheck := context.WithCancel(baseContext)
	defer cancelCheck()
	opts.Context = checkCtx
	var tuiOutput *os.File
	if opts.LiveOutput && !opts.DryRun {
		tuiOutput, _ = opts.Output.(*os.File)
	}
	if opts.Output == nil {
		opts.Output = io.Discard
	}
	if _, ok := opts.Output.(*synchronizedWriter); !ok {
		opts.Output = &synchronizedWriter{writer: opts.Output}
	}
	runBandwidth := opts.RunBandwidth
	runRDMAPing := opts.RunRDMAPing
	runXCCL := opts.RunXCCL
	if !runBandwidth && !runRDMAPing && !runXCCL {
		runBandwidth = true
	}
	runRDMATraffic := runBandwidth || runXCCL
	var tuiStages []string
	if runRDMAPing {
		tuiStages = append(tuiStages, checkTUIStagePing)
	}
	if runBandwidth {
		for _, mode := range normalizedBandwidthModes(opts.BandwidthModes) {
			tuiStages = append(tuiStages, bandwidthStageForMode(mode))
		}
	}
	if runXCCL {
		tuiStages = append(tuiStages, checkTUIStageXCCL)
	}
	targets, err := ResolveTargets(opts.Records, opts.Hosts)
	if err != nil {
		return err
	}
	if len(targets) < 2 && (runBandwidth || runRDMAPing) {
		return errors.New("bandwidth and rdma-ping checks require at least two hosts; single-host mode is supported only for --check-stage xccl")
	}
	targets = markLocalTargets(targets)
	if !opts.DryRun {
		warnHostnameMismatches(opts, targets)
	}
	if runBandwidth && opts.Bundle.Check.Bandwidth.BandwidthQPs < 0 {
		return errors.New("bundle check.bandwidth.bandwidth_qps must not be negative")
	}
	if runRDMATraffic {
		for _, group := range opts.Bundle.Check.Bandwidth.RDMAGroups {
			if err := validateGroup(group); err != nil {
				return err
			}
		}
	}
	resolvedGroups := resolvedRDMAGroups{}
	if runRDMATraffic {
		var err error
		resolvedGroups, err = resolveBandwidthGroups(opts, targets)
		if err != nil {
			return err
		}
		if runBandwidth {
			resolvedGroups, err = resolveXDRTopologyGroups(opts, targets, resolvedGroups)
			if err != nil {
				return err
			}
		}
	}
	if tuiOutput != nil {
		opts.aborts = newCheckAbortManager(tuiStages)
		opts.checkTUI = startCheckTUI(os.Stdin, tuiOutput, tuiStages, checkCtx, cancelCheck, opts.aborts)
		opts.Output = opts.checkTUI
	}
	var failures []string
	var bandwidthResults []Result
	nicBefore, nicFailures := collectNICCounterSnapshots(opts, targets, "before")
	failures = append(failures, nicFailures...)
	canceled := checkCancellationError(opts) != nil
	if runRDMAPing && !canceled {
		opts.checkTUI.SetActiveStage(checkTUIStagePing)
		pingOpts := opts
		finishStage := func() {}
		if opts.aborts != nil {
			pingOpts, finishStage = opts.aborts.beginStage(opts, checkTUIStagePing)
		}
		failures = append(failures, runRDMAPingChecks(pingOpts, targets)...)
		finishStage()
		canceled = checkCancellationError(opts) != nil
	}
	if runBandwidth && !canceled {
		for _, mode := range normalizedBandwidthModes(opts.BandwidthModes) {
			modeOpts := opts
			modeOpts.bandwidthMode = mode
			modeOpts.bandwidthStage = bandwidthStageForMode(mode)
			var modeResults []Result
			var modeFailures []string
			modeResults, modeFailures = runBandwidthMode(modeOpts, targets, resolvedGroups)
			bandwidthResults = append(bandwidthResults, modeResults...)
			failures = append(failures, modeFailures...)
			canceled = checkCancellationError(opts) != nil
			if canceled {
				break
			}
		}
	}
	if runXCCL && !canceled {
		opts.checkTUI.SetActiveStage(checkTUIStageXCCL)
		xcclOpts := opts
		xcclOpts.counterStage = checkTUIStageXCCL
		finishStage := func() {}
		if opts.aborts != nil {
			xcclOpts, finishStage = opts.aborts.beginStage(opts, checkTUIStageXCCL)
		}
		rdmaDeviceBefore, rdmaFailures := collectRDMADeviceCounterSnapshots(xcclOpts, targets, resolvedGroups, "before")
		failures = append(failures, rdmaFailures...)
		if err := runXCCLCheck(xcclOpts, targets, resolvedGroups); err != nil {
			failures = append(failures, err.Error())
			fmt.Fprintf(opts.Output, "FAIL xccl: %v\n", err)
		}
		if checkCancellationError(xcclOpts) == nil {
			rdmaDeviceAfter, afterFailures := collectRDMADeviceCounterSnapshots(xcclOpts, targets, resolvedGroups, "after")
			failures = append(failures, afterFailures...)
			failures = append(failures, compareRDMADeviceCounterSnapshots(xcclOpts, targets, resolvedGroups, rdmaDeviceBefore, rdmaDeviceAfter)...)
		}
		finishStage()
		canceled = checkCancellationError(opts) != nil
	}
	if runBandwidth && !canceled && (opts.checkTUI == nil || opts.DryRun) {
		printBandwidthResultTable(opts.Output, bandwidthResults)
	}
	if runBandwidth && opts.checkTUI == nil {
		printBandwidthFailureDetails(opts.Output, bandwidthResults)
	}
	if !canceled {
		nicAfter, nicFailures := collectNICCounterSnapshots(opts, targets, "after")
		failures = append(failures, nicFailures...)
		failures = append(failures, compareNICCounterSnapshots(opts, targets, nicBefore, nicAfter)...)
		canceled = checkCancellationError(opts) != nil
	}
	failures = append(failures, opts.aborts.failures()...)
	var finalErr error
	if canceled {
		finalErr = checkCancellationError(opts)
	} else if len(failures) > 0 {
		finalErr = fmt.Errorf("check failed: %s", strings.Join(failures, "; "))
	}
	if opts.checkTUI != nil {
		opts.checkTUI.Finish(finalErr)
	}
	return finalErr
}

func runBandwidthMode(opts Options, targets []Target, resolvedGroups resolvedRDMAGroups) ([]Result, []string) {
	stage := opts.currentBandwidthStage()
	opts.counterStage = stage
	opts.checkTUI.SetActiveStage(stage)
	bandwidthOpts := opts
	finishStage := func() {}
	if opts.aborts != nil {
		bandwidthOpts, finishStage = opts.aborts.beginStage(opts, stage)
	}
	defer finishStage()
	rdmaBefore, counterFailures := collectRDMADeviceCounterSnapshots(bandwidthOpts, targets, resolvedGroups, "before")
	if bandwidthOpts.Bundle.Check.Bandwidth.MinGBitsMode() == "auto" && !bandwidthOpts.DryRun {
		bandwidthOpts.bandwidthSpeeds = collectBandwidthNICSpeeds(bandwidthOpts, targets)
	}
	if bandwidthOpts.LiveOutput && !bandwidthOpts.DryRun {
		bandwidthOpts.bandwidthLive = newBandwidthLiveTracker(bandwidthOpts, targets, resolvedGroups)
		registerBandwidthRetests(bandwidthOpts, targets, resolvedGroups)
	}
	var results []Result
	failures := append([]string(nil), counterFailures...)
bandwidthLoop:
	for i := 0; i < len(targets); i++ {
		for j := i + 1; j < len(targets); j++ {
			for _, pair := range [][2]Target{{targets[i], targets[j]}, {targets[j], targets[i]}} {
				if checkCancellationError(bandwidthOpts) != nil {
					break bandwidthLoop
				}
				if bandwidthOpts.Bundle.Check.Bandwidth.Parallel {
					batchResults, errs := runParallel(bandwidthOpts, resolvedGroups, pair[0], pair[1])
					for _, err := range errs {
						failures = append(failures, err.Error())
					}
					for _, result := range batchResults {
						results = append(results, result)
						failures = appendBandwidthResultFailure(bandwidthOpts, failures, result)
					}
					continue
				}
				for _, stream := range bandwidthStreamsForTargets(bandwidthOpts, pair[0], pair[1], resolvedGroups) {
					if checkCancellationError(bandwidthOpts) != nil {
						break bandwidthLoop
					}
					itemID := bandwidthStreamTUIIDForStage(stage, pair[0], pair[1], stream)
					itemOpts, finishItem := beginCheckItem(bandwidthOpts, stage, itemID)
					bandwidthOpts.bandwidthLive.MarkRunning(pair[0], pair[1], []checkStream{stream})
					result, err := runStream(itemOpts, pair[0], pair[1], stream)
					finishItem()
					results = append(results, result)
					if err != nil {
						failures = append(failures, err.Error())
						bandwidthOpts.bandwidthLive.CompleteWithError(result, err)
						continue
					}
					bandwidthOpts.bandwidthLive.Complete(result)
					failures = appendBandwidthResultFailure(bandwidthOpts, failures, result)
				}
			}
		}
	}
	if checkCancellationError(bandwidthOpts) == nil {
		rdmaAfter, afterFailures := collectRDMADeviceCounterSnapshots(bandwidthOpts, targets, resolvedGroups, "after")
		failures = append(failures, afterFailures...)
		failures = append(failures, compareRDMADeviceCounterSnapshots(bandwidthOpts, targets, resolvedGroups, rdmaBefore, rdmaAfter)...)
	}
	return results, failures
}

func appendBandwidthResultFailure(opts Options, failures []string, result Result) []string {
	if opts.DryRun {
		return failures
	}
	if result.ClientError != "" || result.ServerError != "" {
		return failures
	}
	label := resultLabel(result)
	if !result.Passed {
		if result.ThresholdError != "" {
			failures = append(failures, fmt.Sprintf("%s -> %s %s %s", result.Client.Name, result.Server.Name, label, result.ThresholdError))
		} else if result.ThresholdKnown {
			failures = append(failures, fmt.Sprintf("%s -> %s %s %.2f Gbps below %.2f Gbps (%s threshold)", result.Client.Name, result.Server.Name, label, result.GBits, result.ThresholdGBits, result.ThresholdMode))
		}
	}
	return failures
}
