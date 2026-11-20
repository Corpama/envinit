package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

var requiredKernelCmdline = []string{"rw", "biosdevname=0", "iommu=pt", "mitigations=off", "nokaslr"}

const kernelBuildStateFile = "/var/lib/envinit/kernel-boot-config.json"

type kernelBuildState struct {
	Version     int    `json:"version"`
	Fingerprint string `json:"fingerprint"`
}

func (a *App) runKernelStage() error {
	systemPath := a.targetPath(grubFile)
	changed, content, err := ensureGrubCmdline(systemPath, requiredKernelCmdline)
	if err != nil {
		return err
	}
	if changed {
		if err := a.writeManagedFile(grubFile, content, 0o644); err != nil {
			return err
		}
	} else {
		a.logf("grub cmdline already satisfied")
	}

	command := ""
	args := []string{}
	if _, err := exec.LookPath("update-grub"); err == nil {
		command = "update-grub"
	} else if _, err := exec.LookPath("grub2-mkconfig"); err == nil {
		command = "grub2-mkconfig"
		args = []string{"-o", "/boot/grub2/grub.cfg"}
	} else {
		return errors.New("neither update-grub nor grub2-mkconfig found")
	}
	sum := sha256.Sum256([]byte(content + "\x00" + command + "\x00" + fmt.Sprint(args)))
	fingerprint := hex.EncodeToString(sum[:])
	statePath := a.targetPath(kernelBuildStateFile)
	if raw, err := os.ReadFile(statePath); err == nil {
		var state kernelBuildState
		if err := json.Unmarshal(raw, &state); err != nil {
			return fmt.Errorf("needs attention: invalid kernel boot configuration state: %w", err)
		}
		if state.Version == 1 && state.Fingerprint == fingerprint {
			a.logf("kernel boot configuration already generated; skip %s", command)
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := a.runCmd("", nil, command, args...); err != nil {
		return err
	}
	if a.DryRun {
		return nil
	}
	data, err := json.MarshalIndent(kernelBuildState{Version: 1, Fingerprint: fingerprint}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		return err
	}
	return atomicWriteFile(statePath, append(data, '\n'), 0o600)
}
