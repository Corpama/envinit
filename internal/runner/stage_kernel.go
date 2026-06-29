package runner

import (
	"errors"
	"os/exec"
)

var requiredKernelCmdline = []string{"rw", "biosdevname=0", "iommu=pt", "mitigations=off", "nokaslr"}

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

	if _, err := exec.LookPath("update-grub"); err == nil {
		return a.runCmd("", nil, "update-grub")
	}
	if _, err := exec.LookPath("grub2-mkconfig"); err == nil {
		return a.runCmd("", nil, "grub2-mkconfig", "-o", "/boot/grub2/grub.cfg")
	}
	return errors.New("neither update-grub nor grub2-mkconfig found")
}
