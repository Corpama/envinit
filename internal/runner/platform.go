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
		if err := a.runCmdAllowFailure("", nil, "systemctl", "disable", "--now", "network"); err != nil {
			return err
		}
		return a.runCmd("", nil, "systemctl", "enable", "--now", "NetworkManager")
	case "network":
		if err := a.runCmdAllowFailure("", nil, "systemctl", "disable", "--now", "NetworkManager"); err != nil {
			return err
		}
		return a.runCmd("", nil, "systemctl", "enable", "--now", "network")
	default:
		return nil
	}
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
	if a.Bundle.OfflineRepo.Enabled || strings.TrimSpace(a.Bundle.OfflineRepo.MaterialPath) != "" || strings.TrimSpace(a.Bundle.OfflineRepo.CopyTo) != "" || len(a.Bundle.OfflineRepo.Entries) > 0 {
		return a.Bundle.OfflineRepo
	}
	return a.Bundle.OfflineAPT
}

func (a *App) routeScriptPath(iface string) string {
	name := fmt.Sprintf("config_rt_%s.sh", iface)
	if a.usesIfcfgNetwork() {
		return filepath.Join(localSbinDir, "kunlun-"+name)
	}
	return filepath.Join(routeDir, name)
}
