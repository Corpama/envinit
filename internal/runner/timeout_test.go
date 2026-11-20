package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"envinit/internal/spec"
)

func TestRunCmdTimeoutTerminatesHungCommand(t *testing.T) {
	app := &App{Output: ioDiscard{}}
	start := time.Now()
	err := app.runCmdTimeout(500*time.Millisecond, "", nil, "sleep", "30")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error for a hung command")
	}
	if !strings.Contains(err.Error(), "exceeded 500ms and was terminated") {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("timeout not enforced, elapsed %s", elapsed)
	}
}

func TestRunCmdTimeoutZeroMeansNoDeadline(t *testing.T) {
	app := &App{Output: ioDiscard{}}
	if err := app.runCmdTimeout(0, "", nil, "true"); err != nil {
		t.Fatalf("no-timeout command should succeed: %v", err)
	}
}

func TestRunCmdUsesCurrentStageTimeout(t *testing.T) {
	app := &App{Output: ioDiscard{}, currentStageTimeout: 500 * time.Millisecond}
	err := app.runCmd("", nil, "sleep", "30")
	if err == nil || !strings.Contains(err.Error(), "was terminated") {
		t.Fatalf("runCmd should inherit the stage timeout: %v", err)
	}
}

func TestRunCmdTimeoutKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "child-alive")
	// The shell spawns a child that would create the marker after 3s, then the
	// parent sleeps. A process-group kill must stop the child too, so the marker
	// must never appear.
	script := "sh -c 'sleep 3; touch " + marker + "' & sleep 30"
	app := &App{Output: ioDiscard{}}
	if err := app.runCmdTimeout(500*time.Millisecond, "", nil, "sh", "-c", script); err == nil {
		t.Fatal("expected timeout error")
	}
	time.Sleep(4 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("child survived process-group kill; marker was created")
	}
}

func TestCaptureCmdProbeTimeout(t *testing.T) {
	app := &App{Output: ioDiscard{}}
	start := time.Now()
	_, err := app.captureCmd("", nil, "sleep", "60")
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "probe command") {
		t.Fatalf("captureCmd should time out with a probe error: %v", err)
	}
	if elapsed > probeCommandTimeout+5*time.Second {
		t.Fatalf("probe timeout not enforced, elapsed %s", elapsed)
	}
}

func TestStageTimeoutResolution(t *testing.T) {
	b := spec.Bundle{}
	b.ApplyDefaults()
	cases := map[string]time.Duration{
		"software":  360 * time.Second,
		"container": 360 * time.Second,
		"ofed":      0,
		"xre":       0,
		"xdr":       0,
		"firmware":  0,
		"post":      0,
		"udev":      360 * time.Second,
	}
	for stage, want := range cases {
		if got := b.ApplyTimeouts.StageTimeout(stage); got != want {
			t.Fatalf("StageTimeout(%q) = %s, want %s", stage, got, want)
		}
	}
	if got := b.ApplyTimeouts.StageTimeout("unknown-stage"); got != 360*time.Second {
		t.Fatalf("unknown stage should fall back to default: %s", got)
	}
}

func TestStageTimeoutHonorsBundleOverride(t *testing.T) {
	b := spec.Bundle{ApplyTimeouts: spec.ApplyTimeouts{
		DefaultSeconds: 120,
		Stages:         map[string]int{"ofed": 1800, "software": 90},
	}}
	b.ApplyDefaults()
	if got := b.ApplyTimeouts.StageTimeout("ofed"); got != 1800*time.Second {
		t.Fatalf("explicit ofed override lost: %s", got)
	}
	if got := b.ApplyTimeouts.StageTimeout("software"); got != 90*time.Second {
		t.Fatalf("explicit software override lost: %s", got)
	}
	// A stage not overridden but present in the default table keeps its built-in
	// default rather than default_seconds.
	if got := b.ApplyTimeouts.StageTimeout("network"); got != 360*time.Second {
		t.Fatalf("network should keep built-in default: %s", got)
	}
}
