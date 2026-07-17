//go:build windows

package main

import (
	"fmt"
	"os"
)

func applyArchiveMode(path string, mode os.FileMode) error {
	// Windows does not implement Unix executable bits. Preserve only the
	// read-only distinction and let the delivery scripts select .exe files.
	windowsMode := os.FileMode(0o666)
	if mode.Perm()&0o200 == 0 {
		windowsMode = 0o444
	}
	if err := os.Chmod(path, windowsMode); err != nil {
		return fmt.Errorf("apply Windows archive attributes to %s: %w", path, err)
	}
	return nil
}
