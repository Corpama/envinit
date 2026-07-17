package checker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"envinit/internal/spec"
)

func TestParseShowGIDsDiscoversRDMAIPv4AndFiltersNonRDMA(t *testing.T) {
	output := `
DEV      PORT  INDEX  GID                                      IPv4          VER  DEV
mlx5_0   1     0      fe80:0000:0000:0000:92e3:17ff:fe4d:0de2                v1   ens11f0np0
mlx5_0   1     2      0000:0000:0000:0000:0000:ffff:0a3d:0d29  10.61.13.41   v1   ens11f0np0
mlx5_0   1     3      0000:0000:0000:0000:0000:ffff:0a3d:0d29  10.61.13.41   v2   ens11f0np0
mlx5_1   1     2      0000:0000:0000:0000:0000:ffff:0a3d:0e29  10.61.14.41   v1   ens11f1np1
mlx5_2   1     2      0000:0000:0000:0000:0000:ffff:0a3d:0b29  10.61.11.41   v1   ens13f0np0
mlx5_3   1     2      0000:0000:0000:0000:0000:ffff:0a3d:0c29  10.61.12.41   v1   ens13f1np1
mlx5_bond_0 1  2      0000:0000:0000:0000:0000:ffff:0a3d:0a29  10.61.10.41   v1   bond0
mlx5_bond_0 1  4      0000:0000:0000:0000:0000:ffff:0a60:4c40  10.96.76.64   v1   vxlan.calico
`
	items := parseShowGIDs(output)
	want := []discoveredRDMAInterface{
		{Name: "ens13f0np0", IP: "10.61.11.41", IBDevice: "mlx5_2"},
		{Name: "ens13f1np1", IP: "10.61.12.41", IBDevice: "mlx5_3"},
		{Name: "ens11f0np0", IP: "10.61.13.41", IBDevice: "mlx5_0"},
		{Name: "ens11f1np1", IP: "10.61.14.41", IBDevice: "mlx5_1"},
	}
	if len(items) != len(want) {
		t.Fatalf("unexpected item count: got %#v want %#v", items, want)
	}
	for idx := range want {
		if items[idx] != want[idx] {
			t.Fatalf("unexpected item %d: got %#v want %#v", idx, items[idx], want[idx])
		}
	}
}

func TestParseManagementIPDiscoveryPrefersDefaultRouteIface(t *testing.T) {
	output := `
default via 10.61.10.1 dev bond0 proto static metric 100

--ADDR--
2: ens11f0np0    inet 10.61.13.41/24 brd 10.61.13.255 scope global ens11f0np0
3: bond0         inet 10.61.10.41/24 brd 10.61.10.255 scope global bond0
4: vxlan.calico  inet 10.96.76.64/32 scope global vxlan.calico
`
	if got, want := parseManagementIPDiscovery(output), "10.61.10.41"; got != want {
		t.Fatalf("unexpected management IP: got=%s want=%s", got, want)
	}
}

func TestUpdateDelimitedInventoryRDMAWritesDiscoveredFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "inventory.csv")
	content := "host_id,hostname,mgmt_ip\nnode1,xpu-21,10.61.10.41\nnode2,xpu-03,10.61.10.23\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write inventory: %v", err)
	}
	targets := []Target{
		{
			Input:   "node1",
			Name:    "xpu-21",
			Address: "10.61.10.41",
			RDMA: []spec.RDMARecord{
				{Name: "ens13f0np0", IP: "10.61.11.41"},
				{Name: "ens13f1np1", IP: "10.61.12.41"},
			},
		},
		{
			Input:   "node2",
			Name:    "xpu-03",
			Address: "10.61.10.23",
			RDMA: []spec.RDMARecord{
				{Name: "ens11f0np0", IP: "10.61.11.23"},
				{Name: "ens11f1np1", IP: "10.61.12.23"},
			},
		},
	}
	if err := updateDelimitedInventoryRDMA(path, targets); err != nil {
		t.Fatalf("update inventory: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"rdma1_name,rdma1_ip,rdma2_name,rdma2_ip",
		"node1,xpu-21,10.61.10.41,ens13f0np0,10.61.11.41,ens13f1np1,10.61.12.41",
		"node2,xpu-03,10.61.10.23,ens11f0np0,10.61.11.23,ens11f1np1,10.61.12.23",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in inventory:\n%s", want, got)
		}
	}
}

func TestUpdateDelimitedInventoryRDMAAppendsMissingTargetAndDoesNotDuplicateExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "inventory.csv")
	content := "host_id,hostname,mgmt_ip\nxpu-21,xpu-21,10.61.10.41\nxpu-23,xpu-23,10.61.10.43\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write inventory: %v", err)
	}
	targets := []Target{
		{
			Input:   "xpu-23",
			Name:    "xpu-23",
			Address: "10.61.10.43",
			RDMA:    []spec.RDMARecord{{Name: "ens11f0np0", IP: "10.61.11.43"}},
		},
		{
			Input:   "xpu-05",
			Name:    "xpu-05",
			Address: "10.61.10.25",
			RDMA:    []spec.RDMARecord{{Name: "ens11f0np0", IP: "10.61.11.25"}},
		},
	}
	if err := updateDelimitedInventoryRDMA(path, targets); err != nil {
		t.Fatalf("update inventory: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	got := string(data)
	if count := strings.Count(got, "xpu-23"); count != 2 {
		t.Fatalf("expected existing xpu-23 row to be updated without appending duplicate, count=%d:\n%s", count, got)
	}
	if !strings.Contains(got, "xpu-05,xpu-05,10.61.10.25,ens11f0np0,10.61.11.25") {
		t.Fatalf("expected xpu-05 row to be appended, got:\n%s", got)
	}
}

func TestUpdateDelimitedInventoryRDMARejectsDuplicateMatchingRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "inventory.csv")
	content := "host_id,hostname,mgmt_ip\nxpu-05,xpu-05,10.61.10.25\nxpu-05-copy,xpu-05,10.61.10.25\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write inventory: %v", err)
	}
	err := updateDelimitedInventoryRDMA(path, []Target{{
		Input:   "xpu-05",
		Name:    "xpu-05",
		Address: "10.61.10.25",
		RDMA:    []spec.RDMARecord{{Name: "ens11f0np0", IP: "10.61.11.25"}},
	}})
	if err == nil || !strings.Contains(err.Error(), "matches multiple rows") {
		t.Fatalf("expected duplicate row conflict, got %v", err)
	}
}

func TestFilterRDMAFromManagementCandidates(t *testing.T) {
	mgmt := []discoveredIPv4Address{
		{Iface: "bond0", IP: "10.61.10.25", Preferred: true},
		{Iface: "ens11f0np0", IP: "10.61.11.25"},
	}
	rdma := []discoveredRDMAInterface{{Name: "ens11f0np0", IP: "10.61.11.25", IBDevice: "mlx5_0"}}
	got := filterRDMAFromManagementCandidates(mgmt, rdma)
	if len(got) != 1 || got[0].Iface != "bond0" {
		t.Fatalf("expected RDMA iface to be filtered from management candidates, got %#v", got)
	}
}

func TestFilterRDMAFromManagementCandidatesReturnsEmptyWhenAllAreRDMA(t *testing.T) {
	mgmt := []discoveredIPv4Address{{Iface: "ens11f0np0", IP: "10.61.11.25", Preferred: true}}
	rdma := []discoveredRDMAInterface{{Name: "ens11f0np0", IP: "10.61.11.25", IBDevice: "mlx5_0"}}
	if got := filterRDMAFromManagementCandidates(mgmt, rdma); len(got) != 0 {
		t.Fatalf("expected no management candidate after removing RDMA interfaces, got %#v", got)
	}
}

func TestWriteTargetInventoryFieldsDoesNotWriteHostnameAsManagementIP(t *testing.T) {
	row := []string{"node1", ""}
	header := map[string]int{"host_id": 0, "mgmt_ip": 1}
	writeTargetInventoryFields(row, header, Target{Input: "node1", Name: "node1", Address: "node1"})
	if row[1] != "" {
		t.Fatalf("mgmt_ip = %q, want empty when discovery used a hostname connection address", row[1])
	}
}

func TestNetworkDiscoveryReviewCanRemapRDMASlots(t *testing.T) {
	targets := []Target{{
		Name:    "xpu-03",
		Address: "10.61.10.23",
		RDMA: []spec.RDMARecord{
			{Name: "rdma1"},
			{Name: "rdma2"},
		},
	}}
	mgmt := map[string][]discoveredIPv4Address{
		"xpu-03": {{Iface: "bond0", IP: "10.61.10.23", Preferred: true}},
	}
	rdma := map[string][]discoveredRDMAInterface{
		"xpu-03": {
			{Name: "ens13f0np0", IP: "10.61.13.23", IBDevice: "mlx5_4"},
			{Name: "ens11f0np0", IP: "10.61.11.23", IBDevice: "mlx5_0"},
		},
	}
	review := newNetworkDiscoveryReview(targets, mgmt, rdma)
	review.Targets[0].RDMAChoices[0] = 1
	review.Targets[0].RDMAChoices[1] = 0
	got := reviewTargets(review)
	if got[0].RDMA[0].Name != "ens11f0np0" || got[0].RDMA[0].IP != "10.61.11.23" {
		t.Fatalf("unexpected rdma1 mapping: %#v", got[0].RDMA[0])
	}
	if got[0].RDMA[1].Name != "ens13f0np0" || got[0].RDMA[1].IP != "10.61.13.23" {
		t.Fatalf("unexpected rdma2 mapping: %#v", got[0].RDMA[1])
	}
}

func TestConfirmDiscoveredNetworkRejectsNonInteractiveWithoutYes(t *testing.T) {
	var output strings.Builder
	targets := []Target{{Name: "xpu-03", Address: "10.61.10.23"}}
	mgmt := map[string][]discoveredIPv4Address{
		"xpu-03": {{Iface: "bond0", IP: "10.61.10.23", Preferred: true}},
	}
	rdma := map[string][]discoveredRDMAInterface{
		"xpu-03": {{Name: "ens11f0np0", IP: "10.61.11.23", IBDevice: "mlx5_0"}},
	}
	_, err := confirmDiscoveredNetwork(DiscoverOptions{Output: &output}, targets, mgmt, rdma)
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("expected non-interactive confirmation to require --yes, got %v", err)
	}
}
