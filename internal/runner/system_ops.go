package runner

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"os/exec"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// probeCommandTimeout bounds read-only probe/query commands (captureCmd and the
// local network probes). These are expected to return within seconds, so a hung
// device query fails fast with a clear error instead of stalling the run.
const probeCommandTimeout = 30 * time.Second

func (a *App) targetPath(systemPath string) string {
	return resolveTargetPath(a.Root, systemPath)
}

// aptNonInteractiveEnv prevents apt/dpkg from opening interactive config
// prompts that would block forever on stdin during unattended installs.
func aptNonInteractiveEnv(extra map[string]string) map[string]string {
	env := map[string]string{
		"DEBIAN_FRONTEND": "noninteractive",
	}
	for key, value := range extra {
		env[key] = value
	}
	return env
}

func (a *App) logf(format string, args ...any) {
	if a.Output == nil {
		return
	}
	fmt.Fprintf(a.Output, format+"\n", args...)
}

// commandContext derives a context bounded by timeout. A zero timeout means no
// deadline (used by long vendor stages and the post stage). The returned cancel
// must always be called.
func commandContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), timeout)
}

// startProcessGroup runs cmd in its own process group and wires cancellation to
// SIGKILL the whole group, so a timeout also reaps any children the command
// spawned (apt, installers, firmware scripts).
func prepareProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &unix.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := unix.Kill(-cmd.Process.Pid, unix.SIGKILL); err != nil {
			if err == unix.ESRCH {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
}

func timeoutError(rendered string, timeout time.Duration, ctx context.Context, err error) error {
	if timeout > 0 && ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("run %s exceeded %s and was terminated: %w", rendered, timeout, err)
	}
	return fmt.Errorf("run %s: %w", rendered, err)
}

func (a *App) runCmd(dir string, env map[string]string, name string, args ...string) error {
	return a.runCmdTimeout(a.currentStageTimeout, dir, env, name, args...)
}

func (a *App) runCmdTimeout(timeout time.Duration, dir string, env map[string]string, name string, args ...string) error {
	rendered := strings.TrimSpace(strings.Join(append([]string{name}, args...), " "))
	if dir != "" {
		a.logf("run (dir=%s): %s", dir, rendered)
	} else {
		a.logf("run: %s", rendered)
	}
	if a.DryRun {
		return nil
	}
	ctx, cancel := commandContext(timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	prepareProcessGroup(cmd)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdout = a.Output
	cmd.Stderr = a.Output
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	if err := cmd.Run(); err != nil {
		return timeoutError(rendered, timeout, ctx, err)
	}
	return nil
}

func (a *App) runCmdAllowFailure(dir string, env map[string]string, name string, args ...string) error {
	rendered := strings.TrimSpace(strings.Join(append([]string{name}, args...), " "))
	if dir != "" {
		a.logf("run (allow failure, dir=%s): %s", dir, rendered)
	} else {
		a.logf("run (allow failure): %s", rendered)
	}
	if a.DryRun {
		return nil
	}
	ctx, cancel := commandContext(a.currentStageTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	prepareProcessGroup(cmd)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdout = a.Output
	cmd.Stderr = a.Output
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	if err := cmd.Run(); err != nil {
		a.logf("non-fatal command failed: %s (%v)", rendered, err)
	}
	return nil
}

// captureCmd runs a read-only command and buffers its output. It always uses the
// short probe timeout because these are queries (ethtool, mlxconfig query,
// rpm -q, systemctl is-*), not long installers.
func (a *App) captureCmd(dir string, env map[string]string, name string, args ...string) (string, error) {
	rendered := strings.TrimSpace(strings.Join(append([]string{name}, args...), " "))
	a.logf("capture: %s", rendered)
	if a.DryRun {
		return "", nil
	}
	ctx, cancel := commandContext(probeCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	prepareProcessGroup(cmd)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("probe command %s exceeded %s and was terminated: %w\n%s", rendered, probeCommandTimeout, err, string(output))
		}
		return "", fmt.Errorf("run %s: %w\n%s", rendered, err, string(output))
	}
	return string(output), nil
}

func (a *App) expandPackages(packages []string) ([]string, error) {
	if len(packages) == 0 {
		return nil, nil
	}
	unameR, err := a.unameR()
	if err != nil {
		return nil, err
	}
	return expandPackagesWithUname(packages, unameR)
}

func kernelHeadersDir(unameR string) string {
	return filepath.Join("/usr/src", "linux-headers-"+unameR)
}

func (a *App) kernelHeadersDir(unameR string) string {
	template := strings.TrimSpace(a.Bundle.Platform.KernelHeadersDir)
	if template == "" {
		return kernelHeadersDir(unameR)
	}
	return expandUnameTemplate(template, unameR)
}

func (a *App) kernelHeadersPackage() string {
	pkg := strings.TrimSpace(a.Bundle.Platform.KernelHeadersPackage)
	if pkg != "" {
		return pkg
	}
	if a.usesYum() {
		return "kernel-devel-{{uname_r}}"
	}
	return "linux-headers-{{uname_r}}"
}

func expandPackagesWithUname(packages []string, unameR string) ([]string, error) {
	out := make([]string, 0, len(packages))
	for _, item := range packages {
		item = strings.TrimSpace(expandUnameTemplate(item, unameR))
		if item == "" {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func expandUnameTemplate(value string, unameR string) string {
	replacer := strings.NewReplacer("{{uname_r}}", unameR, "{{ uname_r }}", unameR)
	return replacer.Replace(value)
}

func (a *App) requiredPackages() ([]string, error) {
	unameR, err := a.unameR()
	if err != nil {
		return nil, err
	}
	return a.requiredPackagesWithUname(unameR)
}

func (a *App) requiredPackagesWithUname(unameR string) ([]string, error) {
	base := append([]string{}, a.Bundle.Packages...)
	if pkg := a.kernelHeadersPackage(); strings.TrimSpace(pkg) != "" && !strings.EqualFold(strings.TrimSpace(pkg), "none") {
		base = append(base, pkg)
	}
	if action, err := a.postPowerAction(); err == nil && action.Action != "" && action.Action != "none" {
		base = append(base, "ipmitool")
	}
	expanded, err := expandPackagesWithUname(base, unameR)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(expanded))
	for _, item := range expanded {
		if seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out, nil
}

func (a *App) unameR() (string, error) {
	cmd := exec.Command("uname", "-r")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("run uname -r: %w\n%s", err, string(out))
	}
	return strings.TrimSpace(string(out)), nil
}

func (a *App) latestGenericKernel() (string, error) {
	pattern := a.targetPath("/boot/vmlinuz-*-generic")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", fmt.Errorf("glob generic kernels: %w", err)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no generic kernels found under %s", pattern)
	}
	sort.Slice(matches, func(i, j int) bool {
		return compareKernelVersions(kernelVersionFromPath(matches[i]), kernelVersionFromPath(matches[j])) < 0
	})
	return kernelVersionFromPath(matches[len(matches)-1]), nil
}

func (a *App) extractArchive(archive string, destination string) error {
	args := []string{"-xf", archive, "-C", destination}
	if strings.HasSuffix(archive, ".tar.gz") || strings.HasSuffix(archive, ".tgz") {
		args = []string{"-xzf", archive, "-C", destination}
	}
	return a.runCmd("", nil, "tar", args...)
}

func kernelVersionFromPath(path string) string {
	base := filepath.Base(path)
	return strings.TrimPrefix(base, "vmlinuz-")
}

func compareKernelVersions(left string, right string) int {
	leftTokens := kernelVersionTokens(left)
	rightTokens := kernelVersionTokens(right)
	for i := 0; i < len(leftTokens) && i < len(rightTokens); i++ {
		if leftTokens[i] == rightTokens[i] {
			continue
		}
		leftNum, leftErr := strconv.Atoi(leftTokens[i])
		rightNum, rightErr := strconv.Atoi(rightTokens[i])
		switch {
		case leftErr == nil && rightErr == nil:
			if leftNum < rightNum {
				return -1
			}
			return 1
		default:
			if leftTokens[i] < rightTokens[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(leftTokens) < len(rightTokens):
		return -1
	case len(leftTokens) > len(rightTokens):
		return 1
	default:
		return 0
	}
}

func kernelVersionTokens(version string) []string {
	fields := strings.FieldsFunc(version, func(r rune) bool {
		return r == '.' || r == '-' || r == '_'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		out = append(out, field)
	}
	return out
}
