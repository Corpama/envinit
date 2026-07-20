package checker

import (
	"context"
	"errors"
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
	type pairPlan struct {
		source      Target
		destination Target
		items       []rdmaPingItem
		rowOffset   int
		err         error
	}
	var plans []pairPlan
	for i := 0; i < len(targets); i++ {
		for j := i + 1; j < len(targets); j++ {
			for _, pair := range [][2]Target{{targets[i], targets[j]}, {targets[j], targets[i]}} {
				items, err := rdmaPingItems(opts.Bundle, pair[0], pair[1])
				if err != nil {
					plans = append(plans, pairPlan{source: pair[0], destination: pair[1], rowOffset: len(rows), err: err})
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
				plans = append(plans, pairPlan{source: pair[0], destination: pair[1], items: items, rowOffset: len(rows)})
				for _, item := range items {
					rows = append(rows, rdmaPingRow(opts, pair[0], pair[1], item, "WAIT", nil))
				}
			}
		}
	}
	interactive := opts.LiveOutput && !opts.DryRun
	live := interactive && opts.checkTUI == nil
	tableRows := make([][]string, len(rows))
	for idx := range rows {
		tableRows[idx] = rdmaPingTableCells(rows[idx], live)
	}
	table := newLiveResultTable(opts.Output, live, "RDMA ping live results:", rdmaPingResultHeaders(), tableRows)
	if opts.checkTUI != nil {
		items := make([]checkTUIItem, len(rows))
		for idx, row := range rows {
			items[idx] = rdmaPingTUIItem(idx, row)
		}
		opts.checkTUI.SetResults(checkTUIStagePing, rdmaPingResultHeaders(), items)
		for _, row := range rows {
			if row.Failure {
				opts.checkTUI.AppendLog(checkTUIStagePing, rdmaPingRawLogLine(row))
			}
		}
		for _, plan := range plans {
			if plan.err != nil {
				continue
			}
			for index, pingItem := range plan.items {
				source := plan.source
				destination := plan.destination
				item := pingItem
				rowIndex := plan.rowOffset + index
				itemID := fmt.Sprintf("ping-%d", rowIndex)
				opts.checkTUI.RegisterRetest(checkTUIStagePing, itemID, func(ctx context.Context) {
					retestOpts := opts
					retestOpts.Context = ctx
					itemOpts, finishItem := beginRetestCheckItem(retestOpts, checkTUIStagePing, itemID)
					running := rdmaPingRow(retestOpts, source, destination, item, "RUNNING", nil)
					runningItem := rdmaPingTUIItem(rowIndex, running)
					runningItem.Retest = true
					runningItem.Detail += "\n\nManual retest is running; the original check verdict is preserved."
					retestOpts.checkTUI.Update(runningItem)

					err := runRDMAPingOne(itemOpts, source, destination, item)
					finishItem()
					row := rdmaPingRow(retestOpts, source, destination, item, "", err)
					resultItem := rdmaPingTUIItem(rowIndex, row)
					resultItem.Retest = true
					resultItem.Detail += "\n\nThis is the latest manual retest result; the original check verdict is preserved."
					retestOpts.checkTUI.Update(resultItem)
					retestOpts.checkTUI.AppendLog(checkTUIStagePing, "RETEST "+rdmaPingRawLogLine(row))
				})
			}
		}
	}
	completedRows := append([]rdmaPingResultRow(nil), rows...)
	for _, plan := range plans {
		if checkCancellationError(opts) != nil {
			break
		}
		if plan.err != nil {
			failures = append(failures, plan.err.Error())
			continue
		}
		pairRows, pairFailures := runRDMAPingPair(opts, plan.source, plan.destination, plan.items, table, plan.rowOffset)
		copy(completedRows[plan.rowOffset:], pairRows)
		failures = append(failures, pairFailures...)
	}
	if !interactive {
		printRDMAPingResultTable(opts.Output, completedRows)
	} else if opts.checkTUI == nil {
		printRDMAPingFailureDetails(opts.Output, completedRows)
	}
	return failures
}

func runRDMAPingPair(opts Options, source Target, destination Target, items []rdmaPingItem, table *liveResultTable, rowOffset int) ([]rdmaPingResultRow, []string) {
	rows := make([]rdmaPingResultRow, len(items))
	failures := make([]string, 0)
	errs := make([]error, len(items))
	if opts.DryRun {
		for index, item := range items {
			errs[index] = runRDMAPingOne(opts, source, destination, item)
		}
	} else {
		updates := make(map[int][]string, len(items))
		for index, item := range items {
			row := rdmaPingRow(opts, source, destination, item, "RUNNING", nil)
			updates[rowOffset+index] = rdmaPingTableCells(row, true)
			if opts.checkTUI != nil {
				opts.checkTUI.Update(rdmaPingTUIItem(rowOffset+index, row))
			}
		}
		table.UpdateRows(updates)
		runRDMAPingItemsParallel(opts, source, destination, items, errs, rowOffset, func(index int, err error) {
			row := rdmaPingRow(opts, source, destination, items[index], "", err)
			table.Update(rowOffset+index, rdmaPingTableCells(row, true))
			if opts.checkTUI != nil {
				opts.checkTUI.Update(rdmaPingTUIItem(rowOffset+index, row))
				opts.checkTUI.AppendLog(checkTUIStagePing, rdmaPingRawLogLine(row))
			}
		})
	}
	failed := 0
	for index, item := range items {
		rows[index] = rdmaPingRow(opts, source, destination, item, "", errs[index])
		if rows[index].Failure {
			failed++
		}
	}
	if failed > 0 {
		failures = append(failures, fmt.Sprintf("rdma-ping %s -> %s detected %d failure(s); see RDMA ping result summary", source.Name, destination.Name, failed))
	}
	return rows, failures
}

func runRDMAPingItemsParallel(opts Options, source Target, destination Target, items []rdmaPingItem, errs []error, rowOffset int, onResult func(int, error)) {
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
			itemOpts, finishItem := beginCheckItem(opts, checkTUIStagePing, fmt.Sprintf("ping-%d", rowOffset+index))
			defer finishItem()
			sem <- struct{}{}
			defer func() { <-sem }()
			ch <- pingResult{index: index, err: runRDMAPingOne(itemOpts, source, destination, item)}
		}()
	}
	go func() {
		wg.Wait()
		close(ch)
	}()
	for result := range ch {
		errs[result.index] = result.err
		if onResult != nil {
			onResult(result.index, result.err)
		}
	}
}

func rdmaPingRow(opts Options, source Target, destination Target, item rdmaPingItem, status string, err error) rdmaPingResultRow {
	row := rdmaPingResultRow{
		Status:         status,
		Source:         source.Name,
		Destination:    destination.Name,
		SourceNIC:      firstNonEmpty(item.SourceName, rdmaNICLabel(source, item.SourceIndex)),
		DestinationNIC: firstNonEmpty(item.DestinationName, rdmaNICLabel(destination, item.DestinationIndex)),
		SourceIP:       item.SourceIP,
		DestinationIP:  item.DestinationIP,
		Payload:        strconv.Itoa(opts.Bundle.Check.RDMAPing.PayloadSize),
		Result:         "-",
	}
	if status == "WAIT" || status == "RUNNING" {
		return row
	}
	if err != nil {
		if errors.Is(err, errCheckItemAborted) || errors.Is(err, errCheckStageAborted) {
			row.Status = "ABORT"
			row.Result = err.Error()
			row.Failure = true
			return row
		}
		row.Status = "FAIL"
		row.Result = compactTableCell(err.Error())
		row.Failure = true
		return row
	}
	row.Status = "PASS"
	row.Result = "ok"
	return row
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

	headers := rdmaPingResultHeaders()
	widths := make([]int, len(headers))
	for idx, header := range headers {
		widths[idx] = len(header)
	}
	tableRows := make([][]string, 0, len(rows))
	for _, row := range rows {
		cells := rdmaPingTableCells(row, false)
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

func rdmaPingResultHeaders() []string {
	return []string{"STATUS", "SOURCE", "DEST", "SOURCE_NIC", "DEST_NIC", "SOURCE_IP", "DEST_IP", "PAYLOAD", "RESULT"}
}

func rdmaPingTableCells(row rdmaPingResultRow, live bool) []string {
	result := firstNonEmpty(row.Result, "-")
	if live {
		result = compactLiveResult(result, 56)
	}
	return []string{row.Status, row.Source, row.Destination, row.SourceNIC, row.DestinationNIC, firstNonEmpty(row.SourceIP, "-"), firstNonEmpty(row.DestinationIP, "-"), row.Payload, result}
}

func printRDMAPingFailureDetails(output io.Writer, rows []rdmaPingResultRow) {
	printedHeader := false
	for _, row := range rows {
		if !row.Failure {
			continue
		}
		if !printedHeader {
			fmt.Fprintln(output, "RDMA ping failure details:")
			printedHeader = true
		}
		fmt.Fprintf(output, "  FAIL %s %s(%s) -> %s %s(%s): %s\n",
			row.Source, row.SourceNIC, row.SourceIP,
			row.Destination, row.DestinationNIC, row.DestinationIP,
			row.Result,
		)
	}
}

func rdmaPingTUIItem(index int, row rdmaPingResultRow) checkTUIItem {
	detail := fmt.Sprintf("Source: %s\nSource NIC: %s\nSource IP: %s\nDestination: %s\nDestination NIC: %s\nDestination IP: %s\nPayload: %s\nStatus: %s\n\nResult:\n%s",
		row.Source, row.SourceNIC, row.SourceIP,
		row.Destination, row.DestinationNIC, row.DestinationIP,
		row.Payload, row.Status, firstNonEmpty(row.Result, "-"))
	return checkTUIItem{ID: fmt.Sprintf("ping-%d", index), Section: checkTUIStagePing, Status: row.Status, Cells: rdmaPingTableCells(row, true), Detail: detail}
}

func rdmaPingRawLogLine(row rdmaPingResultRow) string {
	return fmt.Sprintf("%s rdma-ping %s %s(%s) -> %s %s(%s): %s",
		row.Status, row.Source, row.SourceNIC, row.SourceIP,
		row.Destination, row.DestinationNIC, row.DestinationIP,
		firstNonEmpty(row.Result, "-"))
}

func rdmaPingItems(bundle spec.Bundle, source Target, destination Target) ([]rdmaPingItem, error) {
	maxItems := maxInt(len(source.RDMA), len(destination.RDMA))
	if maxItems == 0 {
		maxItems = len(bundle.Defaults.RDMAInterfaces)
	}
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
	output, err := runCheckCommand(opts, source, shellJoin(args))
	path := fmt.Sprintf("%s %s -> %s %s(%s)", source.Name, firstNonEmpty(item.SourceName, rdmaLabel(item.SourceIndex)), destination.Name, firstNonEmpty(item.DestinationName, rdmaLabel(item.DestinationIndex)), item.DestinationIP)
	if opts.checkTUI != nil {
		raw := fmt.Sprintf("RAW rdma-ping %s\n%s", path, firstNonEmpty(strings.TrimSpace(output), "(no stdout captured)"))
		if err != nil {
			raw += "\nCOMMAND ERROR:\n" + err.Error()
		}
		opts.checkTUI.AppendLog(checkTUIStagePing, raw)
	}
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
