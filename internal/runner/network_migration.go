package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Import only the old generated schema, backed by exact manifest digests and
// saved physical bindings. Arbitrary system configuration is never adopted.
func (a *App) migrateLegacyNetworkTopology(tx *networkTransaction) error {
	data, err := os.ReadFile(a.targetPath("/var/lib/envinit/nic-bindings.json"))
	if err != nil {
		return fmt.Errorf("legacy ownership migration requires saved physical bindings: %w", err)
	}
	var saved bindingState
	if err := json.Unmarshal(data, &saved); err != nil {
		return err
	}
	if saved.Version != 1 {
		return fmt.Errorf("unsupported legacy binding version")
	}
	byName := map[string]interfaceBinding{}
	for _, b := range saved.Bindings {
		byName[b.Name] = b
	}
	links := map[string]ownedNetworkLink{}
	backend := ""
	for path, digest := range tx.Old.Files {
		if filepath.Dir(path) != netplanDir && (filepath.Dir(path) != networkScriptsDir || !strings.HasPrefix(filepath.Base(path), "ifcfg-")) {
			continue
		}
		data, err := os.ReadFile(a.targetPath(path))
		if err != nil {
			return fmt.Errorf("legacy network file required for migration %s: %w", path, err)
		}
		if contentDigest(data) != digest {
			return fmt.Errorf("legacy network file %s was externally changed", path)
		}
		role := tx.Old.Roles[path]
		if role != "mgmt" && role != "rdma" {
			return fmt.Errorf("missing legacy ownership role for %s", path)
		}
		if filepath.Dir(path) == networkScriptsDir {
			values := map[string]string{}
			for _, line := range strings.Split(string(data), "\n") {
				key, value, ok := strings.Cut(line, "=")
				if ok {
					values[key] = strings.Trim(value, "\"'")
				}
			}
			name := values["DEVICE"]
			link := ownedNetworkLink{Name: name, Role: role, MAC: byName[name].MAC, Master: values["MASTER"], Bond: values["TYPE"] == "Bond", Gateway: values["GATEWAY"], UUID: values["UUID"]}
			link.MTU, _ = strconv.Atoi(values["MTU"])
			if values["IPADDR"] != "" {
				link.Address = values["IPADDR"] + "/" + values["PREFIX"]
			}
			for _, part := range strings.Fields(values["BONDING_OPTS"]) {
				if strings.HasPrefix(part, "mode=") {
					link.BondMode = strings.TrimPrefix(part, "mode=")
				}
			}
			backend = "network"
			if values["NM_CONTROLLED"] == "yes" {
				backend = "networkmanager"
			}
			if backend == "networkmanager" && link.UUID == "" {
				return fmt.Errorf("legacy NetworkManager profile %s has no recorded UUID; verify and migrate profile ownership before changing topology", name)
			}
			links[name] = link
		} else {
			section, name := "", ""
			for _, line := range strings.Split(string(data), "\n") {
				trim := strings.TrimSpace(line)
				indent := len(line) - len(strings.TrimLeft(line, " "))
				if indent == 2 && (trim == "ethernets:" || trim == "bonds:") {
					section = trim
					continue
				}
				if indent == 4 && strings.Contains(trim, ":") {
					name, _, _ = strings.Cut(trim, ":")
					link := links[name]
					link.Name = name
					link.Role = role
					link.MAC = byName[name].MAC
					link.Bond = section == "bonds:"
					links[name] = link
					continue
				}
				if name == "" {
					continue
				}
				link := links[name]
				switch {
				case indent == 8 && strings.HasPrefix(trim, "- ") && strings.Contains(trim, "/"):
					link.Address = strings.TrimPrefix(trim, "- ")
				case strings.HasPrefix(trim, "via:") && role == "mgmt":
					link.Gateway = strings.TrimSpace(strings.TrimPrefix(trim, "via:"))
				case strings.HasPrefix(trim, "mtu:"):
					link.MTU, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(trim, "mtu:")))
				case strings.HasPrefix(trim, "mode:") && link.Bond:
					link.BondMode = strings.TrimSpace(strings.TrimPrefix(trim, "mode:"))
				case strings.HasPrefix(trim, "interfaces: [") && link.Bond:
					members := strings.TrimSuffix(strings.TrimPrefix(trim, "interfaces: ["), "]")
					for _, member := range strings.Split(members, ",") {
						member = strings.TrimSpace(member)
						slave := links[member]
						slave.Master = name
						links[member] = slave
					}
				}
				links[name] = link
			}
		}
	}
	for _, link := range links {
		if !validTopologyLink(link) {
			return fmt.Errorf("legacy network ownership lacks verified physical mapping for %s", link.Name)
		}
		tx.Old.Topology.Links = append(tx.Old.Topology.Links, link)
	}
	tx.Old.Topology.Backend = backend
	for _, entry := range []struct{ path, kind string }{{managementUdevFile, "mgmt"}, {rdmaUdevFile, "rdma"}} {
		data, err := os.ReadFile(a.targetPath(entry.path))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if string(data) != renderUdevRulesForKind(saved.Bindings, entry.kind) {
			return fmt.Errorf("legacy udev file %s does not match saved bindings", entry.path)
		}
		tx.Old.Files[entry.path] = contentDigest(data)
		tx.Old.Roles[entry.path] = entry.kind
	}
	a.logf("migrated verified legacy network ownership: %d links", len(links))
	return nil
}
