package spec

import "testing"

func TestApplyTimeoutsPrefillsCompleteTable(t *testing.T) {
	b := Bundle{}
	b.ApplyDefaults()
	if b.ApplyTimeouts.DefaultSeconds != 360 {
		t.Fatalf("default_seconds = %d, want 360", b.ApplyTimeouts.DefaultSeconds)
	}
	want := map[string]int{
		"software": 360, "ofed": 0, "network": 360, "xre": 0, "xdr": 0,
		"firmware": 0, "container": 360, "mlxconfig": 360, "sysctl": 360,
		"kernel": 360, "post": 0, "udev": 360,
	}
	for stage, seconds := range want {
		if b.ApplyTimeouts.Stages[stage] != seconds {
			t.Fatalf("stage %q = %d, want %d", stage, b.ApplyTimeouts.Stages[stage], seconds)
		}
	}
}

func TestApplyTimeoutsValidateRejectsNegativeAndUnknown(t *testing.T) {
	neg := Bundle{ApplyTimeouts: ApplyTimeouts{DefaultSeconds: -1}}
	neg.ApplyDefaults()
	if err := neg.Validate(); err == nil {
		t.Fatal("negative default_seconds should fail validation")
	}

	unknown := Bundle{ApplyTimeouts: ApplyTimeouts{Stages: map[string]int{"bogus": 10}}}
	unknown.ApplyDefaults()
	if err := unknown.Validate(); err == nil {
		t.Fatal("unknown stage should fail validation")
	}

	negStage := Bundle{ApplyTimeouts: ApplyTimeouts{Stages: map[string]int{"ofed": -5}}}
	negStage.ApplyDefaults()
	if err := negStage.Validate(); err == nil {
		t.Fatal("negative stage timeout should fail validation")
	}
}
