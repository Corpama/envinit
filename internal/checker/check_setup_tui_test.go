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
	model.selectedHosts = []bool{true, true}
	rows := model.parameterRows()
	keys := map[string]bool{}
	for _, row := range rows {
		keys[row.key] = true
	}
	for _, key := range []string{
		"bw_run_mode", "bw_duration", "bw_size", "bw_qps", "bw_gid", "bw_parallel", "bw_port", "bw_memory", "bw_threshold",
		"xccl_layout", "xccl_xpu_ordering", "xccl_ranks", "xccl_test", "xccl_min", "xccl_max", "xccl_step", "xccl_warmup", "xccl_iters", "xccl_dtype", "xccl_timeout", "xccl_xdr", "xccl_validate_topology", "xccl_supernode", "xccl_socket", "xccl_evaluation", "xccl_machine",
	} {
		if !keys[key] {
			t.Fatalf("parameter page is missing %q", key)
		}
	}
}

func TestCheckWizardCanDisableXCCLTopologyValidation(t *testing.T) {
	bundle := spec.Bundle{}
	bundle.ApplyDefaults()
	model := newCheckWizardModel(checkWizardTestRecords(), bundle, false, false, true)
	model.selectedHosts = []bool{true, true}
	model.page = 2
	for index, row := range model.parameterRows() {
		if row.key == "xccl_validate_topology" {
			model.cursor = index
			model.toggleCurrent()
			break
		}
	}
	if model.bundle.Check.XCCL.TopologyValidationEnabled() {
		t.Fatal("XCCL topology validation toggle did not disable the temporary config")
	}
}

func TestCheckWizardExposesSameIndexAndManualRanks(t *testing.T) {
	bundle := spec.Bundle{}
	bundle.ApplyDefaults()
	model := newCheckWizardModel(checkWizardTestRecords(), bundle, false, false, true)
	model.selectedHosts = []bool{true, true}
	model.page = 2
	for index, row := range model.parameterRows() {
		if row.key == "xccl_layout" {
			model.cursor = index
			model.adjustCurrent(1)
			break
		}
	}
	if model.bundle.Check.XCCL.Layout != "same_index" {
		t.Fatalf("layout did not cycle to same_index: %s", model.bundle.Check.XCCL.Layout)
	}
	keys := map[string]bool{}
	for _, row := range model.parameterRows() {
		keys[row.key] = true
	}
	if !keys["xccl_split_step"] || !keys["xccl_split_op"] || keys["xccl_machine"] {
		t.Fatalf("unexpected same-index parameter rows: %#v", keys)
	}
	model.editKey, model.editBuffer = "xccl_ranks", "24"
	if err := model.applyEditedValue(); err != nil || model.bundle.Check.XCCL.Ranks != 24 {
		t.Fatalf("manual ranks edit failed: ranks=%d err=%v", model.bundle.Check.XCCL.Ranks, err)
	}
}

func TestCheckWizardCanSelectPhysicalXPUOrdering(t *testing.T) {
	bundle := spec.Bundle{}
	bundle.ApplyDefaults()
	bundle.Check.XCCL.XPUOrdering = "rail_aligned"
	model := newCheckWizardModel(checkWizardTestRecords(), bundle, false, false, true)
	model.selectedHosts = []bool{true, true}
	model.page = 2
	for index, row := range model.parameterRows() {
		if row.key == "xccl_xpu_ordering" {
			model.cursor = index
			model.adjustCurrent(1)
			break
		}
	}
	if model.bundle.Check.XCCL.XPUOrdering != "physical" {
		t.Fatalf("XPU ordering did not cycle to physical: %q", model.bundle.Check.XCCL.XPUOrdering)
	}
}

func TestCheckWizardDescribesAutomaticXPUOrderingWithoutClaimingAResolvedMode(t *testing.T) {
	cfg := spec.CheckXCCLConfig{Layout: "full_ring", XPUOrdering: "auto"}
	if got, want := xcclXPUOrderingLabel(cfg), "auto (physical first; rail fallback)"; got != want {
		t.Fatalf("full-ring automatic ordering label = %q, want %q", got, want)
	}
	cfg.Layout = "same_index"
	if got, want := xcclXPUOrderingLabel(cfg), "auto (physical for same_index)"; got != want {
		t.Fatalf("same-index automatic ordering label = %q, want %q", got, want)
	}
}

func TestCheckWizardSupportsAutomaticFullRingMachineClassDetection(t *testing.T) {
	bundle := spec.Bundle{}
	bundle.ApplyDefaults()
	model := newCheckWizardModel(checkWizardTestRecords(), bundle, false, false, true)
	model.selectedHosts = []bool{true, true}
	model.page = 2
	rows := model.parameterRows()
	for _, row := range rows {
		if row.key == "xccl_machine" && row.value != "AUTO (detect per host)" {
			t.Fatalf("automatic machine class label = %q", row.value)
		}
	}
	if err := model.validatePage(); err != nil {
		t.Fatalf("automatic machine-class detection should validate in setup: %v", err)
	}
	for index, row := range model.parameterRows() {
		if row.key == "xccl_machine" {
			model.cursor = index
			model.adjustCurrent(1)
			break
		}
	}
	if model.bundle.Check.XCCL.MachineClass != "vc" {
		t.Fatalf("machine class did not cycle to VC: %q", model.bundle.Check.XCCL.MachineClass)
	}
	if err := model.validatePage(); err != nil {
		t.Fatalf("selected machine class should validate: %v", err)
	}
}

func TestCheckWizardUsesSingleHostScopeWithoutMultiHostControls(t *testing.T) {
	bundle := spec.Bundle{}
	bundle.ApplyDefaults()
	model := newCheckWizardModel(checkWizardTestRecords(), bundle, false, false, true)
	model.selectedHosts[0] = true
	model.page = 2
	rows := model.parameterRows()
	keys := map[string]bool{}
	values := map[string]string{}
	for _, row := range rows {
		keys[row.key] = true
		values[row.key] = row.value
	}
	if !keys["xccl_scope"] || values["xccl_scope"] != "single_host" {
		t.Fatalf("single-host scope is not explicit: %#v", values)
	}
	for _, key := range []string{"xccl_layout", "xccl_xpu_ordering", "xccl_machine", "xccl_split_step", "xccl_split_op", "xccl_validate_topology"} {
		if keys[key] {
			t.Fatalf("single-host parameter page unexpectedly exposes %q", key)
		}
	}
	if values["xccl_evaluation"] != "disabled" {
		t.Fatalf("unexpected single-host evaluation label: %q", values["xccl_evaluation"])
	}
	if err := model.validatePage(); err != nil {
		t.Fatalf("single-host XCCL should not require a multi-host machine class: %v", err)
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
	if !strings.Contains(view, "XCCL / socket interface") || strings.Contains(view, "Ping / packet count") {
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

func TestCheckWizardSingleHostChecksPageOnlyOffersXCCL(t *testing.T) {
	bundle := spec.Bundle{}
	bundle.ApplyDefaults()
	model := newCheckWizardModel(checkWizardTestRecords(), bundle, true, true, false)
	model.selectedHosts[0] = true
	model.applySelectedHostScope()
	rows := model.checkRows()
	if len(rows) != 1 || rows[0].key != "xccl" || !rows[0].enabled {
		t.Fatalf("single-host check rows = %#v, want only selected XCCL", rows)
	}
	if model.runPing || model.runBandwidth || model.bundle.Check.XCCL.EvaluationMode != "disabled" {
		t.Fatalf("single-host scope was not applied: ping=%v bandwidth=%v evaluation=%q", model.runPing, model.runBandwidth, model.bundle.Check.XCCL.EvaluationMode)
	}
}

func TestCheckWizardShowsXCCLEnvironmentAndReviewSummary(t *testing.T) {
	bundle := spec.Bundle{}
	bundle.ApplyDefaults()
	bundle.Check.XCCL.Environment = map[string]string{"CUSTOM_FLAG": "custom-value"}
	model := newCheckWizardModel(checkWizardTestRecords(), bundle, false, false, true)
	model.selectedHosts = []bool{true, true}
	envValues := map[string]string{}
	for _, row := range model.environmentRows() {
		envValues[row.label] = row.value
	}
	for _, key := range []string{"BKCL_RDMA_NICS", "BKCL_FORCE_RDMA_NICS_ORDER", "CUDA_VISIBLE_DEVICES", "XPU_VISIBLE_DEVICES", "CUSTOM_FLAG", "LD_LIBRARY_PATH", "PATH", "XPU_HOME"} {
		if _, ok := envValues[key]; !ok {
			t.Fatalf("environment page is missing %s: %#v", key, envValues)
		}
	}
	if !strings.Contains(envValues["BKCL_RDMA_NICS"], "resolved per host") || envValues["CUSTOM_FLAG"] != "custom-value" {
		t.Fatalf("unexpected environment preview: %#v", envValues)
	}
	if review := model.renderReview(160); !strings.Contains(review, "scope=multi_host") || !strings.Contains(review, "Environment:") {
		t.Fatalf("review does not expose multi-host scope and environment summary:\n%s", review)
	}
}

func TestCheckWizardRejectsAutomaticEvaluationForOtherCollectives(t *testing.T) {
	bundle := spec.Bundle{}
	bundle.ApplyDefaults()
	bundle.Check.XCCL.Test = "all_to_all"
	model := newCheckWizardModel(checkWizardTestRecords(), bundle, false, false, true)
	model.selectedHosts = []bool{true, true}
	model.page = checkWizardPageParameters
	if err := model.validatePage(); err == nil || !strings.Contains(err.Error(), "only for all_reduce") {
		t.Fatalf("expected automatic evaluation guard, got %v", err)
	}
	model.bundle.Check.XCCL.EvaluationMode = "disabled"
	if err := model.validatePage(); err != nil {
		t.Fatalf("disabled evaluation should allow all_to_all: %v", err)
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
	model.page = checkWizardPageReview
	if err := model.validate(); err == nil {
		t.Fatal("expected single-host Ping to be rejected")
	}
	model.runPing = false
	model.runXCCL = true
	if err := model.validate(); err != nil {
		t.Fatalf("single-host XCCL should remain supported: %v", err)
	}
}
