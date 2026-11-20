package runner

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

type networkTopology struct {
	Backend string             `json:"backend"`
	Links   []ownedNetworkLink `json:"links,omitempty"`
}

type ownedNetworkLink struct {
	Name     string `json:"name"`
	Role     string `json:"role"`
	MAC      string `json:"mac,omitempty"`
	Master   string `json:"master,omitempty"`
	Bond     bool   `json:"bond,omitempty"`
	BondMode string `json:"bond_mode,omitempty"`
	Address  string `json:"address,omitempty"`
	Gateway  string `json:"gateway,omitempty"`
	MTU      int    `json:"mtu,omitempty"`
	UUID     string `json:"uuid,omitempty"`
}

func networkProfileUUID(name string) string {
	h := contentDigest([]byte("envinit/network/" + name))
	return h[:8] + "-" + h[8:12] + "-5" + h[13:16] + "-a" + h[17:20] + "-" + h[20:32]
}

func (a *App) desiredNetworkTopology(bindings []interfaceBinding) networkTopology {
	t := networkTopology{Backend: a.networkBackend()}
	if a.networkTransaction != nil && !a.configureManagementNetwork() {
		for _, link := range a.networkTransaction.Old.Topology.Links {
			if link.Role == "mgmt" {
				t.Links = append(t.Links, link)
			}
		}
	}
	for _, b := range bindings {
		if b.Kind == "mgmt" && !a.configureManagementNetwork() {
			continue
		}
		link := ownedNetworkLink{Name: b.Name, Role: b.Kind, MAC: b.MAC}
		if b.Kind == "mgmt" {
			link.MTU = a.Machine.MgmtMTU
			if len(a.Machine.MgmtIfaces) > 1 {
				link.Master = a.Machine.MgmtBondName
			} else {
				link.Address = fmt.Sprintf("%s/%d", a.Machine.MgmtIP, a.Machine.MgmtPrefix)
				link.Gateway = a.Machine.MgmtGateway
			}
		} else if a.Bundle.RDMAConfigureIPRoute() {
			for _, item := range a.Machine.RDMA {
				if item.Name == b.Name {
					link.Address = fmt.Sprintf("%s/%d", item.IP, item.Prefix)
					link.MTU = a.Machine.RDMAMTU
				}
			}
		}
		if t.Backend == "networkmanager" && (link.Role == "mgmt" || a.Bundle.RDMAConfigureIPRoute()) {
			link.UUID = networkProfileUUID(link.Name)
		}
		t.Links = append(t.Links, link)
	}
	if a.configureManagementNetwork() && len(a.Machine.MgmtIfaces) > 1 {
		link := ownedNetworkLink{Name: a.Machine.MgmtBondName, Role: "mgmt", Bond: true, BondMode: a.Machine.BondMode, MTU: a.Machine.MgmtMTU,
			Address: fmt.Sprintf("%s/%d", a.Machine.MgmtIP, a.Machine.MgmtPrefix), Gateway: a.Machine.MgmtGateway}
		if t.Backend == "networkmanager" {
			link.UUID = networkProfileUUID(link.Name)
		}
		t.Links = append(t.Links, link)
		for i := range t.Links {
			if t.Links[i].Role == "mgmt" && !t.Links[i].Bond && containsString(a.Machine.MgmtIfaces, t.Links[i].Name) {
				t.Links[i].Master = a.Machine.MgmtBondName
			}
		}
	}
	sort.Slice(t.Links, func(i, j int) bool { return t.Links[i].Name < t.Links[j].Name })
	return t
}

func topologyLink(t networkTopology, name string) (ownedNetworkLink, bool) {
	for _, link := range t.Links {
		if link.Name == name {
			return link, true
		}
	}
	return ownedNetworkLink{}, false
}

func (a *App) actualOwnedLink(link ownedNetworkLink) (string, error) {
	if link.Bond {
		if !a.interfaceExists(link.Name) {
			return "", nil
		}
		if !a.pathExists(filepath.Join("/sys/class/net", link.Name, "bonding")) {
			return "", fmt.Errorf("owned bond %s is occupied by a different device", link.Name)
		}
		return link.Name, nil
	}
	if link.MAC == "" {
		return "", fmt.Errorf("missing physical identity for owned link %s", link.Name)
	}
	byMAC, _, err := localInterfaceIndex(a.Root)
	if err != nil {
		return "", err
	}
	return byMAC[strings.ToLower(link.MAC)], nil
}

func (a *App) interfaceMaster(name string) string {
	p, err := os.Readlink(a.targetPath(filepath.Join("/sys/class/net", name, "master")))
	if err != nil {
		return ""
	}
	return filepath.Base(p)
}

func (a *App) interfaceAddresses(name string) ([]string, error) {
	out, err := a.captureCmd("", nil, "ip", "-j", "-4", "address", "show", "dev", name)
	if err != nil {
		return nil, err
	}
	var links []struct {
		AddrInfo []struct {
			Local  string `json:"local"`
			Prefix int    `json:"prefixlen"`
		} `json:"addr_info"`
	}
	if err := json.Unmarshal([]byte(out), &links); err != nil {
		return nil, fmt.Errorf("decode addresses for %s: %w", name, err)
	}
	var result []string
	for _, link := range links {
		for _, addr := range link.AddrInfo {
			result = append(result, fmt.Sprintf("%s/%d", addr.Local, addr.Prefix))
		}
	}
	return result, nil
}

func containsString(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

// Validate every destructive transition before taking down any interface.
func (a *App) preflightNetworkTopology(next networkTopology) error {
	tx := a.networkTransaction
	if tx == nil || a.DryRun || !a.Bundle.ApplyNetworkImmediately() {
		return nil
	}
	if !a.configureManagementNetwork() && tx.Old.Topology.Backend != next.Backend {
		for _, link := range tx.Old.Topology.Links {
			if link.Role == "mgmt" {
				return fmt.Errorf("cannot switch network backend while preserving management network")
			}
		}
	}
	for _, old := range tx.Old.Topology.Links {
		if old.Role == "mgmt" && !a.configureManagementNetwork() {
			continue
		}
		want, ok := topologyLink(next, old.Name)
		if ok && reflect.DeepEqual(old, want) && tx.Old.Topology.Backend == next.Backend {
			continue
		}
		name, err := a.actualOwnedLink(old)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		master := a.interfaceMaster(name)
		if master != "" && master != old.Master && master != want.Master {
			return fmt.Errorf("interface %s has external master %s", name, master)
		}
		addresses, err := a.interfaceAddresses(name)
		if err != nil {
			return err
		}
		for _, address := range addresses {
			if address != old.Address && address != want.Address {
				return fmt.Errorf("interface %s has unmanaged address %s; cannot retire its topology", name, address)
			}
		}
		if old.Bond {
			members := strings.Fields(readOptionalTrim(a.targetPath(filepath.Join("/sys/class/net", name, "bonding/slaves"))))
			for _, member := range members {
				owned := false
				for _, candidate := range tx.Old.Topology.Links {
					if candidate.Master == old.Name {
						actual, e := a.actualOwnedLink(candidate)
						if e != nil {
							return e
						}
						owned = owned || actual == member
					}
				}
				if !owned {
					return fmt.Errorf("bond %s contains unmanaged member %s", name, member)
				}
			}
		}
	}
	for _, want := range next.Links {
		if want.Role == "mgmt" && !a.configureManagementNetwork() {
			continue
		}
		if !want.Bond {
			name, err := a.actualOwnedLink(want)
			if err != nil {
				return err
			}
			if name != "" {
				var previous ownedNetworkLink
				for _, old := range tx.Old.Topology.Links {
					if !old.Bond && strings.EqualFold(old.MAC, want.MAC) {
						previous = old
						break
					}
				}
				master := a.interfaceMaster(name)
				if master != "" && master != previous.Master && !(master == want.Master && len(tx.Writes) > 0) {
					return fmt.Errorf("target physical port %s has unmanaged master %s", name, master)
				}
				if previous.Name == "" {
					addresses, err := a.interfaceAddresses(name)
					if err != nil {
						return err
					}
					for _, address := range addresses {
						if address != want.Address {
							return fmt.Errorf("target physical port %s has unmanaged address %s", name, address)
						}
					}
				}
			}
		}
		if !a.interfaceExists(want.Name) {
			continue
		}
		if want.Bond {
			old, owned := topologyLink(tx.Old.Topology, want.Name)
			prepared := tx.Writes[ifcfgPath(want.Name)] != "" || tx.Writes[filepath.Join(netplanDir, "00-kunlun-bond.yaml")] != ""
			if (!owned || !old.Bond) && !prepared {
				return fmt.Errorf("bond target %s exists without topology ownership", want.Name)
			}
		}
	}
	return nil
}

func (a *App) retireNetworkTopology(next networkTopology) error {
	tx := a.networkTransaction
	if tx == nil || a.DryRun || !a.Bundle.ApplyNetworkImmediately() {
		return nil
	}
	if err := a.preflightNetworkTopology(next); err != nil {
		return err
	}
	unlock, err := acquireExecutionLock(a.targetPath("/run/lock/envinit-routes.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	// Old routes must be retired before a physical port loses its old name.
	desired := a.desiredOwnedRoutes()
	for _, old := range tx.Old.Runtime {
		keep := desired
		previous, hadPrevious := topologyLink(tx.Old.Topology, old.Device)
		current, hasCurrent := topologyLink(next, old.Device)
		if hadPrevious && (!hasCurrent || previous.MAC != current.MAC) {
			keep = nil
		}
		if err := a.removeObsoleteOwnedRoutes(old, keep); err != nil {
			return err
		}
	}
	// Members and addresses first; virtual bonds last.
	links := append([]ownedNetworkLink(nil), tx.Old.Topology.Links...)
	sort.SliceStable(links, func(i, j int) bool { return !links[i].Bond && links[j].Bond })
	for _, old := range links {
		if old.Role == "mgmt" && !a.configureManagementNetwork() {
			continue
		}
		want, ok := topologyLink(next, old.Name)
		if ok && reflect.DeepEqual(old, want) && tx.Old.Topology.Backend == next.Backend {
			continue
		}
		name, err := a.actualOwnedLink(old)
		if err != nil {
			return err
		}
		if err := a.deactivateOwnedProfile(old, tx.Old.Topology.Backend); err != nil {
			return err
		}
		// Deactivating a virtual profile can remove its kernel link.
		name, err = a.actualOwnedLink(old)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		if old.Gateway != "" && (old.Gateway != want.Gateway || old.Name != want.Name || old.MAC != want.MAC) {
			if err := a.removeManagementDefault(name, old.Gateway); err != nil {
				return err
			}
		}
		if old.Address != "" && (old.Address != want.Address || old.MAC != want.MAC || old.Master != want.Master) {
			addresses, err := a.interfaceAddresses(name)
			if err != nil {
				return err
			}
			if containsString(addresses, old.Address) {
				if err := a.runNetworkCommand("address-delete", name+"/"+old.Address, "ip", "address", "del", old.Address, "dev", name); err != nil {
					return err
				}
			}
		}
		if old.Master != "" && a.interfaceMaster(name) == old.Master && (old.Master != want.Master || old.MAC != want.MAC) {
			if err := a.runNetworkCommand("member-detach", name+"/"+old.Master, "ip", "link", "set", "dev", name, "nomaster"); err != nil {
				return err
			}
		}
		if old.Bond && (!ok || !want.Bond) {
			if err := a.runNetworkCommand("bond-delete", name, "ip", "link", "delete", "dev", name, "type", "bond"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *App) deactivateOwnedProfile(link ownedNetworkLink, backend string) error {
	if backend == "networkmanager" && link.UUID != "" {
		out, err := a.captureCmd("", nil, "nmcli", "-t", "-f", "UUID", "connection", "show")
		if err != nil {
			return err
		}
		if containsString(strings.Fields(out), link.UUID) {
			profile, err := a.captureCmd("", nil, "nmcli", "-g", "connection.interface-name", "connection", "show", "uuid", link.UUID)
			if err != nil {
				return err
			}
			if strings.TrimSpace(profile) != link.Name {
				return fmt.Errorf("owned NetworkManager profile %s now targets %q", link.UUID, strings.TrimSpace(profile))
			}
			// Delete by recorded UUID, never by a possibly ambiguous display name.
			if err := a.runNetworkCommand("nm-profile-delete", link.UUID, "nmcli", "connection", "delete", "uuid", link.UUID); err != nil {
				return err
			}
		}
	} else if backend == "network" && a.networkTransaction.Phase == "prepared" && a.pathExists(ifcfgPath(link.Name)) {
		// A deferred transaction may already have replaced the old profile.
		data, err := os.ReadFile(a.targetPath(ifcfgPath(link.Name)))
		if err != nil {
			return err
		}
		if contentDigest(data) != a.networkTransaction.Old.Files[ifcfgPath(link.Name)] {
			return nil
		}
		if err := a.runNetworkCommand("ifdown", link.Name, "ifdown", link.Name); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) removeManagementDefault(name, gateway string) error {
	out, err := a.captureCmd("", nil, "ip", "-4", "route", "show", "default", "dev", name)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if !containsTokenSubsequenceLine(line, "default via "+gateway+" dev "+name) {
			continue
		}
		args := []string{"-4", "route", "del", "default", "via", gateway, "dev", name}
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == "metric" {
				args = append(args, "metric", fields[i+1])
			}
		}
		if err := a.runNetworkCommand("default-route-delete", strings.Join(args, " "), "ip", args...); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) verifyNetworkTopology(want networkTopology) error {
	if a.DryRun {
		return nil
	}
	for _, link := range want.Links {
		if link.Role == "mgmt" && !a.configureManagementNetwork() {
			continue
		}
		actual, err := a.actualOwnedLink(link)
		if err != nil {
			return err
		}
		if actual != link.Name {
			return fmt.Errorf("network not converged: %s physical binding is at %q", link.Name, actual)
		}
		if master := a.interfaceMaster(actual); master != link.Master {
			return fmt.Errorf("network not converged: %s master=%q, want %q", actual, master, link.Master)
		}
		if link.MTU > 0 {
			mtu := readOptionalTrim(a.targetPath(filepath.Join("/sys/class/net", actual, "mtu")))
			if mtu != strconv.Itoa(link.MTU) {
				return fmt.Errorf("network not converged: %s MTU=%s, want %d", actual, mtu, link.MTU)
			}
		}
		if link.Address != "" {
			addresses, err := a.interfaceAddresses(actual)
			if err != nil {
				return err
			}
			if !containsString(addresses, link.Address) {
				return fmt.Errorf("network not converged: %s missing address %s", actual, link.Address)
			}
		}
		if link.Gateway != "" {
			out, err := a.captureCmd("", nil, "ip", "-4", "route", "show", "default", "dev", actual)
			if err != nil {
				return err
			}
			if !containsTokenSubsequenceLine(out, "default via "+link.Gateway+" dev "+actual) {
				return fmt.Errorf("network not converged: %s missing default gateway %s", actual, link.Gateway)
			}
		}
		if link.Bond && link.BondMode != "" {
			mode := strings.Fields(readOptionalTrim(a.targetPath(filepath.Join("/sys/class/net", actual, "bonding/mode"))))
			if !containsString(mode, link.BondMode) {
				return fmt.Errorf("network not converged: %s bond mode %v, want %s", actual, mode, link.BondMode)
			}
		}
		if link.Bond {
			members := strings.Fields(readOptionalTrim(a.targetPath(filepath.Join("/sys/class/net", actual, "bonding/slaves"))))
			var expected []string
			for _, candidate := range want.Links {
				if candidate.Master == link.Name {
					expected = append(expected, candidate.Name)
				}
			}
			sort.Strings(members)
			sort.Strings(expected)
			if strings.Join(members, ",") != strings.Join(expected, ",") {
				return fmt.Errorf("network not converged: bond %s members=%v, want %v", actual, members, expected)
			}
		}
	}
	for _, route := range a.desiredOwnedRoutes() {
		out, err := a.captureCmd("", nil, "ip", "rule", "show")
		if err != nil {
			return err
		}
		if countOwnedRules(out, route, false) != 1 || countOwnedRules(out, route, true) != 1 {
			return fmt.Errorf("network not converged: missing policy rules for %s", route.Device)
		}
		out, err = a.captureCmd("", nil, "ip", "route", "show", "table", strconv.Itoa(route.Table))
		if err != nil {
			return err
		}
		if !containsTokenSubsequenceLine(out, "default via "+route.Gateway+" dev "+route.Device) || !containsTokenSubsequenceLine(out, route.RouteCIDR+" dev "+route.Device+" scope link src "+route.IP) {
			return fmt.Errorf("network not converged: missing RDMA routes for %s", route.Device)
		}
	}
	return nil
}

func (a *App) verifyRetiredNetworkTopology(want networkTopology) error {
	if a.networkTransaction == nil || a.DryRun {
		return nil
	}
	for _, old := range a.networkTransaction.Old.Topology.Links {
		if old.Role == "mgmt" && !a.configureManagementNetwork() {
			continue
		}
		name, err := a.actualOwnedLink(old)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		var current ownedNetworkLink
		for _, link := range want.Links {
			if (old.Bond && link.Bond && old.Name == link.Name) || (!old.Bond && !link.Bond && strings.EqualFold(old.MAC, link.MAC)) {
				current = link
				break
			}
		}
		if old.Bond && current.Name == "" {
			return fmt.Errorf("network not converged: retired bond %s still exists", name)
		}
		if old.Master != "" && current.Master != old.Master && a.interfaceMaster(name) == old.Master {
			return fmt.Errorf("network not converged: retired member %s still belongs to %s", name, old.Master)
		}
		if old.Address != "" && old.Address != current.Address {
			addresses, err := a.interfaceAddresses(name)
			if err != nil {
				return err
			}
			if containsString(addresses, old.Address) {
				return fmt.Errorf("network not converged: retired address %s remains on %s", old.Address, name)
			}
		}
	}
	return nil
}

// Backend reloads can preserve old addresses or recreate virtual links. Repeat
// exact retirement after activation, using physical identity rather than stale
// names. This also repairs a crash after the initial retirement checkpoint.
func (a *App) cleanupRetiredNetworkObjects() error {
	tx := a.networkTransaction
	if tx == nil || a.DryRun || !a.Bundle.ApplyNetworkImmediately() {
		return nil
	}
	unlock, err := acquireExecutionLock(a.targetPath("/run/lock/envinit-routes.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	for _, old := range tx.Old.Runtime {
		if physical, ok := topologyLink(tx.Old.Topology, old.Device); ok {
			actual, err := a.actualOwnedLink(physical)
			if err != nil {
				return err
			}
			if actual != "" {
				old.Device = actual
			}
		}
		if err := a.removeObsoleteOwnedRoutes(old, a.desiredOwnedRoutes()); err != nil {
			return err
		}
	}
	for _, old := range tx.Old.Topology.Links {
		if old.Role == "mgmt" && !a.configureManagementNetwork() {
			continue
		}
		name, err := a.actualOwnedLink(old)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		var current ownedNetworkLink
		for _, link := range tx.Topology.Links {
			if old.Bond && link.Bond && old.Name == link.Name || !old.Bond && !link.Bond && strings.EqualFold(old.MAC, link.MAC) {
				current = link
				break
			}
		}
		if old.Address != "" && old.Address != current.Address {
			addresses, err := a.interfaceAddresses(name)
			if err != nil {
				return err
			}
			if containsString(addresses, old.Address) {
				if err := a.runNetworkCommand("address-delete", name+"/"+old.Address, "ip", "address", "del", old.Address, "dev", name); err != nil {
					return err
				}
			}
		}
		if old.Gateway != "" && old.Gateway != current.Gateway {
			if err := a.removeManagementDefault(name, old.Gateway); err != nil {
				return err
			}
		}
		if old.Master != "" && old.Master != current.Master && a.interfaceMaster(name) == old.Master {
			if err := a.runNetworkCommand("member-detach", name+"/"+old.Master, "ip", "link", "set", "dev", name, "nomaster"); err != nil {
				return err
			}
		}
		if old.Bond && current.Name == "" {
			if strings.TrimSpace(readOptionalTrim(a.targetPath(filepath.Join("/sys/class/net", name, "bonding/slaves")))) != "" {
				return fmt.Errorf("retired bond %s still has members", name)
			}
			addresses, err := a.interfaceAddresses(name)
			if err != nil {
				return err
			}
			if len(addresses) != 0 {
				return fmt.Errorf("retired bond %s has unmanaged addresses", name)
			}
			if err := a.runNetworkCommand("bond-delete", name, "ip", "link", "delete", "dev", name, "type", "bond"); err != nil {
				return err
			}
		}
	}
	return nil
}

func validTopologyLink(link ownedNetworkLink) bool {
	validName := func(s string) bool {
		if s == "" || len(s) > 15 || s == "." || s == ".." {
			return false
		}
		for _, c := range s {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
				return false
			}
		}
		return true
	}
	if !validName(link.Name) || (link.Master != "" && !validName(link.Master)) {
		return false
	}
	if link.Role != "mgmt" && link.Role != "rdma" {
		return false
	}
	if !link.Bond {
		if _, err := net.ParseMAC(link.MAC); err != nil {
			return false
		}
	}
	if link.Address != "" {
		if _, _, err := net.ParseCIDR(link.Address); err != nil {
			return false
		}
	}
	return link.Gateway == "" || net.ParseIP(link.Gateway) != nil
}

func (a *App) withRetiredInterfaceNames(bindings []interfaceBinding) ([]interfaceBinding, error) {
	result := cloneInterfaceBindings(bindings)
	byMAC, _, err := localInterfaceIndex(a.Root)
	if err != nil {
		return nil, err
	}
	selected := map[string]bool{}
	for i := range result {
		selected[strings.ToLower(result[i].MAC)] = true
		if current := byMAC[strings.ToLower(result[i].MAC)]; current != "" {
			result[i].CurrentName = current
		}
	}
	for _, want := range bindings {
		if !a.interfaceExists(want.Name) {
			continue
		}
		mac, err := a.readMAC(want.Name)
		if err != nil {
			return nil, err
		}
		if selected[strings.ToLower(mac)] {
			continue
		}
		old, ok := topologyLink(a.networkTransaction.Old.Topology, want.Name)
		if !ok || !strings.EqualFold(old.MAC, mac) {
			return nil, fmt.Errorf("target name %s occupied by unowned physical port %s", want.Name, mac)
		}
		// Stable exit name derived from physical identity; the previous mapping
		// remains in the transaction even across interruption or reboot.
		exit := "ei-old" + contentDigest([]byte(strings.ToLower(mac)))[:8]
		if a.interfaceExists(exit) {
			return nil, fmt.Errorf("retired interface name %s is occupied", exit)
		}
		result = append(result, interfaceBinding{Kind: old.Role, Name: exit, CurrentName: want.Name, MAC: mac})
	}
	return result, nil
}
