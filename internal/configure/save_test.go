package configure

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func planningTestPaths(t *testing.T) Paths {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := ResolvePaths(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func putTestFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o640); err != nil {
		t.Fatal(err)
	}
}

func TestSaveGenerationPreservesExtrasBackupsAndNoop(t *testing.T) {
	p := planningTestPaths(t)
	putTestFile(t, p.Bundle, "old bundle")
	putTestFile(t, p.Inventory, "old inventory")
	putTestFile(t, filepath.Join(p.Directory, "notes", "README"), "user notes")
	unlock, err := Lock(p)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if second, err := Lock(p); err == nil {
		second()
		t.Fatal("concurrent writer acquired lock")
	}
	snapshot, err := Snapshot(p.Directory)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := Save(p, map[string][]byte{"bundle.json": []byte("new bundle")}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{p.Bundle: "new bundle", p.Inventory: "old inventory", filepath.Join(backup, "bundle.json"): "old bundle", filepath.Join(p.Directory, "notes", "README"): "user notes"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("%s = %s, %v", path, got, err)
		}
	}
	info, err := os.Stat(p.Bundle)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatal("file mode lost")
	}
	snapshot, err = Snapshot(p.Directory)
	if err != nil {
		t.Fatal(err)
	}
	backup, err = Save(p, map[string][]byte{"bundle.json": []byte("new bundle")}, snapshot)
	if err != nil || backup != "" {
		t.Fatalf("no-op created backup: %s %v", backup, err)
	}
	putTestFile(t, filepath.Join(p.Directory, "notes", "README"), "external edit")
	if _, err := Save(p, map[string][]byte{"inventory.csv": []byte("new")}, snapshot); err == nil {
		t.Fatal("stale review accepted")
	}
}

func TestRecoveryAtDirectoryRenameBoundaries(t *testing.T) {
	for _, installed := range []bool{false, true} {
		t.Run(map[bool]string{false: "old moved", true: "new installed"}[installed], func(t *testing.T) {
			p := planningTestPaths(t)
			putTestFile(t, p.Bundle, "old")
			prefix := transactionPrefix(p.Directory)
			tx := transaction{p.Directory, prefix + "-stage-test", prefix + "-backup-test", true}
			putTestFile(t, filepath.Join(tx.Stage, "bundle.json"), "new")
			data, _ := json.Marshal(tx)
			putTestFile(t, prefix+".json", string(data))
			if err := os.Rename(p.Directory, tx.Backup); err != nil {
				t.Fatal(err)
			}
			if installed {
				if err := os.Rename(tx.Stage, p.Directory); err != nil {
					t.Fatal(err)
				}
			}
			if err := Recover(p); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(p.Bundle)
			if err != nil || string(data) != "old" {
				t.Fatal("old generation not restored")
			}
			pending, err := PendingTransaction(p)
			if err != nil || pending {
				t.Fatal("journal not cleared")
			}
		})
	}
}

func TestPathsAndSymlinkProtection(t *testing.T) {
	p := planningTestPaths(t)
	if _, err := ResolvePaths(p.Project, filepath.Join(p.Project, "other", "bundle.json"), ""); err == nil {
		t.Fatal("project conflict accepted")
	}
	if _, err := ResolvePaths("", p.Bundle, filepath.Join(p.Project, "other", "inventory.csv")); err == nil {
		t.Fatal("cross-directory accepted")
	}
	putTestFile(t, p.Bundle, "original")
	link := filepath.Join(p.Directory, "link")
	if err := os.Symlink(p.Bundle, link); err != nil {
		t.Skip(err)
	}
	if _, err := Snapshot(p.Directory); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("symlink snapshot: %v", err)
	}
}
