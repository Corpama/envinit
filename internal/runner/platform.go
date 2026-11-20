package runner

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"envinit/internal/spec"
)

func (a *App) usesYum() bool {
	return strings.EqualFold(strings.TrimSpace(a.Bundle.Platform.PackageManager), "yum")
}

func (a *App) usesNetworkManager() bool {
	return a.networkBackend() == "networkmanager"
}

func (a *App) usesIfcfgNetwork() bool {
	backend := a.networkBackend()
	return backend == "networkmanager" || backend == "network"
}

func (a *App) networkBackend() string {
	if backend, ok := a.explicitNetworkBackend(); ok {
		return backend
	}
	switch strings.ToLower(strings.TrimSpace(a.Bundle.Platform.NetworkBackend)) {
	case "auto", "ifcfg":
		return a.detectIfcfgNetworkBackend()
	default:
		return ""
	}
}

func (a *App) explicitNetworkBackend() (string, bool) {
	switch strings.ToLower(strings.TrimSpace(a.Bundle.Platform.NetworkBackend)) {
	case "networkmanager", "network-manager", "nm":
		return "networkmanager", true
	case "network", "network-scripts":
		return "network", true
	default:
		return "", false
	}
}

func (a *App) detectIfcfgNetworkBackend() string {
	if serviceActive("network") {
		return "network"
	}
	if serviceActive("NetworkManager") {
		return "networkmanager"
	}
	if _, err := exec.LookPath("nmcli"); err == nil {
		return "networkmanager"
	}
	return "networkmanager"
}

func serviceActive(service string) bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	return exec.Command("systemctl", "is-active", "--quiet", service).Run() == nil
}

func (a *App) ensureExplicitNetworkBackendService() error {
	backend, explicit := a.explicitNetworkBackend()
	if !explicit {
		return nil
	}
	switch backend {
	case "networkmanager":
		return a.convergeExclusiveNetworkService("NetworkManager", "network")
	case "network":
		return a.convergeExclusiveNetworkService("network", "NetworkManager")
	default:
		return nil
	}
}

func (a *App) convergeExclusiveNetworkService(target, other string) error {
	if a.DryRun {
		if err := a.runCmdAllowFailure("", nil, "systemctl", "disable", "--now", other); err != nil {
			return err
		}
		return a.runCmd("", nil, "systemctl", "enable", "--now", target)
	}
	if _, err := a.captureCmd("", nil, "systemctl", "is-active", "--quiet", other); err == nil {
		if err := a.runCmdAllowFailure("", nil, "systemctl", "disable", "--now", other); err != nil {
			return err
		}
	} else if _, err := a.captureCmd("", nil, "systemctl", "is-enabled", "--quiet", other); err == nil {
		if err := a.runCmdAllowFailure("", nil, "systemctl", "disable", other); err != nil {
			return err
		}
	}
	enabledErr := error(nil)
	if _, err := a.captureCmd("", nil, "systemctl", "is-enabled", "--quiet", target); err != nil {
		enabledErr = err
	}
	activeErr := error(nil)
	if _, err := a.captureCmd("", nil, "systemctl", "is-active", "--quiet", target); err != nil {
		activeErr = err
	}
	if enabledErr == nil && activeErr == nil {
		a.logf("network service %s already satisfied", target)
		return nil
	}
	return a.runCmd("", nil, "systemctl", "enable", "--now", target)
}

func (a *App) describeExplicitNetworkBackendServiceSwitch() []string {
	backend, explicit := a.explicitNetworkBackend()
	if !explicit {
		return nil
	}
	switch backend {
	case "networkmanager":
		return []string{
			"disable and stop network service when present: systemctl disable --now network",
			"enable and start NetworkManager: systemctl enable --now NetworkManager",
		}
	case "network":
		return []string{
			"disable and stop NetworkManager when present: systemctl disable --now NetworkManager",
			"enable and start network service: systemctl enable --now network",
		}
	default:
		return nil
	}
}

func (a *App) offlineRepoConfig() spec.OfflineAPTConfig {
	return a.Bundle.EffectiveOfflineRepo()
}

func (a *App) routeScriptPath(iface string) string {
	name := fmt.Sprintf("config_rt_%s.sh", iface)
	if a.usesIfcfgNetwork() {
		return filepath.Join(localSbinDir, "kunlun-"+name)
	}
	return filepath.Join(routeDir, name)
}
