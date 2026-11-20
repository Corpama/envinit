//go:build windows

package configure

// Individual files are flushed before the recoverable directory rename pair.
func syncDirectory(path string) error { return nil }
