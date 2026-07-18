package checker

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"envinit/internal/spec"
)

func ResolveTargets(records []spec.MachineRecord, hostInputs []string) ([]Target, error) {
	return resolveTargets(records, hostInputs, true)
}

func ResolveDiscoveryTargets(records []spec.MachineRecord, hostInputs []string) ([]Target, error) {
	return resolveTargets(records, hostInputs, false)
}

func resolveTargets(records []spec.MachineRecord, hostInputs []string, requireInventoryMgmtIP bool) ([]Target, error) {
	var targets []Target
	seen := map[string]bool{}
	for _, raw := range hostInputs {
		for _, input := range splitHosts(raw) {
			target, err := resolveTarget(records, input, requireInventoryMgmtIP)
			if err != nil {
				return nil, err
			}
			key := strings.ToLower(targetControlAddress(target))
			if seen[key] {
				continue
			}
			seen[key] = true
			targets = append(targets, target)
		}
	}
	return targets, nil
}

func splitHosts(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '|' || r == ' ' || r == '\t' || r == '\n'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

func resolveTarget(records []spec.MachineRecord, input string, requireInventoryMgmtIP bool) (Target, error) {
	input = strings.TrimSpace(input)
	identity, endpoint, explicit, err := parseTargetInput(input)
	if err != nil {
		return Target{}, err
	}
	for _, record := range records {
		if matchesRecord(record, identity) {
			address := strings.TrimSpace(record.MgmtIP)
			controlAddress := address
			if explicit {
				controlAddress = endpoint
			}
			if controlAddress == "" && requireInventoryMgmtIP {
				return Target{}, fmt.Errorf("inventory record %q has no mgmt_ip; provide a reachable endpoint only with discover as %s=<ssh-address>", identity, identity)
			}
			if controlAddress == "" {
				controlAddress = endpoint
			}
			if address == "" {
				address = controlAddress
			}
			return Target{
				Input:             identity,
				Name:              firstNonEmpty(record.Hostname, record.HostID, record.MgmtIP),
				ExpectedHostname:  firstNonEmpty(record.Hostname, record.HostID),
				InventoryIdentity: firstNonEmpty(record.HostID, record.Hostname, record.MgmtIP),
				InventoryMatched:  true,
				ExplicitIdentity:  explicit,
				ControlAddress:    controlAddress,
				Address:           address,
				RDMA:              append([]spec.RDMARecord{}, record.RDMA...),
			}, nil
		}
	}
	if identity == "" {
		return Target{}, errors.New("empty host in --hosts")
	}
	if explicit {
		return Target{
			Input:             identity,
			Name:              identity,
			InventoryIdentity: identity,
			ExplicitIdentity:  true,
			ControlAddress:    endpoint,
			Address:           endpoint,
		}, nil
	}
	if net.ParseIP(endpoint) != nil {
		return Target{Input: endpoint, Name: endpoint, ControlAddress: endpoint, Address: endpoint}, nil
	}
	return Target{Input: endpoint, Name: endpoint, ControlAddress: endpoint, Address: endpoint}, nil
}

func parseTargetInput(input string) (identity, endpoint string, explicit bool, err error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", false, errors.New("empty host in --hosts")
	}
	if !strings.Contains(input, "=") {
		return input, input, false, nil
	}
	parts := strings.Split(input, "=")
	if len(parts) != 2 {
		return "", "", false, fmt.Errorf("invalid --hosts target %q; expected inventory-id=ssh-address", input)
	}
	identity = strings.TrimSpace(parts[0])
	endpoint = strings.TrimSpace(parts[1])
	if identity == "" || endpoint == "" {
		return "", "", false, fmt.Errorf("invalid --hosts target %q; both inventory identity and SSH address are required", input)
	}
	return identity, endpoint, true, nil
}

func targetControlAddress(target Target) string {
	return firstNonEmpty(target.ControlAddress, target.Address)
}

func matchesRecord(record spec.MachineRecord, input string) bool {
	input = strings.TrimSpace(input)
	for _, value := range []string{record.HostID, record.Hostname, record.MgmtIP} {
		if strings.EqualFold(strings.TrimSpace(value), input) {
			return true
		}
	}
	return false
}

func markLocalTargets(targets []Target) []Target {
	localIPs := localIPSet()
	localNames := localHostnameSet()
	for idx := range targets {
		address := strings.TrimSpace(targetControlAddress(targets[idx]))
		ip := net.ParseIP(address)
		if ip != nil && (ip.IsLoopback() || localIPs[ip.String()]) {
			targets[idx].Local = true
			continue
		}
		for _, name := range []string{targets[idx].Input, targets[idx].Name, targets[idx].ExpectedHostname, targetControlAddress(targets[idx])} {
			if localNames[strings.ToLower(strings.TrimSpace(name))] {
				targets[idx].Local = true
				break
			}
		}
	}
	return targets
}

func localHostnameSet() map[string]bool {
	out := map[string]bool{}
	hostname, err := os.Hostname()
	if err != nil {
		return out
	}
	addHostnameVariant(out, hostname)
	return out
}

func addHostnameVariant(out map[string]bool, value string) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return
	}
	out[value] = true
	if idx := strings.IndexByte(value, '.'); idx > 0 {
		out[value[:idx]] = true
	}
}

func localIPSet() map[string]bool {
	out := map[string]bool{}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, addr := range addrs {
		var ip net.IP
		switch value := addr.(type) {
		case *net.IPNet:
			ip = value.IP
		case *net.IPAddr:
			ip = value.IP
		}
		if ip == nil {
			continue
		}
		out[ip.String()] = true
	}
	return out
}

func warnHostnameMismatches(opts Options, targets []Target) {
	for _, target := range targets {
		expected := strings.TrimSpace(target.ExpectedHostname)
		if expected == "" {
			continue
		}
		actual, err := runCommand(opts.Bundle.Check, target, "hostnamectl --static 2>/dev/null || hostname")
		if err != nil {
			fmt.Fprintf(opts.Output, "WARN hostname check failed for %s (%s): %v; continuing\n", target.Name, targetControlAddress(target), err)
			continue
		}
		actual = strings.TrimSpace(actual)
		if actual == "" || strings.EqualFold(actual, expected) {
			continue
		}
		location := "remote"
		if target.Local {
			location = "local"
		}
		fmt.Fprintf(opts.Output, "WARN hostname mismatch: inventory %s expects hostname=%s, %s target %s reports %s; continuing because target is selected by control_address=%s\n", target.Name, expected, location, target.Name, actual, targetControlAddress(target))
	}
}
