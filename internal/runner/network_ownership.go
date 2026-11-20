package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

type networkManifest struct {
	Version  int                         `json:"version"`
	Files    map[string]string           `json:"files"`
	Roles    map[string]string           `json:"roles"`
	Runtime  map[string]ownedRouteObject `json:"runtime,omitempty"`
	Topology networkTopology             `json:"topology,omitempty"`
}

type ownedRouteObject struct {
	Device    string `json:"device"`
	IP        string `json:"ip"`
	Gateway   string `json:"gateway"`
	RouteCIDR string `json:"route_cidr"`
	Table     int    `json:"table"`
	Priority  int    `json:"priority"`
}

// Retire only files whose exact content is still the version written by us.
// The manifest is committed after all desired files exist; an interrupted run
// can safely repeat retirement because already-retired paths may be absent.
func (a *App) reconcileNetworkFiles() error {
	if a.DryRun {
		return nil
	}
	manifestPath := a.targetPath("/var/lib/envinit/network-files.json")
	old := networkManifest{}
	if raw, err := os.ReadFile(manifestPath); err == nil {
		if err := json.Unmarshal(raw, &old); err != nil {
			return err
		}
		if old.Version != 1 && old.Version != 2 {
			return fmt.Errorf("unsupported network ownership manifest version %d", old.Version)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if a.networkTransaction != nil {
		old = a.networkTransaction.Old
	}
	paths := []string{}
	if a.usesIfcfgNetwork() {
		paths = append(paths, a.ifcfgBackupTargets()...)
		if a.usesNetworkManager() && a.Bundle.RDMAConfigureIPRoute() {
			paths = append(paths, nmDispatcherFile)
		}
	} else {
		paths = append(paths, a.netplanBackupTargets()...)
	}
	if a.Bundle.RDMAConfigureIPRoute() {
		for _, item := range a.Machine.RDMA {
			paths = append(paths, a.routeScriptPath(item.Name))
		}
	}
	next := networkManifest{Version: 1, Files: map[string]string{}, Roles: map[string]string{}, Runtime: map[string]ownedRouteObject{}}
	if a.Bundle.RDMAConfigureIPRoute() {
		for _, item := range a.Machine.RDMA {
			owned := ownedRouteObject{
				Device: item.Name, IP: item.IP, Gateway: item.Gateway,
				RouteCIDR: effectiveRDMARouteCIDR(item, a.Machine.RouteCIDR),
				Table:     item.Table, Priority: a.Machine.RoutePriority,
			}
			next.Runtime[ownedRouteKey(owned)] = owned
		}
	}
	mgmtPaths := map[string]bool{filepath.Join(netplanDir, "00-kunlun-bond.yaml"): true}
	mgmtPaths[ifcfgPath(a.Machine.MgmtBondName)] = true
	for _, iface := range a.Machine.MgmtIfaces {
		mgmtPaths[ifcfgPath(iface)] = true
	}
	for _, p := range paths {
		data, err := os.ReadFile(a.targetPath(p))
		if err != nil {
			return err
		}
		h := sha256.Sum256(data)
		next.Files[p] = hex.EncodeToString(h[:])
		next.Roles[p] = "rdma"
		if mgmtPaths[p] {
			next.Roles[p] = "mgmt"
		}
	}
	for p, digest := range old.Files {
		if p == managementUdevFile || p == rdmaUdevFile {
			next.Files[p], next.Roles[p] = digest, old.Roles[p]
			continue
		}
		if _, exists := next.Files[p]; exists {
			continue
		}
		if !ownedNetworkPath(p) {
			return fmt.Errorf("invalid owned network path %s", p)
		}
		role := old.Roles[p]
		// configure_management_network=false means "leave the management
		// connection alone". RDMA names_only/off, however, must retire IP and
		// route files previously owned by envinit so old addressing does not
		// remain active after the requested mode change.
		if role == "mgmt" && !a.configureManagementNetwork() {
			next.Files[p] = digest
			next.Roles[p] = role
			continue
		}
		if role != "mgmt" && role != "rdma" {
			return fmt.Errorf("missing ownership role for %s", p)
		}
		data, err := os.ReadFile(a.targetPath(p))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		h := sha256.Sum256(data)
		if hex.EncodeToString(h[:]) != digest && (a.networkTransaction == nil || hex.EncodeToString(h[:]) != a.networkTransaction.Writes[p]) {
			return fmt.Errorf("stale network file %s has external edits; review before network reload", p)
		}
		if a.networkTransaction != nil {
			a.networkTransaction.Retired[p] = true
			if err := a.saveNetworkTransaction(); err != nil {
				return err
			}
		}
		if err := a.moveToBackup(a.targetPath(p)); err != nil {
			return err
		}
	}
	for key, stale := range old.Runtime {
		if a.networkTransaction != nil {
			// Runtime retirement occurs before renaming, while old device names
			// still identify the old physical ports.
			continue
		}
		if current, ok := next.Runtime[key]; ok && current == stale {
			continue
		}
		if !a.Bundle.ApplyNetworkImmediately() {
			next.Runtime[key] = stale
			continue
		}
		if err := validateOwnedRouteObject(stale); err != nil {
			return fmt.Errorf("invalid stale runtime route %s: %w", key, err)
		}
		if err := a.removeOwnedRouteObject(stale); err != nil {
			return err
		}
	}
	if a.networkTransaction != nil {
		a.networkTransaction.Next = &next
		a.networkTransaction.Phase = "files-written"
		return a.saveNetworkTransaction()
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o700); err != nil {
		return err
	}
	return atomicWriteFile(manifestPath, append(data, '\n'), 0o600)
}

func ownedRouteKey(item ownedRouteObject) string {
	raw, _ := json.Marshal(item)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func validateOwnedRouteObject(item ownedRouteObject) error {
	if strings.TrimSpace(item.Device) == "" || strings.ContainsAny(item.Device, " \t\r\n/") {
		return fmt.Errorf("invalid device %q", item.Device)
	}
	if net.ParseIP(item.IP) == nil || net.ParseIP(item.Gateway) == nil {
		return fmt.Errorf("invalid IP or gateway")
	}
	if _, _, err := net.ParseCIDR(item.RouteCIDR); err != nil {
		return fmt.Errorf("invalid route CIDR %q", item.RouteCIDR)
	}
	if item.Table <= 0 || item.Priority <= 0 {
		return fmt.Errorf("invalid table or priority")
	}
	return nil
}

// Remove only the exact runtime objects previously recorded by envinit. Each
// delete is guarded by a fresh listing and the final absence is verified; the
// implementation never flushes a policy table owned by other software.
func (a *App) removeOwnedRouteObject(item ownedRouteObject) error {
	return a.removeObsoleteOwnedRoutes(item, nil)
}

func (a *App) desiredOwnedRoutes() []ownedRouteObject {
	var result []ownedRouteObject
	if a.Bundle.RDMAConfigureIPRoute() {
		for _, item := range a.Machine.RDMA {
			result = append(result, ownedRouteObject{Device: item.Name, IP: item.IP, Gateway: item.Gateway, RouteCIDR: effectiveRDMARouteCIDR(item, a.Machine.RouteCIDR), Table: item.Table, Priority: a.Machine.RoutePriority})
		}
	}
	return result
}

func (a *App) removeObsoleteOwnedRoutes(item ownedRouteObject, desired []ownedRouteObject) error {
	if err := validateOwnedRouteObject(item); err != nil {
		return err
	}
	for _, rule := range []struct {
		label string
		oif   bool
		args  []string
	}{
		{fmt.Sprintf("priority %d from %s table %d", item.Priority, item.IP, item.Table), false, []string{"rule", "del", "priority", fmt.Sprint(item.Priority), "from", item.IP + "/32", "table", fmt.Sprint(item.Table)}},
		{fmt.Sprintf("priority %d oif %s table %d", item.Priority, item.Device, item.Table), true, []string{"rule", "del", "priority", fmt.Sprint(item.Priority), "oif", item.Device, "table", fmt.Sprint(item.Table)}},
	} {
		shared := false
		for _, next := range desired {
			if next.Table == item.Table && next.Priority == item.Priority && ((!rule.oif && next.IP == item.IP) || (rule.oif && next.Device == item.Device)) {
				shared = true
			}
		}
		if shared {
			continue
		}
		for deleted := 0; ; deleted++ {
			if deleted > 256 {
				return fmt.Errorf("too many duplicate stale policy rules for %s", rule.label)
			}
			out, err := a.captureCmd("", nil, "ip", "rule", "show")
			if err != nil {
				return fmt.Errorf("inspect stale policy rules: %w", err)
			}
			if !containsOwnedRule(out, item, rule.oif) {
				break
			}
			if err := rejectAmbiguousRuleDeletion(out, item, rule.oif); err != nil {
				return err
			}
			if err := a.runNetworkCommand("rule-delete", strings.Join(rule.args, " "), "ip", rule.args...); err != nil {
				return fmt.Errorf("remove stale policy rule %q: %w", rule.label, err)
			}
		}
	}
	routes := []struct {
		line string
		args []string
	}{
		{fmt.Sprintf("default via %s dev %s", item.Gateway, item.Device), []string{"route", "del", "default", "via", item.Gateway, "dev", item.Device, "table", fmt.Sprint(item.Table)}},
		{fmt.Sprintf("%s dev %s scope link src %s", item.RouteCIDR, item.Device, item.IP), []string{"route", "del", item.RouteCIDR, "dev", item.Device, "scope", "link", "src", item.IP, "table", fmt.Sprint(item.Table)}},
	}
	for idx, route := range routes {
		shared := false
		for _, next := range desired {
			if next.Table == item.Table && next.Device == item.Device && ((idx == 0 && next.Gateway == item.Gateway) || (idx == 1 && next.RouteCIDR == item.RouteCIDR && next.IP == item.IP)) {
				shared = true
			}
		}
		if shared {
			continue
		}
		out, err := a.captureCmd("", nil, "ip", "route", "show", "table", fmt.Sprint(item.Table))
		if err != nil {
			if strings.Contains(err.Error(), "FIB table does not exist") {
				continue
			}
			return fmt.Errorf("inspect stale table %d: %w", item.Table, err)
		}
		if !containsTokenSubsequenceLine(out, route.line) {
			continue
		}
		args := append([]string(nil), route.args...)
		matches := 0
		for _, line := range strings.Split(out, "\n") {
			if !containsTokenSubsequenceLine(line, route.line) {
				continue
			}
			matches++
			fields := strings.Fields(line)
			for i := 1; i < len(fields); {
				if fields[i] == "linkdown" {
					i++
					continue
				}
				if i+1 >= len(fields) {
					return fmt.Errorf("ambiguous owned route %q", line)
				}
				switch fields[i] {
				case "via", "dev", "scope", "src", "proto":
				case "metric":
					args = append(args, "metric", fields[i+1])
				default:
					return fmt.Errorf("route %q has unowned attributes; review before deletion", line)
				}
				i += 2
			}
		}
		if matches != 1 {
			return fmt.Errorf("multiple routes match owned object %q; review before deletion", route.line)
		}
		if err := a.runNetworkCommand("route-delete", strings.Join(args, " "), "ip", args...); err != nil {
			return fmt.Errorf("remove stale route %q: %w", route.line, err)
		}
		verify, err := a.captureCmd("", nil, "ip", "route", "show", "table", fmt.Sprint(item.Table))
		if err != nil && strings.Contains(err.Error(), "FIB table does not exist") {
			continue
		}
		if err != nil || containsTokenSubsequenceLine(verify, route.line) {
			return fmt.Errorf("stale route %q remains after deletion", route.line)
		}
	}
	return nil
}

func containsOwnedRule(output string, item ownedRouteObject, wantOIF bool) bool {
	wantPriority, wantTable := fmt.Sprint(item.Priority), fmt.Sprint(item.Table)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.TrimSuffix(fields[0], ":") != wantPriority {
			continue
		}
		values := map[string]string{}
		valid := true
		for idx := 1; idx < len(fields); idx += 2 {
			if idx+1 >= len(fields) {
				valid = false
				break
			}
			switch fields[idx] {
			case "from", "oif", "lookup", "table":
				if _, exists := values[fields[idx]]; exists {
					valid = false
				}
				values[fields[idx]] = fields[idx+1]
			case "proto", "protocol":
				// Kernel origin metadata does not change rule selectors.
			default:
				valid = false
			}
		}
		if !valid {
			continue
		}
		table := values["lookup"]
		if table == "" {
			table = values["table"]
		}
		if table != wantTable {
			continue
		}
		if wantOIF {
			if values["from"] == "all" && values["oif"] == item.Device {
				return true
			}
			continue
		}
		from := strings.TrimSuffix(values["from"], "/32")
		if from == item.IP && values["oif"] == "" {
			return true
		}
	}
	return false
}

// ip rule del accepts partial selectors; even an exact positive match is unsafe
// when a foreign rule also matches those deletion arguments.
func rejectAmbiguousRuleDeletion(output string, item ownedRouteObject, wantOIF bool) error {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.TrimSuffix(fields[0], ":") != fmt.Sprint(item.Priority) {
			continue
		}
		values := map[string]string{}
		for i := 1; i+1 < len(fields); i++ {
			values[fields[i]] = fields[i+1]
		}
		table := values["lookup"]
		if table == "" {
			table = values["table"]
		}
		if table != fmt.Sprint(item.Table) {
			continue
		}
		matches := strings.TrimSuffix(values["from"], "/32") == item.IP
		if wantOIF {
			matches = values["from"] == "all" && values["oif"] == item.Device
		}
		if matches && !containsOwnedRule(line, item, wantOIF) {
			return fmt.Errorf("foreign rule overlaps owned deletion selectors: %s", line)
		}
	}
	return nil
}

func countOwnedRules(output string, item ownedRouteObject, wantOIF bool) int {
	count := 0
	for _, line := range strings.Split(output, "\n") {
		if containsOwnedRule(line, item, wantOIF) {
			count++
		}
	}
	return count
}

func containsTokenSubsequenceLine(output, wanted string) bool {
	wantedFields := strings.Fields(wanted)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		at := 0
		for _, field := range fields {
			if at < len(wantedFields) && field == wantedFields[at] {
				at++
			}
		}
		if at == len(wantedFields) {
			return true
		}
	}
	return false
}

func ownedNetworkPath(p string) bool {
	if p != filepath.Clean(p) {
		return false
	}
	dir, base := filepath.Dir(p), filepath.Base(p)
	switch dir {
	case netplanDir:
		return strings.HasPrefix(base, "00-kunlun-") || strings.HasPrefix(base, "10-kunlun-")
	case networkScriptsDir:
		return strings.HasPrefix(base, "ifcfg-") || strings.HasPrefix(base, "route-") || strings.HasPrefix(base, "rule-")
	case localSbinDir:
		return strings.HasPrefix(base, "kunlun-config_rt_") && strings.HasSuffix(base, ".sh")
	case routeDir:
		return strings.HasPrefix(base, "config_rt_") && strings.HasSuffix(base, ".sh")
	default:
		return p == nmDispatcherFile
	}
}
