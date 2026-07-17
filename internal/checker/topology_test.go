package checker

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"envinit/internal/spec"
)

const sampleXPUTopology = `
        XPU0 XPU1 XPU2 XPU3 XPU4 XPU5 XPU6 XPU7 NIC0 NIC1 NIC2 NIC3 NIC4 CPU Affinity NUMA Affinity
XPU0    X    XL   XL   XL   SYS  SYS  SYS  SYS  NODE PIX  NODE SYS  SYS  0-51,104-155 0
XPU1    XL   X    XL   XL   SYS  XL   SYS  SYS  NODE PIX  NODE SYS  SYS  0-51,104-155 0
XPU2    XL   XL   X    XL   SYS  SYS  XL   SYS  NODE NODE PIX  SYS  SYS  0-51,104-155 0
XPU3    XL   XL   XL   X    SYS  SYS  SYS  XL   NODE NODE PIX  SYS  SYS  0-51,104-155 0
XPU4    XL   SYS  SYS  SYS  X    XL   XL   XL   SYS  SYS  SYS  PIX  NODE 52-103,156-207 1
XPU5    SYS  XL   SYS  SYS  XL   X    XL   XL   SYS  SYS  SYS  PIX  NODE 52-103,156-207 1
XPU6    SYS  SYS  XL   SYS  XL   XL   X    XL   SYS  SYS  SYS  NODE PIX  52-103,156-207 1
XPU7    SYS  SYS  SYS  XL   XL   XL   XL   X    SYS  SYS  SYS  NODE PIX  52-103,156-207 1

Legend:
  X = Self

NIC Legend:
  NIC0: mlx5_0
  NIC1: mlx5_1
  NIC2: mlx5_2
  NIC3: mlx5_3
  NIC4: mlx5_4
`

func TestParseAndAssignXPUOffsetsByTopology(t *testing.T) {
	topology, err := parseXPUTopology(sampleXPUTopology)
	if err != nil {
		t.Fatalf("parse topology: %v", err)
	}
	groups, assignments, err := assignXPUOffsetsByTopology([]spec.CheckRDMAGroup{
		{IBDevice: "mlx5_1", XPUOffsets: []string{"legacy-wrong-value"}},
		{IBDevice: "mlx5_2"},
		{IBDevice: "mlx5_3"},
		{IBDevice: "mlx5_4"},
	}, topology)
	if err != nil {
		t.Fatalf("assign topology: %v", err)
	}
	wantAssignments := map[int][]int{0: {0, 1}, 1: {2, 3}, 2: {4, 5}, 3: {6, 7}}
	if !reflect.DeepEqual(assignments, wantAssignments) {
		t.Fatalf("unexpected assignments: got %#v want %#v", assignments, wantAssignments)
	}
	wantOffsets := [][]string{
		{"0x0000000090001000", "0x1000000090001000"},
		{"0x2000000090001000", "0x3000000090001000"},
		{"0x4000000090001000", "0x5000000090001000"},
		{"0x6000000090001000", "0x7000000090001000"},
	}
	for idx := range groups {
		if !reflect.DeepEqual(groups[idx].XPUOffsets, wantOffsets[idx]) {
			t.Fatalf("group %d offsets: got %#v want %#v", idx, groups[idx].XPUOffsets, wantOffsets[idx])
		}
	}
}

func TestAssignXPUOffsetsUsesOnlyParticipatingNICs(t *testing.T) {
	topology, err := parseXPUTopology(sampleXPUTopology)
	if err != nil {
		t.Fatalf("parse topology: %v", err)
	}
	groups, assignments, err := assignXPUOffsetsByTopology([]spec.CheckRDMAGroup{{IBDevice: "mlx5_0"}}, topology)
	if err != nil {
		t.Fatalf("assign topology: %v", err)
	}
	if len(assignments[0]) != 8 || len(groups[0].XPUOffsets) != 8 {
		t.Fatalf("expected the sole participating NIC to own all XPUs: groups=%#v assignments=%#v", groups, assignments)
	}
}

func TestAssignXPUOffsetsRejectsNICMissingFromLegend(t *testing.T) {
	topology, err := parseXPUTopology(sampleXPUTopology)
	if err != nil {
		t.Fatalf("parse topology: %v", err)
	}
	_, _, err = assignXPUOffsetsByTopology([]spec.CheckRDMAGroup{{IBDevice: "mlx5_99"}}, topology)
	if err == nil {
		t.Fatal("expected missing NIC legend error")
	}
}

func TestAssignXPUOffsetsSupportsEightNICs(t *testing.T) {
	var output strings.Builder
	for index := 0; index < 8; index++ {
		fmt.Fprintf(&output, " XPU%d", index)
	}
	for index := 0; index < 8; index++ {
		fmt.Fprintf(&output, " NIC%d", index)
	}
	output.WriteByte('\n')
	for xpu := 0; xpu < 8; xpu++ {
		fmt.Fprintf(&output, "XPU%d", xpu)
		for otherXPU := 0; otherXPU < 8; otherXPU++ {
			if otherXPU == xpu {
				output.WriteString(" X")
			} else {
				output.WriteString(" SYS")
			}
		}
		for nic := 0; nic < 8; nic++ {
			if nic == xpu {
				output.WriteString(" PIX")
			} else {
				output.WriteString(" SYS")
			}
		}
		output.WriteByte('\n')
	}
	output.WriteString("NIC Legend:\n")
	groups := make([]spec.CheckRDMAGroup, 8)
	for index := 0; index < 8; index++ {
		fmt.Fprintf(&output, "NIC%d: mlx5_%d\n", index, index+1)
		groups[index].IBDevice = fmt.Sprintf("mlx5_%d", index+1)
	}

	topology, err := parseXPUTopology(output.String())
	if err != nil {
		t.Fatalf("parse eight-NIC topology: %v", err)
	}
	resolved, assignments, err := assignXPUOffsetsByTopology(groups, topology)
	if err != nil {
		t.Fatalf("assign eight-NIC topology: %v", err)
	}
	for index := 0; index < 8; index++ {
		if !reflect.DeepEqual(assignments[index], []int{index}) {
			t.Fatalf("group %d assignment: %#v", index, assignments[index])
		}
		if !reflect.DeepEqual(resolved[index].XPUOffsets, []string{xdrMmapOffsetForXPU(index)}) {
			t.Fatalf("group %d offsets: %#v", index, resolved[index].XPUOffsets)
		}
	}
}

func TestBandwidthStreamsUsePerTargetTopologyOffsets(t *testing.T) {
	cfg := spec.CheckBandwidthConfig{MmapDevice: "/dev/xdrdrv"}
	serverGroups := []spec.CheckRDMAGroup{{IBDevice: "mlx5_2", XPUOffsets: []string{"server-offset"}}}
	clientGroups := []spec.CheckRDMAGroup{{IBDevice: "mlx5_7", XPUOffsets: []string{"client-offset"}}}
	streams := bandwidthStreamsForGroups(cfg, serverGroups, clientGroups)
	if len(streams) != 1 {
		t.Fatalf("unexpected streams: %#v", streams)
	}
	if streams[0].ServerOffset != "server-offset" || streams[0].ClientOffset != "client-offset" {
		t.Fatalf("stream did not preserve per-target offsets: %#v", streams[0])
	}
	if streams[0].ServerGroup.IBDevice != "mlx5_2" || streams[0].ClientGroup.IBDevice != "mlx5_7" {
		t.Fatalf("stream did not preserve per-target devices: %#v", streams[0])
	}
}

func TestBandwidthResultMarksDegradedTopology(t *testing.T) {
	var output bytes.Buffer
	printBandwidthResultTable(&output, []Result{{
		Client:         Target{Name: "node-a"},
		Server:         Target{Name: "node-b"},
		ClientGroup:    spec.CheckRDMAGroup{IBDevice: "mlx5_1"},
		ServerGroup:    spec.CheckRDMAGroup{IBDevice: "mlx5_2"},
		ClientXP:       "0x0000000090001000",
		ServerXP:       "0x1000000090001000",
		ClientTopology: "PIX",
		ServerTopology: "NODE",
		Degraded:       true,
		Port:           18515,
		GBits:          82.5,
		Passed:         true,
	}})
	got := output.String()
	for _, want := range []string{
		"STATUS",
		"CLIENT_TOPO",
		"SERVER_TOPO",
		"WARN",
		"PIX",
		"NODE(DEGRADED)",
		"1 completed stream(s) used non-PIX XPU/NIC mappings",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in degraded result output:\n%s", want, got)
		}
	}
}

func TestXDRMmapOffsetForXPU(t *testing.T) {
	want := []string{
		"0x0000000090001000",
		"0x1000000090001000",
		"0x7000000090001000",
	}
	for index, xpu := range []int{0, 1, 7} {
		if got := xdrMmapOffsetForXPU(xpu); got != want[index] {
			t.Fatalf("XPU%d offset: got %s want %s", xpu, got, want[index])
		}
	}
}
