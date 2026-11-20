package runner

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Stage a complete, synced file before replacing the live configuration.
func atomicWriteFile(path string, data []byte, mode fs.FileMode) error {
	return (AtomicReplacer{Mode: mode}).Replace(path, data)
}

// Copy backups before touching the live file, including across filesystems.
func (a *App) copyToBackup(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refuse to replace non-regular configuration %s", path)
	}
	now := time.Now
	if a.now != nil {
		now = a.now
	}
	dest, err := a.nextBackupPath(path, now())
	if err != nil {
		return err
	}
	a.logf("backup copy %s -> %s", path, dest)
	if a.DryRun {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		out.Close()
		if !ok {
			os.Remove(dest)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}
