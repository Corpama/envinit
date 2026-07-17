package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"envinit/internal/spec"
)

const (
	applyProgressFile    = "/var/lib/envinit/apply-progress.json"
	applyProgressVersion = 1
)

type applyProgress struct {
	Version             int      `json:"version"`
	HostID              string   `json:"host_id"`
	ConfigurationSHA256 string   `json:"configuration_sha256"`
	CompletedStages     []string `json:"completed_stages"`
	CurrentStage        string   `json:"current_stage,omitempty"`
	LastError           string   `json:"last_error,omitempty"`
	UpdatedAt           string   `json:"updated_at"`
}

func (a *App) runStagesWithProgress() error {
	fingerprint, err := a.applyConfigurationFingerprint()
	if err != nil {
		return err
	}
	if a.ResetApplyProgress {
		if err := a.removeApplyProgress(); err != nil {
			return err
		}
		a.logf("cleared saved apply progress because --restart was requested")
	}
	progress, err := a.loadApplyProgress(fingerprint)
	if err != nil {
		return err
	}
	completed := make(map[string]bool, len(progress.CompletedStages))
	for _, stage := range progress.CompletedStages {
		completed[stage] = true
	}

	for _, stage := range stageOrder {
		if completed[stage] {
			a.logf("resume: skip completed stage %s", stage)
			continue
		}
		progress.CurrentStage = stage
		progress.LastError = ""
		if err := a.saveApplyProgress(progress); err != nil {
			return err
		}
		if err := a.executeStage(stage); err != nil {
			progress.LastError = err.Error()
			if saveErr := a.saveApplyProgress(progress); saveErr != nil {
				return fmt.Errorf("stage %s failed: %v; additionally failed to save apply progress: %w", stage, err, saveErr)
			}
			return fmt.Errorf("stage %s failed; progress saved and the next apply will retry this stage: %w", stage, err)
		}
		completed[stage] = true
		progress.CompletedStages = orderedCompletedStages(completed)
		progress.CurrentStage = ""
		progress.LastError = ""
		if err := a.saveApplyProgress(progress); err != nil {
			return fmt.Errorf("stage %s completed but saving apply progress failed: %w", stage, err)
		}
	}
	a.logf("apply progress: all stages are complete; use --restart to run the full workflow again")
	return nil
}

func (a *App) loadApplyProgress(fingerprint string) (applyProgress, error) {
	progress := a.newApplyProgress(fingerprint)
	path := a.targetPath(applyProgressFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return progress, nil
	}
	if err != nil {
		return applyProgress{}, fmt.Errorf("read apply progress %s: %w", path, err)
	}
	var saved applyProgress
	if err := json.Unmarshal(data, &saved); err != nil {
		return applyProgress{}, fmt.Errorf("parse apply progress %s: %w; use --restart to discard it", path, err)
	}
	if saved.Version != applyProgressVersion || saved.HostID != progress.HostID || saved.ConfigurationSHA256 != fingerprint {
		a.logf("ignore saved apply progress because the version, host, or resolved configuration changed; start from software")
		return progress, nil
	}
	savedCompleted := make(map[string]bool, len(saved.CompletedStages))
	for _, stage := range saved.CompletedStages {
		savedCompleted[stage] = true
	}
	validCompleted := make([]string, 0, len(saved.CompletedStages))
	for _, stage := range stageOrder {
		if !savedCompleted[stage] {
			break
		}
		validCompleted = append(validCompleted, stage)
	}
	saved.CompletedStages = validCompleted
	if strings.TrimSpace(saved.CurrentStage) != "" {
		a.logf("resume apply from stage %s; completed stages: %s", saved.CurrentStage, strings.Join(saved.CompletedStages, ","))
	} else if len(saved.CompletedStages) > 0 {
		a.logf("resume apply with completed stages: %s", strings.Join(saved.CompletedStages, ","))
	}
	return saved, nil
}

func (a *App) saveApplyProgress(progress applyProgress) error {
	progress.Version = applyProgressVersion
	progress.UpdatedAt = a.applyProgressNow().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(progress, "", "  ")
	if err != nil {
		return fmt.Errorf("encode apply progress: %w", err)
	}
	data = append(data, '\n')
	path := a.targetPath(applyProgressFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create apply progress directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".apply-progress-*.tmp")
	if err != nil {
		return fmt.Errorf("create apply progress temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod apply progress temporary file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write apply progress temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync apply progress temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close apply progress temporary file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace apply progress %s: %w", path, err)
	}
	return nil
}

func (a *App) removeApplyProgress() error {
	path := a.targetPath(applyProgressFile)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove apply progress %s: %w", path, err)
	}
	return nil
}

func (a *App) newApplyProgress(fingerprint string) applyProgress {
	hostID := strings.TrimSpace(a.Machine.HostID)
	if hostID == "" {
		hostID = strings.TrimSpace(a.Machine.Hostname)
	}
	return applyProgress{
		Version:             applyProgressVersion,
		HostID:              hostID,
		ConfigurationSHA256: fingerprint,
		CompletedStages:     []string{},
	}
}

func (a *App) applyConfigurationFingerprint() (string, error) {
	var source any = a.Machine
	if a.hasApplySourceRecord {
		source = a.applySourceRecord
	}
	applyBundle := a.Bundle
	applyBundle.Check = spec.CheckConfig{}
	payload := struct {
		Bundle any `json:"bundle"`
		Source any `json:"inventory_record"`
	}{Bundle: applyBundle, Source: source}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode resolved apply configuration: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (a *App) applyProgressNow() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func orderedCompletedStages(completed map[string]bool) []string {
	out := make([]string, 0, len(completed))
	for _, stage := range stageOrder {
		if completed[stage] {
			out = append(out, stage)
		}
	}
	return out
}
