package runner

import (
	"encoding/json"
	"envinit/internal/spec"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutionLockExcludesConcurrentApply(t *testing.T) {
	p := filepath.Join(t.TempDir(), "apply.lock")
	release, err := acquireExecutionLock(p)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := acquireExecutionLock(p); err == nil {
		other()
		t.Fatal("concurrent operation acquired lock")
	}
	release()
	release, err = acquireExecutionLock(p)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestFailedBackupPreservesLiveConfiguration(t *testing.T) {
	root := t.TempDir()
	a := &App{Root: root, Output: io.Discard}
	p := filepath.Join(root, "etc/example.conf")
	if err := atomicWriteFile(p, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.Bundle.Defaults.BackupRoot = "/blocked/backups"
	if err := a.writeManagedFile("/etc/example.conf", "new", 0o600); err == nil {
		t.Fatal("expected backup failure")
	}
	data, err := os.ReadFile(p)
	if err != nil || string(data) != "old" {
		t.Fatalf("lost live config: %q %v", data, err)
	}
}

func TestManagedFileRepeatedWriteAndModeRepair(t *testing.T) {
	root := t.TempDir()
	a := &App{Root: root, Output: io.Discard}
	for i := 0; i < 3; i++ {
		if err := a.writeManagedFile("/etc/example.conf", "same", 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(root, "etc/example.conf")
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.writeManagedFile("/etc/example.conf", "same", 0o600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0o600 {
		t.Fatal("mode not repaired")
	}
	if _, err := os.Stat(filepath.Join(root, "var/lib/envinit/backups")); !os.IsNotExist(err) {
		t.Fatal("unchanged content generated backups")
	}
}

func TestInterruptedOperationIsNotReplayed(t *testing.T) {
	a := &App{Root: t.TempDir(), Output: io.Discard}
	calls := 0
	run := func() error { calls++; return errors.New("interrupted") }
	if a.executeRecorded("install", "material", "", run) == nil {
		t.Fatal("expected failure")
	}
	if a.executeRecorded("install", "material", "", run) == nil {
		t.Fatal("expected recovery gate")
	}
	if calls != 1 {
		t.Fatalf("replayed interrupted operation %d times", calls)
	}
}

func TestSuccessfulUnverifiedOperationIsNotReplayed(t *testing.T) {
	a := &App{Root: t.TempDir(), Output: io.Discard}
	calls := 0
	run := func() error { calls++; return nil }
	if err := a.executeRecorded("firmware", "material", "", run); err != nil {
		t.Fatal(err)
	}
	if err := a.executeRecorded("firmware", "material", "", run); err == nil || !strings.Contains(err.Error(), "needs attention") {
		t.Fatalf("expected unverified recovery gate, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("replayed unverified operation %d times", calls)
	}
}

func TestCompletionCheckMakesOperationRepeatable(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "complete")
	a := &App{Root: root, Output: io.Discard}
	calls := 0
	run := func() error {
		calls++
		return os.WriteFile(marker, []byte("ok"), 0o600)
	}
	check := "test -f " + marker
	if err := a.executeRecorded("installer", "material", check, run); err != nil {
		t.Fatal(err)
	}
	if err := a.executeRecorded("installer", "material", check, run); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("completion check did not suppress replay: %d", calls)
	}
}

func TestDirectoryCopyBacksUpBeforeReplacement(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "material")
	target := filepath.Join(root, "opt/app")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "version"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "version"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &App{Root: root, Output: io.Discard}
	if err := a.copyDirWithBackup(source, target); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(target, "version"))
	if err != nil || string(got) != "new" {
		t.Fatalf("target not replaced: %q %v", got, err)
	}
	matches, err := filepath.Glob(filepath.Join(root, "var/lib/envinit/backups", "*", "opt/app/version"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("backup missing: %v %v", matches, err)
	}
	old, err := os.ReadFile(matches[0])
	if err != nil || string(old) != "old" {
		t.Fatalf("backup content: %q %v", old, err)
	}
}

func TestOwnedRouteMatchingAllowsKernelMetadataButNotDifferentRoute(t *testing.T) {
	output := "default via 40.1.1.254 dev xgbe1 proto static metric 100\n" +
		"40.1.1.0/24 dev xgbe1 proto static scope link src 40.1.1.11 metric 100\n" +
		"40.1.2.0/24 dev xgbe2 scope link src 40.1.2.11\n"
	if !containsTokenSubsequenceLine(output, "default via 40.1.1.254 dev xgbe1") {
		t.Fatal("default route with kernel metadata was not matched")
	}
	if !containsTokenSubsequenceLine(output, "40.1.1.0/24 dev xgbe1 scope link src 40.1.1.11") {
		t.Fatal("connected route with kernel metadata was not matched")
	}
	if containsTokenSubsequenceLine(output, "40.1.1.0/24 dev xgbe2 scope link src 40.1.1.11") {
		t.Fatal("different route was matched")
	}
}

func TestKernelBootConfigRebuildIsRecorded(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc/default"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc/default/grub"), []byte("GRUB_CMDLINE_LINUX=\"rw biosdevname=0 iommu=pt mitigations=off nokaslr\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "update-grub.log")
	script := "#!/bin/sh\nprintf 'run\\n' >> " + logPath + "\n"
	if err := os.WriteFile(filepath.Join(binDir, "update-grub"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	a := &App{Root: root, Output: io.Discard}
	if err := a.runKernelStage(); err != nil {
		t.Fatal(err)
	}
	if err := a.runKernelStage(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil || string(data) != "run\n" {
		t.Fatalf("boot config rebuilt unexpectedly: %q %v", data, err)
	}
}

func TestBindingStateRestoresManagementAndRDMA(t *testing.T) {
	root := t.TempDir()
	mustWriteNetDevice(t, root, "eno1", "aa:bb:cc:dd:ee:01", "0000:10:00.0", "ixgbe", 0, "p0")
	mustWriteNetDevice(t, root, "rdma0", "aa:bb:cc:dd:ee:02", "0000:20:00.0", "mlx5_core", 0, "p0")
	a := &App{
		Root: root,
		Machine: spec.MachineConfig{
			MgmtIfaces: []string{"mgmt0"},
			RDMA:       []spec.RDMAConfig{{Name: "rdma1"}},
		},
		Output: io.Discard,
	}
	bindings := []interfaceBinding{
		{Kind: "mgmt", Name: "mgmt0", MAC: "aa:bb:cc:dd:ee:01", Reason: "user selected"},
		{Kind: "rdma", Name: "rdma1", MAC: "aa:bb:cc:dd:ee:02", Reason: "unique probe"},
	}
	if err := a.saveBindingState("plan", bindings); err != nil {
		t.Fatal(err)
	}
	if err := a.restoreBindingState("plan"); err != nil {
		t.Fatal(err)
	}
	if len(a.Machine.MgmtMACs) != 1 || a.Machine.MgmtMACs[0] != bindings[0].MAC || a.Machine.RDMA[0].MAC != bindings[1].MAC {
		t.Fatalf("bindings not restored: mgmt=%v rdma=%v", a.Machine.MgmtMACs, a.Machine.RDMA)
	}
}

func TestRemoveOwnedRouteObjectKeepsForeignRoutes(t *testing.T) {
	dir := t.TempDir()
	rules := filepath.Join(dir, "rules")
	routes := filepath.Join(dir, "routes")
	if err := os.WriteFile(rules, []byte("32761: from 40.1.1.11 lookup 101\n32761: from all oif xgbe1 lookup 101\n1000: from all lookup main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(routes, []byte("default via 40.1.1.254 dev xgbe1 proto static\n40.1.1.0/24 dev xgbe1 proto static scope link src 40.1.1.11\n10.0.0.0/8 dev other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	script := `#!/bin/sh
case "$1 $2" in
  "rule show") cat "$ENVINIT_TEST_RULES" ;;
  "rule del")
    if printf '%s\n' "$*" | grep -q ' from '; then grep -v '^32761: from 40.1.1.11 lookup 101$' "$ENVINIT_TEST_RULES" > "$ENVINIT_TEST_RULES.tmp"
    else grep -v '^32761: from all oif xgbe1 lookup 101$' "$ENVINIT_TEST_RULES" > "$ENVINIT_TEST_RULES.tmp"; fi
    mv "$ENVINIT_TEST_RULES.tmp" "$ENVINIT_TEST_RULES" ;;
  "route show") cat "$ENVINIT_TEST_ROUTES" ;;
  "route del")
    if [ "$3" = default ]; then grep -v '^default via 40.1.1.254 dev xgbe1 proto static$' "$ENVINIT_TEST_ROUTES" > "$ENVINIT_TEST_ROUTES.tmp"
    else grep -v '^40.1.1.0/24 dev xgbe1 proto static scope link src 40.1.1.11$' "$ENVINIT_TEST_ROUTES" > "$ENVINIT_TEST_ROUTES.tmp"; fi
    mv "$ENVINIT_TEST_ROUTES.tmp" "$ENVINIT_TEST_ROUTES" ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "ip"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENVINIT_TEST_RULES", rules)
	t.Setenv("ENVINIT_TEST_ROUTES", routes)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	a := &App{Output: io.Discard}
	item := ownedRouteObject{Device: "xgbe1", IP: "40.1.1.11", Gateway: "40.1.1.254", RouteCIDR: "40.1.1.0/24", Table: 101, Priority: 32761}
	if err := a.removeOwnedRouteObject(item); err != nil {
		t.Fatal(err)
	}
	remainingRules, _ := os.ReadFile(rules)
	remainingRoutes, _ := os.ReadFile(routes)
	if string(remainingRules) != "1000: from all lookup main\n" || string(remainingRoutes) != "10.0.0.0/8 dev other\n" {
		t.Fatalf("foreign objects changed: rules=%q routes=%q", remainingRules, remainingRoutes)
	}
}

func TestDeferredNetworkApplyRetainsStaleRuntimeOwnership(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "var/lib/envinit/network-files.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	stale := ownedRouteObject{Device: "xgbe1", IP: "40.1.1.11", Gateway: "40.1.1.254", RouteCIDR: "40.1.1.0/24", Table: 101, Priority: 32761}
	manifest := networkManifest{Version: 1, Files: map[string]string{}, Roles: map[string]string{}, Runtime: map[string]ownedRouteObject{ownedRouteKey(stale): stale}}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(statePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	disabled := false
	a := &App{Root: root, Output: io.Discard, Bundle: spec.Bundle{Defaults: spec.Defaults{RDMAMode: spec.RDMAModeOff, ApplyNetworkImmediately: &disabled}}}
	if err := a.reconcileNetworkFiles(); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var got networkManifest
	if err := json.Unmarshal(updated, &got); err != nil {
		t.Fatal(err)
	}
	if got.Runtime[ownedRouteKey(stale)] != stale {
		t.Fatalf("deferred stale runtime ownership was lost: %#v", got.Runtime)
	}
}
