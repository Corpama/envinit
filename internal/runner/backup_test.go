package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"envinit/internal/spec"
)

func TestMoveToBackupUsesConfiguredCentralRootAndPreservesPath(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "etc", "netplan", "00-kunlun-bond.yaml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := &App{
		Root: root,
		Bundle: spec.Bundle{Defaults: spec.Defaults{
			BackupRoot: "/srv/envinit-backups",
		}},
		Output: ioDiscard{},
		now: func() time.Time {
			return time.Date(2026, 9, 3, 16, 49, 4, 0, time.UTC)
		},
	}
	if err := app.moveToBackup(target); err != nil {
		t.Fatalf("move to backup: %v", err)
	}
	backup := filepath.Join(root, "srv/envinit-backups/20260903_164904/etc/netplan/00-kunlun-bond.yaml")
	data, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("read centralized backup: %v", err)
	}
	if string(data) != "old\n" {
		t.Fatalf("unexpected backup content %q", data)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected source to be moved, stat err=%v", err)
	}
	if matches, err := filepath.Glob(target + ".bak.*"); err != nil || len(matches) != 0 {
		t.Fatalf("unexpected sibling backups %v (err=%v)", matches, err)
	}
}

func TestMoveToBackupAvoidsCollisionWithinSameTimestamp(t *testing.T) {
	root := t.TempDir()
	app := &App{
		Root:   root,
		Output: ioDiscard{},
		now: func() time.Time {
			return time.Date(2026, 9, 3, 16, 49, 4, 0, time.UTC)
		},
	}
	target := filepath.Join(root, "etc/example.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"first", "second"} {
		if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := app.moveToBackup(target); err != nil {
			t.Fatalf("move %q to backup: %v", content, err)
		}
	}
	base := backupPathForTest(root, "20260903_164904", "/etc/example.conf")
	for path, want := range map[string]string{base: "first", base + ".1": "second"} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("backup %s = %q, err=%v, want %q", path, data, err, want)
		}
	}
}

func TestRelocateLegacyNetworkBackupsForUbuntuAndKylin(t *testing.T) {
	root := t.TempDir()
	legacy := []string{
		"/etc/netplan/00-kunlun-bond.yaml.bak.20260903_161524",
		"/etc/sysconfig/network-scripts/ifcfg-xgbe8.bak.20260903_161524",
		"/etc/sysconfig/network-scripts/route-xgbe8.bak.20260903_161524",
		"/etc/sysconfig/network-scripts/rule-xgbe8.bak.20260903_161524",
	}
	for _, systemPath := range legacy {
		path := filepath.Join(root, strings.TrimPrefix(systemPath, "/"))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(systemPath), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(root, "etc/sysconfig/network-scripts/ifcfg-operator.bak.manual")
	if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := &App{
		Root:   root,
		Output: ioDiscard{},
		now: func() time.Time {
			return time.Date(2026, 9, 3, 17, 0, 0, 0, time.UTC)
		},
	}
	if err := app.relocateLegacyNetworkBackups(); err != nil {
		t.Fatalf("relocate legacy network backups: %v", err)
	}
	for _, systemPath := range legacy {
		original := filepath.Join(root, strings.TrimPrefix(systemPath, "/"))
		if _, err := os.Stat(original); !os.IsNotExist(err) {
			t.Fatalf("expected legacy backup %s to be relocated, stat err=%v", original, err)
		}
		backup := backupPathForTest(root, "20260903_170000", systemPath)
		if _, err := os.Stat(backup); err != nil {
			t.Fatalf("expected centralized legacy backup %s: %v", backup, err)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("manual non-envinit backup should remain: %v", err)
	}
}
