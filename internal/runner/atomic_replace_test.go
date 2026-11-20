package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicReplacerReplacesCompleteContentAndMode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "etc", "envinit.conf")
	if err := (AtomicReplacer{Mode: 0o640}).Replace(path, []byte("complete\n")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "complete\n" {
		t.Fatalf("content=%q err=%v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode=%v err=%v", info.Mode().Perm(), err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if len(entry.Name()) >= 15 && entry.Name()[:15] == ".envinit-atomic-" {
			t.Fatalf("temporary file leaked: %s", entry.Name())
		}
	}
}

func TestAtomicReplacerRefusesInvalidParentWithoutChangingExistingFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := (AtomicReplacer{Mode: 0o600}).Replace(filepath.Join(path, "child"), []byte("new"))
	if err == nil {
		t.Fatal("expected invalid parent failure")
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "old" {
		t.Fatalf("existing file changed: %q %v", data, readErr)
	}
}
