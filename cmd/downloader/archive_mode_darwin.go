//go:build darwin

package main

import (
	"fmt"
	"os"
)

func applyArchiveMode(path string, mode os.FileMode) error {
	// macOS preserves the executable bits carried by the Linux delivery tar,
	// which is required for env_init and the bundled run scripts.
	if err := os.Chmod(path, mode.Perm()); err != nil {
		return fmt.Errorf("apply macOS archive mode to %s: %w", path, err)
	}
	return nil
}
