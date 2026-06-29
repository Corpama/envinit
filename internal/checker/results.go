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
	for _, result := range results {
		status := "PASS"
		if !result.Passed {
			status = "FAIL"
		}
		row := bandwidthResultRow{
			Status:     status,
			Client:     result.Client.Name,
			Server:     result.Server.Name,
			ClientRDMA: rdmaLabel(result.ClientRDMAIndex),
			ServerRDMA: rdmaLabel(result.ServerRDMAIndex),
			ClientDev:  result.ClientGroup.IBDevice,
			ServerDev:  result.ServerGroup.IBDevice,
			Port:       "-",
			ClientXP:   "-",
			ServerXP:   "-",
			Bandwidth:  "unknown",
			Failure:    !result.Passed,
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
		rows = append(rows, row)
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
			{left.ClientRDMA, right.ClientRDMA},
			{left.ServerRDMA, right.ServerRDMA},
			{left.ClientDev, right.ClientDev},
			{left.ServerDev, right.ServerDev},
			{left.Port, right.Port},
			{left.ClientXP, right.ClientXP},
			{left.ServerXP, right.ServerXP},
		} {
			if pair[0] != pair[1] {
				return pair[0] < pair[1]
			}
		}
		return false
	})

	headers := []string{"STATUS", "CLIENT", "SERVER", "CLIENT_RDMA", "SERVER_RDMA", "CLIENT_DEV", "SERVER_DEV", "PORT", "CLIENT_XPU", "SERVER_XPU", "BANDWIDTH"}
	widths := make([]int, len(headers))
	for idx, header := range headers {
		widths[idx] = len(header)
	}
	tableRows := make([][]string, 0, len(rows))
	for _, row := range rows {
		cells := []string{
			row.Status,
			row.Client,
			row.Server,
			row.ClientRDMA,
			row.ServerRDMA,
			row.ClientDev,
			row.ServerDev,
			row.Port,
			row.ClientXP,
			row.ServerXP,
			row.Bandwidth,
		}
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
}

func rdmaLabel(index int) string {
	if index < 0 {
		return "-"
	}
	return fmt.Sprintf("rdma%d", index+1)
}
