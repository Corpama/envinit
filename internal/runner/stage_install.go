package runner

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"envinit/internal/spec"

	"os/exec"
	"path/filepath"
)

func (a *App) runAPTStage() error {
	if a.usesYum() {
		return a.runYumStage()
	}
	packages, err := a.requiredPackages()
	if err != nil {
		return err
	}
	if offlineRepoEnabled(a.Bundle.OfflineAPT) {
		if err := a.prepareOfflineAPTMaterials(); err != nil {
			return err
		}
		if a.Bundle.DisableExistingAptSources() {
			if err := a.disableExistingAptSources(); err != nil {
				return err
			}
		}
		content := strings.Join(a.renderOfflineAPTEntries(), "\n") + "\n"
		if err := a.writeManagedFile(a.Bundle.OfflineAPT.TargetFile, content, 0o644); err != nil {
			return err
		}
	}

	if len(packages) == 0 && !offlineRepoEnabled(a.Bundle.OfflineAPT) {
		a.logf("skip software: no offline apt entries and no packages configured")
		return nil
	}
	if err := a.runCmd("", nil, "apt-get", "update"); err != nil {
		return err
	}
	if len(packages) == 0 {
		return nil
	}
	args := append([]string{"install", "-y"}, packages...)
	return a.runCmd("", nil, "apt-get", args...)
}

func (a *App) prepareOfflineAPTMaterials() error {
	source := strings.TrimSpace(a.Bundle.OfflineAPT.MaterialPath)
	target := strings.TrimSpace(a.Bundle.OfflineAPT.CopyTo)
	if source == "" || target == "" {
		return nil
	}
	return a.copyMaterial(source, target)
}

func (a *App) renderOfflineAPTEntries() []string {
	return a.renderOfflineRepoEntries(a.Bundle.OfflineAPT)
}

func (a *App) renderOfflineRepoEntries(repo spec.OfflineAPTConfig) []string {
	entries := make([]string, 0, len(repo.Entries))
	source := strings.TrimSpace(repo.MaterialPath)
	target := strings.TrimSpace(repo.CopyTo)
	replacer := strings.NewReplacer(
		"{{offline_apt_target}}", target,
		"{{ offline_apt_target }}", target,
		"{{offline_repo_target}}", target,
		"{{ offline_repo_target }}", target,
	)
	for _, entry := range repo.Entries {
		rendered := strings.TrimSpace(replacer.Replace(entry))
		if source != "" && target != "" {
			rendered = strings.ReplaceAll(rendered, source, target)
		}
		if rendered == "" {
			continue
		}
		entries = append(entries, rendered)
	}
	return entries
}

func (a *App) runYumStage() error {
	packages, err := a.requiredPackages()
	if err != nil {
		return err
	}
	repo := a.offlineRepoConfig()
	if offlineRepoEnabled(repo) {
		if err := a.prepareOfflineRepoMaterials(repo); err != nil {
			return err
		}
		if a.Bundle.DisableExistingRepos() {
			if err := a.disableExistingYumRepos(repo.TargetFile); err != nil {
				return err
			}
		}
		content := strings.Join(a.renderOfflineRepoEntries(repo), "\n") + "\n"
		if err := a.writeManagedFile(repo.TargetFile, content, 0o644); err != nil {
			return err
		}
	}
	if len(packages) == 0 && !offlineRepoEnabled(repo) {
		a.logf("skip software: no offline yum repo entries and no packages configured")
		return nil
	}
	if err := a.runCmd("", nil, "yum", "makecache"); err != nil {
		return err
	}
	if len(packages) == 0 {
		return nil
	}
	args := append([]string{"install", "-y"}, packages...)
	return a.runCmd("", nil, "yum", args...)
}

func offlineRepoEnabled(repo spec.OfflineAPTConfig) bool {
	return repo.Enabled && (len(repo.Entries) > 0 || strings.TrimSpace(repo.MaterialPath) != "")
}

func (a *App) prepareOfflineRepoMaterials(repo spec.OfflineAPTConfig) error {
	source := strings.TrimSpace(repo.MaterialPath)
	target := strings.TrimSpace(repo.CopyTo)
	if source == "" || target == "" {
		return nil
	}
	return a.copyMaterial(source, target)
}

func (a *App) runOFEDStage() error {
	archive := strings.TrimSpace(a.Bundle.Artifacts.OFEDArchive)
	if archive == "" {
		a.logf("skip ofed: ofed_archive not configured")
		return nil
	}
	if err := a.ensureOFEDPrerequisites(); err != nil {
		return err
	}

	extractDir := filepath.Join(a.Bundle.Artifacts.WorkDir, "ofed-"+a.now().Format("20060102-150405"))
	if !a.DryRun {
		if err := os.MkdirAll(extractDir, 0o755); err != nil {
			return fmt.Errorf("create ofed work dir: %w", err)
		}
	}
	if err := a.extractArchive(archive, extractDir); err != nil {
		return err
	}

	installDir, err := findDirWithFiles(extractDir, "mlnxofedinstall")
	if err != nil {
		return fmt.Errorf("locate mlnxofedinstall: %w", err)
	}

	kernel, err := a.unameR()
	if err != nil {
		return err
	}

	return a.runCmd(
		installDir,
		nil,
		"./mlnxofedinstall",
		"--without-fw-update",
		"--add-kernel-support",
		"-k", kernel,
		"--skip-distro-check",
		"--force",
	)
}

func (a *App) ensureOFEDPrerequisites() error {
	packages, err := a.ofedPrerequisitePackages()
	if err != nil {
		return err
	}
	if len(packages) == 0 {
		return nil
	}
	if a.DryRun {
		a.logf("dry-run: would ensure OFED prerequisite packages before install: %s", strings.Join(packages, " "))
		return nil
	}
	missing, err := a.missingOFEDPrerequisitePackages(packages)
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		a.logf("OFED prerequisite packages are already installed: %s", strings.Join(packages, " "))
		return nil
	}
	a.logf("install missing OFED prerequisite packages: %s", strings.Join(missing, " "))
	if a.usesYum() {
		args := append([]string{"install", "-y"}, missing...)
		return a.runCmd("", nil, "yum", args...)
	}
	if err := a.runCmd("", nil, "apt-get", "update"); err != nil {
		return err
	}
	args := append([]string{"install", "-y"}, missing...)
	return a.runCmd("", nil, "apt-get", args...)
}

func (a *App) ofedPrerequisitePackages() ([]string, error) {
	if a.usesYum() {
		return []string{"elfutils-devel"}, nil
	}
	unameR, err := a.unameR()
	if err != nil {
		return nil, err
	}
	return []string{
		expandUnameTemplate("linux-headers-{{uname_r}}", unameR),
		"build-essential",
		"debhelper",
		"fakeroot",
	}, nil
}

func (a *App) missingOFEDPrerequisitePackages(packages []string) ([]string, error) {
	missing := make([]string, 0, len(packages))
	for _, pkg := range packages {
		var err error
		if a.usesYum() {
			_, err = a.captureCmd("", nil, "rpm", "-q", pkg)
		} else {
			_, err = a.captureCmd("", nil, "dpkg", "-s", pkg)
		}
		if err != nil {
			missing = append(missing, pkg)
		}
	}
	return missing, nil
}

func (a *App) runXREStage() error {
	installer := strings.TrimSpace(a.Bundle.Artifacts.XREInstaller)
	if installer == "" {
		a.logf("skip xre: xre_installer not configured")
		return nil
	}
	cardModel, err := normalizeXRECardModel(a.Bundle.XRE.CardModel)
	if err != nil {
		return err
	}
	unameR, err := a.unameR()
	if err != nil {
		return err
	}
	args := append([]string{installer}, a.Bundle.Artifacts.XREArgs...)
	env := map[string]string{
		"KERNELDIR": a.kernelHeadersDir(unameR),
	}
	if err := a.runCmd("", env, "bash", args...); err != nil {
		return err
	}
	if err := a.runCmdAllowFailure("", nil, "bash", "-lc", `cat /proc/kunlun/version | grep KUNLUN | awk '{print $10}'`); err != nil {
		return err
	}
	if err := a.runCmd("", nil, "sleep", "10"); err != nil {
		return err
	}
	return a.configureXRECard(cardModel)
}

func (a *App) configureXRECard(cardModel string) error {
	if cardModel == xreCardModelP900 {
		a.logf("skip xre card tuning: card_model=%s", cardModel)
		return nil
	}
	if a.DryRun {
		a.logf("dry-run: would run xpu-smi -q and configure %s only if all P800 cards are VD", kunlunModprobeFile)
		return nil
	}
	output, err := a.captureCmd("", nil, "xpu-smi", "-q")
	if err != nil {
		return err
	}
	variant, partNumbers, err := classifyP800PartNumbers(output)
	if err != nil {
		return err
	}
	a.logf("P800 XPU variant: %s, part numbers: %s", variant, strings.Join(partNumbers, ","))
	if variant == "VC" {
		a.logf("keep default %s for P800 VC", kunlunModprobeFile)
		return nil
	}
	if err := a.writeManagedFile(kunlunModprobeFile, renderP800VDKunlunModprobe(), 0o644); err != nil {
		return err
	}
	return a.reloadKunlunModulesSerially()
}

func (a *App) reloadKunlunModulesSerially() error {
	commands := [][]string{
		{"bash", "-c", `command -v lsof >/dev/null || { echo "lsof is required to reload kunlun modules safely" >&2; exit 1; }; pids="$(lsof /dev/xpu* 2>/dev/null | awk 'NR > 1 {print $2}')"; if [ -n "$pids" ]; then kill -9 $pids; fi`},
		{"rmmod", "kunlun_peermem"},
		{"rmmod", "kunlun"},
		{"modprobe", "kunlun"},
		{"modprobe", "kunlun_peermem"},
	}
	for _, command := range commands {
		if err := a.runCmd("", nil, command[0], command[1:]...); err != nil {
			return err
		}
	}
	return nil
}

func normalizeXRECardModel(value string) (string, error) {
	cardModel := strings.ToUpper(strings.TrimSpace(value))
	switch cardModel {
	case xreCardModelP800, xreCardModelP900:
		return cardModel, nil
	case "":
		return "", errors.New("xre.card_model is required when xre_installer is configured; supported values: P800, P900")
	default:
		return "", fmt.Errorf("unsupported xre.card_model %q; supported values: P800, P900", value)
	}
}

func classifyP800PartNumbers(output string) (string, []string, error) {
	partNumbers := make([]string, 0)
	var variant string
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.Contains(line, "XPU Part Number") {
			continue
		}
		_, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(value) == "" {
			return "", nil, fmt.Errorf("invalid XPU Part Number line: %q", line)
		}
		partNumber := strings.TrimSpace(value)
		currentVariant := ""
		switch partNumber {
		case p800PartNumberVC:
			currentVariant = "VC"
		case p800PartNumberVD:
			currentVariant = "VD"
		default:
			return "", nil, fmt.Errorf("unknown P800 XPU Part Number %q", partNumber)
		}
		if variant != "" && variant != currentVariant {
			return "", nil, fmt.Errorf("mixed P800 XPU variants detected: %s and %s", variant, currentVariant)
		}
		variant = currentVariant
		partNumbers = append(partNumbers, partNumber)
	}
	if err := scanner.Err(); err != nil {
		return "", nil, fmt.Errorf("parse xpu-smi output: %w", err)
	}
	if len(partNumbers) == 0 {
		return "", nil, errors.New("xpu-smi -q did not report any XPU Part Number")
	}
	return variant, partNumbers, nil
}

func renderP800VDKunlunModprobe() string {
	return "options kunlun KLreg_RegistryDwords=\"RmDisableSMMU=1;C2CHighSpeed=1\"\n" +
		"install kunlun /sbin/modprobe --ignore-install kunlun && /sbin/modprobe kunlun-peermem\n"
}

func (a *App) runXDRStage() error {
	archive := strings.TrimSpace(a.Bundle.Artifacts.XDRArchive)
	if archive == "" {
		a.logf("skip xdr: xdr_archive not configured")
		return nil
	}

	unameR, err := a.unameR()
	if err != nil {
		return err
	}
	extractDir := filepath.Join(a.Bundle.Artifacts.WorkDir, "xdr-"+a.now().Format("20060102-150405"))
	if !a.DryRun {
		if err := os.MkdirAll(extractDir, 0o755); err != nil {
			return fmt.Errorf("create xdr work dir: %w", err)
		}
	}
	if err := a.extractArchive(archive, extractDir); err != nil {
		return err
	}

	buildDir, err := findDirWithFiles(extractDir, "build.sh", "install.sh")
	if err != nil {
		return fmt.Errorf("locate xdr build directory: %w", err)
	}

	env := map[string]string{
		"KERNELDIR": a.kernelHeadersDir(unameR),
	}
	if err := a.runCmd(buildDir, env, "./build.sh"); err != nil {
		return err
	}
	if err := a.removeFileIfExists(filepath.Join("/lib/modules", unameR, "extra", "xdr.ko")); err != nil {
		return err
	}
	_ = a.runCmd("", nil, "rmmod", "xdr")
	if err := a.runCmd("", nil, "depmod"); err != nil {
		return err
	}
	if err := a.refreshInitramfs(); err != nil {
		return err
	}
	if err := a.runCmd(buildDir, env, "./install.sh"); err != nil {
		return err
	}
	if err := a.runCmdAllowFailure("", nil, "cat", "/proc/xdr/version"); err != nil {
		return err
	}
	return a.runCmdAllowFailure("", nil, "bash", "-lc", `dmesg -T | grep 'XDR disabled'`)
}

func (a *App) refreshInitramfs() error {
	if _, err := exec.LookPath("dracut"); err == nil {
		return a.runCmd("", nil, "dracut", "-f")
	}
	if _, err := exec.LookPath("update-initramfs"); err == nil {
		return a.runCmd("", nil, "update-initramfs", "-u")
	}
	return errors.New("neither dracut nor update-initramfs found in PATH")
}

func (a *App) runFirmwareStage() error {
	archive := strings.TrimSpace(a.Bundle.Artifacts.FirmwareArchive)
	if archive == "" {
		a.logf("skip firmware: firmware_archive not configured")
		return nil
	}
	extractDir := filepath.Join(a.Bundle.Artifacts.WorkDir, "firmware-"+a.now().Format("20060102-150405"))
	if !a.DryRun {
		if err := os.MkdirAll(extractDir, 0o755); err != nil {
			return fmt.Errorf("create firmware work dir: %w", err)
		}
	}
	if err := a.extractArchive(archive, extractDir); err != nil {
		return err
	}
	updateDir, err := findDirWithFiles(extractDir, "auto_update.sh")
	if err != nil {
		return fmt.Errorf("locate firmware auto_update.sh: %w", err)
	}
	return a.runCmd(updateDir, nil, "bash", "auto_update.sh")
}

func (a *App) runContainerStage() error {
	if len(a.Bundle.Artifacts.ContainerPackages) == 0 {
		a.logf("skip container: no container packages configured")
		return nil
	}
	return a.installLocalPackages(a.Bundle.Artifacts.ContainerPackages)
}

func (a *App) localPackageInstallDescription(packages []string) string {
	if a.usesYum() {
		return "yum localinstall -y " + strings.Join(packages, " ")
	}
	return "dpkg -i " + strings.Join(packages, " ")
}

func (a *App) installLocalPackages(packages []string) error {
	if a.usesYum() {
		args := append([]string{"localinstall", "-y"}, packages...)
		return a.runCmd("", nil, "yum", args...)
	}
	args := append([]string{"-i"}, packages...)
	return a.runCmd("", nil, "dpkg", args...)
}

func (a *App) runMlxConfigStage() error {
	if !a.Bundle.RDMAExists() {
		a.logf("skip mlxconfig: rdma_mode=off")
		return nil
	}
	if len(a.Bundle.MlxConfig.Settings) == 0 {
		a.logf("skip mlxconfig: no settings configured")
		return nil
	}
	if a.canConfirmNICBindingsForStandaloneStage() {
		if _, err := a.confirmedNICBindings(); err != nil {
			return err
		}
	}
	if err := a.runCmd("", nil, "mst", "start"); err != nil {
		return err
	}
	devices, err := a.mlxconfigDevices()
	if err != nil {
		return err
	}

	keys := make([]string, 0, len(a.Bundle.MlxConfig.Settings))
	for key := range a.Bundle.MlxConfig.Settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, device := range devices {
		queryOut, err := a.captureCmd("", nil, "mlxconfig", "-d", device, "query")
		if err != nil {
			return err
		}
		for _, key := range keys {
			target := a.Bundle.MlxConfig.Settings[key]
			current := parseMlxConfigValue(queryOut, key)
			if current == target {
				a.logf("mlxconfig %s %s already %s", device, key, target)
				continue
			}
			if err := a.runCmd("", nil, "mlxconfig", "-y", "-d", device, "set", fmt.Sprintf("%s=%s", key, target)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *App) runSysctlStage() error {
	if a.canConfirmNICBindingsForStandaloneStage() {
		if _, err := a.confirmedNICBindings(); err != nil {
			return err
		}
	}
	if err := a.ensureSysctlSettings(); err != nil {
		return err
	}
	return a.runCmd("", nil, "sysctl", "-p")
}
