package nicdetect

import "testing"

func TestRecommendSeparatesOneHundredGManagementFromFourHundredGRDMA(t *testing.T) {
	facts := []Facts{{Name: "eth0", VendorID: "0x15b3", DeviceID: "0x1017", MaxSpeedMbps: 100000, HasRDMA: true}}
	for idx := 1; idx <= 4; idx++ {
		facts = append(facts, Facts{Name: "eth" + string(rune('0'+idx)), PCI: "0000:4" + string(rune('0'+idx)) + ":00.0", VendorID: "0x15b3", DeviceID: "0x1023", MaxSpeedMbps: 400000, HasRDMA: true})
	}
	decision := Recommend(Plan{ManagementCount: 1, RDMACount: 4}, facts)
	if decision.Confidence != ConfidenceStrong {
		t.Fatalf("unexpected confidence: %#v", decision)
	}
	if len(decision.Management) != 1 || decision.Management[0].NIC.Name != "eth0" {
		t.Fatalf("unexpected management recommendation: %#v", decision.Management)
	}
	if len(decision.RDMA) != 4 {
		t.Fatalf("unexpected RDMA recommendation: %#v", decision.RDMA)
	}
}

func TestRecommendExpandsCoherentRDMAGroup(t *testing.T) {
	facts := []Facts{{Name: "mgmt0", VendorID: "0x8086", DeviceID: "0x100e", MaxSpeedMbps: 25000}}
	for idx := 0; idx < 8; idx++ {
		facts = append(facts, Facts{Name: "rdma" + string(rune('0'+idx)), VendorID: "0x15b3", DeviceID: "0x1023", MaxSpeedMbps: 400000, HasRDMA: true})
	}
	decision := Recommend(Plan{ManagementCount: 1, RDMACount: 4, AllowRDMAExpansion: true}, facts)
	if len(decision.RDMA) != 8 {
		t.Fatalf("expected expansion to 8 RDMA NICs, got %#v", decision)
	}
	if len(decision.Management) != 1 || decision.Management[0].NIC.Name != "mgmt0" {
		t.Fatalf("unexpected management recommendation: %#v", decision.Management)
	}
}

func TestRecommendDoesNotGuessWithinFiveIdenticalNICs(t *testing.T) {
	var facts []Facts
	for idx := 0; idx < 5; idx++ {
		facts = append(facts, Facts{Name: "eth" + string(rune('0'+idx)), VendorID: "0x15b3", DeviceID: "0x1017", MaxSpeedMbps: 100000, HasRDMA: true})
	}
	decision := Recommend(Plan{ManagementCount: 1, RDMACount: 4}, facts)
	if decision.Confidence != ConfidenceAmbiguous {
		t.Fatalf("expected ambiguity, got %#v", decision)
	}
	if len(decision.RDMA) != 0 || len(decision.Management) != 0 {
		t.Fatalf("did not expect guessed bindings: %#v", decision)
	}
}

func TestRecommendKeepsLinkDownNICInMaxSpeedGroup(t *testing.T) {
	facts := []Facts{{Name: "mgmt0", VendorID: "0x8086", DeviceID: "0x100e", MaxSpeedMbps: 25000}}
	for idx := 0; idx < 4; idx++ {
		facts = append(facts, Facts{Name: "rdma" + string(rune('0'+idx)), VendorID: "0x15b3", DeviceID: "0x1023", CurrentSpeedMbps: 0, MaxSpeedMbps: 400000, HasRDMA: true, LinkKnown: true, LinkUp: idx != 0})
	}
	decision := Recommend(Plan{ManagementCount: 1, RDMACount: 4}, facts)
	if len(decision.RDMA) != 4 {
		t.Fatalf("expected all four max-speed peers, got %#v", decision)
	}
}

func TestRecommendUsesMTUToSeparateSameModelManagementAndRDMA(t *testing.T) {
	facts := []Facts{{Name: "eth0", VendorID: "0x15b3", DeviceID: "0x1023", MaxSpeedMbps: 400000, MTU: 1500, HasRDMA: true}}
	for idx := 1; idx <= 4; idx++ {
		facts = append(facts, Facts{Name: "eth" + string(rune('0'+idx)), VendorID: "0x15b3", DeviceID: "0x1023", MaxSpeedMbps: 400000, MTU: 4200, HasRDMA: true})
	}
	decision := Recommend(Plan{ManagementCount: 1, RDMACount: 4}, facts)
	if decision.Confidence != ConfidenceStrong || len(decision.RDMA) != 4 || len(decision.Management) != 1 {
		t.Fatalf("expected MTU evidence to separate one management and four RDMA NICs: %#v", decision)
	}
	if decision.Management[0].NIC.Name != "eth0" {
		t.Fatalf("management NIC = %s, want eth0", decision.Management[0].NIC.Name)
	}
}

func TestRecommendUsesDefaultRouteToKeepRDMACapableManagementNIC(t *testing.T) {
	facts := []Facts{{Name: "eth0", VendorID: "0x15b3", DeviceID: "0x1023", MaxSpeedMbps: 400000, MTU: 4200, HasRDMA: true, DefaultRoute: true}}
	for idx := 1; idx <= 4; idx++ {
		facts = append(facts, Facts{Name: "eth" + string(rune('0'+idx)), VendorID: "0x15b3", DeviceID: "0x1023", MaxSpeedMbps: 400000, MTU: 4200, HasRDMA: true})
	}
	decision := Recommend(Plan{ManagementCount: 1, RDMACount: 4}, facts)
	if decision.Confidence != ConfidenceStrong || len(decision.RDMA) != 4 || len(decision.Management) != 1 || decision.Management[0].NIC.Name != "eth0" {
		t.Fatalf("expected default-route evidence to retain eth0 as management: %#v", decision)
	}
}

func TestRecommendUsesPlannedAddressNetworkAsSupportingEvidence(t *testing.T) {
	facts := []Facts{{Name: "eth0", VendorID: "0x15b3", DeviceID: "0x1023", MaxSpeedMbps: 400000, MTU: 4200, HasRDMA: true, Addresses: []Address{{IP: "192.168.32.11", Prefix: 24}}}}
	for idx := 1; idx <= 4; idx++ {
		facts = append(facts, Facts{Name: "eth" + string(rune('0'+idx)), VendorID: "0x15b3", DeviceID: "0x1023", MaxSpeedMbps: 400000, MTU: 4200, HasRDMA: true, Addresses: []Address{{IP: "25.16.2." + string(rune('1'+idx)), Prefix: 28}}})
	}
	decision := Recommend(Plan{
		ManagementCount: 1,
		RDMACount:       4,
		ManagementHints: []SlotHint{{Index: 0, IP: "192.168.32.100", Prefix: 24}},
	}, facts)
	if decision.Confidence != ConfidenceStrong || len(decision.RDMA) != 4 || len(decision.Management) != 1 || decision.Management[0].NIC.Name != "eth0" {
		t.Fatalf("expected planned management subnet to distinguish eth0: %#v", decision)
	}
}
