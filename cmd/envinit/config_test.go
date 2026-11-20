package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"envinit/internal/configure"
)

func TestConfigReadOnlyThenSaveAndSingleFile(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	b := filepath.Join(source, "bundle.json")
	i := filepath.Join(source, "inventory.tsv")
	if err := os.WriteFile(b, []byte(`{"platform":{"os_family":"ubuntu"},"post_power_action":{"action":"none"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(i, []byte("hostname\tmgmt_ip\tOwner\n\t\tTeam\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "new-project")
	args := []string{"--project", project, "--bundle-template", b, "--inventory-template", i}
	if err := runConfig(append(args, "--check-only")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(project); !os.IsNotExist(err) {
		t.Fatal("check-only created project or lock")
	}
	if err := runConfig(append(args, "--yes")); err != nil {
		t.Fatal(err)
	}
	p, err := configure.ResolvePaths(project, "", "")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(p.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(i, []byte("hostname\tmgmt_ip\tOwner\na\t10.0.0.2\tNew\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runConfig(append(args, "--yes", "--replace", "--save", "inventory")); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(p.Bundle)
	if err != nil || string(before) != string(after) {
		t.Fatal("single inventory save changed bundle")
	}
	csv, err := os.ReadFile(p.Inventory)
	if err != nil || !strings.Contains(string(csv), "a,10.0.0.2,New") {
		t.Fatal("import missing", err)
	}
	if err := os.WriteFile(i, []byte("hostname\tmgmt_ip\tOwner\na\tinvalid\tNew\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runConfig(append(args, "--yes", "--replace")); err == nil {
		t.Fatal("--yes bypassed validation")
	}
	unchanged, err := os.ReadFile(p.Inventory)
	if err != nil || string(csv) != string(unchanged) {
		t.Fatal("invalid input changed inventory")
	}
}

func TestConfigPlainEOFIsCancellation(t *testing.T) {
	ui := configUI{plain: true, input: bufio.NewReader(strings.NewReader("")), output: os.Stdout}
	if _, err := ui.ask("Confirm", ""); err == nil {
		t.Fatal("EOF accepted as Enter")
	}
}

func TestConfigBundleOnlyCannotDropLegacyPlanning(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := configure.ResolvePaths(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p.Directory, 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"platform":{"os_family":"ubuntu"},"defaults":{"mgmt_prefix":24,"mgmt_gateway":"10.0.0.1"}}`)
	if err := os.WriteFile(p.Bundle, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Inventory, []byte("host_id,mgmt_ip\na,10.0.0.2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runConfig([]string{"--project", root, "--yes", "--save", "bundle"}); err == nil || !strings.Contains(err.Error(), "--save both") {
		t.Fatalf("legacy migration must require both: %v", err)
	}
	after, err := os.ReadFile(p.Bundle)
	if err != nil || string(after) != string(data) {
		t.Fatal("bundle was modified")
	}
}
