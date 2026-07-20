package checker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"envinit/internal/spec"
)

func runCommand(cfg spec.CheckConfig, target Target, remoteCommand string) (string, error) {
	return runCommandStreamingContext(context.Background(), cfg, target, remoteCommand, nil, nil)
}

func runCommandStreaming(cfg spec.CheckConfig, target Target, remoteCommand string, liveStdout, liveStderr io.Writer) (string, error) {
	return runCommandStreamingContext(context.Background(), cfg, target, remoteCommand, liveStdout, liveStderr)
}

func runCheckCommand(opts Options, target Target, remoteCommand string) (string, error) {
	if err := checkCancellationError(opts); err != nil {
		return "", err
	}
	if opts.CommandRunner != nil {
		output, err := opts.CommandRunner(opts.Bundle.Check, target, remoteCommand)
		if cancelErr := checkCancellationError(opts); cancelErr != nil {
			return output, cancelErr
		}
		return output, err
	}
	output, err := runCommandStreamingContext(checkContext(opts), opts.Bundle.Check, target, remoteCommand, nil, nil)
	if checkContext(opts).Err() != nil {
		return output, checkCancellationError(opts)
	}
	return output, err
}

func runCheckCommandStreaming(opts Options, target Target, remoteCommand string, liveStdout, liveStderr io.Writer) (string, error) {
	output, err := runCommandStreamingContext(checkContext(opts), opts.Bundle.Check, target, remoteCommand, liveStdout, liveStderr)
	if checkContext(opts).Err() != nil {
		return output, checkCancellationError(opts)
	}
	return output, err
}

func runCommandStreamingContext(ctx context.Context, cfg spec.CheckConfig, target Target, remoteCommand string, liveStdout, liveStderr io.Writer) (string, error) {
	if target.Local {
		cmd := newCancelableCommand(ctx, "sh", "-c", remoteCommand)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = captureWriter(&stdout, liveStdout)
		cmd.Stderr = captureWriter(&stderr, liveStderr)
		if err := cmd.Run(); err != nil {
			return stdout.String(), fmt.Errorf("local command on %s: %w\n%s", target.Name, err, stderr.String())
		}
		return stdout.String(), nil
	}

	var lastStdout string
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		stdout, err := runSSHCommandOnceStreamingContext(ctx, cfg, target, remoteCommand, liveStdout, liveStderr)
		if err == nil {
			return stdout, nil
		}
		lastStdout = stdout
		lastErr = err
		if !isTransientSSHError(err) || attempt == 3 {
			break
		}
		if err := waitContext(ctx, time.Duration(attempt)*500*time.Millisecond); err != nil {
			return lastStdout, err
		}
	}
	return lastStdout, lastErr
}

func captureWriter(capture io.Writer, live io.Writer) io.Writer {
	if live == nil {
		return capture
	}
	return io.MultiWriter(capture, live)
}

func copyFileToTarget(opts Options, target Target, localPath, remotePath string) error {
	if err := checkCancellationError(opts); err != nil {
		return err
	}
	if opts.FileCopier != nil {
		return opts.FileCopier(opts.Bundle.Check, target, localPath, remotePath)
	}
	if target.Local {
		return copyLocalFile(localPath, remotePath)
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if err := runSCPOnceContext(checkContext(opts), opts.Bundle.Check, target, localPath, remotePath); err == nil {
			return nil
		} else {
			if cancelErr := checkCancellationError(opts); cancelErr != nil {
				return cancelErr
			}
			lastErr = err
			if !isTransientSSHError(err) || attempt == 3 {
				break
			}
			if err := waitContext(checkContext(opts), time.Duration(attempt)*500*time.Millisecond); err != nil {
				return checkCancellationError(opts)
			}
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
	return runSCPOnceContext(context.Background(), cfg, target, localPath, remotePath)
}

func runSCPOnceContext(ctx context.Context, cfg spec.CheckConfig, target Target, localPath, remotePath string) error {
	args := append([]string{}, cfg.SSH.Options...)
	destination := targetControlAddress(target)
	if strings.TrimSpace(cfg.SSH.User) != "" {
		destination = cfg.SSH.User + "@" + destination
	}
	args = append(args, localPath, destination+":"+remotePath)
	cmd := newCancelableCommand(ctx, "scp", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("scp to %s: %w\n%s", destination, err, stderr.String())
	}
	return nil
}

func runSSHCommandOnce(cfg spec.CheckConfig, target Target, remoteCommand string) (string, error) {
	return runSSHCommandOnceStreaming(cfg, target, remoteCommand, nil, nil)
}

func runSSHCommandOnceStreaming(cfg spec.CheckConfig, target Target, remoteCommand string, liveStdout, liveStderr io.Writer) (string, error) {
	return runSSHCommandOnceStreamingContext(context.Background(), cfg, target, remoteCommand, liveStdout, liveStderr)
}

func runSSHCommandOnceStreamingContext(ctx context.Context, cfg spec.CheckConfig, target Target, remoteCommand string, liveStdout, liveStderr io.Writer) (string, error) {
	args := append([]string{}, cfg.SSH.Options...)
	destination := targetControlAddress(target)
	if strings.TrimSpace(cfg.SSH.User) != "" {
		destination = cfg.SSH.User + "@" + destination
	}
	args = append(args, destination, remoteCommand)
	cmd := newCancelableCommand(ctx, "ssh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = captureWriter(&stdout, liveStdout)
	cmd.Stderr = captureWriter(&stderr, liveStderr)
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("ssh %s: %w\n%s", destination, err, stderr.String())
	}
	return stdout.String(), nil
}

var errCheckCanceled = errors.New("check canceled by user")

func checkContext(opts Options) context.Context {
	if opts.Context != nil {
		return opts.Context
	}
	return context.Background()
}

func checkCancellationError(opts Options) error {
	ctx := checkContext(opts)
	if err := ctx.Err(); err != nil {
		cause := context.Cause(ctx)
		if errors.Is(cause, errCheckItemAborted) || errors.Is(cause, errCheckStageAborted) {
			return cause
		}
		return fmt.Errorf("%w: %w", errCheckCanceled, err)
	}
	return nil
}

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func newCancelableCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	return cmd
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
