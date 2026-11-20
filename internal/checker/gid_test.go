package checker

import "testing"

func TestParseGIDCandidatesSupportsSysfsAndShowGIDsFormats(t *testing.T) {
	output := "GID|3|192.168.10.11|ens5f0|RoCE v2\nmlx5_0 1 4 gid 192.168.10.12 v1 ens5f0\n"
	got := parseGIDCandidates(output, "mlx5_0", 1)
	if len(got) != 2 {
		t.Fatalf("candidate count = %d, want 2: %#v", len(got), got)
	}
	if got[0].Index != 3 || got[0].Address != "192.168.10.11" || got[0].Netdev != "ens5f0" {
		t.Fatalf("unexpected sysfs candidate: %#v", got[0])
	}
	if got[1].Index != 4 || got[1].Address != "192.168.10.12" || got[1].Netdev != "ens5f0" {
		t.Fatalf("unexpected show_gids candidate: %#v", got[1])
	}
}

func TestPreferredGIDMatchesInterfaceAndPrefersRoCEv2(t *testing.T) {
	candidates := []gidCandidate{
		{Index: 2, Address: "192.168.10.11", Netdev: "ens5f0", Version: "v1"},
		{Index: 5, Address: "192.168.10.11", Netdev: "ens5f0", Version: "v2"},
		{Index: 7, Address: "192.168.10.12", Netdev: "ens6f0", Version: "v2"},
	}
	if got, ok := preferredGID(candidates, "ens5f0", "192.168.10.11"); !ok || got != 5 {
		t.Fatalf("preferred GID = %d, %v; want 5, true", got, ok)
	}
	if _, ok := preferredGID(candidates, "ens404", "192.168.10.11"); ok {
		t.Fatal("unexpected GID match for a different interface")
	}
}
