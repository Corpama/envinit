package runner

import (
	"reflect"
	"strings"
	"testing"
)

func TestProbeMappingRequiresGlobalUniqueness(t *testing.T) {
	for _, tt := range []struct {
		name   string
		edges  [][]bool
		want   []int
		unique bool
	}{
		{"crossed", [][]bool{{false, true}, {true, false}}, []int{1, 0}, true},
		{"shared gateway", [][]bool{{true, true}, {true, true}}, nil, false},
		{"all down", [][]bool{{false, false}, {false, false}}, nil, false},
		{"avoid greedy", [][]bool{{true, true}, {true, false}}, []int{1, 0}, true},
		{"reuse prohibited", [][]bool{{true, false}, {true, false}}, nil, false},
		{"extra candidate", [][]bool{{true, true}}, nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, unique := uniqueProbeMapping(tt.edges)
			if unique != tt.unique || (unique && !reflect.DeepEqual(got, tt.want)) {
				t.Fatalf("got %v,%t want %v,%t", got, unique, tt.want, tt.unique)
			}
		})
	}
}

func TestHardwareOrderProbeCandidatesUsesPCIOrder(t *testing.T) {
	candidates := []probeLink{
		{Name: "ens7np0", MAC: "00:00:00:00:00:04"},
		{Name: "ens1np0", MAC: "00:00:00:00:00:02"},
		{Name: "ens5np0", MAC: "00:00:00:00:00:03"},
		{Name: "ens3np0", MAC: "00:00:00:00:00:01"},
	}
	physical := map[string]netDevice{
		"ens7np0": {Name: "ens7np0", PCI: "0000:5d:00.0"},
		"ens1np0": {Name: "ens1np0", PCI: "0000:1b:00.0"},
		"ens5np0": {Name: "ens5np0", PCI: "0000:3b:00.0"},
		"ens3np0": {Name: "ens3np0", PCI: "0000:2d:00.0"},
	}

	ordered := hardwareOrderProbeCandidates(candidates, physical)
	got := make([]string, 0, len(ordered))
	for _, candidate := range ordered {
		got = append(got, candidate.Name)
	}
	want := []string{"ens1np0", "ens3np0", "ens5np0", "ens7np0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got hardware order %v, want %v", got, want)
	}
}

func TestHardwareOrderProbeFallbackPreservesFourSameSubnetCandidates(t *testing.T) {
	bindings := []interfaceBinding{
		{Kind: "rdma", Name: "rdma1"},
		{Kind: "rdma", Name: "rdma2"},
		{Kind: "rdma", Name: "rdma3"},
		{Kind: "rdma", Name: "rdma4"},
	}
	candidates := []probeLink{
		{Name: "ens1np0", MAC: "00:00:00:00:00:01"},
		{Name: "ens3np0", MAC: "00:00:00:00:00:03"},
		{Name: "ens5np0", MAC: "00:00:00:00:00:05"},
		{Name: "ens7np0", MAC: "00:00:00:00:00:07"},
	}
	applyHardwareOrderProbeFallback(bindings, []int{0, 1, 2, 3}, candidates)

	for idx, want := range candidates {
		if bindings[idx].CurrentName != want.Name || bindings[idx].MAC != want.MAC {
			t.Fatalf("slot %d got %s/%s, want %s/%s", idx, bindings[idx].CurrentName, bindings[idx].MAC, want.Name, want.MAC)
		}
		if !bindings[idx].NeedsReview || bindings[idx].Confidence != "ambiguous" || bindings[idx].Reason != "hardware order fallback; connectivity unverified" {
			t.Fatalf("slot %d was not marked for unverified review: %#v", idx, bindings[idx])
		}
	}
}

func TestFourSameGatewayProbeMatrixRequiresManualConfirmation(t *testing.T) {
	bindings := []interfaceBinding{
		{Kind: "rdma", Name: "rdma1", Address: "33.3.133.151/25", Gateway: "33.3.133.129"},
		{Kind: "rdma", Name: "rdma2", Address: "33.3.133.152/25", Gateway: "33.3.133.129"},
		{Kind: "rdma", Name: "rdma3", Address: "33.3.133.153/25", Gateway: "33.3.133.129"},
		{Kind: "rdma", Name: "rdma4", Address: "33.3.133.154/25", Gateway: "33.3.133.129"},
	}
	candidates := []probeLink{
		{Name: "ens1np0", MAC: "00:00:00:00:00:01"},
		{Name: "ens3np0", MAC: "00:00:00:00:00:03"},
		{Name: "ens5np0", MAC: "00:00:00:00:00:05"},
		{Name: "ens7np0", MAC: "00:00:00:00:00:07"},
	}
	// Every planned IP can reach the same gateway through every port. This is
	// valid connectivity evidence, but it has no unique IP-to-port solution.
	edges := make([][]bool, len(bindings))
	for row := range edges {
		edges[row] = []bool{true, true, true, true}
	}
	if _, unique := uniqueProbeMapping(edges); unique {
		t.Fatal("same-gateway matrix must not produce a unique mapping")
	}

	applyHardwareOrderProbeFallback(bindings, []int{0, 1, 2, 3}, candidates)
	if err := validateReviewBindings(bindings); err != nil {
		t.Fatalf("hardware-order candidates should be complete and physically unique: %v", err)
	}
	for _, binding := range bindings {
		if !binding.NeedsReview || binding.Confidence == "strong" || binding.Reason != "hardware order fallback; connectivity unverified" {
			t.Fatalf("same-gateway mapping was presented as verified: %#v", binding)
		}
	}
}

func TestProbeFallbackWarningClassifiesOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		edges       [][]bool
		all         int
		tested      int
		incomplete  bool
		wantMessage string
	}{
		{
			name:        "all unreachable",
			edges:       [][]bool{{false, false}, {false, false}},
			all:         2,
			tested:      2,
			wantMessage: "gateway unreachable from all probe candidates",
		},
		{
			name:        "no candidates tested",
			edges:       [][]bool{{}},
			all:         2,
			wantMessage: "could not test any candidate NIC",
		},
		{
			name:        "skipped candidates",
			edges:       [][]bool{{true}, {false}},
			all:         2,
			tested:      1,
			wantMessage: "evidence incomplete",
		},
		{
			name:        "link not ready",
			edges:       [][]bool{{false, true}, {false, true}},
			all:         2,
			tested:      2,
			incomplete:  true,
			wantMessage: "evidence incomplete because one or more candidates did not complete both ARP and ICMP",
		},
		{
			name:        "partial protocol evidence",
			edges:       [][]bool{{false, true}, {false, true}},
			all:         2,
			tested:      2,
			incomplete:  true,
			wantMessage: "evidence incomplete because one or more candidates did not complete both ARP and ICMP",
		},
		{
			name:        "non unique",
			edges:       [][]bool{{true, true}, {true, true}},
			all:         2,
			tested:      2,
			wantMessage: "did not produce a unique NIC mapping",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := probeFallbackWarning(tt.edges, tt.all, tt.tested, tt.incomplete)
			if !strings.Contains(got, tt.wantMessage) {
				t.Fatalf("warning %q does not contain %q", got, tt.wantMessage)
			}
		})
	}
}
