package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

const networkTransactionFile = "/var/lib/envinit/network-transaction.json"
const networkManifestFile = "/var/lib/envinit/network-files.json"

// Old remains the last committed ownership record until both persistent and
// live configuration have been verified. Writes are journaled BEFORE replacing
// files, including when a process dies between rename(2) and saving progress.
type networkTransaction struct {
	Version      int               `json:"version"`
	Fingerprint  string            `json:"fingerprint"`
	Phase        string            `json:"phase"`
	Old          networkManifest   `json:"old"`
	Next         *networkManifest  `json:"next,omitempty"`
	Writes       map[string]string `json:"writes"`
	Retired      map[string]bool   `json:"retired"`
	Topology     networkTopology   `json:"topology"`
	PlannedFiles map[string]string `json:"planned_files,omitempty"`
	RestoreFiles map[string]string `json:"restore_files,omitempty"`
	Actions      []networkAction   `json:"actions,omitempty"`
}

type networkAction struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Target   string   `json:"target"`
	Status   string   `json:"status"`
	Error    string   `json:"error,omitempty"`
	Command  []string `json:"command,omitempty"`
	Before   string   `json:"before,omitempty"`
	After    string   `json:"after,omitempty"`
	Attempts int      `json:"attempts,omitempty"`
}

func (a *App) beginNetworkAction(kind, target string) (int, error) {
	if a.networkTransaction == nil {
		return -1, nil
	}
	for i := range a.networkTransaction.Actions {
		if a.networkTransaction.Actions[i].Kind == kind && a.networkTransaction.Actions[i].Target == target {
			a.networkTransaction.Actions[i].Status = "started"
			a.networkTransaction.Actions[i].Error = ""
			return i, a.saveNetworkTransaction()
		}
	}
	a.networkTransaction.Actions = append(a.networkTransaction.Actions, networkAction{ID: fmt.Sprintf("%s/%d", kind, len(a.networkTransaction.Actions)+1), Kind: kind, Target: target, Status: "started"})
	idx := len(a.networkTransaction.Actions) - 1
	return idx, a.saveNetworkTransaction()
}

func (a *App) finishNetworkAction(index int, err error) error {
	if index < 0 || a.networkTransaction == nil {
		return err
	}
	if err != nil {
		a.networkTransaction.Actions[index].Status = "failed"
		a.networkTransaction.Actions[index].Error = err.Error()
		if saveErr := a.saveNetworkTransaction(); saveErr != nil {
			return fmt.Errorf("%w; saving action failure: %v", err, saveErr)
		}
		return err
	}
	a.networkTransaction.Actions[index].Status = "done"
	a.networkTransaction.Actions[index].Error = ""
	if saveErr := a.saveNetworkTransaction(); saveErr != nil {
		return saveErr
	}
	return nil
}

// Always recheck the live condition, including for previously completed actions:
// a backend reload or reboot can recreate an object after its first retirement.
func (a *App) runNetworkCommand(kind, target, name string, args ...string) error {
	if a.DryRun || a.networkTransaction == nil {
		return a.runCmd("", nil, name, args...)
	}
	before, done, err := a.observeNetworkAction(kind, target, args)
	if err != nil {
		return err
	}
	index, err := a.beginNetworkAction(kind, target)
	if err != nil {
		return err
	}
	if index >= 0 {
		action := &a.networkTransaction.Actions[index]
		action.Command = append([]string{name}, args...)
		action.Before = before
		if !done {
			action.Attempts++
		}
		if err := a.saveNetworkTransaction(); err != nil {
			return err
		}
	}
	if done {
		return a.finishNetworkAction(index, nil)
	}
	commandErr := a.runCmd("", nil, name, args...)
	after, done, inspectErr := a.observeNetworkAction(kind, target, args)
	if index >= 0 {
		a.networkTransaction.Actions[index].After = after
	}
	// A rule command removes one duplicate at a time. Each iteration must make
	// observable progress; the outer loop checks complete absence.
	if kind == "rule-delete" && after != before && inspectErr == nil {
		done = true
	}
	if inspectErr != nil {
		return a.finishNetworkAction(index, inspectErr)
	}
	if !done {
		if commandErr != nil {
			return a.finishNetworkAction(index, commandErr)
		}
		return a.finishNetworkAction(index, fmt.Errorf("network action %s/%s did not converge: %s", kind, target, after))
	}
	return a.finishNetworkAction(index, nil)
}

// Backend activation is intentionally retried when runtime verification says
// that the previous activation did not converge. The action record documents
// the attempt, while verifyNetworkTopology remains the completion check.
func (a *App) runNetworkActivation(kind, target, name string, args ...string) error {
	return a.runNetworkActivationEnv(kind, target, nil, name, args...)
}

func (a *App) runNetworkActivationEnv(kind, target string, env map[string]string, name string, args ...string) error {
	if a.DryRun || a.networkTransaction == nil {
		return a.runCmd("", env, name, args...)
	}
	index, err := a.beginNetworkAction(kind, target)
	if err != nil {
		return err
	}
	a.networkTransaction.Actions[index].Command = append([]string{name}, args...)
	a.networkTransaction.Actions[index].Attempts++
	if err := a.saveNetworkTransaction(); err != nil {
		return err
	}
	err = a.runCmd("", env, name, args...)
	return a.finishNetworkAction(index, err)
}

func contentDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (a *App) saveNetworkTransaction() error {
	if a.DryRun || a.networkTransaction == nil {
		return nil
	}
	return a.writeNetworkState(networkTransactionFile, a.networkTransaction)
}

func (a *App) writeNetworkState(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	target := a.targetPath(path)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	return atomicWriteFile(target, append(data, '\n'), 0o600)
}

func (a *App) beginNetworkTransaction() error {
	copyApp := *a
	copyApp.Bundle.Defaults.ApplyNetworkImmediately = nil
	fingerprint, err := copyApp.applyConfigurationFingerprint()
	if err != nil {
		return err
	}
	tx := &networkTransaction{Version: 1, Fingerprint: fingerprint, Phase: "prepared", Writes: map[string]string{}, Retired: map[string]bool{}}
	data, err := os.ReadFile(a.targetPath(networkTransactionFile))
	if err == nil {
		if err := json.Unmarshal(data, tx); err != nil {
			return fmt.Errorf("read network transaction: %w", err)
		}
		if tx.Version != 1 || tx.Fingerprint != fingerprint {
			return fmt.Errorf("unfinished network transaction has a different configuration; resume its original inventory/bundle before changing targets (%s)", networkTransactionFile)
		}
		a.logf("resume network transaction: %s", tx.Phase)
	} else if !os.IsNotExist(err) {
		return err
	} else {
		data, err = os.ReadFile(a.targetPath(networkManifestFile))
		if err == nil {
			if err := json.Unmarshal(data, &tx.Old); err != nil {
				return err
			}
			if tx.Old.Version != 1 && tx.Old.Version != 2 {
				return fmt.Errorf("unsupported network manifest version %d", tx.Old.Version)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if tx.Writes == nil {
		tx.Writes = map[string]string{}
	}
	if tx.Retired == nil {
		tx.Retired = map[string]bool{}
	}
	switch tx.Phase {
	case "prepared", "retired", "files-written", "awaiting-runtime":
	default:
		return fmt.Errorf("unknown network transaction phase %q", tx.Phase)
	}
	if tx.RestoreFiles == nil {
		tx.RestoreFiles = map[string]string{}
	}
	if tx.Phase == "awaiting-runtime" && a.Bundle.ApplyNetworkImmediately() {
		tx.Phase = "prepared"
	}
	// Check SAME-PATH replacements as well as files that will be retired.
	for path, digest := range tx.Old.Files {
		if !ownedNetworkPath(path) && path != managementUdevFile && path != rdmaUdevFile {
			return fmt.Errorf("invalid network ownership path %s", path)
		}
		data, err := os.ReadFile(a.targetPath(path))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		actual := contentDigest(data)
		if actual != digest && actual != tx.Writes[path] && actual != tx.PlannedFiles[path] {
			return fmt.Errorf("network file %s has external edits; review before changing network", path)
		}
	}
	for path, digest := range tx.Writes {
		if !ownedNetworkPath(path) && path != managementUdevFile && path != rdmaUdevFile {
			return fmt.Errorf("invalid pending network path %s", path)
		}
		data, err := os.ReadFile(a.targetPath(path))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		actual := contentDigest(data)
		if actual != digest && actual != tx.Old.Files[path] && actual != tx.PlannedFiles[path] {
			return fmt.Errorf("pending network file %s has external edits", path)
		}
	}
	if tx.Old.Version == 1 && len(tx.Old.Topology.Links) == 0 && len(tx.Old.Files) != 0 {
		if err := a.migrateLegacyNetworkTopology(tx); err != nil {
			return err
		}
	}
	a.networkTransaction = tx
	return a.saveNetworkTransaction()
}

func (a *App) journalNetworkWrite(path string, data []byte) error {
	if a.networkTransaction == nil || (!ownedNetworkPath(path) && path != managementUdevFile && path != rdmaUdevFile) {
		return nil
	}
	tx := a.networkTransaction
	// Refuse a new target path already occupied by unknown content. Existing
	// identical generated content can be adopted without overwriting it.
	if tx.Old.Files[path] == "" && tx.Writes[path] == "" {
		existing, err := os.ReadFile(a.targetPath(path))
		if err == nil && contentDigest(existing) != contentDigest(data) {
			return fmt.Errorf("network target %s exists without ownership; review before replacement", path)
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	tx.Writes[path] = contentDigest(data)
	return a.saveNetworkTransaction()
}

func (a *App) finishNetworkTransaction() error {
	tx := a.networkTransaction
	if tx == nil || a.DryRun {
		return nil
	}
	if tx.Next == nil {
		return fmt.Errorf("network transaction has no prepared manifest")
	}
	if !a.Bundle.ApplyNetworkImmediately() {
		tx.Phase = "awaiting-runtime"
		a.logf("network files prepared; runtime convergence awaits reboot/apply")
		return a.saveNetworkTransaction()
	}
	if err := a.verifyNetworkTopology(tx.Topology); err != nil {
		return err
	}
	if err := a.verifyRetiredNetworkTopology(tx.Topology); err != nil {
		return err
	}
	for _, path := range []string{managementUdevFile, rdmaUdevFile} {
		if digest := tx.Writes[path]; digest != "" {
			tx.Next.Files[path] = digest
			role := "rdma"
			if path == managementUdevFile {
				role = "mgmt"
			}
			tx.Next.Roles[path] = role
		}
	}
	for path, digest := range tx.Next.Files {
		data, err := os.ReadFile(a.targetPath(path))
		if err != nil {
			return err
		}
		if contentDigest(data) != digest {
			return fmt.Errorf("network file %s changed before commit", path)
		}
	}
	tx.Next.Topology = tx.Topology
	tx.Next.Version = 2
	if err := a.writeNetworkState(networkManifestFile, tx.Next); err != nil {
		return err
	}
	if err := os.Remove(a.targetPath(networkTransactionFile)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(a.targetPath("/run/envinit-network-changing")); err != nil && !os.IsNotExist(err) {
		return err
	}
	a.networkTransaction = nil
	return nil
}

// Pre-render every managed file before retiring any live object.
func (a *App) preflightNetworkFiles(bindings []interfaceBinding) error {
	tx := a.networkTransaction
	if tx == nil {
		return nil
	}
	files := map[string]string{}
	if a.configureManagementNetwork() {
		files[managementUdevFile] = renderUdevRulesForKind(bindings, "mgmt")
		if a.usesIfcfgNetwork() {
			if len(a.Machine.MgmtIfaces) == 1 {
				name := a.Machine.MgmtIfaces[0]
				files[ifcfgPath(name)] = renderMgmtIfcfg(a.Machine, name, a.usesNetworkManager())
			} else {
				files[ifcfgPath(a.Machine.MgmtBondName)] = renderBondIfcfg(a.Machine, a.usesNetworkManager())
				for _, name := range a.Machine.MgmtIfaces {
					files[ifcfgPath(name)] = renderBondSlaveIfcfg(a.Machine, name, a.usesNetworkManager())
				}
			}
		} else {
			files[filepath.Join(netplanDir, "00-kunlun-bond.yaml")] = renderMgmtNetplan(a.Machine)
		}
	}
	if a.Bundle.RDMAExists() || tx.Old.Files[rdmaUdevFile] != "" {
		files[rdmaUdevFile] = renderUdevRulesForKind(bindings, "rdma")
	}
	if a.Bundle.RDMAConfigureIPRoute() {
		for _, item := range a.Machine.RDMA {
			if a.usesIfcfgNetwork() {
				files[ifcfgPath(item.Name)] = renderRDMAIfcfg(item, a.Machine.RDMAMTU, a.usesNetworkManager())
				files[ifcfgRoutePath(item.Name)] = renderIfcfgRoute(item, a.Machine.RouteCIDR)
				files[ifcfgRulePath(item.Name)] = renderIfcfgRule(item, a.Machine.RoutePriority)
			} else {
				files[filepath.Join(netplanDir, "10-kunlun-"+item.Name+".yaml")] = renderRDMANetplan(item, a.Machine.RDMAMTU)
			}
			files[a.routeScriptPath(item.Name)] = renderRouteScript(item, a.Machine.RouteCIDR, a.Machine.RoutePriority)
		}
		if a.usesNetworkManager() {
			files[nmDispatcherFile] = renderNetworkManagerDispatcher(a.Machine.RDMA)
		}
	}
	tx.PlannedFiles = map[string]string{}
	for path, content := range files {
		digest := contentDigest([]byte(content))
		data, err := os.ReadFile(a.targetPath(path))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && tx.Old.Files[path] == "" && tx.Writes[path] == "" && contentDigest(data) != digest {
			return fmt.Errorf("network target %s exists without ownership; review before replacement", path)
		}
		tx.PlannedFiles[path] = digest
		a.logf("network plan: ensure %s sha256=%s", path, digest[:12])
	}
	if !a.DryRun && !a.usesIfcfgNetwork() {
		if err := a.validateStagedNetplan(files); err != nil {
			return err
		}
	}
	return a.saveNetworkTransaction()
}

func (a *App) validateStagedNetplan(files map[string]string) error {
	base := a.targetPath("/var/lib/envinit")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return err
	}
	root, err := os.MkdirTemp(base, "netplan-validation-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	paths, err := filepath.Glob(a.targetPath(netplanDir + "/*.yaml"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		systemPath := filepath.Join(netplanDir, filepath.Base(path))
		if a.networkTransaction.Old.Files[systemPath] != "" {
			if _, desired := files[systemPath]; !desired && !(a.networkTransaction.Old.Roles[systemPath] == "mgmt" && !a.configureManagementNetwork()) {
				continue
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := atomicWriteFile(resolveTargetPath(root, systemPath), data, 0o600); err != nil {
			return err
		}
	}
	for path, content := range files {
		if filepath.Dir(path) == netplanDir {
			if err := atomicWriteFile(resolveTargetPath(root, path), []byte(content), 0o600); err != nil {
				return err
			}
		}
	}
	if err := a.runCmd("", nil, "netplan", "generate", "--root-dir", root); err != nil {
		return fmt.Errorf("new netplan configuration failed validation before live changes: %w", err)
	}
	return nil
}

// Save old contents before preventing route dispatchers from resurrecting
// retired objects. Desired scripts will replace these guards during rendering.
func (a *App) suspendOwnedRouteReplay() error {
	tx := a.networkTransaction
	if tx == nil || a.DryRun || !a.Bundle.ApplyNetworkImmediately() {
		return nil
	}
	if err := atomicWriteFile(a.targetPath("/run/envinit-network-changing"), []byte("network apply\n"), 0o600); err != nil {
		return err
	}
	unchanged := reflect.DeepEqual(tx.Old.Topology, tx.Topology)
	if unchanged {
		for _, old := range tx.Old.Runtime {
			found := false
			for _, next := range a.desiredOwnedRoutes() {
				found = found || old == next
			}
			if !found {
				unchanged = false
			}
		}
	}
	if unchanged {
		return nil
	}
	for path, digest := range tx.Old.Files {
		if path != nmDispatcherFile && filepath.Dir(path) != routeDir && filepath.Dir(path) != localSbinDir {
			continue
		}
		data, err := os.ReadFile(a.targetPath(path))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if _, saved := tx.RestoreFiles[path]; !saved {
			if contentDigest(data) != digest {
				return fmt.Errorf("route replay file %s changed before suspension", path)
			}
			tx.RestoreFiles[path] = string(data)
			if err := a.saveNetworkTransaction(); err != nil {
				return err
			}
		}
		if err := a.writeManagedFile(path, "#!/bin/sh\n# envinit network transaction in progress\nexit 0\n", 0o755); err != nil {
			return err
		}
	}
	return nil
}
