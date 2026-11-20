package checker

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// checkReport collects the same result updates sent to the TUI, while keeping
// raw output on disk so a long check does not retain its logs in memory.
type checkReport struct {
	mu       sync.Mutex
	dir      string
	started  time.Time
	ended    time.Time
	hosts    []string
	stages   []string
	tables   map[string]checkTUITable
	retests  []checkTUIItem
	counters []string
	logs     map[string]*os.File
	logErr   error
	status   string
	finalErr string
}

type checkReportDocument struct {
	SchemaVersion int                `json:"schema_version"`
	StartedAt     string             `json:"started_at"`
	EndedAt       string             `json:"ended_at"`
	Hosts         []string           `json:"hosts"`
	Status        string             `json:"status"`
	Error         string             `json:"error,omitempty"`
	Stages        []checkReportStage `json:"stages"`
	Counters      []string           `json:"counter_summaries,omitempty"`
	Retests       []checkReportItem  `json:"retests,omitempty"`
}

type checkReportStage struct {
	Name    string            `json:"name"`
	Headers []string          `json:"headers"`
	Items   []checkReportItem `json:"items"`
}

type checkReportItem struct {
	ID      string                `json:"id"`
	Status  string                `json:"status"`
	Cells   []string              `json:"cells"`
	Detail  string                `json:"detail,omitempty"`
	Plot    *checkTUIPlotPoint    `json:"plot,omitempty"`
	Heatmap *checkTUIHeatmapPoint `json:"heatmap,omitempty"`
}

func newCheckReport(stages, hosts []string) (*checkReport, error) {
	dir, err := os.MkdirTemp("", "envinit-check-report-")
	if err != nil {
		return nil, err
	}
	return &checkReport{dir: dir, started: time.Now(), hosts: append([]string(nil), hosts...), stages: append([]string(nil), stages...), tables: map[string]checkTUITable{}, logs: map[string]*os.File{}, status: "incomplete"}, nil
}

func defaultCheckReportPath(started time.Time) string {
	return filepath.Join(".", "check-reports", "check-"+started.Format("20060102-150405")+".zip")
}

func (r *checkReport) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, file := range r.logs {
		_ = file.Close()
	}
	_ = os.RemoveAll(r.dir)
}

func (r *checkReport) setHosts(hosts []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hosts = append([]string(nil), hosts...)
}

func (r *checkReport) setResults(stage string, headers []string, items []checkTUIItem) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !containsString(r.stages, stage) {
		r.stages = append(r.stages, stage)
	}
	r.tables[stage] = checkTUITable{Headers: append([]string(nil), headers...), Items: cloneReportItems(items)}
}

func (r *checkReport) update(item checkTUIItem) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if item.Retest {
		if item.Status != "RUNNING" && item.Status != "WAIT" {
			r.retests = append(r.retests, cloneReportItems([]checkTUIItem{item})[0])
		}
		return
	}
	table := r.tables[item.Section]
	for idx := range table.Items {
		if table.Items[idx].ID == item.ID {
			table.Items[idx] = cloneReportItems([]checkTUIItem{item})[0]
			r.tables[item.Section] = table
			return
		}
	}
	table.Items = append(table.Items, cloneReportItems([]checkTUIItem{item})[0])
	r.tables[item.Section] = table
}

func cloneReportItems(items []checkTUIItem) []checkTUIItem {
	out := make([]checkTUIItem, len(items))
	for i, item := range items {
		item.Cells = append([]string(nil), item.Cells...)
		out[i] = item
	}
	return out
}

func (r *checkReport) appendCounter(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters = append(r.counters, strings.TrimSpace(text))
	if err := r.appendLogLocked("counters", text); err != nil && r.logErr == nil {
		r.logErr = err
	}
}

func (r *checkReport) appendLog(stage, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	err := r.appendLogLocked(stage, value)
	if err != nil && r.logErr == nil {
		r.logErr = err
	}
	return err
}

func (r *checkReport) appendLogLocked(stage, value string) error {
	if stage == "" {
		stage = "preflight"
	}
	file := r.logs[stage]
	if file == nil {
		var err error
		file, err = os.OpenFile(filepath.Join(r.dir, reportLogName(stage)), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		r.logs[stage] = file
	}
	if !strings.HasSuffix(value, "\n") {
		value += "\n"
	}
	_, err := io.WriteString(file, value)
	return err
}

func reportLogName(stage string) string {
	var b strings.Builder
	for _, ch := range strings.ToLower(stage) {
		if ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' {
			b.WriteRune(ch)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-") + ".log"
}

func (r *checkReport) finish(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ended = time.Now()
	r.status = "passed"
	if err != nil {
		r.status = "failed"
		r.finalErr = err.Error()
		if errors.Is(err, errCheckCanceled) {
			r.status = "cancelled"
		}
		if logErr := r.appendLogLocked("final", "FINAL ERROR: "+err.Error()); logErr != nil && r.logErr == nil {
			r.logErr = logErr
		}
	}
}

func (r *checkReport) document() checkReportDocument {
	r.mu.Lock()
	defer r.mu.Unlock()
	doc := checkReportDocument{SchemaVersion: 1, StartedAt: r.started.Format(time.RFC3339), Hosts: append([]string(nil), r.hosts...), Status: r.status, Error: r.finalErr, Counters: append([]string(nil), r.counters...)}
	if !r.ended.IsZero() {
		doc.EndedAt = r.ended.Format(time.RFC3339)
	}
	for _, stage := range r.stages {
		table := r.tables[stage]
		entry := checkReportStage{Name: stage, Headers: append([]string(nil), table.Headers...)}
		for _, item := range table.Items {
			entry.Items = append(entry.Items, reportItem(item))
		}
		doc.Stages = append(doc.Stages, entry)
	}
	for _, item := range r.retests {
		doc.Retests = append(doc.Retests, reportItem(item))
	}
	return doc
}

func reportItem(item checkTUIItem) checkReportItem {
	result := checkReportItem{ID: item.ID, Status: item.Status, Cells: append([]string(nil), item.Cells...), Detail: reportDetail(item.Detail)}
	if item.Plot != nil {
		point := *item.Plot
		point.AlgBW = finiteReportValue(point.AlgBW)
		point.BusBW = finiteReportValue(point.BusBW)
		result.Plot = &point
	}
	if item.Heatmap != nil {
		point := *item.Heatmap
		point.MeasuredGBits = finiteReportValue(point.MeasuredGBits)
		point.BaselineGBits = finiteReportValue(point.BaselineGBits)
		point.ThresholdGBits = finiteReportValue(point.ThresholdGBits)
		result.Heatmap = &point
	}
	return result
}

func finiteReportValue(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}

func reportDetail(detail string) string {
	var lines []string
	skipOutput := false
	for _, line := range strings.Split(detail, "\n") {
		switch strings.TrimSpace(line) {
		case "CLIENT OUTPUT:", "SERVER OUTPUT:":
			skipOutput = true
			lines = append(lines, line+" see logs/")
			continue
		case "SERVER ERROR:":
			skipOutput = false
		}
		if !skipOutput {
			lines = append(lines, line)
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func (r *checkReport) export(path string) error {
	r.mu.Lock()
	logErr := r.logErr
	r.mu.Unlock()
	if logErr != nil {
		return fmt.Errorf("raw log collection was incomplete: %w", logErr)
	}
	path = strings.TrimSpace(path)
	if path == "" || filepath.Ext(path) != ".zip" {
		return fmt.Errorf("export path must end in .zip")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(absolute, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(absolute)
		}
	}()
	archive := zip.NewWriter(file)
	doc := r.document()
	jsonData, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := zipReportBytes(archive, "report.json", append(jsonData, '\n')); err != nil {
		return err
	}
	if err := zipReportBytes(archive, "report.txt", []byte(formatCheckReport(doc))); err != nil {
		return err
	}
	r.mu.Lock()
	stages := make([]string, 0, len(r.logs))
	for stage := range r.logs {
		stages = append(stages, stage)
	}
	sort.Strings(stages)
	for _, stage := range stages {
		if err := r.logs[stage].Sync(); err != nil {
			r.mu.Unlock()
			return err
		}
	}
	r.mu.Unlock()
	for _, stage := range stages {
		input, err := os.Open(filepath.Join(r.dir, reportLogName(stage)))
		if err != nil {
			return err
		}
		entry, err := archive.Create("logs/" + reportLogName(stage))
		if err == nil {
			_, err = io.Copy(entry, input)
		}
		_ = input.Close()
		if err != nil {
			return err
		}
	}
	if err := archive.Close(); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	remove = false
	return nil
}

func zipReportBytes(archive *zip.Writer, name string, data []byte) error {
	entry, err := archive.Create(name)
	if err != nil {
		return err
	}
	_, err = entry.Write(data)
	return err
}

func formatCheckReport(doc checkReportDocument) string {
	var b strings.Builder
	fmt.Fprintln(&b, "env_init Check 测试报告")
	fmt.Fprintln(&b, strings.Repeat("=", 60))
	fmt.Fprintf(&b, "开始：%s\n结束：%s\n主机：%s\n结论：%s\n", doc.StartedAt, doc.EndedAt, strings.Join(doc.Hosts, ", "), strings.ToUpper(doc.Status))
	if doc.Error != "" {
		fmt.Fprintf(&b, "错误：%s\n", doc.Error)
	}
	for _, stage := range doc.Stages {
		fmt.Fprintf(&b, "\n[%s]  原始日志：logs/%s\n", stage.Name, reportLogName(stage.Name))
		if len(stage.Headers) > 0 {
			fmt.Fprintln(&b, strings.Join(stage.Headers, " | "))
		}
		for _, item := range stage.Items {
			fmt.Fprintln(&b, strings.Join(item.Cells, " | "))
			if item.Detail != "" {
				fmt.Fprintf(&b, "  %s\n", strings.ReplaceAll(item.Detail, "\n", "\n  "))
			}
		}
	}
	if len(doc.Counters) > 0 {
		fmt.Fprintln(&b, "\n[Counter Delta]  原始日志：logs/counters.log")
		for _, summary := range doc.Counters {
			fmt.Fprintln(&b, summary)
		}
	}
	if len(doc.Retests) > 0 {
		fmt.Fprintln(&b, "\n[单项重测]  不改变原始整轮结论；原始日志：logs/retests.log")
		for _, item := range doc.Retests {
			fmt.Fprintf(&b, "%s | %s\n", item.Status, strings.Join(item.Cells, " | "))
		}
	}
	return b.String()
}
