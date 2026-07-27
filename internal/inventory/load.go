package inventory

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"envinit/internal/spec"
)

func Load(path string, sheet string) ([]spec.MachineRecord, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".xlsx":
		rows, err := loadXLSX(path, sheet)
		if err != nil {
			return nil, err
		}
		return parseRows(rows)
	case ".csv", ".txt", ".tsv":
		rows, err := loadDelimited(path)
		if err != nil {
			return nil, err
		}
		return parseRows(rows)
	default:
		return nil, fmt.Errorf("unsupported inventory format %q, use .csv/.tsv/.txt/.xlsx", ext)
	}
}

func loadDelimited(path string) ([][]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read inventory: %w", err)
	}
	delimiter := detectDelimiter(data)
	reader := csv.NewReader(bytes.NewReader(data))
	reader.Comma = delimiter
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	rows, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse delimited inventory: %w", err)
	}
	return rows, nil
}

func detectDelimiter(data []byte) rune {
	firstLine := string(data)
	if idx := strings.IndexByte(firstLine, '\n'); idx >= 0 {
		firstLine = firstLine[:idx]
	}
	candidates := []rune{',', '\t', ';'}
	best := ','
	bestCount := -1
	for _, candidate := range candidates {
		count := strings.Count(firstLine, string(candidate))
		if count > bestCount {
			best = candidate
			bestCount = count
		}
	}
	return best
}

func parseRows(rows [][]string) ([]spec.MachineRecord, error) {
	if len(rows) < 2 {
		return nil, fmt.Errorf("inventory must contain header plus at least one data row")
	}

	header := make(map[int]string)
	for idx, value := range rows[0] {
		header[idx] = canonicalHeader(value)
	}

	out := make([]spec.MachineRecord, 0, len(rows)-1)
	for line, row := range rows[1:] {
		if isBlankRow(row) {
			continue
		}
		record, err := rowToRecord(header, row)
		if err != nil {
			return nil, fmt.Errorf("inventory row %d: %w", line+2, err)
		}
		out = append(out, record)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("inventory does not contain any usable records")
	}
	return out, nil
}

func rowToRecord(header map[int]string, row []string) (spec.MachineRecord, error) {
	record := spec.MachineRecord{}

	for idx, raw := range row {
		key := header[idx]
		value := strings.TrimSpace(raw)
		switch key {
		case "host_id", "host", "node_id", "asset_tag":
			record.HostID = value
		case "hostname", "node", "machine":
			record.Hostname = value
		case "mgmt_ip", "bond_ip", "management_ip":
			record.MgmtIP = value
		case "mgmt_prefix", "bond_prefix", "management_prefix":
			record.MgmtPrefix = value
		case "mgmt_gateway", "bond_gateway", "management_gateway":
			record.MgmtGateway = value
		case "mgmt_bond_name", "bond_name":
			record.MgmtBondName = value
		case "mgmt_iface1", "mgmt_port1", "bond_iface1", "bond_port1":
			record.MgmtIface1 = value
		case "mgmt_iface2", "mgmt_port2", "bond_iface2", "bond_port2":
			record.MgmtIface2 = value
		case "mgmt_mac1", "mgmt_port1_mac", "bond_iface1_mac", "bond_port1_mac":
			record.MgmtMAC1 = value
		case "mgmt_mac2", "mgmt_port2_mac", "bond_iface2_mac", "bond_port2_mac":
			record.MgmtMAC2 = value
		case "mgmt_nameservers", "nameservers", "dns":
			record.MgmtNameserver = value
		default:
			if ok := assignRDMAField(&record, key, value); ok {
				continue
			}
		}
	}
	for len(record.RDMA) > 0 && emptyRDMARecord(record.RDMA[len(record.RDMA)-1]) {
		record.RDMA = record.RDMA[:len(record.RDMA)-1]
	}

	return record, nil
}

func emptyRDMARecord(record spec.RDMARecord) bool {
	return strings.TrimSpace(record.Name) == "" &&
		strings.TrimSpace(record.MAC) == "" &&
		strings.TrimSpace(record.IP) == "" &&
		strings.TrimSpace(record.Prefix) == "" &&
		strings.TrimSpace(record.RailID) == "" &&
		strings.TrimSpace(record.Gateway) == "" &&
		strings.TrimSpace(record.Table) == "" &&
		strings.TrimSpace(record.RouteCIDR) == ""
}

func assignRDMAField(record *spec.MachineRecord, key string, value string) bool {
	if !strings.HasPrefix(key, "rdma") {
		return false
	}

	rest := strings.TrimPrefix(key, "rdma")
	if rest == "" {
		return false
	}

	idxEnd := 0
	for idxEnd < len(rest) && rest[idxEnd] >= '0' && rest[idxEnd] <= '9' {
		idxEnd++
	}
	if idxEnd == 0 {
		return false
	}

	n, err := strconv.Atoi(rest[:idxEnd])
	if err != nil || n < 1 {
		return false
	}
	for len(record.RDMA) < n {
		record.RDMA = append(record.RDMA, spec.RDMARecord{})
	}

	field := strings.TrimPrefix(rest[idxEnd:], "_")
	item := &record.RDMA[n-1]
	switch field {
	case "ip":
		item.IP = value
	case "mac":
		item.MAC = value
	case "prefix":
		item.Prefix = value
	case "rail", "rail_id":
		item.RailID = value
	case "gateway":
		item.Gateway = value
	case "iface", "name", "dev":
		item.Name = value
	case "table":
		item.Table = value
	case "route_cidr", "cidr", "network":
		item.RouteCIDR = value
	default:
		return false
	}
	return true
}

func canonicalHeader(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	replacer := strings.NewReplacer("-", "_", " ", "_", "/", "_", ".", "_", "(", "", ")", "")
	raw = replacer.Replace(raw)
	raw = strings.Trim(raw, "_")
	return raw
}

func isBlankRow(row []string) bool {
	for _, item := range row {
		if strings.TrimSpace(item) != "" {
			return false
		}
	}
	return true
}

type workbookXML struct {
	Sheets []sheetXML `xml:"sheets>sheet"`
}

type sheetXML struct {
	Name string `xml:"name,attr"`
	ID   string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
}

type relationshipsXML struct {
	Items []relationshipXML `xml:"Relationship"`
}

type relationshipXML struct {
	ID     string `xml:"Id,attr"`
	Target string `xml:"Target,attr"`
}

type sharedStringsXML struct {
	Items []sharedStringItem `xml:"si"`
}

type sharedStringItem struct {
	Text string      `xml:"t"`
	Runs []sharedRun `xml:"r"`
}

type sharedRun struct {
	Text string `xml:"t"`
}

type worksheetXML struct {
	Rows []rowXML `xml:"sheetData>row"`
}

type rowXML struct {
	Cells []cellXML `xml:"c"`
}

type cellXML struct {
	Ref       string       `xml:"r,attr"`
	Type      string       `xml:"t,attr"`
	Value     string       `xml:"v"`
	InlineStr inlineString `xml:"is"`
}

type inlineString struct {
	Text string      `xml:"t"`
	Runs []sharedRun `xml:"r"`
}

func loadXLSX(path string, sheetName string) ([][]string, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open xlsx: %w", err)
	}
	defer reader.Close()

	workbookData, err := readZipFile(reader.File, "xl/workbook.xml")
	if err != nil {
		return nil, err
	}
	var workbook workbookXML
	if err := xml.Unmarshal(workbookData, &workbook); err != nil {
		return nil, fmt.Errorf("parse workbook xml: %w", err)
	}
	if len(workbook.Sheets) == 0 {
		return nil, fmt.Errorf("xlsx contains no sheets")
	}

	relsData, err := readZipFile(reader.File, "xl/_rels/workbook.xml.rels")
	if err != nil {
		return nil, err
	}
	var rels relationshipsXML
	if err := xml.Unmarshal(relsData, &rels); err != nil {
		return nil, fmt.Errorf("parse workbook rels: %w", err)
	}

	relMap := make(map[string]string, len(rels.Items))
	for _, item := range rels.Items {
		relMap[item.ID] = workbookRelationshipTarget(item.Target)
	}

	targetSheet := workbook.Sheets[0]
	if strings.TrimSpace(sheetName) != "" {
		found := false
		for _, item := range workbook.Sheets {
			if strings.EqualFold(strings.TrimSpace(item.Name), strings.TrimSpace(sheetName)) {
				targetSheet = item
				found = true
				break
			}
		}
		if !found {
			available := make([]string, 0, len(workbook.Sheets))
			for _, item := range workbook.Sheets {
				available = append(available, item.Name)
			}
			sort.Strings(available)
			return nil, fmt.Errorf("sheet %q not found, available: %s", sheetName, strings.Join(available, ", "))
		}
	}

	sheetPath := relMap[targetSheet.ID]
	if sheetPath == "" {
		return nil, fmt.Errorf("xlsx missing relationship target for sheet %q", targetSheet.Name)
	}

	sharedStrings, err := loadSharedStrings(reader.File)
	if err != nil {
		return nil, err
	}

	sheetData, err := readZipFile(reader.File, sheetPath)
	if err != nil {
		return nil, err
	}
	var worksheet worksheetXML
	if err := xml.Unmarshal(sheetData, &worksheet); err != nil {
		return nil, fmt.Errorf("parse worksheet %q: %w", targetSheet.Name, err)
	}

	rows := make([][]string, 0, len(worksheet.Rows))
	for _, row := range worksheet.Rows {
		out := make([]string, 0, len(row.Cells))
		for _, cell := range row.Cells {
			index := columnIndex(cell.Ref)
			for len(out) <= index {
				out = append(out, "")
			}
			out[index] = resolveCellValue(cell, sharedStrings)
		}
		rows = append(rows, out)
	}
	return rows, nil
}

func workbookRelationshipTarget(target string) string {
	target = strings.TrimSpace(target)
	if strings.HasPrefix(target, "/") {
		return strings.TrimPrefix(path.Clean(target), "/")
	}
	return path.Clean(path.Join("xl", target))
}

func loadSharedStrings(files []*zip.File) ([]string, error) {
	data, err := readZipFile(files, "xl/sharedStrings.xml")
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, nil
		}
		return nil, err
	}
	var doc sharedStringsXML
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse shared strings: %w", err)
	}
	out := make([]string, 0, len(doc.Items))
	for _, item := range doc.Items {
		if item.Text != "" {
			out = append(out, item.Text)
			continue
		}
		var builder strings.Builder
		for _, run := range item.Runs {
			builder.WriteString(run.Text)
		}
		out = append(out, builder.String())
	}
	return out, nil
}

func resolveCellValue(cell cellXML, shared []string) string {
	switch cell.Type {
	case "s":
		index, err := strconv.Atoi(strings.TrimSpace(cell.Value))
		if err == nil && index >= 0 && index < len(shared) {
			return shared[index]
		}
	case "inlineStr":
		if cell.InlineStr.Text != "" {
			return cell.InlineStr.Text
		}
		var builder strings.Builder
		for _, run := range cell.InlineStr.Runs {
			builder.WriteString(run.Text)
		}
		return builder.String()
	default:
		return strings.TrimSpace(cell.Value)
	}
	return strings.TrimSpace(cell.Value)
}

func columnIndex(ref string) int {
	col := 0
	for _, r := range ref {
		if r >= 'A' && r <= 'Z' {
			col = col*26 + int(r-'A'+1)
			continue
		}
		if r >= 'a' && r <= 'z' {
			col = col*26 + int(r-'a'+1)
			continue
		}
		break
	}
	if col == 0 {
		return 0
	}
	return col - 1
}

func readZipFile(files []*zip.File, target string) ([]byte, error) {
	for _, file := range files {
		if file.Name != target {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", target, err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", target, err)
		}
		return data, nil
	}
	return nil, fmt.Errorf("zip entry %s not found", target)
}
