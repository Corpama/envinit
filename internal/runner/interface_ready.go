package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (a *App) ensureInterfacesReady() error {
	missing := a.missingPlannedInterfaces()
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("target interface names are not present yet: %s; run the udev stage so physical NICs can be bound to target names and temporarily renamed before applying network settings", strings.Join(missing, ", "))
}

func (a *App) missingPlannedInterfaces() []string {
	missing := make([]string, 0, len(a.Machine.MgmtIfaces)+len(a.Machine.RDMA))
	if a.configureManagementNetwork() {
		for _, iface := range a.Machine.MgmtIfaces {
			if !a.interfaceExists(iface) {
				missing = append(missing, iface)
			}
		}
	}
	for _, item := range a.Machine.RDMA {
		if !a.interfaceExists(item.Name) {
			missing = append(missing, item.Name)
		}
	}
	return missing
}

func (a *App) interfaceExists(iface string) bool {
	path := a.targetPath(filepath.Join("/sys/class/net", iface))
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
