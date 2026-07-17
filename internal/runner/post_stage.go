package runner

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"envinit/internal/spec"
	"path/filepath"
)

type resolvedPowerAction struct {
	Action  string
	Confirm bool
}

func (a *App) runPostStage() error {
	if err := a.installPostPackages(); err != nil {
		return err
	}
	if a.Bundle.RDMAExists() {
		if a.canConfirmNICBindingsForStandaloneStage() {
			if _, err := a.confirmedNICBindings(); err != nil {
				return err
			}
		}
		if err := a.ensurePostBootService(); err != nil {
			return err
		}
	} else {
		a.logf("skip RDMA post-boot service: rdma_mode=off")
	}
	if err := a.runPostTasks(); err != nil {
		return err
	}
	powerAction, err := a.postPowerAction()
	if err != nil {
		return err
	}
	if powerAction.Action == "" || powerAction.Action == "none" {
		a.logf("skip post: post_power_action is none")
		return nil
	}
	if powerAction.Confirm {
		ok, err := a.confirmAction("power " + powerAction.Action)
		if err != nil {
			return err
		}
		if !ok {
			a.logf("skip post: power %s not confirmed", powerAction.Action)
			return nil
		}
	}
	return a.runCmd("", nil, "ipmitool", "power", powerAction.Action)
}

func (a *App) installPostPackages() error {
	for _, pkg := range nonEmpty(a.Bundle.PostPackages) {
		if err := a.installLocalPackages([]string{pkg}); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) runPostTasks() error {
	for idx, task := range a.Bundle.PostTasks {
		if err := a.runPostTask(idx, task); err != nil {
			return err
		}
	}
	return nil
}

func describePostTask(idx int, total int, task spec.PostTask) (string, error) {
	label := fmt.Sprintf("post task %d/%d", idx+1, total)
	if strings.TrimSpace(task.Name) != "" {
		label = fmt.Sprintf("%s (%s)", label, strings.TrimSpace(task.Name))
	}
	switch normalizePostTaskType(task.Type) {
	case "copy":
		if err := requirePostTaskFields(idx, task, "source", "target"); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s: copy %s to %s", label, strings.TrimSpace(task.Source), strings.TrimSpace(task.Target)), nil
	case "cmd":
		if err := requirePostTaskFields(idx, task, "command"); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s: run %s", label, strings.TrimSpace(task.Command)), nil
	case "mv":
		if err := requirePostTaskFields(idx, task, "source", "target"); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s: move %s to %s", label, strings.TrimSpace(task.Source), strings.TrimSpace(task.Target)), nil
	case "rm":
		if err := requirePostTaskFields(idx, task, "path"); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s: remove %s", label, strings.TrimSpace(task.Path)), nil
	case "mkdir":
		if err := requirePostTaskFields(idx, task, "path"); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s: create directory %s", label, strings.TrimSpace(task.Path)), nil
	default:
		return "", fmt.Errorf("post_tasks[%d].type %q is unsupported", idx, task.Type)
	}
}

func (a *App) runPostTask(idx int, task spec.PostTask) error {
	switch normalizePostTaskType(task.Type) {
	case "copy":
		if err := requirePostTaskFields(idx, task, "source", "target"); err != nil {
			return err
		}
		source := strings.TrimSpace(task.Source)
		target := strings.TrimSpace(task.Target)
		if err := a.copyMaterial(source, target); err != nil {
			return err
		}
		if strings.TrimSpace(task.Mode) == "" {
			return nil
		}
		mode, err := parsePostTaskMode(idx, task.Mode)
		if err != nil {
			return err
		}
		return a.chmodPath(target, mode)
	case "cmd":
		if err := requirePostTaskFields(idx, task, "command"); err != nil {
			return err
		}
		return a.runCmd("", nil, "bash", "-lc", strings.TrimSpace(task.Command))
	case "mv":
		if err := requirePostTaskFields(idx, task, "source", "target"); err != nil {
			return err
		}
		return a.movePath(strings.TrimSpace(task.Source), strings.TrimSpace(task.Target))
	case "rm":
		if err := requirePostTaskFields(idx, task, "path"); err != nil {
			return err
		}
		return a.removePath(strings.TrimSpace(task.Path))
	case "mkdir":
		if err := requirePostTaskFields(idx, task, "path"); err != nil {
			return err
		}
		mode := fs.FileMode(0o755)
		if strings.TrimSpace(task.Mode) != "" {
			parsed, err := parsePostTaskMode(idx, task.Mode)
			if err != nil {
				return err
			}
			mode = parsed
		}
		return a.mkdirPath(strings.TrimSpace(task.Path), mode)
	default:
		return fmt.Errorf("post_tasks[%d].type %q is unsupported", idx, task.Type)
	}
}

func normalizePostTaskType(raw string) string {
	return strings.TrimSpace(strings.ToLower(raw))
}

func requirePostTaskFields(idx int, task spec.PostTask, fields ...string) error {
	values := map[string]string{
		"source":  task.Source,
		"target":  task.Target,
		"path":    task.Path,
		"command": task.Command,
	}
	for _, field := range fields {
		if strings.TrimSpace(values[field]) == "" {
			return fmt.Errorf("post_tasks[%d].%s is required", idx, field)
		}
	}
	return nil
}

func parsePostTaskMode(idx int, raw string) (fs.FileMode, error) {
	raw = strings.TrimSpace(raw)
	value, err := strconv.ParseUint(raw, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("post_tasks[%d].mode %q must be an octal file mode such as 0644 or 0755", idx, raw)
	}
	return fs.FileMode(value).Perm(), nil
}

func (a *App) postPowerAction() (resolvedPowerAction, error) {
	action := normalizePowerAction(a.Bundle.PostPowerAction.Action)
	if action == "" {
		return resolvedPowerAction{}, fmt.Errorf("unsupported post_power_action.action %q", a.Bundle.PostPowerAction.Action)
	}
	confirm := false
	if a.Bundle.PostPowerAction.Confirm != nil {
		confirm = *a.Bundle.PostPowerAction.Confirm
	}
	return resolvedPowerAction{Action: action, Confirm: confirm}, nil
}

func normalizePowerAction(raw string) string {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "", "none":
		return "none"
	case "soft", "shutdown":
		return "soft"
	case "off", "power_off", "poweroff":
		return "off"
	case "cycle", "power_cycle", "reboot":
		return "cycle"
	case "reset":
		return "reset"
	case "on", "power_on":
		return "on"
	case "status":
		return "status"
	default:
		return ""
	}
}

func (a *App) ensurePostBootService() error {
	script, err := a.renderPostBootScriptWithExistingCustom()
	if err != nil {
		return err
	}
	if err := a.writeManagedFile(postBootScript, script, 0o755); err != nil {
		return err
	}
	if err := a.writeManagedFile(postBootService, renderPostBootService(), 0o644); err != nil {
		return err
	}
	if err := a.runCmd("", nil, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := a.runCmd("", nil, "systemctl", "enable", filepath.Base(postBootService)); err != nil {
		return err
	}
	return a.runCmd("", nil, "systemctl", "restart", filepath.Base(postBootService))
}

func (a *App) renderPostBootScriptWithExistingCustom() (string, error) {
	custom := defaultPostBootCustomBlock()
	existing, err := os.ReadFile(a.targetPath(postBootScript))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("read %s: %w", postBootScript, err)
	}
	if err == nil {
		custom = extractPostBootCustomBlock(string(existing))
	}
	return renderPostBootScript(a.Machine, custom), nil
}

func (a *App) confirmAction(action string) (bool, error) {
	if a.DryRun {
		a.logf("dry-run: would ask for confirmation before ipmitool %s", action)
		return false, nil
	}
	info, err := os.Stdin.Stat()
	if err != nil {
		return false, fmt.Errorf("check stdin for confirmation: %w", err)
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		a.logf("skip post: stdin is not interactive, so confirmation cannot be collected")
		return false, nil
	}

	fmt.Fprintf(a.Output, "Confirm %s now? Type 'yes' to continue: ", action)
	reader := bufio.NewReader(os.Stdin)
	answer, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read %s confirmation: %w", strings.ReplaceAll(action, " ", "-"), err)
	}
	answer = strings.TrimSpace(strings.ToLower(answer))
	return answer == "yes", nil
}
