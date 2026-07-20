package checker

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"

	"envinit/internal/spec"
)

func TestResolveTargetsUsesInventoryMgmtIP(t *testing.T) {
	targets, err := ResolveTargets([]spec.MachineRecord{
		{HostID: "node1", Hostname: "xpu-node1", MgmtIP: "10.157.5.207"},
		{HostID: "node2", Hostname: "xpu-node2", MgmtIP: "10.157.5.206"},
	}, []string{"node1,xpu-node2"})
	if err != nil {
		t.Fatalf("resolve targets: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("unexpected targets: %#v", targets)
	}
	if targets[0].Address != "10.157.5.207" || targets[1].Address != "10.157.5.206" {
		t.Fatalf("unexpected addresses: %#v", targets)
	}
	if targetControlAddress(targets[0]) != "10.157.5.207" || targetControlAddress(targets[1]) != "10.157.5.206" {
		t.Fatalf("unexpected control addresses: %#v", targets)
	}
}

func TestResolveDiscoveryTargetExplicitIdentityAndEndpoint(t *testing.T) {
	targets, err := ResolveDiscoveryTargets([]spec.MachineRecord{
		{HostID: "node1", Hostname: "instance-node1"},
	}, []string{"node1=192.168.32.11"})
	if err != nil {
		t.Fatalf("resolve explicit discovery target: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("unexpected targets: %#v", targets)
	}
	target := targets[0]
	if !target.InventoryMatched || !target.ExplicitIdentity || target.InventoryIdentity != "node1" {
		t.Fatalf("explicit inventory binding was not preserved: %#v", target)
	}
	if got := targetControlAddress(target); got != "192.168.32.11" {
		t.Fatalf("control address = %q, want explicit SSH endpoint", got)
	}
	if target.Address != "192.168.32.11" {
		t.Fatalf("initial management address = %q, want reachable endpoint until discovery selects management IP", target.Address)
	}
}

func TestResolveDiscoveryTargetAllowsExplicitNewIdentity(t *testing.T) {
	targets, err := ResolveDiscoveryTargets(nil, []string{"node9=192.168.32.19"})
	if err != nil {
		t.Fatalf("resolve explicit new target: %v", err)
	}
	target := targets[0]
	if target.InventoryMatched || !target.ExplicitIdentity || target.InventoryIdentity != "node9" {
		t.Fatalf("unexpected explicit new target: %#v", target)
	}
	if got := targetControlAddress(target); got != "192.168.32.19" {
		t.Fatalf("control address = %q, want 192.168.32.19", got)
	}
}

func TestResolveDiscoveryTargetRejectsInvalidExplicitSyntax(t *testing.T) {
	for _, input := range []string{"=192.168.32.11", "node1=", "node1=192.168.32.11=extra"} {
		if _, err := ResolveDiscoveryTargets(nil, []string{input}); err == nil {
			t.Fatalf("expected invalid target %q to fail", input)
		}
	}
}

func TestInventoryRDMAInterfacesOverrideLongerBundleDefaults(t *testing.T) {
	bundle := spec.Bundle{Defaults: spec.Defaults{RDMAInterfaces: []spec.RDMAInterfaceDefault{
		{Name: "default1"},
		{Name: "default2"},
		{Name: "default3"},
		{Name: "default4"},
	}}, Check: spec.CheckConfig{Bandwidth: spec.CheckBandwidthConfig{RDMAGroups: []spec.CheckRDMAGroup{
		{IBDevice: "mlx5_0"},
		{IBDevice: "mlx5_1"},
		{IBDevice: "mlx5_2"},
		{IBDevice: "mlx5_3"},
	}}}}
	target := Target{RDMA: []spec.RDMARecord{{Name: "ens1"}, {Name: "ens2"}}}
	interfaces := targetCounterInterfaces(bundle, target)
	if got := strings.Join(interfaces, ","); got != "ens1,ens2" {
		t.Fatalf("expected inventory interface count to win, got %q", got)
	}

	groups, err := resolveBandwidthGroups(Options{Bundle: bundle, DryRun: true}, []Target{{
		Name: "node1",
		RDMA: target.RDMA,
	}})
	if err != nil {
		t.Fatalf("resolve groups: %v", err)
	}
	if len(groups["node1"]) != 2 {
		t.Fatalf("expected 2 bandwidth groups, got %#v", groups["node1"])
	}

	source := Target{Name: "node1", RDMA: []spec.RDMARecord{{Name: "ens1", IP: "10.1.1.1"}, {Name: "ens2", IP: "10.1.2.1"}}}
	destination := Target{Name: "node2", RDMA: []spec.RDMARecord{{Name: "ens1", IP: "10.1.1.2"}, {Name: "ens2", IP: "10.1.2.2"}}}
	items, err := rdmaPingItems(bundle, source, destination)
	if err != nil {
		t.Fatalf("build RDMA ping items: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("expected a 2x2 matrix without phantom defaults, got %#v", items)
	}
}

func TestRunRejectsSingleHostForBandwidth(t *testing.T) {
	err := Run(Options{
		Bundle:       spec.Bundle{},
		Records:      []spec.MachineRecord{{HostID: "node1", MgmtIP: "10.0.0.1"}},
		Hosts:        []string{"node1"},
		RunBandwidth: true,
		DryRun:       true,
		Output:       io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "single-host mode is supported only") {
		t.Fatalf("expected single-host bandwidth rejection, got %v", err)
	}
}

func TestRunDryRunPrintsExpectedCommands(t *testing.T) {
	var output bytes.Buffer
	bundle := spec.Bundle{
		Check: spec.CheckConfig{
			Bandwidth: spec.CheckBandwidthConfig{
				Iterations:  100,
				ReportGBits: true,
				RDMAGroups: []spec.CheckRDMAGroup{
					{
						IBDevice: "mlx5_1",
					},
				},
			},
		},
	}
	bundle.ApplyDefaults()
	err := Run(Options{
		Bundle: bundle,
		Records: []spec.MachineRecord{
			{HostID: "node1", Hostname: "node1", MgmtIP: "10.157.5.207"},
			{HostID: "node2", Hostname: "node2", MgmtIP: "10.157.5.206"},
		},
		Hosts:  []string{"node1,node2"},
		DryRun: true,
		Output: &output,
	})
	if err != nil {
		t.Fatalf("dry-run check: %v", err)
	}
	got := output.String()
	for _, want := range []string{
		"dry-run server node1:",
		"'-n' '100'",
		"dry-run client node2:",
		"'-F'",
		"'-R'",
		"'10.157.5.207'",
		"dry-run server node2:",
		"dry-run client node1:",
		"Bandwidth result summary:",
		"STATUS",
		"CLIENT",
		"SERVER",
		"CLIENT_IP",
		"SERVER_IP",
		"18515",
		"BANDWIDTH",
		"PASS    node1",
		"PASS    node2",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in output:\n%s", want, got)
		}
	}
	clientLineStart := strings.Index(got, "dry-run client node2:")
	clientLineEnd := strings.Index(got[clientLineStart:], "\n")
	clientLine := got[clientLineStart : clientLineStart+clientLineEnd]
	if strings.Contains(clientLine, "--run_infinitely") {
		t.Fatalf("did not expect client to run infinitely:\n%s", clientLine)
	}
	if strings.Contains(got, "'-D'") || strings.Contains(got, "'-x'") || strings.Contains(got, "--run_infinitely") {
		t.Fatalf("did not expect duration, gid index, or run infinitely args:\n%s", got)
	}
	if strings.Contains(got, "--mmap") || strings.Contains(got, "'-s'") || strings.Contains(got, "xpu(client=") {
		t.Fatalf("did not expect default bandwidth check to use mmap, message size, or xpu labels:\n%s", got)
	}
	if !strings.HasSuffix(clientLine, "'10.157.5.207'") {
		t.Fatalf("expected client address to be positional final argument:\n%s", clientLine)
	}
}

func TestRunDryRunDerivesRDMAGroupsFromInventory(t *testing.T) {
	var output bytes.Buffer
	bundle := spec.Bundle{Check: spec.CheckConfig{Bandwidth: spec.CheckBandwidthConfig{Iterations: 100}}}
	bundle.ApplyDefaults()
	err := Run(Options{
		Bundle: bundle,
		Records: []spec.MachineRecord{
			{
				HostID: "node1", MgmtIP: "10.157.5.207",
				RDMA: []spec.RDMARecord{{Name: "ens11np0", IP: "10.247.1.11"}, {Name: "ens13np0", IP: "10.247.2.11"}},
			},
			{
				HostID: "node2", MgmtIP: "10.157.5.206",
				RDMA: []spec.RDMARecord{{Name: "ens11np0", IP: "10.247.1.12"}, {Name: "ens13np0", IP: "10.247.2.12"}},
			},
		},
		Hosts:  []string{"node1,node2"},
		DryRun: true,
		Output: &output,
	})
	if err != nil {
		t.Fatalf("dry-run without rdma_groups: %v", err)
	}
	got := output.String()
	for _, want := range []string{
		"<resolve-ib-device:ens11np0>",
		"<resolve-ib-device:ens13np0>",
		"10.247.1.11",
		"10.247.2.12",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in inventory-derived dry-run output:\n%s", want, got)
		}
	}
}

func TestRunXDRDryRunPerformsReadOnlyTopologyDiscovery(t *testing.T) {
	var output bytes.Buffer
	topologyCalls := 0
	bundle := spec.Bundle{Check: spec.CheckConfig{Bandwidth: spec.CheckBandwidthConfig{Iterations: 100, MmapDevice: "/dev/xdrdrv"}}}
	err := Run(Options{
		Bundle: bundle,
		Records: []spec.MachineRecord{
			{HostID: "node1", MgmtIP: "10.157.5.207", RDMA: []spec.RDMARecord{{Name: "ens11np0", IP: "10.247.1.11"}}},
			{HostID: "node2", MgmtIP: "10.157.5.206", RDMA: []spec.RDMARecord{{Name: "ens11np0", IP: "10.247.1.12"}}},
		},
		Hosts:  []string{"node1,node2"},
		DryRun: true,
		Output: &output,
		CommandRunner: func(_ spec.CheckConfig, _ Target, command string) (string, error) {
			if command == "xpu-smi topo -m" {
				topologyCalls++
				return " XPU0 NIC0\nXPU0 X PIX\nNIC Legend:\nNIC0: mlx5_7\n", nil
			}
			if strings.Contains(command, "/sys/class/net/") {
				return "mlx5_7\n", nil
			}
			return "", fmt.Errorf("unexpected discovery command: %s", command)
		},
	})
	if err != nil {
		t.Fatalf("xdr dry-run discovery: %v", err)
	}
	if topologyCalls != 2 {
		t.Fatalf("expected one topology call per target, got %d", topologyCalls)
	}
	got := output.String()
	for _, want := range []string{
		"dry-run discovery xdr topology",
		"ib_device=mlx5_7",
		"'--mmap-offset=0x0000000090001000'",
		"'-d' 'mlx5_7'",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in xdr discovery dry-run output:\n%s", want, got)
		}
	}
}

func TestIBWriteBWArgsIncludesConfiguredQPs(t *testing.T) {
	args := ibWriteBWArgs(spec.CheckBandwidthConfig{
		Iterations:   100,
		BandwidthQPs: 4,
		ReportGBits:  true,
	}, spec.CheckRDMAGroup{IBDevice: "mlx5_1"}, "", "10.247.1.12", 18515)
	got := shellJoin(args)
	if !strings.Contains(got, "'-d' 'mlx5_1' '-q' '4'") {
		t.Fatalf("expected configured QP count in ib_write_bw args:\n%s", got)
	}
}

func TestIBWriteBWArgsVerbsUsesDurationGIDAndNoRDMACM(t *testing.T) {
	args := ibWriteBWArgsForMode(spec.CheckBandwidthConfig{
		Duration:      10,
		RunByDuration: true,
		GIDIndex:      3,
		BandwidthQPs:  4,
		MessageSize:   1048576,
		ReportGBits:   true,
	}, spec.CheckRDMAGroup{IBDevice: "mlx5_2"}, "", "192.168.32.11", 18520, BandwidthModeVerbs)
	got := shellJoin(args)
	for _, want := range []string{"'-D' '10'", "'-f' '2'", "'-N'", "'-d' 'mlx5_2'", "'-i' '1'", "'-x' '3'", "'-q' '4'", "'-s' '1048576'", "'192.168.32.11'"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %s in Verbs command:\n%s", want, got)
		}
	}
	if strings.Contains(got, "'-R'") || strings.Contains(got, "'-n'") {
		t.Fatalf("Verbs duration command must not use RDMA-CM or iteration mode:\n%s", got)
	}
}

func TestBandwidthAddressUsesControlForVerbsAndRDMAIPForCM(t *testing.T) {
	server := Target{ControlAddress: "192.168.32.11", Address: "192.168.32.11", RDMA: []spec.RDMARecord{{IP: "25.16.2.2"}}}
	stream := checkStream{ServerRDMAIndex: 0}
	verbs := Options{bandwidthMode: BandwidthModeVerbs}
	if got := bandwidthAddressForMode(verbs, server, stream); got != "192.168.32.11" {
		t.Fatalf("expected Verbs control address, got %q", got)
	}
	cm := Options{bandwidthMode: BandwidthModeRDMACM}
	if got := bandwidthAddressForMode(cm, server, stream); got != "25.16.2.2" {
		t.Fatalf("expected RDMA-CM peer RDMA IP, got %q", got)
	}
}

func TestNormalizedBandwidthModesPreservesLegacyRDMACMDefault(t *testing.T) {
	legacy := normalizedBandwidthModes(nil)
	if len(legacy) != 1 || legacy[0] != BandwidthModeRDMACM {
		t.Fatalf("unexpected legacy modes: %v", legacy)
	}
	both := normalizedBandwidthModes([]string{"verbs", "rdma-cm", "verbs"})
	if len(both) != 2 || both[0] != BandwidthModeVerbs || both[1] != BandwidthModeRDMACM {
		t.Fatalf("unexpected normalized modes: %v", both)
	}
}

func TestRunRejectsNegativeBundleBandwidthQPs(t *testing.T) {
	bundle := spec.Bundle{Check: spec.CheckConfig{Bandwidth: spec.CheckBandwidthConfig{
		BandwidthQPs: -1,
		RDMAGroups:   []spec.CheckRDMAGroup{{IBDevice: "mlx5_1"}},
	},
	}}
	err := Run(Options{
		Bundle: bundle,
		Records: []spec.MachineRecord{
			{HostID: "node1", MgmtIP: "10.157.5.207"},
			{HostID: "node2", MgmtIP: "10.157.5.206"},
		},
		Hosts:  []string{"node1,node2"},
		DryRun: true,
		Output: io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "bandwidth_qps") {
		t.Fatalf("expected invalid bandwidth_qps error, got %v", err)
	}
}

func TestRunParallelDryRunUsesEmulatedKVTransferAndXDRMmap(t *testing.T) {
	var output bytes.Buffer
	bundle := spec.Bundle{
		Check: spec.CheckConfig{
			Bandwidth: spec.CheckBandwidthConfig{
				Iterations:  100,
				MessageSize: 8388608,
				ReportGBits: true,
				MmapDevice:  "/dev/xdrdrv",
				Parallel:    true,
				RDMAGroups: []spec.CheckRDMAGroup{
					{
						IBDevice: "mlx5_1",
						XPUOffsets: []string{
							"0x0000000090001000",
							"0x1000000090001000",
						},
					},
				},
			},
		},
	}
	bundle.ApplyDefaults()
	err := Run(Options{
		Bundle: bundle,
		Records: []spec.MachineRecord{
			{HostID: "node1", Hostname: "node1", MgmtIP: "10.157.5.207"},
			{HostID: "node2", Hostname: "node2", MgmtIP: "10.157.5.206"},
		},
		Hosts:  []string{"node1,node2"},
		DryRun: true,
		Output: &output,
	})
	if err != nil {
		t.Fatalf("xdr mmap dry-run check: %v", err)
	}
	got := output.String()
	for _, want := range []string{
		"'--mmap=/dev/xdrdrv'",
		"'--mmap-offset=0x0000000090001000' '-F' '-R' '-p' '18515'",
		"'--mmap-offset=0x1000000090001000' '-F' '-R' '-p' '18516'",
		"'-s' '8388608'",
		"Bandwidth result summary:",
		"0x0000000090001000",
		"0x1000000090001000",
		"unknown",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in output:\n%s", want, got)
		}
	}
}

func TestRunSequentialXDRMmapCoversEveryOffset(t *testing.T) {
	var output bytes.Buffer
	bundle := spec.Bundle{
		Check: spec.CheckConfig{
			Bandwidth: spec.CheckBandwidthConfig{
				Iterations:  100,
				ReportGBits: true,
				MmapDevice:  "/dev/xdrdrv",
				Parallel:    false,
				RDMAGroups: []spec.CheckRDMAGroup{
					{
						IBDevice: "mlx5_1",
						XPUOffsets: []string{
							"0x0000000090001000",
							"0x1000000090001000",
						},
					},
					{
						IBDevice: "mlx5_2",
						XPUOffsets: []string{
							"0x2000000090001000",
							"0x3000000090001000",
						},
					},
				},
			},
		},
	}
	bundle.ApplyDefaults()
	err := Run(Options{
		Bundle: bundle,
		Records: []spec.MachineRecord{
			{HostID: "node1", Hostname: "node1", MgmtIP: "10.157.5.207"},
			{HostID: "node2", Hostname: "node2", MgmtIP: "10.157.5.206"},
		},
		Hosts:  []string{"node1,node2"},
		DryRun: true,
		Output: &output,
	})
	if err != nil {
		t.Fatalf("sequential xdr mmap dry-run check: %v", err)
	}
	got := output.String()
	for _, want := range []string{
		"'--mmap-offset=0x0000000090001000' '-F' '-R' '-p' '18515'",
		"'--mmap-offset=0x1000000090001000' '-F' '-R' '-p' '18516'",
		"'--mmap-offset=0x2000000090001000' '-F' '-R' '-p' '18519'",
		"'--mmap-offset=0x3000000090001000' '-F' '-R' '-p' '18520'",
		"0x0000000090001000  0x0000000090001000",
		"0x1000000090001000  0x1000000090001000",
		"0x2000000090001000  0x2000000090001000",
		"0x3000000090001000  0x3000000090001000",
		"mlx5_1      mlx5_2",
		"mlx5_2      mlx5_1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in output:\n%s", want, got)
		}
	}
}

func TestRunParallelDryRunUsesSameOffsetsAndUniquePorts(t *testing.T) {
	var output bytes.Buffer
	bundle := spec.Bundle{
		Check: spec.CheckConfig{
			Bandwidth: spec.CheckBandwidthConfig{
				Iterations:  100,
				MessageSize: 8388608,
				ReportGBits: true,
				MmapDevice:  "/dev/xdrdrv",
				Parallel:    true,
				RDMAGroups: []spec.CheckRDMAGroup{
					{
						IBDevice: "mlx5_1",
						XPUOffsets: []string{
							"0x0000000090001000",
							"0x1000000090001000",
						},
					},
				},
			},
		},
	}
	bundle.ApplyDefaults()
	err := Run(Options{
		Bundle: bundle,
		Records: []spec.MachineRecord{
			{HostID: "node1", Hostname: "node1", MgmtIP: "10.157.5.207"},
			{HostID: "node2", Hostname: "node2", MgmtIP: "10.157.5.206"},
		},
		Hosts:  []string{"node1,node2"},
		DryRun: true,
		Output: &output,
	})
	if err != nil {
		t.Fatalf("parallel dry-run check: %v", err)
	}
	got := output.String()
	for _, want := range []string{
		"'--mmap-offset=0x0000000090001000' '-F' '-R' '-p' '18515'",
		"'--mmap-offset=0x1000000090001000' '-F' '-R' '-p' '18516'",
		"'-s' '8388608'",
		"Bandwidth result summary:",
		"0x0000000090001000",
		"0x1000000090001000",
		"unknown",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in output:\n%s", want, got)
		}
	}
	clientLineStart := strings.Index(got, "dry-run client node2:")
	clientLineEnd := strings.Index(got[clientLineStart:], "\n")
	clientLine := got[clientLineStart : clientLineStart+clientLineEnd]
	if strings.Contains(clientLine, "--run_infinitely") {
		t.Fatalf("did not expect client to run infinitely:\n%s", clientLine)
	}
	if !strings.HasSuffix(clientLine, "'10.157.5.207'") {
		t.Fatalf("expected client address to be positional final argument:\n%s", clientLine)
	}
}

func TestBandwidthStreamBatchesAvoidRDMAGroupOversubscription(t *testing.T) {
	cfg := spec.CheckBandwidthConfig{
		RDMAGroups: []spec.CheckRDMAGroup{
			{IBDevice: "mlx5_1"},
			{IBDevice: "mlx5_2"},
			{IBDevice: "mlx5_3"},
			{IBDevice: "mlx5_4"},
		},
	}
	batches := bandwidthStreamBatches(cfg)
	assertBandwidthBatches(t, batches, 16, 4)
}

func TestBandwidthStreamBatchesAvoidRDMAGroupOversubscriptionWithXDRMmap(t *testing.T) {
	cfg := spec.CheckBandwidthConfig{
		MmapDevice: "/dev/xdrdrv",
		RDMAGroups: []spec.CheckRDMAGroup{
			{IBDevice: "mlx5_1", XPUOffsets: []string{"0x0", "0x1"}},
			{IBDevice: "mlx5_2", XPUOffsets: []string{"0x2", "0x3"}},
			{IBDevice: "mlx5_3", XPUOffsets: []string{"0x4", "0x5"}},
			{IBDevice: "mlx5_4", XPUOffsets: []string{"0x6", "0x7"}},
		},
	}
	batches := bandwidthStreamBatches(cfg)
	assertBandwidthBatches(t, batches, 64, 4)
}

func TestRunDryRunUsesMatchingRDMAAddressWhenPresent(t *testing.T) {
	var output bytes.Buffer
	bundle := spec.Bundle{
		Check: spec.CheckConfig{
			Bandwidth: spec.CheckBandwidthConfig{
				Iterations:  100,
				ReportGBits: true,
				RDMAGroups: []spec.CheckRDMAGroup{
					{IBDevice: "mlx5_1"},
					{IBDevice: "mlx5_2"},
				},
			},
		},
	}
	bundle.ApplyDefaults()
	err := Run(Options{
		Bundle: bundle,
		Records: []spec.MachineRecord{
			{
				HostID: "node1", Hostname: "node1", MgmtIP: "10.157.5.207",
				RDMA: []spec.RDMARecord{{Name: "ens11np0", IP: "10.247.1.11"}, {Name: "ens13np0", IP: "10.247.2.11"}},
			},
			{
				HostID: "node2", Hostname: "node2", MgmtIP: "10.157.5.206",
				RDMA: []spec.RDMARecord{{Name: "ens11np0", IP: "10.247.1.12"}, {Name: "ens13np0", IP: "10.247.2.12"}},
			},
		},
		Hosts:  []string{"node1,node2"},
		DryRun: true,
		Output: &output,
	})
	if err != nil {
		t.Fatalf("dry-run check: %v", err)
	}
	got := output.String()
	for _, want := range []string{
		"'-d' 'mlx5_1' '--report_gbits' '-F' '-R' '-p' '18515' '10.247.1.11'",
		"'-d' 'mlx5_1' '--report_gbits' '-F' '-R' '-p' '18516' '10.247.2.11'",
		"'-d' 'mlx5_2' '--report_gbits' '-F' '-R' '-p' '18517' '10.247.1.11'",
		"'-d' 'mlx5_2' '--report_gbits' '-F' '-R' '-p' '18518' '10.247.2.11'",
		"CLIENT_NIC",
		"SERVER_NIC",
		"CLIENT_IP",
		"SERVER_IP",
		"ens11np0",
		"ens13np0",
		"10.247.1.11",
		"10.247.2.12",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in output:\n%s", want, got)
		}
	}
}

func TestParseResolvedIBDevices(t *testing.T) {
	got := parseResolvedIBDevices(`
mlx5_3

mlx5_1
mlx5_3
`)
	want := []string{"mlx5_1", "mlx5_3"}
	if len(got) != len(want) {
		t.Fatalf("unexpected device count: got %#v want %#v", got, want)
	}
	for idx := range want {
		if got[idx] != want[idx] {
			t.Fatalf("unexpected devices: got %#v want %#v", got, want)
		}
	}
}

func TestResolveStreamGroupsUsesPerTargetIBDevices(t *testing.T) {
	stream := checkStream{
		ServerGroup:     spec.CheckRDMAGroup{IBDevice: "mlx5_1"},
		ServerRDMAIndex: 0,
		ClientGroup:     spec.CheckRDMAGroup{IBDevice: "mlx5_1"},
		ClientRDMAIndex: 0,
		Port:            18515,
	}
	server := Target{Name: "node-a", Address: "10.0.0.1"}
	client := Target{Name: "node-b", Address: "10.0.0.2"}
	groups := resolvedRDMAGroups{
		"node-a": {{IBDevice: "mlx5_2"}},
		"node-b": {{IBDevice: "mlx5_1"}},
	}
	got := resolveStreamGroups(groups, server, client, stream)
	if got.ServerGroup.IBDevice != "mlx5_2" {
		t.Fatalf("unexpected server device: %#v", got)
	}
	if got.ClientGroup.IBDevice != "mlx5_1" {
		t.Fatalf("unexpected client device: %#v", got)
	}
}

func TestRunRDMAPingDryRunUsesJumboPayload(t *testing.T) {
	var output bytes.Buffer
	bundle := spec.Bundle{}
	bundle.Defaults.RDMAInterfaces = []spec.RDMAInterfaceDefault{
		{Name: "ens15np0"},
		{Name: "ens16np0"},
	}
	bundle.ApplyDefaults()
	err := Run(Options{
		Bundle:      bundle,
		RunRDMAPing: true,
		Records: []spec.MachineRecord{
			{
				HostID: "node1",
				MgmtIP: "10.157.5.207",
				RDMA: []spec.RDMARecord{
					{Name: "ens15np0", IP: "10.247.1.11"},
					{Name: "ens16np0", IP: "10.247.2.11"},
				},
			},
			{
				HostID: "node2",
				MgmtIP: "10.157.5.206",
				RDMA: []spec.RDMARecord{
					{Name: "ens15np0", IP: "10.247.1.12"},
					{Name: "ens16np0", IP: "10.247.2.12"},
				},
			},
		},
		Hosts:  []string{"node1,node2"},
		DryRun: true,
		Output: &output,
	})
	if err != nil {
		t.Fatalf("rdma ping dry-run: %v", err)
	}
	got := output.String()
	for _, want := range []string{
		"dry-run rdma-ping node1:",
		"'ping' '-c' '3' '-W' '2' '-M' 'do' '-s' '8972' '-I' 'ens15np0' '10.247.1.12'",
		"'ping' '-c' '3' '-W' '2' '-M' 'do' '-s' '8972' '-I' 'ens15np0' '10.247.2.12'",
		"'ping' '-c' '3' '-W' '2' '-M' 'do' '-s' '8972' '-I' 'ens16np0' '10.247.1.12'",
		"'ping' '-c' '3' '-W' '2' '-M' 'do' '-s' '8972' '-I' 'ens16np0' '10.247.2.12'",
		"dry-run rdma-ping node2:",
		"'ping' '-c' '3' '-W' '2' '-M' 'do' '-s' '8972' '-I' 'ens15np0' '10.247.1.11'",
		"'ping' '-c' '3' '-W' '2' '-M' 'do' '-s' '8972' '-I' 'ens15np0' '10.247.2.11'",
		"'ping' '-c' '3' '-W' '2' '-M' 'do' '-s' '8972' '-I' 'ens16np0' '10.247.1.11'",
		"'ping' '-c' '3' '-W' '2' '-M' 'do' '-s' '8972' '-I' 'ens16np0' '10.247.2.11'",
		"RDMA ping result summary:",
		"STATUS",
		"SOURCE",
		"SOURCE_NIC",
		"DEST_NIC",
		"SOURCE_IP",
		"DEST_IP",
		"PASS    node1   node2  ens15np0",
		"PASS    node2   node1  ens16np0",
		"10.247.1.11",
		"10.247.1.12",
		"10.247.2.11",
		"8972",
		"ok",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in output:\n%s", want, got)
		}
	}
	if gotCount := strings.Count(got, "dry-run rdma-ping "); gotCount != 8 {
		t.Fatalf("expected 2x2 cross ping in both directions (8 commands), got %d:\n%s", gotCount, got)
	}
	if strings.Contains(got, "ib_write_bw") {
		t.Fatalf("did not expect bandwidth check commands:\n%s", got)
	}
}

func TestRDMAPingItemsBuildCrossMatrix(t *testing.T) {
	bundle := spec.Bundle{
		Defaults: spec.Defaults{
			RDMAInterfaces: []spec.RDMAInterfaceDefault{{Name: "ens11np0"}, {Name: "ens13np0"}},
		},
	}
	source := Target{
		Name: "node1",
		RDMA: []spec.RDMARecord{{Name: "ens11np0", IP: "10.247.1.11"}, {Name: "ens13np0", IP: "10.247.2.11"}},
	}
	destination := Target{
		Name: "node2",
		RDMA: []spec.RDMARecord{{IP: "10.247.1.12"}, {IP: "10.247.2.12"}},
	}
	items, err := rdmaPingItems(bundle, source, destination)
	if err != nil {
		t.Fatalf("build rdma ping items: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("expected 2x2 cross matrix, got %#v", items)
	}
	want := []rdmaPingItem{
		{SourceIndex: 0, DestinationIndex: 0, SourceName: "ens11np0", SourceIP: "10.247.1.11", DestinationName: "ens11np0", DestinationIP: "10.247.1.12"},
		{SourceIndex: 0, DestinationIndex: 1, SourceName: "ens11np0", SourceIP: "10.247.1.11", DestinationName: "ens13np0", DestinationIP: "10.247.2.12"},
		{SourceIndex: 1, DestinationIndex: 0, SourceName: "ens13np0", SourceIP: "10.247.2.11", DestinationName: "ens11np0", DestinationIP: "10.247.1.12"},
		{SourceIndex: 1, DestinationIndex: 1, SourceName: "ens13np0", SourceIP: "10.247.2.11", DestinationName: "ens13np0", DestinationIP: "10.247.2.12"},
	}
	for index := range want {
		if items[index] != want[index] {
			t.Fatalf("unexpected item %d: got %#v want %#v", index, items[index], want[index])
		}
	}
}

func TestPingFailureSummaryPrefersPacketLoss(t *testing.T) {
	output := `PING 10.247.2.12 (10.247.2.12) from 10.247.1.11 ens11np0: 8972(9000) bytes of data.

--- 10.247.2.12 ping statistics ---
3 packets transmitted, 0 received, 100% packet loss, time 2032ms
`
	got := pingFailureSummary(output, fmt.Errorf("local command on node1: exit status 1"))
	if got != "3 packets transmitted, 0 received, 100% packet loss, time 2032ms" {
		t.Fatalf("unexpected summary: %q", got)
	}
}

func TestPingFailureSummaryUsesCommandStderr(t *testing.T) {
	err := fmt.Errorf("local command on node1: exit status 2\nping: SO_BINDTODEVICE ens404: No such device")
	got := pingFailureSummary("", err)
	if got != "ping: SO_BINDTODEVICE ens404: No such device" {
		t.Fatalf("unexpected summary: %q", got)
	}
}

func TestIsTransientSSHErrorDetectsKexReset(t *testing.T) {
	err := fmt.Errorf("ssh 10.0.0.2: exit status 255\nkex_exchange_identification: read: Connection reset by peer")
	if !isTransientSSHError(err) {
		t.Fatalf("expected transient ssh error: %v", err)
	}
	if isTransientSSHError(fmt.Errorf("ssh 10.0.0.2: exit status 255\nPermission denied")) {
		t.Fatal("did not expect auth failure to be transient")
	}
}

func TestRunRDMAPingRequiresRDMAIPs(t *testing.T) {
	bundle := spec.Bundle{Defaults: spec.Defaults{RDMAInterfaces: []spec.RDMAInterfaceDefault{{Name: "ens1"}}}}
	bundle.ApplyDefaults()
	err := Run(Options{
		Bundle:      bundle,
		RunRDMAPing: true,
		Records: []spec.MachineRecord{
			{HostID: "node1", MgmtIP: "10.157.5.207"},
			{HostID: "node2", MgmtIP: "10.157.5.206"},
		},
		Hosts:  []string{"node1,node2"},
		DryRun: true,
		Output: io.Discard,
	})
	if err == nil {
		t.Fatal("expected missing RDMA IPs to fail")
	}
	if !strings.Contains(err.Error(), "rdma1_ip") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMarkLocalTargetsUsesLoopbackAddress(t *testing.T) {
	targets := markLocalTargets([]Target{{Name: "self", Address: "127.0.0.1"}})
	if len(targets) != 1 || !targets[0].Local {
		t.Fatalf("expected loopback target to be local: %#v", targets)
	}
}

func TestMarkLocalTargetsUsesHostname(t *testing.T) {
	names := localHostnameSet()
	var hostname string
	for name := range names {
		hostname = name
		break
	}
	if hostname == "" {
		t.Skip("local hostname is unavailable")
	}
	targets := markLocalTargets([]Target{{Input: hostname, Name: hostname, Address: hostname}})
	if len(targets) != 1 || !targets[0].Local {
		t.Fatalf("expected hostname target to be local: %#v", targets)
	}
}

func TestRunCommandUsesLocalExecution(t *testing.T) {
	out, err := runCommand(spec.CheckConfig{}, Target{Name: "self", Address: "127.0.0.1", Local: true}, "printf local-ok")
	if err != nil {
		t.Fatalf("run local command: %v", err)
	}
	if out != "local-ok" {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestWarnHostnameMismatchesIsNonBlocking(t *testing.T) {
	var output bytes.Buffer
	bundle := spec.Bundle{}
	bundle.ApplyDefaults()
	warnHostnameMismatches(Options{
		Bundle: bundle,
		Output: &output,
	}, []Target{
		{
			Name:             "node1",
			ExpectedHostname: "definitely-not-this-hostname",
			Address:          "127.0.0.1",
			Local:            true,
		},
	})
	got := output.String()
	if !strings.Contains(got, "WARN hostname mismatch") {
		t.Fatalf("expected hostname warning, got:\n%s", got)
	}
	if !strings.Contains(got, "continuing") {
		t.Fatalf("expected warning to say continuing, got:\n%s", got)
	}
}

func TestParseNICCounterOutput(t *testing.T) {
	got := parseNICCounterOutput(`
__envinit_iface=ens11np0
driver: mlx5_core
bus-info: 0000:11:00.0
ens11np0             UP             10:70:fd:00:00:11 <BROADCAST,MULTICAST,UP,LOWER_UP>
     rx_prio5_pause_duration: 10
     tx_prio5_pause_duration: 20
     rx_crc_errors_phy: 0
__envinit_iface=ens13np0
driver: mlx5_core
     roce_adp_retrans: 3
`)
	if got["ens11np0"]["rx_prio5_pause_duration"] != 10 {
		t.Fatalf("unexpected pause counter: %#v", got)
	}
	if got["ens11np0"]["tx_prio5_pause_duration"] != 20 {
		t.Fatalf("unexpected tx pause counter: %#v", got)
	}
	if got["ens13np0"]["roce_adp_retrans"] != 3 {
		t.Fatalf("unexpected retrans counter: %#v", got)
	}
}

func TestCompareNICCounterSnapshotsFailsOnlyAbnormalDeltas(t *testing.T) {
	var output bytes.Buffer
	target := Target{
		Name: "node1",
		RDMA: []spec.RDMARecord{{Name: "ens11np0"}},
	}
	opts := Options{
		Bundle: spec.Bundle{},
		Output: &output,
	}
	before := map[string]nicCounterSnapshot{
		"node1": {
			Interfaces: map[string]map[string]int64{
				"ens11np0": {
					"rx_prio5_pause_duration": 10,
					"np_cnp_sent":             7,
					"rx_discards_phy":         0,
					"rx_prio3_buf_discard":    0,
					"rx_err_lane_0_phy":       100,
				},
			},
		},
	}
	after := map[string]nicCounterSnapshot{
		"node1": {
			Interfaces: map[string]map[string]int64{
				"ens11np0": {
					"rx_prio5_pause_duration": 15,
					"np_cnp_sent":             8,
					"rx_discards_phy":         1,
					"rx_prio3_buf_discard":    4,
					"rx_err_lane_0_phy":       473,
				},
			},
		},
	}
	failures := compareNICCounterSnapshots(opts, []Target{target}, before, after)
	if len(failures) != 1 {
		t.Fatalf("expected one abnormal failure, got %#v", failures)
	}
	if !strings.Contains(failures[0], "detected 3 abnormal counter delta") {
		t.Fatalf("expected summarized failure, got %#v", failures)
	}
	got := output.String()
	for _, want := range []string{
		"NIC counter delta summary:",
		"STATUS",
		"COUNTER",
		"INFO",
		"rx_prio5_pause_duration",
		"+5",
		"np_cnp_sent",
		"rx_err_lane_0_phy",
		"+373",
		"\033[31mFAIL",
		"rx_discards_phy",
		"rx_prio3_buf_discard",
		"+1",
		"+4",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in output:\n%s", want, got)
		}
	}
	if strings.Contains(got, "rx_no_buffer: 0") {
		t.Fatalf("did not expect zero counters in output:\n%s", got)
	}
}

func TestNICCounterClassificationTreatsFECcorrectedLanesAsInformational(t *testing.T) {
	for _, name := range []string{"rx_err_lane_0_phy", "rx_err_lane_1_phy", "rx_err_lane_12_phy"} {
		if isAbnormalNICCounter(name) {
			t.Fatalf("%s is a FEC-corrected lane counter and must not be a hard failure", name)
		}
	}
	for _, name := range []string{"rx_crc_errors_phy", "rx_pcs_symbol_err_phy", "rs_fec_uncorrectable_blocks"} {
		if !isAbnormalNICCounter(name) {
			t.Fatalf("%s must remain an abnormal counter", name)
		}
	}
}

func TestParseRDMADeviceCounterOutput(t *testing.T) {
	got := parseRDMADeviceCounterOutput(`
__envinit_rdma=mlx5_1:1
counters.port_rcv_errors: 0
hw_counters.roce_adp_retrans: 3
__envinit_rdma=mlx5_2:1
counters.port_xmit_data: 10
`)
	if got["mlx5_1"]["1"]["hw_counters.roce_adp_retrans"] != 3 {
		t.Fatalf("unexpected rdma counter parse: %#v", got)
	}
	if got["mlx5_2"]["1"]["counters.port_xmit_data"] != 10 {
		t.Fatalf("unexpected rdma counter parse: %#v", got)
	}
}

func TestCompareRDMADeviceCounterSnapshotsSummarizesAbnormalDeltas(t *testing.T) {
	var output bytes.Buffer
	opts := Options{
		Bundle: spec.Bundle{Check: spec.CheckConfig{Bandwidth: spec.CheckBandwidthConfig{RDMAGroups: []spec.CheckRDMAGroup{{IBDevice: "mlx5_1"}}}}},
		Output: &output,
	}
	target := Target{Name: "node1"}
	before := map[string]rdmaDeviceCounterSnapshot{
		"node1": {
			Devices: map[string]map[string]map[string]int64{
				"mlx5_1": {
					"1": {
						"counters.port_xmit_data":      100,
						"hw_counters.roce_adp_retrans": 0,
						"hw_counters.port_xmit_wait":   0,
						"hw_counters.rp_cnp_handled":   0,
					},
				},
			},
		},
	}
	after := map[string]rdmaDeviceCounterSnapshot{
		"node1": {
			Devices: map[string]map[string]map[string]int64{
				"mlx5_1": {
					"1": {
						"counters.port_xmit_data":      150,
						"hw_counters.roce_adp_retrans": 2,
						"hw_counters.port_xmit_wait":   9,
						"hw_counters.rp_cnp_handled":   1,
					},
				},
			},
		},
	}
	failures := compareRDMADeviceCounterSnapshots(opts, []Target{target}, resolvedRDMAGroups{
		"node1": {{IBDevice: "mlx5_1"}},
	}, before, after)
	if len(failures) != 1 {
		t.Fatalf("expected summarized failure, got %#v", failures)
	}
	got := output.String()
	for _, want := range []string{
		"RDMA device counter delta summary:",
		"INFO",
		"counters.port_xmit_data",
		"+50",
		"\033[31mFAIL",
		"hw_counters.roce_adp_retrans",
		"+2",
		"hw_counters.port_xmit_wait",
		"+9",
		"hw_counters.rp_cnp_handled",
		"+1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in output:\n%s", want, got)
		}
	}
}

func TestParseBandwidthGBits(t *testing.T) {
	got, ok := ParseBandwidthGBits(`
#bytes     #iterations    BW peak[Gb/sec]    BW average[Gb/sec]
65536      1000           389.51             388.42
`)
	if !ok {
		t.Fatal("expected bandwidth to parse")
	}
	if got != 388.42 {
		t.Fatalf("unexpected bandwidth: %v", got)
	}
}

func TestParseBandwidthGBitsIgnoresMsgRateColumn(t *testing.T) {
	got, ok := ParseBandwidthGBits(`
#bytes     #iterations    BW peak[Gb/sec]    BW average[Gb/sec]   MsgRate[Mpps]
8388608    1000           398.77             397.12              0.005919
`)
	if !ok {
		t.Fatal("expected bandwidth to parse")
	}
	if got != 397.12 {
		t.Fatalf("unexpected bandwidth: %v", got)
	}
}

func TestParseBandwidthGBitsFromRDMACMOutput(t *testing.T) {
	got, ok := ParseBandwidthGBits(`
************************************
* Waiting for client to connect... *
************************************
allocated mmap buffer of size 131072 at 0x7f37a7356000
---------------------------------------------------------------------------------------
                    RDMA_Write BW Test
 Dual-port       : OFF		Device         : mlx5_1
 Number of qps   : 1		Transport type : IB
 Connection type : RC		Using SRQ      : OFF
 PCIe relax order: ON
 ibv_wr* API     : ON
 CQ Moderation   : 1
 Mtu             : 4096[B]
 Link type       : Ethernet
 GID index       : 3
 Max inline data : 0[B]
 rdma_cm QPs	 : ON
 Data ex. method : rdma_cm
---------------------------------------------------------------------------------------
 Waiting for client rdma_cm QP to connect
 Please run the same command with the IB/RoCE interface IP
---------------------------------------------------------------------------------------
 local address: LID 0000 QPN 0x012c PSN 0x240c4c
 GID: 00:00:00:00:00:00:00:00:00:00:255:255:10:247:01:12
 remote address: LID 0000 QPN 0x012e PSN 0x705c4a
 GID: 00:00:00:00:00:00:00:00:00:00:255:255:10:247:01:18
---------------------------------------------------------------------------------------
 #bytes     #iterations    BW peak[Gb/sec]    BW average[Gb/sec]   MsgRate[Mpps]
 65536      100              347.18             344.50 		   0.657082
---------------------------------------------------------------------------------------
`)
	if !ok {
		t.Fatal("expected bandwidth to parse")
	}
	if got != 344.50 {
		t.Fatalf("unexpected bandwidth: %v", got)
	}
}

func assertBandwidthBatches(t *testing.T, batches [][]checkStream, wantStreams int, wantMaxBatchSize int) {
	t.Helper()
	gotStreams := 0
	for batchIndex, batch := range batches {
		if len(batch) > wantMaxBatchSize {
			t.Fatalf("batch %d has %d streams, want at most %d", batchIndex, len(batch), wantMaxBatchSize)
		}
		clients := make(map[int]bool)
		servers := make(map[int]bool)
		for _, stream := range batch {
			gotStreams++
			if clients[stream.ClientRDMAIndex] {
				t.Fatalf("batch %d reuses client RDMA group %d: %#v", batchIndex, stream.ClientRDMAIndex, batch)
			}
			if servers[stream.ServerRDMAIndex] {
				t.Fatalf("batch %d reuses server RDMA group %d: %#v", batchIndex, stream.ServerRDMAIndex, batch)
			}
			clients[stream.ClientRDMAIndex] = true
			servers[stream.ServerRDMAIndex] = true
		}
	}
	if gotStreams != wantStreams {
		t.Fatalf("unexpected stream count: got %d want %d", gotStreams, wantStreams)
	}
}
