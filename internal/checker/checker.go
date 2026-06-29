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
	if !runBandwidth && !runRDMAPing {
		runBandwidth = true
	}
	targets, err := ResolveTargets(opts.Records, opts.Hosts)
	if err != nil {
		return err
	}
	if len(targets) < 2 {
		return errors.New("check requires at least two hosts")
	}
	targets = markLocalTargets(targets)
	if !opts.DryRun {
		warnHostnameMismatches(opts, targets)
	}
	if runBandwidth && len(opts.Bundle.Check.RDMAGroups) == 0 {
		return errors.New("bundle check.rdma_groups is required")
	}
	if runBandwidth && opts.Bundle.Check.BandwidthQPs < 0 {
		return errors.New("bundle check.bandwidth_qps must not be negative")
	}
	if runBandwidth {
		for _, group := range opts.Bundle.Check.RDMAGroups {
			if err := validateGroup(opts.Bundle.Check, group); err != nil {
				return err
			}
		}
	}
	resolvedGroups := resolvedRDMAGroups{}
	if runBandwidth {
		var err error
		resolvedGroups, err = resolveBandwidthGroups(opts, targets)
		if err != nil {
			return err
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
	if runBandwidth {
		var rdmaFailures []string
		rdmaDeviceBefore, rdmaFailures = collectRDMADeviceCounterSnapshots(opts, targets, resolvedGroups, "before")
		failures = append(failures, rdmaFailures...)
	}
	if runBandwidth {
		for i := 0; i < len(targets); i++ {
			for j := i + 1; j < len(targets); j++ {
				for _, pair := range [][2]Target{{targets[i], targets[j]}, {targets[j], targets[i]}} {
					if opts.Bundle.Check.Parallel {
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

					for _, stream := range bandwidthStreams(opts.Bundle.Check) {
						stream = resolveStreamGroups(resolvedGroups, pair[0], pair[1], stream)
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
	if runBandwidth {
		printBandwidthResultTable(opts.Output, bandwidthResults)
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
		failures = append(failures, fmt.Sprintf("%s -> %s %s %.2f Gbps below %.2f Gbps", result.Client.Name, result.Server.Name, label, result.GBits, opts.Bundle.Check.MinGBits))
	}
	return failures
}
