package spec

import "testing"

func TestApplyDefaultsSetsOfflineAPTCopyTarget(t *testing.T) {
	b := Bundle{
		OfflineAPT: OfflineAPTConfig{
			MaterialPath: "/mnt/usb/repo",
		},
	}
	b.ApplyDefaults()
	if b.OfflineAPT.CopyTo != "/opt/repo" {
		t.Fatalf("unexpected offline apt copy target: %s", b.OfflineAPT.CopyTo)
	}
}

func TestApplyDefaultsSetsActiveBackupMII(t *testing.T) {
	b := Bundle{
		Defaults: Defaults{
			BondMode: "active-backup",
		},
	}
	b.ApplyDefaults()
	if b.Defaults.BondMIIMonitorInterval != 100 {
		t.Fatalf("unexpected active-backup mii monitor interval: %d", b.Defaults.BondMIIMonitorInterval)
	}
	if b.Defaults.BondLACPRate != "" || b.Defaults.BondTransmitHashPolicy != "" {
		t.Fatalf("did not expect 802.3ad-only defaults for active-backup: lacp=%q hash=%q", b.Defaults.BondLACPRate, b.Defaults.BondTransmitHashPolicy)
	}
}

func TestResolveMachineRejectsInvalidActiveBackupPrimary(t *testing.T) {
	disabled := false
	b := Bundle{
		Defaults: Defaults{
			MgmtInterfaces: []string{"enp2s0", "enp3s0"},
			BondMode:       "active-backup",
			BondPrimary:    "enp9s0",
			RDMAExsist:     &disabled,
		},
	}
	b.ApplyDefaults()
	_, err := ResolveMachine(b, MachineRecord{
		HostID: "node01",
		MgmtIP: "172.16.18.11",
	}, nil)
	if err == nil {
		t.Fatal("expected invalid bond primary to fail")
	}
}

func TestResolveMachineSkipsRDMAWhenDisabled(t *testing.T) {
	disabled := false
	b := Bundle{
		Defaults: Defaults{
			RDMAExsist: &disabled,
		},
	}
	b.ApplyDefaults()
	cfg, err := ResolveMachine(b, MachineRecord{
		HostID:     "node01",
		MgmtIP:     "172.16.18.11",
		MgmtIface1: "enp2s0",
	}, nil)
	if err != nil {
		t.Fatalf("resolve machine: %v", err)
	}
	if len(cfg.RDMA) != 0 {
		t.Fatalf("expected no RDMA interfaces, got %#v", cfg.RDMA)
	}
}

func TestTopLevelRDMAExistOverridesDefaults(t *testing.T) {
	enabled := true
	disabled := false
	b := Bundle{
		TopLevelRDMAExist: &disabled,
		Defaults: Defaults{
			RDMAExsist: &enabled,
		},
	}
	if b.RDMAExists() {
		t.Fatal("expected top-level rdma_exist=false to override defaults.rdma_exsist=true")
	}
}

func TestTopLevelRDMAConfigureIPRouteOverridesDefaults(t *testing.T) {
	enabled := true
	disabled := false
	b := Bundle{
		TopLevelRDMAConfigureIPRoute: &disabled,
		Defaults: Defaults{
			RDMAConfigureIPRoute: &enabled,
		},
	}
	if b.RDMAConfigureIPRoute() {
		t.Fatal("expected top-level rdma_configure_ip_route=false to override defaults.rdma_configure_ip_route=true")
	}
}

func TestResolveMachineAllowsBlankRDMAIPWhenRouteConfigDisabled(t *testing.T) {
	disabled := false
	b := Bundle{
		Defaults: Defaults{
			RDMAConfigureIPRoute: &disabled,
			RDMAInterfaces: []RDMAInterfaceDefault{
				{Name: "ens11np0"},
				{Name: "ens13np0"},
				{Name: "ens15np0"},
				{Name: "ens17np0"},
			},
		},
	}
	b.ApplyDefaults()
	cfg, err := ResolveMachine(b, MachineRecord{
		HostID:     "node01",
		MgmtIP:     "172.16.18.11",
		MgmtIface1: "enp2s0",
	}, nil)
	if err != nil {
		t.Fatalf("resolve machine: %v", err)
	}
	if len(cfg.RDMA) != 4 {
		t.Fatalf("expected default RDMA interfaces, got %#v", cfg.RDMA)
	}
	if cfg.RDMA[0].IP != "" {
		t.Fatalf("expected blank RDMA IP when route config is disabled, got %q", cfg.RDMA[0].IP)
	}
	if cfg.RDMA[0].Table != 0 {
		t.Fatalf("expected omitted route table to stay unused as 0, got %d", cfg.RDMA[0].Table)
	}
}
