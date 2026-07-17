package runner

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

func (a *App) Describe() (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Target machine: %s\n", a.Machine.HostID)
	if a.Machine.Hostname != "" {
		fmt.Fprintf(&b, "Hostname: %s\n", a.Machine.Hostname)
	}
	if line := a.describeHostnameAction(); line != "" {
		fmt.Fprintf(&b, "Hostname action: %s\n", line)
	}
	if a.configureManagementNetwork() {
		fmt.Fprintf(&b, "Management network: %s/%d via %s, uplink=%s, members=%s\n",
			a.Machine.MgmtIP,
			a.Machine.MgmtPrefix,
			a.Machine.MgmtGateway,
			a.managementSummaryName(),
			strings.Join(a.Machine.MgmtIfaces, ","),
		)
	} else {
		fmt.Fprintf(&b, "Management network: skipped (mgmt_ip is empty or configure_management_network=false)\n")
	}
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
		switch a.networkBackend() {
		case "networkmanager":
			return a.describeNetworkManagerStage(), nil
		case "network":
			return a.describeLegacyNetworkStage(), nil
		}
		lines := []string{}
		if a.Bundle.BackupExistingNetplan() {
			lines = append(lines, "backup existing envinit-managed netplan files that will be rewritten")
		}
		if !a.configureManagementNetwork() {
			lines = append(lines, "skip management network configuration because mgmt_ip is empty or configure_management_network=false")
		} else if len(a.Machine.MgmtIfaces) == 1 {
			lines = append(lines, fmt.Sprintf("write %s with single-interface management config on %s (%s/%d via %s)", filepath.Join(netplanDir, "00-kunlun-bond.yaml"), a.Machine.MgmtIfaces[0], a.Machine.MgmtIP, a.Machine.MgmtPrefix, a.Machine.MgmtGateway))
		} else {
			lines = append(lines, fmt.Sprintf("write %s with bond %s %s over %s (%s/%d via %s)", filepath.Join(netplanDir, "00-kunlun-bond.yaml"), a.Machine.MgmtBondName, a.bondSummary(), strings.Join(a.Machine.MgmtIfaces, ","), a.Machine.MgmtIP, a.Machine.MgmtPrefix, a.Machine.MgmtGateway))
		}
		if !a.Bundle.RDMAExists() {
			lines = append(lines, "skip all RDMA actions because rdma_mode=off")
		} else if !a.Bundle.RDMAConfigureIPRoute() {
			lines = append(lines, "skip RDMA IP, netplan, route, and policy-rule configuration because rdma_mode=names_only")
		} else {
			lines = append(lines, "ensure networkd-dispatcher is installed and enabled for RDMA route replay")
			for _, item := range a.Machine.RDMA {
				lines = append(lines,
					fmt.Sprintf("write %s with %s/%d mtu %d", filepath.Join(netplanDir, fmt.Sprintf("10-kunlun-%s.yaml", item.Name)), item.IP, item.Prefix, a.Machine.RDMAMTU),
					fmt.Sprintf("write %s for table %d via %s route_cidr=%s priority=%d", a.routeScriptPath(item.Name), item.Table, item.Gateway, effectiveRDMARouteCIDR(item, a.Machine.RouteCIDR), a.Machine.RoutePriority),
				)
			}
		}
		if a.Bundle.ApplyNetworkImmediately() {
			lines = append(lines, "run netplan generate")
			lines = append(lines, "run netplan apply")
		} else {
			lines = append(lines, "skip immediate netplan apply because apply_network_immediately=false")
		}
		if a.Bundle.ApplyNetworkImmediately() && a.Bundle.RDMAConfigureIPRoute() {
			for _, item := range a.Machine.RDMA {
				lines = append(lines, fmt.Sprintf("run bash %s", a.routeScriptPath(item.Name)))
			}
		}
		if a.Bundle.RDMAExists() {
			for _, item := range a.Machine.RDMA {
				lines = append(lines, fmt.Sprintf("best-effort enable RoCE adaptive routing on %s using ethtool bus-info and mlxreg ROCE_ACCL adaptive_routing_forced_en=0x1", item.Name))
			}
		}
		return a.appendNetworkUdevRuleActions(lines), nil
	case "udev":
		lines := []string{
			"reuse confirmed NIC bindings from the network stage, or review NIC bindings when udev is run standalone",
		}
		lines = a.appendUdevManagedFileActions(lines)
		lines = append(lines,
			"run udevadm control --reload-rules",
			"persistent names take effect after reboot; the network stage handles current-boot temporary renaming before applying network settings",
		)
		return lines, nil
	case "software":
		if a.usesYum() {
			return a.describeYumStage()
		}
		lines := []string{}
		if offlineRepoEnabled(a.Bundle.OfflineAPT) {
			if strings.TrimSpace(a.Bundle.OfflineAPT.MaterialPath) != "" && strings.TrimSpace(a.Bundle.OfflineAPT.CopyTo) != "" {
				lines = append(lines, fmt.Sprintf("copy offline apt materials from %s to %s", a.Bundle.OfflineAPT.MaterialPath, a.Bundle.OfflineAPT.CopyTo))
			}
			if a.Bundle.DisableExistingAptSources() {
				lines = append(lines, "backup existing apt source files except the envinit-managed offline source file")
			}
			lines = append(lines, fmt.Sprintf("write %s with offline apt entries: %s", a.Bundle.OfflineAPT.TargetFile, strings.Join(a.renderOfflineAPTEntries(), " ; ")))
		}
		packages, err := a.requiredPackages()
		if err != nil {
			return nil, err
		}
		if offlineRepoEnabled(a.Bundle.OfflineAPT) || len(packages) > 0 {
			lines = append(lines, "run apt-get update")
		}
		if len(packages) > 0 {
			lines = append(lines, fmt.Sprintf("run apt-get install -y %s", strings.Join(packages, " ")))
		}
		if len(lines) == 0 {
			lines = append(lines, "no software actions; no offline apt entries and no packages configured")
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
		lines := []string{}
		if packages, err := a.ofedPrerequisitePackages(); err == nil && len(packages) > 0 {
			if a.usesYum() {
				lines = append(lines, fmt.Sprintf("ensure OFED prerequisite packages are installed: rpm -q <pkg> or yum install -y %s", strings.Join(packages, " ")))
			} else {
				lines = append(lines, fmt.Sprintf("ensure OFED prerequisite packages are installed: dpkg -s <pkg> or apt-get install -y %s", strings.Join(packages, " ")))
			}
		}
		lines = append(lines,
			fmt.Sprintf("extract %s into %s", a.Bundle.Artifacts.OFEDArchive, filepath.Join(a.Bundle.Artifacts.WorkDir, "ofed-<timestamp>")),
			fmt.Sprintf("run ./mlnxofedinstall --without-fw-update --add-kernel-support -k %s --skip-distro-check --force", kernel),
		)
		return lines, nil
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
			fmt.Sprintf("run KERNELDIR=%s %s", a.kernelHeadersDir(unameR), strings.Join(parts, " ")),
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
			fmt.Sprintf("run KERNELDIR=%s ./build.sh", a.kernelHeadersDir(unameR)),
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
			fmt.Sprintf("run %s", a.localPackageInstallDescription(a.Bundle.Artifacts.ContainerPackages)),
		}, nil
	case "mlxconfig":
		if !a.Bundle.RDMAExists() {
			return []string{"skip mlxconfig: rdma_mode=off"}, nil
		}
		if len(a.Bundle.MlxConfig.Settings) == 0 {
			return []string{"skip: no mlxconfig settings configured"}, nil
		}
		keys := make([]string, 0, len(a.Bundle.MlxConfig.Settings))
		for key, value := range a.Bundle.MlxConfig.Settings {
			keys = append(keys, fmt.Sprintf("%s=%s", key, value))
		}
		sort.Strings(keys)
		lines := []string{
			"run mst start",
		}
		if strings.TrimSpace(a.Bundle.MlxConfig.DeviceGlob) != "" {
			lines = append(lines, fmt.Sprintf("scan devices matching %s", a.Bundle.MlxConfig.DeviceGlob))
		} else {
			lines = append(lines,
				fmt.Sprintf("load persisted MST device selection from %s when present", mstSelectionFile),
				"otherwise discover /dev/mst/*_pciconf* devices and ask for confirmation when interactive",
			)
		}
		lines = append(lines, fmt.Sprintf("query each selected device and set values when needed: %s", strings.Join(keys, ", ")))
		return lines, nil
	case "sysctl":
		lines := []string{
			fmt.Sprintf("append missing kernel parameters into %s", sysctlFile),
		}
		for _, line := range desiredSysctlLines(a.Machine) {
			lines = append(lines, fmt.Sprintf("ensure %s", line))
		}
		lines = append(lines, "run sysctl -p")
		return lines, nil
	case "kernel":
		return []string{
			fmt.Sprintf("ensure %s contains %s", grubFile, strings.Join(requiredKernelCmdline, " ")),
			"run update-grub when available, otherwise run grub2-mkconfig -o /boot/grub2/grub.cfg",
		}, nil
	case "post":
		lines := []string{}
		postPackages := nonEmpty(a.Bundle.PostPackages)
		for idx, pkg := range postPackages {
			lines = append(lines, fmt.Sprintf("install post package %d/%d with %s", idx+1, len(postPackages), a.localPackageInstallDescription([]string{pkg})))
		}
		if a.Bundle.RDMAExists() {
			lines = append(lines,
				fmt.Sprintf("write %s to disable PCIe ACSCtl, tune RDMA MaxReadReq/ring buffers, set CNP DSCP, and enable RoCE adaptive routing at boot", postBootScript),
				fmt.Sprintf("write, enable, and restart %s", postBootService),
			)
		} else {
			lines = append(lines, "skip RDMA post-boot service because rdma_mode=off")
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

func (a *App) describeNetworkManagerStage() []string {
	lines := []string{}
	lines = append(lines, a.describeExplicitNetworkBackendServiceSwitch()...)
	if a.Bundle.BackupExistingNetwork() {
		lines = append(lines, "backup existing envinit-managed ifcfg, route, and rule files that will be rewritten")
	}
	if !a.configureManagementNetwork() {
		lines = append(lines, "skip management network configuration because mgmt_ip is empty or configure_management_network=false")
	} else if len(a.Machine.MgmtIfaces) == 1 {
		lines = append(lines, fmt.Sprintf("write %s with single-interface management config on %s (%s/%d via %s)", ifcfgPath(a.Machine.MgmtIfaces[0]), a.Machine.MgmtIfaces[0], a.Machine.MgmtIP, a.Machine.MgmtPrefix, a.Machine.MgmtGateway))
	} else {
		lines = append(lines, fmt.Sprintf("write %s and member ifcfg files with bond %s %s over %s (%s/%d via %s)", ifcfgPath(a.Machine.MgmtBondName), a.Machine.MgmtBondName, a.bondSummary(), strings.Join(a.Machine.MgmtIfaces, ","), a.Machine.MgmtIP, a.Machine.MgmtPrefix, a.Machine.MgmtGateway))
	}
	if !a.Bundle.RDMAExists() {
		lines = append(lines, "skip all RDMA actions because rdma_mode=off")
	} else if !a.Bundle.RDMAConfigureIPRoute() {
		lines = append(lines, "skip RDMA IP, ifcfg, route, and policy-rule configuration because rdma_mode=names_only")
	} else {
		for _, item := range a.Machine.RDMA {
			lines = append(lines,
				fmt.Sprintf("write %s with %s/%d mtu %d", ifcfgPath(item.Name), item.IP, item.Prefix, a.Machine.RDMAMTU),
				fmt.Sprintf("write %s and %s for table %d via %s route_cidr=%s priority=%d", ifcfgRoutePath(item.Name), ifcfgRulePath(item.Name), item.Table, item.Gateway, effectiveRDMARouteCIDR(item, a.Machine.RouteCIDR), a.Machine.RoutePriority),
				fmt.Sprintf("write %s for immediate policy route apply", a.routeScriptPath(item.Name)),
			)
		}
		lines = append(lines, fmt.Sprintf("write %s to replay RDMA policy route scripts on NetworkManager up events", nmDispatcherFile))
	}
	if a.Bundle.ApplyNetworkImmediately() {
		lines = append(lines, "run nmcli connection reload")
		for _, iface := range a.networkManagerApplyInterfaces() {
			lines = append(lines, fmt.Sprintf("run nmcli connection up %s", iface))
		}
	} else {
		lines = append(lines, "skip immediate NetworkManager reload/up because apply_network_immediately=false")
	}
	if a.Bundle.ApplyNetworkImmediately() && a.Bundle.RDMAConfigureIPRoute() {
		for _, item := range a.Machine.RDMA {
			lines = append(lines, fmt.Sprintf("run bash %s", a.routeScriptPath(item.Name)))
		}
	}
	if a.Bundle.RDMAExists() {
		for _, item := range a.Machine.RDMA {
			lines = append(lines, fmt.Sprintf("best-effort enable RoCE adaptive routing on %s using ethtool bus-info and mlxreg ROCE_ACCL adaptive_routing_forced_en=0x1", item.Name))
		}
	}
	return a.appendNetworkUdevRuleActions(lines)
}

func (a *App) describeLegacyNetworkStage() []string {
	lines := []string{}
	lines = append(lines, a.describeExplicitNetworkBackendServiceSwitch()...)
	if a.Bundle.BackupExistingNetwork() {
		lines = append(lines, "backup existing envinit-managed ifcfg, route, and rule files that will be rewritten")
	}
	if !a.configureManagementNetwork() {
		lines = append(lines, "skip management network configuration because mgmt_ip is empty or configure_management_network=false")
	} else if len(a.Machine.MgmtIfaces) == 1 {
		lines = append(lines, fmt.Sprintf("write %s with single-interface management config on %s (%s/%d via %s, NM_CONTROLLED=no)", ifcfgPath(a.Machine.MgmtIfaces[0]), a.Machine.MgmtIfaces[0], a.Machine.MgmtIP, a.Machine.MgmtPrefix, a.Machine.MgmtGateway))
	} else {
		lines = append(lines, fmt.Sprintf("write %s and member ifcfg files with bond %s %s over %s (%s/%d via %s, NM_CONTROLLED=no)", ifcfgPath(a.Machine.MgmtBondName), a.Machine.MgmtBondName, a.bondSummary(), strings.Join(a.Machine.MgmtIfaces, ","), a.Machine.MgmtIP, a.Machine.MgmtPrefix, a.Machine.MgmtGateway))
	}
	if !a.Bundle.RDMAExists() {
		lines = append(lines, "skip all RDMA actions because rdma_mode=off")
	} else if !a.Bundle.RDMAConfigureIPRoute() {
		lines = append(lines, "skip RDMA IP, ifcfg, route, and policy-rule configuration because rdma_mode=names_only")
	} else {
		for _, item := range a.Machine.RDMA {
			lines = append(lines,
				fmt.Sprintf("write %s with %s/%d mtu %d", ifcfgPath(item.Name), item.IP, item.Prefix, a.Machine.RDMAMTU),
				fmt.Sprintf("write %s and %s for table %d via %s route_cidr=%s priority=%d", ifcfgRoutePath(item.Name), ifcfgRulePath(item.Name), item.Table, item.Gateway, effectiveRDMARouteCIDR(item, a.Machine.RouteCIDR), a.Machine.RoutePriority),
				fmt.Sprintf("write %s for immediate policy route apply", a.routeScriptPath(item.Name)),
			)
		}
	}
	if a.Bundle.ApplyNetworkImmediately() {
		for _, iface := range a.legacyNetworkApplyInterfaces() {
			lines = append(lines, fmt.Sprintf("run ifup %s", iface))
		}
	} else {
		lines = append(lines, "skip immediate ifup because apply_network_immediately=false")
	}
	if a.Bundle.ApplyNetworkImmediately() && a.Bundle.RDMAConfigureIPRoute() {
		for _, item := range a.Machine.RDMA {
			lines = append(lines, fmt.Sprintf("run bash %s", a.routeScriptPath(item.Name)))
		}
	}
	if a.Bundle.RDMAExists() {
		for _, item := range a.Machine.RDMA {
			lines = append(lines, fmt.Sprintf("best-effort enable RoCE adaptive routing on %s using ethtool bus-info and mlxreg ROCE_ACCL adaptive_routing_forced_en=0x1", item.Name))
		}
	}
	return a.appendNetworkUdevRuleActions(lines)
}

func (a *App) appendNetworkUdevRuleActions(lines []string) []string {
	if !a.hasPersistentNICNamingTargets() {
		return lines
	}
	lines = a.appendUdevManagedFileActions(lines)
	return append(lines,
		"run udevadm control --reload-rules",
		"persistent NIC names take effect after reboot",
	)
}

func (a *App) appendUdevManagedFileActions(lines []string) []string {
	if a.configureManagementNetwork() {
		lines = append(lines, fmt.Sprintf("write %s with persistent names for confirmed management NIC bindings", managementUdevFile))
	}
	if a.Bundle.RDMAExists() && len(a.Machine.RDMA) > 0 {
		lines = append(lines, fmt.Sprintf("write %s with persistent names for confirmed RDMA NIC bindings", rdmaUdevFile))
	}
	return lines
}

func (a *App) describeYumStage() ([]string, error) {
	repo := a.offlineRepoConfig()
	lines := []string{}
	if offlineRepoEnabled(repo) {
		if strings.TrimSpace(repo.MaterialPath) != "" && strings.TrimSpace(repo.CopyTo) != "" {
			lines = append(lines, fmt.Sprintf("copy offline yum repo materials from %s to %s", repo.MaterialPath, repo.CopyTo))
		}
		if a.Bundle.DisableExistingRepos() {
			lines = append(lines, "backup existing yum repo files except the envinit-managed offline repo file")
		}
		lines = append(lines, fmt.Sprintf("write %s with offline yum repo entries: %s", repo.TargetFile, strings.Join(a.renderOfflineRepoEntries(repo), " ; ")))
	}
	packages, err := a.requiredPackages()
	if err != nil {
		return nil, err
	}
	if offlineRepoEnabled(repo) || len(packages) > 0 {
		lines = append(lines, "run yum makecache")
	}
	if len(packages) > 0 {
		lines = append(lines, fmt.Sprintf("run yum install -y %s", strings.Join(packages, " ")))
	}
	if len(lines) == 0 {
		lines = append(lines, "no software actions; no offline yum repo entries and no packages configured")
	}
	return lines, nil
}

func (a *App) selectedStages() []string {
	if a.Stages["all"] {
		return append([]string{}, stageOrder...)
	}
	out := make([]string, 0, len(stageOrder))
	for _, stage := range stageOrder {
		if a.stageEnabled(stage) {
			out = append(out, stage)
		}
	}
	if a.stageEnabled("udev") && !a.stageEnabled("network") {
		out = append(out, "udev")
	}
	return out
}

func (a *App) plannedFiles() []string {
	files := make([]string, 0, 2+len(a.Machine.RDMA)*2)
	networkSelected := a.Stages["all"] || a.Stages["network"]
	if networkSelected {
		if a.usesIfcfgNetwork() {
			if a.configureManagementNetwork() {
				if len(a.Machine.MgmtIfaces) == 1 {
					files = append(files, ifcfgPath(a.Machine.MgmtIfaces[0]))
				} else {
					files = append(files, ifcfgPath(a.Machine.MgmtBondName))
					for _, iface := range a.Machine.MgmtIfaces {
						files = append(files, ifcfgPath(iface))
					}
				}
			}
			if a.Bundle.RDMAConfigureIPRoute() {
				if a.usesNetworkManager() {
					files = append(files, nmDispatcherFile)
				}
				for _, item := range a.Machine.RDMA {
					files = append(files, ifcfgPath(item.Name), ifcfgRoutePath(item.Name), ifcfgRulePath(item.Name), a.routeScriptPath(item.Name))
				}
			}
		} else {
			if a.configureManagementNetwork() {
				files = append(files, filepath.Join(netplanDir, "00-kunlun-bond.yaml"))
			}
			if a.Bundle.RDMAConfigureIPRoute() {
				for _, item := range a.Machine.RDMA {
					files = append(files, filepath.Join(netplanDir, fmt.Sprintf("10-kunlun-%s.yaml", item.Name)))
					files = append(files, a.routeScriptPath(item.Name))
				}
			}
		}
		files = a.appendPlannedUdevFiles(files)
	}
	if !networkSelected && a.stageEnabled("udev") && a.hasPersistentNICNamingTargets() {
		files = a.appendPlannedUdevFiles(files)
	}
	if a.Stages["all"] || a.Stages["sysctl"] {
		files = append(files, sysctlFile)
	}
	if a.stageEnabled("software") && offlineRepoEnabled(a.Bundle.OfflineAPT) {
		files = append(files, a.Bundle.OfflineAPT.TargetFile)
	}
	if a.stageEnabled("software") && a.usesYum() {
		repo := a.offlineRepoConfig()
		if offlineRepoEnabled(repo) {
			files = append(files, repo.TargetFile)
		}
	}
	if a.stageEnabled("kernel") {
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

func (a *App) appendPlannedUdevFiles(files []string) []string {
	if a.configureManagementNetwork() {
		files = append(files, managementUdevFile)
	}
	if a.Bundle.RDMAExists() && len(a.Machine.RDMA) > 0 {
		files = append(files, rdmaUdevFile)
	}
	return files
}
