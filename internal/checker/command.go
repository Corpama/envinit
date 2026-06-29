package checker

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"envinit/internal/spec"
)

func runCommand(cfg spec.CheckConfig, target Target, remoteCommand string) (string, error) {
	if target.Local {
		cmd := exec.Command("sh", "-c", remoteCommand)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return stdout.String(), fmt.Errorf("local command on %s: %w\n%s", target.Name, err, stderr.String())
		}
		return stdout.String(), nil
	}

	var lastStdout string
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		stdout, err := runSSHCommandOnce(cfg, target, remoteCommand)
		if err == nil {
			return stdout, nil
		}
		lastStdout = stdout
		lastErr = err
		if !isTransientSSHError(err) || attempt == 3 {
			break
		}
		time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
	}
	return lastStdout, lastErr
}

func runSSHCommandOnce(cfg spec.CheckConfig, target Target, remoteCommand string) (string, error) {
	args := append([]string{}, cfg.SSHOptions...)
	destination := target.Address
	if strings.TrimSpace(cfg.SSHUser) != "" {
		destination = cfg.SSHUser + "@" + destination
	}
	args = append(args, destination, remoteCommand)
	cmd := exec.Command("ssh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("ssh %s: %w\n%s", destination, err, stderr.String())
	}
	return stdout.String(), nil
}

func isTransientSSHError(err error) bool {
	text := strings.ToLower(err.Error())
	for _, pattern := range []string{
		"kex_exchange_identification",
		"connection reset by peer",
		"connection timed out",
		"connection timeout",
		"connection closed by remote host",
		"connection closed",
		"banner exchange",
		"maxstartups",
	} {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}
