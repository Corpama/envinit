package spec

import (
	"strings"
	"testing"
)

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

func TestApplyDefaultsGeneratesOfflineAPTEntries(t *testing.T) {
	b := Bundle{
		Platform: PlatformConfig{
			PackageManager: "apt",
		},
		OfflineAPT: OfflineAPTConfig{
			Enabled:      true,
			MaterialPath: "/mnt/usb/repo",
		},
	}
	b.ApplyDefaults()
	want := "deb [trusted=yes] file:{{offline_apt_target}} ./"
	if len(b.OfflineAPT.Entries) != 1 || b.OfflineAPT.Entries[0] != want {
		t.Fatalf("unexpected offline apt entries: %#v", b.OfflineAPT.Entries)
	}
}

func TestApplyDefaultsSetsYumPlatformDefaults(t *testing.T) {
	b := Bundle{
		Platform: PlatformConfig{
			OSFamily: "kylin",
		},
		OfflineRepo: OfflineAPTConfig{
			Enabled:      true,
			MaterialPath: "/mnt/usb/rpm-repo",
		},
	}
	b.ApplyDefaults()
	if b.Platform.PackageManager != "yum" {
		t.Fatalf("unexpected package manager: %s", b.Platform.PackageManager)
	}
	if b.Platform.NetworkBackend != "auto" {
		t.Fatalf("unexpected network backend: %s", b.Platform.NetworkBackend)
	}
	if b.Platform.KernelHeadersPackage != "kernel-devel-{{uname_r}}" {
		t.Fatalf("unexpected kernel devel package: %s", b.Platform.KernelHeadersPackage)
	}
	if b.Platform.KernelHeadersDir != "/usr/src/kernels/{{uname_r}}" {
		t.Fatalf("unexpected kernel devel dir: %s", b.Platform.KernelHeadersDir)
	}
	if b.OfflineRepo.TargetFile != "/etc/yum.repos.d/kunlun-offline.repo" {
		t.Fatalf("unexpected yum repo target: %s", b.OfflineRepo.TargetFile)
	}
	if b.OfflineRepo.CopyTo != "/opt/rpm-repo" {
		t.Fatalf("unexpected offline repo copy target: %s", b.OfflineRepo.CopyTo)
	}
}

func TestApplyDefaultsGeneratesOfflineYumRepoEntries(t *testing.T) {
	b := Bundle{
		Platform: PlatformConfig{
			PackageManager: "yum",
		},
		OfflineRepo: OfflineAPTConfig{
			Enabled:      true,
			MaterialPath: "/mnt/usb/rpm-repo",
		},
	}
	b.ApplyDefaults()
	want := []string{
		"[kunlun-offline]",
		"name=Kunlun Offline",
		"baseurl=file://{{offline_repo_target}}",
		"enabled=1",
		"gpgcheck=0",
	}
	if strings.Join(b.OfflineRepo.Entries, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unexpected offline repo entries: %#v", b.OfflineRepo.Entries)
	}
}

func TestApplyDefaultsSetsUbuntuPlatformDefaults(t *testing.T) {
	b := Bundle{
		Platform: PlatformConfig{
			OSFamily: "ubuntu",
		},
	}
	b.ApplyDefaults()
	if b.Platform.PackageManager != "apt" {
		t.Fatalf("unexpected package manager: %s", b.Platform.PackageManager)
	}
	if b.Platform.NetworkBackend != "netplan" {
		t.Fatalf("unexpected network backend: %s", b.Platform.NetworkBackend)
	}
}

func TestApplyDefaultsInfersUbuntuFromAPT(t *testing.T) {
	b := Bundle{
		Platform: PlatformConfig{
			PackageManager: "apt",
		},
	}
	b.ApplyDefaults()
	if b.Platform.OSFamily != "ubuntu" {
		t.Fatalf("unexpected os family: %s", b.Platform.OSFamily)
	}
	if b.Platform.NetworkBackend != "netplan" {
		t.Fatalf("unexpected network backend: %s", b.Platform.NetworkBackend)
	}
}

func TestApplyDefaultsTreatsAutoPlatformFieldsAsUnset(t *testing.T) {
	b := Bundle{
		Platform: PlatformConfig{
			OSFamily:       "auto",
			PackageManager: "auto",
			NetworkBackend: "auto",
		},
	}
	b.ApplyDefaults()
	if b.Platform.OSFamily != "redhat" {
		t.Fatalf("unexpected os family: %s", b.Platform.OSFamily)
	}
	if b.Platform.PackageManager != "yum" {
		t.Fatalf("unexpected package manager: %s", b.Platform.PackageManager)
	}
	if b.Platform.NetworkBackend != "auto" {
		t.Fatalf("unexpected network backend: %s", b.Platform.NetworkBackend)
	}
}

func TestValidateRejectsUbuntuNetworkManagerBackend(t *testing.T) {
	b := Bundle{
		Platform: PlatformConfig{
			OSFamily:       "ubuntu",
			PackageManager: "apt",
			NetworkBackend: "Networkmanager",
		},
	}
	b.ApplyDefaults()
	err := b.Validate()
	if err == nil {
		t.Fatal("expected ubuntu with networkmanager backend to fail")
	}
	if !strings.Contains(err.Error(), `platform.network_backend "networkmanager" is not supported`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAllowsUbuntuNetplanBackend(t *testing.T) {
	b := Bundle{
		Platform: PlatformConfig{
			OSFamily:       "ubuntu",
			PackageManager: "apt",
			NetworkBackend: "NetPlan",
		},
	}
	b.ApplyDefaults()
	if err := b.Validate(); err != nil {
		t.Fatalf("expected ubuntu netplan backend to pass: %v", err)
	}
}

func TestValidateAllowsYumNetworkManagerBackend(t *testing.T) {
	b := Bundle{
		Platform: PlatformConfig{
			OSFamily:       "kylin",
			PackageManager: "yum",
			NetworkBackend: "NetworkManager",
		},
	}
	b.ApplyDefaults()
	if err := b.Validate(); err != nil {
		t.Fatalf("expected yum networkmanager backend to pass: %v", err)
	}
}

func TestPlatformOptionsOverrideLegacyDefaultPolicyFields(t *testing.T) {
	enabled := true
	disabled := false
	b := Bundle{
		Defaults: Defaults{
			BackupExistingNetplan:     false,
			BackupExistingNetwork:     false,
			DisableExistingAptSources: true,
			DisableExistingRepos:      true,
		},
		PlatformOptions: PlatformOptions{
			Ubuntu: UbuntuPlatformOptions{
				BackupExistingNetplan:     &enabled,
				DisableExistingAptSources: &disabled,
			},
			RedHat: RedHatPlatformOptions{
				BackupExistingNetwork: &enabled,
				DisableExistingRepos:  &disabled,
			},
		},
	}
	if !b.BackupExistingNetplan() {
		t.Fatal("expected ubuntu platform option to enable netplan backup")
	}
	if b.DisableExistingAptSources() {
		t.Fatal("expected ubuntu platform option to disable apt source backup")
	}
	if !b.BackupExistingNetwork() {
		t.Fatal("expected redhat platform option to enable network backup")
	}
	if b.DisableExistingRepos() {
		t.Fatal("expected redhat platform option to disable repo backup")
	}
}

func TestNetworkControlDefaultsAreEnabled(t *testing.T) {
	b := Bundle{}
	if !b.ConfigureManagementNetwork() {
		t.Fatal("expected management network configuration to default on")
	}
	if !b.ApplyNetworkImmediately() {
		t.Fatal("expected immediate network apply to default on")
	}
	disabled := false
	b.Defaults.ConfigureManagementNetwork = &disabled
	b.Defaults.ApplyNetworkImmediately = &disabled
	if b.ConfigureManagementNetwork() {
		t.Fatal("expected explicit configure_management_network=false")
	}
	if b.ApplyNetworkImmediately() {
		t.Fatal("expected explicit apply_network_immediately=false")
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

func TestResolveMachineSkipsManagementWhenMgmtIPIsBlank(t *testing.T) {
	disabled := false
	b := Bundle{
		Defaults: Defaults{
			MgmtInterfaces: []string{"enp2s0", "enp3s0"},
			MgmtGateway:    "172.16.18.1",
			RDMAExsist:     &disabled,
		},
	}
	b.ApplyDefaults()
	cfg, err := ResolveMachine(b, MachineRecord{
		HostID: "node01",
	}, nil)
	if err != nil {
		t.Fatalf("resolve machine: %v", err)
	}
	if cfg.MgmtIP != "" || cfg.MgmtGateway != "" || len(cfg.MgmtIfaces) != 0 || len(cfg.MgmtMACs) != 0 {
		t.Fatalf("expected management network to be skipped, got ip=%q gateway=%q ifaces=%v macs=%v", cfg.MgmtIP, cfg.MgmtGateway, cfg.MgmtIfaces, cfg.MgmtMACs)
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
