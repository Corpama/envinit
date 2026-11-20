package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type operationState struct {
	Version         int             `json:"version"`
	ID              string          `json:"id"`
	Fingerprint     string          `json:"fingerprint"`
	Input           json.RawMessage `json:"input,omitempty"`
	CompletionCheck string          `json:"completion_check,omitempty"`
	Status          string          `json:"status"`
	Verification    string          `json:"verification,omitempty"`
	UpdatedAt       time.Time       `json:"updated_at"`
	Error           string          `json:"error,omitempty"`
}

// executeRecorded is for non-idempotent operations. An interrupted command is
// deliberately not replayed without a successful completion check.
func (a *App) executeRecorded(id string, input any, check string, run func() error) error {
	if a.DryRun {
		a.logf("dry-run: recorded operation %s", id)
		return run()
	}
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	key := sha256.Sum256([]byte(id))
	path := a.targetPath(filepath.Join("/var/lib/envinit/operations", hex.EncodeToString(key[:])+".json"))
	state := operationState{
		Version: 1, ID: id, Fingerprint: hex.EncodeToString(sum[:]), Input: append(json.RawMessage(nil), data...),
		CompletionCheck: check, Status: "running", UpdatedAt: time.Now().UTC(),
	}
	var previous operationState
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &previous); err != nil {
			return fmt.Errorf("operation %s has unreadable state: %w", id, err)
		}
		if previous.Version != 1 || previous.ID != id {
			return fmt.Errorf("operation %s state identity mismatch", id)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	verified := false
	if check != "" {
		_, err := a.captureCmd("", nil, "bash", "-lc", check)
		verified = err == nil
	}
	if verified {
		state.Status = "verified"
		state.Verification = "completion check passed before execution"
		a.logf("already satisfied: %s (completion check)", id)
		return saveOperationState(path, state)
	}
	if previous.ID != "" && previous.Status != "verified" {
		return fmt.Errorf("needs attention: operation %s was %s; completion is not verified; inspect the operation and provide a completion check before retrying (state: %s)", id, previous.Status, path)
	}
	if err := saveOperationState(path, state); err != nil {
		return err
	}
	if err := run(); err != nil {
		state.Status, state.Error = "unknown", err.Error()
		if saveErr := saveOperationState(path, state); saveErr != nil {
			return fmt.Errorf("%w; saving operation state: %v", err, saveErr)
		}
		return err
	}
	state.Status = "unverified"
	if check != "" {
		if _, err := a.captureCmd("", nil, "bash", "-lc", check); err != nil {
			state.Error = err.Error()
			if saveErr := saveOperationState(path, state); saveErr != nil {
				return saveErr
			}
			return fmt.Errorf("operation %s returned success but completion check failed: %w", id, err)
		}
		state.Status = "verified"
		state.Verification = "completion check passed after execution"
	}
	return saveOperationState(path, state)
}

func saveOperationState(path string, state operationState) error {
	state.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicWriteFile(path, append(data, '\n'), 0o600)
}
