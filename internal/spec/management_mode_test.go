package spec

import "testing"

func TestExplicitManagementMode(t *testing.T) {
	b := Bundle{}
	b.ApplyDefaults()
	b.Defaults.RDMAMode = "off"
	for _, mode := range []string{"single", "bond"} {
		t.Run(mode, func(t *testing.T) {
			r := MachineRecord{HostID: "node1", MgmtMode: mode, MgmtIP: "10.0.0.2", MgmtPrefix: "24", MgmtGateway: "10.0.0.1"}
			m, e := ResolveMachine(b, r, nil)
			if e != nil {
				t.Fatal(e)
			}
			want := 1
			if mode == "bond" {
				want = 2
			}
			if len(m.MgmtIfaces) != want {
				t.Fatalf("members=%v", m.MgmtIfaces)
			}
			if mode == "single" && m.MgmtBondName != "" {
				t.Fatal("single retained inherited bond name")
			}
			r.MgmtIface1 = "eth0"
			m, e = ResolveMachine(b, r, nil)
			if e != nil {
				t.Fatal(e)
			}
			if len(m.MgmtIfaces) != want {
				t.Fatal("partial member downgraded mode")
			}
		})
	}
	for _, r := range []MachineRecord{{MgmtMode: "bad"}, {MgmtMode: "single", MgmtBondName: "bond0"}, {MgmtMode: "single", MgmtIface2: "eth1"}} {
		if _, e := ResolveMachine(b, r, nil); e == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
}
