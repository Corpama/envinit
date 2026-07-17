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
		status := "PASS"
		if !result.Passed {
			status = "FAIL"
		} else if result.Degraded {
			status = "WARN"
		}
		if result.Degraded {
			degradedCount++
		}
		row := bandwidthResultRow{
			Status:     status,
			Client:     result.Client.Name,
			Server:     result.Server.Name,
			ClientNIC:  rdmaNICLabel(result.Client, result.ClientRDMAIndex),
			ServerNIC:  rdmaNICLabel(result.Server, result.ServerRDMAIndex),
			ClientIP:   rdmaIPLabel(result.Client, result.ClientRDMAIndex),
			ServerIP:   bandwidthPeerAddress(result.Server, checkStream{ServerRDMAIndex: result.ServerRDMAIndex}),
			ClientDev:  result.ClientGroup.IBDevice,
			ServerDev:  result.ServerGroup.IBDevice,
			Port:       "-",
			ClientXP:   "-",
			ServerXP:   "-",
			ClientTopo: topologyResultLabel(result.ClientTopology),
			ServerTopo: topologyResultLabel(result.ServerTopology),
			Bandwidth:  "unknown",
			Failure:    !result.Passed,
			Degraded:   result.Degraded,
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

	headers := []string{"STATUS", "CLIENT", "SERVER", "CLIENT_NIC", "SERVER_NIC", "CLIENT_IP", "SERVER_IP", "CLIENT_DEV", "SERVER_DEV", "PORT", "CLIENT_XPU", "SERVER_XPU", "CLIENT_TOPO", "SERVER_TOPO", "BANDWIDTH"}
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
			row.ClientNIC,
			row.ServerNIC,
			row.ClientIP,
			row.ServerIP,
			row.ClientDev,
			row.ServerDev,
			row.Port,
			row.ClientXP,
			row.ServerXP,
			row.ClientTopo,
			row.ServerTopo,
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
	if degradedCount > 0 {
		fmt.Fprintf(output, "WARN bandwidth topology: %d completed stream(s) used non-PIX XPU/NIC mappings; bandwidth may be limited by the PCIe/NUMA path\n", degradedCount)
	}
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
