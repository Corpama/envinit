package checker

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"envinit/internal/spec"
)

func validateGroup(group spec.CheckRDMAGroup) error {
	if strings.TrimSpace(group.IBDevice) == "" {
		return fmt.Errorf("check.bandwidth.rdma_groups[].ib_device is required")
	}
	return nil
}

func runParallel(opts Options, groupsByTarget resolvedRDMAGroups, server Target, client Target) ([]Result, []error) {
	streams := bandwidthStreamsForTargets(opts, server, client, groupsByTarget)
	batches := bandwidthStreamBatchesFromStreams(streams)
	if opts.DryRun {
		dryRunConfig := opts.Bundle.Check.Bandwidth
		dryRunConfig.MinGBitsAuto = false
		dryRunConfig.MinGBits = 0
		results := make([]Result, 0)
		for batchIndex, batch := range batches {
			fmt.Fprintf(opts.Output, "dry-run bandwidth batch %d %s -> %s: %d stream(s)\n", batchIndex+1, client.Name, server.Name, len(batch))
			for _, stream := range batch {
				serverArgs := ibWriteBWArgsForMode(opts.Bundle.Check.Bandwidth, stream.ServerGroup, stream.ServerOffset, "", stream.Port, opts.currentBandwidthMode())
				clientArgs := ibWriteBWArgsForMode(opts.Bundle.Check.Bandwidth, stream.ClientGroup, stream.ClientOffset, bandwidthAddressForMode(opts, server, stream), stream.Port, opts.currentBandwidthMode())
				fmt.Fprintf(opts.Output, "dry-run server %s: %s\n", server.Name, shellJoin(serverArgs))
				fmt.Fprintf(opts.Output, "dry-run client %s: %s\n", client.Name, shellJoin(clientArgs))
				results = append(results, resultFromOutput(dryRunConfig, server, client, stream, ""))
			}
		}
		return results, nil
	}

	var results []Result
	var errs []error
	for _, batch := range batches {
		if checkCancellationError(opts) != nil {
			break
		}
		batchResults, batchErrs := runParallelBatch(opts, server, client, batch)
		results = append(results, batchResults...)
		errs = append(errs, batchErrs...)
	}
	return results, errs
}

func resolveStreamGroups(groupsByTarget resolvedRDMAGroups, server Target, client Target, stream checkStream) checkStream {
	if groups := groupsByTarget[server.Name]; stream.ServerRDMAIndex >= 0 && stream.ServerRDMAIndex < len(groups) {
		stream.ServerGroup = groups[stream.ServerRDMAIndex]
	}
	if groups := groupsByTarget[client.Name]; stream.ClientRDMAIndex >= 0 && stream.ClientRDMAIndex < len(groups) {
		stream.ClientGroup = groups[stream.ClientRDMAIndex]
	}
	return stream
}

func runParallelBatch(opts Options, server Target, client Target, streams []checkStream) ([]Result, []error) {
	type serverProcess struct {
		stream checkStream
		pid    string
		log    string
		opts   Options
		finish func()
	}
	processes := make([]serverProcess, 0, len(streams))
	results := make([]Result, 0, len(streams))
	var errs []error
	for _, stream := range streams {
		itemOpts, finishItem := beginCheckItem(opts, opts.currentBandwidthStage(), bandwidthStreamTUIIDForStage(opts.currentBandwidthStage(), server, client, stream))
		serverArgs := ibWriteBWArgsForMode(opts.Bundle.Check.Bandwidth, stream.ServerGroup, stream.ServerOffset, "", stream.Port, opts.currentBandwidthMode())
		logPath := fmt.Sprintf("/tmp/envinit-check-%s-%d-%d.log", sanitizeName(stream.ServerGroup.IBDevice), stream.Port, time.Now().UnixNano())
		serverCmd := bandwidthServerStartCommand(serverArgs, logPath)
		pid, err := runCheckCommand(itemOpts, server, serverCmd)
		if err != nil {
			serverOutput, cleanupErr := collectBandwidthServerOutput(itemOpts, server, "", logPath)
			streamErr := fmt.Errorf("start server on %s %s: %w", server.Name, streamLabel(stream), err)
			serverError := streamErr.Error()
			if cleanupErr != nil {
				serverError += "; cleanup: " + cleanupErr.Error()
			}
			result := failedBandwidthResult(opts.Bundle.Check.Bandwidth, server, client, stream, "", serverOutput, "", serverError)
			results = append(results, result)
			errs = append(errs, streamErr)
			opts.bandwidthLive.CompleteWithError(result, err)
			finishItem()
			continue
		}
		processes = append(processes, serverProcess{stream: stream, pid: strings.TrimSpace(pid), log: logPath, opts: itemOpts, finish: finishItem})
	}
	if len(processes) == 0 {
		return results, errs
	}
	runningStreams := make([]checkStream, 0, len(processes))
	for _, process := range processes {
		runningStreams = append(runningStreams, process.stream)
	}
	opts.bandwidthLive.MarkRunning(server, client, runningStreams)
	if err := waitContext(checkContext(opts), 800*time.Millisecond); err != nil {
		for _, process := range processes {
			serverOutput, _ := collectBandwidthServerOutput(process.opts, server, process.pid, process.log)
			result := failedBandwidthResult(opts.Bundle.Check.Bandwidth, server, client, process.stream, "", serverOutput, err.Error(), "")
			results = append(results, result)
			opts.bandwidthLive.CompleteWithError(result, checkCancellationError(opts))
			process.finish()
		}
		return results, append(errs, checkCancellationError(opts))
	}

	type streamResult struct {
		index  int
		result Result
		err    error
	}
	ch := make(chan streamResult, len(processes))
	var wg sync.WaitGroup
	for index, process := range processes {
		index := index
		process := process
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer process.finish()
			clientArgs := ibWriteBWArgsForMode(opts.Bundle.Check.Bandwidth, process.stream.ClientGroup, process.stream.ClientOffset, bandwidthAddressForMode(opts, server, process.stream), process.stream.Port, opts.currentBandwidthMode())
			clientOutput, clientErr := runCheckCommand(process.opts, client, shellJoin(clientArgs))
			serverOutput, serverErr := collectBandwidthServerOutput(process.opts, server, process.pid, process.log)
			result := resultFromOutput(opts.Bundle.Check.Bandwidth, server, client, process.stream, clientOutput)
			result.ServerOutput = serverOutput
			var runErr error
			if clientErr != nil {
				result.ClientError = clientErr.Error()
				runErr = fmt.Errorf("run client on %s against %s %s: %w", client.Name, server.Name, streamLabel(process.stream), clientErr)
			}
			if serverErr != nil {
				result.ServerError = serverErr.Error()
				runErr = errors.Join(runErr, fmt.Errorf("collect server output on %s %s: %w", server.Name, streamLabel(process.stream), serverErr))
			}
			if runErr != nil {
				result.Passed = false
			}
			ch <- streamResult{index: index, result: result, err: runErr}
		}()
	}
	go func() {
		wg.Wait()
		close(ch)
	}()

	ordered := make([]*Result, len(processes))
	for item := range ch {
		ordered[item.index] = &item.result
		opts.bandwidthLive.CompleteWithError(item.result, item.err)
		if item.err != nil {
			errs = append(errs, item.err)
			continue
		}
	}
	for _, result := range ordered {
		if result != nil {
			results = append(results, *result)
		}
	}
	return results, errs
}

func runStream(opts Options, server Target, client Target, stream checkStream) (Result, error) {
	serverArgs := ibWriteBWArgsForMode(opts.Bundle.Check.Bandwidth, stream.ServerGroup, stream.ServerOffset, "", stream.Port, opts.currentBandwidthMode())
	clientArgs := ibWriteBWArgsForMode(opts.Bundle.Check.Bandwidth, stream.ClientGroup, stream.ClientOffset, bandwidthAddressForMode(opts, server, stream), stream.Port, opts.currentBandwidthMode())

	if opts.DryRun {
		fmt.Fprintf(opts.Output, "dry-run server %s: %s\n", server.Name, shellJoin(serverArgs))
		fmt.Fprintf(opts.Output, "dry-run client %s: %s\n", client.Name, shellJoin(clientArgs))
		dryRunConfig := opts.Bundle.Check.Bandwidth
		dryRunConfig.MinGBitsAuto = false
		dryRunConfig.MinGBits = 0
		return resultFromOutput(dryRunConfig, server, client, stream, ""), nil
	}

	logPath := fmt.Sprintf("/tmp/envinit-check-%s-%d-%d.log", sanitizeName(stream.ServerGroup.IBDevice), stream.Port, time.Now().UnixNano())
	serverCmd := bandwidthServerStartCommand(serverArgs, logPath)
	pid, err := runCheckCommand(opts, server, serverCmd)
	if err != nil {
		serverOutput, cleanupErr := collectBandwidthServerOutput(opts, server, "", logPath)
		streamErr := fmt.Errorf("start server on %s: %w", server.Name, err)
		serverError := streamErr.Error()
		if cleanupErr != nil {
			serverError += "; cleanup: " + cleanupErr.Error()
		}
		return failedBandwidthResult(opts.Bundle.Check.Bandwidth, server, client, stream, "", serverOutput, "", serverError), streamErr
	}
	pid = strings.TrimSpace(pid)
	if err := waitContext(checkContext(opts), 800*time.Millisecond); err != nil {
		serverOutput, serverErr := collectBandwidthServerOutput(opts, server, pid, logPath)
		cancelErr := checkCancellationError(opts)
		serverError := ""
		if serverErr != nil {
			serverError = serverErr.Error()
		}
		return failedBandwidthResult(opts.Bundle.Check.Bandwidth, server, client, stream, "", serverOutput, cancelErr.Error(), serverError), cancelErr
	}

	clientOutput, clientErr := runCheckCommand(opts, client, shellJoin(clientArgs))
	serverOutput, serverErr := collectBandwidthServerOutput(opts, server, pid, logPath)
	result := resultFromOutput(opts.Bundle.Check.Bandwidth, server, client, stream, clientOutput)
	result.ServerOutput = serverOutput
	var runErr error
	if clientErr != nil {
		result.ClientError = clientErr.Error()
		runErr = fmt.Errorf("run client on %s against %s: %w", client.Name, server.Name, clientErr)
	}
	if serverErr != nil {
		result.ServerError = serverErr.Error()
		runErr = errors.Join(runErr, fmt.Errorf("collect server output on %s: %w", server.Name, serverErr))
	}
	if runErr != nil {
		result.Passed = false
	}
	return result, runErr
}

func collectBandwidthServerOutput(opts Options, server Target, pid, logPath string) (string, error) {
	pidPath := logPath + ".pid"
	command := fmt.Sprintf("sleep 0.1; p=%s; if [ -f %s ]; then p=$(cat %s 2>/dev/null || true); fi; case \"$p\" in ''|*[!0-9]*) ;; *) kill \"$p\" >/dev/null 2>&1 || true ;; esac; sleep 0.05; if [ -f %s ]; then cat %s; fi; rm -f %s %s",
		shellQuote(strings.TrimSpace(pid)), shellQuote(pidPath), shellQuote(pidPath), shellQuote(logPath), shellQuote(logPath), shellQuote(logPath), shellQuote(pidPath))
	return runCommand(opts.Bundle.Check, server, command)
}

func bandwidthServerStartCommand(serverArgs []string, logPath string) string {
	pidPath := logPath + ".pid"
	return fmt.Sprintf("nohup %s > %s 2>&1 & p=$!; printf '%%s\\n' \"$p\" > %s; printf '%%s\\n' \"$p\"",
		shellJoin(serverArgs), shellQuote(logPath), shellQuote(pidPath))
}

func failedBandwidthResult(cfg spec.CheckBandwidthConfig, server, client Target, stream checkStream, clientOutput, serverOutput, clientError, serverError string) Result {
	result := pendingBandwidthResultForConfig(cfg, server, client, stream)
	result.Passed = false
	result.ClientOutput = clientOutput
	result.Output = clientOutput
	result.ServerOutput = serverOutput
	result.ClientError = clientError
	result.ServerError = serverError
	return result
}

func resultFromOutput(cfg spec.CheckBandwidthConfig, server Target, client Target, stream checkStream, output string) Result {
	gbits, ok := ParseBandwidthGBits(output)
	if !ok {
		gbits = math.NaN()
	}
	serverTopology := groupTopologyLink(stream.ServerGroup, stream.ServerOffset)
	clientTopology := groupTopologyLink(stream.ClientGroup, stream.ClientOffset)
	result := Result{
		Server:          server,
		Client:          client,
		ServerGroup:     stream.ServerGroup,
		ClientGroup:     stream.ClientGroup,
		ServerRDMAIndex: stream.ServerRDMAIndex,
		ClientRDMAIndex: stream.ClientRDMAIndex,
		ServerXP:        stream.ServerOffset,
		ClientXP:        stream.ClientOffset,
		ServerTopology:  serverTopology,
		ClientTopology:  clientTopology,
		Degraded:        topologyLinkDegraded(serverTopology) || topologyLinkDegraded(clientTopology),
		Port:            stream.Port,
		GBits:           gbits,
		Passed:          true,
		Output:          output,
		ClientOutput:    output,
	}
	result.ClientMaxMbps = stream.ClientSpeed.MaximumMbps
	result.ServerMaxMbps = stream.ServerSpeed.MaximumMbps
	result.ClientNowMbps = stream.ClientSpeed.CurrentMbps
	result.ServerNowMbps = stream.ServerSpeed.CurrentMbps
	result.ClientSpeedError = stream.ClientSpeed.Error
	result.ServerSpeedError = stream.ServerSpeed.Error
	applyBandwidthThreshold(cfg, &result, ok)
	return result
}

func applyBandwidthThreshold(cfg spec.CheckBandwidthConfig, result *Result, measured bool) {
	result.ThresholdMode = cfg.MinGBitsMode()
	switch result.ThresholdMode {
	case "manual":
		result.ThresholdKnown = true
		result.ThresholdGBits = cfg.MinGBits
	case "auto":
		if result.ClientMaxMbps <= 0 || result.ServerMaxMbps <= 0 {
			result.ThresholdError = fmt.Sprintf("auto threshold unavailable: client max=%s (%s), server max=%s (%s)", bandwidthSpeedLabel(result.ClientMaxMbps), firstNonEmpty(result.ClientSpeedError, "no maximum speed"), bandwidthSpeedLabel(result.ServerMaxMbps), firstNonEmpty(result.ServerSpeedError, "no maximum speed"))
			result.Passed = false
			return
		}
		result.ThresholdKnown = true
		result.BaselineGBits = float64(minInt(result.ClientMaxMbps, result.ServerMaxMbps)) / 1000
		result.ThresholdGBits = result.BaselineGBits * bandwidthAutoThresholdRatio
	case "disabled":
		result.Passed = true
		return
	}
	if measured {
		result.Passed = !math.IsNaN(result.GBits) && result.GBits >= result.ThresholdGBits
	}
}

func bandwidthStreams(cfg spec.CheckBandwidthConfig) []checkStream {
	return bandwidthStreamsForGroups(cfg, cfg.RDMAGroups, cfg.RDMAGroups)
}

func bandwidthStreamsForGroups(cfg spec.CheckBandwidthConfig, serverGroups, clientGroups []spec.CheckRDMAGroup) []checkStream {
	streams := make([]checkStream, 0)
	port := bandwidthBasePort(cfg)
	mmapEnabled := strings.TrimSpace(cfg.MmapDevice) != ""
	for clientGroupIndex, clientGroup := range clientGroups {
		clientOffsets := []string{""}
		if mmapEnabled {
			clientOffsets = clientGroup.XPUOffsets
		}
		for serverGroupIndex, serverGroup := range serverGroups {
			serverOffsets := []string{""}
			if mmapEnabled {
				serverOffsets = serverGroup.XPUOffsets
			}
			for _, clientOffset := range clientOffsets {
				for _, serverOffset := range serverOffsets {
					streams = append(streams, checkStream{
						ServerGroup:     serverGroup,
						ServerRDMAIndex: serverGroupIndex,
						ClientGroup:     clientGroup,
						ClientRDMAIndex: clientGroupIndex,
						ServerOffset:    strings.TrimSpace(serverOffset),
						ClientOffset:    strings.TrimSpace(clientOffset),
						Port:            port,
					})
					port++
				}
			}
		}
	}
	return streams
}

func bandwidthStreamBatches(cfg spec.CheckBandwidthConfig) [][]checkStream {
	return bandwidthStreamBatchesForGroups(cfg, cfg.RDMAGroups, cfg.RDMAGroups)
}

func bandwidthStreamBatchesForGroups(cfg spec.CheckBandwidthConfig, serverGroups, clientGroups []spec.CheckRDMAGroup) [][]checkStream {
	streams := bandwidthStreamsForGroups(cfg, serverGroups, clientGroups)
	return bandwidthStreamBatchesFromStreams(streams)
}

func bandwidthStreamBatchesFromStreams(streams []checkStream) [][]checkStream {
	batches := make([][]checkStream, 0)
	remaining := append([]checkStream(nil), streams...)
	for len(remaining) > 0 {
		usedClients := make(map[int]bool)
		usedServers := make(map[int]bool)
		batch := make([]checkStream, 0)
		nextRemaining := make([]checkStream, 0, len(remaining))
		for _, stream := range remaining {
			if usedClients[stream.ClientRDMAIndex] || usedServers[stream.ServerRDMAIndex] {
				nextRemaining = append(nextRemaining, stream)
				continue
			}
			batch = append(batch, stream)
			usedClients[stream.ClientRDMAIndex] = true
			usedServers[stream.ServerRDMAIndex] = true
		}
		if len(batch) == 0 {
			return append(batches, remaining)
		}
		batches = append(batches, batch)
		remaining = nextRemaining
	}
	return batches
}

func bandwidthBasePort(cfg spec.CheckBandwidthConfig) int {
	if cfg.BasePort > 0 {
		return cfg.BasePort
	}
	return 18515
}

func bandwidthPeerAddress(server Target, stream checkStream) string {
	if stream.ServerRDMAIndex >= 0 && stream.ServerRDMAIndex < len(server.RDMA) {
		if address := strings.TrimSpace(server.RDMA[stream.ServerRDMAIndex].IP); address != "" {
			return address
		}
	}
	return server.Address
}

func bandwidthAddressForMode(opts Options, server Target, stream checkStream) string {
	if opts.currentBandwidthMode() == BandwidthModeVerbs {
		return targetControlAddress(server)
	}
	return bandwidthPeerAddress(server, stream)
}

func ibWriteBWArgs(cfg spec.CheckBandwidthConfig, group spec.CheckRDMAGroup, offset string, serverAddress string, port int) []string {
	return ibWriteBWArgsForMode(cfg, group, offset, serverAddress, port, BandwidthModeRDMACM)
}

func ibWriteBWArgsForMode(cfg spec.CheckBandwidthConfig, group spec.CheckRDMAGroup, offset string, serverAddress string, port int, mode string) []string {
	iterations := cfg.Iterations
	if iterations == 0 {
		iterations = 100
	}
	args := []string{"ib_write_bw"}
	if cfg.RunByDuration {
		duration := cfg.Duration
		if duration <= 0 {
			duration = 10
		}
		args = append(args, "-D", strconv.Itoa(duration), "-f", "2", "-N")
	} else {
		args = append(args, "-n", strconv.Itoa(iterations))
	}
	args = append(args, "-d", group.IBDevice)
	if mode == BandwidthModeVerbs {
		args = append(args, "-i", "1")
		if cfg.GIDIndex >= 0 {
			args = append(args, "-x", strconv.Itoa(cfg.GIDIndex))
		}
	}
	if cfg.BandwidthQPs > 0 {
		args = append(args, "-q", strconv.Itoa(cfg.BandwidthQPs))
	}
	if cfg.MessageSize > 0 {
		args = append(args, "-s", strconv.Itoa(cfg.MessageSize))
	}
	if cfg.ReportGBits {
		args = append(args, "--report_gbits")
	}
	if strings.TrimSpace(cfg.MmapDevice) != "" {
		args = append(args, "--mmap="+cfg.MmapDevice)
	}
	if strings.TrimSpace(offset) != "" {
		args = append(args, "--mmap-offset="+offset)
	}
	args = append(args, "-F")
	if mode != BandwidthModeVerbs {
		args = append(args, "-R")
	}
	if port > 0 {
		args = append(args, "-p", strconv.Itoa(port))
	}
	if strings.TrimSpace(serverAddress) != "" {
		args = append(args, serverAddress)
	}
	return args
}
func ParseBandwidthGBits(output string) (float64, bool) {
	var bandwidth float64
	ok := false
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "---") {
			continue
		}
		values := numericFields(line)
		if len(values) < 4 {
			continue
		}
		// perftest bandwidth rows are:
		// #bytes #iterations BW peak[Gb/sec] BW average[Gb/sec] ...
		// Any trailing MsgRate column is not bandwidth.
		bandwidth = values[3]
		ok = true
	}
	return bandwidth, ok
}

func numericFields(line string) []float64 {
	var values []float64
	for _, field := range strings.Fields(line) {
		value, err := strconv.ParseFloat(strings.Trim(field, "[],"), 64)
		if err != nil {
			continue
		}
		values = append(values, value)
	}
	return values
}
