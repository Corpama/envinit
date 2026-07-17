package spec

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCheckConfigUnmarshalAcceptsNestedBandwidth(t *testing.T) {
	var cfg CheckConfig
	if err := json.Unmarshal([]byte(`{
		"bandwidth": {"iterations": 77, "min_gbits": 380},
		"rdma_ping": {"count": 4, "payload_size": 8972, "timeout": 3},
		"ssh": {"user": "root", "options": ["-p", "22"]}
	}`), &cfg); err != nil {
		t.Fatalf("unmarshal nested check config: %v", err)
	}
	if cfg.Bandwidth.Iterations != 77 || cfg.Bandwidth.MinGBits != 380 {
		t.Fatalf("unexpected nested bandwidth config: %#v", cfg.Bandwidth)
	}
	if cfg.RDMAPing.Count != 4 || cfg.RDMAPing.PayloadSize != 8972 || cfg.RDMAPing.Timeout != 3 {
		t.Fatalf("unexpected nested RDMA ping config: %#v", cfg.RDMAPing)
	}
	if cfg.SSH.User != "root" || len(cfg.SSH.Options) != 2 {
		t.Fatalf("unexpected nested SSH config: %#v", cfg.SSH)
	}
}

func TestCheckConfigUnmarshalAcceptsLegacyFlatBandwidth(t *testing.T) {
	var cfg CheckConfig
	if err := json.Unmarshal([]byte(`{
		"iterations": 77,
		"min_gbits": 380,
		"rdma_groups": [{"ib_device": "mlx5_1", "xpu_offsets": []}],
		"rdma_ping_count": 4,
		"rdma_ping_payload_size": 8972,
		"rdma_ping_timeout": 3,
		"ssh_user": "root",
		"ssh_options": ["-p", "22"]
	}`), &cfg); err != nil {
		t.Fatalf("unmarshal legacy check config: %v", err)
	}
	if cfg.Bandwidth.Iterations != 77 || cfg.Bandwidth.MinGBits != 380 {
		t.Fatalf("unexpected legacy bandwidth config: %#v", cfg.Bandwidth)
	}
	if len(cfg.Bandwidth.RDMAGroups) != 1 || cfg.Bandwidth.RDMAGroups[0].IBDevice != "mlx5_1" {
		t.Fatalf("unexpected legacy RDMA groups: %#v", cfg.Bandwidth.RDMAGroups)
	}
	if cfg.RDMAPing.Count != 4 || cfg.RDMAPing.PayloadSize != 8972 || cfg.RDMAPing.Timeout != 3 {
		t.Fatalf("unexpected legacy RDMA ping config: %#v", cfg.RDMAPing)
	}
	if cfg.SSH.User != "root" || len(cfg.SSH.Options) != 2 {
		t.Fatalf("unexpected legacy SSH config: %#v", cfg.SSH)
	}
}

func TestCheckConfigNestedBandwidthTakesPrecedenceOverLegacyFields(t *testing.T) {
	var cfg CheckConfig
	if err := json.Unmarshal([]byte(`{
		"iterations": 99,
		"min_gbits": 100,
		"bandwidth": {"iterations": 77, "min_gbits": 380},
		"rdma_ping_count": 9,
		"rdma_ping": {"count": 4},
		"ssh_user": "legacy",
		"ssh": {"user": "nested"}
	}`), &cfg); err != nil {
		t.Fatalf("unmarshal mixed check config: %v", err)
	}
	if cfg.Bandwidth.Iterations != 77 || cfg.Bandwidth.MinGBits != 380 {
		t.Fatalf("nested bandwidth should take precedence: %#v", cfg.Bandwidth)
	}
	if cfg.RDMAPing.Count != 4 {
		t.Fatalf("nested RDMA ping should take precedence: %#v", cfg.RDMAPing)
	}
	if cfg.SSH.User != "nested" {
		t.Fatalf("nested SSH should take precedence: %#v", cfg.SSH)
	}
}

func TestCheckConfigRejectsUnknownTopLevelField(t *testing.T) {
	var cfg CheckConfig
	err := json.Unmarshal([]byte(`{"bandwidth": {}, "unexpected": true}`), &cfg)
	if err == nil || !strings.Contains(err.Error(), `unknown field "unexpected"`) {
		t.Fatalf("expected unknown check field error, got %v", err)
	}
}

func TestCheckConfigRejectsUnknownNestedField(t *testing.T) {
	var cfg CheckConfig
	err := json.Unmarshal([]byte(`{"ssh": {"user": "root", "unexpected": true}}`), &cfg)
	if err == nil || !strings.Contains(err.Error(), `unknown field "unexpected"`) {
		t.Fatalf("expected unknown nested check field error, got %v", err)
	}
}

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

func TestApplyDefaultsSetsXCCLRuntimeDefaults(t *testing.T) {
	var b Bundle
	b.ApplyDefaults()
	if b.Check.XCCL.WorkRoot != "/tmp/envinit-xccl-check" || b.Check.XCCL.XPUHome != "/usr/local/xpu" {
		t.Fatalf("unexpected XCCL runtime defaults: %#v", b.Check.XCCL)
	}
	if b.Check.XCCL.Test != "all_reduce" || b.Check.XCCL.MinBytes != "128m" || b.Check.XCCL.MaxBytes != "128m" {
		t.Fatalf("unexpected XCCL performance defaults: %#v", b.Check.XCCL)
	}
	if b.Check.XCCL.EnableXDR == nil || !*b.Check.XCCL.EnableXDR {
		t.Fatalf("XCCL XDR should default to enabled: %#v", b.Check.XCCL.EnableXDR)
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
			Kylin: YumPlatformOptions{
				BackupExistingNetwork: &enabled,
				DisableExistingRepos:  &disabled,
			},
		},
		Platform: PlatformConfig{
			OSFamily: "kylin",
		},
	}
	if !b.BackupExistingNetplan() {
		t.Fatal("expected ubuntu platform option to enable netplan backup")
	}
	if b.DisableExistingAptSources() {
		t.Fatal("expected ubuntu platform option to disable apt source backup")
	}
	if !b.BackupExistingNetwork() {
		t.Fatal("expected kylin platform option to enable network backup")
	}
	if b.DisableExistingRepos() {
		t.Fatal("expected kylin platform option to disable repo backup")
	}
}

func TestKylinPlatformOptionsFallbackToLegacyRedHatKey(t *testing.T) {
	enabled := true
	disabled := false
	b := Bundle{
		Platform: PlatformConfig{
			OSFamily: "kylin",
		},
		PlatformOptions: PlatformOptions{
			RedHat: YumPlatformOptions{
				BackupExistingNetwork: &enabled,
				DisableExistingRepos:  &disabled,
			},
		},
	}
	if !b.BackupExistingNetwork() {
		t.Fatal("expected legacy redhat platform option to enable network backup for kylin")
	}
	if b.DisableExistingRepos() {
		t.Fatal("expected legacy redhat platform option to disable repo backup for kylin")
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
	b := Bundle{
		Defaults: Defaults{
			MgmtInterfaces: []string{"enp2s0", "enp3s0"},
			BondMode:       "active-backup",
			BondPrimary:    "enp9s0",
			RDMAMode:       RDMAModeOff,
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
	b := Bundle{
		Defaults: Defaults{
			RDMAMode: RDMAModeOff,
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
	b := Bundle{
		Defaults: Defaults{
			MgmtInterfaces: []string{"enp2s0", "enp3s0"},
			MgmtGateway:    "172.16.18.1",
			RDMAMode:       RDMAModeOff,
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

func TestRDMAModeDefaultsToFull(t *testing.T) {
	b := Bundle{
		Defaults: Defaults{},
	}
	b.ApplyDefaults()
	if !b.RDMAExists() || !b.RDMAConfigureIPRoute() {
		t.Fatal("expected blank rdma_mode to default to full")
	}
}

func TestRDMAModeNamesOnlyKeepsRDMAWithoutIPRoute(t *testing.T) {
	b := Bundle{
		Defaults: Defaults{
			RDMAMode: RDMAModeNamesOnly,
		},
	}
	if !b.RDMAExists() || b.RDMAConfigureIPRoute() {
		t.Fatal("expected rdma_mode=names_only to keep RDMA enabled and disable IP route config")
	}
}

func TestApplyDefaultsDoesNotInjectRDMAInterfaces(t *testing.T) {
	b := Bundle{}
	b.ApplyDefaults()
	if len(b.Defaults.RDMAInterfaces) != 0 {
		t.Fatalf("expected RDMA interface defaults to remain empty, got %#v", b.Defaults.RDMAInterfaces)
	}
}

func TestValidateRejectsInvalidRDMAMode(t *testing.T) {
	b := Bundle{
		Defaults: Defaults{
			RDMAMode: "name_only",
		},
	}
	b.ApplyDefaults()
	if err := b.Validate(); err == nil {
		t.Fatal("expected invalid rdma_mode to fail validation")
	}
}

func TestResolveMachineAllowsBlankRDMAIPWhenRouteConfigDisabled(t *testing.T) {
	b := Bundle{
		Defaults: Defaults{
			RDMAMode: RDMAModeNamesOnly,
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

func TestResolveMachineUsesDynamicRDMAInventoryCount(t *testing.T) {
	b := Bundle{}
	b.ApplyDefaults()
	record := MachineRecord{
		HostID:     "xpu21",
		MgmtIP:     "10.61.10.43",
		MgmtIface1: "eth0",
		RDMA:       make([]RDMARecord, 8),
	}
	for idx := range record.RDMA {
		record.RDMA[idx] = RDMARecord{
			Name: fmt.Sprintf("ens%d", idx+1),
			IP:   fmt.Sprintf("10.61.%d.43", 11+idx),
		}
	}

	cfg, err := ResolveMachine(b, record, nil)
	if err != nil {
		t.Fatalf("resolve machine: %v", err)
	}
	if len(cfg.RDMA) != 8 {
		t.Fatalf("expected 8 RDMA configs, got %d: %#v", len(cfg.RDMA), cfg.RDMA)
	}
	if cfg.RDMA[4].Name != "ens5" || cfg.RDMA[4].Table != 105 {
		t.Fatalf("unexpected rdma5 config: %#v", cfg.RDMA[4])
	}
	if cfg.RDMA[7].Name != "ens8" || cfg.RDMA[7].IP != "10.61.18.43" || cfg.RDMA[7].Table != 108 {
		t.Fatalf("unexpected rdma8 config: %#v", cfg.RDMA[7])
	}
}

func TestResolveMachineInventoryCountOverridesBundleDefaults(t *testing.T) {
	b := Bundle{
		Defaults: Defaults{
			RDMAMode: RDMAModeNamesOnly,
			RDMAInterfaces: []RDMAInterfaceDefault{
				{Name: "default1", Table: 101},
				{Name: "default2", Table: 102},
				{Name: "default3", Table: 103},
				{Name: "default4", Table: 104},
			},
		},
	}
	b.ApplyDefaults()
	cfg, err := ResolveMachine(b, MachineRecord{
		HostID: "node01",
		RDMA: []RDMARecord{
			{Name: "ens1"},
			{Name: "ens2"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("resolve machine: %v", err)
	}
	if len(cfg.RDMA) != 2 {
		t.Fatalf("expected inventory to define exactly 2 RDMA interfaces, got %#v", cfg.RDMA)
	}
	if cfg.RDMA[0].Name != "ens1" || cfg.RDMA[1].Name != "ens2" {
		t.Fatalf("unexpected resolved RDMA interfaces: %#v", cfg.RDMA)
	}
}

func TestResolveMachineAssignsLogicalNamesToIPOnlyRDMAInventory(t *testing.T) {
	b := Bundle{}
	b.ApplyDefaults()
	cfg, err := ResolveMachine(b, MachineRecord{
		HostID: "node01",
		RDMA: []RDMARecord{
			{IP: "10.1.1.1"},
			{IP: "10.1.2.1"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("resolve machine: %v", err)
	}
	if len(cfg.RDMA) != 2 || cfg.RDMA[0].Name != "rdma1" || cfg.RDMA[1].Name != "rdma2" {
		t.Fatalf("expected dynamic logical RDMA names, got %#v", cfg.RDMA)
	}
}
