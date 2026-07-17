package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"envinit/internal/bundle"
	"envinit/internal/inventory"
)

func TestKylinCentOSNetworkManagerFilesFromPlanning(t *testing.T) {
	root := runKylinCentOSNetworkFilesFromPlanning(t, "networkmanager")
	assertCommonKylinCentOSIfcfgFiles(t, root, "yes")
	assertFileContainsAll(t, root, nmDispatcherFile, []string{
		"kunlun-config_rt_xgbe1.sh",
		"kunlun-config_rt_xgbe4.sh",
	})
}

func TestKylinCentOSLegacyNetworkFilesFromPlanning(t *testing.T) {
	root := runKylinCentOSNetworkFilesFromPlanning(t, "network")
	assertCommonKylinCentOSIfcfgFiles(t, root, "no")
	if _, err := os.Stat(resolveTargetPath(root, nmDispatcherFile)); err == nil {
		t.Fatalf("did not expect NetworkManager dispatcher for legacy network backend")
	}
}

func runKylinCentOSNetworkFilesFromPlanning(t *testing.T, backend string) string {
	t.Helper()
	tmp := t.TempDir()
	planningDir := filepath.Join(tmp, "planning")
	if err := os.MkdirAll(planningDir, 0o755); err != nil {
		t.Fatalf("mkdir planning dir: %v", err)
	}
	bundlePath := filepath.Join(planningDir, "bundle.json")
	inventoryPath := filepath.Join(planningDir, "inventory.csv")
	bundleJSON := strings.ReplaceAll(`{
  "defaults": {
    "configure_management_network": true,
    "apply_network_immediately": false,
    "mgmt_bond_name": "bond0",
    "mgmt_prefix": 24,
    "mgmt_gateway": "10.101.9.1",
    "mgmt_mtu": 1500,
    "bond_mode": "802.3ad",
    "bond_lacp_rate": "slow",
    "bond_transmit_hash_policy": "layer3+4",
    "rdma_mtu": 9000,
    "rdma_mode": "full",
    "route_priority": 32761
  },
  "platform": {
    "os_family": "kylin",
    "package_manager": "yum",
    "network_backend": "{{backend}}"
  }
}`, "{{backend}}", backend)
	inventoryCSV := strings.Join([]string{
		"host_id,hostname,mgmt_ip,mgmt_prefix,mgmt_gateway,mgmt_iface1,mgmt_mac1,mgmt_iface2,mgmt_mac2,rdma1_name,rdma1_ip,rdma1_prefix,rdma1_gateway,rdma1_mac,rdma2_name,rdma2_ip,rdma2_prefix,rdma2_gateway,rdma2_mac,rdma3_name,rdma3_ip,rdma3_prefix,rdma3_gateway,rdma3_mac,rdma4_name,rdma4_ip,rdma4_prefix,rdma4_gateway,rdma4_mac",
		"xpu11,xpu11,10.101.9.11,24,10.101.9.1,ens20f0np0,aa:bb:cc:dd:ee:01,ens20f1np1,aa:bb:cc:dd:ee:02,xgbe1,172.18.12.10,25,172.18.12.126,aa:bb:cc:dd:ee:11,xgbe2,172.18.12.138,25,172.18.12.254,aa:bb:cc:dd:ee:12,xgbe3,172.18.13.10,25,172.18.13.126,aa:bb:cc:dd:ee:13,xgbe4,172.18.13.138,25,172.18.13.254,aa:bb:cc:dd:ee:14",
		"",
	}, "\n")
	if err := os.WriteFile(bundlePath, []byte(bundleJSON), 0o644); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	if err := os.WriteFile(inventoryPath, []byte(inventoryCSV), 0o644); err != nil {
		t.Fatalf("write inventory: %v", err)
	}

	root := filepath.Join(tmp, "root")
	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir fake bin dir: %v", err)
	}
	for _, name := range []string{"systemctl", "udevadm"} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, dev := range []struct {
		name string
		mac  string
		pci  string
	}{
		{"ens20f0np0", "aa:bb:cc:dd:ee:01", "0000:20:00.0"},
		{"ens20f1np1", "aa:bb:cc:dd:ee:02", "0000:21:00.0"},
		{"xgbe1", "aa:bb:cc:dd:ee:11", "0000:41:00.0"},
		{"xgbe2", "aa:bb:cc:dd:ee:12", "0000:42:00.0"},
		{"xgbe3", "aa:bb:cc:dd:ee:13", "0000:43:00.0"},
		{"xgbe4", "aa:bb:cc:dd:ee:14", "0000:44:00.0"},
	} {
		mustWriteNetDeviceWithSpeed(t, root, dev.name, dev.mac, dev.pci, "mlx5_core", 0, "p0", 400000)
	}

	b, err := bundle.Load(bundlePath)
	if err != nil {
		t.Fatalf("load bundle: %v", err)
	}
	if b.Platform.OSFamily != "kylin" || b.Platform.PackageManager != "yum" || b.Platform.NetworkBackend != backend {
		t.Fatalf("explicit platform was not preserved: %#v", b.Platform)
	}
	records, err := inventory.Load(inventoryPath, "")
	if err != nil {
		t.Fatalf("load inventory: %v", err)
	}
	app, err := New(b, records, "xpu11", root, true, map[string]bool{"network": true}, nil)
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	app.DryRun = false
	app.now = func() time.Time { return time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC) }
	if backend == "networkmanager" {
		if err := app.runNetworkManagerStage(); err != nil {
			t.Fatalf("run networkmanager stage: %v", err)
		}
	} else {
		if err := app.runLegacyNetworkStage(); err != nil {
			t.Fatalf("run legacy network stage: %v", err)
		}
	}
	return root
}

func assertCommonKylinCentOSIfcfgFiles(t *testing.T, root string, nmControlled string) {
	t.Helper()
	assertFileContainsAll(t, root, ifcfgPath("bond0"), []string{
		"DEVICE=bond0",
		"TYPE=Bond",
		"BOOTPROTO=static",
		"IPADDR=10.101.9.11",
		"PREFIX=24",
		"GATEWAY=10.101.9.1",
		"NM_CONTROLLED=" + nmControlled,
		`BONDING_OPTS="mode=802.3ad miimon=100 lacp_rate=slow xmit_hash_policy=layer3+4"`,
	})
	for _, iface := range []string{"ens20f0np0", "ens20f1np1"} {
		assertFileContainsAll(t, root, ifcfgPath(iface), []string{
			"BOOTPROTO=none",
			"MASTER=bond0",
			"SLAVE=yes",
			"NM_CONTROLLED=" + nmControlled,
		})
	}
	assertFileContainsAll(t, root, ifcfgPath("xgbe1"), []string{
		"DEVICE=xgbe1",
		"BOOTPROTO=static",
		"IPADDR=172.18.12.10",
		"PREFIX=25",
		"MTU=9000",
		"NM_CONTROLLED=" + nmControlled,
	})
	assertFileContainsAll(t, root, ifcfgRoutePath("xgbe1"), []string{
		"default via 172.18.12.126 dev xgbe1 table 101",
		"172.18.12.0/25 dev xgbe1 scope link table 101 src 172.18.12.10 proto static",
	})
	assertFileContainsAll(t, root, ifcfgRoutePath("xgbe2"), []string{
		"default via 172.18.12.254 dev xgbe2 table 102",
		"172.18.12.128/25 dev xgbe2 scope link table 102 src 172.18.12.138 proto static",
	})
	assertFileContainsAll(t, root, ifcfgRulePath("xgbe1"), []string{
		"from all oif xgbe1 table 101 priority 32761",
		"from 172.18.12.10 table 101 priority 32761",
	})
	assertFileContainsAll(t, root, aRouteScriptPath("xgbe1"), []string{
		`ROUTE_CIDR="172.18.12.0/25"`,
		`ip route replace "$ROUTE_CIDR" dev "$DEV" scope link table "$TABLE" src "$IP" proto static`,
	})
	assertFileContainsAll(t, root, managementUdevFile, []string{
		`ATTR{address}=="aa:bb:cc:dd:ee:01"`,
		`NAME="ens20f0np0"`,
	})
	assertFileContainsAll(t, root, rdmaUdevFile, []string{
		`ATTR{address}=="aa:bb:cc:dd:ee:14"`,
		`NAME="xgbe4"`,
	})
}

func aRouteScriptPath(iface string) string {
	return filepath.Join("/usr/local/sbin", "kunlun-config_rt_"+iface+".sh")
}

func assertFileContainsAll(t *testing.T, root string, systemPath string, wants []string) {
	t.Helper()
	content, err := os.ReadFile(resolveTargetPath(root, systemPath))
	if err != nil {
		t.Fatalf("read %s: %v", systemPath, err)
	}
	for _, want := range wants {
		if !strings.Contains(string(content), want) {
			t.Fatalf("expected %q in %s:\n%s", want, systemPath, content)
		}
	}
}
