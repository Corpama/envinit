package checker

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

func Run(opts Options) error {
	if opts.Output == nil {
		opts.Output = io.Discard
	}
	runBandwidth := opts.RunBandwidth
	runRDMAPing := opts.RunRDMAPing
	runXCCL := opts.RunXCCL
	if !runBandwidth && !runRDMAPing && !runXCCL {
		runBandwidth = true
	}
	runRDMATraffic := runBandwidth || runXCCL
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

	var failures []string
	var bandwidthResults []Result
	nicBefore, nicFailures := collectNICCounterSnapshots(opts, targets, "before")
	failures = append(failures, nicFailures...)
	if runRDMAPing {
		failures = append(failures, runRDMAPingChecks(opts, targets)...)
	}
	var rdmaDeviceBefore map[string]rdmaDeviceCounterSnapshot
	if runRDMATraffic {
		var rdmaFailures []string
		rdmaDeviceBefore, rdmaFailures = collectRDMADeviceCounterSnapshots(opts, targets, resolvedGroups, "before")
		failures = append(failures, rdmaFailures...)
	}
	if runBandwidth {
		for i := 0; i < len(targets); i++ {
			for j := i + 1; j < len(targets); j++ {
				for _, pair := range [][2]Target{{targets[i], targets[j]}, {targets[j], targets[i]}} {
					if opts.Bundle.Check.Bandwidth.Parallel {
						results, errs := runParallel(opts, resolvedGroups, pair[0], pair[1])
						for _, err := range errs {
							failures = append(failures, err.Error())
							fmt.Fprintf(opts.Output, "FAIL %s -> %s: %v\n", pair[1].Name, pair[0].Name, err)
						}
						for _, result := range results {
							bandwidthResults = append(bandwidthResults, result)
							failures = appendBandwidthResultFailure(opts, failures, result)
						}
						continue
					}

					for _, stream := range bandwidthStreamsForGroups(opts.Bundle.Check.Bandwidth, resolvedGroups[pair[0].Name], resolvedGroups[pair[1].Name]) {
						result, err := runStream(opts, pair[0], pair[1], stream)
						if err != nil {
							failures = append(failures, err.Error())
							fmt.Fprintf(opts.Output, "FAIL %s -> %s %s: %v\n", pair[1].Name, pair[0].Name, streamLabel(stream), err)
							continue
						}
						bandwidthResults = append(bandwidthResults, result)
						failures = appendBandwidthResultFailure(opts, failures, result)
					}
				}
			}
		}
	}
	if runXCCL {
		if err := runXCCLCheck(opts, targets, resolvedGroups); err != nil {
			failures = append(failures, err.Error())
			fmt.Fprintf(opts.Output, "FAIL xccl: %v\n", err)
		}
	}
	if runBandwidth {
		printBandwidthResultTable(opts.Output, bandwidthResults)
	}
	if runRDMATraffic {
		rdmaDeviceAfter, rdmaFailures := collectRDMADeviceCounterSnapshots(opts, targets, resolvedGroups, "after")
		failures = append(failures, rdmaFailures...)
		failures = append(failures, compareRDMADeviceCounterSnapshots(opts, targets, resolvedGroups, rdmaDeviceBefore, rdmaDeviceAfter)...)
	}
	nicAfter, nicFailures := collectNICCounterSnapshots(opts, targets, "after")
	failures = append(failures, nicFailures...)
	failures = append(failures, compareNICCounterSnapshots(opts, targets, nicBefore, nicAfter)...)
	if len(failures) > 0 {
		return fmt.Errorf("check failed: %s", strings.Join(failures, "; "))
	}
	return nil
}

func appendBandwidthResultFailure(opts Options, failures []string, result Result) []string {
	label := resultLabel(result)
	if !result.Passed {
		failures = append(failures, fmt.Sprintf("%s -> %s %s %.2f Gbps below %.2f Gbps", result.Client.Name, result.Server.Name, label, result.GBits, opts.Bundle.Check.Bandwidth.MinGBits))
	}
	return failures
}
