package checker

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"

	"envinit/internal/spec"
)

func runRDMAPingChecks(opts Options, targets []Target) []string {
	var failures []string
	var rows []rdmaPingResultRow
	for i := 0; i < len(targets); i++ {
		for j := i + 1; j < len(targets); j++ {
			for _, pair := range [][2]Target{{targets[i], targets[j]}, {targets[j], targets[i]}} {
				items, err := rdmaPingItems(opts.Bundle, pair[0], pair[1])
				if err != nil {
					failures = append(failures, err.Error())
					rows = append(rows, rdmaPingResultRow{
						Status:         "FAIL",
						Source:         pair[0].Name,
						Destination:    pair[1].Name,
						SourceNIC:      "-",
						DestinationNIC: "-",
						SourceIP:       "-",
						Payload:        strconv.Itoa(opts.Bundle.Check.RDMAPing.PayloadSize),
						Result:         err.Error(),
						Failure:        true,
					})
					continue
				}
				pairRows, pairFailures := runRDMAPingPair(opts, pair[0], pair[1], items)
				rows = append(rows, pairRows...)
				failures = append(failures, pairFailures...)
			}
		}
	}
	printRDMAPingResultTable(opts.Output, rows)
	return failures
}

func runRDMAPingPair(opts Options, source Target, destination Target, items []rdmaPingItem) ([]rdmaPingResultRow, []string) {
	rows := make([]rdmaPingResultRow, len(items))
	failures := make([]string, 0)
	errs := make([]error, len(items))
	if opts.DryRun {
		for index, item := range items {
			errs[index] = runRDMAPingOne(opts, source, destination, item)
		}
	} else {
		runRDMAPingItemsParallel(opts, source, destination, items, errs)
	}
	failed := 0
	for index, item := range items {
		if errs[index] != nil {
			failed++
			rows[index] = rdmaPingResultRow{
				Status:         "FAIL",
				Source:         source.Name,
				Destination:    destination.Name,
				SourceNIC:      firstNonEmpty(item.SourceName, rdmaNICLabel(source, item.SourceIndex)),
				DestinationNIC: firstNonEmpty(item.DestinationName, rdmaNICLabel(destination, item.DestinationIndex)),
				SourceIP:       item.SourceIP,
				DestinationIP:  item.DestinationIP,
				Payload:        strconv.Itoa(opts.Bundle.Check.RDMAPing.PayloadSize),
				Result:         compactTableCell(errs[index].Error()),
				Failure:        true,
			}
			continue
		}
		rows[index] = rdmaPingResultRow{
			Status:         "PASS",
			Source:         source.Name,
			Destination:    destination.Name,
			SourceNIC:      firstNonEmpty(item.SourceName, rdmaNICLabel(source, item.SourceIndex)),
			DestinationNIC: firstNonEmpty(item.DestinationName, rdmaNICLabel(destination, item.DestinationIndex)),
			SourceIP:       item.SourceIP,
			DestinationIP:  item.DestinationIP,
			Payload:        strconv.Itoa(opts.Bundle.Check.RDMAPing.PayloadSize),
			Result:         "ok",
		}
	}
	if failed > 0 {
		failures = append(failures, fmt.Sprintf("rdma-ping %s -> %s detected %d failure(s); see RDMA ping result summary", source.Name, destination.Name, failed))
	}
	return rows, failures
}

func runRDMAPingItemsParallel(opts Options, source Target, destination Target, items []rdmaPingItem, errs []error) {
	type pingResult struct {
		index int
		err   error
	}
	ch := make(chan pingResult, len(items))
	sem := make(chan struct{}, rdmaPingConcurrency(source))
	var wg sync.WaitGroup
	for index, item := range items {
		index := index
		item := item
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ch <- pingResult{index: index, err: runRDMAPingOne(opts, source, destination, item)}
		}()
	}
	wg.Wait()
	close(ch)
	for result := range ch {
		errs[result.index] = result.err
	}
}

func rdmaPingConcurrency(source Target) int {
	if source.Local {
		return 8
	}
	return 4
}

func printRDMAPingResultTable(output io.Writer, rows []rdmaPingResultRow) {
	if len(rows) == 0 {
		fmt.Fprintln(output, "WARN rdma-ping results: no completed ping checks")
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left := rows[i]
		right := rows[j]
		if left.Failure != right.Failure {
			return left.Failure
		}
		for _, pair := range [][2]string{
			{left.Source, right.Source},
			{left.Destination, right.Destination},
			{left.SourceNIC, right.SourceNIC},
			{left.DestinationNIC, right.DestinationNIC},
			{left.SourceIP, right.SourceIP},
			{left.DestinationIP, right.DestinationIP},
		} {
			if pair[0] != pair[1] {
				return pair[0] < pair[1]
			}
		}
		return false
	})

	headers := []string{"STATUS", "SOURCE", "DEST", "SOURCE_NIC", "DEST_NIC", "SOURCE_IP", "DEST_IP", "PAYLOAD", "RESULT"}
	widths := make([]int, len(headers))
	for idx, header := range headers {
		widths[idx] = len(header)
	}
	tableRows := make([][]string, 0, len(rows))
	for _, row := range rows {
		cells := []string{
			row.Status,
			row.Source,
			row.Destination,
			row.SourceNIC,
			row.DestinationNIC,
			firstNonEmpty(row.SourceIP, "-"),
			firstNonEmpty(row.DestinationIP, "-"),
			row.Payload,
			firstNonEmpty(row.Result, "-"),
		}
		for idx, cell := range cells {
			if len(cell) > widths[idx] {
				widths[idx] = len(cell)
			}
		}
		tableRows = append(tableRows, cells)
	}

	fmt.Fprintln(output, "RDMA ping result summary:")
	fmt.Fprintln(output, formatTableLine(headers, widths))
	fmt.Fprintln(output, formatTableSeparator(widths))
	for idx, cells := range tableRows {
		line := formatTableLine(cells, widths)
		if rows[idx].Failure {
			line = redText(line)
		}
		fmt.Fprintln(output, line)
	}
}

func rdmaPingItems(bundle spec.Bundle, source Target, destination Target) ([]rdmaPingItem, error) {
	maxItems := maxInt(len(source.RDMA), len(destination.RDMA), len(bundle.Defaults.RDMAInterfaces))
	sourceNames := make([]string, maxItems)
	sourceIPs := make([]string, maxItems)
	destinationNames := make([]string, maxItems)
	destinationIPs := make([]string, maxItems)
	var missing []string
	for idx := 0; idx < maxItems; idx++ {
		destinationIP := ""
		if idx < len(destination.RDMA) {
			destinationIP = strings.TrimSpace(destination.RDMA[idx].IP)
		}
		destinationName := ""
		if idx < len(destination.RDMA) {
			destinationName = strings.TrimSpace(destination.RDMA[idx].Name)
		}
		if destinationName == "" && idx < len(bundle.Defaults.RDMAInterfaces) {
			destinationName = strings.TrimSpace(bundle.Defaults.RDMAInterfaces[idx].Name)
		}
		sourceName := ""
		if idx < len(source.RDMA) {
			sourceName = strings.TrimSpace(source.RDMA[idx].Name)
		}
		if sourceName == "" && idx < len(bundle.Defaults.RDMAInterfaces) {
			sourceName = strings.TrimSpace(bundle.Defaults.RDMAInterfaces[idx].Name)
		}
		if sourceName == "" && idx < len(source.RDMA) {
			sourceName = strings.TrimSpace(source.RDMA[idx].IP)
		}
		sourceIP := ""
		if idx < len(source.RDMA) {
			sourceIP = strings.TrimSpace(source.RDMA[idx].IP)
		}
		if destinationIP == "" {
			missing = append(missing, fmt.Sprintf("%s rdma%d_ip", destination.Name, idx+1))
		}
		if sourceIP == "" {
			missing = append(missing, fmt.Sprintf("%s rdma%d_ip", source.Name, idx+1))
		}
		if sourceName == "" {
			missing = append(missing, fmt.Sprintf("%s rdma%d_name", source.Name, idx+1))
		}
		sourceNames[idx] = sourceName
		sourceIPs[idx] = sourceIP
		destinationNames[idx] = destinationName
		destinationIPs[idx] = destinationIP
	}
	if maxItems == 0 {
		if len(missing) == 0 {
			return nil, fmt.Errorf("no RDMA ping targets for %s -> %s", source.Name, destination.Name)
		}
		return nil, fmt.Errorf("no RDMA ping targets for %s -> %s; fill inventory fields: %s", source.Name, destination.Name, strings.Join(missing, ", "))
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("incomplete RDMA ping inventory for %s -> %s; fill fields: %s", source.Name, destination.Name, strings.Join(missing, ", "))
	}
	items := make([]rdmaPingItem, 0, maxItems*maxItems)
	for sourceIndex, sourceName := range sourceNames {
		for destinationIndex, destinationIP := range destinationIPs {
			items = append(items, rdmaPingItem{
				SourceIndex:      sourceIndex,
				DestinationIndex: destinationIndex,
				SourceName:       sourceName,
				SourceIP:         sourceIPs[sourceIndex],
				DestinationName:  destinationNames[destinationIndex],
				DestinationIP:    destinationIP,
			})
		}
	}
	return items, nil
}

func runRDMAPingOne(opts Options, source Target, destination Target, item rdmaPingItem) error {
	args := rdmaPingArgs(opts.Bundle.Check, item)
	if opts.DryRun {
		fmt.Fprintf(opts.Output, "dry-run rdma-ping %s: %s\n", source.Name, shellJoin(args))
		return nil
	}
	output, err := runCommand(opts.Bundle.Check, source, shellJoin(args))
	path := fmt.Sprintf("%s %s -> %s %s(%s)", source.Name, firstNonEmpty(item.SourceName, rdmaLabel(item.SourceIndex)), destination.Name, firstNonEmpty(item.DestinationName, rdmaLabel(item.DestinationIndex)), item.DestinationIP)
	if err != nil {
		return fmt.Errorf("ping %s: %s", path, pingFailureSummary(output, err))
	}
	if strings.Contains(output, " 0% packet loss") {
		return nil
	}
	return fmt.Errorf("ping %s: %s", path, pingFailureSummary(output, nil))
}

func pingFailureSummary(output string, err error) string {
	if line := packetLossLine(output); line != "" {
		return compactTableCell(line)
	}
	if line := firstInterestingPingLine(output); line != "" {
		return compactTableCell(line)
	}
	if err != nil {
		if line := firstInterestingCommandErrorLine(err.Error()); line != "" {
			return compactTableCell(line)
		}
		return compactTableCell(err.Error())
	}
	return "ping did not report 0% packet loss"
}

func packetLossLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "packet loss") {
			return line
		}
	}
	return ""
}

func firstInterestingPingLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "PING ") || strings.HasPrefix(line, "--- ") || strings.Contains(line, " ping statistics ---") {
			continue
		}
		return line
	}
	return ""
}

func firstInterestingCommandErrorLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "exit status") || strings.HasPrefix(line, "local command on ") || strings.HasPrefix(line, "ssh ") {
			continue
		}
		return line
	}
	return ""
}

func compactTableCell(text string) string {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, " ")
}

func rdmaPingArgs(cfg spec.CheckConfig, item rdmaPingItem) []string {
	return []string{
		"ping",
		"-c", strconv.Itoa(cfg.RDMAPing.Count),
		"-W", strconv.Itoa(cfg.RDMAPing.Timeout),
		"-M", "do",
		"-s", strconv.Itoa(cfg.RDMAPing.PayloadSize),
		"-I", item.SourceName,
		item.DestinationIP,
	}
}
