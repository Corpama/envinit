package configure

import (
	"strings"
	"testing"
)

const testBundle = `{"platform":{"os_family":"kylin","package_manager":"yum","network_backend":"auto"},"defaults":{"configure_management_network":true},"post_power_action":{"action":"none"}}`
const testTable = "host_id,mgmt_mode,mgmt_bond_name,mgmt_ip,mgmt_prefix,mgmt_gateway,rdma1_ip,rdma1_prefix,rdma1_gateway\na,single,,10.0.0.2,24,10.0.0.1,172.18.12.10,25,172.18.12.126\nb,bond,bond1,10.0.0.3,24,10.0.0.1,172.18.12.11,25,172.18.12.126\n"

func TestOfflineMixedPlanning(t *testing.T) {
	s, err := ValidateProject([]byte(testBundle), []byte(testTable))
	if err != nil {
		t.Fatal(err)
	}
	if s.MachineCount != 2 || s.RDMASlots != 2 || len(s.Machines[0].MgmtIfaces) != 1 || len(s.Machines[1].MgmtIfaces) != 2 {
		t.Fatalf("unexpected summary: %+v", s)
	}
	if s.Machines[0].MgmtBondName != "" || s.Machines[1].MgmtBondName != "bond1" {
		t.Fatal("mode did not determine target")
	}
}
func TestPlanningRejectsUnsafeInput(t *testing.T) {
	for _, tc := range []struct{ name, table, contains string }{
		{"missing-prefix", strings.Replace(testTable, "10.0.0.2,24,", "10.0.0.2,,", 1), "explicitly"},
		{"missing-gateway", strings.Replace(testTable, "10.0.0.2,24,10.0.0.1", "10.0.0.2,24,", 1), "explicitly"},
		{"duplicate-ip", strings.Replace(testTable, "10.0.0.3", "10.0.0.2", 1), "duplicate IP"},
		{"bad-prefix", strings.Replace(testTable, ",25,", ",33,", 1), "prefix"},
		{"off-subnet", strings.Replace(testTable, "172.18.12.126", "172.18.13.1", 1), "gateway"},
		{"missing-mode", strings.Replace(testTable, "a,single,", "a,,", 1), "mgmt_mode"},
		{"single-bond", strings.Replace(testTable, "a,single,,", "a,single,bond0,", 1), "single"},
		{"traversal-bond", strings.Replace(testTable, "b,bond,bond1", "b,bond,../../x", 1), "interface name"},
		{"duplicate-alias", "host_id,mgmt_ip,management_ip\na,10.0.0.2,10.0.0.3\n", "duplicate"},
		{"slot-too-large", "host_id,rdma999999999_ip\na,10.0.0.2\n", "1..64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateProject([]byte(testBundle), []byte(tc.table))
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("got %v, want %s", err, tc.contains)
			}
		})
	}
}

func TestExplicitCommonPrefixAllowed(t *testing.T) {
	b := strings.Replace(testBundle, `"configure_management_network":true`, `"configure_management_network":true,"mgmt_prefix":24`, 1)
	table := strings.Replace(testTable, "10.0.0.2,24,", "10.0.0.2,,", 1)
	if _, err := ValidateProject([]byte(b), []byte(table)); err != nil {
		t.Fatal(err)
	}
}
