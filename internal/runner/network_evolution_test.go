package runner

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"envinit/internal/spec"
)

func evolutionApp(t *testing.T) *App {
	t.Helper()
	return &App{Root: t.TempDir(), Output: io.Discard, Bundle: spec.Bundle{Defaults: spec.Defaults{RDMAMode: spec.RDMAModeOff}}}
}

func TestNetworkTransactionPreservesSuccessfulManifestUntilVerification(t *testing.T) {
	a := evolutionApp(t)
	path := filepath.Join(netplanDir, "00-kunlun-bond.yaml")
	old := networkManifest{Version: 2, Files: map[string]string{path: contentDigest([]byte("old"))}, Roles: map[string]string{path: "mgmt"}}
	if err := a.writeNetworkState(networkManifestFile, old); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(a.targetPath(path), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.beginNetworkTransaction(); err != nil {
		t.Fatal(err)
	}
	if err := a.writeManagedFile(path, "new", 0o600); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(a.targetPath(networkManifestFile))
	var got networkManifest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Files[path] != old.Files[path] {
		t.Fatal("successful manifest changed before activation")
	}
	// Simulate a fresh process after the write but before backend activation.
	b := *a
	b.networkTransaction = nil
	if err := b.beginNetworkTransaction(); err != nil {
		t.Fatalf("resume own partial write: %v", err)
	}
	if err := atomicWriteFile(a.targetPath(path), []byte("external"), 0o600); err != nil {
		t.Fatal(err)
	}
	b.networkTransaction = nil
	if err := b.beginNetworkTransaction(); err == nil || !strings.Contains(err.Error(), "external edits") {
		t.Fatalf("expected same-path external edit protection, got %v", err)
	}
}

func TestNetworkTransactionRejectsChangedTargetButAllowsActivationPolicy(t *testing.T) {
	a := evolutionApp(t)
	disabled := false
	a.Bundle.Defaults.ApplyNetworkImmediately = &disabled
	if err := a.beginNetworkTransaction(); err != nil {
		t.Fatal(err)
	}
	a.networkTransaction.Phase = "awaiting-runtime"
	if err := a.saveNetworkTransaction(); err != nil {
		t.Fatal(err)
	}
	enabled := true
	a.Bundle.Defaults.ApplyNetworkImmediately = &enabled
	if err := a.beginNetworkTransaction(); err != nil {
		t.Fatalf("activation policy should not invalidate target: %v", err)
	}
	if a.networkTransaction.Phase != "prepared" {
		t.Fatalf("phase=%s", a.networkTransaction.Phase)
	}
	a.Machine.MgmtIP = "10.2.0.1"
	if err := a.beginNetworkTransaction(); err == nil {
		t.Fatal("overwrote unfinished transaction with another target")
	}
}

func TestNetworkCommitRequiresLiveBindingAndKeepsRecoveryRecord(t *testing.T) {
	a := evolutionApp(t)
	if err := a.beginNetworkTransaction(); err != nil {
		t.Fatal(err)
	}
	a.networkTransaction.Next = &networkManifest{Version: 2, Files: map[string]string{}, Roles: map[string]string{}}
	a.networkTransaction.Topology = networkTopology{Links: []ownedNetworkLink{{Name: "rdma1", Role: "rdma", MAC: "aa:bb:cc:dd:ee:01"}}}
	if err := a.finishNetworkTransaction(); err == nil {
		t.Fatal("committed without live target")
	}
	if !a.pathExists(networkTransactionFile) {
		t.Fatal("lost recovery record")
	}
	if a.pathExists(networkManifestFile) {
		t.Fatal("committed unverified state")
	}
}

func TestRDMARebindReleasesOwnedOccupantAndResolvesTemporaryNames(t *testing.T) {
	a := evolutionApp(t)
	mustWriteNetDevice(t, a.Root, "rdma1", "aa:bb:cc:dd:ee:01", "0000:01:00.0", "mlx5_core", 0, "p0")
	mustWriteNetDevice(t, a.Root, "ei-tmp0", "aa:bb:cc:dd:ee:02", "0000:02:00.0", "mlx5_core", 0, "p0")
	a.networkTransaction = &networkTransaction{Old: networkManifest{Topology: networkTopology{Links: []ownedNetworkLink{{Name: "rdma1", Role: "rdma", MAC: "aa:bb:cc:dd:ee:01"}}}}}
	bindings := []interfaceBinding{{Name: "rdma1", CurrentName: "ens8f0", MAC: "aa:bb:cc:dd:ee:02", Kind: "rdma"}}
	got, err := a.withRetiredInterfaceNames(bindings)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].CurrentName != "ei-tmp0" || got[1].CurrentName != "rdma1" || !strings.HasPrefix(got[1].Name, "ei-old") {
		t.Fatalf("unexpected rename plan: %+v", got)
	}
	a.networkTransaction.Old.Topology.Links = nil
	if _, err := a.withRetiredInterfaceNames(bindings); err == nil {
		t.Fatal("released unowned target name")
	}
}

func TestOwnedRuleSelectorsRejectForeignExtensions(t *testing.T) {
	route := ownedRouteObject{Device: "rdma1", IP: "40.1.1.11", Table: 101, Priority: 32761}
	for _, line := range []string{
		"32761: from 40.1.1.11 fwmark 0x1 lookup 101",
		"32761: from 40.1.1.11 to 10.0.0.0/8 lookup 101",
		"32761: not from 40.1.1.11 lookup 101",
		"32761: from 40.1.1.11 lookup 101 suppress_prefixlength 0",
	} {
		if containsOwnedRule(line, route, false) {
			t.Fatalf("matched external rule %q", line)
		}
	}
	if !containsOwnedRule("32761: from 40.1.1.11/32 lookup 101 proto static", route, false) {
		t.Fatal("did not match exact rule with kernel metadata")
	}
}

func TestGatewayChangePreservesSharedRulesAndConnectedRoute(t *testing.T) {
	a := evolutionApp(t)
	bin := t.TempDir()
	state := filepath.Join(t.TempDir(), "deleted")
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("EV_STATE", state)
	t.Setenv("EV_LOG", log)
	script := `#!/bin/sh
echo "$*" >> "$EV_LOG"
case "$1 $2" in
"route show")
  if [ ! -f "$EV_STATE" ]; then echo 'default via 40.1.1.254 dev rdma1 proto static'; fi
  echo '40.1.1.0/24 dev rdma1 proto static scope link src 40.1.1.11' ;;
"route del") : > "$EV_STATE" ;;
*) exit 9 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "ip"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	old := ownedRouteObject{Device: "rdma1", IP: "40.1.1.11", Gateway: "40.1.1.254", RouteCIDR: "40.1.1.0/24", Table: 101, Priority: 32761}
	next := old
	next.Gateway = "40.1.1.253"
	if err := a.removeObsoleteOwnedRoutes(old, []ownedRouteObject{next}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(log)
	if strings.Contains(string(data), "rule ") || strings.Contains(string(data), "route del 40.") {
		t.Fatalf("deleted shared object: %s", data)
	}
	if !strings.Contains(string(data), "route del default via 40.1.1.254") {
		t.Fatalf("did not remove old default: %s", data)
	}
}

func TestSysctlRetirementRestoresOriginalLinesAndPreservesForeignKeys(t *testing.T) {
	a := evolutionApp(t)
	key := "net.ipv4.conf.rdma9.arp_ignore"
	old := sysctlOwnership{Version: 1, Entries: map[string]ownedSysctl{key: {Value: "2", OriginalLines: []string{key + " = 0"}}}}
	if err := a.writeNetworkState(sysctlOwnershipFile, old); err != nil {
		t.Fatal(err)
	}
	content := key + " = 2\nnet.ipv4.conf.foreign.arp_ignore = 7\n# custom\n"
	if err := atomicWriteFile(a.targetPath(sysctlFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.ensureSysctlSettings(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(a.targetPath(sysctlFile))
	for _, want := range []string{key + " = 0", "net.ipv4.conf.foreign.arp_ignore = 7", "# custom"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("lost %q: %s", want, data)
		}
	}
	if strings.Contains(string(data), key+" = 2") {
		t.Fatal("stale managed value remained")
	}
	// Resuming a partially written configuration is allowed.
	if err := a.ensureSysctlSettings(); err != nil {
		t.Fatalf("sysctl resume: %v", err)
	}
	if err := atomicWriteFile(a.targetPath(sysctlFile), append(data, []byte("# external edit\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.ensureSysctlSettings(); err == nil {
		t.Fatal("lost concurrent sysctl edit")
	}
}

func TestSysctlExternalEditBlocksBeforeWriting(t *testing.T) {
	a := evolutionApp(t)
	key := "net.ipv4.conf.rdma1.arp_ignore"
	if err := a.writeNetworkState(sysctlOwnershipFile, sysctlOwnership{Version: 1, Entries: map[string]ownedSysctl{key: {Value: "2"}}}); err != nil {
		t.Fatal(err)
	}
	content := key + "=7\n"
	if err := atomicWriteFile(a.targetPath(sysctlFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.ensureSysctlSettings(); err == nil {
		t.Fatal("overwrote externally edited owned sysctl")
	}
	data, _ := os.ReadFile(a.targetPath(sysctlFile))
	if string(data) != content {
		t.Fatal("modified file before conflict detection")
	}
}

func TestMissingSysctlOwnershipDoesNotAdoptExistingCanonicalKey(t *testing.T) {
	a := evolutionApp(t)
	a.Machine.RDMA = []spec.RDMAConfig{{Name: "rdma1", IP: "40.1.1.2", Prefix: 24, Gateway: "40.1.1.1", Table: 101}}
	key := "net.ipv4.conf.rdma1.arp_ignore"
	content := key + " = 2\n"
	if err := atomicWriteFile(a.targetPath(sysctlFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.ensureSysctlSettings(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(a.targetPath(sysctlTransactionFile))
	if err != nil {
		t.Fatal(err)
	}
	var ownership sysctlOwnership
	var transaction sysctlTransaction
	if err := json.Unmarshal(data, &transaction); err != nil {
		t.Fatal(err)
	}
	ownership = transaction.Next
	if !ownership.Unowned[key] {
		t.Fatalf("existing canonical key was adopted: %+v", ownership)
	}
	if _, ok := ownership.Entries[key]; ok {
		t.Fatalf("existing key became owned: %+v", ownership)
	}
}

func TestNetworkActionPersistsFailureAndRetriesSameAction(t *testing.T) {
	a := evolutionApp(t)
	a.networkTransaction = &networkTransaction{Version: 1, Writes: map[string]string{}, Retired: map[string]bool{}}
	mustWriteNetDevice(t, a.Root, "eth0", "aa:bb:cc:dd:ee:01", "0000:01:00.0", "ixgbe", 0, "p0")
	if err := os.WriteFile(filepath.Join(a.Root, "sys/class/net/eth0/flags"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "fail")
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EV_FAIL", marker)
	if err := os.WriteFile(filepath.Join(bin, "ip"), []byte("#!/bin/sh\nif [ -f \"$EV_FAIL\" ]; then exit 7; fi\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if err := a.runNetworkCommand("link-down", "eth0", "ip", "link", "set", "dev", "eth0", "down"); err == nil {
		t.Fatal("expected action failure")
	}
	if len(a.networkTransaction.Actions) != 1 || a.networkTransaction.Actions[0].Status != "failed" || a.networkTransaction.Actions[0].Attempts != 1 {
		t.Fatalf("bad failed action: %+v", a.networkTransaction.Actions)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	// There is no real /sys fixture for this command; the retry must at least
	// reuse the same action record rather than append a duplicate record.
	_ = a.runNetworkCommand("link-down", "eth0", "ip", "link", "set", "dev", "eth0", "down")
	if len(a.networkTransaction.Actions) != 1 {
		t.Fatalf("retry appended duplicate action: %+v", a.networkTransaction.Actions)
	}
}

func TestDeferredNetworkDoesNotCommitLiveSuccess(t *testing.T) {
	a := evolutionApp(t)
	disabled := false
	a.Bundle.Defaults.ApplyNetworkImmediately = &disabled
	if err := a.beginNetworkTransaction(); err != nil {
		t.Fatal(err)
	}
	a.networkTransaction.Next = &networkManifest{Version: 2, Files: map[string]string{}, Roles: map[string]string{}}
	if err := a.finishNetworkTransaction(); err != nil {
		t.Fatal(err)
	}
	if a.pathExists(networkManifestFile) {
		t.Fatal("deferred configuration recorded as live success")
	}
	if a.networkTransaction.Phase != "awaiting-runtime" {
		t.Fatal("missing deferred status")
	}
}

func TestLegacyNetplanMigrationUsesManifestAndSavedPhysicalBindings(t *testing.T) {
	a := evolutionApp(t)
	bindings := []interfaceBinding{{Name: "mgmt0", Kind: "mgmt", MAC: "aa:bb:cc:dd:ee:01"}, {Name: "mgmt1", Kind: "mgmt", MAC: "aa:bb:cc:dd:ee:02"}}
	if err := a.saveBindingState("old", bindings); err != nil {
		t.Fatal(err)
	}
	machine := spec.MachineConfig{MgmtIfaces: []string{"mgmt0", "mgmt1"}, MgmtBondName: "bond0", MgmtIP: "10.1.0.2", MgmtPrefix: 24, MgmtGateway: "10.1.0.1", MgmtMTU: 1500, BondMode: "802.3ad"}
	path := filepath.Join(netplanDir, "00-kunlun-bond.yaml")
	content := renderMgmtNetplan(machine)
	if err := atomicWriteFile(a.targetPath(path), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	tx := &networkTransaction{Old: networkManifest{Version: 1, Files: map[string]string{path: contentDigest([]byte(content))}, Roles: map[string]string{path: "mgmt"}}}
	if err := a.migrateLegacyNetworkTopology(tx); err != nil {
		t.Fatal(err)
	}
	bond, ok := topologyLink(tx.Old.Topology, "bond0")
	if !ok || !bond.Bond || bond.Address != "10.1.0.2/24" || bond.Gateway != "10.1.0.1" {
		t.Fatalf("bad migrated bond: %+v", bond)
	}
	member, _ := topologyLink(tx.Old.Topology, "mgmt0")
	if member.Master != "bond0" || member.MAC != bindings[0].MAC {
		t.Fatalf("bad migrated member: %+v", member)
	}
}

func TestBondToSingleRetiresAddressMembersAndBondWithoutFlush(t *testing.T) {
	a := evolutionApp(t)
	a.Machine = spec.MachineConfig{MgmtIP: "10.1.0.2", MgmtPrefix: 24, MgmtIfaces: []string{"mgmt0"}}
	for i, name := range []string{"mgmt0", "mgmt1"} {
		mac := []string{"aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02"}[i]
		mustWriteNetDevice(t, a.Root, name, mac, []string{"0000:01:00.0", "0000:02:00.0"}[i], "ixgbe", i, "p0")
		if err := os.Symlink("../bond0", a.targetPath("/sys/class/net/"+name+"/master")); err != nil {
			t.Fatal(err)
		}
	}
	if err := atomicWriteFile(a.targetPath("/sys/class/net/bond0/bonding/slaves"), []byte("mgmt0 mgmt1"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := networkTopology{Links: []ownedNetworkLink{
		{Name: "mgmt0", Role: "mgmt", MAC: "aa:bb:cc:dd:ee:01", Master: "bond0"},
		{Name: "mgmt1", Role: "mgmt", MAC: "aa:bb:cc:dd:ee:02", Master: "bond0"},
		{Name: "bond0", Role: "mgmt", Bond: true, Address: "10.1.0.2/24"},
	}}
	next := networkTopology{Links: []ownedNetworkLink{{Name: "mgmt0", Role: "mgmt", MAC: "aa:bb:cc:dd:ee:01", Address: "10.1.0.2/24"}}}
	a.networkTransaction = &networkTransaction{Phase: "prepared", Old: networkManifest{Topology: old}}
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("EV_ROOT", a.Root)
	t.Setenv("EV_LOG", log)
	script := `#!/bin/sh
echo "$*" >> "$EV_LOG"
case "$1 $2" in
"-j -4")
  if [ "$6" = bond0 ] && [ ! -f "$EV_ROOT/address-deleted" ]; then
    echo '[{"addr_info":[{"local":"10.1.0.2","prefixlen":24}]}]'
  else echo '[{"addr_info":[]}]'; fi ;;
"address del") : > "$EV_ROOT/address-deleted" ;;
"link set") /bin/rm "$EV_ROOT/sys/class/net/$4/master" ;;
"link delete") /bin/rm -r "$EV_ROOT/sys/class/net/$4" ;;
*) exit 8 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "ip"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if err := a.retireNetworkTopology(next); err != nil {
		t.Fatal(err)
	}
	if a.interfaceExists("bond0") || a.interfaceMaster("mgmt0") != "" || a.interfaceMaster("mgmt1") != "" {
		t.Fatal("old topology remains")
	}
	data, _ := os.ReadFile(log)
	if !strings.Contains(string(data), "address del 10.1.0.2/24 dev bond0") || strings.Contains(string(data), "flush") {
		t.Fatalf("unexpected operations: %s", data)
	}
	// A crash after commands but before checkpoint must be repeatable.
	if err := a.retireNetworkTopology(next); err != nil {
		t.Fatalf("repeat retirement: %v", err)
	}
}

func TestTopologyPreflightProtectsForeignMemberAddress(t *testing.T) {
	a := evolutionApp(t)
	mustWriteNetDevice(t, a.Root, "rdma1", "aa:bb:cc:dd:ee:01", "0000:01:00.0", "mlx5_core", 0, "p0")
	a.networkTransaction = &networkTransaction{Old: networkManifest{Topology: networkTopology{Links: []ownedNetworkLink{{Name: "rdma1", Role: "rdma", MAC: "aa:bb:cc:dd:ee:01", Address: "40.1.1.2/24"}}}}}
	bin := t.TempDir()
	script := "#!/bin/sh\necho '[{\"addr_info\":[{\"local\":\"172.16.1.1\",\"prefixlen\":24}]}]'\n"
	if err := os.WriteFile(filepath.Join(bin, "ip"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if err := a.preflightNetworkTopology(networkTopology{}); err == nil || !strings.Contains(err.Error(), "unmanaged address") {
		t.Fatalf("expected foreign address protection, got %v", err)
	}
}

func TestUnchangedFilesStillRetryFailedBackendAndVerifyRuntime(t *testing.T) {
	for _, backend := range []string{"netplan", "networkmanager", "network"} {
		t.Run(backend, func(t *testing.T) {
			a := evolutionApp(t)
			a.Bundle.Platform.NetworkBackend = backend
			a.Machine = spec.MachineConfig{MgmtIP: "10.1.0.2", MgmtIfaces: []string{"mgmt0"}}
			mustWriteNetDevice(t, a.Root, "mgmt0", "aa:bb:cc:dd:ee:01", "0000:01:00.0", "ixgbe", 0, "p0")
			topology := networkTopology{Backend: backend, Links: []ownedNetworkLink{{Name: "mgmt0", Role: "mgmt", MAC: "aa:bb:cc:dd:ee:01", Address: "10.1.0.2/24"}}}
			a.networkTransaction = &networkTransaction{Phase: "files-written", Topology: topology, Old: networkManifest{Topology: topology}, Next: &networkManifest{}}
			bin := t.TempDir()
			marker := filepath.Join(t.TempDir(), "fail")
			log := filepath.Join(t.TempDir(), "calls")
			if err := os.WriteFile(marker, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("EV_FAIL", marker)
			t.Setenv("EV_LOG", log)
			for _, name := range []string{"netplan", "nmcli", "ifup"} {
				script := `#!/bin/sh
echo "$0 $*" >> "$EV_LOG"
if [ "$1" = generate ] || [ "$2" = reload ]; then exit 0; fi
if [ -f "$EV_FAIL" ]; then exit 7; fi
`
				if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(bin, "ip"), []byte("#!/bin/sh\necho '[{\"addr_info\":[]}]'\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			if err := a.applyNetworkSettingsIfChanged(false); err == nil {
				t.Fatal("backend failure was swallowed")
			}
			if err := os.Remove(marker); err != nil {
				t.Fatal(err)
			}
			if err := a.applyNetworkSettingsIfChanged(false); err != nil {
				t.Fatalf("retry unchanged files: %v", err)
			}
			if err := a.verifyNetworkTopology(topology); err == nil {
				t.Fatal("successful backend exit accepted without live address")
			}
			data, _ := os.ReadFile(log)
			if len(strings.Split(strings.TrimSpace(string(data)), "\n")) < 2 {
				t.Fatalf("backend was not retried: %s", data)
			}
		})
	}
}

func TestRenderedRouteScriptRejectsOverlappingForeignRule(t *testing.T) {
	// Exercise the generated shell, rather than only its Go rendering.
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "writes")
	t.Setenv("EV_LOG", log)
	script := `#!/bin/sh
case "$1 $2" in
"-o -4") echo '2: rdma1 inet 40.1.1.11/24' ;;
"rule show") echo '32761: not from 40.1.1.11 lookup 101' ;;
*) echo "$*" >> "$EV_LOG" ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "ip"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "flock"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := renderRouteScript(spec.RDMAConfig{Name: "rdma1", IP: "40.1.1.11", Prefix: 24, Gateway: "40.1.1.254", Table: 101}, "auto", 32761)
	// Keep all runtime paths in this test's sandbox.
	content = strings.ReplaceAll(content, "/run/lock", t.TempDir())
	content = strings.ReplaceAll(content, "/run/envinit-network-changing", filepath.Join(t.TempDir(), "guard"))
	path := filepath.Join(t.TempDir(), "route.sh")
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", path)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "external policy rule") {
		t.Fatalf("expected overlap rejection: %s %v", out, err)
	}
	if data, err := os.ReadFile(log); err == nil {
		t.Fatalf("mutated routes before conflict detection: %s", data)
	}
}

func TestSysctlRuntimeDriftIsRepairedWithoutFileChange(t *testing.T) {
	a := evolutionApp(t)
	if err := a.ensureSysctlSettings(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(a.targetPath(sysctlFile))
	state := t.TempDir()
	bin := t.TempDir()
	t.Setenv("EV_SYSCTL", state)
	for _, line := range desiredSysctlLines(a.Machine) {
		key, value, _ := parseSysctlLine(line)
		if err := os.WriteFile(filepath.Join(state, key), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	key := "net.core.rmem_max"
	if err := os.WriteFile(filepath.Join(state, key), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = -n ]; then /bin/cat "$EV_SYSCTL/$2"; exit; fi
if [ "$1" = -w ]; then key=${2%%=*}; value=${2#*=}; printf '%s' "$value" > "$EV_SYSCTL/$key"; exit; fi
exit 1
`
	if err := os.WriteFile(filepath.Join(bin, "sysctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if err := a.applyOwnedSysctl(); err != nil {
		t.Fatal(err)
	}
	value, _ := os.ReadFile(filepath.Join(state, key))
	if string(value) != "212992000" {
		t.Fatalf("runtime not repaired: %s", value)
	}
	after, _ := os.ReadFile(a.targetPath(sysctlFile))
	if string(before) != string(after) {
		t.Fatal("runtime repair changed file")
	}
	if a.pathExists(sysctlTransactionFile) {
		t.Fatal("verified sysctl transaction not committed")
	}
}
