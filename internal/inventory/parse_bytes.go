package inventory

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"

	"envinit/internal/spec"
)

// ParseDelimitedBytes reads an offline planning table without touching the host.
// Unlike the legacy file loader, it rejects duplicate headers and ragged rows.
func ParseDelimitedBytes(data []byte) ([]spec.MachineRecord, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = detectDelimiter(data)
	r.TrimLeadingSpace = true
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("inventory: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("inventory is empty")
	}
	if err := validatePlanningHeaders(rows[0]); err != nil {
		return nil, err
	}
	records, err := parseRows(rows)
	if err != nil {
		return nil, err
	}
	legacySchema := false
	for _, raw := range rows[0] {
		if canonicalHeader(raw) == "host_id" {
			legacySchema = true
			break
		}
	}
	for idx := range records {
		records[idx].LegacySchema = legacySchema
		records[idx].FromInventory = true
	}
	return records, nil
}

func validatePlanningHeaders(headers []string) error {
	seen := map[string]bool{}
	for _, h := range headers {
		key := PlanningColumnName(h)
		if key == "" || seen[canonicalHeader(h)] {
			return fmt.Errorf("empty or duplicate inventory column %q", h)
		}
		seen[canonicalHeader(h)] = true
		// Catch misspelled reserved fields without prohibiting project metadata.
		known := map[string]bool{"mgmt_ip": true, "mgmt_prefix": true, "mgmt_gateway": true, "mgmt_target_name": true, "mgmt_bond_name": true, "mgmt_mode": true, "mgmt_iface1": true, "mgmt_iface2": true, "mgmt_mac1": true, "mgmt_mac2": true, "mgmt_nameservers": true, "mgmt_port1": true, "mgmt_port2": true, "mgmt_port1_mac": true, "mgmt_port2_mac": true}
		if strings.HasPrefix(key, "mgmt_") && !known[key] {
			return fmt.Errorf("unknown management column %q", h)
		}
		if strings.HasPrefix(key, "rdma") {
			rest := strings.TrimPrefix(key, "rdma")
			digits := strings.TrimRight(rest, "_abcdefghijklmnopqrstuvwxyz")
			n, e := strconv.Atoi(digits)
			if e != nil || n < 1 || n > 64 {
				return fmt.Errorf("RDMA column %q must use slot 1..64", h)
			}
			var probe spec.MachineRecord
			if !assignRDMAField(&probe, key, "") {
				return fmt.Errorf("unknown RDMA column %q", h)
			}
		}
	}
	return nil
}

// PlanningColumnName resolves aliases so a wizard cannot introduce two columns
// that write the same field. Metadata columns keep their original spelling.
func PlanningColumnName(raw string) string {
	k := canonicalHeader(raw)
	aliases := map[string]string{"host": "host_id", "node_id": "host_id", "asset_tag": "host_id", "node": "hostname", "machine": "hostname", "management_mode": "mgmt_mode", "bond_ip": "mgmt_ip", "management_ip": "mgmt_ip", "bond_prefix": "mgmt_prefix", "management_prefix": "mgmt_prefix", "bond_gateway": "mgmt_gateway", "management_gateway": "mgmt_gateway", "bond_name": "mgmt_target_name", "mgmt_bond_name": "mgmt_target_name", "management_target_name": "mgmt_target_name", "nameservers": "mgmt_nameservers", "dns": "mgmt_nameservers"}
	for i := 1; i <= 2; i++ {
		s := strconv.Itoa(i)
		for _, prefix := range []string{"mgmt_port", "bond_iface", "bond_port"} {
			aliases[prefix+s] = "mgmt_iface" + s
		}
		for _, prefix := range []string{"mgmt_port", "bond_iface", "bond_port"} {
			aliases[prefix+s+"_mac"] = "mgmt_mac" + s
		}
	}
	if v := aliases[k]; v != "" {
		return v
	}
	if strings.HasPrefix(k, "rdma") {
		if strings.HasSuffix(k, "_route_cidr") {
			return k
		}
		for old, newName := range map[string]string{"_iface": "_name", "_dev": "_name", "_rail": "_rail_id", "_cidr": "_route_cidr", "_network": "_route_cidr"} {
			if strings.HasSuffix(k, old) {
				return strings.TrimSuffix(k, old) + newName
			}
		}
		return k
	}
	if strings.HasPrefix(k, "mgmt_") || k == "host_id" || k == "hostname" {
		return k
	}
	return strings.TrimSpace(raw)
}
