package spec

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

// Compatibility is confined to decoding; serialized bundles use the canonical
// schema. Raw presence is significant for false, zero and empty arrays.
func (b *Bundle) UnmarshalJSON(data []byte) error {
	type plain Bundle
	var decoded plain
	if err := decodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == nil {
		return fmt.Errorf("bundle must be a JSON object")
	}
	warn := func(old, replacement string) {
		decoded.Warnings = append(decoded.Warnings, fmt.Sprintf("%s is deprecated; use %s", old, replacement))
	}
	if old, ok := raw["offline_apt"]; ok {
		warn("offline_apt", "offline_repo")
		merged, err := mergeJSONObjects(raw["offline_repo"], old, "offline_repo", "offline_apt")
		if err != nil {
			return err
		}
		if err := decodeStrictJSON(merged, &decoded.OfflineRepo); err != nil {
			return err
		}
	}
	decoded.OfflineAPT = OfflineAPTConfig{}
	var defaults map[string]json.RawMessage
	if err := json.Unmarshal(raw["defaults"], &defaults); len(raw["defaults"]) > 0 && err != nil {
		return err
	}
	if defaults == nil {
		defaults = map[string]json.RawMessage{}
	}
	for _, key := range []string{"mgmt_bond_name", "mgmt_interfaces", "mgmt_prefix", "mgmt_gateway", "rdma_interfaces", "rdma_prefix", "rdma_route_cidr"} {
		if _, ok := defaults[key]; ok {
			warn("defaults."+key, "inventory per-machine fields")
		}
	}
	var platforms map[string]map[string]json.RawMessage
	if value, ok := raw["platform_options"]; ok {
		if err := json.Unmarshal(value, &platforms); err != nil {
			return err
		}
		for _, platform := range []string{"ubuntu", "redhat", "kylin"} {
			keys := make([]string, 0, len(platforms[platform]))
			for key := range platforms[platform] {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				value := platforms[platform][key]
				old := "platform_options." + platform + "." + key
				warn(old, "defaults."+key)
				if current, ok := defaults[key]; ok && !equalJSON(current, value) {
					return fmt.Errorf("conflicting defaults.%s and %s", key, old)
				}
				defaults[key] = value
			}
		}
		encoded, _ := json.Marshal(defaults)
		if err := decodeStrictJSON(encoded, &decoded.Defaults); err != nil {
			return err
		}
	}
	decoded.PlatformOptions = PlatformOptions{}
	var check map[string]json.RawMessage
	if value := raw["check"]; len(value) > 0 {
		if err := json.Unmarshal(value, &check); err != nil {
			return err
		}
		for key := range check {
			if key != "bandwidth" && key != "rdma_ping" && key != "ssh" && key != "xccl" {
				warn("check."+key, "nested check configuration")
			}
		}
	}
	sort.Strings(decoded.Warnings)
	*b = Bundle(decoded)
	return nil
}

func equalJSON(a, b json.RawMessage) bool {
	var left, right any
	return json.Unmarshal(a, &left) == nil && json.Unmarshal(b, &right) == nil && reflect.DeepEqual(left, right)
}

// EffectiveOfflineRepo confines the fallback for directly constructed legacy
// bundles to the spec boundary. Decoded JSON has already been normalized.
func (b Bundle) EffectiveOfflineRepo() OfflineAPTConfig {
	if reflect.DeepEqual(b.OfflineRepo, OfflineAPTConfig{}) {
		return b.OfflineAPT
	}
	return b.OfflineRepo
}

func mergeJSONObjects(current, old json.RawMessage, currentPath, oldPath string) ([]byte, error) {
	values := map[string]json.RawMessage{}
	legacy := map[string]json.RawMessage{}
	if len(current) > 0 {
		if err := json.Unmarshal(current, &values); err != nil {
			return nil, err
		}
	}
	if len(old) > 0 {
		if err := json.Unmarshal(old, &legacy); err != nil {
			return nil, err
		}
	}
	if values == nil {
		values = map[string]json.RawMessage{}
	}
	for key, value := range legacy {
		if existing, ok := values[key]; ok && !equalJSON(existing, value) {
			return nil, fmt.Errorf("conflicting %s.%s and %s.%s", currentPath, key, oldPath, key)
		}
		values[key] = value
	}
	return json.Marshal(values)
}

func (b Bundle) MarshalJSON() ([]byte, error) {
	type plain Bundle
	encoded, err := json.Marshal(plain(b))
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		return nil, err
	}
	delete(raw, "offline_apt")
	delete(raw, "platform_options")
	raw["offline_repo"], _ = json.Marshal(b.EffectiveOfflineRepo())
	var defaults map[string]json.RawMessage
	if err := json.Unmarshal(raw["defaults"], &defaults); err != nil {
		return nil, err
	}
	for _, key := range []string{"mgmt_bond_name", "mgmt_interfaces", "mgmt_prefix", "mgmt_gateway", "rdma_interfaces", "rdma_prefix", "rdma_route_cidr"} {
		delete(defaults, key)
	}
	raw["defaults"], _ = json.Marshal(defaults)
	return json.Marshal(raw)
}
