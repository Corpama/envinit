//go:build !windows && !darwin

package main

import (
	"fmt"
	"os"
)

func applyArchiveMode(path string, mode os.FileMode) error {
	if err := os.Chmod(path, mode.Perm()); err != nil {
		return fmt.Errorf("apply archive mode to %s: %w", path, err)
	}
	return nil
}
