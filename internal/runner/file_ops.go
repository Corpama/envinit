package runner

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"envinit/internal/spec"
	"path/filepath"
)

type treeEntry struct {
	path  string
	isDir bool
	size  int64
	mode  fs.FileMode
}

var legacySiblingBackupPattern = regexp.MustCompile(`\.bak\.\d{8}_\d{6}(?:\.\d+)?$`)

func resolveTargetPath(root string, systemPath string) string {
	if root == "/" || root == "" {
		return systemPath
	}
	clean := strings.TrimPrefix(systemPath, "/")
	return filepath.Join(root, clean)
}

func localInterfaceIndex(root string) (map[string]string, []string, error) {
	dir := resolveTargetPath(root, "/sys/class/net")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]string{}, nil, nil
		}
		return nil, nil, fmt.Errorf("read %s: %w", dir, err)
	}
	ifaceByMAC := map[string]string{}
	localMACs := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		// Do not let a bond/team/bridge runtime MAC shadow the permanent MAC
		// of one of its physical members during machine resolution.
		if aggregateInterface(dir, name) {
			continue
		}
		mac, ok := readSysfsInterfaceMAC(dir, name)
		if !ok {
			continue
		}
		if mac == "" {
			continue
		}
		ifaceByMAC[mac] = name
		localMACs = append(localMACs, mac)
	}
	sort.Strings(localMACs)
	return ifaceByMAC, localMACs, nil
}

func aggregateInterface(netDir, name string) bool {
	for _, marker := range []string{"bonding", "bridge", "team"} {
		if _, err := os.Stat(filepath.Join(netDir, name, marker)); err == nil {
			return true
		}
	}
	return false
}

// A bond can propagate its runtime MAC to every enslaved physical interface.
// perm_address remains the stable identity of the physical NIC.
func readSysfsInterfaceMAC(netDir, name string) (string, bool) {
	// For an enslaved NIC, bonding exposes the original hardware address at
	// /sys/class/net/<slave>/bonding_slave/perm_hwaddr. The ordinary address
	// file may instead expose the bond's runtime MAC.
	permHWAddr := filepath.Join(netDir, name, "bonding_slave", "perm_hwaddr")
	if data, err := os.ReadFile(permHWAddr); err == nil {
		if mac, err := spec.NormalizeMAC(strings.TrimSpace(string(data))); err == nil && mac != "" {
			return mac, true
		}
	}
	for _, filename := range []string{"perm_address", "address"} {
		data, err := os.ReadFile(filepath.Join(netDir, name, filename))
		if err != nil {
			continue
		}
		mac, err := spec.NormalizeMAC(strings.TrimSpace(string(data)))
		if err == nil && mac != "" {
			return mac, true
		}
	}
	return "", false
}

func (a *App) disableExistingNetplan() error {
	targets := a.netplanBackupTargets()
	if len(targets) == 0 {
		a.logf("skip backing up existing netplan files because no netplan network files will be rewritten")
		return nil
	}
	// Each changed file is backed up by writeManagedFile immediately before
	// atomic replacement. Never remove live files ahead of rendering.
	return nil
}

func (a *App) netplanBackupTargets() []string {
	targets := []string{}
	if a.configureManagementNetwork() {
		targets = append(targets, filepath.Join(netplanDir, "00-kunlun-bond.yaml"))
	}
	if !a.Bundle.RDMAConfigureIPRoute() {
		return targets
	}
	for _, item := range a.Machine.RDMA {
		targets = append(targets, filepath.Join(netplanDir, fmt.Sprintf("10-kunlun-%s.yaml", item.Name)))
	}
	return targets
}

func (a *App) disableExistingAptSources() error {
	patterns := []string{
		a.targetPath("/etc/apt/sources.list"),
		filepath.Join(a.targetPath("/etc/apt/sources.list.d"), "*.list"),
		filepath.Join(a.targetPath("/etc/apt/sources.list.d"), "*.sources"),
	}
	target := a.targetPath(a.offlineRepoConfig().TargetFile)
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return err
		}
		for _, match := range matches {
			if match == target {
				continue
			}
			if err := a.moveToBackup(match); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *App) disableExistingYumRepos(targetFile string) error {
	pattern := filepath.Join(a.targetPath("/etc/yum.repos.d"), "*.repo")
	target := a.targetPath(targetFile)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	for _, match := range matches {
		if match == target {
			continue
		}
		if err := a.moveToBackup(match); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) disableExistingIfcfg() error {
	targets := a.ifcfgBackupTargets()
	if len(targets) == 0 {
		a.logf("skip backing up existing ifcfg files because no ifcfg network files will be rewritten")
		return nil
	}
	return nil
}

func (a *App) relocateLegacyNetworkBackups() error {
	patterns := []string{
		filepath.Join(a.targetPath(netplanDir), "*.bak.*"),
		filepath.Join(a.targetPath(networkScriptsDir), "ifcfg-*.bak.*"),
		filepath.Join(a.targetPath(networkScriptsDir), "route-*.bak.*"),
		filepath.Join(a.targetPath(networkScriptsDir), "rule-*.bak.*"),
	}
	seen := map[string]bool{}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return fmt.Errorf("scan legacy network backups %s: %w", pattern, err)
		}
		for _, match := range matches {
			if seen[match] || !legacySiblingBackupPattern.MatchString(filepath.Base(match)) {
				continue
			}
			seen[match] = true
			if err := a.moveToBackup(match); err != nil {
				return fmt.Errorf("relocate legacy network backup %s: %w", match, err)
			}
		}
	}
	var unknown []string
	for _, pattern := range []string{
		filepath.Join(a.targetPath(netplanDir), "*.bak*"),
		filepath.Join(a.targetPath(networkScriptsDir), "ifcfg-*.bak*"),
		filepath.Join(a.targetPath(networkScriptsDir), "route-*.bak*"),
		filepath.Join(a.targetPath(networkScriptsDir), "rule-*.bak*"),
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return fmt.Errorf("scan possible network backups %s: %w", pattern, err)
		}
		for _, match := range matches {
			if seen[match] || legacySiblingBackupPattern.MatchString(filepath.Base(match)) {
				continue
			}
			unknown = append(unknown, match)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("needs attention: possible network configuration backups may be loaded by the active backend; envinit did not move files outside its exact legacy backup convention: %s", strings.Join(unknown, ", "))
	}
	return nil
}

func (a *App) ifcfgBackupTargets() []string {
	targets := []string{}
	if a.configureManagementNetwork() {
		if len(a.Machine.MgmtIfaces) == 1 {
			targets = append(targets, ifcfgPath(a.Machine.MgmtIfaces[0]))
		} else {
			targets = append(targets, ifcfgPath(a.Machine.MgmtBondName))
			for _, iface := range a.Machine.MgmtIfaces {
				targets = append(targets, ifcfgPath(iface))
			}
		}
	}
	if !a.Bundle.RDMAConfigureIPRoute() {
		return targets
	}
	for _, item := range a.Machine.RDMA {
		targets = append(targets, ifcfgPath(item.Name), ifcfgRoutePath(item.Name), ifcfgRulePath(item.Name))
	}
	return targets
}

func (a *App) backupExistingTargets(targets []string) error {
	seen := map[string]bool{}
	for _, target := range targets {
		path := a.targetPath(target)
		if seen[path] {
			continue
		}
		seen[path] = true
		if err := a.moveToBackup(path); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) moveToBackup(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect backup source %s: %w", path, err)
	}
	now := a.now
	if now == nil {
		now = time.Now
	}
	backup, err := a.nextBackupPath(path, now())
	if err != nil {
		return err
	}
	if info.IsDir() && pathContains(path, backup) {
		return fmt.Errorf("backup root must not be inside directory being backed up: %s -> %s", path, backup)
	}
	a.logf("backup %s -> %s", path, backup)
	if a.DryRun {
		a.managedFileRevision++
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
		return fmt.Errorf("create backup directory for %s: %w", path, err)
	}
	if err := os.Rename(path, backup); err != nil {
		if errors.Is(err, syscall.EXDEV) && info.Mode().IsRegular() {
			if err := a.copyToBackup(path); err != nil {
				return err
			}
			if err := os.Remove(path); err != nil {
				return err
			}
			a.managedFileRevision++
			return nil
		}
		return fmt.Errorf("move %s to backup %s: %w", path, backup, err)
	}
	a.managedFileRevision++
	return nil
}

func (a *App) nextBackupPath(path string, at time.Time) (string, error) {
	rel, err := a.backupRelativePath(path)
	if err != nil {
		return "", err
	}
	backupRoot := strings.TrimSpace(a.Bundle.Defaults.BackupRoot)
	if backupRoot == "" {
		backupRoot = defaultBackupRoot
	}
	base := filepath.Join(a.targetPath(backupRoot), at.Format("20060102_150405"), rel)
	for suffix := 0; ; suffix++ {
		candidate := base
		if suffix > 0 {
			candidate = fmt.Sprintf("%s.%d", base, suffix)
		}
		if _, err := os.Lstat(candidate); errors.Is(err, fs.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", fmt.Errorf("inspect backup target %s: %w", candidate, err)
		}
	}
}

func (a *App) backupRelativePath(path string) (string, error) {
	cleanPath := filepath.Clean(path)
	if a.Root != "" && a.Root != "/" {
		cleanRoot := filepath.Clean(a.Root)
		rel, err := filepath.Rel(cleanRoot, cleanPath)
		if err != nil {
			return "", fmt.Errorf("resolve backup source %s relative to root %s: %w", path, a.Root, err)
		}
		if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return "", fmt.Errorf("backup source %s is outside alternate root %s", path, a.Root)
		}
		return rel, nil
	}
	if !filepath.IsAbs(cleanPath) {
		return "", fmt.Errorf("backup source %s must be an absolute path", path)
	}
	rel := strings.TrimPrefix(cleanPath, string(os.PathSeparator))
	if rel == "" {
		return "", fmt.Errorf("refuse to back up filesystem root")
	}
	return rel, nil
}

func pathContains(parent string, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func (a *App) writeManagedFile(systemPath string, content string, mode fs.FileMode) error {
	if err := a.journalNetworkWrite(systemPath, []byte(content)); err != nil {
		return err
	}
	target := a.targetPath(systemPath)
	if !a.DryRun {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(target), err)
		}
	}
	if existing, err := os.ReadFile(target); err == nil {
		if bytes.Equal(existing, []byte(content)) {
			info, err := os.Lstat(target)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("refuse non-regular configuration %s", target)
			}
			if info.Mode().Perm() != mode.Perm() && !a.DryRun {
				if err := os.Chmod(target, mode.Perm()); err != nil {
					return err
				}
				a.managedFileRevision++
			}
			a.logf("unchanged %s", systemPath)
			return nil
		}
		if err := a.copyToBackup(target); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	a.logf("write %s", systemPath)
	if a.DryRun {
		// Treat the planned write as a change so callers also describe the
		// reload/restart that would follow a real replacement.
		a.managedFileRevision++
		return nil
	}
	if err := atomicWriteFile(target, []byte(content), mode); err != nil {
		return fmt.Errorf("write %s: %w", systemPath, err)
	}
	a.managedFileRevision++
	return os.Chmod(target, mode)
}

func (a *App) removeFileIfExists(path string) error {
	a.logf("remove %s if exists", path)
	if a.DryRun {
		return nil
	}
	if err := os.Remove(a.targetPath(path)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

func (a *App) movePath(source string, target string) error {
	sourcePath := a.targetPath(source)
	targetPath := a.targetPath(target)
	if filepath.Clean(sourcePath) == filepath.Clean(targetPath) {
		a.logf("already satisfied: move %s -> %s", source, target)
		return nil
	}
	a.logf("move %s -> %s", source, target)
	if a.DryRun {
		return nil
	}
	sourceInfo, err := os.Lstat(sourcePath)
	if err != nil {
		return fmt.Errorf("inspect move source %s: %w", source, err)
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(targetPath), err)
	}
	rollback := ""
	if targetInfo, err := os.Lstat(targetPath); err == nil {
		if targetInfo.IsDir() {
			if err := a.copyDirectoryToBackup(targetPath); err != nil {
				return err
			}
		} else if targetInfo.Mode().IsRegular() {
			if err := a.copyToBackup(targetPath); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("refuse to replace non-regular move target %s", target)
		}
		rollback = filepath.Join(filepath.Dir(targetPath), fmt.Sprintf(".envinit-old-%d", time.Now().UnixNano()))
		if err := os.Rename(targetPath, rollback); err != nil {
			return fmt.Errorf("prepare move target %s: %w", target, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := placeMovedPath(sourcePath, targetPath, sourceInfo); err != nil {
		if rollback != "" {
			if restoreErr := os.Rename(rollback, targetPath); restoreErr != nil {
				return fmt.Errorf("move %s to %s: %w; restore failed: %v", source, target, err, restoreErr)
			}
		}
		return fmt.Errorf("move %s to %s: %w", source, target, err)
	}
	if rollback != "" {
		if err := os.RemoveAll(rollback); err != nil {
			return err
		}
	}
	return nil
}

func placeMovedPath(source, target string, info fs.FileInfo) error {
	if err := os.Rename(source, target); err == nil {
		return nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	if info.Mode().IsRegular() {
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		if err := atomicWriteFile(target, data, info.Mode()); err != nil {
			return err
		}
		return os.Remove(source)
	}
	if !info.IsDir() {
		return fmt.Errorf("refuse cross-filesystem move of non-regular path %s", source)
	}
	staged, err := os.MkdirTemp(filepath.Dir(target), ".envinit-move-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staged)
	if err := copyDirectoryTree(source, staged); err != nil {
		return err
	}
	if err := os.Rename(staged, target); err != nil {
		return err
	}
	return os.RemoveAll(source)
}

func (a *App) removePath(path string) error {
	target := a.targetPath(path)
	a.logf("remove %s", path)
	if a.DryRun {
		return nil
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

func (a *App) mkdirPath(path string, mode fs.FileMode) error {
	target := a.targetPath(path)
	a.logf("mkdir %s", path)
	if a.DryRun {
		return nil
	}
	if err := os.MkdirAll(target, mode); err != nil {
		return fmt.Errorf("mkdir %s: %w", path, err)
	}
	return os.Chmod(target, mode)
}

func (a *App) chmodPath(path string, mode fs.FileMode) error {
	a.logf("chmod %04o %s", mode.Perm(), path)
	if a.DryRun {
		return nil
	}
	if err := os.Chmod(a.targetPath(path), mode); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

func (a *App) copyMaterial(source string, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("stat material %s: %w", source, err)
	}
	targetPath := a.targetPath(target)
	if info.IsDir() {
		return a.copyDirWithBackup(source, targetPath)
	}
	return a.copyFileWithBackup(source, targetPath, info.Mode())
}

func (a *App) copyDirWithBackup(source string, target string) error {
	exists := false
	if info, err := os.Lstat(target); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("refuse to replace non-directory target %s with directory material", target)
		}
		exists = true
		same, err := dirTreesEqual(source, target)
		if err != nil {
			return err
		}
		if same {
			a.logf("unchanged %s", target)
			return nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	a.logf("copy %s -> %s", source, target)
	if a.DryRun {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	staged, err := os.MkdirTemp(filepath.Dir(target), ".envinit-dir-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staged)
	if err := copyDirectoryTree(source, staged); err != nil {
		return fmt.Errorf("stage directory %s: %w", source, err)
	}
	rollback := ""
	if exists {
		if err := a.copyDirectoryToBackup(target); err != nil {
			return err
		}
		rollback = filepath.Join(filepath.Dir(target), ".envinit-old-"+filepath.Base(staged))
		if err := os.Rename(target, rollback); err != nil {
			return fmt.Errorf("prepare directory replacement %s: %w", target, err)
		}
	}
	if err := os.Rename(staged, target); err != nil {
		if rollback != "" {
			if restoreErr := os.Rename(rollback, target); restoreErr != nil {
				return fmt.Errorf("replace directory %s: %w; restore failed: %v", target, err, restoreErr)
			}
		}
		return fmt.Errorf("replace directory %s: %w", target, err)
	}
	if rollback != "" {
		if err := os.RemoveAll(rollback); err != nil {
			return fmt.Errorf("remove replaced directory %s: %w", rollback, err)
		}
	}
	a.managedFileRevision++
	return nil
}

func copyDirectoryTree(source string, target string) error {
	return filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dest := target
		if rel != "." {
			dest = filepath.Join(target, rel)
		}
		if d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			if err := os.MkdirAll(dest, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chmod(dest, info.Mode().Perm())
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refuse non-regular material %s", path)
		}
		return copyFileContents(path, dest, info.Mode())
	})
}

func (a *App) copyDirectoryToBackup(path string) error {
	now := time.Now
	if a.now != nil {
		now = a.now
	}
	dest, err := a.nextBackupPath(path, now())
	if err != nil {
		return err
	}
	if pathContains(path, dest) {
		return fmt.Errorf("backup root must not be inside directory being backed up: %s -> %s", path, dest)
	}
	a.logf("backup copy %s -> %s", path, dest)
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	staged, err := os.MkdirTemp(filepath.Dir(dest), ".envinit-backup-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staged)
	if err := copyDirectoryTree(path, staged); err != nil {
		return err
	}
	return os.Rename(staged, dest)
}

func (a *App) copyFileWithBackup(source string, target string, mode fs.FileMode) error {
	if targetInfo, err := os.Lstat(target); err == nil {
		if !targetInfo.Mode().IsRegular() {
			return fmt.Errorf("refuse to replace non-regular target %s", target)
		}
		same, err := filesEqual(source, target)
		if err != nil {
			return err
		}
		if same {
			a.logf("unchanged %s", target)
			if a.DryRun {
				return nil
			}
			info, err := os.Stat(target)
			if err != nil {
				return err
			}
			if info.Mode().Perm() != mode.Perm() {
				if err := os.Chmod(target, mode.Perm()); err != nil {
					return err
				}
				a.managedFileRevision++
			}
			return nil
		}
		if err := a.copyToBackup(target); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	a.logf("copy %s -> %s", source, target)
	if a.DryRun {
		return nil
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := atomicWriteFile(target, data, mode); err != nil {
		return err
	}
	a.managedFileRevision++
	return nil
}

func copyFileContents(source string, target string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Chmod(mode.Perm()); err != nil {
		return err
	}
	return out.Sync()
}

func dirTreesEqual(source string, target string) (bool, error) {
	sourceEntries, err := collectTreeEntries(source)
	if err != nil {
		return false, err
	}
	targetEntries, err := collectTreeEntries(target)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if len(sourceEntries) != len(targetEntries) {
		return false, nil
	}
	for rel, sourceEntry := range sourceEntries {
		targetEntry, ok := targetEntries[rel]
		if !ok {
			return false, nil
		}
		if sourceEntry.isDir != targetEntry.isDir {
			return false, nil
		}
		if sourceEntry.mode.Perm() != targetEntry.mode.Perm() {
			return false, nil
		}
		if sourceEntry.isDir {
			continue
		}
		if sourceEntry.size != targetEntry.size {
			return false, nil
		}
		same, err := filesEqual(sourceEntry.path, targetEntry.path)
		if err != nil {
			return false, err
		}
		if !same {
			return false, nil
		}
	}
	return true, nil
}

func collectTreeEntries(root string) (map[string]treeEntry, error) {
	entries := map[string]treeEntry{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entries[rel] = treeEntry{
			path:  path,
			isDir: d.IsDir(),
			size:  info.Size(),
			mode:  info.Mode(),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func filesEqual(left string, right string) (bool, error) {
	leftInfo, err := os.Stat(left)
	if err != nil {
		return false, err
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if leftInfo.IsDir() || rightInfo.IsDir() {
		return false, nil
	}
	if leftInfo.Size() != rightInfo.Size() {
		return false, nil
	}
	leftData, err := os.ReadFile(left)
	if err != nil {
		return false, err
	}
	rightData, err := os.ReadFile(right)
	if err != nil {
		return false, err
	}
	return bytes.Equal(leftData, rightData), nil
}
