package configure

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"

	"envinit/internal/inventory"
	"envinit/internal/spec"
)

func TestTemplateBlankPlanningAndRoundTrip(t *testing.T) {
	table, err := inventory.ParseTemplate([]byte("hostname,mgmt_mode,mgmt_target_name,mgmt_ip,mgmt_prefix,mgmt_gateway,rdma1_ip,Owner\n,single,eth0,,,,,  Project Team  \n"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := PrepareTemplate([]byte(testBundle), table)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Pending) < 7 || !strings.Contains(string(result.InventoryCSV), "  Project Team  ") {
		t.Fatalf("missing blanks or metadata: %s %+v", result.InventoryCSV, result.Pending)
	}
	if strings.Contains(string(result.InventoryCSV), "pending") {
		t.Fatal("preview placeholder leaked into CSV")
	}
	if _, err := inventory.ParseDelimitedBytes(result.InventoryCSV); err == nil {
		t.Fatal("deployment parser accepted blank hostname")
	}
	copy, err := inventory.ParseTemplate(result.InventoryCSV)
	if err != nil {
		t.Fatal(err)
	}
	again, err := PrepareTemplate(result.BundleJSON, copy)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result.BundleJSON, again.BundleJSON) || !bytes.Equal(result.InventoryCSV, again.InventoryCSV) {
		t.Fatal("normalization is not idempotent")
	}
}

func TestTemplateRejectsFilledConflictsEvenWithBlanks(t *testing.T) {
	for _, input := range []string{
		"hostname,mgmt_ip\n,10.0.0.2\nb,10.0.0.2\n",
		"hostname,mgmt_prefix\n,33\n",
		"hostname,mgmt_mode,mgmt_iface1,mgmt_iface2\n,single,eth0,eth1\n",
		"hostname,rdma1_mac,mgmt_mac1\n,aa:bb:cc:dd:ee:01,AA:BB:CC:DD:EE:01\n",
		"hostname,rdma1_table\n,-1\n",
		"hostname,mgmt_ip,mgmt_prefix\n,10.0.0.255,24\n",
		"hostname,mgmt_ip,mgmt_prefix,mgmt_gateway\n,10.0.0.2,24,10.1.0.1\n",
		"hostname,rdma1_name,mgmt_iface1\n,eth0,eth0\n",
	} {
		t.Run(input, func(t *testing.T) {
			table, err := inventory.ParseTemplate([]byte(input))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := PrepareTemplate([]byte(testBundle), table); err == nil {
				t.Fatal("accepted conflicting values")
			}
		})
	}
}

func TestTemplateCompleteMatchesDeploymentResolver(t *testing.T) {
	bData, err := os.ReadFile("../../examples/bundle.ubuntu22.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	table, err := inventory.LoadTemplate("../../examples/inventory.sample.csv", "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := PrepareTemplate(bData, table)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := ValidateProject(result.BundleJSON, result.InventoryCSV)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Machines, summary.Machines) {
		t.Fatalf("preview diverged from deployment\n%+v\n%+v", result.Machines, summary.Machines)
	}
	decoded, err := DecodeBundle(result.BundleJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.PostTasks, result.Bundle.PostTasks) || !reflect.DeepEqual(decoded.Artifacts, result.Bundle.Artifacts) {
		t.Fatal("advanced fields lost")
	}
}

func TestLegacyDefaultsMaterializedBeforeCanonicalSave(t *testing.T) {
	b := strings.Replace(testBundle, `"configure_management_network":true`, `"configure_management_network":true,"mgmt_prefix":24,"mgmt_gateway":"10.0.0.1","rdma_interfaces":[{"name":"xgbe7","table":107,"gateway":"172.16.0.1"}],"rdma_prefix":24`, 1)
	table, err := inventory.ParseTemplate([]byte("host_id,mgmt_ip,rdma1_ip\na,10.0.0.2,172.16.0.2\n"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := PrepareTemplate([]byte(b), table)
	if err != nil {
		t.Fatal(err)
	}
	records, err := inventory.ParseDelimitedBytes(result.InventoryCSV)
	if err != nil {
		t.Fatal(err)
	}
	if records[0].MgmtPrefix != "24" || records[0].RDMA[0].Table != "" {
		t.Fatalf("hidden route table leaked into canonical inventory: %+v", records[0].RDMA[0])
	}
	canonical, err := DecodeBundle(result.BundleJSON)
	if err != nil {
		t.Fatal(err)
	}
	canonical.ApplyDefaults()
	resolved, err := spec.ResolveMachine(canonical, records[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.RDMA[0].Table != 101 {
		t.Fatalf("omitted route table did not use slot default: %+v", resolved.RDMA[0])
	}
}

func TestPolicyEditingPreservesExplicitFalseAndZero(t *testing.T) {
	data, err := EditPolicy([]byte(testBundle), "defaults.configure_management_network", "false")
	if err != nil {
		t.Fatal(err)
	}
	data, err = EditPolicy(data, "check.bandwidth.gid_index", "0")
	if err != nil {
		t.Fatal(err)
	}
	b, err := DecodeBundle(data)
	if err != nil {
		t.Fatal(err)
	}
	b.ApplyDefaults()
	if b.ConfigureManagementNetwork() || b.Check.Bandwidth.GIDIndexAuto {
		t.Fatal("explicit false/zero lost")
	}
	if _, err := EditPolicy(data, "platform.os_family", "ubuntu"); err == nil {
		t.Fatal("platform was editable")
	}
}

func TestPolicyEditorUsesCurrentValueColumn(t *testing.T) {
	data := []byte(`{"platform":{"os_family":"ubuntu"},"defaults":{"mgmt_mtu":1500},"post_power_action":{"action":"none"}}`)
	result, err := PolicyEditor(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range result.Rows {
		if row.Path == "defaults.mgmt_mtu" {
			if row.Current != "1500" || !row.Explicit {
				t.Fatalf("current value was not retained: %+v", row)
			}
			continue
		}
		if row.Path == "defaults.rdma_mtu" && (row.Current != "" || row.Explicit) {
			t.Fatal("unset value must not display a derived default")
		}
	}
	if !result.Expanded["默认网络"] || result.Expanded["超时时间"] {
		t.Fatalf("configured/unset category expansion: %+v", result.Expanded)
	}
}
