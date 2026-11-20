package configure

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Lock coordinates config writers and the downloader's existing delivery lock.
// It is deliberately not called by check-only.
func Lock(p Paths) (func(), error) {
	var releases []func()
	release := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	paths := []string{transactionPrefix(p.Directory) + ".lock"}
	if p.Project != p.Directory {
		paths = append(paths, filepath.Join(p.Project, ".envinit-project.lock"))
	}
	for _, path := range paths {
		if err := RejectSymlinks(path); err != nil {
			release()
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			release()
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			release()
			return nil, err
		}
		unlock, err := lockFile(f)
		if err != nil {
			f.Close()
			release()
			return nil, fmt.Errorf("project is busy (%s): %w", path, err)
		}
		releases = append(releases, func() { unlock(); _ = f.Close() })
	}
	return release, nil
}

type transaction struct {
	Directory, Stage, Backup string
	HadOriginal              bool
}

func PendingTransaction(p Paths) (bool, error) {
	path := transactionPrefix(p.Directory) + ".json"
	if err := RejectSymlinks(path); err != nil {
		return false, err
	}
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

// Recover rolls an interrupted generation back, and must be explicitly requested.
func Recover(p Paths) error {
	path := transactionPrefix(p.Directory) + ".json"
	if err := RejectSymlinks(path); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var tx transaction
	if err := json.Unmarshal(data, &tx); err != nil {
		return err
	}
	prefix := transactionPrefix(p.Directory)
	if tx.Directory != p.Directory || !strings.HasPrefix(tx.Stage, prefix+"-stage-") || !strings.HasPrefix(tx.Backup, prefix+"-backup-") || filepath.Dir(tx.Stage) != filepath.Dir(p.Directory) || filepath.Dir(tx.Backup) != filepath.Dir(p.Directory) {
		return fmt.Errorf("invalid transaction paths in %s", path)
	}
	for _, target := range []string{tx.Directory, tx.Stage, tx.Backup} {
		if err := RejectSymlinks(target); err != nil {
			return err
		}
	}
	if _, err := os.Stat(tx.Backup); err == nil {
		if _, err := os.Stat(tx.Directory); err == nil {
			// Preserve an installed or externally edited generation for inspection.
			failed, err := os.MkdirTemp(filepath.Dir(p.Directory), filepath.Base(prefix)+"-recovered-")
			if err != nil {
				return err
			}
			if err := os.Remove(failed); err != nil {
				return err
			}
			if err := os.Rename(tx.Directory, failed); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(tx.Backup, tx.Directory); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	} else if tx.HadOriginal {
		if _, err := os.Stat(tx.Directory); err != nil {
			return fmt.Errorf("original and backup unavailable; inspect %s: %w", path, err)
		}
	} else {
		// No original: preserve the installed generation for inspection and
		// restore the original absence of the planning directory.
		if _, err := os.Stat(tx.Directory); err == nil {
			if err := os.Rename(tx.Directory, tx.Backup); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.RemoveAll(tx.Stage); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(p.Directory)); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(p.Directory))
}

// Snapshot detects edits made by external editors between review and save.
func Snapshot(dir string) (string, error) {
	if err := RejectSymlinks(dir); err != nil {
		return "", err
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return "missing", nil
	} else if err != nil {
		return "", err
	}
	h := sha256.New()
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("unsupported planning entry: %s", path)
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00%d\x00", rel, info.Mode(), info.Size())
		if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.Copy(h, f)
			closeErr := f.Close()
			if err != nil {
				return err
			}
			return closeErr
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), err
}

// Save installs a whole directory generation. Readers may see a brief missing
// directory during the rename pair, but never a mixed bundle/inventory pair.
// Callers hold Lock, validate the final combination, and provide its snapshot.
func Save(p Paths, files map[string][]byte, expected string) (string, error) {
	if pending, err := PendingTransaction(p); err != nil {
		return "", err
	} else if pending {
		return "", fmt.Errorf("interrupted transaction: run config --recover first")
	}
	current, err := Snapshot(p.Directory)
	if err != nil {
		return "", err
	}
	if current != expected {
		return "", fmt.Errorf("planning files changed since review; reload before saving")
	}
	changed := false
	for name, data := range files {
		if name != filepath.Base(p.Bundle) && name != filepath.Base(p.Inventory) {
			return "", fmt.Errorf("invalid save target %s", name)
		}
		old, err := os.ReadFile(filepath.Join(p.Directory, name))
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		changed = changed || err != nil || !bytes.Equal(old, data)
	}
	if !changed {
		return "", nil
	}
	prefix := transactionPrefix(p.Directory)
	stage, err := os.MkdirTemp(filepath.Dir(p.Directory), filepath.Base(prefix)+"-stage-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	hadOriginal := current != "missing"
	if hadOriginal {
		if err := copyTree(p.Directory, stage); err != nil {
			return "", err
		}
	}
	for name, data := range files {
		mode := os.FileMode(0o600)
		if info, err := os.Stat(filepath.Join(p.Directory, name)); err == nil {
			mode = info.Mode().Perm()
		}
		if err := writeSynced(filepath.Join(stage, name), data, mode); err != nil {
			return "", err
		}
	}
	if hadOriginal {
		info, err := os.Stat(p.Directory)
		if err != nil {
			return "", err
		}
		if err := os.Chmod(stage, info.Mode().Perm()); err != nil {
			return "", err
		}
	}
	if err := syncTreeDirectories(stage); err != nil {
		return "", err
	}
	backup, err := os.MkdirTemp(filepath.Dir(p.Directory), filepath.Base(prefix)+"-backup-")
	if err != nil {
		return "", err
	}
	if err := os.Remove(backup); err != nil {
		return "", err
	}
	tx := transaction{p.Directory, stage, backup, hadOriginal}
	data, err := json.Marshal(tx)
	if err != nil {
		return "", err
	}
	journal := prefix + ".json"
	journalFile, err := os.CreateTemp(filepath.Dir(journal), filepath.Base(prefix)+"-journal-")
	if err != nil {
		return "", err
	}
	journalTemp := journalFile.Name()
	if err := journalFile.Close(); err != nil {
		return "", err
	}
	defer os.Remove(journalTemp)
	if err := writeSynced(journalTemp, data, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(journalTemp, journal); err != nil {
		return "", err
	}
	// Journal and stage are durable before the first rename.
	if err := syncDirectory(filepath.Dir(p.Directory)); err != nil {
		return "", err
	}
	current, err = Snapshot(p.Directory)
	if err != nil || current != expected {
		_ = os.Remove(journal)
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("planning changed while preparing save")
	}
	if hadOriginal {
		if err := os.Rename(p.Directory, backup); err != nil {
			_ = os.Remove(journal)
			return "", err
		}
	}
	if err := os.Rename(stage, p.Directory); err != nil {
		if hadOriginal {
			if restore := os.Rename(backup, p.Directory); restore != nil {
				return "", fmt.Errorf("save: %v; restore: %v; transaction %s backup %s", err, restore, journal, backup)
			}
		}
		_ = os.Remove(journal)
		return "", err
	}
	if err := syncDirectory(filepath.Dir(p.Directory)); err != nil {
		return "", fmt.Errorf("generation installed, transaction pending at %s: %w", journal, err)
	}
	if err := os.Remove(journal); err != nil {
		return "", err
	}
	if err := syncDirectory(filepath.Dir(p.Directory)); err != nil {
		return "", err
	}
	if !hadOriginal {
		backup = ""
	}
	return backup, nil
}

func syncTreeDirectories(root string) error {
	var dirs []string
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	}); err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := syncDirectory(dirs[i]); err != nil {
			return err
		}
	}
	return nil
}

func writeSynced(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func copyTree(source, target string) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(target, rel)
		if info.IsDir() {
			if rel == "." {
				return nil
			}
			return os.Mkdir(dest, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported planning entry %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return writeSynced(dest, data, info.Mode().Perm())
	})
}
