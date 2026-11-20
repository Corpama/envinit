package runner

import (
	"testing"
)

func TestResolveExistingProbeBindingsUsesPhysicalAddressOwner(t *testing.T) {
	bindings := []interfaceBinding{
		{Kind: "rdma", Name: "ens3np0", Address: "33.3.133.151/25", NeedsReview: true},
		{Kind: "rdma", Name: "ens5np0", Address: "33.3.133.152/25", NeedsReview: true},
	}
	links := []probeLink{
		{
			Name:      "ens1np0",
			MAC:       "aa:bb:cc:dd:ee:01",
			Addresses: []probeAddress{{Address: "33.3.133.151", Scope: "global"}},
		},
	}
	physical := map[string]netDevice{
		"ens1np0": {Name: "ens1np0", MAC: "aa:bb:cc:dd:ee:01"},
	}
	used := map[string]bool{}

	pending := resolveExistingProbeBindings(bindings, []int{0, 1}, links, physical, used)
	if len(pending) != 1 || pending[0] != 1 {
		t.Fatalf("unexpected pending probe slots: %#v", pending)
	}
	if bindings[0].CurrentName != "ens1np0" || bindings[0].MAC != "aa:bb:cc:dd:ee:01" {
		t.Fatalf("existing address owner was not reused: %#v", bindings[0])
	}
	if bindings[0].Confidence != "ambiguous" || !bindings[0].NeedsReview {
		t.Fatalf("existing physical address owner should remain reviewable: %#v", bindings[0])
	}
	if bindings[0].ProbeWarning == "" {
		t.Fatalf("expected warning for skipped gateway probe: %#v", bindings[0])
	}
	if len(bindings[0].ProbeDetails) != 1 {
		t.Fatalf("expected existing-address probe detail, got %#v", bindings[0].ProbeDetails)
	}
}

func TestResolveExistingProbeBindingsLeavesNonPhysicalOwnerForReview(t *testing.T) {
	bindings := []interfaceBinding{{Kind: "rdma", Name: "ens3np0", Address: "33.3.133.151/25"}}
	links := []probeLink{{
		Name:      "bond0",
		MAC:       "aa:bb:cc:dd:ee:01",
		Master:    "",
		Addresses: []probeAddress{{Address: "33.3.133.151"}},
	}}
	physical := map[string]netDevice{}

	pending := resolveExistingProbeBindings(bindings, []int{0}, links, physical, map[string]bool{})
	if len(pending) != 0 {
		t.Fatalf("must not actively probe an already configured address: %#v", pending)
	}
	if !bindings[0].NeedsReview || bindings[0].MAC != "" {
		t.Fatalf("non-physical address owner should require review: %#v", bindings[0])
	}
}
