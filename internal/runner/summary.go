package runner

import (
	"fmt"
	"strings"
)

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

func (a *App) configureManagementNetwork() bool {
	return a.Bundle.ConfigureManagementNetwork() && strings.TrimSpace(a.Machine.MgmtIP) != ""
}

func (a *App) hasPersistentNICNamingTargets() bool {
	return (a.configureManagementNetwork() && len(a.Machine.MgmtIfaces) > 0) || (a.Bundle.RDMAExists() && len(a.Machine.RDMA) > 0)
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
