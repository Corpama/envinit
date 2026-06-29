package runner

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"os/exec"
	"path/filepath"
)

func (a *App) targetPath(systemPath string) string {
	return resolveTargetPath(a.Root, systemPath)
}

func (a *App) logf(format string, args ...any) {
	if a.Output == nil {
		return
	}
	fmt.Fprintf(a.Output, format+"\n", args...)
}

func (a *App) runCmd(dir string, env map[string]string, name string, args ...string) error {
	rendered := strings.TrimSpace(strings.Join(append([]string{name}, args...), " "))
	if dir != "" {
		a.logf("run (dir=%s): %s", dir, rendered)
	} else {
		a.logf("run: %s", rendered)
	}
	if a.DryRun {
		return nil
	}
	cmd := exec.Command(name, args...)
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
		return fmt.Errorf("run %s: %w", rendered, err)
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
	cmd := exec.Command(name, args...)
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

func (a *App) captureCmd(dir string, env map[string]string, name string, args ...string) (string, error) {
	rendered := strings.TrimSpace(strings.Join(append([]string{name}, args...), " "))
	a.logf("capture: %s", rendered)
	if a.DryRun {
		return "", nil
	}
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
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
