package checker

import (
	"fmt"
	"io"
	"strings"
	"sync"
)

type liveResultTable struct {
	mu        sync.Mutex
	output    io.Writer
	enabled   bool
	title     string
	headers   []string
	rows      [][]string
	lineCount int
	rendered  bool
}

func newLiveResultTable(output io.Writer, enabled bool, title string, headers []string, rows [][]string) *liveResultTable {
	table := &liveResultTable{
		output:  output,
		enabled: enabled,
		title:   title,
		headers: append([]string(nil), headers...),
		rows:    cloneTableRows(rows),
	}
	if enabled {
		table.renderLocked()
	}
	return table
}

func (t *liveResultTable) Update(index int, cells []string) {
	t.UpdateRows(map[int][]string{index: cells})
}

func (t *liveResultTable) UpdateRows(updates map[int][]string) {
	if t == nil || !t.enabled {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	changed := false
	for index, cells := range updates {
		if index < 0 || index >= len(t.rows) {
			continue
		}
		t.rows[index] = append([]string(nil), cells...)
		changed = true
	}
	if changed {
		t.renderLocked()
	}
}

func (t *liveResultTable) renderLocked() {
	if t.rendered {
		if t.lineCount > 0 {
			fmt.Fprintf(t.output, "\033[%dA", t.lineCount)
		}
		fmt.Fprint(t.output, "\r\033[J")
	}
	widths := make([]int, len(t.headers))
	for idx, header := range t.headers {
		widths[idx] = len(header)
	}
	for _, row := range t.rows {
		for idx, cell := range row {
			if idx < len(widths) && len(cell) > widths[idx] {
				widths[idx] = len(cell)
			}
		}
	}
	fmt.Fprintln(t.output, t.title)
	fmt.Fprintln(t.output, formatTableLine(t.headers, widths))
	fmt.Fprintln(t.output, formatTableSeparator(widths))
	for _, row := range t.rows {
		line := formatTableLine(row, widths)
		if len(row) > 0 && row[0] == "FAIL" {
			line = redText(line)
		}
		fmt.Fprintln(t.output, line)
	}
	t.lineCount = len(t.rows) + 3
	t.rendered = true
}

func cloneTableRows(rows [][]string) [][]string {
	cloned := make([][]string, len(rows))
	for idx := range rows {
		cloned[idx] = append([]string(nil), rows[idx]...)
	}
	return cloned
}

func compactLiveResult(value string, max int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if max <= 3 || len(value) <= max {
		return value
	}
	return value[:max-3] + "..."
}
