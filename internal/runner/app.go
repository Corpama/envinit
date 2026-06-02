package runner

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"envinit/internal/spec"
)

const (
	netplanDir = "/etc/netplan"
	routeDir   = "/etc/networkd-dispatcher/routable.d"
	udevFile   = "/etc/udev/rules.d/70-persistent-net.rules"
	sysctlFile = "/etc/sysctl.conf"
	grubFile   = "/etc/default/grub"

	kunlunModprobeFile = "/etc/modprobe.d/kunlun.conf"

	postBootScript  = "/usr/local/sbin/kunlun-post-boot.sh"
	postBootService = "/etc/systemd/system/kunlun-post-boot.service"
)

const (
	xreCardModelP800 = "P800"
	xreCardModelP900 = "P900"
	p800PartNumberVC = "B00100300110112"
	p800PartNumberVD = "B00100300110312"
)

var stageOrder = []string{
	"apt",
	"ofed",
	"udev",
	"network",
	"xre",
	"xdr",
	"firmware",
	"container",
	"mlxconfig",
	"sysctl",
	"iommu",
	"post",
}

var knownStages = func() map[string]bool {
	out := map[string]bool{"all": true}
	for _, stage := range stageOrder {
		out[stage] = true
	}
	return out
}()

type App struct {
	Bundle        spec.Bundle
	Machine       spec.MachineConfig
	Root          string
	DryRun        bool
	Stages        map[string]bool
	Output        io.Writer
	HostSpecified bool

	now func() time.Time
}

type interfaceBinding struct {
	Kind        string
	Name        string
	MAC         string
	CurrentName string
}

type netDevice struct {
	Name         string
	MAC          string
	PCI          string
	Driver       string
	PhysPortName string
	DevPort      int
	HasDevPort   bool
}

func New(bundle spec.Bundle, records []spec.MachineRecord, host string, root string, dryRun bool, stages map[string]bool, out io.Writer) (*App, error) {
	if root == "" {
		root = "/"
	}
	ifaceByMAC, localMACs, err := localInterfaceIndex(root)
	if err != nil {
		return nil, err
	}
	record, err := matchMachine(records, host, root, localMACs)
	if err != nil {
		return nil, err
	}

	machine, err := spec.ResolveMachine(bundle, record, ifaceByMAC)
	if err != nil {
		return nil, err
	}

	if out == nil {
		out = io.Discard
	}
	return &App{
		Bundle:        bundle,
		Machine:       machine,
		Root:          root,
		DryRun:        dryRun,
		Stages:        stages,
		Output:        out,
		HostSpecified: strings.TrimSpace(host) != "",
		now:           time.Now,
	}, nil
}

func (a *App) Describe() (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Target machine: %s\n", a.Machine.HostID)
	if a.Machine.Hostname != "" {
		fmt.Fprintf(&b, "Hostname: %s\n", a.Machine.Hostname)
	}
	if line := a.describeHostnameAction(); line != "" {
		fmt.Fprintf(&b, "Hostname action: %s\n", line)
	}
	fmt.Fprintf(&b, "Management network: %s/%d via %s, uplink=%s, members=%s\n",
		a.Machine.MgmtIP,
		a.Machine.MgmtPrefix,
		a.Machine.MgmtGateway,
		a.managementSummaryName(),
		strings.Join(a.Machine.MgmtIfaces, ","),
	)
	for _, item := range a.Machine.RDMA {
		fmt.Fprintf(&b, "RDMA: %s -> %s/%d via %s table %d\n", item.Name, item.IP, item.Prefix, item.Gateway, item.Table)
	}
	if hasAny(a.Machine.MgmtMACs...) {
		fmt.Fprintf(&b, "Management MACs: %s\n", strings.Join(nonEmpty(a.Machine.MgmtMACs), ", "))
	}
	for idx, item := range a.Machine.RDMA {
		if item.MAC != "" {
			fmt.Fprintf(&b, "RDMA%d MAC: %s\n", idx+1, item.MAC)
		}
	}
	fmt.Fprintf(&b, "Stages: %s\n", strings.Join(a.selectedStages(), ", "))
	fmt.Fprintf(&b, "Files to be written:\n")
	for _, file := range a.plannedFiles() {
		fmt.Fprintf(&b, "  - %s\n", file)
	}
	details, err := a.describeStageActions()
	if err != nil {
		return "", err
	}
	if len(details) > 0 {
		fmt.Fprintf(&b, "Detailed actions:\n")
		for _, stage := range a.selectedStages() {
			lines := details[stage]
			if len(lines) == 0 {
				continue
			}
			fmt.Fprintf(&b, "  [%s]\n", stage)
			for _, line := range lines {
				fmt.Fprintf(&b, "    - %s\n", line)
			}
		}
	}
	return b.String(), nil
}

func (a *App) describeStageActions() (map[string][]string, error) {
	out := map[string][]string{}
	for _, stage := range a.selectedStages() {
		lines, err := a.describeStage(stage)
		if err != nil {
			return nil, err
		}
		out[stage] = lines
	}
	return out, nil
}

func (a *App) describeStage(stage string) ([]string, error) {
	switch stage {
	case "network":
		lines := []string{}
		if a.Bundle.Defaults.BackupExistingNetplan {
			lines = append(lines, "backup existing /etc/netplan/*.yaml files except envinit-managed files")
		}
		if len(a.Machine.MgmtIfaces) == 1 {
			lines = append(lines, fmt.Sprintf("write %s with single-interface management config on %s (%s/%d via %s)", filepath.Join(netplanDir, "00-kunlun-bond.yaml"), a.Machine.MgmtIfaces[0], a.Machine.MgmtIP, a.Machine.MgmtPrefix, a.Machine.MgmtGateway))
		} else {
			lines = append(lines, fmt.Sprintf("write %s with bond %s %s over %s (%s/%d via %s)", filepath.Join(netplanDir, "00-kunlun-bond.yaml"), a.Machine.MgmtBondName, a.bondSummary(), strings.Join(a.Machine.MgmtIfaces, ","), a.Machine.MgmtIP, a.Machine.MgmtPrefix, a.Machine.MgmtGateway))
		}
		if !a.Bundle.RDMAExists() {
			lines = append(lines, "skip all RDMA actions because rdma_exsist=false")
		} else if !a.Bundle.RDMAConfigureIPRoute() {
			lines = append(lines, "skip RDMA IP, netplan, route, and policy-rule configuration because rdma_configure_ip_route=false")
		} else {
			for _, item := range a.Machine.RDMA {
				lines = append(lines,
					fmt.Sprintf("write %s with %s/%d mtu %d", filepath.Join(netplanDir, fmt.Sprintf("10-kunlun-%s.yaml", item.Name)), item.IP, item.Prefix, a.Machine.RDMAMTU),
					fmt.Sprintf("write %s for table %d via %s route_cidr=%s priority=%d", filepath.Join(routeDir, fmt.Sprintf("config_rt_%s.sh", item.Name)), item.Table, item.Gateway, a.Machine.RouteCIDR, a.Machine.RoutePriority),
				)
			}
		}
		lines = append(lines, "run netplan generate")
		lines = append(lines, "run netplan apply")
		if a.Bundle.RDMAConfigureIPRoute() {
			for _, item := range a.Machine.RDMA {
				lines = append(lines, fmt.Sprintf("run bash %s", filepath.Join(routeDir, fmt.Sprintf("config_rt_%s.sh", item.Name))))
			}
		}
		if a.Bundle.RDMAExists() {
			for _, item := range a.Machine.RDMA {
				lines = append(lines, fmt.Sprintf("best-effort enable RoCE adaptive routing on %s using ethtool bus-info and mlxreg ROCE_ACCL adaptive_routing_forced_en=0x1", item.Name))
			}
		}
		return lines, nil
	case "udev":
		lines := []string{
			"after OFED, discover RDMA interfaces with missing MACs by mlx5_core PCI order and bind them to configured target names",
			fmt.Sprintf("write %s with persistent names for management and RDMA interfaces", udevFile),
			"run udevadm control --reload-rules",
			"temporarily rename RDMA interfaces to target names for the current boot when needed",
		}
		return lines, nil
	case "apt":
		lines := []string{}
		if a.Bundle.OfflineAPT.Enabled && len(a.Bundle.OfflineAPT.Entries) > 0 {
			if strings.TrimSpace(a.Bundle.OfflineAPT.MaterialPath) != "" && strings.TrimSpace(a.Bundle.OfflineAPT.CopyTo) != "" {
				lines = append(lines, fmt.Sprintf("copy offline apt materials from %s to %s", a.Bundle.OfflineAPT.MaterialPath, a.Bundle.OfflineAPT.CopyTo))
			}
			if a.Bundle.Defaults.DisableExistingAptSources {
				lines = append(lines, "backup existing apt source files except the envinit-managed offline source file")
			}
			lines = append(lines, fmt.Sprintf("write %s with offline apt entries: %s", a.Bundle.OfflineAPT.TargetFile, strings.Join(a.renderOfflineAPTEntries(), " ; ")))
		}
		packages, err := a.requiredPackages()
		if err != nil {
			return nil, err
		}
		if a.Bundle.OfflineAPT.Enabled && len(a.Bundle.OfflineAPT.Entries) > 0 || len(packages) > 0 {
			lines = append(lines, "run apt-get update")
		}
		if len(packages) > 0 {
			lines = append(lines, fmt.Sprintf("run apt-get install -y %s", strings.Join(packages, " ")))
		}
		if len(lines) == 0 {
			lines = append(lines, "no apt actions; no offline apt entries and no packages configured")
		}
		return lines, nil
	case "ofed":
		if strings.TrimSpace(a.Bundle.Artifacts.OFEDArchive) == "" {
			return []string{"skip: ofed_archive not configured"}, nil
		}
		kernel := "<uname -r>"
		if value, err := a.unameR(); err == nil && strings.TrimSpace(value) != "" {
			kernel = value
		}
		return []string{
			fmt.Sprintf("extract %s into %s", a.Bundle.Artifacts.OFEDArchive, filepath.Join(a.Bundle.Artifacts.WorkDir, "ofed-<timestamp>")),
			fmt.Sprintf("run ./mlnxofedinstall --without-fw-update --add-kernel-support -k %s --skip-distro-check --force", kernel),
		}, nil
	case "xre":
		if strings.TrimSpace(a.Bundle.Artifacts.XREInstaller) == "" {
			return []string{"skip: xre_installer not configured"}, nil
		}
		cardModel, err := normalizeXRECardModel(a.Bundle.XRE.CardModel)
		if err != nil {
			return nil, err
		}
		unameR := "<uname -r>"
		if value, err := a.unameR(); err == nil && strings.TrimSpace(value) != "" {
			unameR = value
		}
		parts := append([]string{"bash", a.Bundle.Artifacts.XREInstaller}, a.Bundle.Artifacts.XREArgs...)
		lines := []string{
			fmt.Sprintf("run KERNELDIR=%s %s", kernelHeadersDir(unameR), strings.Join(parts, " ")),
			"run cat /proc/kunlun/version | grep KUNLUN | awk '{print $10}'",
			"run sleep 10",
		}
		if cardModel == xreCardModelP800 {
			lines = append(lines,
				"run xpu-smi -q and classify XPU Part Number: VC="+p800PartNumberVC+", VD="+p800PartNumberVD,
				"for P800 VD only: backup and overwrite "+kunlunModprobeFile+" with C2CHighSpeed=1",
				"for P800 VD only: serially kill processes using /dev/xpu*, unload kunlun_peermem and kunlun, then load kunlun and kunlun_peermem",
			)
		} else {
			lines = append(lines, "skip post-install XRE card tuning for P900")
		}
		return lines, nil
	case "xdr":
		if strings.TrimSpace(a.Bundle.Artifacts.XDRArchive) == "" {
			return []string{"skip: xdr_archive not configured"}, nil
		}
		unameR := "<uname -r>"
		if value, err := a.unameR(); err == nil && strings.TrimSpace(value) != "" {
			unameR = value
		}
		return []string{
			fmt.Sprintf("extract %s into %s", a.Bundle.Artifacts.XDRArchive, filepath.Join(a.Bundle.Artifacts.WorkDir, "xdr-<timestamp>")),
			fmt.Sprintf("run KERNELDIR=%s ./build.sh", filepath.Join("/usr/src", "linux-headers-"+unameR)),
			fmt.Sprintf("remove %s if it exists", filepath.Join("/lib/modules", unameR, "extra", "xdr.ko")),
			"run rmmod xdr if it is loaded",
			"run depmod",
			"run dracut -f, or update-initramfs -u when dracut is unavailable",
			"run ./install.sh",
			"run cat /proc/xdr/version",
			`run dmesg -T | grep 'XDR disabled'`,
		}, nil
	case "firmware":
		if strings.TrimSpace(a.Bundle.Artifacts.FirmwareArchive) == "" {
			return []string{"skip: firmware_archive not configured"}, nil
		}
		return []string{
			fmt.Sprintf("extract %s into %s", a.Bundle.Artifacts.FirmwareArchive, filepath.Join(a.Bundle.Artifacts.WorkDir, "firmware-<timestamp>")),
			"run bash auto_update.sh from the extracted firmware directory",
		}, nil
	case "container":
		if len(a.Bundle.Artifacts.ContainerPackages) == 0 {
			return []string{"skip: no container packages configured"}, nil
		}
		return []string{
			fmt.Sprintf("run dpkg -i %s", strings.Join(a.Bundle.Artifacts.ContainerPackages, " ")),
		}, nil
	case "mlxconfig":
		if !a.Bundle.RDMAExists() {
			return []string{"skip mlxconfig: rdma_exsist=false"}, nil
		}
		if len(a.Bundle.MlxConfig.Settings) == 0 {
			return []string{"skip: no mlxconfig settings configured"}, nil
		}
		keys := make([]string, 0, len(a.Bundle.MlxConfig.Settings))
		for key, value := range a.Bundle.MlxConfig.Settings {
			keys = append(keys, fmt.Sprintf("%s=%s", key, value))
		}
		sort.Strings(keys)
		return []string{
			"run mst start",
			fmt.Sprintf("scan devices matching %s", a.Bundle.MlxConfig.DeviceGlob),
			fmt.Sprintf("query each matched device and set values when needed: %s", strings.Join(keys, ", ")),
		}, nil
	case "sysctl":
		lines := []string{
			fmt.Sprintf("append missing kernel parameters into %s", sysctlFile),
		}
		for _, line := range desiredSysctlLines(a.Machine) {
			lines = append(lines, fmt.Sprintf("ensure %s", line))
		}
		lines = append(lines, "run sysctl -p")
		return lines, nil
	case "iommu":
		return []string{
			fmt.Sprintf("ensure %s contains rw biosdevname=0 iommu=pt mitigations=off", grubFile),
			"run update-grub when available, otherwise run grub2-mkconfig -o /boot/grub2/grub.cfg",
		}, nil
	case "post":
		lines := []string{}
		postPackages := nonEmpty(a.Bundle.PostPackages)
		for idx, pkg := range postPackages {
			lines = append(lines, fmt.Sprintf("install post package %d/%d with dpkg -i %s", idx+1, len(postPackages), pkg))
		}
		if a.Bundle.RDMAExists() {
			lines = append(lines,
				fmt.Sprintf("write %s to set RDMA ring buffers to 8192 and enable RoCE adaptive routing at boot", postBootScript),
				fmt.Sprintf("write and enable %s", postBootService),
			)
		} else {
			lines = append(lines, "skip RDMA post-boot service because rdma_exsist=false")
		}
		postTasks := a.Bundle.PostTasks
		for idx, task := range postTasks {
			line, err := describePostTask(idx, len(postTasks), task)
			if err != nil {
				return nil, err
			}
			lines = append(lines, line)
		}
		powerAction, err := a.postPowerAction()
		if err != nil {
			return nil, err
		}
		if powerAction.Action == "" || powerAction.Action == "none" {
			return append(lines, "skip final power action: post_power_action is none"), nil
		}
		if powerAction.Confirm {
			return append(lines, fmt.Sprintf("ask for confirmation before running ipmitool power %s", powerAction.Action)), nil
		}
		return append(lines, fmt.Sprintf("run ipmitool power %s", powerAction.Action)), nil
	default:
		return nil, fmt.Errorf("unknown stage %q", stage)
	}
}

func (a *App) Apply() error {
	if !a.DryRun {
		if a.Root != "" && a.Root != "/" {
			return errors.New("apply mode does not support --root; use plan for alternate-root previews")
		}
		if os.Geteuid() != 0 {
			return errors.New("apply mode must be run as root")
		}
	}
	if err := a.ensureHostname(); err != nil {
		return err
	}
	for _, stage := range stageOrder {
		if !a.Stages["all"] && !a.Stages[stage] {
			continue
		}
		if err := a.runStage(stage); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) runStage(stage string) error {
	a.logf("==> stage: %s", stage)
	switch stage {
	case "network":
		return a.runNetworkStage()
	case "udev":
		return a.runUdevStage()
	case "apt":
		return a.runAPTStage()
	case "ofed":
		return a.runOFEDStage()
	case "xre":
		return a.runXREStage()
	case "xdr":
		return a.runXDRStage()
	case "firmware":
		return a.runFirmwareStage()
	case "container":
		return a.runContainerStage()
	case "mlxconfig":
		return a.runMlxConfigStage()
	case "sysctl":
		return a.runSysctlStage()
	case "iommu":
		return a.runIOMMUStage()
	case "post":
		return a.runPostStage()
	default:
		return fmt.Errorf("unknown stage %q", stage)
	}
}

func (a *App) describeHostnameAction() string {
	if !a.HostSpecified {
		return ""
	}
	desired := strings.TrimSpace(a.Machine.Hostname)
	if desired == "" {
		return "skip hostname enforcement because the matched inventory row has no hostname"
	}
	current, err := os.Hostname()
	if err != nil {
		return fmt.Sprintf("ensure system hostname is %s", desired)
	}
	if strings.EqualFold(strings.TrimSpace(current), desired) {
		return fmt.Sprintf("system hostname already matches %s", desired)
	}
	return fmt.Sprintf("set system hostname from %s to %s", current, desired)
}

func (a *App) ensureHostname() error {
	if !a.HostSpecified {
		return nil
	}
	desired := strings.TrimSpace(a.Machine.Hostname)
	if desired == "" {
		a.logf("skip hostname enforcement: matched inventory row has no hostname")
		return nil
	}
	current, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("read current hostname: %w", err)
	}
	if strings.EqualFold(strings.TrimSpace(current), desired) {
		a.logf("hostname already %s", desired)
		return nil
	}
	a.logf("set hostname %s -> %s", current, desired)
	if _, err := exec.LookPath("hostnamectl"); err == nil {
		return a.runCmd("", nil, "hostnamectl", "set-hostname", desired)
	}
	if err := a.writeManagedFile("/etc/hostname", desired+"\n", 0o644); err != nil {
		return err
	}
	return a.runCmd("", nil, "hostname", desired)
}

func IsKnownStage(stage string) bool {
	return knownStages[strings.TrimSpace(stage)]
}

func (a *App) runNetworkStage() error {
	if err := a.ensureInterfacesReady(); err != nil {
		return err
	}
	if a.Bundle.Defaults.BackupExistingNetplan {
		if err := a.disableExistingNetplan(); err != nil {
			return err
		}
	}

	mgmtPath := filepath.Join(netplanDir, "00-kunlun-bond.yaml")
	if err := a.writeManagedFile(mgmtPath, renderMgmtNetplan(a.Machine), 0o600); err != nil {
		return err
	}

	for _, item := range a.Machine.RDMA {
		if a.Bundle.RDMAConfigureIPRoute() {
			netplanPath := filepath.Join(netplanDir, fmt.Sprintf("10-kunlun-%s.yaml", item.Name))
			if err := a.writeManagedFile(netplanPath, renderRDMANetplan(item, a.Machine.RDMAMTU), 0o600); err != nil {
				return err
			}

			routePath := filepath.Join(routeDir, fmt.Sprintf("config_rt_%s.sh", item.Name))
			if err := a.writeManagedFile(routePath, renderRouteScript(item, a.Machine.RouteCIDR, a.Machine.RoutePriority), 0o755); err != nil {
				return err
			}
		}
	}

	if err := a.runCmd("", nil, "netplan", "generate"); err != nil {
		return err
	}
	if err := a.runCmd("", nil, "netplan", "apply"); err != nil {
		return err
	}
	if a.Bundle.RDMAConfigureIPRoute() {
		for _, item := range a.Machine.RDMA {
			routePath := filepath.Join(routeDir, fmt.Sprintf("config_rt_%s.sh", item.Name))
			if err := a.runCmd("", nil, "bash", routePath); err != nil {
				return err
			}
		}
	}
	if a.Bundle.RDMAExists() {
		a.enableRoCEAdaptiveRouting()
	}
	return nil
}

func (a *App) enableRoCEAdaptiveRouting() {
	for _, item := range a.Machine.RDMA {
		output, err := a.captureCmd("", nil, "ethtool", "-i", item.Name)
		if err != nil {
			a.logf("skip RoCE adaptive routing on %s: %v", item.Name, err)
			continue
		}
		busInfo, err := parseEthtoolBusInfo(output)
		if err != nil {
			a.logf("skip RoCE adaptive routing on %s: %v", item.Name, err)
			continue
		}
		_ = a.runCmdAllowFailure("", nil, "mlxreg", "-d", busInfo, "--reg_name", "ROCE_ACCL", "--set", "adaptive_routing_forced_en=0x1", "--yes")
	}
}

func parseEthtoolBusInfo(output string) (string, error) {
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "bus-info" {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			break
		}
		return value, nil
	}
	return "", errors.New("bus-info not found in ethtool output")
}

func (a *App) runUdevStage() error {
	bindings, err := a.interfaceBindings()
	if err != nil {
		return err
	}
	content := renderUdevRules(bindings)
	if err := a.writeManagedFile(udevFile, content, 0o644); err != nil {
		return err
	}
	if err := a.runCmd("", nil, "udevadm", "control", "--reload-rules"); err != nil {
		return err
	}
	if err := a.renameRDMATemporarily(bindings); err != nil {
		return err
	}
	a.logf("udev rules reloaded; RDMA interfaces were temporarily renamed for this boot when needed. Reboot is still required for persistent udev naming.")
	return nil
}

func (a *App) runAPTStage() error {
	packages, err := a.requiredPackages()
	if err != nil {
		return err
	}
	if a.Bundle.OfflineAPT.Enabled && len(a.Bundle.OfflineAPT.Entries) > 0 {
		if err := a.prepareOfflineAPTMaterials(); err != nil {
			return err
		}
		if a.Bundle.Defaults.DisableExistingAptSources {
			if err := a.disableExistingAptSources(); err != nil {
				return err
			}
		}
		content := strings.Join(a.renderOfflineAPTEntries(), "\n") + "\n"
		if err := a.writeManagedFile(a.Bundle.OfflineAPT.TargetFile, content, 0o644); err != nil {
			return err
		}
	}

	if len(packages) == 0 && !(a.Bundle.OfflineAPT.Enabled && len(a.Bundle.OfflineAPT.Entries) > 0) {
		a.logf("skip apt: no offline apt entries and no packages configured")
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
	entries := make([]string, 0, len(a.Bundle.OfflineAPT.Entries))
	source := strings.TrimSpace(a.Bundle.OfflineAPT.MaterialPath)
	target := strings.TrimSpace(a.Bundle.OfflineAPT.CopyTo)
	replacer := strings.NewReplacer(
		"{{offline_apt_target}}", target,
		"{{ offline_apt_target }}", target,
	)
	for _, entry := range a.Bundle.OfflineAPT.Entries {
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

func (a *App) runOFEDStage() error {
	archive := strings.TrimSpace(a.Bundle.Artifacts.OFEDArchive)
	if archive == "" {
		a.logf("skip ofed: ofed_archive not configured")
		return nil
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
		"KERNELDIR": kernelHeadersDir(unameR),
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
		"KERNELDIR": filepath.Join("/usr/src", "linux-headers-"+unameR),
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
	args := append([]string{"-i"}, a.Bundle.Artifacts.ContainerPackages...)
	return a.runCmd("", nil, "dpkg", args...)
}

func (a *App) runMlxConfigStage() error {
	if !a.Bundle.RDMAExists() {
		a.logf("skip mlxconfig: rdma_exsist=false")
		return nil
	}
	if len(a.Bundle.MlxConfig.Settings) == 0 {
		a.logf("skip mlxconfig: no settings configured")
		return nil
	}
	if err := a.runCmd("", nil, "mst", "start"); err != nil {
		return err
	}
	devices, err := filepath.Glob(a.Bundle.MlxConfig.DeviceGlob)
	if err != nil {
		return fmt.Errorf("glob mlxconfig devices: %w", err)
	}
	filtered := make([]string, 0, len(devices))
	for _, device := range devices {
		if strings.HasSuffix(device, ".1") || strings.HasSuffix(device, ".2") {
			continue
		}
		filtered = append(filtered, device)
	}
	if len(filtered) == 0 {
		return fmt.Errorf("no mlxconfig devices matched %s", a.Bundle.MlxConfig.DeviceGlob)
	}

	keys := make([]string, 0, len(a.Bundle.MlxConfig.Settings))
	for key := range a.Bundle.MlxConfig.Settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, device := range filtered {
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
	if err := a.ensureSysctlSettings(); err != nil {
		return err
	}
	return a.runCmd("", nil, "sysctl", "-p")
}

func (a *App) runIOMMUStage() error {
	systemPath := a.targetPath(grubFile)
	changed, content, err := ensureGrubCmdline(systemPath, []string{"rw", "biosdevname=0", "iommu=pt", "mitigations=off"})
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

func (a *App) runPostStage() error {
	if err := a.installPostPackages(); err != nil {
		return err
	}
	if a.Bundle.RDMAExists() {
		if err := a.ensurePostBootService(); err != nil {
			return err
		}
	} else {
		a.logf("skip RDMA post-boot service: rdma_exsist=false")
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
		if err := a.runCmd("", nil, "dpkg", "-i", pkg); err != nil {
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

type resolvedPowerAction struct {
	Action  string
	Confirm bool
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
	return a.runCmd("", nil, "systemctl", "enable", filepath.Base(postBootService))
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

func (a *App) interfaceBindings() ([]interfaceBinding, error) {
	deviceByMAC, err := a.netDeviceByMAC()
	if err != nil {
		return nil, err
	}
	var bindings []interfaceBinding
	for idx, name := range a.Machine.MgmtIfaces {
		mac := ""
		if idx < len(a.Machine.MgmtMACs) {
			mac = a.Machine.MgmtMACs[idx]
		}
		if mac == "" {
			mac, err = a.readMAC(name)
			if err != nil {
				return nil, err
			}
		}
		current := name
		if device, ok := deviceByMAC[mac]; ok {
			current = device.Name
		}
		bindings = append(bindings, interfaceBinding{Kind: "mgmt", Name: name, MAC: mac, CurrentName: current})
	}
	rdmaBindings, err := a.rdmaInterfaceBindings(deviceByMAC)
	if err != nil {
		return nil, err
	}
	bindings = append(bindings, rdmaBindings...)
	return bindings, nil
}

func (a *App) rdmaInterfaceBindings(deviceByMAC map[string]netDevice) ([]interfaceBinding, error) {
	if !a.Bundle.RDMAExists() {
		return nil, nil
	}
	needsDiscovery := false
	for _, item := range a.Machine.RDMA {
		if strings.TrimSpace(item.MAC) == "" {
			needsDiscovery = true
			break
		}
	}
	if needsDiscovery {
		return a.discoverRDMABindings()
	}
	out := make([]interfaceBinding, 0, len(a.Machine.RDMA))
	for _, item := range a.Machine.RDMA {
		mac := strings.TrimSpace(item.MAC)
		current := item.Name
		if device, ok := deviceByMAC[mac]; ok {
			current = device.Name
		}
		out = append(out, interfaceBinding{Kind: "rdma", Name: item.Name, MAC: mac, CurrentName: current})
	}
	return out, nil
}

func (a *App) discoverRDMABindings() ([]interfaceBinding, error) {
	devices, err := a.discoverRDMADevices()
	if err != nil {
		return nil, err
	}
	if len(devices) != len(a.Machine.RDMA) {
		return nil, fmt.Errorf("discovered %d RDMA candidates, but %d RDMA target names are configured; candidates: %s; provide rdma*_mac values to avoid ambiguous naming", len(devices), len(a.Machine.RDMA), describeNetDevices(devices))
	}
	out := make([]interfaceBinding, 0, len(a.Machine.RDMA))
	for idx, item := range a.Machine.RDMA {
		device := devices[idx]
		if strings.TrimSpace(item.MAC) != "" && !strings.EqualFold(strings.TrimSpace(item.MAC), device.MAC) {
			return nil, fmt.Errorf("rdma%d_mac %s does not match discovered device %s (%s)", idx+1, item.MAC, device.Name, device.MAC)
		}
		a.Machine.RDMA[idx].MAC = device.MAC
		out = append(out, interfaceBinding{Kind: "rdma", Name: item.Name, MAC: device.MAC, CurrentName: device.Name})
	}
	return out, nil
}

func renderUdevRules(bindings []interfaceBinding) string {
	seen := map[string]bool{}
	var b strings.Builder
	b.WriteString("# This file is autogenerated. Do not edit manually.\n")
	for _, item := range bindings {
		if seen[item.Name] {
			continue
		}
		seen[item.Name] = true
		fmt.Fprintf(&b, "SUBSYSTEM==\"net\", ACTION==\"add\", DRIVERS==\"?*\", ATTR{address}==\"%s\", ATTR{type}==\"1\", NAME=\"%s\"\n", item.MAC, item.Name)
	}
	return b.String()
}

func (a *App) renameRDMATemporarily(bindings []interfaceBinding) error {
	type pendingRename struct {
		current string
		target  string
		temp    string
	}
	var pending []pendingRename
	reservedTemps := map[string]bool{}
	for _, binding := range bindings {
		if binding.Kind != "rdma" {
			continue
		}
		current := strings.TrimSpace(binding.CurrentName)
		target := strings.TrimSpace(binding.Name)
		if current == "" || target == "" || current == target {
			continue
		}
		temp := a.availableTempInterfaceName(len(pending), reservedTemps)
		reservedTemps[temp] = true
		pending = append(pending, pendingRename{
			current: current,
			target:  target,
			temp:    temp,
		})
	}
	if len(pending) == 0 {
		return nil
	}
	for _, item := range pending {
		if err := a.runCmd("", nil, "ip", "link", "set", "dev", item.current, "down"); err != nil {
			return err
		}
		if err := a.runCmd("", nil, "ip", "link", "set", "dev", item.current, "name", item.temp); err != nil {
			return err
		}
	}
	for _, item := range pending {
		if err := a.runCmd("", nil, "ip", "link", "set", "dev", item.temp, "name", item.target); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) availableTempInterfaceName(idx int, reserved map[string]bool) string {
	for suffix := idx; ; suffix++ {
		name := fmt.Sprintf("ei-tmp%d", suffix)
		if !reserved[name] && !a.interfaceExists(name) {
			return name
		}
	}
}

func (a *App) readMAC(iface string) (string, error) {
	path := a.targetPath(filepath.Join("/sys/class/net", iface, "address"))
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read mac for %s: %w", iface, err)
	}
	mac, err := spec.NormalizeMAC(strings.TrimSpace(string(data)))
	if err != nil {
		return "", fmt.Errorf("normalize mac for %s: %w", iface, err)
	}
	return mac, nil
}

func (a *App) netDeviceByMAC() (map[string]netDevice, error) {
	devices, err := a.discoverNetDevices()
	if err != nil {
		return nil, err
	}
	out := make(map[string]netDevice, len(devices))
	for _, device := range devices {
		if device.MAC == "" {
			continue
		}
		out[device.MAC] = device
	}
	return out, nil
}

func (a *App) discoverRDMADevices() ([]netDevice, error) {
	devices, err := a.discoverNetDevices()
	if err != nil {
		return nil, err
	}
	mgmtNames := map[string]bool{}
	for _, name := range a.Machine.MgmtIfaces {
		mgmtNames[name] = true
	}
	mgmtMACs := map[string]bool{}
	for _, mac := range a.Machine.MgmtMACs {
		if strings.TrimSpace(mac) != "" {
			mgmtMACs[strings.TrimSpace(mac)] = true
		}
	}
	out := make([]netDevice, 0, len(devices))
	for _, device := range devices {
		if device.Name == "lo" || device.MAC == "" {
			continue
		}
		if !strings.EqualFold(device.Driver, "mlx5_core") {
			continue
		}
		if mgmtNames[device.Name] || mgmtMACs[device.MAC] {
			continue
		}
		out = append(out, device)
	}
	sortNetDevices(out)
	return out, nil
}

func (a *App) discoverNetDevices() ([]netDevice, error) {
	dir := a.targetPath("/sys/class/net")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	devices := make([]netDevice, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		mac, err := a.readMAC(name)
		if err != nil {
			continue
		}
		devicePath, err := filepath.EvalSymlinks(filepath.Join(dir, name, "device"))
		if err != nil {
			continue
		}
		driver := ""
		if driverPath, err := filepath.EvalSymlinks(filepath.Join(devicePath, "driver")); err == nil {
			driver = filepath.Base(driverPath)
		}
		devPort, hasDevPort := readOptionalInt(filepath.Join(dir, name, "dev_port"))
		devices = append(devices, netDevice{
			Name:         name,
			MAC:          mac,
			PCI:          filepath.Base(devicePath),
			Driver:       driver,
			PhysPortName: readOptionalTrim(filepath.Join(dir, name, "phys_port_name")),
			DevPort:      devPort,
			HasDevPort:   hasDevPort,
		})
	}
	sortNetDevices(devices)
	return devices, nil
}

func sortNetDevices(devices []netDevice) {
	sort.Slice(devices, func(i, j int) bool {
		left, right := devices[i], devices[j]
		if left.PCI != right.PCI {
			return left.PCI < right.PCI
		}
		if left.HasDevPort != right.HasDevPort {
			return left.HasDevPort
		}
		if left.DevPort != right.DevPort {
			return left.DevPort < right.DevPort
		}
		if left.PhysPortName != right.PhysPortName {
			return left.PhysPortName < right.PhysPortName
		}
		return left.Name < right.Name
	})
}

func readOptionalTrim(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func readOptionalInt(path string) (int, bool) {
	raw := readOptionalTrim(path)
	if raw == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return value, true
}

func describeNetDevices(devices []netDevice) string {
	if len(devices) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(devices))
	for _, device := range devices {
		parts = append(parts, fmt.Sprintf("%s(mac=%s,pci=%s,port=%s,dev_port=%d)", device.Name, device.MAC, device.PCI, device.PhysPortName, device.DevPort))
	}
	return strings.Join(parts, ", ")
}

func resolveTargetPath(root string, systemPath string) string {
	if root == "/" || root == "" {
		return systemPath
	}
	clean := strings.TrimPrefix(systemPath, "/")
	return filepath.Join(root, clean)
}

func localInterfaceIndex(root string) (map[string]string, []string, error) {
	dir := resolveTargetPath(root, "/sys/class/net")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]string{}, nil, nil
		}
		return nil, nil, fmt.Errorf("read %s: %w", dir, err)
	}
	ifaceByMAC := map[string]string{}
	localMACs := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(dir, name, "address")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		mac, err := spec.NormalizeMAC(strings.TrimSpace(string(data)))
		if err != nil {
			continue
		}
		if mac == "" {
			continue
		}
		ifaceByMAC[mac] = name
		localMACs = append(localMACs, mac)
	}
	sort.Strings(localMACs)
	return ifaceByMAC, localMACs, nil
}

func (a *App) disableExistingNetplan() error {
	dir := a.targetPath(netplanDir)
	entries, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return fmt.Errorf("scan netplan dir: %w", err)
	}
	managed := map[string]bool{
		filepath.Join(dir, "00-kunlun-bond.yaml"): true,
	}
	for _, item := range a.Machine.RDMA {
		managed[filepath.Join(dir, fmt.Sprintf("10-kunlun-%s.yaml", item.Name))] = true
	}
	for _, entry := range entries {
		if managed[entry] {
			continue
		}
		if err := a.moveToBackup(entry); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) disableExistingAptSources() error {
	patterns := []string{
		a.targetPath("/etc/apt/sources.list"),
		filepath.Join(a.targetPath("/etc/apt/sources.list.d"), "*.list"),
		filepath.Join(a.targetPath("/etc/apt/sources.list.d"), "*.sources"),
	}
	target := a.targetPath(a.Bundle.OfflineAPT.TargetFile)
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return err
		}
		for _, match := range matches {
			if match == target {
				continue
			}
			if err := a.moveToBackup(match); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *App) moveToBackup(path string) error {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	backup := fmt.Sprintf("%s.bak.%s", path, a.now().Format("20060102_150405"))
	a.logf("backup %s -> %s", path, backup)
	if a.DryRun {
		return nil
	}
	return os.Rename(path, backup)
}

func (a *App) writeManagedFile(systemPath string, content string, mode fs.FileMode) error {
	target := a.targetPath(systemPath)
	if !a.DryRun {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(target), err)
		}
	}
	if existing, err := os.ReadFile(target); err == nil {
		if bytes.Equal(existing, []byte(content)) {
			a.logf("unchanged %s", systemPath)
			return nil
		}
		if err := a.moveToBackup(target); err != nil {
			return err
		}
	}
	a.logf("write %s", systemPath)
	if a.DryRun {
		return nil
	}
	if err := os.WriteFile(target, []byte(content), mode); err != nil {
		return fmt.Errorf("write %s: %w", systemPath, err)
	}
	return os.Chmod(target, mode)
}

func (a *App) removeFileIfExists(path string) error {
	a.logf("remove %s if exists", path)
	if a.DryRun {
		return nil
	}
	if err := os.Remove(a.targetPath(path)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

func (a *App) movePath(source string, target string) error {
	sourcePath := a.targetPath(source)
	targetPath := a.targetPath(target)
	a.logf("move %s -> %s", source, target)
	if a.DryRun {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(targetPath), err)
	}
	if _, err := os.Stat(targetPath); err == nil {
		if err := a.moveToBackup(targetPath); err != nil {
			return err
		}
	}
	if err := os.Rename(sourcePath, targetPath); err != nil {
		return fmt.Errorf("move %s to %s: %w", source, target, err)
	}
	return nil
}

func (a *App) removePath(path string) error {
	target := a.targetPath(path)
	a.logf("remove %s", path)
	if a.DryRun {
		return nil
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

func (a *App) mkdirPath(path string, mode fs.FileMode) error {
	target := a.targetPath(path)
	a.logf("mkdir %s", path)
	if a.DryRun {
		return nil
	}
	if err := os.MkdirAll(target, mode); err != nil {
		return fmt.Errorf("mkdir %s: %w", path, err)
	}
	return os.Chmod(target, mode)
}

func (a *App) chmodPath(path string, mode fs.FileMode) error {
	a.logf("chmod %04o %s", mode.Perm(), path)
	if a.DryRun {
		return nil
	}
	if err := os.Chmod(a.targetPath(path), mode); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

func (a *App) copyMaterial(source string, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("stat material %s: %w", source, err)
	}
	targetPath := a.targetPath(target)
	if info.IsDir() {
		return a.copyDirWithBackup(source, targetPath)
	}
	return a.copyFileWithBackup(source, targetPath, info.Mode())
}

func (a *App) copyDirWithBackup(source string, target string) error {
	if _, err := os.Stat(target); err == nil {
		same, err := dirTreesEqual(source, target)
		if err != nil {
			return err
		}
		if same {
			a.logf("unchanged %s", target)
			return nil
		}
		if err := a.moveToBackup(target); err != nil {
			return err
		}
	}
	a.logf("copy %s -> %s", source, target)
	if a.DryRun {
		return nil
	}
	return filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dest := target
		if rel != "." {
			dest = filepath.Join(target, rel)
		}
		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFileContents(path, dest, info.Mode())
	})
}

func (a *App) copyFileWithBackup(source string, target string, mode fs.FileMode) error {
	if _, err := os.Stat(target); err == nil {
		same, err := filesEqual(source, target)
		if err != nil {
			return err
		}
		if same {
			a.logf("unchanged %s", target)
			return nil
		}
		if err := a.moveToBackup(target); err != nil {
			return err
		}
	}
	a.logf("copy %s -> %s", source, target)
	if a.DryRun {
		return nil
	}
	return copyFileContents(source, target, mode)
}

func copyFileContents(source string, target string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Chmod(mode.Perm())
}

func dirTreesEqual(source string, target string) (bool, error) {
	sourceEntries, err := collectTreeEntries(source)
	if err != nil {
		return false, err
	}
	targetEntries, err := collectTreeEntries(target)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if len(sourceEntries) != len(targetEntries) {
		return false, nil
	}
	for rel, sourceEntry := range sourceEntries {
		targetEntry, ok := targetEntries[rel]
		if !ok {
			return false, nil
		}
		if sourceEntry.isDir != targetEntry.isDir {
			return false, nil
		}
		if sourceEntry.isDir {
			continue
		}
		if sourceEntry.size != targetEntry.size {
			return false, nil
		}
		same, err := filesEqual(sourceEntry.path, targetEntry.path)
		if err != nil {
			return false, err
		}
		if !same {
			return false, nil
		}
	}
	return true, nil
}

type treeEntry struct {
	path  string
	isDir bool
	size  int64
}

func collectTreeEntries(root string) (map[string]treeEntry, error) {
	entries := map[string]treeEntry{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entries[rel] = treeEntry{
			path:  path,
			isDir: d.IsDir(),
			size:  info.Size(),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func filesEqual(left string, right string) (bool, error) {
	leftInfo, err := os.Stat(left)
	if err != nil {
		return false, err
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if leftInfo.IsDir() || rightInfo.IsDir() {
		return false, nil
	}
	if leftInfo.Size() != rightInfo.Size() {
		return false, nil
	}
	leftData, err := os.ReadFile(left)
	if err != nil {
		return false, err
	}
	rightData, err := os.ReadFile(right)
	if err != nil {
		return false, err
	}
	return bytes.Equal(leftData, rightData), nil
}

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

func expandPackagesWithUname(packages []string, unameR string) ([]string, error) {
	out := make([]string, 0, len(packages))
	replacer := strings.NewReplacer("{{uname_r}}", unameR, "{{ uname_r }}", unameR)
	for _, item := range packages {
		item = strings.TrimSpace(replacer.Replace(item))
		if item == "" {
			continue
		}
		out = append(out, item)
	}
	return out, nil
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
	base = append(base, "linux-headers-{{uname_r}}")
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

func (a *App) ensureInterfacesReady() error {
	missing := make([]string, 0, len(a.Machine.MgmtIfaces)+len(a.Machine.RDMA))
	for _, iface := range a.Machine.MgmtIfaces {
		if !a.interfaceExists(iface) {
			missing = append(missing, iface)
		}
	}
	for _, item := range a.Machine.RDMA {
		if !a.interfaceExists(item.Name) {
			missing = append(missing, item.Name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("expected interfaces are not present yet: %s; run the ofed and udev stages first so RDMA devices can be discovered and temporarily renamed before the network stage", strings.Join(missing, ", "))
}

func (a *App) interfaceExists(iface string) bool {
	path := a.targetPath(filepath.Join("/sys/class/net", iface))
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func (a *App) selectedStages() []string {
	if a.Stages["all"] {
		return append([]string{}, stageOrder...)
	}
	out := make([]string, 0, len(stageOrder))
	for _, stage := range stageOrder {
		if a.Stages[stage] {
			out = append(out, stage)
		}
	}
	return out
}

func (a *App) plannedFiles() []string {
	files := make([]string, 0, 2+len(a.Machine.RDMA)*2)
	if a.Stages["all"] || a.Stages["network"] {
		files = append(files, filepath.Join(netplanDir, "00-kunlun-bond.yaml"))
		if a.Bundle.RDMAConfigureIPRoute() {
			for _, item := range a.Machine.RDMA {
				files = append(files, filepath.Join(netplanDir, fmt.Sprintf("10-kunlun-%s.yaml", item.Name)))
				files = append(files, filepath.Join(routeDir, fmt.Sprintf("config_rt_%s.sh", item.Name)))
			}
		}
	}
	if a.Stages["all"] || a.Stages["udev"] {
		files = append(files, udevFile)
	}
	if a.Stages["all"] || a.Stages["sysctl"] {
		files = append(files, sysctlFile)
	}
	if (a.Stages["all"] || a.Stages["apt"]) && a.Bundle.OfflineAPT.Enabled && len(a.Bundle.OfflineAPT.Entries) > 0 {
		files = append(files, a.Bundle.OfflineAPT.TargetFile)
	}
	if a.Stages["all"] || a.Stages["iommu"] {
		files = append(files, grubFile)
	}
	if (a.Stages["all"] || a.Stages["xre"]) && strings.EqualFold(strings.TrimSpace(a.Bundle.XRE.CardModel), xreCardModelP800) {
		files = append(files, kunlunModprobeFile)
	}
	if a.HostSpecified && strings.TrimSpace(a.Machine.Hostname) != "" {
		files = append(files, "/etc/hostname")
	}
	if a.Stages["all"] || a.Stages["post"] {
		if a.Bundle.RDMAExists() {
			files = append(files, postBootScript, postBootService)
		}
		for _, task := range a.Bundle.PostTasks {
			switch normalizePostTaskType(task.Type) {
			case "copy", "mv":
				if strings.TrimSpace(task.Target) != "" {
					files = append(files, task.Target)
				}
			case "mkdir":
				if strings.TrimSpace(task.Path) != "" {
					files = append(files, task.Path)
				}
			}
		}
	}
	return files
}

func matchMachine(records []spec.MachineRecord, host string, root string, localMACs []string) (spec.MachineRecord, error) {
	if strings.TrimSpace(host) != "" {
		for _, record := range records {
			if matchesRecord(record, host) {
				return record, nil
			}
		}
		return spec.MachineRecord{}, fmt.Errorf("inventory does not contain host %q", host)
	}

	hostname, _ := os.Hostname()
	localIPs := localIPv4s()

	var hostnameMatches []spec.MachineRecord
	for _, record := range records {
		if hostname != "" && (strings.EqualFold(record.Hostname, hostname) || strings.EqualFold(record.HostID, hostname)) {
			hostnameMatches = append(hostnameMatches, record)
		}
	}
	if len(hostnameMatches) == 1 {
		return hostnameMatches[0], nil
	}

	var ipMatches []spec.MachineRecord
	for _, record := range records {
		if slices.Contains(localIPs, strings.TrimSpace(record.MgmtIP)) {
			ipMatches = append(ipMatches, record)
			continue
		}
		for _, item := range record.RDMA {
			if slices.Contains(localIPs, strings.TrimSpace(item.IP)) {
				ipMatches = append(ipMatches, record)
				break
			}
		}
	}
	if len(ipMatches) == 1 {
		return ipMatches[0], nil
	}

	var macMatches []spec.MachineRecord
	for _, record := range records {
		for _, mac := range recordMACs(record) {
			if slices.Contains(localMACs, mac) {
				macMatches = append(macMatches, record)
				break
			}
		}
	}
	if len(macMatches) == 1 {
		return macMatches[0], nil
	}

	return spec.MachineRecord{}, errors.New("failed to auto-match the current machine; specify --host with a host_id/hostname/mgmt_ip from the inventory, or add MAC addresses to the inventory")
}

func matchesRecord(record spec.MachineRecord, host string) bool {
	host = strings.TrimSpace(host)
	if strings.EqualFold(strings.TrimSpace(record.HostID), host) {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(record.Hostname), host) {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(record.MgmtIP), host) {
		return true
	}
	for _, mac := range recordMACs(record) {
		if strings.EqualFold(mac, host) {
			return true
		}
	}
	return false
}

func localIPv4s() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP == nil || ipNet.IP.To4() == nil {
				continue
			}
			out = append(out, ipNet.IP.String())
		}
	}
	sort.Strings(out)
	return out
}

func renderMgmtNetplan(machine spec.MachineConfig) string {
	var b strings.Builder
	b.WriteString("network:\n")
	b.WriteString("  version: 2\n")
	b.WriteString("  renderer: networkd\n")
	b.WriteString("  ethernets:\n")
	for _, iface := range machine.MgmtIfaces {
		if len(machine.MgmtIfaces) == 1 {
			fmt.Fprintf(&b, "    %s:\n", iface)
			fmt.Fprintf(&b, "      addresses:\n        - %s/%d\n", machine.MgmtIP, machine.MgmtPrefix)
			fmt.Fprintf(&b, "      mtu: %d\n", machine.MgmtMTU)
			writeNameservers(&b, machine.MgmtDNS, "      ")
			b.WriteString("      routes:\n")
			fmt.Fprintf(&b, "        - to: default\n          via: %s\n", machine.MgmtGateway)
		} else {
			fmt.Fprintf(&b, "    %s: {}\n", iface)
		}
	}
	if len(machine.MgmtIfaces) > 1 {
		fmt.Fprintf(&b, "  bonds:\n    %s:\n", machine.MgmtBondName)
		fmt.Fprintf(&b, "      interfaces: [%s]\n", strings.Join(machine.MgmtIfaces, ", "))
		fmt.Fprintf(&b, "      addresses:\n        - %s/%d\n", machine.MgmtIP, machine.MgmtPrefix)
		fmt.Fprintf(&b, "      mtu: %d\n", machine.MgmtMTU)
		writeNameservers(&b, machine.MgmtDNS, "      ")
		b.WriteString("      parameters:\n")
		fmt.Fprintf(&b, "        mode: %s\n", machine.BondMode)
		if strings.EqualFold(machine.BondMode, "802.3ad") {
			fmt.Fprintf(&b, "        lacp-rate: %s\n", machine.BondLACPRate)
			fmt.Fprintf(&b, "        transmit-hash-policy: %s\n", machine.BondXmitHash)
		}
		if strings.EqualFold(machine.BondMode, "active-backup") && machine.BondMII > 0 {
			fmt.Fprintf(&b, "        mii-monitor-interval: %d\n", machine.BondMII)
		}
		if strings.EqualFold(machine.BondMode, "active-backup") && strings.TrimSpace(machine.BondPrimary) != "" {
			fmt.Fprintf(&b, "        primary: %s\n", machine.BondPrimary)
		}
		b.WriteString("      routes:\n")
		fmt.Fprintf(&b, "        - to: default\n          via: %s\n", machine.MgmtGateway)
	}
	return b.String()
}

func writeNameservers(b *strings.Builder, dns []string, indent string) {
	fmt.Fprintf(b, "%snameservers:\n", indent)
	if len(dns) == 0 {
		fmt.Fprintf(b, "%s  addresses: []\n", indent)
		return
	}
	fmt.Fprintf(b, "%s  addresses:\n", indent)
	for _, item := range dns {
		fmt.Fprintf(b, "%s    - %s\n", indent, item)
	}
}

func renderRDMANetplan(item spec.RDMAConfig, mtu int) string {
	var b strings.Builder
	b.WriteString("network:\n")
	b.WriteString("  version: 2\n")
	b.WriteString("  renderer: networkd\n")
	b.WriteString("  ethernets:\n")
	fmt.Fprintf(&b, "    %s:\n", item.Name)
	fmt.Fprintf(&b, "      addresses:\n        - %s/%d\n", item.IP, item.Prefix)
	b.WriteString("      ignore-carrier: true\n")
	fmt.Fprintf(&b, "      mtu: %d\n", mtu)
	return b.String()
}

func renderRouteScript(item spec.RDMAConfig, routeCIDR string, priority int) string {
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString("set -euo pipefail\n\n")
	fmt.Fprintf(&b, "IP=%q\n", item.IP)
	fmt.Fprintf(&b, "DEV=%q\n", item.Name)
	fmt.Fprintf(&b, "TABLE=%q\n", strconv.Itoa(item.Table))
	fmt.Fprintf(&b, "GW=%q\n", item.Gateway)
	fmt.Fprintf(&b, "ROUTE_CIDR=%q\n", routeCIDR)
	fmt.Fprintf(&b, "PRIORITY=%q\n", strconv.Itoa(priority))
	fmt.Fprintf(&b, "BROADCAST=%q\n\n", ipv4Broadcast(item.IP, item.Prefix))
	b.WriteString("if ! ip addr show \"$DEV\" | grep -q \"inet \"; then\n")
	b.WriteString("    echo \"device $DEV no ip address, so add ip address for it\"\n")
	b.WriteString("    ip addr add \"${IP}/" + strconv.Itoa(item.Prefix) + "\" brd \"$BROADCAST\" dev \"$DEV\"\n")
	b.WriteString("fi\n\n")
	b.WriteString("while read -r line; do\n")
	b.WriteString("    [ -n \"$line\" ] || continue\n")
	b.WriteString("    ip rule del \"${line#*: }\" 2>/dev/null || true\n")
	b.WriteString("done < <(ip rule show | grep \"$DEV\" || true)\n\n")
	b.WriteString("ip rule del from all oif \"$DEV\" table \"$TABLE\" priority \"$PRIORITY\" 2>/dev/null || true\n")
	b.WriteString("ip rule del from \"$IP\" table \"$TABLE\" priority \"$PRIORITY\" 2>/dev/null || true\n\n")
	b.WriteString("ip route replace default via \"$GW\" dev \"$DEV\" table \"$TABLE\"\n")
	b.WriteString("ip route replace \"$ROUTE_CIDR\" via \"$GW\" table \"$TABLE\" src \"$IP\" proto static\n")
	b.WriteString("ip rule add from all oif \"$DEV\" table \"$TABLE\" priority \"$PRIORITY\"\n")
	b.WriteString("ip rule add from \"$IP\" table \"$TABLE\" priority \"$PRIORITY\"\n")
	return b.String()
}

const (
	postBootCustomBegin = "# BEGIN CUSTOM ACTIONS"
	postBootCustomEnd   = "# END CUSTOM ACTIONS"
)

func renderPostBootScript(machine spec.MachineConfig, customBlock string) string {
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString("set -euo pipefail\n\n")
	b.WriteString("# This file is managed by envinit. The custom section is preserved on update.\n")
	b.WriteString("RDMA_INTERFACES=(\n")
	for _, item := range machine.RDMA {
		fmt.Fprintf(&b, "  %q\n", item.Name)
	}
	b.WriteString(")\n\n")
	b.WriteString("for iface in \"${RDMA_INTERFACES[@]}\"; do\n")
	b.WriteString("    if ! ip link show \"$iface\" >/dev/null 2>&1; then\n")
	b.WriteString("        echo \"skip missing interface: $iface\"\n")
	b.WriteString("        continue\n")
	b.WriteString("    fi\n\n")
	b.WriteString("    echo \"set ring buffer depth on $iface\"\n")
	b.WriteString("    if ! ethtool -G \"$iface\" rx 8192 tx 8192; then\n")
	b.WriteString("        echo \"skip ring buffer tuning on $iface: ethtool -G failed\"\n")
	b.WriteString("    fi\n\n")
	b.WriteString("    if ! bus_info=$(ethtool -i \"$iface\" 2>/dev/null | awk -F': ' '$1 == \"bus-info\" {print $2; exit}'); then\n")
	b.WriteString("        echo \"skip RoCE AR on $iface: ethtool -i failed\"\n")
	b.WriteString("        continue\n")
	b.WriteString("    fi\n")
	b.WriteString("    if [ -z \"$bus_info\" ]; then\n")
	b.WriteString("        echo \"skip RoCE AR on $iface: missing bus-info\"\n")
	b.WriteString("        continue\n")
	b.WriteString("    fi\n")
	b.WriteString("    echo \"enable RoCE adaptive routing on $iface ($bus_info)\"\n")
	b.WriteString("    if ! mlxreg -d \"$bus_info\" --reg_name ROCE_ACCL --set adaptive_routing_forced_en=0x1 --yes; then\n")
	b.WriteString("        echo \"skip RoCE AR on $iface: mlxreg failed\"\n")
	b.WriteString("        continue\n")
	b.WriteString("    fi\n")
	b.WriteString("done\n\n")
	b.WriteString(postBootCustomBegin + "\n")
	b.WriteString(customBlock)
	if !strings.HasSuffix(customBlock, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(postBootCustomEnd + "\n")
	return b.String()
}

func renderPostBootService() string {
	return strings.Join([]string{
		"[Unit]",
		"Description=Kunlun post-boot RDMA tuning",
		"After=network-online.target",
		"Wants=network-online.target",
		"",
		"[Service]",
		"Type=oneshot",
		"ExecStart=" + postBootScript,
		"RemainAfterExit=yes",
		"",
		"[Install]",
		"WantedBy=multi-user.target",
		"",
	}, "\n")
}

func defaultPostBootCustomBlock() string {
	return "# Add custom boot-time actions below. This section is preserved by envinit.\n"
}

func extractPostBootCustomBlock(content string) string {
	begin := strings.Index(content, postBootCustomBegin)
	end := strings.Index(content, postBootCustomEnd)
	if begin == -1 || end == -1 || end < begin {
		return defaultPostBootCustomBlock()
	}
	begin += len(postBootCustomBegin)
	custom := content[begin:end]
	custom = strings.TrimPrefix(custom, "\r\n")
	custom = strings.TrimPrefix(custom, "\n")
	if strings.TrimSpace(custom) == "" {
		return defaultPostBootCustomBlock()
	}
	return custom
}

func desiredSysctlLines(machine spec.MachineConfig) []string {
	lines := []string{
		"net.core.rmem_max = 212992000",
		"net.core.rmem_default = 212992000",
		"net.core.wmem_max = 212992000",
		"net.core.wmem_default = 212992000",
		"net.ipv4.tcp_rmem = 4096000 131072000 629145600",
		"net.ipv4.tcp_wmem = 4096000 16384000 419430400",
	}
	for _, item := range machine.RDMA {
		lines = append(lines,
			fmt.Sprintf("net.ipv4.conf.%s.arp_ignore=2", item.Name),
			fmt.Sprintf("net.ipv4.conf.%s.arp_announce=1", item.Name),
			fmt.Sprintf("net.ipv4.conf.%s.rp_filter=2", item.Name),
			fmt.Sprintf("net.ipv6.conf.%s.disable_ipv6=0", item.Name),
		)
	}
	return lines
}

func (a *App) ensureSysctlSettings() error {
	target := a.targetPath(sysctlFile)
	if !a.DryRun {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(target), err)
		}
	}
	existing, err := os.ReadFile(target)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read %s: %w", sysctlFile, err)
	}

	lines := strings.Split(strings.ReplaceAll(string(existing), "\r\n", "\n"), "\n")
	lastValues := map[string]string{}
	for _, line := range lines {
		key, value, ok := parseSysctlLine(line)
		if !ok {
			continue
		}
		lastValues[key] = value
	}

	changed := false
	for _, line := range desiredSysctlLines(a.Machine) {
		key, value, ok := parseSysctlLine(line)
		if !ok {
			continue
		}
		if lastValues[key] == value {
			continue
		}
		lines = append(lines, line)
		lastValues[key] = value
		changed = true
	}

	if !changed {
		a.logf("unchanged %s", sysctlFile)
		return nil
	}

	content := strings.Join(lines, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	a.logf("update %s", sysctlFile)
	if a.DryRun {
		return nil
	}
	return os.WriteFile(target, []byte(content), 0o644)
}

func parseSysctlLine(line string) (string, string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	parts := strings.SplitN(trimmed, "=", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	key := strings.TrimSpace(parts[0])
	value := strings.TrimSpace(parts[1])
	if key == "" {
		return "", "", false
	}
	return key, value, true
}

func parseMlxConfigValue(output string, key string) string {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != key {
			continue
		}
		return fields[len(fields)-1]
	}
	return ""
}

func findDirWithFiles(root string, files ...string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		for _, name := range files {
			if _, err := os.Stat(filepath.Join(path, name)); err != nil {
				return nil
			}
		}
		found = path
		return io.EOF
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("did not find directory containing %s under %s", strings.Join(files, ", "), root)
	}
	return found, nil
}

func ensureGrubCmdline(path string, required []string) (bool, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, "", fmt.Errorf("read grub config: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	changed := false
	found := false
	for idx, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "GRUB_CMDLINE_LINUX=") {
			continue
		}
		found = true
		value := strings.TrimPrefix(trimmed, "GRUB_CMDLINE_LINUX=")
		unquoted, err := strconv.Unquote(value)
		if err != nil {
			unquoted = strings.Trim(value, `"`)
		}
		tokens := strings.Fields(unquoted)
		filtered := make([]string, 0, len(tokens))
		for _, token := range tokens {
			if token == "quiet" {
				changed = true
				continue
			}
			filtered = append(filtered, token)
		}
		for _, token := range required {
			if !slices.Contains(filtered, token) {
				filtered = append(filtered, token)
				changed = true
			}
		}
		lines[idx] = fmt.Sprintf("GRUB_CMDLINE_LINUX=%q", strings.Join(filtered, " "))
	}
	if !found {
		changed = true
		lines = append(lines, fmt.Sprintf("GRUB_CMDLINE_LINUX=%q", strings.Join(required, " ")))
	}
	if !changed {
		return false, string(data), nil
	}
	return true, strings.Join(lines, "\n"), nil
}

func ipv4Broadcast(ip string, prefix int) string {
	parsed := net.ParseIP(ip).To4()
	if parsed == nil {
		return ""
	}
	mask := net.CIDRMask(prefix, 32)
	out := make(net.IP, len(parsed))
	for idx := range parsed {
		out[idx] = parsed[idx] | ^mask[idx]
	}
	return out.String()
}

func recordMACs(record spec.MachineRecord) []string {
	out := make([]string, 0, 6)
	for _, raw := range []string{record.MgmtMAC1, record.MgmtMAC2} {
		if mac, err := spec.NormalizeMAC(raw); err == nil && mac != "" {
			out = append(out, mac)
		}
	}
	for _, item := range record.RDMA {
		if mac, err := spec.NormalizeMAC(item.MAC); err == nil && mac != "" {
			out = append(out, mac)
		}
	}
	return out
}

func hasAny(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func nonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

func (a *App) managementSummaryName() string {
	if len(a.Machine.MgmtIfaces) > 1 {
		return a.Machine.MgmtBondName
	}
	if len(a.Machine.MgmtIfaces) == 1 {
		return a.Machine.MgmtIfaces[0]
	}
	return "n/a"
}

func (a *App) bondSummary() string {
	parts := []string{fmt.Sprintf("(mode=%s", a.Machine.BondMode)}
	if strings.EqualFold(a.Machine.BondMode, "active-backup") && strings.TrimSpace(a.Machine.BondPrimary) != "" {
		parts = append(parts, fmt.Sprintf("primary=%s", a.Machine.BondPrimary))
	}
	return strings.Join(parts, " ") + ")"
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
