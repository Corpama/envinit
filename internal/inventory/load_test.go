package inventory

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCSV(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "inventory.csv")
	content := "host_id,hostname,mgmt_ip,mgmt_prefix,mgmt_gateway,mgmt_mac1,mgmt_mac2,rdma1_ip,rdma1_mac,rdma2_ip,rdma3_ip,rdma4_ip\n" +
		"xpu11,xpu11,10.101.9.11,26,10.101.9.1,AA:BB:CC:DD:EE:01,aa-bb-cc-dd-ee-02,11.1.1.11,AA:BB:CC:DD:EE:11,11.1.2.11,11.1.3.11,11.1.4.11\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write csv: %v", err)
	}

	rows, err := Load(path, "")
	if err != nil {
		t.Fatalf("load csv: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].MgmtIP != "10.101.9.11" {
		t.Fatalf("unexpected mgmt ip: %s", rows[0].MgmtIP)
	}
	if rows[0].MgmtMAC1 != "AA:BB:CC:DD:EE:01" {
		t.Fatalf("unexpected mgmt mac1: %s", rows[0].MgmtMAC1)
	}
	if rows[0].RDMA[0].MAC != "AA:BB:CC:DD:EE:11" {
		t.Fatalf("unexpected rdma1 mac: %s", rows[0].RDMA[0].MAC)
	}
	if rows[0].RDMA[3].IP != "11.1.4.11" {
		t.Fatalf("unexpected rdma4 ip: %s", rows[0].RDMA[3].IP)
	}
}

func TestLoadXLSX(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "inventory.xlsx")
	if err := writeMinimalXLSX(path); err != nil {
		t.Fatalf("write xlsx: %v", err)
	}

	rows, err := Load(path, "Sheet1")
	if err != nil {
		t.Fatalf("load xlsx: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].HostID != "xpu12" {
		t.Fatalf("unexpected host id: %s", rows[0].HostID)
	}
	if rows[0].RDMA[1].IP != "11.1.2.12" {
		t.Fatalf("unexpected rdma2 ip: %s", rows[0].RDMA[1].IP)
	}
}

func TestLoadXLSXWithAbsoluteRelationshipTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "inventory.xlsx")
	if err := writeMinimalXLSXWithSheetTarget(path, "/xl/worksheets/sheet1.xml"); err != nil {
		t.Fatalf("write xlsx: %v", err)
	}

	rows, err := Load(path, "Sheet1")
	if err != nil {
		t.Fatalf("load xlsx: %v", err)
	}
	if rows[0].HostID != "xpu12" {
		t.Fatalf("unexpected host id: %s", rows[0].HostID)
	}
}

func writeMinimalXLSX(path string) error {
	return writeMinimalXLSXWithSheetTarget(path, "worksheets/sheet1.xml")
}

func writeMinimalXLSXWithSheetTarget(path string, sheetTarget string) error {
	var buf bytes.Buffer
	zipWriter := zip.NewWriter(&buf)

	files := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
  <Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
  <Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/>
</Types>`,
		"_rels/.rels": `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`,
		"xl/workbook.xml": `<?xml version="1.0" encoding="UTF-8"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <sheets>
    <sheet name="Sheet1" sheetId="1" r:id="rId1"/>
  </sheets>
</workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="` + sheetTarget + `"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/>
</Relationships>`,
		"xl/sharedStrings.xml": `<?xml version="1.0" encoding="UTF-8"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="18" uniqueCount="18">
  <si><t>host_id</t></si>
  <si><t>hostname</t></si>
  <si><t>mgmt_ip</t></si>
  <si><t>mgmt_prefix</t></si>
  <si><t>mgmt_gateway</t></si>
  <si><t>rdma1_ip</t></si>
  <si><t>rdma2_ip</t></si>
  <si><t>rdma3_ip</t></si>
  <si><t>rdma4_ip</t></si>
  <si><t>xpu12</t></si>
  <si><t>xpu12</t></si>
  <si><t>10.101.9.12</t></si>
  <si><t>26</t></si>
  <si><t>10.101.9.1</t></si>
  <si><t>11.1.1.12</t></si>
  <si><t>11.1.2.12</t></si>
  <si><t>11.1.3.12</t></si>
  <si><t>11.1.4.12</t></si>
</sst>`,
		"xl/worksheets/sheet1.xml": `<?xml version="1.0" encoding="UTF-8"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <sheetData>
    <row r="1">
      <c r="A1" t="s"><v>0</v></c>
      <c r="B1" t="s"><v>1</v></c>
      <c r="C1" t="s"><v>2</v></c>
      <c r="D1" t="s"><v>3</v></c>
      <c r="E1" t="s"><v>4</v></c>
      <c r="F1" t="s"><v>5</v></c>
      <c r="G1" t="s"><v>6</v></c>
      <c r="H1" t="s"><v>7</v></c>
      <c r="I1" t="s"><v>8</v></c>
    </row>
    <row r="2">
      <c r="A2" t="s"><v>9</v></c>
      <c r="B2" t="s"><v>10</v></c>
      <c r="C2" t="s"><v>11</v></c>
      <c r="D2" t="s"><v>12</v></c>
      <c r="E2" t="s"><v>13</v></c>
      <c r="F2" t="s"><v>14</v></c>
      <c r="G2" t="s"><v>15</v></c>
      <c r="H2" t="s"><v>16</v></c>
      <c r="I2" t="s"><v>17</v></c>
    </row>
  </sheetData>
</worksheet>`,
	}

	for name, content := range files {
		w, err := zipWriter.Create(name)
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte(content)); err != nil {
			return err
		}
	}
	if err := zipWriter.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
