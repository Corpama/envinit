package main

import (
	"reflect"
	"testing"

	"envinit/internal/spec"
)

func TestParseStagesAcceptsKnownStages(t *testing.T) {
	got, err := parseStages("network,udev,sysctl,iommu")
	if err != nil {
		t.Fatalf("parse stages: %v", err)
	}
	want := map[string]bool{
		"network": true,
		"udev":    true,
		"sysctl":  true,
		"iommu":   true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected stages: got=%v want=%v", got, want)
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
