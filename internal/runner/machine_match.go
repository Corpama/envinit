package runner

import (
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"sort"
	"strings"

	"envinit/internal/spec"
)

func matchMachine(records []spec.MachineRecord, host string, root string, localMACs []string) (spec.MachineRecord, error) {
	if strings.TrimSpace(host) != "" {
		for _, record := range records {
			if matchesRecord(record, host) {
				return record, nil
			}
		}
		return spec.MachineRecord{}, fmt.Errorf("inventory does not contain host %q", host)
	}

	hostname, _ := os.Hostname()
	localIPs := localIPv4s()

	var hostnameMatches []spec.MachineRecord
	for _, record := range records {
		if hostname != "" && (strings.EqualFold(record.Hostname, hostname) || strings.EqualFold(record.HostID, hostname)) {
			hostnameMatches = append(hostnameMatches, record)
		}
	}
	if len(hostnameMatches) == 1 {
		return hostnameMatches[0], nil
	}

	var ipMatches []spec.MachineRecord
	for _, record := range records {
		if slices.Contains(localIPs, strings.TrimSpace(record.MgmtIP)) {
			ipMatches = append(ipMatches, record)
			continue
		}
		for _, item := range record.RDMA {
			if slices.Contains(localIPs, strings.TrimSpace(item.IP)) {
				ipMatches = append(ipMatches, record)
				break
			}
		}
	}
	if len(ipMatches) == 1 {
		return ipMatches[0], nil
	}

	var macMatches []spec.MachineRecord
	for _, record := range records {
		for _, mac := range recordMACs(record) {
			if slices.Contains(localMACs, mac) {
				macMatches = append(macMatches, record)
				break
			}
		}
	}
	if len(macMatches) == 1 {
		return macMatches[0], nil
	}

	return spec.MachineRecord{}, errors.New("failed to auto-match the current machine; specify --host with a host_id/hostname/mgmt_ip from the inventory, or add MAC addresses to the inventory")
}

func matchesRecord(record spec.MachineRecord, host string) bool {
	host = strings.TrimSpace(host)
	if strings.EqualFold(strings.TrimSpace(record.HostID), host) {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(record.Hostname), host) {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(record.MgmtIP), host) {
		return true
	}
	for _, mac := range recordMACs(record) {
		if strings.EqualFold(mac, host) {
			return true
		}
	}
	return false
}

func localIPv4s() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP == nil || ipNet.IP.To4() == nil {
				continue
			}
			out = append(out, ipNet.IP.String())
		}
	}
	sort.Strings(out)
	return out
}

func recordMACs(record spec.MachineRecord) []string {
	out := make([]string, 0, 6)
	for _, raw := range []string{record.MgmtMAC1, record.MgmtMAC2} {
		if mac, err := spec.NormalizeMAC(raw); err == nil && mac != "" {
			out = append(out, mac)
		}
	}
	for _, item := range record.RDMA {
		if mac, err := spec.NormalizeMAC(item.MAC); err == nil && mac != "" {
			out = append(out, mac)
		}
	}
	return out
}
