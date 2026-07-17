package runner

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"envinit/internal/spec"
)

func TestApplyProgressResumesFromFailedStage(t *testing.T) {
	root := t.TempDir()
	first := newProgressTestApp(root)
	var firstRun []string
	first.runStageOverride = func(stage string) error {
		firstRun = append(firstRun, stage)
		if stage == "xdr" {
			return errors.New("simulated xdr failure")
		}
		return nil
	}
	err := first.runSelectedStages()
	if err == nil || !strings.Contains(err.Error(), "next apply will retry this stage") {
		t.Fatalf("expected saved failure, got %v", err)
	}
	wantFirst := []string{"software", "ofed", "network", "xre", "xdr"}
	if !reflect.DeepEqual(firstRun, wantFirst) {
		t.Fatalf("unexpected first run: got %v want %v", firstRun, wantFirst)
	}

	progress := readApplyProgressForTest(t, root)
	info, err := os.Stat(resolveTargetPath(root, applyProgressFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("unexpected progress permissions: %o", info.Mode().Perm())
	}
	if progress.CurrentStage != "xdr" || progress.LastError != "simulated xdr failure" {
		t.Fatalf("unexpected failed progress: %#v", progress)
	}
	if want := []string{"software", "ofed", "network", "xre"}; !reflect.DeepEqual(progress.CompletedStages, want) {
		t.Fatalf("unexpected completed stages: got %v want %v", progress.CompletedStages, want)
	}

	second := newProgressTestApp(root)
	var secondRun []string
	second.runStageOverride = func(stage string) error {
		secondRun = append(secondRun, stage)
		return nil
	}
	if err := second.runSelectedStages(); err != nil {
		t.Fatalf("resume stages: %v", err)
	}
	wantSecond := []string{"xdr", "firmware", "container", "mlxconfig", "sysctl", "kernel", "post"}
	if !reflect.DeepEqual(secondRun, wantSecond) {
		t.Fatalf("unexpected resumed run: got %v want %v", secondRun, wantSecond)
	}
	progress = readApplyProgressForTest(t, root)
	if progress.CurrentStage != "" || progress.LastError != "" || !reflect.DeepEqual(progress.CompletedStages, stageOrder) {
		t.Fatalf("unexpected completed progress: %#v", progress)
	}

	third := newProgressTestApp(root)
	third.runStageOverride = func(stage string) error {
		t.Fatalf("completed workflow should not rerun stage %s", stage)
		return nil
	}
	if err := third.runSelectedStages(); err != nil {
		t.Fatalf("load completed progress: %v", err)
	}
}

func TestApplyProgressConfigurationChangeStartsFromBeginning(t *testing.T) {
	root := t.TempDir()
	first := newProgressTestApp(root)
	failureReached := false
	first.runStageOverride = func(stage string) error {
		if stage == "network" {
			failureReached = true
			return errors.New("stop")
		}
		return nil
	}
	if err := first.runSelectedStages(); err == nil || !failureReached {
		t.Fatalf("expected initial failure, got %v", err)
	}

	changed := newProgressTestApp(root)
	changed.Bundle.PostPackages = []string{"data/new-package.rpm"}
	var ran []string
	changed.runStageOverride = func(stage string) error {
		ran = append(ran, stage)
		return errors.New("stop after first stage")
	}
	if err := changed.runSelectedStages(); err == nil {
		t.Fatal("expected test stop")
	}
	if !reflect.DeepEqual(ran, []string{"software"}) {
		t.Fatalf("configuration change did not restart from software: %v", ran)
	}
}

func TestExplicitStageRunDoesNotUseOrUpdateApplyProgress(t *testing.T) {
	root := t.TempDir()
	app := newProgressTestApp(root)
	app.Stages = map[string]bool{"xdr": true}
	var ran []string
	app.runStageOverride = func(stage string) error {
		ran = append(ran, stage)
		return nil
	}
	if err := app.runSelectedStages(); err != nil {
		t.Fatalf("run explicit stage: %v", err)
	}
	if !reflect.DeepEqual(ran, []string{"xdr"}) {
		t.Fatalf("unexpected explicit stages: %v", ran)
	}
	if _, err := os.Stat(resolveTargetPath(root, applyProgressFile)); !os.IsNotExist(err) {
		t.Fatalf("explicit stage unexpectedly wrote progress: %v", err)
	}
}

func TestApplyProgressFingerprintUsesOriginalInventoryRecord(t *testing.T) {
	app := newProgressTestApp(t.TempDir())
	app.applySourceRecord = spec.MachineRecord{HostID: "node-1", MgmtIface1: "mgmt0", MgmtMAC1: "aa:bb:cc:dd:ee:01"}
	app.hasApplySourceRecord = true
	before, err := app.applyConfigurationFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	app.Machine.MgmtIfaces = []string{"renamed-after-network-stage"}
	afterRuntimeChange, err := app.applyConfigurationFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if before != afterRuntimeChange {
		t.Fatal("runtime interface rename unexpectedly invalidated apply progress")
	}
	app.applySourceRecord.MgmtIface1 = "changed-in-inventory"
	afterInventoryChange, err := app.applyConfigurationFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if before == afterInventoryChange {
		t.Fatal("inventory change did not invalidate apply progress")
	}
	app.applySourceRecord.MgmtIface1 = "mgmt0"
	app.Bundle.Check.Bandwidth.MinGBits = 390
	afterCheckOnlyChange, err := app.applyConfigurationFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if before != afterCheckOnlyChange {
		t.Fatal("check-only bundle change unexpectedly invalidated apply progress")
	}
}

func newProgressTestApp(root string) *App {
	return &App{
		Root:        root,
		Bundle:      spec.Bundle{},
		Machine:     spec.MachineConfig{HostID: "node-1", Hostname: "node-1"},
		Stages:      map[string]bool{"all": true},
		ResumeApply: true,
		Output:      ioDiscard{},
	}
}

func readApplyProgressForTest(t *testing.T, root string) applyProgress {
	t.Helper()
	data, err := os.ReadFile(resolveTargetPath(root, applyProgressFile))
	if err != nil {
		t.Fatalf("read apply progress: %v", err)
	}
	var progress applyProgress
	if err := json.Unmarshal(data, &progress); err != nil {
		t.Fatalf("parse apply progress: %v", err)
	}
	return progress
}
