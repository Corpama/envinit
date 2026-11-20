package runner

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// AtomicReplacer is the single file replacement primitive used by managed
// configuration, boot scripts and state journals. It never exposes a partial
// file: data is synced before rename and the containing directory is synced
// after rename.
type AtomicReplacer struct {
	Mode fs.FileMode
}

func (r AtomicReplacer) Replace(path string, data []byte) error {
	mode := r.Mode.Perm()
	if mode == 0 {
		mode = 0o600
	}
	if err := atomicReplace(path, data, mode); err != nil {
		return fmt.Errorf("atomic replace %s: %w", path, err)
	}
	return nil
}

func atomicReplace(path string, data []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".envinit-atomic-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(mode.Perm()); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
