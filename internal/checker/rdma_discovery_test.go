package checker

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"envinit/internal/spec"
)

func TestBindDiscoveryEndpointByRemoteHostname(t *testing.T) {
	records := []spec.MachineRecord{{
		HostID: "node1", Hostname: "instance-hjwfqcl0",
		RDMA: []spec.RDMARecord{{Name: "eth1", IP: "25.16.2.2"}},
	}}
	targets, err := ResolveDiscoveryTargets(records, []string{"192.168.32.11"})
	if err != nil {
		t.Fatalf("resolve endpoint: %v", err)
	}
	var output strings.Builder
	bound, err := bindDiscoveryTargetIdentities(DiscoverOptions{
		Records: records,
		Output:  &output,
		CommandRunner: func(_ spec.CheckConfig, target Target, command string) (string, error) {
			if got := targetControlAddress(target); got != "192.168.32.11" {
				t.Fatalf("hostname discovery control address = %q", got)
			}
			if command != "hostnamectl --static 2>/dev/null || hostname" {
				t.Fatalf("unexpected command: %s", command)
			}
			return "instance-hjwfqcl0\n", nil
		},
	}, targets)
	if err != nil {
		t.Fatalf("bind endpoint: %v", err)
	}
	target := bound[0]
	if !target.InventoryMatched || target.InventoryIdentity != "node1" || target.Name != "instance-hjwfqcl0" {
		t.Fatalf("endpoint was not bound to inventory row: %#v", target)
	}
	if got := targetControlAddress(target); got != "192.168.32.11" {
		t.Fatalf("control address changed after binding: %q", got)
	}
	if len(target.RDMA) != 1 || target.RDMA[0].Name != "eth1" {
		t.Fatalf("inventory RDMA planning was not inherited: %#v", target.RDMA)
	}
	if !strings.Contains(output.String(), "control=192.168.32.11 hostname=instance-hjwfqcl0 inventory=node1") {
		t.Fatalf("missing concise binding explanation: %s", output.String())
	}
}

func TestBindDiscoveryEndpointAcceptsShortHostnameMatch(t *testing.T) {
	records := []spec.MachineRecord{{HostID: "node1", Hostname: "instance-hjwfqcl0.cluster.local"}}
	targets, err := ResolveDiscoveryTargets(records, []string{"192.168.32.11"})
	if err != nil {
		t.Fatalf("resolve endpoint: %v", err)
	}
	bound, err := bindDiscoveryTargetIdentities(DiscoverOptions{
		Records: records,
		Output:  io.Discard,
		CommandRunner: func(_ spec.CheckConfig, _ Target, _ string) (string, error) {
			return "instance-hjwfqcl0\n", nil
		},
	}, targets)
	if err != nil {
		t.Fatalf("bind endpoint by short hostname: %v", err)
	}
	if !bound[0].InventoryMatched || bound[0].InventoryIdentity != "node1" {
		t.Fatalf("short hostname did not bind inventory row: %#v", bound[0])
	}
}

func TestBindDiscoveryEndpointRejectsAmbiguousHostname(t *testing.T) {
	records := []spec.MachineRecord{
		{HostID: "node1", Hostname: "duplicate.cluster-a.local"},
		{HostID: "node2", Hostname: "duplicate.cluster-b.local"},
	}
	targets, err := ResolveDiscoveryTargets(records, []string{"192.168.32.11"})
	if err != nil {
		t.Fatalf("resolve endpoint: %v", err)
	}
	_, err = bindDiscoveryTargetIdentities(DiscoverOptions{
		Records: records,
		Output:  io.Discard,
		CommandRunner: func(_ spec.CheckConfig, _ Target, _ string) (string, error) {
			return "duplicate\n", nil
		},
	}, targets)
	if err == nil || !strings.Contains(err.Error(), "matches 2 inventory rows") || !strings.Contains(err.Error(), "<inventory-id>=192.168.32.11") {
		t.Fatalf("expected safe ambiguous-hostname rejection, got %v", err)
	}
}

func TestBindDiscoveryEndpointCreatesNewIdentityFromUnknownHostname(t *testing.T) {
	targets, err := ResolveDiscoveryTargets(nil, []string{"192.168.32.11"})
	if err != nil {
		t.Fatalf("resolve endpoint: %v", err)
	}
	var output strings.Builder
	bound, err := bindDiscoveryTargetIdentities(DiscoverOptions{
		Output: &output,
		CommandRunner: func(_ spec.CheckConfig, _ Target, _ string) (string, error) {
			return "new-host\n", nil
		},
	}, targets)
	if err != nil {
		t.Fatalf("bind unknown hostname as new target: %v", err)
	}
	target := bound[0]
	if target.InventoryMatched || target.InventoryIdentity != "new-host" || target.Name != "new-host" || target.DiscoveredHostname != "new-host" {
		t.Fatalf("unknown hostname did not become a new inventory identity: %#v", target)
	}
	if got := targetControlAddress(target); got != "192.168.32.11" {
		t.Fatalf("control address = %q, want original SSH endpoint", got)
	}
	if !strings.Contains(output.String(), "hostname=new-host inventory=new-row") {
		t.Fatalf("missing new-row binding log: %s", output.String())
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "inventory.csv")
	if err := os.WriteFile(path, []byte("host_id,hostname,mgmt_ip\nnode1,node1,192.168.32.10\n"), 0o644); err != nil {
		t.Fatalf("write inventory: %v", err)
	}
	target.Address = "192.168.32.11"
	target.RDMA = []spec.RDMARecord{{Name: "eth1", IP: "25.16.2.2"}}
	if err := updateDelimitedInventoryRDMA(path, []Target{target}); err != nil {
		t.Fatalf("append hostname-discovered target: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	if !strings.Contains(string(data), "new-host,new-host,192.168.32.11,eth1,25.16.2.2") {
		t.Fatalf("new hostname row was not appended:\n%s", data)
	}
}

func TestBindDiscoveryExplicitNewIdentityKeepsIDAndUsesRemoteHostname(t *testing.T) {
	targets, err := ResolveDiscoveryTargets(nil, []string{"node9=192.168.32.19"})
	if err != nil {
		t.Fatalf("resolve explicit new endpoint: %v", err)
	}
	bound, err := bindDiscoveryTargetIdentities(DiscoverOptions{
		Output: io.Discard,
		CommandRunner: func(_ spec.CheckConfig, _ Target, _ string) (string, error) {
			return "instance-new9\n", nil
		},
	}, targets)
	if err != nil {
		t.Fatalf("bind explicit new endpoint: %v", err)
	}
	target := bound[0]
	if target.InventoryIdentity != "node9" || target.Name != "instance-new9" || target.DiscoveredHostname != "instance-new9" {
		t.Fatalf("explicit identity or discovered hostname lost: %#v", target)
	}
	row := make([]string, 3)
	writeNewTargetIdentity(row, map[string]int{"host_id": 0, "hostname": 1, "mgmt_ip": 2}, target)
	if row[0] != "node9" || row[1] != "instance-new9" {
		t.Fatalf("new inventory identity = %#v, want host_id=node9 hostname=instance-new9", row)
	}
}

func TestExplicitDiscoveryIdentityCannotUpdateAnotherHostnameRow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "inventory.csv")
	content := "host_id,hostname,mgmt_ip\nnode1,expected-node1,192.168.32.11\nnode2,actual-node2,192.168.32.12\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write inventory: %v", err)
	}
	target := Target{
		Input:              "node1",
		Name:               "expected-node1",
		InventoryIdentity:  "node1",
		InventoryMatched:   true,
		ExplicitIdentity:   true,
		DiscoveredHostname: "actual-node2",
		Address:            "192.168.32.99",
		RDMA:               []spec.RDMARecord{{Name: "eth1", IP: "25.16.2.2"}},
	}
	if err := updateDelimitedInventoryRDMA(path, []Target{target}); err != nil {
		t.Fatalf("update explicitly selected row: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "node1,expected-node1,192.168.32.99,eth1,25.16.2.2") {
		t.Fatalf("explicitly selected node1 was not updated:\n%s", got)
	}
	if !strings.Contains(got, "node2,actual-node2,192.168.32.12,,") {
		t.Fatalf("remote hostname row was changed by explicit mapping:\n%s", got)
	}
}

func TestAutoDiscoverClassifiesManagementGIDAndExpandsEightRDMANICs(t *testing.T) {
	var output strings.Builder
	target := Target{
		Input:   "node1",
		Name:    "node1",
		Address: "node1",
		RDMA: []spec.RDMARecord{
			{Name: "rdma1"}, {Name: "rdma2"}, {Name: "rdma3"}, {Name: "rdma4"},
		},
	}
	opts := DiscoverOptions{
		DryRun:  true,
		Confirm: false,
		Output:  &output,
		CommandRunner: func(_ spec.CheckConfig, _ Target, command string) (string, error) {
			switch {
			case strings.Contains(command, "--ADDR--"):
				var b strings.Builder
				fmt.Fprintln(&b, "default via 192.168.32.1 dev eth0")
				fmt.Fprintln(&b, "--ADDR--")
				fmt.Fprintln(&b, "2: eth0 inet 192.168.32.11/24 scope global eth0")
				for idx := 1; idx <= 8; idx++ {
					fmt.Fprintf(&b, "%d: eth%d inet 25.16.%d.%d/28 scope global eth%d\n", idx+2, idx, idx, 2, idx)
				}
				return b.String(), nil
			case strings.TrimSpace(command) == "show_gids":
				var b strings.Builder
				fmt.Fprintln(&b, "DEV PORT INDEX GID IPv4 VER DEV")
				fmt.Fprintln(&b, "mlx5_0 1 2 gid 192.168.32.11 v1 eth0")
				for idx := 1; idx <= 8; idx++ {
					fmt.Fprintf(&b, "mlx5_%d 1 2 gid 25.16.%d.2 v1 eth%d\n", idx, idx, idx)
				}
				return b.String(), nil
			case strings.Contains(command, "NIC|%s"):
				var b strings.Builder
				fmt.Fprintln(&b, "NIC|eth0|00:11:22:33:44:00|0000:20:00.0|mlx5_core|0x15b3|0x1017|100000|100000|1500|1|up|p0|0")
				for idx := 1; idx <= 8; idx++ {
					fmt.Fprintf(&b, "NIC|eth%d|00:11:22:33:44:%02d|0000:%02d:00.0|mlx5_core|0x15b3|0x1023|400000|400000|4200|1|up|p0|0\n", idx, idx, 32+idx)
				}
				return b.String(), nil
			default:
				return "", fmt.Errorf("unexpected command: %s", command)
			}
		},
	}
	updated, err := autoDiscoverNetworkTargets(opts, []Target{target})
	if err != nil {
		t.Fatalf("auto discover: %v", err)
	}
	if updated[0].Address != "192.168.32.11" {
		t.Fatalf("management address = %s, want eth0 address", updated[0].Address)
	}
	if len(updated[0].RDMA) != 8 {
		t.Fatalf("RDMA count = %d, want 8: %#v", len(updated[0].RDMA), updated[0].RDMA)
	}
	for _, record := range updated[0].RDMA {
		if record.Name == "eth0" {
			t.Fatalf("management GID interface was classified as RDMA: %#v", updated[0].RDMA)
		}
	}
}

func TestUpdateDelimitedInventoryRDMAExpandsFullTemplateLayoutAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "inventory.csv")
	var header []string
	header = append(header, "host_id", "mgmt_ip")
	for idx := 1; idx <= 4; idx++ {
		for _, field := range []string{"name", "ip", "prefix", "gateway", "mac", "table", "route_cidr"} {
			header = append(header, fmt.Sprintf("rdma%d_%s", idx, field))
		}
	}
	content := strings.Join(header, ",") + "\nnode1,192.168.32.11\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write inventory: %v", err)
	}
	target := Target{Input: "node1", Name: "node1", Address: "192.168.32.11"}
	for idx := 1; idx <= 8; idx++ {
		target.RDMA = append(target.RDMA, spec.RDMARecord{Name: fmt.Sprintf("eth%d", idx), IP: fmt.Sprintf("25.16.%d.2", idx), Prefix: "28", MAC: fmt.Sprintf("00:11:22:33:44:%02d", idx)})
	}
	before, after, err := updateDelimitedInventoryRDMAWithChange(path, []Target{target})
	if err != nil {
		t.Fatalf("expand inventory: %v", err)
	}
	if before != 4 || after != 8 {
		t.Fatalf("slot change = %d -> %d, want 4 -> 8", before, after)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	for _, field := range []string{"rdma8_name", "rdma8_ip", "rdma8_prefix", "rdma8_gateway", "rdma8_mac", "rdma8_table", "rdma8_route_cidr"} {
		if !strings.Contains(string(data), field) {
			t.Fatalf("missing expanded field %s:\n%s", field, data)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat inventory: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("inventory mode = %o, want 600", info.Mode().Perm())
	}
}

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
5: nodelocaldns   inet 169.254.25.10/32 scope global nodelocaldns
`
	if got, want := parseManagementIPDiscovery(output), "10.61.10.41"; got != want {
		t.Fatalf("unexpected management IP: got=%s want=%s", got, want)
	}
	for _, item := range parseManagementIPCandidates(output) {
		if item.IP == "169.254.25.10" || item.Iface == "nodelocaldns" {
			t.Fatalf("IPv4 link-local address leaked into management candidates: %#v", item)
		}
	}
}

func TestDiscoveredCandidatesRejectNonRoutableIPv4Classes(t *testing.T) {
	for _, value := range []string{"", "not-an-ip", "0.0.0.0", "127.0.0.1", "169.254.25.10", "224.0.0.1", "255.255.255.255"} {
		if usableDiscoveredIPv4(value) {
			t.Fatalf("%q must not be a usable discovered IPv4 address", value)
		}
	}
	for _, value := range []string{"10.0.0.1", "25.16.1.34", "100.64.0.1", "192.168.32.8"} {
		if !usableDiscoveredIPv4(value) {
			t.Fatalf("%q should remain a usable discovered IPv4 address", value)
		}
	}

	items := parseShowGIDs(`
DEV PORT INDEX GID IPv4 VER DEV
mlx5_0 1 2 0000:0000:0000:0000:0000:ffff:a9fe:190a 169.254.25.10 v1 nodelocaldns
mlx5_1 1 2 0000:0000:0000:0000:0000:ffff:1910:0122 25.16.1.34 v1 eth1
`)
	if len(items) != 1 || items[0].Name != "eth1" || items[0].IP != "25.16.1.34" {
		t.Fatalf("link-local show_gids entry was not filtered: %#v", items)
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

func TestUpdateDelimitedInventoryRDMAClearsTrailingStaleSlots(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "inventory.csv")
	content := "host_id,mgmt_ip,rdma1_name,rdma1_ip,rdma1_prefix,rdma2_name,rdma2_ip,rdma2_prefix,rdma3_name,rdma3_ip,rdma3_gateway,rdma4_name,rdma4_ip,rdma4_table\n" +
		"node1,10.61.10.41,old1,10.61.11.41,24,old2,10.61.12.41,24,old3,10.61.13.41,10.61.13.1,old4,10.61.14.41,104\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write inventory: %v", err)
	}
	targets := []Target{{
		Input:   "node1",
		Name:    "node1",
		Address: "10.61.10.41",
		RDMA: []spec.RDMARecord{
			{Name: "ens1", IP: "10.61.11.41"},
			{Name: "ens2", IP: "10.61.12.41"},
		},
	}}
	if err := updateDelimitedInventoryRDMA(path, targets); err != nil {
		t.Fatalf("update inventory: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "node1,10.61.10.41,ens1,10.61.11.41,24,ens2,10.61.12.41,24,,,,,,\n") {
		t.Fatalf("expected stale rdma3/rdma4 fields to be cleared while retained slots keep their settings:\n%s", got)
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

func TestNetworkDiscoveryReviewDoesNotExpandFromRawCandidateCount(t *testing.T) {
	target := Target{Name: "node1", Address: "192.168.32.11", RDMA: []spec.RDMARecord{{Name: "eth1"}, {Name: "eth2"}, {Name: "eth3"}, {Name: "eth4"}}}
	mgmt := map[string][]discoveredIPv4Address{"node1": {{Iface: "eth0", IP: "192.168.32.11", Confidence: "strong"}}}
	var candidates []discoveredRDMAInterface
	for idx := 1; idx <= 8; idx++ {
		candidates = append(candidates, discoveredRDMAInterface{Name: fmt.Sprintf("eth%d", idx), IP: fmt.Sprintf("25.16.%d.2", idx), Confidence: "strong"})
	}
	review := newNetworkDiscoveryReview([]Target{target}, mgmt, map[string][]discoveredRDMAInterface{"node1": candidates})
	if got := len(review.Targets[0].RDMAChoices); got != 4 {
		t.Fatalf("slot count = %d, want final planned/confirmed count 4 instead of raw candidate count 8", got)
	}
}

func TestNetworkDiscoveryReviewRejectsSameNICForManagementAndRDMA(t *testing.T) {
	target := Target{Name: "node1", Address: "192.168.32.11", RDMA: []spec.RDMARecord{{Name: "eth0"}}}
	mgmt := map[string][]discoveredIPv4Address{"node1": {{Iface: "eth0", IP: "192.168.32.11", Confidence: "strong"}}}
	rdma := map[string][]discoveredRDMAInterface{"node1": {{Name: "eth0", IP: "25.16.1.2", Confidence: "strong"}}}
	review := newNetworkDiscoveryReview([]Target{target}, mgmt, rdma)
	if err := validateNetworkDiscoveryReview(review); err == nil || !strings.Contains(err.Error(), "both management and RDMA") {
		t.Fatalf("expected cross-role NIC conflict, got %v", err)
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
