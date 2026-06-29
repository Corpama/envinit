package runner

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func (a *App) describeHostnameAction() string {
	if !a.HostSpecified {
		return ""
	}
	desired := strings.TrimSpace(a.Machine.Hostname)
	if desired == "" {
		return "skip hostname enforcement because the matched inventory row has no hostname"
	}
	current, err := os.Hostname()
	if err != nil {
		return fmt.Sprintf("ensure system hostname is %s", desired)
	}
	if strings.EqualFold(strings.TrimSpace(current), desired) {
		return fmt.Sprintf("system hostname already matches %s", desired)
	}
	return fmt.Sprintf("set system hostname from %s to %s", current, desired)
}

func (a *App) ensureHostname() error {
	if !a.HostSpecified {
		return nil
	}
	desired := strings.TrimSpace(a.Machine.Hostname)
	if desired == "" {
		a.logf("skip hostname enforcement: matched inventory row has no hostname")
		return nil
	}
	current, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("read current hostname: %w", err)
	}
	if strings.EqualFold(strings.TrimSpace(current), desired) {
		a.logf("hostname already %s", desired)
		return nil
	}
	a.logf("set hostname %s -> %s", current, desired)
	if _, err := exec.LookPath("hostnamectl"); err == nil {
		return a.runCmd("", nil, "hostnamectl", "set-hostname", desired)
	}
	if err := a.writeManagedFile("/etc/hostname", desired+"\n", 0o644); err != nil {
		return err
	}
	return a.runCmd("", nil, "hostname", desired)
}
