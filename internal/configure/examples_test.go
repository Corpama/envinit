package configure

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"envinit/internal/bundle"
	"envinit/internal/inventory"
	"envinit/internal/spec"
)

func TestExampleProjectsValidate(t *testing.T) {
	const inventoryPath = "../../examples/inventory.sample.csv"
	data, err := os.ReadFile(inventoryPath)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	if err != nil {
		t.Fatalf("example inventory column counts: %v", err)
	}
	wantHeader := []string{"hostname", "mgmt_mode", "mgmt_target_name", "mgmt_ip", "mgmt_prefix", "mgmt_gateway", "mgmt_iface1", "mgmt_mac1", "mgmt_iface2", "mgmt_mac2"}
	for slot := 1; slot <= 4; slot++ {
		for _, field := range []string{"name", "ip", "prefix", "gateway", "mac"} {
			wantHeader = append(wantHeader, fmt.Sprintf("rdma%d_%s", slot, field))
		}
	}
	if len(rows) != 3 || !reflect.DeepEqual(rows[0], wantHeader) {
		t.Fatalf("want canonical 30-column inventory with two machines, got %v", rows)
	}
	records, err := inventory.Load(inventoryPath, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		if r.LegacySchema || r.HostID != "" {
			t.Fatalf("example must use canonical hostname: %+v", r)
		}
	}
	for _, profile := range []string{"ubuntu22", "kylin10sp3"} {
		t.Run(profile, func(t *testing.T) {
			path := "../../examples/bundle." + profile + ".sample.json"
			bundleJSON, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			summary, err := ValidateProject(bundleJSON, data)
			if err != nil {
				t.Fatalf("offline project validation: %v", err)
			}
			if summary.MachineCount != 2 || summary.RDMASlots != 8 || len(summary.Machines) != 2 {
				t.Fatalf("want two machines with four RDMA slots each: %+v", summary)
			}
			// Check the normal plan/apply loader against the host-independent validator.
			b, err := bundle.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			for i, m := range summary.Machines {
				resolved, err := spec.ResolveMachine(b, records[i], nil)
				if err != nil || !reflect.DeepEqual(resolved, m) {
					t.Fatalf("offline/runtime resolution differs: err=%v runtime=%+v offline=%+v", err, resolved, m)
				}
				if m.Hostname != fmt.Sprintf("xpu%d", i+11) || m.MgmtMode != "bond" || m.MgmtTargetName != "bond0" || m.MgmtBondName != "bond0" || len(m.MgmtIfaces) != 2 || m.MgmtPrefix != 26 || m.MgmtGateway != "10.101.9.1" || len(m.RDMA) != 4 {
					t.Fatalf("unexpected machine configuration: %+v", m)
				}
				for slot, port := range m.RDMA {
					gateways := []string{"172.18.12.126", "172.18.12.254", "172.18.13.126", "172.18.13.254"}
					routes := []string{"172.18.12.0/25", "172.18.12.128/25", "172.18.13.0/25", "172.18.13.128/25"}
					if port.Name != fmt.Sprintf("xgbe%d", slot) || port.Prefix != 25 || port.Gateway != gateways[slot] || port.Table != 101+slot || port.RouteCIDR != routes[slot] {
						t.Fatalf("%s rdma%d: unexpected derived network: %+v", m.Hostname, slot+1, port)
					}
				}
			}
			for _, warning := range summary.Warnings {
				if strings.Contains(warning, "deprecated") {
					t.Fatalf("canonical examples produced a deprecation warning: %s", warning)
				}
			}
			t.Logf("machines=%d RDMA slots=%d warnings=%v", summary.MachineCount, summary.RDMASlots, summary.Warnings)
		})
	}
}
