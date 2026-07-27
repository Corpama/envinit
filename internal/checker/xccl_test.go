package checker

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"envinit/internal/spec"
)

func TestXCCLPlanUsesPerXPUTopologyOrder(t *testing.T) {
	topology, err := parseXPUTopology(sampleXPUTopology)
	if err != nil {
		t.Fatalf("parse topology: %v", err)
	}
	target := Target{
		Name: "node-a",
		RDMA: []spec.RDMARecord{
			{Name: "ens11np0", IP: "10.61.11.27", Prefix: "24"},
			{Name: "ens13np0", IP: "10.61.13.27", Prefix: "24"},
			{Name: "ens15np0", IP: "10.61.15.27", Prefix: "24"},
			{Name: "ens17np0", IP: "10.61.17.27", Prefix: "24"},
		},
	}
	groups := []spec.CheckRDMAGroup{
		{IBDevice: "mlx5_1"},
		{IBDevice: "mlx5_2"},
		{IBDevice: "mlx5_3"},
		{IBDevice: "mlx5_4"},
	}
	plan, err := xcclPlanFromTopology(spec.Bundle{}, target, groups, topology)
	if err != nil {
		t.Fatalf("create XCCL plan: %v", err)
	}
	wantOrder := []string{"ens11np0", "ens11np0", "ens13np0", "ens13np0", "ens15np0", "ens15np0", "ens17np0", "ens17np0"}
	if !reflect.DeepEqual(plan.RDMANICOrder, wantOrder) {
		t.Fatalf("unexpected XPU/NIC order: got %#v want %#v", plan.RDMANICOrder, wantOrder)
	}
	if plan.XPUCount != 8 {
		t.Fatalf("unexpected XPU count: %d", plan.XPUCount)
	}
	wantRails := []string{"10.61.11.0/24", "10.61.11.0/24", "10.61.13.0/24", "10.61.13.0/24", "10.61.15.0/24", "10.61.15.0/24", "10.61.17.0/24", "10.61.17.0/24"}
	if !reflect.DeepEqual(plan.RDMARailOrder, wantRails) {
		t.Fatalf("unexpected XPU/RDMA rail order: got %#v want %#v", plan.RDMARailOrder, wantRails)
	}
	if !strings.Contains(strings.Join(plan.Mapping, ","), "XPU7=ens17np0(mlx5_4,PIX)") {
		t.Fatalf("unexpected mapping: %#v", plan.Mapping)
	}
}

func TestXCCLPlanUsesAllEightEqualPIXNICsAndAlignsRails(t *testing.T) {
	topology := pairedEightNICXPUTopology()
	groups := make([]spec.CheckRDMAGroup, 8)
	for index := range groups {
		groups[index].IBDevice = fmt.Sprintf("mlx5_%d", index)
	}
	targetA := Target{Name: "xpu-07"}
	for rail := 11; rail <= 18; rail++ {
		targetA.RDMA = append(targetA.RDMA, spec.RDMARecord{Name: fmt.Sprintf("ens%d", rail), IP: fmt.Sprintf("10.61.%d.27", rail), Prefix: "24"})
	}
	targetB := Target{Name: "xpu-23"}
	for _, rail := range []int{13, 14, 11, 12, 17, 18, 15, 16} {
		targetB.RDMA = append(targetB.RDMA, spec.RDMARecord{Name: fmt.Sprintf("ens%d", rail), IP: fmt.Sprintf("10.61.%d.43", rail), Prefix: "24"})
	}
	planA, err := xcclPlanFromTopology(spec.Bundle{}, targetA, groups, topology)
	if err != nil {
		t.Fatalf("plan xpu-07: %v", err)
	}
	planB, err := xcclPlanFromTopology(spec.Bundle{}, targetB, groups, topology)
	if err != nil {
		t.Fatalf("plan xpu-23: %v", err)
	}
	if len(planA.RDMANICs) != 8 || len(planB.RDMANICs) != 8 {
		t.Fatalf("equal PIX candidates must be balanced across all eight NICs: A=%#v B=%#v", planA.RDMANICs, planB.RDMANICs)
	}
	plans := []xcclTargetPlan{planA, planB}
	alignXCCLPlansByRail(plans)
	wantXPUOrder := []int{2, 3, 0, 1, 6, 7, 4, 5}
	if !reflect.DeepEqual(plans[1].XPUOrder, wantXPUOrder) {
		t.Fatalf("xpu-23 aligned physical XPU order: got %#v want %#v", plans[1].XPUOrder, wantXPUOrder)
	}
	if !reflect.DeepEqual(plans[1].RDMARailOrder, plans[0].RDMARailOrder) {
		t.Fatalf("aligned rail orders differ: A=%#v B=%#v", plans[0].RDMARailOrder, plans[1].RDMARailOrder)
	}
	wantNICOrder := []string{"ens11", "ens12", "ens13", "ens14", "ens15", "ens16", "ens17", "ens18"}
	if !reflect.DeepEqual(plans[1].RDMANICOrder, wantNICOrder) {
		t.Fatalf("xpu-23 aligned NIC order: got %#v want %#v", plans[1].RDMANICOrder, wantNICOrder)
	}
	if got := xcclPlanVisibleDevices(plans[1]); got != "2,3,0,1,6,7,4,5" {
		t.Fatalf("xpu-23 CUDA_VISIBLE_DEVICES order = %q", got)
	}
	if err := validateXCCLPlanConsistency(plans); err != nil {
		t.Fatalf("rail-aligned isomorphic plans should validate: %v", err)
	}
}

func TestXCCLResolvedOrderingDisplayReportsActualPermutation(t *testing.T) {
	cfg := spec.CheckXCCLConfig{Layout: "full_ring", XPUOrdering: "auto"}
	physicalPlans := []xcclTargetPlan{
		{Target: Target{Name: "node-a"}, XPUCount: 4, XPUOrder: []int{0, 1, 2, 3}},
		{Target: Target{Name: "node-b"}, XPUCount: 4, XPUOrder: []int{0, 1, 2, 3}},
	}
	mode, reason := xcclResolvedOrderingDisplay(cfg, physicalPlans)
	if mode != "physical" || !strings.Contains(reason, "already matches") {
		t.Fatalf("identity alignment display = %q, %q", mode, reason)
	}

	reorderedPlans := append([]xcclTargetPlan(nil), physicalPlans...)
	reorderedPlans[1].XPUOrder = []int{2, 3, 0, 1}
	mode, reason = xcclResolvedOrderingDisplay(cfg, reorderedPlans)
	if mode != "rail_aligned" || !strings.Contains(reason, "differs") {
		t.Fatalf("reordered alignment display = %q, %q", mode, reason)
	}
}

func TestXCCLGlobalAssignmentPreservesPIXForConstrainedXPU(t *testing.T) {
	topology := xpuTopology{
		NICDevices: map[string]string{"NIC0": "dev0", "NIC1": "dev1"},
		Links: map[int]map[string]string{
			0: {"NIC0": "PIX", "NIC1": "PIX"},
			1: {"NIC0": "PIX", "NIC1": "SYS"},
		},
	}
	candidates := []xcclNICCandidate{
		{iface: "eth0", device: "dev0", nic: "NIC0"},
		{iface: "eth1", device: "dev1", nic: "NIC1"},
	}
	assignments, err := assignXCCLCandidates(topology, []int{0, 1}, candidates)
	if err != nil {
		t.Fatalf("assign candidates: %v", err)
	}
	if want := []int{1, 0}; !reflect.DeepEqual(assignments, want) {
		t.Fatalf("global assignment = %#v, want %#v", assignments, want)
	}
}

func pairedEightNICXPUTopology() xpuTopology {
	topology := xpuTopology{NICDevices: map[string]string{}, Links: map[int]map[string]string{}}
	for nic := 0; nic < 8; nic++ {
		topology.NICDevices[fmt.Sprintf("NIC%d", nic)] = fmt.Sprintf("mlx5_%d", nic)
	}
	for xpu := 0; xpu < 8; xpu++ {
		topology.Links[xpu] = map[string]string{}
		pairStart := xpu / 2 * 2
		for nic := 0; nic < 8; nic++ {
			link := "SYS"
			if nic == pairStart || nic == pairStart+1 {
				link = "PIX"
			}
			topology.Links[xpu][fmt.Sprintf("NIC%d", nic)] = link
		}
	}
	return topology
}

func TestXCCLRankScriptExportsRuntimeAndTopologyEnvironment(t *testing.T) {
	enableXDR := true
	cfg := spec.CheckXCCLConfig{
		XPUHome:   "/usr/local/xpu",
		Test:      "all_reduce",
		Timeout:   120,
		EnableXDR: &enableXDR,
		Supernode: true,
		Environment: map[string]string{
			"BKCL_DEBUG": "1",
		},
	}
	plan := xcclTargetPlan{
		XPUCount:        4,
		RDMANICs:        []string{"ens11np0", "ens13np0"},
		RDMANICOrder:    []string{"ens11np0", "ens11np0", "ens13np0", "ens13np0"},
		SocketInterface: "bond0",
	}
	script := xcclRankScript(cfg, plan, "/tmp/envinit-xccl-check/run", true)
	for _, want := range []string{
		"unset XPU_VISIBLE_DEVICES CUDA_VISIBLE_DEVICES",
		"export XPU_HOME='/usr/local/xpu'",
		"/var/lib/envinit/check-runtime/mpich-5.0.1/lib",
		"/usr/local/xpu/so",
		"BKCL_ENABLE_XDR='1'",
		"BKCL_FORCE_RDMA_NICS_ORDER='ens11np0,ens11np0,ens13np0,ens13np0'",
		"BKCL_RDMA_NICS='ens11np0,ens11np0,ens13np0,ens13np0'",
		"BKCL_SOCKET_IFNAME='bond0'",
		"BKCL_SWITCH_TOPO='1'",
		"BKCL_RDMA_VERBS='1'",
		"BKCL_TREE_THRESHOLD='1'",
		"BKCL_USE_AR='1'",
		"BKCL_FLAT_RING='1'",
		"BKCL_USE_RDMA='1'",
		"BKCL_USE_XDR_COPY='1'",
		"CUDA_VISIBLE_DEVICES='0,1,2,3'",
		"XPU_VISIBLE_DEVICES='0,1,2,3'",
		"BKCL_DEBUG='1'",
		"exec '/tmp/envinit-xccl-check/run/runtime/xccl_Linux_x86_64/systest/xccl_perf' \"$@\"",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("expected %q in rank script:\n%s", want, script)
		}
	}
}

func TestParseXCCLPerformanceRows(t *testing.T) {
	rows := parseXCCLPerformanceRows(`
XCCL running perf test all_reduce on 16 ranks
   size(B)      count   type     op   time(us) algbw(GB/s) busbw(GB/s)
  67108864   16777216  float    sum    1200.50       55.90       52.40
 134217728   33554432  float    sum    2100.25       63.91       59.92
`)
	if len(rows) != 2 {
		t.Fatalf("unexpected rows: %#v", rows)
	}
	if rows[1].SizeBytes != 134217728 || rows[1].BusGBs != 59.92 || rows[1].Operation != "sum" {
		t.Fatalf("unexpected parsed row: %#v", rows[1])
	}
	if rows[1].Mode != "out-of-place" {
		t.Fatalf("legacy perf row mode = %q, want out-of-place", rows[1].Mode)
	}
}

func TestParseXCCLSysTestPerformanceRowsUsesInPlaceColumns(t *testing.T) {
	output := `
#   size      count    type  redop root           out-of-place                       in-place
#    (B)   (elements)                         time  algbw  busbw #wrong      time  algbw  busbw #wrong
     1024          256 float sum -1             47   0.02   0.04 N/A          46   0.02   0.04 N/A
134217728     33554432 float sum -1           2073  64.73 113.29 N/A        2081  64.47 112.82 N/A
268435456     67108864 float sum -1           4186  64.11 112.20 N/A        4205  63.83 111.70 N/A
# Out of bounds values : 0 OK
# Avg bus bandwidth    : 41.9883
`
	rows := parseXCCLPerformanceRows(output)
	if len(rows) != 6 {
		t.Fatalf("unexpected systest rows: %#v", rows)
	}
	last := rows[5]
	if last.SizeBytes != 268435456 || last.Mode != "in-place" || last.TimeUS != 4205 || last.AlgGBs != 63.83 || last.BusGBs != 111.70 {
		t.Fatalf("unexpected in-place systest row: %#v", last)
	}
	if rows[4].Mode != "out-of-place" || rows[4].BusGBs != 112.20 {
		t.Fatalf("unexpected out-of-place systest row: %#v", rows[4])
	}
	selected := selectXCCLPerformanceRow(rows)
	if selected.SizeBytes != 134217728 || selected.BusGBs != 112.82 {
		t.Fatalf("SOP evaluation row = %#v, want second-largest size with in-place busbw", selected)
	}
	var details bytes.Buffer
	printXCCLSizeResults(&details, rows, selected)
	for _, want := range []string{
		"XCCL size result details",
		"out-of-place",
		"in-place",
		"134217728",
		"112.82",
		"268435456",
		"111.70",
	} {
		if !strings.Contains(details.String(), want) {
			t.Fatalf("expected %q in size details:\n%s", want, details.String())
		}
	}
}

func TestSelectXCCLPerformanceRowFallsBackToOnlySize(t *testing.T) {
	row := xcclPerformanceRow{SizeBytes: 134217728, BusGBs: 59.92}
	if got := selectXCCLPerformanceRow([]xcclPerformanceRow{row}); got != row {
		t.Fatalf("single-size selection = %#v, want %#v", got, row)
	}
}

func TestXCCLMPIRunArgsUseSysTestInterface(t *testing.T) {
	cfg := spec.CheckXCCLConfig{
		Test:             "all_reduce",
		MinBytes:         "1024",
		MaxBytes:         "256m",
		StepFactor:       2,
		WarmupIterations: 5,
		Iterations:       20,
		DataType:         "float",
	}
	got := strings.Join(xcclMPIRunArgs(cfg, "/tmp/envinit-xccl-check/run", 8, false), " ")
	for _, want := range []string{
		"mpiexec.hydra -launcher fork -np 8",
		"-wdir /tmp/envinit-xccl-check/run",
		"run-rank.sh -O allReduce -x 1 -b 1024 -e 256m -f 2 -w 5 -n 20 -c 0 -d float",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in systest command:\n%s", want, got)
		}
	}
}

func TestXCCLMPIRunArgsAddSameIndexSplitMode(t *testing.T) {
	cfg := spec.CheckXCCLConfig{
		Test: "all_reduce", MinBytes: "1m", MaxBytes: "2g", StepFactor: 2,
		Iterations: 20, DataType: "fp16", Layout: "same_index", SplitStep: 8, SplitOperation: 0,
	}
	got := strings.Join(xcclMPIRunArgs(cfg, "/tmp/envinit-xccl-check/run", 24, true), " ")
	for _, want := range []string{
		"-f /tmp/envinit-xccl-check/run/hosts -np 24",
		"-d fp16 --split_mode --split_step 8 --split_op 0",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in same-index command:\n%s", want, got)
		}
	}
}

func TestResolveXCCLRanksAutoAndManual(t *testing.T) {
	if got, source, err := resolveXCCLRanks(spec.CheckXCCLConfig{}, 24); err != nil || got != 24 || source != "auto" {
		t.Fatalf("auto ranks = %d,%s,%v; want 24,auto,nil", got, source, err)
	}
	if got, source, err := resolveXCCLRanks(spec.CheckXCCLConfig{Ranks: 16}, 24); err != nil || got != 16 || source != "manual" {
		t.Fatalf("manual ranks = %d,%s,%v; want 16,manual,nil", got, source, err)
	}
	if _, _, err := resolveXCCLRanks(spec.CheckXCCLConfig{Ranks: 25}, 24); err == nil || !strings.Contains(err.Error(), "exceeds 24 discovered XPUs") {
		t.Fatalf("expected manual oversubscription error, got %v", err)
	}
}

func TestXCCLAutomaticEvaluationRules(t *testing.T) {
	tests := []struct {
		name         string
		cfg          spec.CheckXCCLConfig
		bus          float64
		wantStatus   string
		wantBaseline float64
		wantRequired float64
	}{
		{name: "vc full ring pass", cfg: spec.CheckXCCLConfig{Layout: "full_ring", MachineClass: "vc", EvaluationMode: "auto"}, bus: 60.01, wantStatus: "PASS", wantBaseline: 100, wantRequired: .60},
		{name: "vc boundary fails", cfg: spec.CheckXCCLConfig{Layout: "full_ring", MachineClass: "vc", EvaluationMode: "auto"}, bus: 60, wantStatus: "FAIL", wantBaseline: 100, wantRequired: .60},
		{name: "vd full ring pass", cfg: spec.CheckXCCLConfig{Layout: "full_ring", MachineClass: "vd", EvaluationMode: "auto"}, bus: 93.63, wantStatus: "PASS", wantBaseline: 150, wantRequired: .60},
		{name: "same index pass", cfg: spec.CheckXCCLConfig{Layout: "same_index", EvaluationMode: "auto"}, bus: 182.39, wantStatus: "PASS", wantBaseline: 200, wantRequired: .90},
		{name: "same index boundary fails", cfg: spec.CheckXCCLConfig{Layout: "same_index", EvaluationMode: "auto"}, bus: 180, wantStatus: "FAIL", wantBaseline: 200, wantRequired: .90},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opts := Options{Bundle: spec.Bundle{Check: spec.CheckConfig{XCCL: test.cfg}}}
			evaluation := evaluateXCCLResult(opts, nil, []xcclPerformanceRow{{SizeBytes: 1 << 30, Mode: "in-place", BusGBs: test.bus}})
			if evaluation.Status != test.wantStatus || evaluation.BaselineGBs != test.wantBaseline || evaluation.RequiredUtilization != test.wantRequired {
				t.Fatalf("evaluation = %#v", evaluation)
			}
		})
	}
}

func TestEffectiveXCCLConfigSeparatesSingleAndMultiHostModes(t *testing.T) {
	cfg := spec.CheckXCCLConfig{Layout: "full_ring", EvaluationMode: "auto", Test: "all_reduce"}
	single := effectiveXCCLConfig(cfg, false)
	if single.Layout != "single_host" || single.EvaluationMode != "disabled" {
		t.Fatalf("single-host effective config = %#v", single)
	}
	multi := effectiveXCCLConfig(cfg, true)
	if multi.Layout != "full_ring" || multi.EvaluationMode != "auto" {
		t.Fatalf("multi-host effective config = %#v", multi)
	}
	if err := validateXCCLExecutionConfig(cfg, false); err != nil {
		t.Fatalf("single-host execution must not require a multi-host machine class: %v", err)
	}
	if err := validateXCCLExecutionConfig(cfg, true); err == nil || !strings.Contains(err.Error(), "VC or VD") {
		t.Fatalf("multi-host full-ring auto evaluation should require machine class, got %v", err)
	}
}

func TestValidateXCCLExecutionConfigRejectsUndefinedAutomaticCollectiveBaseline(t *testing.T) {
	cfg := spec.CheckXCCLConfig{Layout: "full_ring", EvaluationMode: "auto", MachineClass: "vc", Test: "all_to_all"}
	err := validateXCCLExecutionConfig(cfg, true)
	if err == nil || !strings.Contains(err.Error(), "only for all_reduce") {
		t.Fatalf("expected fail-closed automatic evaluation error, got %v", err)
	}
	for _, mode := range []string{"manual", "disabled"} {
		cfg.EvaluationMode = mode
		if err := validateXCCLExecutionConfig(cfg, true); err != nil {
			t.Fatalf("%s evaluation should permit all_to_all: %v", mode, err)
		}
	}
}

func TestResolveXCCLMachineClassUsesWeakestHost(t *testing.T) {
	partNumbers := map[string]string{
		"node-vd": "    XPU Part Number : B00100300110312\n",
		"node-vc": "    XPU Part Number : B00100300110112\n",
	}
	var output bytes.Buffer
	opts := Options{
		Output: &output,
		CommandRunner: func(_ spec.CheckConfig, target Target, command string) (string, error) {
			if command != "xpu-smi -q" {
				return "", fmt.Errorf("unexpected command: %s", command)
			}
			return partNumbers[target.Name], nil
		},
	}
	cfg, err := resolveXCCLMachineClass(opts, spec.CheckXCCLConfig{Layout: "full_ring", EvaluationMode: "auto"}, []Target{{Name: "node-vd"}, {Name: "node-vc"}}, true)
	if err != nil {
		t.Fatalf("resolve machine class: %v", err)
	}
	if cfg.MachineClass != "vc" {
		t.Fatalf("weakest-link machine class = %q, want vc", cfg.MachineClass)
	}
	for _, want := range []string{"node-vd detected=VD", "node-vc detected=VC", "machine class downgrade", "applying VC full-ring baseline"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q in machine classification log:\n%s", want, output.String())
		}
	}
}

func TestResolveXCCLMachineClassKeepsAllVD(t *testing.T) {
	var output bytes.Buffer
	opts := Options{
		Output: &output,
		CommandRunner: func(_ spec.CheckConfig, _ Target, _ string) (string, error) {
			return "XPU Part Number : B00100300110312\n", nil
		},
	}
	cfg, err := resolveXCCLMachineClass(opts, spec.CheckXCCLConfig{Layout: "full_ring", EvaluationMode: "auto"}, []Target{{Name: "node-a"}, {Name: "node-b"}}, true)
	if err != nil || cfg.MachineClass != "vd" {
		t.Fatalf("all-VD machine class = %#v, err=%v", cfg, err)
	}
	if !strings.Contains(output.String(), "selected=VD source=auto weakest-link") {
		t.Fatalf("missing automatic selection log:\n%s", output.String())
	}
}

func TestLimitXCCLPlansBalancesManualRanksAcrossHosts(t *testing.T) {
	makePlan := func(name string) xcclTargetPlan {
		plan := xcclTargetPlan{Target: Target{Name: name, Address: name}, XPUCount: 8}
		for index := 0; index < 8; index++ {
			plan.XPUOrder = append(plan.XPUOrder, index)
			plan.RDMANICOrder = append(plan.RDMANICOrder, fmt.Sprintf("eth%d", index))
			plan.RDMADeviceOrder = append(plan.RDMADeviceOrder, fmt.Sprintf("dev%d", index))
			plan.RDMALinkOrder = append(plan.RDMALinkOrder, "PIX")
			plan.RDMARailOrder = append(plan.RDMARailOrder, fmt.Sprintf("rail%d", index))
			plan.Mapping = append(plan.Mapping, fmt.Sprintf("XPU%d=eth%d", index, index))
		}
		return plan
	}
	plans, err := limitXCCLPlansForRanks(spec.CheckXCCLConfig{Layout: "full_ring"}, []xcclTargetPlan{makePlan("node-a"), makePlan("node-b")}, 8)
	if err != nil {
		t.Fatalf("limit ranks: %v", err)
	}
	if plans[0].XPUCount != 4 || plans[1].XPUCount != 4 || xcclHostFile(plans) != "node-a:4\nnode-b:4\n" {
		t.Fatalf("manual ranks were not balanced: %#v hostfile=%q", plans, xcclHostFile(plans))
	}
	if got := xcclPlanVisibleDevices(plans[1]); got != "0,1,2,3" {
		t.Fatalf("limited visible devices = %q", got)
	}
	if _, err := limitXCCLPlansForRanks(spec.CheckXCCLConfig{Layout: "same_index"}, []xcclTargetPlan{makePlan("node-a"), makePlan("node-b")}, 7); err == nil || !strings.Contains(err.Error(), "divisible") {
		t.Fatalf("same-index uneven ranks should fail, got %v", err)
	}
	if _, err := limitXCCLPlansForRanks(spec.CheckXCCLConfig{Layout: "full_ring"}, []xcclTargetPlan{makePlan("node-a"), makePlan("node-b")}, 1); err == nil || !strings.Contains(err.Error(), "at least one rank per host") {
		t.Fatalf("rank count below host count should fail, got %v", err)
	}
}

func TestXCCLSharedSubnetUsesTopologyFallbackWithoutRequiringRailIDs(t *testing.T) {
	plan := xcclTargetPlan{
		Target:          Target{Name: "node-a"},
		RDMADeviceOrder: []string{"dev0", "dev1"},
		RDMARailOrder:   []string{"10.61.10.0/24", "10.61.10.0/24"},
	}
	warnings := xcclRailInferenceWarningsForPlan(plan)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "shared fabric") || !strings.Contains(warnings[0], "Leave rdmaN_rail_id empty") {
		t.Fatalf("shared subnet fallback warning = %#v", warnings)
	}
	if err := validateXCCLPlanConsistency([]xcclTargetPlan{plan}); err != nil {
		t.Fatalf("shared subnet must not require manual rail IDs: %v", err)
	}

	plan.RDMARailOrder = []string{"explicit:fabric-a-port-1", "explicit:fabric-a-port-2"}
	if warnings := xcclRailInferenceWarningsForPlan(plan); len(warnings) != 0 {
		t.Fatalf("explicit rail IDs should suppress inference warning: %#v", warnings)
	}
}

func TestXCCLMissingRDMAIPUsesOptionalSlotFallback(t *testing.T) {
	plan := xcclTargetPlan{Target: Target{Name: "node-a"}, RDMADeviceOrder: []string{"dev0"}, RDMARailOrder: []string{"slot:rdma1"}}
	warnings := xcclRailInferenceWarningsForPlan(plan)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "optional") || !strings.Contains(warnings[0], "topology/inventory order") {
		t.Fatalf("slot fallback warning = %#v", warnings)
	}
}

func TestPrintXCCLRankEnvironmentsIncludesActualPerHostValues(t *testing.T) {
	var output bytes.Buffer
	cfg := spec.CheckXCCLConfig{XPUHome: "/usr/local/xpu", Timeout: 120, Environment: map[string]string{"CUSTOM_FLAG": "value"}}
	plans := []xcclTargetPlan{{
		Target: Target{Name: "node-a"}, XPUCount: 2, XPUOrder: []int{2, 3},
		RDMANICOrder: []string{"eth1", "eth2"}, SocketInterface: "bond0",
	}}
	printXCCLRankEnvironments(Options{Output: &output}, cfg, plans, "/tmp/run", true)
	for _, want := range []string{
		"INFO xccl environment: host=node-a",
		"ENV xccl node-a BKCL_RDMA_NICS=eth1,eth2",
		"ENV xccl node-a BKCL_SOCKET_IFNAME=bond0",
		"ENV xccl node-a CUDA_VISIBLE_DEVICES=2,3",
		"ENV xccl node-a XPU_VISIBLE_DEVICES=2,3",
		"ENV xccl node-a CUSTOM_FLAG=value",
		"ENV xccl node-a LD_LIBRARY_PATH=/tmp/run/runtime/xccl_Linux_x86_64/so:",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q in environment log:\n%s", want, output.String())
		}
	}
}

func TestXCCLResultMarksDegradedTopologyAsWarning(t *testing.T) {
	var output bytes.Buffer
	opts := Options{
		Bundle: spec.Bundle{Check: spec.CheckConfig{XCCL: spec.CheckXCCLConfig{Test: "all_reduce", EvaluationMode: "disabled"}}},
		Output: &output,
	}
	plans := []xcclTargetPlan{{
		Target:  Target{Name: "node-a"},
		Mapping: []string{"XPU0=ens11np0(mlx5_1,NODE)"},
	}}
	err := printXCCLResult(opts, plans, 1, []xcclPerformanceRow{{SizeBytes: 134217728, TimeUS: 2100.25, AlgGBs: 63.91, BusGBs: 59.92}})
	if err != nil {
		t.Fatalf("degraded topology without a bandwidth threshold should warn, not fail: %v", err)
	}
	for _, want := range []string{"WARN", "DEGRADED", "PCIe/NUMA"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("expected %q in degraded XCCL summary:\n%s", want, output.String())
		}
	}
}

func TestRunXCCLDryRunDiscoversTopologyAndPrintsEnvironment(t *testing.T) {
	tempDir := t.TempDir()
	mpichArchive := filepath.Join(tempDir, "mpich.tar.gz")
	xcclArchive := filepath.Join(tempDir, "xccl.tar.gz")
	for _, path := range []string{mpichArchive, xcclArchive} {
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatalf("write artifact: %v", err)
		}
	}
	bundle := spec.Bundle{Check: spec.CheckConfig{XCCL: spec.CheckXCCLConfig{
		Enabled:      true,
		MPICHArchive: mpichArchive,
		XCCLArchive:  xcclArchive,
		MachineClass: "vc",
	}}}
	bundle.ApplyDefaults()
	var output bytes.Buffer
	err := Run(Options{
		Bundle: bundle,
		Records: []spec.MachineRecord{
			{HostID: "node-a", MgmtIP: "10.0.0.1", RDMA: []spec.RDMARecord{{Name: "ens11np0", IP: "10.1.0.1"}, {Name: "ens13np0", IP: "10.2.0.1"}, {Name: "ens15np0", IP: "10.3.0.1"}, {Name: "ens17np0", IP: "10.4.0.1"}}},
			{HostID: "node-b", MgmtIP: "10.0.0.2", RDMA: []spec.RDMARecord{{Name: "ens11np0", IP: "10.1.0.2"}, {Name: "ens13np0", IP: "10.2.0.2"}, {Name: "ens15np0", IP: "10.3.0.2"}, {Name: "ens17np0", IP: "10.4.0.2"}}},
		},
		Hosts:   []string{"node-a,node-b"},
		RunXCCL: true,
		DryRun:  true,
		Output:  &output,
		CommandRunner: func(_ spec.CheckConfig, target Target, command string) (string, error) {
			switch {
			case command == "xpu-smi topo -m":
				return sampleXPUTopology, nil
			case strings.Contains(command, "/sys/class/net/"):
				for idx, iface := range []string{"ens11np0", "ens13np0", "ens15np0", "ens17np0"} {
					if strings.Contains(command, iface) {
						return fmt.Sprintf("mlx5_%d\n", idx+1), nil
					}
				}
			case strings.Contains(command, "ip -o -4 addr show"):
				return "bond0\n", nil
			}
			return "", fmt.Errorf("unexpected command for %s: %s", target.Name, command)
		},
	})
	if err != nil {
		t.Fatalf("XCCL dry-run: %v\n%s", err, output.String())
	}
	got := output.String()
	for _, want := range []string{
		"INFO xccl topology: node-a xpus=8 xpu_order=0,1,2,3,4,5,6,7 socket_iface=ens11np0",
		"unique_rdma_nics(4)=ens11np0,ens13np0,ens15np0,ens17np0",
		"rdma_nics(8)=ens11np0,ens11np0,ens13np0,ens13np0,ens15np0,ens15np0,ens17np0,ens17np0",
		"force_order(8)=ens11np0,ens11np0,ens13np0,ens13np0,ens15np0,ens15np0,ens17np0,ens17np0",
		"dry-run xccl copy node-a:",
		"BKCL_ENABLE_XDR='1'",
		"BKCL_SOCKET_IFNAME='ens11np0'",
		"CUDA_VISIBLE_DEVICES='0,1,2,3,4,5,6,7'",
		"XPU_VISIBLE_DEVICES='0,1,2,3,4,5,6,7'",
		"INFO xccl ranks: np=16 source=auto discovered_xpus=16",
		"mpiexec.hydra",
		"ranks=16",
		"remove only key marker envinit-xccl-dry-run",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in dry-run output:\n%s", want, got)
		}
	}
}

func TestRunSingleHostXCCLDryRunUsesLocalLauncherWithoutSSHAuthorization(t *testing.T) {
	tempDir := t.TempDir()
	mpichArchive := filepath.Join(tempDir, "mpich.tar.gz")
	xcclArchive := filepath.Join(tempDir, "xccl.tar.gz")
	for _, path := range []string{mpichArchive, xcclArchive} {
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatalf("write artifact: %v", err)
		}
	}
	bundle := spec.Bundle{Check: spec.CheckConfig{XCCL: spec.CheckXCCLConfig{
		Enabled:      true,
		MPICHArchive: mpichArchive,
		XCCLArchive:  xcclArchive,
		MachineClass: "vc",
	}}}
	bundle.ApplyDefaults()
	var output bytes.Buffer
	err := Run(Options{
		Bundle: bundle,
		Records: []spec.MachineRecord{{
			HostID: "node-a", MgmtIP: "10.0.0.1",
			RDMA: []spec.RDMARecord{{Name: "ens11np0", IP: "10.1.0.1"}, {Name: "ens13np0", IP: "10.2.0.1"}, {Name: "ens15np0", IP: "10.3.0.1"}, {Name: "ens17np0", IP: "10.4.0.1"}},
		}},
		Hosts:   []string{"node-a"},
		RunXCCL: true,
		DryRun:  true,
		Output:  &output,
		CommandRunner: func(_ spec.CheckConfig, _ Target, command string) (string, error) {
			switch {
			case command == "xpu-smi topo -m":
				return sampleDirectIBDeviceXPUTopology, nil
			case strings.Contains(command, "/sys/class/net/"):
				for idx, iface := range []string{"ens11np0", "ens13np0", "ens15np0", "ens17np0"} {
					if strings.Contains(command, iface) {
						return fmt.Sprintf("mlx5_%d\n", idx+1), nil
					}
				}
			case strings.Contains(command, "ip -o -4 addr show"):
				return "bond0\n", nil
			}
			return "", fmt.Errorf("unexpected command: %s", command)
		},
	})
	if err != nil {
		t.Fatalf("single-host XCCL dry-run: %v\n%s", err, output.String())
	}
	got := output.String()
	for _, want := range []string{
		"INFO xccl topology: node-a xpus=8 xpu_order=0,1,2,3,4,5,6,7 socket_iface=ens11np0",
		"local Hydra processes on node-a",
		"authorized_keys will not be modified",
		"'-launcher' 'fork'",
		"ranks=8",
		"do not touch authorized_keys",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in single-host dry-run output:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"temporary authorization", "ssh-wrapper", "/hosts'"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("did not expect %q in single-host dry-run output:\n%s", unwanted, got)
		}
	}
}

func TestXCCLDryRunIgnoresLegacyGroupCountAndResolvesActualDevices(t *testing.T) {
	bundle := spec.Bundle{
		Check: spec.CheckConfig{
			Bandwidth: spec.CheckBandwidthConfig{
				RDMAGroups: []spec.CheckRDMAGroup{{IBDevice: "legacy_mlx5", XPUOffsets: []string{"0x1"}}},
			},
		},
	}
	target := Target{
		Name: "node-a",
		RDMA: []spec.RDMARecord{{Name: "ens11np0"}, {Name: "ens13np0"}},
	}
	resolved, err := resolveBandwidthGroups(Options{
		Bundle:  bundle,
		RunXCCL: true,
		DryRun:  true,
		Output:  &bytes.Buffer{},
		CommandRunner: func(_ spec.CheckConfig, _ Target, command string) (string, error) {
			switch {
			case strings.Contains(command, "ens11np0"):
				return "mlx5_1\n", nil
			case strings.Contains(command, "ens13np0"):
				return "mlx5_2\n", nil
			default:
				return "", fmt.Errorf("unexpected command: %s", command)
			}
		},
	}, []Target{target})
	if err != nil {
		t.Fatalf("resolve XCCL groups: %v", err)
	}
	groups := resolved[target.Name]
	if len(groups) != 2 || groups[0].IBDevice != "mlx5_1" || groups[1].IBDevice != "mlx5_2" {
		t.Fatalf("XCCL dry-run must use all inventory NICs and actual devices, got %#v", groups)
	}
}

func TestValidateXCCLPlanConsistencyAllowsDifferentLocalNames(t *testing.T) {
	plans := []xcclTargetPlan{
		{Target: Target{Name: "node-a"}, XPUCount: 2, RDMANICOrder: []string{"ens11np0", "ens11np0"}, RDMADeviceOrder: []string{"mlx5_0", "mlx5_0"}, RDMALinkOrder: []string{"PIX", "PIX"}, RDMARailOrder: []string{"10.61.11.0/24", "10.61.11.0/24"}, SocketInterface: "bond0"},
		{Target: Target{Name: "node-b"}, XPUCount: 2, RDMANICOrder: []string{"rdma0", "rdma0"}, RDMADeviceOrder: []string{"mlx5_8", "mlx5_8"}, RDMALinkOrder: []string{"PIX", "PIX"}, RDMARailOrder: []string{"10.61.11.0/24", "10.61.11.0/24"}, SocketInterface: "eth0"},
	}
	if err := validateXCCLPlanConsistency(plans); err != nil {
		t.Fatalf("local interface/device/socket names must not be compared across hosts: %v", err)
	}
}

func TestValidateXCCLPlanConsistencyRejectsDifferentRailOrder(t *testing.T) {
	plans := []xcclTargetPlan{
		{Target: Target{Name: "node-a"}, XPUCount: 2, RDMADeviceOrder: []string{"mlx5_0", "mlx5_1"}, RDMALinkOrder: []string{"PIX", "PIX"}, RDMARailOrder: []string{"10.61.11.0/24", "10.61.13.0/24"}},
		{Target: Target{Name: "node-b"}, XPUCount: 2, RDMADeviceOrder: []string{"mlx5_4", "mlx5_5"}, RDMALinkOrder: []string{"PIX", "PIX"}, RDMARailOrder: []string{"10.61.13.0/24", "10.61.11.0/24"}},
	}
	err := validateXCCLPlanConsistency(plans)
	if err == nil || !strings.Contains(err.Error(), "rail order differs") {
		t.Fatalf("expected semantic rail-order error, got %v", err)
	}
}

func TestConfiguredXCCLPlanConsistencyCanBeDisabled(t *testing.T) {
	disabled := false
	plans := []xcclTargetPlan{
		{Target: Target{Name: "node-a"}, XPUCount: 8},
		{Target: Target{Name: "node-b"}, XPUCount: 4},
	}
	if err := validateConfiguredXCCLPlanConsistency(spec.CheckXCCLConfig{ValidateTopology: &disabled}, plans); err != nil {
		t.Fatalf("disabled topology validation must allow a forced run: %v", err)
	}
}

func TestValidateXCCLConfigRejectsManagedEnvironmentOverride(t *testing.T) {
	tempDir := t.TempDir()
	mpichArchive := filepath.Join(tempDir, "mpich.tar.gz")
	xcclArchive := filepath.Join(tempDir, "xccl.tar.gz")
	if err := os.WriteFile(mpichArchive, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(xcclArchive, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	enableXDR := true
	err := validateXCCLConfig(spec.CheckXCCLConfig{
		MPICHArchive: mpichArchive,
		XCCLArchive:  xcclArchive,
		WorkRoot:     "/tmp/envinit-xccl-check",
		XPUHome:      "/usr/local/xpu",
		Test:         "all_reduce",
		MinBytes:     "128m",
		MaxBytes:     "128m",
		StepFactor:   2,
		Iterations:   20,
		Timeout:      120,
		DataType:     "float",
		MachineClass: "vc",
		EnableXDR:    &enableXDR,
		Environment:  map[string]string{"BKCL_RDMA_NICS": "wrong0"},
	})
	if err == nil || !strings.Contains(err.Error(), "managed by envinit") {
		t.Fatalf("expected managed environment error, got %v", err)
	}
}

func TestValidateXCCLConfigAllowsTunableBKCLOverrides(t *testing.T) {
	tempDir := t.TempDir()
	mpichArchive := filepath.Join(tempDir, "mpich.tar.gz")
	xcclArchive := filepath.Join(tempDir, "xccl.tar.gz")
	for _, path := range []string{mpichArchive, xcclArchive} {
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := spec.CheckXCCLConfig{
		MPICHArchive: mpichArchive, XCCLArchive: xcclArchive,
		WorkRoot: "/tmp/envinit-xccl-check", XPUHome: "/usr/local/xpu",
		Test: "all_reduce", MinBytes: "1m", MaxBytes: "2g", StepFactor: 2,
		Iterations: 20, Timeout: 120, DataType: "fp16",
		Environment: map[string]string{"BKCL_DEBUG": "1", "BKCL_FLAT_RING": "0", "BCCL_ERROR_FILE": "/tmp/bccl.%h.%p.log"},
	}
	if err := validateXCCLConfig(cfg); err != nil {
		t.Fatalf("tunable BKCL environment override should validate: %v", err)
	}
	plan := xcclTargetPlan{RDMANICOrder: []string{"eth1"}}
	env := xcclManagedRankEnvironment(cfg, plan, true)
	if env["BKCL_DEBUG"] != "1" || env["BKCL_FLAT_RING"] != "0" || env["BCCL_ERROR_FILE"] != "/tmp/bccl.%h.%p.log" {
		t.Fatalf("environment overrides were not applied last: %#v", env)
	}
}

func TestSingleHostEnvironmentDoesNotInheritMultiHostScriptVariables(t *testing.T) {
	cfg := spec.CheckXCCLConfig{
		Timeout: 120,
		Environment: map[string]string{
			"BKCL_FLAT_RING": "0",
			"BKCL_DEBUG":     "1",
			"CUSTOM_FLAG":    "single-ok",
		},
	}
	env := xcclManagedRankEnvironment(cfg, xcclTargetPlan{RDMANICOrder: []string{"eth1"}}, false)
	for _, key := range []string{"BKCL_FLAT_RING", "BKCL_DEBUG"} {
		if _, ok := env[key]; ok {
			t.Fatalf("single-host environment unexpectedly contains multi-host variable %s: %#v", key, env)
		}
	}
	if env["CUSTOM_FLAG"] != "single-ok" {
		t.Fatalf("single-host custom environment was lost: %#v", env)
	}
}

func TestGeneratedXCCLShellIsPortableSyntax(t *testing.T) {
	enableXDR := true
	cfg := spec.CheckXCCLConfig{
		XPUHome:     "/usr/local/xpu",
		Test:        "all_reduce",
		Timeout:     120,
		EnableXDR:   &enableXDR,
		StepFactor:  2,
		Iterations:  20,
		DataType:    "float",
		MinBytes:    "128m",
		MaxBytes:    "128m",
		Environment: map[string]string{"BKCL_DEBUG": "1"},
	}
	plan := xcclTargetPlan{
		RDMANICs:        []string{"ens11np0", "ens13np0"},
		RDMANICOrder:    []string{"ens11np0", "ens11np0", "ens13np0", "ens13np0"},
		SocketInterface: "bond0",
	}
	checkCfg := spec.CheckConfig{SSH: spec.CheckSSHConfig{User: "root", Options: []string{"-p", "22"}}}
	workDir := "/tmp/envinit-xccl-check/run"
	scripts := map[string]string{
		"rank":           xcclRankScript(cfg, plan, workDir, true),
		"ssh-wrapper":    xcclSSHWrapper(checkCfg, workDir),
		"install-multi":  xcclInstallRuntimeCommand(cfg, workDir, true),
		"install-single": xcclInstallRuntimeCommand(cfg, workDir, false),
		"authorize":      xcclAuthorizeKeyCommand(workDir, "envinit-xccl-run"),
		"cleanup-multi":  xcclCleanupCommand(workDir, "envinit-xccl-run", true),
		"cleanup-single": xcclCleanupCommand(workDir, "envinit-xccl-run", false),
		"tracked-mpirun": xcclTrackedMPIRunCommand(workDir, []string{"mpirun", "-n", "2"}),
	}
	for name, script := range scripts {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command("sh", "-n")
			cmd.Stdin = strings.NewReader(script)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("generated %s shell syntax is invalid: %v\n%s\n%s", name, err, output, script)
			}
		})
	}
}

func TestXCCLCleanupOnlyRemovesMarkedAuthorization(t *testing.T) {
	command := xcclCleanupCommand("/tmp/envinit-xccl-check/run", "envinit-xccl-run", true)
	for _, want := range []string{
		"awk -v marker='envinit-xccl-run'",
		"index($0, marker) == 0",
		"authorized-keys-created",
		"mpich-link-created",
		"mpirun.pid",
		"/proc/$p/cmdline",
		"rm -rf -- '/tmp/envinit-xccl-check/run'",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("expected %q in cleanup command:\n%s", want, command)
		}
	}
	if strings.Contains(command, "rm -f \"$HOME/.ssh/authorized_keys\"") {
		t.Fatalf("cleanup must not unconditionally remove authorized_keys:\n%s", command)
	}
}

func TestSingleHostXCCLCleanupDoesNotTouchSSHFiles(t *testing.T) {
	command := xcclCleanupCommand("/tmp/envinit-xccl-check/run", "envinit-xccl-run", false)
	for _, unwanted := range []string{"authorized_keys", "$HOME/.ssh", "awk -v marker"} {
		if strings.Contains(command, unwanted) {
			t.Fatalf("single-host cleanup must not reference %q:\n%s", unwanted, command)
		}
	}
	for _, want := range []string{"mpich-link-created", "rm -rf -- '/tmp/envinit-xccl-check/run'"} {
		if !strings.Contains(command, want) {
			t.Fatalf("expected %q in single-host cleanup:\n%s", want, command)
		}
	}
}
