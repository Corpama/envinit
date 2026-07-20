package checker

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
)

func printBandwidthResultTable(output io.Writer, results []Result) {
	if len(results) == 0 {
		fmt.Fprintln(output, "WARN bandwidth results: no completed bandwidth streams")
		return
	}
	rows := make([]bandwidthResultRow, 0, len(results))
	degradedCount := 0
	for _, result := range results {
		status := bandwidthResultStatus(result)
		if result.Degraded {
			degradedCount++
		}
		rows = append(rows, bandwidthResultRowFromResult(result, status))
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left := rows[i]
		right := rows[j]
		if left.Failure != right.Failure {
			return left.Failure
		}
		for _, pair := range [][2]string{
			{left.Client, right.Client},
			{left.Server, right.Server},
			{left.ClientNIC, right.ClientNIC},
			{left.ServerNIC, right.ServerNIC},
			{left.ClientIP, right.ClientIP},
			{left.ServerIP, right.ServerIP},
			{left.ClientDev, right.ClientDev},
			{left.ServerDev, right.ServerDev},
			{left.Port, right.Port},
			{left.ClientXP, right.ClientXP},
			{left.ServerXP, right.ServerXP},
			{left.ClientTopo, right.ClientTopo},
			{left.ServerTopo, right.ServerTopo},
		} {
			if pair[0] != pair[1] {
				return pair[0] < pair[1]
			}
		}
		return false
	})

	headers := bandwidthResultHeaders()
	widths := make([]int, len(headers))
	for idx, header := range headers {
		widths[idx] = len(header)
	}
	tableRows := make([][]string, 0, len(rows))
	for _, row := range rows {
		cells := bandwidthRowCells(row)
		for idx, cell := range cells {
			if len(cell) > widths[idx] {
				widths[idx] = len(cell)
			}
		}
		tableRows = append(tableRows, cells)
	}

	fmt.Fprintln(output, "Bandwidth result summary:")
	fmt.Fprintln(output, formatTableLine(headers, widths))
	fmt.Fprintln(output, formatTableSeparator(widths))
	for idx, cells := range tableRows {
		line := formatTableLine(cells, widths)
		if rows[idx].Failure {
			line = redText(line)
		}
		fmt.Fprintln(output, line)
	}
	if degradedCount > 0 {
		fmt.Fprintf(output, "WARN bandwidth topology: %d completed stream(s) used non-PIX XPU/NIC mappings; bandwidth may be limited by the PCIe/NUMA path\n", degradedCount)
	}
}

func printBandwidthFailureDetails(output io.Writer, results []Result) {
	printedHeader := false
	for _, result := range results {
		if result.ClientError == "" && result.ServerError == "" {
			continue
		}
		if !printedHeader {
			fmt.Fprintln(output, "Bandwidth failure details:")
			printedHeader = true
		}
		fmt.Fprintf(output, "FAIL %s -> %s %s port=%d\n", result.Client.Name, result.Server.Name, resultLabel(result), result.Port)
		printBandwidthSideDetails(output, "CLIENT", result.Client.Name, result.ClientError, result.ClientOutput)
		printBandwidthSideDetails(output, "SERVER", result.Server.Name, result.ServerError, result.ServerOutput)
	}
}

func printBandwidthSideDetails(output io.Writer, side, host, errorText, rawOutput string) {
	errorText = strings.TrimSpace(errorText)
	rawOutput = strings.TrimSpace(rawOutput)
	if errorText == "" {
		errorText = "no command error reported"
	}
	if rawOutput == "" {
		rawOutput = "(no output captured)"
	}
	fmt.Fprintf(output, "  %s %s error: %s\n", side, host, compactTableCell(errorText))
	fmt.Fprintf(output, "  %s %s output:\n", side, host)
	for _, line := range strings.Split(rawOutput, "\n") {
		fmt.Fprintf(output, "    %s\n", line)
	}
}

func bandwidthResultStatus(result Result) string {
	if !result.Passed || result.ClientError != "" || result.ServerError != "" {
		return "FAIL"
	}
	if result.Degraded {
		return "WARN"
	}
	return "PASS"
}

func bandwidthResultRowFromResult(result Result, status string) bandwidthResultRow {
	row := bandwidthResultRow{
		Status: status, Client: result.Client.Name, Server: result.Server.Name,
		ClientNIC: rdmaNICLabel(result.Client, result.ClientRDMAIndex), ServerNIC: rdmaNICLabel(result.Server, result.ServerRDMAIndex),
		ClientIP: rdmaIPLabel(result.Client, result.ClientRDMAIndex), ServerIP: bandwidthPeerAddress(result.Server, checkStream{ServerRDMAIndex: result.ServerRDMAIndex}),
		ClientDev: result.ClientGroup.IBDevice, ServerDev: result.ServerGroup.IBDevice,
		Port: "-", ClientXP: "-", ServerXP: "-",
		ClientTopo: topologyResultLabel(result.ClientTopology), ServerTopo: topologyResultLabel(result.ServerTopology),
		Bandwidth: "unknown", Failure: status == "FAIL", Degraded: result.Degraded,
	}
	if result.Port > 0 {
		row.Port = strconv.Itoa(result.Port)
	}
	if strings.TrimSpace(result.ClientXP) != "" {
		row.ClientXP = strings.TrimSpace(result.ClientXP)
	}
	if strings.TrimSpace(result.ServerXP) != "" {
		row.ServerXP = strings.TrimSpace(result.ServerXP)
	}
	if !math.IsNaN(result.GBits) {
		row.Bandwidth = fmt.Sprintf("%.2f Gbps", result.GBits)
	}
	if status == "FAIL" && (result.ClientError != "" || result.ServerError != "") {
		row.Bandwidth = "error (details below)"
	}
	return row
}

func bandwidthResultHeaders() []string {
	return []string{"STATUS", "CLIENT", "SERVER", "CLIENT_NIC", "SERVER_NIC", "CLIENT_IP", "SERVER_IP", "CLIENT_DEV", "SERVER_DEV", "PORT", "CLIENT_XPU", "SERVER_XPU", "CLIENT_TOPO", "SERVER_TOPO", "BANDWIDTH"}
}

func bandwidthResultCells(result Result, status string, _ bool) []string {
	row := bandwidthResultRowFromResult(result, status)
	return bandwidthRowCells(row)
}

func bandwidthRowCells(row bandwidthResultRow) []string {
	return []string{row.Status, row.Client, row.Server, row.ClientNIC, row.ServerNIC, row.ClientIP, row.ServerIP, row.ClientDev, row.ServerDev, row.Port, row.ClientXP, row.ServerXP, row.ClientTopo, row.ServerTopo, row.Bandwidth}
}

func topologyResultLabel(link string) string {
	link = strings.ToUpper(strings.TrimSpace(link))
	if link == "" {
		return "-"
	}
	if topologyLinkDegraded(link) {
		return link + "(DEGRADED)"
	}
	return link
}

func rdmaLabel(index int) string {
	if index < 0 {
		return "-"
	}
	return fmt.Sprintf("rdma%d", index+1)
}

func rdmaNICLabel(target Target, index int) string {
	if index >= 0 && index < len(target.RDMA) {
		if name := strings.TrimSpace(target.RDMA[index].Name); name != "" {
			return name
		}
	}
	return rdmaLabel(index)
}

func rdmaIPLabel(target Target, index int) string {
	if index >= 0 && index < len(target.RDMA) {
		if address := strings.TrimSpace(target.RDMA[index].IP); address != "" {
			return address
		}
	}
	if address := strings.TrimSpace(target.Address); address != "" {
		return address
	}
	return "-"
}
