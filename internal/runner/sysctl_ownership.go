package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const sysctlOwnershipFile = "/var/lib/envinit/sysctl-ownership.json"
const sysctlTransactionFile = "/var/lib/envinit/sysctl-transaction.json"

type ownedSysctl struct {
	Value           string   `json:"value"`
	OriginalLines   []string `json:"original_lines,omitempty"`
	OriginalRuntime string   `json:"original_runtime,omitempty"`
	MAC             string   `json:"mac,omitempty"`
	LegacyInferred  bool     `json:"legacy_inferred,omitempty"`
}

type sysctlOwnership struct {
	Version int                    `json:"version"`
	Entries map[string]ownedSysctl `json:"entries"`
	Unowned map[string]bool        `json:"unowned,omitempty"`
}

type sysctlTransaction struct {
	Version       int             `json:"version"`
	DesiredDigest string          `json:"desired_digest"`
	Before        string          `json:"before"`
	Content       string          `json:"content"`
	Old           sysctlOwnership `json:"old"`
	Next          sysctlOwnership `json:"next"`
}

func normalizedSysctlValue(value string) string { return strings.Join(strings.Fields(value), " ") }

func (a *App) readSysctlState(path string, value any) error {
	data, err := os.ReadFile(a.targetPath(path))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, value); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

func sysctlInterface(key string) string {
	parts := strings.Split(key, ".")
	if len(parts) == 5 && parts[0] == "net" && (parts[1] == "ipv4" || parts[1] == "ipv6") && parts[2] == "conf" {
		return parts[3]
	}
	return ""
}

func (a *App) sysctlIdentity(key string) string {
	iface := sysctlInterface(key)
	if iface == "" {
		return ""
	}
	mac, _ := a.readMAC(iface)
	return mac
}

func (a *App) procSysctlValue(key string) string {
	return normalizedSysctlValue(readOptionalTrim(a.targetPath(filepath.Join("/proc/sys", strings.ReplaceAll(key, ".", "/")))))
}

func (a *App) prepareOwnedSysctl() error {
	lines := desiredSysctlLines(a.Machine)
	desired := map[string]string{}
	identities := map[string]string{}
	for _, line := range lines {
		key, value, _ := parseSysctlLine(line)
		desired[key] = normalizedSysctlValue(value)
		identities[key] = a.sysctlIdentity(key)
	}
	raw, _ := json.Marshal(struct{ Values, Identities map[string]string }{desired, identities})
	digest := contentDigest(raw)
	existing, err := os.ReadFile(a.targetPath(sysctlFile))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var tx sysctlTransaction
	if err := a.readSysctlState(sysctlTransactionFile, &tx); err != nil {
		return err
	}
	if tx.Version != 0 {
		if tx.Version != 1 || tx.DesiredDigest != digest {
			return fmt.Errorf("unfinished sysctl transaction has different targets or physical bindings; resume original configuration")
		}
		if string(existing) != tx.Before && string(existing) != tx.Content {
			return fmt.Errorf("%s changed during unfinished sysctl transaction", sysctlFile)
		}
		return a.writeManagedFile(sysctlFile, tx.Content, 0o644)
	}
	old := sysctlOwnership{Version: 1, Entries: map[string]ownedSysctl{}}
	if err := a.readSysctlState(sysctlOwnershipFile, &old); err != nil {
		return err
	}
	fileLines := strings.Split(strings.ReplaceAll(string(existing), "\r\n", "\n"), "\n")
	legacyOwnershipMissing := false
	if _, statErr := os.Stat(a.targetPath(sysctlOwnershipFile)); os.IsNotExist(statErr) {
		legacyOwnershipMissing = true
		// A matching value is not evidence of ownership: distributions and
		// operators commonly use the same canonical values. Keep all historical
		// entries and require a human to establish ownership before retirement.
		a.logf("sysctl ownership history is missing; preserve existing parameters and review retired keys manually")
	}
	if old.Unowned == nil {
		old.Unowned = map[string]bool{}
	}
	if legacyOwnershipMissing {
		// Existing keys from an old deployment are not adopted merely because
		// their values match the current template. New keys absent from the file
		// can still be introduced and will receive explicit ownership evidence.
		for _, line := range fileLines {
			key, _, ok := parseSysctlLine(line)
			if ok && desired[key] != "" {
				old.Unowned[key] = true
			}
		}
	}
	if old.Version != 1 {
		return fmt.Errorf("unsupported sysctl ownership version %d", old.Version)
	}
	next := sysctlOwnership{Version: 1, Entries: map[string]ownedSysctl{}}
	next.Unowned = map[string]bool{}
	for key := range old.Unowned {
		next.Unowned[key] = true
	}
	for key, entry := range old.Entries {
		for _, line := range fileLines {
			k, v, ok := parseSysctlLine(line)
			if ok && k == key && normalizedSysctlValue(v) != entry.Value {
				return fmt.Errorf("owned sysctl %s has external edits", key)
			}
		}
	}
	for key, value := range desired {
		if old.Unowned[key] {
			next.Unowned[key] = true
			continue
		}
		entry, exists := old.Entries[key]
		if !exists {
			for _, line := range fileLines {
				k, _, ok := parseSysctlLine(line)
				if ok && k == key {
					entry.OriginalLines = append(entry.OriginalLines, line)
				}
			}
			entry.OriginalRuntime = a.procSysctlValue(key)
		} else if entry.MAC != identities[key] {
			entry.OriginalRuntime = a.procSysctlValue(key)
		}
		entry.Value = value
		entry.MAC = identities[key]
		next.Entries[key] = entry
	}
	seen := map[string]bool{}
	var out []string
	for _, line := range fileLines {
		key, _, ok := parseSysctlLine(line)
		if !ok {
			out = append(out, line)
			continue
		}
		if value, wanted := desired[key]; wanted {
			if old.Unowned[key] {
				if !seen[key] {
					out = append(out, line)
					seen[key] = true
				}
				continue
			}
			if !seen[key] {
				out = append(out, key+" = "+value)
				seen[key] = true
			}
			continue
		}
		if entry, owned := old.Entries[key]; owned {
			if !seen[key] {
				out = append(out, entry.OriginalLines...)
				seen[key] = true
			}
			continue
		}
		out = append(out, line)
	}
	for _, line := range lines {
		key, _, _ := parseSysctlLine(line)
		if !seen[key] {
			out = append(out, line)
			seen[key] = true
		}
	}
	content := strings.Join(out, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	tx = sysctlTransaction{Version: 1, DesiredDigest: digest, Before: string(existing), Content: content, Old: old, Next: next}
	if !a.DryRun {
		if err := a.writeNetworkState(sysctlTransactionFile, tx); err != nil {
			return err
		}
	}
	a.logf("sysctl reconciliation: previous keys=%d desired keys=%d", len(old.Entries), len(next.Entries))
	return a.writeManagedFile(sysctlFile, content, 0o644)
}

func (a *App) applyOwnedSysctl() error {
	if a.DryRun {
		a.logf("would verify/apply desired sysctl values and restore retired owned values when original runtime is known")
		return nil
	}
	var tx sysctlTransaction
	if err := a.readSysctlState(sysctlTransactionFile, &tx); err != nil {
		return err
	}
	if tx.Version != 1 {
		return fmt.Errorf("missing prepared sysctl transaction")
	}
	// Resolve retired interface keys by physical identity, even after A -> B
	// reuses the same logical interface name.
	byMAC, _, err := localInterfaceIndex(a.Root)
	if err != nil {
		return err
	}
	for key, old := range tx.Old.Entries {
		next, exists := tx.Next.Entries[key]
		if exists && next.MAC == old.MAC {
			continue
		}
		actualKey := key
		if iface := sysctlInterface(key); iface != "" {
			if old.MAC == "" {
				a.logf("retain unknown original runtime for %s", key)
				continue
			}
			name := byMAC[old.MAC]
			if name == "" {
				continue
			}
			actualKey = strings.Replace(key, ".conf."+iface+".", ".conf."+name+".", 1)
		}
		// A renamed port may now carry another desired RDMA role.
		if _, wanted := tx.Next.Entries[actualKey]; wanted {
			continue
		}
		if old.OriginalRuntime == "" {
			a.logf("original runtime unknown for %s; persistent entry retired only", actualKey)
			continue
		}
		current, err := a.captureCmd("", nil, "sysctl", "-n", actualKey)
		if err != nil {
			return err
		}
		current = normalizedSysctlValue(current)
		if current == old.OriginalRuntime {
			continue
		}
		if current != old.Value {
			return fmt.Errorf("retired sysctl %s runtime changed externally", actualKey)
		}
		if err := a.setAndVerifySysctl(actualKey, old.OriginalRuntime); err != nil {
			return err
		}
	}
	keys := make([]string, 0, len(tx.Next.Entries))
	for key := range tx.Next.Entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if tx.Next.Unowned[key] {
			continue
		}
		value := tx.Next.Entries[key].Value
		current, err := a.captureCmd("", nil, "sysctl", "-n", key)
		if err != nil {
			return err
		}
		if normalizedSysctlValue(current) != value {
			if err := a.setAndVerifySysctl(key, value); err != nil {
				return err
			}
		}
	}
	if err := a.writeNetworkState(sysctlOwnershipFile, tx.Next); err != nil {
		return err
	}
	return os.Remove(a.targetPath(sysctlTransactionFile))
}

func (a *App) setAndVerifySysctl(key, value string) error {
	if err := a.runCmd("", nil, "sysctl", "-w", key+"="+value); err != nil {
		return err
	}
	actual, err := a.captureCmd("", nil, "sysctl", "-n", key)
	if err != nil {
		return err
	}
	if normalizedSysctlValue(actual) != value {
		return fmt.Errorf("sysctl %s did not converge to %s", key, value)
	}
	return nil
}
