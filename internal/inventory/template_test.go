package inventory

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTemplateStrictStructureAndAliasMerge(t *testing.T) {
	for _, text := range []string{"hostname,mgmt_prfix\na,24\n", "hostname,rdma65_ip\na,\n", "hostname,mgmt_ip\na\n", "hostname,host_id\na,b\n", "owner\nteam\n"} {
		if _, err := ParseTemplate([]byte(text)); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
	table, err := ParseTemplate([]byte("host_id,hostname,rdma1_iface,Owner\na,a,, Team \n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(table.Header, ",") != "hostname,rdma1_name,Owner" || table.Rows[0][2] != " Team " {
		t.Fatalf("unexpected normalization: %+v", table)
	}
	records, err := table.Records()
	if err != nil || len(records[0].RDMA) != 1 {
		t.Fatal("empty RDMA slot lost", err)
	}
}

func TestTemplateXLSXSheetAndTrailingCells(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inventory.xlsx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	files := map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="First" r:id="r1"/><sheet name="Second" r:id="r2"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="r1" Target="worksheets/sheet1.xml"/><Relationship Id="r2" Target="worksheets/sheet2.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row><c r="A1" t="inlineStr"><is><t>hostname</t></is></c><c r="B1" t="inlineStr"><is><t>mgmt_ip</t></is></c></row><row><c r="A2" t="inlineStr"><is><t>first</t></is></c></row></sheetData></worksheet>`,
		"xl/worksheets/sheet2.xml":   `<worksheet><sheetData><row><c r="A1" t="inlineStr"><is><t>hostname</t></is></c></row><row><c r="A2" t="inlineStr"><is><t>second</t></is></c></row></sheetData></worksheet>`,
	}
	for name, data := range files {
		entry, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	first, err := LoadTemplate(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if string(first.CSV()) != "hostname,mgmt_ip\nfirst,\n" {
		t.Fatalf("%s", first.CSV())
	}
	second, err := LoadTemplate(path, "Second")
	if err != nil {
		t.Fatal(err)
	}
	if second.Rows[0][0] != "second" {
		t.Fatal("wrong sheet")
	}
	if _, err := LoadTemplate(path, "Missing"); err == nil {
		t.Fatal("missing sheet accepted")
	}
}
