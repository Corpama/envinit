package main

import (
	"reflect"
	"strings"
	"testing"

	"envinit/internal/runner"
	"envinit/internal/spec"
)

func TestParseStagesAcceptsKnownStages(t *testing.T) {
	got, err := parseStages("network,udev,sysctl,kernel")
	if err != nil {
		t.Fatalf("parse stages: %v", err)
	}
	want := map[string]bool{
		"network": true,
		"udev":    true,
		"sysctl":  true,
		"kernel":  true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected stages: got=%v want=%v", got, want)
	}
}

func TestParseStagesCanonicalizesKernelAliases(t *testing.T) {
	got, err := parseStages("iommu kernel-params")
	if err != nil {
		t.Fatalf("parse stages: %v", err)
	}
	want := map[string]bool{
		"kernel": true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected stages: got=%v want=%v", got, want)
	}
}

func TestGroupDryRunLogByStage(t *testing.T) {
	groups := groupDryRunLogByStage(strings.Join([]string{
		"set hostname localhost -> xpu11",
		"==> stage: udev",
		"write /etc/udev/rules.d/70-persistent-net.rules",
		"run: udevadm control --reload-rules",
		"==> stage: network",
		"write /etc/sysconfig/network-scripts/ifcfg-bond0",
	}, "\n"))

	if len(groups.Prelude) != 1 || groups.Prelude[0] != "set hostname localhost -> xpu11" {
		t.Fatalf("unexpected prelude: %#v", groups.Prelude)
	}
	wantOrder := []string{"udev", "network"}
	if !reflect.DeepEqual(groups.Order, wantOrder) {
		t.Fatalf("unexpected stage order: got=%v want=%v", groups.Order, wantOrder)
	}
	if got := groups.ByStage["udev"]; len(got) != 2 {
		t.Fatalf("unexpected udev actions: %#v", got)
	}
}

func TestRenderPlanPreviewGroupsStageDetails(t *testing.T) {
	app := fakePlanApp()
	got := renderPlanPreview(app, strings.Join([]string{
		"hostname already xpu11",
		"==> stage: network",
		"write /etc/sysconfig/network-scripts/ifcfg-bond0",
		"run: nmcli connection reload",
	}, "\n"))

	for _, want := range []string{
		"Plan preview (dry-run; no changes have been made)",
		"Platform: os_family=kylin package_manager=yum network_backend=auto",
		"Stages",
		"  - network (2 actions)",
		"[network]",
		"  - write /etc/sysconfig/network-scripts/ifcfg-bond0",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in plan preview:\n%s", want, got)
		}
	}
}

func fakePlanApp() *runner.App {
	return &runner.App{
		Bundle: spec.Bundle{
			Platform: spec.PlatformConfig{
				OSFamily:       "kylin",
				PackageManager: "yum",
				NetworkBackend: "auto",
			},
		},
		Machine: spec.MachineConfig{
			HostID:       "test1",
			Hostname:     "xpu11",
			MgmtIP:       "10.101.9.11",
			MgmtPrefix:   24,
			MgmtGateway:  "10.101.9.1",
			MgmtBondName: "bond0",
			MgmtIfaces:   []string{"ens20f0np0", "ens20f1np1"},
			RDMA: []spec.RDMAConfig{
				{Name: "ens11np0", IP: "11.1.1.11", Prefix: 24, Gateway: "11.1.1.1", Table: 101},
			},
		},
	}
}

func TestParseStagesAcceptsWhitespaceSeparatedStages(t *testing.T) {
	got, err := parseStages("network udev\tsysctl")
	if err != nil {
		t.Fatalf("parse stages: %v", err)
	}
	want := map[string]bool{
		"network": true,
		"udev":    true,
		"sysctl":  true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected stages: got=%v want=%v", got, want)
	}
}

func TestParseStagesCanonicalizesPackageAliases(t *testing.T) {
	got, err := parseStages("packages yum")
	if err != nil {
		t.Fatalf("parse stages: %v", err)
	}
	want := map[string]bool{
		"software": true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected stages: got=%v want=%v", got, want)
	}
}

func TestParseStagesRejectsUnknownStage(t *testing.T) {
	if _, err := parseStages("network,systcl"); err == nil {
		t.Fatal("expected unknown stage error")
	}
}

func TestNormalizeArgsForStagesPreservesLaterFlags(t *testing.T) {
	got, err := normalizeArgsForStages([]string{
		"--inventory", "inventory.csv",
		"--bundle", "bundle.json",
		"--stages", "network", "udev",
		"--host", "xpu11",
	})
	if err != nil {
		t.Fatalf("normalize args: %v", err)
	}
	want := []string{
		"--inventory", "inventory.csv",
		"--bundle", "bundle.json",
		"--stages", "network,udev",
		"--host", "xpu11",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected normalized args: got=%v want=%v", got, want)
	}
}

func TestNormalizeArgsForStagesSupportsEqualsForm(t *testing.T) {
	got, err := normalizeArgsForStages([]string{
		"--inventory", "inventory.csv",
		"--bundle", "bundle.json",
		"--stages=network", "udev", "sysctl",
		"--host", "xpu11",
	})
	if err != nil {
		t.Fatalf("normalize args: %v", err)
	}
	want := []string{
		"--inventory", "inventory.csv",
		"--bundle", "bundle.json",
		"--stages=network,udev,sysctl",
		"--host", "xpu11",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected normalized args: got=%v want=%v", got, want)
	}
}

func TestApplyCheckOverridesConvertsMTUToPingPayload(t *testing.T) {
	b := spec.Bundle{}
	b.ApplyDefaults()
	if err := applyCheckOverrides(&b, checkOverrideOptions{rdmaPingCount: 10, rdmaPingMTU: 9000, rdmaPingTimeout: 5}); err != nil {
		t.Fatalf("apply check overrides: %v", err)
	}
	if b.Check.RDMAPingCount != 10 {
		t.Fatalf("unexpected ping count: %d", b.Check.RDMAPingCount)
	}
	if b.Check.RDMAPingTimeout != 5 {
		t.Fatalf("unexpected ping timeout: %d", b.Check.RDMAPingTimeout)
	}
	if b.Check.RDMAPingPayloadSize != 8972 {
		t.Fatalf("unexpected ping payload size: %d", b.Check.RDMAPingPayloadSize)
	}
}

func TestApplyCheckOverridesSetsBandwidthQPs(t *testing.T) {
	b := spec.Bundle{Check: spec.CheckConfig{BandwidthQPs: 2}}
	if err := applyCheckOverrides(&b, checkOverrideOptions{bandwidthQPs: 8}); err != nil {
		t.Fatalf("apply check overrides: %v", err)
	}
	if b.Check.BandwidthQPs != 8 {
		t.Fatalf("unexpected bandwidth QP count: %d", b.Check.BandwidthQPs)
	}
}

func TestApplyCheckOverridesRejectsNegativeBandwidthQPs(t *testing.T) {
	b := spec.Bundle{}
	if err := applyCheckOverrides(&b, checkOverrideOptions{bandwidthQPs: -1}); err == nil {
		t.Fatal("expected invalid bandwidth QP count")
	}
}

func TestApplyCheckOverridesRejectsInvalidMTU(t *testing.T) {
	b := spec.Bundle{}
	b.ApplyDefaults()
	if err := applyCheckOverrides(&b, checkOverrideOptions{rdmaPingMTU: 28}); err == nil {
		t.Fatal("expected invalid mtu error")
	}
}

func TestApplyCheckOverridesEnablesEmulatedKVTransferAndXDRMmap(t *testing.T) {
	b := spec.Bundle{}
	b.ApplyDefaults()
	err := applyCheckOverrides(&b, checkOverrideOptions{
		emuKVTransfer: true,
		bandwidthMmap: "xdr",
	})
	if err != nil {
		t.Fatalf("apply check overrides: %v", err)
	}
	if b.Check.MessageSize != 8388608 {
		t.Fatalf("unexpected message size: %d", b.Check.MessageSize)
	}
	if b.Check.MmapDevice != "/dev/xdrdrv" {
		t.Fatalf("unexpected mmap device: %q", b.Check.MmapDevice)
	}
}

func TestApplyCheckOverridesDisablesLegacyBandwidthDefaults(t *testing.T) {
	b := spec.Bundle{
		Check: spec.CheckConfig{
			MessageSize: 8388608,
			MmapDevice:  "/dev/xdrdrv",
		},
	}
	b.ApplyDefaults()
	if err := applyCheckOverrides(&b, checkOverrideOptions{}); err != nil {
		t.Fatalf("apply check overrides: %v", err)
	}
	if b.Check.MessageSize != 0 || b.Check.MmapDevice != "" {
		t.Fatalf("expected bandwidth defaults to be disabled, got size=%d mmap=%q", b.Check.MessageSize, b.Check.MmapDevice)
	}
}

func TestApplyCheckOverridesRejectsUnknownBandwidthMmap(t *testing.T) {
	b := spec.Bundle{}
	b.ApplyDefaults()
	if err := applyCheckOverrides(&b, checkOverrideOptions{bandwidthMmap: "foo"}); err == nil {
		t.Fatal("expected unknown bandwidth mmap error")
	}
}

func TestParseCheckStagesAcceptsBandwidthAndRDMAPing(t *testing.T) {
	runBandwidth, runRDMAPing, err := parseCheckStages("bandwidth,rdma-ping")
	if err != nil {
		t.Fatalf("parse check stages: %v", err)
	}
	if !runBandwidth || !runRDMAPing {
		t.Fatalf("unexpected stages: bandwidth=%v rdma-ping=%v", runBandwidth, runRDMAPing)
	}
}

func TestParseCheckStagesRejectsUnknownStage(t *testing.T) {
	if _, _, err := parseCheckStages("latency"); err == nil {
		t.Fatal("expected unknown check-stage error")
	}
}
