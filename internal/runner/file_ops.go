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
	"time"

	"envinit/internal/spec"
	"path/filepath"
)

type treeEntry struct {
	path  string
	isDir bool
	size  int64
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
		path := filepath.Join(dir, name, "address")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		mac, err := spec.NormalizeMAC(strings.TrimSpace(string(data)))
		if err != nil {
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

func (a *App) disableExistingNetplan() error {
	targets := a.netplanBackupTargets()
	if len(targets) == 0 {
		a.logf("skip backing up existing netplan files because no netplan network files will be rewritten")
		return nil
	}
	return a.backupExistingTargets(targets)
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
	target := a.targetPath(a.Bundle.OfflineAPT.TargetFile)
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
	return a.backupExistingTargets(targets)
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
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
		return fmt.Errorf("create backup directory for %s: %w", path, err)
	}
	if err := os.Rename(path, backup); err != nil {
		return fmt.Errorf("move %s to backup %s (backup_root must be on the same filesystem): %w", path, backup, err)
	}
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
	target := a.targetPath(systemPath)
	if !a.DryRun {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(target), err)
		}
	}
	if existing, err := os.ReadFile(target); err == nil {
		if bytes.Equal(existing, []byte(content)) {
			a.logf("unchanged %s", systemPath)
			return nil
		}
		if err := a.moveToBackup(target); err != nil {
			return err
		}
	}
	a.logf("write %s", systemPath)
	if a.DryRun {
		return nil
	}
	if err := os.WriteFile(target, []byte(content), mode); err != nil {
		return fmt.Errorf("write %s: %w", systemPath, err)
	}
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
	a.logf("move %s -> %s", source, target)
	if a.DryRun {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(targetPath), err)
	}
	if _, err := os.Stat(targetPath); err == nil {
		if err := a.moveToBackup(targetPath); err != nil {
			return err
		}
	}
	if err := os.Rename(sourcePath, targetPath); err != nil {
		return fmt.Errorf("move %s to %s: %w", source, target, err)
	}
	return nil
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
	if _, err := os.Stat(target); err == nil {
		same, err := dirTreesEqual(source, target)
		if err != nil {
			return err
		}
		if same {
			a.logf("unchanged %s", target)
			return nil
		}
		if err := a.moveToBackup(target); err != nil {
			return err
		}
	}
	a.logf("copy %s -> %s", source, target)
	if a.DryRun {
		return nil
	}
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
			return os.MkdirAll(dest, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFileContents(path, dest, info.Mode())
	})
}

func (a *App) copyFileWithBackup(source string, target string, mode fs.FileMode) error {
	if _, err := os.Stat(target); err == nil {
		same, err := filesEqual(source, target)
		if err != nil {
			return err
		}
		if same {
			a.logf("unchanged %s", target)
			return nil
		}
		if err := a.moveToBackup(target); err != nil {
			return err
		}
	}
	a.logf("copy %s -> %s", source, target)
	if a.DryRun {
		return nil
	}
	return copyFileContents(source, target, mode)
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
	return out.Chmod(mode.Perm())
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
