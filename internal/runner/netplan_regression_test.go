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

func TestUbuntuNetplanFilesFromPlanning(t *testing.T) {
	tmp := t.TempDir()
	planningDir := filepath.Join(tmp, "planning")
	if err := os.MkdirAll(planningDir, 0o755); err != nil {
		t.Fatalf("mkdir planning dir: %v", err)
	}
	bundlePath := filepath.Join(planningDir, "bundle.json")
	inventoryPath := filepath.Join(planningDir, "inventory.csv")
	bundleJSON := `{
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
    "os_family": "ubuntu",
    "package_manager": "apt",
    "network_backend": "netplan"
  }
}`
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
	if err := os.WriteFile(filepath.Join(binDir, "udevadm"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake udevadm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "dpkg"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake dpkg: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "systemctl"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake systemctl: %v", err)
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
	if b.Platform.OSFamily != "ubuntu" || b.Platform.PackageManager != "apt" || b.Platform.NetworkBackend != "netplan" {
		t.Fatalf("explicit ubuntu platform was not preserved: %#v", b.Platform)
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
	if err := app.runNetworkStage(); err != nil {
		t.Fatalf("run netplan network stage: %v", err)
	}

	assertFileContainsAll(t, root, filepath.Join(netplanDir, "00-kunlun-bond.yaml"), []string{
		"renderer: networkd",
		"ens20f0np0: {}",
		"ens20f1np1: {}",
		"bond0:",
		"interfaces: [ens20f0np0, ens20f1np1]",
		"- 10.101.9.11/24",
		"mtu: 1500",
		"mode: 802.3ad",
		"lacp-rate: slow",
		"transmit-hash-policy: layer3+4",
		"via: 10.101.9.1",
	})
	assertFileContainsAll(t, root, filepath.Join(netplanDir, "10-kunlun-xgbe1.yaml"), []string{
		"renderer: networkd",
		"xgbe1:",
		"- 172.18.12.10/25",
		"ignore-carrier: true",
		"mtu: 9000",
	})
	assertFileContainsAll(t, root, filepath.Join(netplanDir, "10-kunlun-xgbe2.yaml"), []string{
		"xgbe2:",
		"- 172.18.12.138/25",
	})
	assertFileContainsAll(t, root, filepath.Join(routeDir, "config_rt_xgbe1.sh"), []string{
		`IP="172.18.12.10"`,
		`DEV="xgbe1"`,
		`TABLE="101"`,
		`GW="172.18.12.126"`,
		`ROUTE_CIDR="172.18.12.0/25"`,
		`ip route replace "$ROUTE_CIDR" dev "$DEV" scope link table "$TABLE" src "$IP" proto static`,
		`ip rule add from "$IP" table "$TABLE" priority "$PRIORITY"`,
	})
	assertFileContainsAll(t, root, managementUdevFile, []string{
		`ATTR{address}=="aa:bb:cc:dd:ee:01"`,
		`NAME="ens20f0np0"`,
	})
	assertFileContainsAll(t, root, rdmaUdevFile, []string{
		`ATTR{address}=="aa:bb:cc:dd:ee:12"`,
		`NAME="xgbe2"`,
	})
}
