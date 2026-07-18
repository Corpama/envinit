package checker

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

func copyFileToTarget(opts Options, target Target, localPath, remotePath string) error {
	if opts.FileCopier != nil {
		return opts.FileCopier(opts.Bundle.Check, target, localPath, remotePath)
	}
	if target.Local {
		return copyLocalFile(localPath, remotePath)
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if err := runSCPOnce(opts.Bundle.Check, target, localPath, remotePath); err == nil {
			return nil
		} else {
			lastErr = err
			if !isTransientSSHError(err) || attempt == 3 {
				break
			}
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
		}
	}
	return lastErr
}

func copyLocalFile(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open local check artifact %s: %w", source, err)
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create local check artifact directory: %w", err)
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create local check artifact %s: %w", target, err)
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return fmt.Errorf("copy local check artifact %s: %w", target, err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close local check artifact %s: %w", target, err)
	}
	return nil
}

func runSCPOnce(cfg spec.CheckConfig, target Target, localPath, remotePath string) error {
	args := append([]string{}, cfg.SSH.Options...)
	destination := targetControlAddress(target)
	if strings.TrimSpace(cfg.SSH.User) != "" {
		destination = cfg.SSH.User + "@" + destination
	}
	args = append(args, localPath, destination+":"+remotePath)
	cmd := exec.Command("scp", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("scp to %s: %w\n%s", destination, err, stderr.String())
	}
	return nil
}

func runSSHCommandOnce(cfg spec.CheckConfig, target Target, remoteCommand string) (string, error) {
	args := append([]string{}, cfg.SSH.Options...)
	destination := targetControlAddress(target)
	if strings.TrimSpace(cfg.SSH.User) != "" {
		destination = cfg.SSH.User + "@" + destination
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
