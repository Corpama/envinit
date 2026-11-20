package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func commandValue(args []string, key string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key {
			return args[i+1]
		}
	}
	return ""
}

func positionalAfter(args []string, token string) string {
	for i := range args[:max(0, len(args)-1)] {
		if args[i] == token {
			return args[i+1]
		}
	}
	return ""
}

// The observation is saved on both sides of the command. Ownership validation
// happens in the caller; these conditions verify the exact command's effect.
func (a *App) observeNetworkAction(kind, target string, args []string) (string, bool, error) {
	dev := commandValue(args, "dev")
	switch kind {
	case "address-delete":
		if !a.interfaceExists(dev) {
			return "absent", true, nil
		}
		addresses, err := a.interfaceAddresses(dev)
		address := strings.TrimPrefix(target, dev+"/")
		return strings.Join(addresses, ","), !containsString(addresses, address), err
	case "member-detach":
		master := a.interfaceMaster(dev)
		oldMaster := strings.TrimPrefix(target, dev+"/")
		if master != "" && master != oldMaster {
			return master, false, fmt.Errorf("%s now has external master %s", dev, master)
		}
		return master, master == "", nil
	case "bond-delete":
		if !a.interfaceExists(dev) {
			return "absent", true, nil
		}
		if !a.pathExists(filepath.Join("/sys/class/net", dev, "bonding")) {
			return "", false, fmt.Errorf("%s is no longer a bond", dev)
		}
		members := readOptionalTrim(a.targetPath(filepath.Join("/sys/class/net", dev, "bonding/slaves")))
		addresses, err := a.interfaceAddresses(dev)
		if err != nil {
			return "", false, err
		}
		// The precondition is intentionally allowed here: retireNetworkTopology
		// detaches members and removes addresses immediately before deleting the
		// bond. A second observation after the command must see the link absent.
		return fmt.Sprintf("members=%s addresses=%s", members, strings.Join(addresses, ",")), false, nil
	case "nm-profile-delete":
		out, err := a.captureCmd("", nil, "nmcli", "-t", "-f", "UUID", "connection", "show")
		return out, !containsString(strings.Fields(out), target), err
	case "ifdown", "link-down", "link-up":
		if kind == "ifdown" {
			dev = target
		}
		data, err := os.ReadFile(a.targetPath(filepath.Join("/sys/class/net", dev, "flags")))
		if os.IsNotExist(err) {
			return "absent", kind != "link-up", nil
		}
		if err != nil {
			return "", false, err
		}
		flags, err := strconv.ParseUint(strings.TrimSpace(string(data)), 0, 32)
		up := flags&1 != 0
		return strings.TrimSpace(string(data)), up == (kind == "link-up"), err
	case "link-rename":
		// Target contains the permanent MAC and final name, not the temporary
		// name that may disappear between executing ip and saving the journal.
		mac, want, _ := strings.Cut(target, "/")
		byMAC, _, err := localInterfaceIndex(a.Root)
		actual := byMAC[strings.ToLower(mac)]
		if err == nil && actual != want && actual != dev {
			err = fmt.Errorf("rename identity %s is at %q, expected %s or %s", mac, actual, dev, want)
		}
		return actual, actual == want, err
	case "rule-delete":
		table, _ := strconv.Atoi(commandValue(args, "table"))
		priority, _ := strconv.Atoi(commandValue(args, "priority"))
		item := ownedRouteObject{IP: strings.TrimSuffix(commandValue(args, "from"), "/32"), Device: commandValue(args, "oif"), Table: table, Priority: priority}
		out, err := a.captureCmd("", nil, "ip", "rule", "show")
		if err != nil {
			return "", false, err
		}
		if err := rejectAmbiguousRuleDeletion(out, item, item.Device != ""); err != nil {
			return "", false, err
		}
		count := countOwnedRules(out, item, item.Device != "")
		return strconv.Itoa(count), count == 0, nil
	case "route-delete", "default-route-delete":
		table := commandValue(args, "table")
		if table == "" {
			table = "main"
		}
		show := []string{"route", "show", "table", table}
		if kind == "default-route-delete" {
			show = []string{"-4", "route", "show", "default", "dev", dev}
		}
		out, err := a.captureCmd("", nil, "ip", show...)
		if err != nil && strings.Contains(err.Error(), "FIB table does not exist") {
			return "absent", true, nil
		}
		if err != nil {
			return "", false, err
		}
		var wantedFields []string
		for i := 0; i < len(args); i++ {
			if args[i] == "table" && i+1 < len(args) {
				break
			}
			if args[i] == "-4" || args[i] == "route" || args[i] == "del" {
				continue
			}
			wantedFields = append(wantedFields, args[i])
		}
		wanted := strings.Join(wantedFields, " ")
		for _, line := range strings.Split(out, "\n") {
			if containsTokenSubsequenceLine(line, wanted) {
				return line, false, nil
			}
		}
		return "absent", true, nil
	default:
		return "", false, fmt.Errorf("missing completion check for network action %s", kind)
	}
}
