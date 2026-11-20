package runner

import (
	"envinit/internal/spec"
	"testing"
)

func TestExplicitBondDiscoveryRetainsSlotIdentity(t *testing.T) {
	a := &App{Machine: spec.MachineConfig{MgmtMode: "bond", MgmtBondName: "bond0", MgmtIfaces: []string{"eth9", ""}, MgmtMACs: []string{"02:00:00:00:00:09", ""}}}
	devices := []netDevice{{Name: "eth1", MAC: "02:00:00:00:00:01", VendorID: "x", DeviceID: "y", MaxSpeedMbps: 1000}, {Name: "eth9", MAC: "02:00:00:00:00:09", VendorID: "x", DeviceID: "y", MaxSpeedMbps: 1000}}
	if err := a.resolveExplicitManagementDevices(devices); err != nil {
		t.Fatal(err)
	}
	if a.Machine.MgmtIfaces[0] != "eth9" || a.Machine.MgmtIfaces[1] != "eth1" {
		t.Fatalf("slots reordered: %v", a.Machine.MgmtIfaces)
	}
}

func TestExplicitBondNeverDowngrades(t *testing.T) {
	a := &App{Machine: spec.MachineConfig{MgmtMode: "bond", MgmtBondName: "bond0", MgmtIfaces: []string{"", ""}}}
	if err := a.resolveExplicitManagementDevices([]netDevice{{Name: "eth0", MAC: "02:00:00:00:00:01", MaxSpeedMbps: 1000}}); err == nil {
		t.Fatal("one-member bond accepted")
	}
}
