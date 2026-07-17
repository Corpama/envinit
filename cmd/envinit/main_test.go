package main

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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

func TestPlanPreviewModelNavigatesStagesAndScrollsActions(t *testing.T) {
	app := fakePlanApp()
	preview := buildPlanPreview(app, strings.Join([]string{
		"hostname already xpu11",
		"==> stage: network",
		"write /etc/sysconfig/network-scripts/ifcfg-bond0",
		"run: nmcli connection reload",
		"run: nmcli connection up bond0",
		"run: nmcli connection up ens11np0",
		"run: bash /usr/local/sbin/kunlun-config_rt_ens11np0.sh",
		"run: bash /usr/local/sbin/kunlun-config_rt_ens13np0.sh",
		"best-effort enable RoCE adaptive routing on ens11np0",
		"best-effort enable RoCE adaptive routing on ens13np0",
		"==> stage: post",
		"write /etc/systemd/system/kunlun-post-boot.service",
	}, "\n"))
	model := newPlanPreviewModel(preview)
	model.height = 14

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(planPreviewModel)
	if got := model.stages[model.stage].Name; got != "network" {
		t.Fatalf("expected network stage after down, got %s", got)
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	model = updated.(planPreviewModel)
	if model.scroll == 0 {
		t.Fatal("expected PgDown to scroll stage actions")
	}
	view := model.View()
	for _, want := range []string{
		"Plan Preview",
		"Target: test1",
		"network",
		"Keys: Up/Down stage",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected %q in TUI view:\n%s", want, view)
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
	if b.Check.RDMAPing.Count != 10 {
		t.Fatalf("unexpected ping count: %d", b.Check.RDMAPing.Count)
	}
	if b.Check.RDMAPing.Timeout != 5 {
		t.Fatalf("unexpected ping timeout: %d", b.Check.RDMAPing.Timeout)
	}
	if b.Check.RDMAPing.PayloadSize != 8972 {
		t.Fatalf("unexpected ping payload size: %d", b.Check.RDMAPing.PayloadSize)
	}
}

func TestApplyCheckOverridesSetsBandwidthQPs(t *testing.T) {
	b := spec.Bundle{Check: spec.CheckConfig{Bandwidth: spec.CheckBandwidthConfig{BandwidthQPs: 2}}}
	if err := applyCheckOverrides(&b, checkOverrideOptions{bandwidthQPs: 8}); err != nil {
		t.Fatalf("apply check overrides: %v", err)
	}
	if b.Check.Bandwidth.BandwidthQPs != 8 {
		t.Fatalf("unexpected bandwidth QP count: %d", b.Check.Bandwidth.BandwidthQPs)
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
	if b.Check.Bandwidth.MessageSize != 8388608 {
		t.Fatalf("unexpected message size: %d", b.Check.Bandwidth.MessageSize)
	}
	if b.Check.Bandwidth.MmapDevice != "/dev/xdrdrv" {
		t.Fatalf("unexpected mmap device: %q", b.Check.Bandwidth.MmapDevice)
	}
}

func TestApplyCheckOverridesDisablesLegacyBandwidthDefaults(t *testing.T) {
	b := spec.Bundle{
		Check: spec.CheckConfig{
			Bandwidth: spec.CheckBandwidthConfig{
				MessageSize: 8388608,
				MmapDevice:  "/dev/xdrdrv",
			},
		},
	}
	b.ApplyDefaults()
	if err := applyCheckOverrides(&b, checkOverrideOptions{}); err != nil {
		t.Fatalf("apply check overrides: %v", err)
	}
	if b.Check.Bandwidth.MessageSize != 0 || b.Check.Bandwidth.MmapDevice != "" {
		t.Fatalf("expected bandwidth defaults to be disabled, got size=%d mmap=%q", b.Check.Bandwidth.MessageSize, b.Check.Bandwidth.MmapDevice)
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
	runBandwidth, runRDMAPing, runXCCL, err := parseCheckStages("bandwidth,rdma-ping")
	if err != nil {
		t.Fatalf("parse check stages: %v", err)
	}
	if !runBandwidth || !runRDMAPing || runXCCL {
		t.Fatalf("unexpected stages: bandwidth=%v rdma-ping=%v xccl=%v", runBandwidth, runRDMAPing, runXCCL)
	}
}

func TestParseCheckStagesAcceptsXCCL(t *testing.T) {
	runBandwidth, runRDMAPing, runXCCL, err := parseCheckStages("xccl")
	if err != nil {
		t.Fatalf("parse check stages: %v", err)
	}
	if runBandwidth || runRDMAPing || !runXCCL {
		t.Fatalf("unexpected stages: bandwidth=%v rdma-ping=%v xccl=%v", runBandwidth, runRDMAPing, runXCCL)
	}
}

func TestParseCheckStagesRejectsUnknownStage(t *testing.T) {
	if _, _, _, err := parseCheckStages("latency"); err == nil {
		t.Fatal("expected unknown check-stage error")
	}
}
