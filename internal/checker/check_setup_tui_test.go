package checker

import (
	"strings"
	"testing"

	"envinit/internal/spec"
)

func checkWizardTestRecords() []spec.MachineRecord {
	return []spec.MachineRecord{
		{HostID: "node-a", MgmtIP: "192.168.32.11", RDMA: []spec.RDMARecord{{Name: "eth1"}, {Name: "eth2"}}},
		{HostID: "node-b", MgmtIP: "192.168.32.18", RDMA: []spec.RDMARecord{{Name: "eth1"}, {Name: "eth2"}}},
	}
}

func TestCheckWizardExposesOperationalBandwidthAndXCCLParameters(t *testing.T) {
	bundle := spec.Bundle{}
	bundle.ApplyDefaults()
	model := newCheckWizardModel(checkWizardTestRecords(), bundle, true, true, true)
	rows := model.parameterRows()
	keys := map[string]bool{}
	for _, row := range rows {
		keys[row.key] = true
	}
	for _, key := range []string{
		"bw_run_mode", "bw_duration", "bw_size", "bw_qps", "bw_gid", "bw_parallel", "bw_port", "bw_memory", "bw_threshold",
		"xccl_test", "xccl_min", "xccl_max", "xccl_step", "xccl_warmup", "xccl_iters", "xccl_dtype", "xccl_timeout", "xccl_xdr", "xccl_supernode", "xccl_socket", "xccl_min_bus",
	} {
		if !keys[key] {
			t.Fatalf("parameter page is missing %q", key)
		}
	}
}

func TestCheckWizardParameterPageScrollsToSelectedRow(t *testing.T) {
	bundle := spec.Bundle{}
	bundle.ApplyDefaults()
	model := newCheckWizardModel(checkWizardTestRecords(), bundle, true, true, true)
	model.page = 2
	model.height = 18
	model.cursor = len(model.parameterRows()) - 1
	view := model.View()
	if !strings.Contains(view, "XCCL / minimum bus BW GB/s") || strings.Contains(view, "Ping / packet count") {
		t.Fatalf("parameter viewport did not follow the selected row:\n%s", view)
	}
}

func TestCheckWizardCanToggleParallelAndBandwidthModes(t *testing.T) {
	bundle := spec.Bundle{}
	bundle.ApplyDefaults()
	model := newCheckWizardModel(checkWizardTestRecords(), bundle, false, true, false)
	model.page = 2
	for index, row := range model.parameterRows() {
		if row.key == "bw_parallel" {
			model.cursor = index
			model.toggleCurrent()
			break
		}
	}
	if !model.bundle.Check.Bandwidth.Parallel {
		t.Fatal("parallel toggle did not update the temporary bandwidth config")
	}
	for index, row := range model.parameterRows() {
		if row.key == "bw_threshold" {
			model.cursor = index
			model.adjustCurrent(1)
			break
		}
	}
	if model.bundle.Check.Bandwidth.MinGBitsMode() != "disabled" {
		t.Fatalf("threshold cycle did not move auto -> disabled: %s", model.bundle.Check.Bandwidth.MinGBitsMode())
	}
}

func TestCheckWizardRequiresExplicitHostSelection(t *testing.T) {
	bundle := spec.Bundle{}
	bundle.ApplyDefaults()
	model := newCheckWizardModel(checkWizardTestRecords(), bundle, true, true, false)
	if err := model.validatePage(); err == nil {
		t.Fatal("expected the hosts page to require a selection")
	}
	model.selectedHosts[0] = true
	model.selectedHosts[1] = true
	if err := model.validatePage(); err != nil {
		t.Fatalf("unexpected host validation error: %v", err)
	}
}

func TestCheckWizardUsesSaturationDefaultsWithoutChangingSourceBundle(t *testing.T) {
	bundle := spec.Bundle{}
	bundle.ApplyDefaults()
	model := newCheckWizardModel(checkWizardTestRecords(), bundle, true, true, false)
	if !model.bundle.Check.Bandwidth.RunByDuration || model.bundle.Check.Bandwidth.Duration != 10 {
		t.Fatalf("expected duration-mode 10 second bandwidth run, got enabled=%v duration=%d", model.bundle.Check.Bandwidth.RunByDuration, model.bundle.Check.Bandwidth.Duration)
	}
	if model.bundle.Check.Bandwidth.MessageSize != 1048576 {
		t.Fatalf("expected 1 MiB bandwidth messages, got %d", model.bundle.Check.Bandwidth.MessageSize)
	}
	if bundle.Check.Bandwidth.RunByDuration || bundle.Check.Bandwidth.MessageSize != 0 {
		t.Fatal("wizard defaults must not mutate the loaded source bundle")
	}
	if !model.verbs || !model.rdmaCM {
		t.Fatal("interactive bandwidth should select both Verbs and RDMA-CM by default")
	}
}

func TestCheckWizardReviewCountsBidirectionalCrossMatrix(t *testing.T) {
	if got := wizardPathCount(checkWizardTestRecords()); got != 8 {
		t.Fatalf("expected two directions times 2x2 NICs = 8 paths, got %d", got)
	}
}

func TestCheckWizardRejectsSingleHostForPingOrBandwidth(t *testing.T) {
	bundle := spec.Bundle{}
	bundle.ApplyDefaults()
	model := newCheckWizardModel(checkWizardTestRecords(), bundle, true, false, false)
	model.selectedHosts[0] = true
	model.page = 3
	if err := model.validate(); err == nil {
		t.Fatal("expected single-host Ping to be rejected")
	}
	model.runPing = false
	model.runXCCL = true
	if err := model.validate(); err != nil {
		t.Fatalf("single-host XCCL should remain supported: %v", err)
	}
}
