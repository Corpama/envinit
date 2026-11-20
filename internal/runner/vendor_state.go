package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"
)

// Vendor archives have no common version/health interface. Require an explicit
// target-state check rather than treating installer exit status as verification.
func (a *App) runVendorStage(stage, material string, run func() error) error {
	if a.DryRun || strings.TrimSpace(material) == "" {
		return run()
	}
	check := strings.TrimSpace(a.Bundle.StageChecks[stage])
	f, err := os.Open(material)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	kernel, err := a.unameR()
	if err != nil {
		return err
	}
	devices, err := a.discoverNetDevices()
	if err != nil {
		return err
	}
	input := struct {
		MaterialSHA256, Kernel string
		Devices                []netDevice
		Check                  string
		Options                any
	}{hex.EncodeToString(h.Sum(nil)), kernel, devices, check, a.Bundle.Artifacts}
	if check == "" {
		a.logf("%s has no stage_checks.%s; it may run once, but later runs will pause until completion can be verified", stage, stage)
	}
	return a.executeRecorded("vendor/"+stage, input, check, run)
}
