package checker

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"envinit/internal/spec"
)

func TestSSHControlOptionsAddUnattendedDefaults(t *testing.T) {
	got := strings.Join(sshControlOptions(spec.CheckConfig{}), " ")
	for _, want := range []string{
		"-o BatchMode=yes",
		"-o ConnectTimeout=10",
		"-o ServerAliveInterval=15",
		"-o ServerAliveCountMax=4",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in default ssh options: %s", want, got)
		}
	}
}

func TestSSHControlOptionsPreserveExplicitBundleValues(t *testing.T) {
	cfg := spec.CheckConfig{SSH: spec.CheckSSHConfig{Options: []string{
		"-o", "ConnectTimeout=45",
		"-o", "BatchMode=no",
		"-o", "Port=2222",
	}}}
	got := sshControlOptions(cfg)
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "-o ConnectTimeout=45") || strings.Contains(joined, "-o ConnectTimeout=10") {
		t.Fatalf("explicit ConnectTimeout must win: %s", joined)
	}
	if !strings.Contains(joined, "-o BatchMode=no") || strings.Contains(joined, "-o BatchMode=yes") {
		t.Fatalf("explicit BatchMode must win: %s", joined)
	}
	if !strings.Contains(joined, "-o Port=2222") {
		t.Fatalf("unrelated options must be preserved: %s", joined)
	}
	if !strings.Contains(joined, "-o ServerAliveInterval=15") {
		t.Fatalf("missing keepalive fallback: %s", joined)
	}
}

func TestSSHControlOptionsApplyConfiguredPort(t *testing.T) {
	cfg := spec.CheckConfig{SSH: spec.CheckSSHConfig{Port: 9527}}
	joined := strings.Join(sshControlOptions(cfg), " ")
	if !strings.Contains(joined, "-o Port=9527") {
		t.Fatalf("configured port must be passed as -o Port=: %s", joined)
	}
}

func TestSSHControlOptionsExplicitPortWinsOverConfiguredPort(t *testing.T) {
	cfg := spec.CheckConfig{SSH: spec.CheckSSHConfig{
		Port:    9527,
		Options: []string{"-o", "Port=2222"},
	}}
	joined := strings.Join(sshControlOptions(cfg), " ")
	if !strings.Contains(joined, "-o Port=2222") || strings.Contains(joined, "-o Port=9527") {
		t.Fatalf("explicit Port option must win over check.ssh.port: %s", joined)
	}
}

func TestPreflightTargetConnectivityIsPerTargetConcurrent(t *testing.T) {
	binDir := t.TempDir()
	// One host hangs far longer than the per-target deadline; the other returns
	// immediately. Concurrent probing means total wall time is bounded by the
	// slow host's deadline, not the sum, and the fast host still reports ok.
	script := "#!/bin/sh\ncase \"$*\" in\n*slowhost*) sleep 120 ;;\n*) exit 0 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(binDir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write ssh stub: %v", err)
	}
	t.Setenv("PATH", binDir)

	var out bytes.Buffer
	start := time.Now()
	err := preflightTargetConnectivity(context.Background(), spec.CheckConfig{}, []Target{
		{Name: "fast", ControlAddress: "fasthost"},
		{Name: "slow", ControlAddress: "slowhost"},
	}, &out)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected failure for the stalled target")
	}
	if !strings.Contains(err.Error(), "slow (slowhost)") {
		t.Fatalf("expected stalled target in error: %v", err)
	}
	if strings.Contains(err.Error(), "fast (fasthost)") {
		t.Fatalf("fast target should have passed: %v", err)
	}
	if elapsed > probeTargetTimeout+10*time.Second {
		t.Fatalf("preflight took %s; per-target concurrent probing should bound it near %s", elapsed, probeTargetTimeout)
	}
}

func TestPreflightTargetConnectivityReportsEveryUnreachableTarget(t *testing.T) {
	binDir := t.TempDir()
	failing := filepath.Join(binDir, "ssh")
	script := "#!/bin/sh\nprintf 'ssh: connect to host %s port 22: No route to host\\n' \"$#\" >&2\nexit 255\n"
	if err := os.WriteFile(failing, []byte(script), 0o755); err != nil {
		t.Fatalf("write ssh stub: %v", err)
	}
	t.Setenv("PATH", binDir)

	var out bytes.Buffer
	err := preflightTargetConnectivity(context.Background(), spec.CheckConfig{}, []Target{
		{Name: "node-a", ControlAddress: "10.0.0.1"},
		{Name: "node-b", ControlAddress: "10.0.0.2"},
		{Name: "self", Local: true},
	}, &out)
	if err == nil {
		t.Fatal("expected preflight failure for unreachable targets")
	}
	for _, want := range []string{"ssh preflight failed for 2 target(s)", "node-a (10.0.0.1)", "node-b (10.0.0.2)"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected %q in preflight error: %v", want, err)
		}
	}
	if !strings.Contains(err.Error(), "No route to host") {
		t.Fatalf("expected raw ssh output in preflight error: %v", err)
	}
	if !strings.Contains(out.String(), "PREFLIGHT node-a:") {
		t.Fatalf("expected raw ssh output echoed as raw log: %s", out.String())
	}
	if strings.Contains(err.Error(), "self") {
		t.Fatalf("local targets must not be probed over ssh: %v", err)
	}
}

func TestPreflightTargetConnectivityPassesWhenReachable(t *testing.T) {
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "ssh"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write ssh stub: %v", err)
	}
	t.Setenv("PATH", binDir)

	var out bytes.Buffer
	if err := preflightTargetConnectivity(context.Background(), spec.CheckConfig{}, []Target{
		{Name: "node-a", ControlAddress: "10.0.0.1"},
	}, &out); err != nil {
		t.Fatalf("preflight should pass: %v", err)
	}
	if !strings.Contains(out.String(), "INFO ssh preflight: node-a (10.0.0.1) reachable") {
		t.Fatalf("unexpected preflight output: %s", out.String())
	}
}
