//go:build !linux

package runner

func (a *App) recoverInterruptedProbe() error { return nil }
