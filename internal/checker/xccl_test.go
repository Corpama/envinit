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
			{Name: "ens11np0"},
			{Name: "ens13np0"},
			{Name: "ens15np0"},
			{Name: "ens17np0"},
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
	if !strings.Contains(strings.Join(plan.Mapping, ","), "XPU7=ens17np0(mlx5_4,PIX)") {
		t.Fatalf("unexpected mapping: %#v", plan.Mapping)
	}
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
		RDMANICs:        []string{"ens11np0", "ens13np0"},
		RDMANICOrder:    []string{"ens11np0", "ens11np0", "ens13np0", "ens13np0"},
		SocketInterface: "bond0",
	}
	script := xcclRankScript(cfg, plan, "/tmp/envinit-xccl-check/run")
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
		"BKCL_TREE_THRESHOLD='0'",
		"BKCL_DEBUG='1'",
		"exec '/tmp/envinit-xccl-check/run/runtime/xccl_Linux_x86_64/perf/all_reduce' \"$@\"",
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
}

func TestXCCLResultMarksDegradedTopologyAsWarning(t *testing.T) {
	var output bytes.Buffer
	opts := Options{
		Bundle: spec.Bundle{Check: spec.CheckConfig{XCCL: spec.CheckXCCLConfig{Test: "all_reduce"}}},
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
		"INFO xccl topology: node-a xpus=8 socket_iface=bond0",
		"force_order=ens11np0,ens11np0,ens13np0,ens13np0,ens15np0,ens15np0,ens17np0,ens17np0",
		"dry-run xccl copy node-a:",
		"BKCL_ENABLE_XDR='1'",
		"BKCL_SOCKET_IFNAME='bond0'",
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
			return "", fmt.Errorf("unexpected command: %s", command)
		},
	})
	if err != nil {
		t.Fatalf("single-host XCCL dry-run: %v\n%s", err, output.String())
	}
	got := output.String()
	for _, want := range []string{
		"INFO xccl topology: node-a xpus=8 socket_iface=bond0",
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

func TestValidateXCCLPlanConsistencyRejectsDifferentPerHostMapping(t *testing.T) {
	plans := []xcclTargetPlan{
		{Target: Target{Name: "node-a"}, XPUCount: 2, RDMANICOrder: []string{"ens11np0", "ens11np0"}, SocketInterface: "bond0"},
		{Target: Target{Name: "node-b"}, XPUCount: 2, RDMANICOrder: []string{"ens13np0", "ens13np0"}, SocketInterface: "bond0"},
	}
	err := validateXCCLPlanConsistency(plans)
	if err == nil || !strings.Contains(err.Error(), "same RDMA interface names") {
		t.Fatalf("expected inconsistent XCCL mapping error, got %v", err)
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
		EnableXDR:    &enableXDR,
		Environment:  map[string]string{"BKCL_RDMA_NICS": "wrong0"},
	})
	if err == nil || !strings.Contains(err.Error(), "managed by envinit") {
		t.Fatalf("expected managed environment error, got %v", err)
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
		"rank":           xcclRankScript(cfg, plan, workDir),
		"ssh-wrapper":    xcclSSHWrapper(checkCfg, workDir),
		"install-multi":  xcclInstallRuntimeCommand(cfg, workDir, true),
		"install-single": xcclInstallRuntimeCommand(cfg, workDir, false),
		"authorize":      xcclAuthorizeKeyCommand(workDir, "envinit-xccl-run"),
		"cleanup-multi":  xcclCleanupCommand(workDir, "envinit-xccl-run", true),
		"cleanup-single": xcclCleanupCommand(workDir, "envinit-xccl-run", false),
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
