package inventory

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"envinit/internal/spec"
)

// TemplateTable retains metadata and empty planning slots. Deployment loaders
// intentionally remain stricter: a template is not a deployable inventory.
type TemplateTable struct {
	Header   []string
	Rows     [][]string
	Warnings []string
}

func LoadTemplate(path, sheet string) (*TemplateTable, error) {
	if strings.EqualFold(filepath.Ext(path), ".xlsx") {
		rows, err := loadXLSX(path, sheet)
		if err != nil {
			return nil, err
		}
		// XLSX omits trailing empty cells, unlike a delimited record.
		if len(rows) > 0 {
			for i := 1; i < len(rows); i++ {
				for len(rows[i]) < len(rows[0]) {
					rows[i] = append(rows[i], "")
				}
			}
		}
		return templateRows(rows)
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv", ".tsv", ".txt":
	default:
		return nil, fmt.Errorf("unsupported inventory format %q", filepath.Ext(path))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseTemplate(data)
}

func ParseTemplate(data []byte) (*TemplateTable, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = detectDelimiter(data)
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("inventory: %w", err)
	}
	return templateRows(rows)
}

func templateRows(rows [][]string) (*TemplateTable, error) {
	if len(rows) == 0 {
		return nil, fmt.Errorf("inventory is empty")
	}
	if err := validatePlanningHeaders(rows[0]); err != nil {
		return nil, err
	}
	t := &TemplateTable{}
	indices := map[string]int{}
	mapping := make([]int, len(rows[0]))
	for i, raw := range rows[0] {
		name := PlanningColumnName(raw)
		if name == "host_id" {
			name = "hostname"
		}
		if name != raw {
			t.Warnings = append(t.Warnings, fmt.Sprintf("inventory column %s -> %s", raw, name))
		}
		idx, ok := indices[name]
		if !ok {
			idx = len(t.Header)
			indices[name] = idx
			t.Header = append(t.Header, name)
		}
		mapping[i] = idx
	}
	if _, ok := indices["hostname"]; !ok {
		return nil, fmt.Errorf("inventory requires a hostname column")
	}
	for line, source := range rows[1:] {
		if len(source) != len(rows[0]) {
			return nil, fmt.Errorf("inventory row %d: expected %d columns, got %d", line+2, len(rows[0]), len(source))
		}
		row := make([]string, len(t.Header))
		for i, value := range source {
			idx := mapping[i]
			name := t.Header[idx]
			if knownPlanningColumn(name) {
				value = strings.TrimSpace(value)
			}
			if row[idx] != "" && value != "" && row[idx] != value {
				return nil, fmt.Errorf("inventory row %d: conflicting values for %s", line+2, name)
			}
			if value != "" {
				row[idx] = value
			}
		}
		t.Rows = append(t.Rows, row)
	}
	return t, nil
}

func knownPlanningColumn(name string) bool {
	return name == "hostname" || strings.HasPrefix(name, "mgmt_") || strings.HasPrefix(name, "rdma")
}

func (t *TemplateTable) CSV() []byte {
	var out bytes.Buffer
	w := csv.NewWriter(&out)
	_ = w.Write(t.Header)
	_ = w.WriteAll(t.Rows)
	w.Flush()
	return out.Bytes()
}

func (t *TemplateTable) Set(row int, column, value string) {
	idx := -1
	for i, h := range t.Header {
		if h == column {
			idx = i
			break
		}
	}
	if idx < 0 {
		idx = len(t.Header)
		t.Header = append(t.Header, column)
		for i := range t.Rows {
			t.Rows[i] = append(t.Rows[i], "")
		}
	}
	t.Rows[row][idx] = value
}

func (t *TemplateTable) Records() ([]spec.MachineRecord, error) {
	header := map[int]string{}
	slots := 0
	for i, h := range t.Header {
		header[i] = h
		if strings.HasPrefix(h, "rdma") {
			n, _ := strconv.Atoi(strings.SplitN(strings.TrimPrefix(h, "rdma"), "_", 2)[0])
			if n > slots {
				slots = n
			}
		}
	}
	var records []spec.MachineRecord
	for i, row := range t.Rows {
		r, err := rowToRecordMode(header, row, true)
		if err != nil {
			return nil, fmt.Errorf("inventory row %d: %w", i+2, err)
		}
		r.FromInventory = true
		for len(r.RDMA) < slots {
			r.RDMA = append(r.RDMA, spec.RDMARecord{})
		}
		records = append(records, r)
	}
	return records, nil
}
