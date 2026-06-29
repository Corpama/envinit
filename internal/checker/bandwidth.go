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

func validateGroup(cfg spec.CheckConfig, group spec.CheckRDMAGroup) error {
	if strings.TrimSpace(group.IBDevice) == "" {
		return errors.New("check.rdma_groups[].ib_device is required")
	}
	if strings.TrimSpace(cfg.MmapDevice) == "" {
		return nil
	}
	if len(group.XPUOffsets) < 1 {
		return fmt.Errorf("check.rdma_groups[%s].xpu_offsets requires at least 1 offset when bandwidth mmap is enabled", group.IBDevice)
	}
	return nil
}

func runParallel(opts Options, groupsByTarget resolvedRDMAGroups, server Target, client Target) ([]Result, []error) {
	batches := bandwidthStreamBatches(opts.Bundle.Check)
	if opts.DryRun {
		results := make([]Result, 0)
		for batchIndex, batch := range batches {
			fmt.Fprintf(opts.Output, "dry-run bandwidth batch %d %s -> %s: %d stream(s)\n", batchIndex+1, client.Name, server.Name, len(batch))
			for _, stream := range batch {
				stream = resolveStreamGroups(groupsByTarget, server, client, stream)
				serverArgs := ibWriteBWArgs(opts.Bundle.Check, stream.ServerGroup, stream.ServerOffset, "", stream.Port)
				clientArgs := ibWriteBWArgs(opts.Bundle.Check, stream.ClientGroup, stream.ClientOffset, bandwidthPeerAddress(server, stream), stream.Port)
				fmt.Fprintf(opts.Output, "dry-run server %s: %s\n", server.Name, shellJoin(serverArgs))
				fmt.Fprintf(opts.Output, "dry-run client %s: %s\n", client.Name, shellJoin(clientArgs))
				results = append(results, resultFromOutput(opts.Bundle.Check, server, client, stream, ""))
			}
		}
		return results, nil
	}

	var results []Result
	var errs []error
	for _, batch := range batches {
		for idx := range batch {
			batch[idx] = resolveStreamGroups(groupsByTarget, server, client, batch[idx])
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
	}
	processes := make([]serverProcess, 0, len(streams))
	var errs []error
	for _, stream := range streams {
		serverArgs := ibWriteBWArgs(opts.Bundle.Check, stream.ServerGroup, stream.ServerOffset, "", stream.Port)
		logPath := fmt.Sprintf("/tmp/envinit-check-%s-%d-%d.log", sanitizeName(stream.ServerGroup.IBDevice), stream.Port, time.Now().UnixNano())
		serverCmd := fmt.Sprintf("nohup %s > %s 2>&1 & echo $!", shellJoin(serverArgs), shellQuote(logPath))
		pid, err := runCommand(opts.Bundle.Check, server, serverCmd)
		if err != nil {
			errs = append(errs, fmt.Errorf("start server on %s %s: %w", server.Name, streamLabel(stream), err))
			continue
		}
		processes = append(processes, serverProcess{stream: stream, pid: strings.TrimSpace(pid)})
	}
	defer func() {
		for _, process := range processes {
			_, _ = runCommand(opts.Bundle.Check, server, fmt.Sprintf("kill %s >/dev/null 2>&1 || true", shellQuote(process.pid)))
		}
	}()
	if len(processes) == 0 {
		return nil, errs
	}
	time.Sleep(800 * time.Millisecond)

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
			clientArgs := ibWriteBWArgs(opts.Bundle.Check, process.stream.ClientGroup, process.stream.ClientOffset, bandwidthPeerAddress(server, process.stream), process.stream.Port)
			output, err := runCommand(opts.Bundle.Check, client, shellJoin(clientArgs))
			if err != nil {
				ch <- streamResult{index: index, err: fmt.Errorf("run client on %s against %s %s: %w", client.Name, server.Name, streamLabel(process.stream), err)}
				return
			}
			ch <- streamResult{index: index, result: resultFromOutput(opts.Bundle.Check, server, client, process.stream, output)}
		}()
	}
	wg.Wait()
	close(ch)

	ordered := make([]*Result, len(processes))
	for item := range ch {
		if item.err != nil {
			errs = append(errs, item.err)
			continue
		}
		ordered[item.index] = &item.result
	}
	results := make([]Result, 0, len(processes))
	for _, result := range ordered {
		if result != nil {
			results = append(results, *result)
		}
	}
	return results, errs
}

func runStream(opts Options, server Target, client Target, stream checkStream) (Result, error) {
	serverArgs := ibWriteBWArgs(opts.Bundle.Check, stream.ServerGroup, stream.ServerOffset, "", stream.Port)
	clientArgs := ibWriteBWArgs(opts.Bundle.Check, stream.ClientGroup, stream.ClientOffset, bandwidthPeerAddress(server, stream), stream.Port)

	if opts.DryRun {
		fmt.Fprintf(opts.Output, "dry-run server %s: %s\n", server.Name, shellJoin(serverArgs))
		fmt.Fprintf(opts.Output, "dry-run client %s: %s\n", client.Name, shellJoin(clientArgs))
		return resultFromOutput(opts.Bundle.Check, server, client, stream, ""), nil
	}

	logPath := fmt.Sprintf("/tmp/envinit-check-%s-%d-%d.log", sanitizeName(stream.ServerGroup.IBDevice), stream.Port, time.Now().UnixNano())
	serverCmd := fmt.Sprintf("nohup %s > %s 2>&1 & echo $!", shellJoin(serverArgs), shellQuote(logPath))
	pid, err := runCommand(opts.Bundle.Check, server, serverCmd)
	if err != nil {
		return Result{}, fmt.Errorf("start server on %s: %w", server.Name, err)
	}
	pid = strings.TrimSpace(pid)
	defer func() {
		_, _ = runCommand(opts.Bundle.Check, server, fmt.Sprintf("kill %s >/dev/null 2>&1 || true", shellQuote(pid)))
	}()
	time.Sleep(800 * time.Millisecond)

	output, err := runCommand(opts.Bundle.Check, client, shellJoin(clientArgs))
	if err != nil {
		return Result{}, fmt.Errorf("run client on %s against %s: %w", client.Name, server.Name, err)
	}
	return resultFromOutput(opts.Bundle.Check, server, client, stream, output), nil
}

func resultFromOutput(cfg spec.CheckConfig, server Target, client Target, stream checkStream, output string) Result {
	gbits, ok := ParseBandwidthGBits(output)
	if !ok {
		gbits = math.NaN()
	}
	passed := true
	if cfg.MinGBits > 0 {
		passed = ok && gbits >= cfg.MinGBits
	}
	return Result{
		Server:          server,
		Client:          client,
		ServerGroup:     stream.ServerGroup,
		ClientGroup:     stream.ClientGroup,
		ServerRDMAIndex: stream.ServerRDMAIndex,
		ClientRDMAIndex: stream.ClientRDMAIndex,
		ServerXP:        stream.ServerOffset,
		ClientXP:        stream.ClientOffset,
		Port:            stream.Port,
		GBits:           gbits,
		Passed:          passed,
		Output:          output,
	}
}

func bandwidthStreams(cfg spec.CheckConfig) []checkStream {
	streams := make([]checkStream, 0)
	port := bandwidthBasePort(cfg)
	mmapEnabled := strings.TrimSpace(cfg.MmapDevice) != ""
	for clientGroupIndex, clientGroup := range cfg.RDMAGroups {
		clientOffsets := []string{""}
		if mmapEnabled {
			clientOffsets = clientGroup.XPUOffsets
		}
		for serverGroupIndex, serverGroup := range cfg.RDMAGroups {
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

func bandwidthStreamBatches(cfg spec.CheckConfig) [][]checkStream {
	streams := bandwidthStreams(cfg)
	batches := make([][]checkStream, 0)
	remaining := append([]checkStream(nil), streams...)
	for len(remaining) > 0 {
		usedClients := make(map[int]bool)
		usedServers := make(map[int]bool)
		batch := make([]checkStream, 0, len(cfg.RDMAGroups))
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

func bandwidthBasePort(cfg spec.CheckConfig) int {
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

func ibWriteBWArgs(cfg spec.CheckConfig, group spec.CheckRDMAGroup, offset string, serverAddress string, port int) []string {
	iterations := cfg.Iterations
	if iterations == 0 {
		iterations = 100
	}
	args := []string{
		"ib_write_bw",
		"-n", strconv.Itoa(iterations),
		"-d", group.IBDevice,
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
	args = append(args, "-F", "-R")
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
