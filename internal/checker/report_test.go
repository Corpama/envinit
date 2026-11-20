package checker

import (
	"archive/zip"
	"encoding/json"
	"io"
	"math"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCheckReportZIPKeepsOriginalVerdictAndSeparateLogs(t *testing.T) {
	report, err := newCheckReport([]string{"Preflight", checkTUIStagePing, "BW RDMA-CM"}, []string{"node-1", "node-2"})
	if err != nil {
		t.Fatal(err)
	}
	defer report.close()
	if err := report.appendLog("Preflight", "INFO ssh preflight: node-2 reachable"); err != nil {
		t.Fatal(err)
	}
	report.setResults("BW RDMA-CM", []string{"STATUS", "BANDWIDTH"}, []checkTUIItem{{ID: "bw-1", Section: "BW RDMA-CM", Status: "WAIT", Cells: []string{"WAIT", "-"}}})
	report.update(checkTUIItem{
		ID: "bw-1", Section: "BW RDMA-CM", Status: "FAIL", Cells: []string{"FAIL", "312.40 Gbps"},
		Detail:  "Threshold: 360 Gbps\n\nCLIENT OUTPUT:\nraw client output\n\nSERVER ERROR:\n-\n\nSERVER OUTPUT:\nraw server output",
		Heatmap: &checkTUIHeatmapPoint{MeasuredGBits: math.NaN()},
	})
	if err := report.appendLog("BW RDMA-CM", "raw client output\nraw server output"); err != nil {
		t.Fatal(err)
	}
	report.appendCounter("NIC counter delta summary:\nFAIL node-2 rx_crc_errors +3")
	report.finish(errCheckCanceled)
	report.update(checkTUIItem{ID: "bw-1", Section: "BW RDMA-CM", Status: "PASS", Cells: []string{"PASS", "381.70 Gbps"}, Retest: true})
	if err := report.appendLog("retests", "RETEST PASS bw-1 381.70 Gbps"); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "check.zip")
	if err := report.export(archivePath); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	entries := map[string]string{}
	for _, file := range archive.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		entries[file.Name] = string(data)
	}
	for _, name := range []string{"report.json", "report.txt", "logs/preflight.log", "logs/bw-rdma-cm.log", "logs/counters.log", "logs/retests.log", "logs/final.log"} {
		if _, ok := entries[name]; !ok {
			t.Errorf("missing %s", name)
		}
	}
	var doc checkReportDocument
	if err := json.Unmarshal([]byte(entries["report.json"]), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Status != "cancelled" || len(doc.Stages) != 3 || len(doc.Stages[2].Items) != 1 || doc.Stages[2].Items[0].Status != "FAIL" {
		t.Fatalf("original verdict changed: %#v", doc)
	}
	if len(doc.Retests) != 1 || doc.Retests[0].Status != "PASS" {
		t.Fatalf("retest missing: %#v", doc.Retests)
	}
	if strings.Contains(entries["report.txt"], "raw client output") || !strings.Contains(entries["logs/bw-rdma-cm.log"], "raw client output") {
		t.Fatal("raw output was not separated from the report")
	}
	if err := report.export(archivePath); err == nil {
		t.Fatal("export unexpectedly overwrote an existing archive")
	}
}

func TestCheckTUIExportKeyAfterFinish(t *testing.T) {
	model := newCheckTUIModel()
	model.finished = true
	model.exportPath = filepath.Join(t.TempDir(), "结果.zip")
	called := false
	model.exportArchive = func(path string) error {
		called = true
		if path != model.exportPath {
			t.Fatalf("export path = %q", path)
		}
		return nil
	}
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	model = updated.(checkTUIModel)
	if !model.exportEditing {
		t.Fatal("e did not open export path prompt")
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(checkTUIModel)
	if !called || model.exportEditing || !strings.Contains(model.notice, "Exported") {
		t.Fatalf("export was not completed: %#v", model)
	}
}
