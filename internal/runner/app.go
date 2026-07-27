package runner

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"envinit/internal/spec"
	"envinit/internal/xpuvariant"
)

const (
	netplanDir             = "/etc/netplan"
	networkScriptsDir      = "/etc/sysconfig/network-scripts"
	nmDispatcherFile       = "/etc/NetworkManager/dispatcher.d/90-kunlun-rdma-routes"
	routeDir               = "/etc/networkd-dispatcher/routable.d"
	localSbinDir           = "/usr/local/sbin"
	managementUdevFile     = "/etc/udev/rules.d/70-kunlun-management-net.rules"
	rdmaUdevFile           = "/etc/udev/rules.d/71-kunlun-rdma-net.rules"
	rdmaSelectedFile       = "/var/lib/envinit/selected_interfaces"
	legacyRDMASelectedFile = "/etc/rdma/rdma_conf/selected_interfaces"
	sysctlFile             = "/etc/sysctl.conf"
	grubFile               = "/etc/default/grub"

	kunlunModprobeFile = "/etc/modprobe.d/kunlun.conf"

	postBootScript  = "/usr/local/sbin/kunlun-post-boot.sh"
	postBootService = "/etc/systemd/system/kunlun-post-boot.service"
)

const (
	xreCardModelP800 = "P800"
	xreCardModelP900 = "P900"
	p800PartNumberVC = xpuvariant.PartNumberVC
	p800PartNumberVD = xpuvariant.PartNumberVD
)

var stageOrder = []string{
	"software",
	"ofed",
	"network",
	"xre",
	"xdr",
	"firmware",
	"container",
	"mlxconfig",
	"sysctl",
	"kernel",
	"post",
}

var knownStages = func() map[string]bool {
	out := map[string]bool{"all": true}
	for _, stage := range stageOrder {
		out[stage] = true
	}
	out["udev"] = true
	return out
}()

type App struct {
	Bundle                  spec.Bundle
	Machine                 spec.MachineConfig
	Root                    string
	DryRun                  bool
	InteractiveDryRunReview bool
	Stages                  map[string]bool
	Output                  io.Writer
	HostSpecified           bool
	ResumeApply             bool
	ResetApplyProgress      bool

	networkApplyDeferred       bool
	confirmedInterfaceBindings []interfaceBinding
	interfaceBindingsConfirmed bool
	udevRulesPersisted         bool
	now                        func() time.Time
	runStageOverride           func(string) error
	applySourceRecord          spec.MachineRecord
	hasApplySourceRecord       bool
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
	app := &App{
		Bundle:               bundle,
		Machine:              machine,
		Root:                 root,
		DryRun:               dryRun,
		Stages:               stages,
		Output:               out,
		HostSpecified:        strings.TrimSpace(host) != "",
		applySourceRecord:    record,
		hasApplySourceRecord: true,
		now:                  time.Now,
	}
	if app.configureManagementNetwork() {
		if err := app.ensureAutoManagementInterfaces(); err != nil {
			return nil, err
		}
	}
	return app, nil
}

func (a *App) Apply() error {
	if !a.DryRun {
		if a.Root != "" && a.Root != "/" {
			return errors.New("apply mode does not support --root; use plan for alternate-root previews")
		}
		if os.Geteuid() != 0 {
			return errors.New("apply mode must be run as root")
		}
		if err := requireApplyTTY(); err != nil {
			return err
		}
	}
	if err := a.ensureHostname(); err != nil {
		return err
	}
	if err := a.runSelectedStages(); err != nil {
		return err
	}
	if a.stageEnabled("udev") && !a.stageEnabled("network") {
		if err := a.runStage("udev"); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) runSelectedStages() error {
	if a.ResumeApply && !a.DryRun && a.Stages["all"] {
		return a.runStagesWithProgress()
	}
	for _, stage := range stageOrder {
		if !a.stageEnabled(stage) {
			continue
		}
		if err := a.executeStage(stage); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) executeStage(stage string) error {
	if a.runStageOverride != nil {
		return a.runStageOverride(stage)
	}
	return a.runStage(stage)
}

func requireApplyTTY() error {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("apply mode requires an interactive TTY; run from a local console or an SSH session allocated with a TTY, not a non-interactive remote command: %w", err)
	}
	_ = tty.Close()
	return nil
}

func (a *App) runStage(stage string) error {
	a.logf("==> stage: %s", stage)
	switch stage {
	case "network":
		return a.runNetworkStage()
	case "udev":
		return a.runUdevStage()
	case "software":
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
	case "kernel":
		return a.runKernelStage()
	case "post":
		return a.runPostStage()
	default:
		return fmt.Errorf("unknown stage %q", stage)
	}
}

func IsKnownStage(stage string) bool {
	_, ok := CanonicalStage(stage)
	return ok
}

func CanonicalStage(stage string) (string, bool) {
	stage = strings.TrimSpace(strings.ToLower(stage))
	switch stage {
	case "software", "software-repo", "software_repo", "softwarerepo", "packages", "package", "repo", "apt", "yum":
		return "software", true
	case "kernel", "kernel-params", "kernel_params", "kernelparams", "kernel-param", "kernel_param", "kernelparam", "boot-params", "boot_params", "bootparams", "iommu":
		return "kernel", true
	default:
		return stage, knownStages[stage]
	}
}

func (a *App) stageEnabled(stage string) bool {
	if a.Stages["all"] || a.Stages[stage] {
		return true
	}
	return (stage == "software" && a.Stages["apt"]) || (stage == "kernel" && a.Stages["iommu"])
}

const (
	postBootCustomBegin = "# BEGIN CUSTOM ACTIONS"
	postBootCustomEnd   = "# END CUSTOM ACTIONS"
)
