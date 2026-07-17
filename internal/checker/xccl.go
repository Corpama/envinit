package checker

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"envinit/internal/spec"
)

const (
	xcclMPICHVersion = "5.0.1"
	xcclMPICHPrefix  = "/var/lib/envinit/check-runtime/mpich-5.0.1"
)

func runXCCLCheck(opts Options, targets []Target, groupsByTarget resolvedRDMAGroups) (runErr error) {
	cfg := opts.Bundle.Check.XCCL
	if err := validateXCCLConfig(cfg); err != nil {
		return err
	}
	plans, err := resolveXCCLTargetPlans(opts, targets, groupsByTarget)
	if err != nil {
		return err
	}
	if err := validateXCCLPlanConsistency(plans); err != nil {
		return err
	}
	printXCCLPlan(opts, plans)

	runID := "dry-run"
	if !opts.DryRun {
		runID, err = newXCCLRunID()
		if err != nil {
			return err
		}
	}
	workDir := filepath.Join(filepath.Clean(cfg.WorkRoot), runID)
	marker := "envinit-xccl-" + runID
	coordinator := plans[0].Target
	multiHost := len(plans) > 1
	totalRanks := 0
	for _, plan := range plans {
		totalRanks += plan.XPUCount
	}

	if opts.DryRun {
		printXCCLDryRun(opts, plans, coordinator, workDir, marker, totalRanks, multiHost)
		return nil
	}

	localTemp, err := os.MkdirTemp("", "envinit-xccl-")
	if err != nil {
		return fmt.Errorf("create local XCCL staging directory: %w", err)
	}
	defer os.RemoveAll(localTemp)
	privateKey := ""
	publicKey := ""
	if multiHost {
		privateKey, publicKey, err = generateXCCLSSHKey(localTemp, marker)
		if err != nil {
			return err
		}
	}

	remoteTouched := false
	defer func() {
		if !remoteTouched {
			return
		}
		cleanupErr := cleanupXCCLTargets(opts, plans, workDir, marker, multiHost)
		if cleanupErr == nil {
			if multiHost {
				fmt.Fprintf(opts.Output, "INFO xccl cleanup: removed temporary SSH authorization and runtime %s from all targets\n", workDir)
			} else {
				fmt.Fprintf(opts.Output, "INFO xccl cleanup: removed temporary runtime %s from %s; authorized_keys was not modified\n", workDir, coordinator.Name)
			}
			return
		}
		fmt.Fprintf(opts.Output, "WARN xccl cleanup: %v\n", cleanupErr)
		if runErr == nil {
			runErr = cleanupErr
		} else {
			runErr = errors.Join(runErr, cleanupErr)
		}
	}()

	remoteTouched = true
	for idx, plan := range plans {
		if _, err := runCommand(opts.Bundle.Check, plan.Target, xcclPrepareDirectoriesCommand(workDir)); err != nil {
			return fmt.Errorf("prepare XCCL runtime on %s: %w", plan.Target.Name, err)
		}
		rankScript := filepath.Join(localTemp, fmt.Sprintf("rank-%d.sh", idx))
		if err := os.WriteFile(rankScript, []byte(xcclRankScript(cfg, plan, workDir)), 0o700); err != nil {
			return fmt.Errorf("write XCCL rank script for %s: %w", plan.Target.Name, err)
		}
		copies := [][2]string{
			{cfg.MPICHArchive, filepath.Join(workDir, "incoming", "mpich.tar.gz")},
			{cfg.XCCLArchive, filepath.Join(workDir, "incoming", "xccl.tar.gz")},
			{rankScript, filepath.Join(workDir, "run-rank.sh")},
		}
		if multiHost {
			copies = append(copies, [2]string{publicKey, filepath.Join(workDir, "ssh", "id_ed25519.pub")})
		}
		for _, item := range copies {
			if err := copyFileToTarget(opts, plan.Target, item[0], item[1]); err != nil {
				return fmt.Errorf("distribute XCCL runtime to %s: %w", plan.Target.Name, err)
			}
		}
		if _, err := runCommand(opts.Bundle.Check, plan.Target, xcclInstallRuntimeCommand(cfg, workDir, multiHost)); err != nil {
			return fmt.Errorf("install temporary XCCL runtime on %s: %w", plan.Target.Name, err)
		}
		if multiHost {
			if _, err := runCommand(opts.Bundle.Check, plan.Target, xcclAuthorizeKeyCommand(workDir, marker)); err != nil {
				return fmt.Errorf("authorize temporary XCCL SSH key on %s: %w", plan.Target.Name, err)
			}
		}
	}

	if multiHost {
		hostFile := filepath.Join(localTemp, "hosts")
		if err := os.WriteFile(hostFile, []byte(xcclHostFile(plans)), 0o600); err != nil {
			return fmt.Errorf("write XCCL hostfile: %w", err)
		}
		sshWrapper := filepath.Join(localTemp, "ssh-wrapper")
		if err := os.WriteFile(sshWrapper, []byte(xcclSSHWrapper(opts.Bundle.Check, workDir)), 0o700); err != nil {
			return fmt.Errorf("write XCCL SSH wrapper: %w", err)
		}
		for _, item := range [][2]string{
			{privateKey, filepath.Join(workDir, "ssh", "id_ed25519")},
			{hostFile, filepath.Join(workDir, "hosts")},
			{sshWrapper, filepath.Join(workDir, "ssh-wrapper")},
		} {
			if err := copyFileToTarget(opts, coordinator, item[0], item[1]); err != nil {
				return fmt.Errorf("stage XCCL coordinator file %s: %w", filepath.Base(item[1]), err)
			}
		}
		if _, err := runCommand(opts.Bundle.Check, coordinator, xcclCoordinatorPermissionsCommand(workDir)); err != nil {
			return fmt.Errorf("secure XCCL coordinator files: %w", err)
		}
		if _, err := runCommand(opts.Bundle.Check, coordinator, xcclTemporarySSHProbeCommand(plans, workDir)); err != nil {
			return fmt.Errorf("verify temporary XCCL SSH mesh from %s: %w", coordinator.Name, err)
		}
	}

	mpirunArgs := xcclMPIRunArgs(cfg, workDir, totalRanks, multiHost)
	fmt.Fprintf(opts.Output, "INFO xccl mpirun coordinator=%s ranks=%d: %s\n", coordinator.Name, totalRanks, shellJoin(mpirunArgs))
	output, err := runCommand(opts.Bundle.Check, coordinator, shellJoin(mpirunArgs))
	if strings.TrimSpace(output) != "" {
		fmt.Fprintln(opts.Output, "XCCL raw output:")
		fmt.Fprintln(opts.Output, strings.TrimRight(output, "\n"))
	}
	if err != nil {
		return fmt.Errorf("run XCCL %s on %d ranks: %w", cfg.Test, totalRanks, err)
	}
	rows := parseXCCLPerformanceRows(output)
	if len(rows) == 0 {
		return fmt.Errorf("XCCL %s completed but no performance rows were parsed", cfg.Test)
	}
	return printXCCLResult(opts, plans, totalRanks, rows)
}

func validateXCCLConfig(cfg spec.CheckXCCLConfig) error {
	if strings.TrimSpace(cfg.MPICHArchive) == "" {
		return errors.New("bundle check.xccl.mpich_archive is required")
	}
	if strings.TrimSpace(cfg.XCCLArchive) == "" {
		return errors.New("bundle check.xccl.xccl_archive is required")
	}
	for name, path := range map[string]string{
		"mpich_archive": cfg.MPICHArchive,
		"xccl_archive":  cfg.XCCLArchive,
	} {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("bundle check.xccl.%s %s: %w", name, path, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("bundle check.xccl.%s %s is not a regular file", name, path)
		}
	}
	workRoot := filepath.Clean(strings.TrimSpace(cfg.WorkRoot))
	if !filepath.IsAbs(workRoot) || workRoot == "/" || workRoot == "/tmp" || workRoot == "/var/tmp" {
		return fmt.Errorf("bundle check.xccl.work_root must be a dedicated absolute directory below /tmp or /var/tmp, got %q", cfg.WorkRoot)
	}
	if !strings.HasPrefix(workRoot+"/", "/tmp/") && !strings.HasPrefix(workRoot+"/", "/var/tmp/") {
		return fmt.Errorf("bundle check.xccl.work_root must be below /tmp or /var/tmp, got %q", cfg.WorkRoot)
	}
	for _, r := range workRoot {
		if r == '/' || r == '.' || r == '_' || r == '-' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			continue
		}
		return fmt.Errorf("bundle check.xccl.work_root contains unsupported character %q", r)
	}
	if !filepath.IsAbs(strings.TrimSpace(cfg.XPUHome)) {
		return fmt.Errorf("bundle check.xccl.xpu_home must be an absolute path, got %q", cfg.XPUHome)
	}
	allowedTests := map[string]bool{
		"all_reduce": true, "all_gather": true, "all_to_all": true, "broadcast": true,
		"reduce": true, "reduce_scatter": true, "sendrecv": true,
	}
	if !allowedTests[cfg.Test] {
		return fmt.Errorf("bundle check.xccl.test %q is not supported", cfg.Test)
	}
	if cfg.StepFactor <= 0 || cfg.WarmupIterations < 0 || cfg.Iterations <= 0 || cfg.Timeout <= 0 || cfg.MinBusBandwidthGBs < 0 {
		return errors.New("bundle check.xccl step_factor, iterations, and timeout must be positive; warmup_iterations and min_bus_bandwidth_gbs must not be negative")
	}
	if strings.TrimSpace(cfg.MinBytes) == "" || strings.TrimSpace(cfg.MaxBytes) == "" || strings.TrimSpace(cfg.DataType) == "" {
		return errors.New("bundle check.xccl min_bytes, max_bytes, and data_type must not be empty")
	}
	protected := map[string]bool{
		"PATH": true, "LD_LIBRARY_PATH": true, "XPU_HOME": true, "XPU_VISIBLE_DEVICES": true, "CUDA_VISIBLE_DEVICES": true,
		"BKCL_TIMEOUT": true, "BKCL_ENABLE_XDR": true,
		"BKCL_RDMA_NICS": true, "BKCL_FORCE_RDMA_NICS_ORDER": true, "BKCL_SOCKET_IFNAME": true,
		"BKCL_SWITCH_TOPO": true, "BKCL_RDMA_VERBS": true, "BKCL_TREE_THRESHOLD": true,
	}
	for key, value := range cfg.Environment {
		if !validEnvironmentName(key) {
			return fmt.Errorf("bundle check.xccl.environment contains invalid variable name %q", key)
		}
		if protected[key] {
			return fmt.Errorf("bundle check.xccl.environment %s is managed by envinit; use topology discovery or the dedicated bundle field", key)
		}
		if strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("bundle check.xccl.environment %s contains a newline or NUL byte", key)
		}
	}
	return nil
}

func resolveXCCLTargetPlans(opts Options, targets []Target, groupsByTarget resolvedRDMAGroups) ([]xcclTargetPlan, error) {
	plans := make([]xcclTargetPlan, 0, len(targets))
	for _, target := range targets {
		output, err := runDiscoveryCommand(opts, target, "xpu-smi topo -m")
		if err != nil {
			return nil, fmt.Errorf("discover XCCL topology for %s: %w", target.Name, err)
		}
		topology, err := parseXPUTopology(output)
		if err != nil {
			return nil, fmt.Errorf("discover XCCL topology for %s: %w", target.Name, err)
		}
		plan, err := xcclPlanFromTopology(opts.Bundle, target, groupsByTarget[target.Name], topology)
		if err != nil {
			return nil, err
		}
		plan.SocketInterface, err = resolveXCCLSocketInterface(opts, target)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func validateXCCLPlanConsistency(plans []xcclTargetPlan) error {
	if len(plans) == 0 {
		return errors.New("XCCL check has no target plans")
	}
	baseline := plans[0]
	for _, plan := range plans[1:] {
		if plan.XPUCount != baseline.XPUCount {
			return fmt.Errorf("XCCL requires the same XPU count on every host: %s=%d, %s=%d", baseline.Target.Name, baseline.XPUCount, plan.Target.Name, plan.XPUCount)
		}
		if strings.Join(plan.RDMANICOrder, ",") != strings.Join(baseline.RDMANICOrder, ",") {
			return fmt.Errorf("XCCL requires the same RDMA interface names and XPU order on every host: %s=%s, %s=%s", baseline.Target.Name, strings.Join(baseline.RDMANICOrder, ","), plan.Target.Name, strings.Join(plan.RDMANICOrder, ","))
		}
		if plan.SocketInterface != baseline.SocketInterface {
			return fmt.Errorf("XCCL requires the same BKCL_SOCKET_IFNAME on every host: %s=%s, %s=%s; set check.xccl.socket_interface after normalizing the management interface name", baseline.Target.Name, baseline.SocketInterface, plan.Target.Name, plan.SocketInterface)
		}
	}
	return nil
}

func xcclPlanFromTopology(bundle spec.Bundle, target Target, groups []spec.CheckRDMAGroup, topology xpuTopology) (xcclTargetPlan, error) {
	if len(groups) == 0 {
		return xcclTargetPlan{}, fmt.Errorf("resolve XCCL RDMA mapping for %s: no RDMA groups", target.Name)
	}
	deviceToNIC := map[string]string{}
	for nic, device := range topology.NICDevices {
		deviceToNIC[strings.TrimSpace(device)] = nic
	}
	type candidate struct {
		iface  string
		device string
		nic    string
	}
	candidates := make([]candidate, 0, len(groups))
	for idx, group := range groups {
		iface := strings.TrimSpace(targetRDMAInterfaceName(bundle, target, idx))
		device := strings.TrimSpace(group.IBDevice)
		nic := deviceToNIC[device]
		if iface == "" || device == "" || nic == "" {
			return xcclTargetPlan{}, fmt.Errorf("resolve XCCL RDMA mapping for %s rdma%d: iface=%q ib_device=%q is incomplete or absent from xpu-smi NIC legend", target.Name, idx+1, iface, device)
		}
		candidates = append(candidates, candidate{iface: iface, device: device, nic: nic})
	}

	xpuIndexes := make([]int, 0, len(topology.Links))
	for xpu := range topology.Links {
		xpuIndexes = append(xpuIndexes, xpu)
	}
	sort.Ints(xpuIndexes)
	plan := xcclTargetPlan{Target: target, XPUCount: len(xpuIndexes)}
	seenNICs := map[string]bool{}
	for _, xpu := range xpuIndexes {
		bestRank := int(^uint(0) >> 1)
		best := -1
		bestLink := ""
		for idx, item := range candidates {
			link := strings.ToUpper(strings.TrimSpace(topology.Links[xpu][item.nic]))
			rank, ok := topologyLinkRank(link)
			if !ok || rank >= bestRank {
				continue
			}
			bestRank = rank
			best = idx
			bestLink = link
		}
		if best < 0 {
			return xcclTargetPlan{}, fmt.Errorf("resolve XCCL RDMA mapping for %s XPU%d: no reachable participating NIC", target.Name, xpu)
		}
		item := candidates[best]
		plan.RDMANICOrder = append(plan.RDMANICOrder, item.iface)
		plan.Mapping = append(plan.Mapping, fmt.Sprintf("XPU%d=%s(%s,%s)", xpu, item.iface, item.device, bestLink))
		if !seenNICs[item.iface] {
			seenNICs[item.iface] = true
			plan.RDMANICs = append(plan.RDMANICs, item.iface)
		}
	}
	if plan.XPUCount == 0 {
		return xcclTargetPlan{}, fmt.Errorf("resolve XCCL RDMA mapping for %s: topology contains no XPUs", target.Name)
	}
	return plan, nil
}

func resolveXCCLSocketInterface(opts Options, target Target) (string, error) {
	if value := strings.TrimSpace(opts.Bundle.Check.XCCL.SocketInterface); value != "" {
		command := fmt.Sprintf("test -d /sys/class/net/%s && printf '%%s\\n' %s", shellQuote(value), shellQuote(value))
		output, err := runDiscoveryCommand(opts, target, command)
		if err != nil {
			return "", fmt.Errorf("verify XCCL socket interface %s on %s: %w", value, target.Name, err)
		}
		return strings.TrimSpace(strings.SplitN(output, "\n", 2)[0]), nil
	}
	address := strings.TrimSpace(target.Address)
	command := "ip -o -4 route show default 2>/dev/null | awk '{for (i=1;i<=NF;i++) if ($i==\"dev\") {print $(i+1); exit}}'"
	if net.ParseIP(address) != nil {
		command = fmt.Sprintf("ip -o -4 addr show scope global 2>/dev/null | awk -v ip=%s '$4 ~ (\"^\" ip \"/\") {print $2; exit}'", shellQuote(address))
	}
	output, err := runDiscoveryCommand(opts, target, command)
	if err != nil {
		return "", fmt.Errorf("discover XCCL socket interface for %s: %w", target.Name, err)
	}
	iface := strings.TrimSpace(strings.SplitN(output, "\n", 2)[0])
	if iface == "" {
		return "", fmt.Errorf("discover XCCL socket interface for %s: no interface carries management address %s; set check.xccl.socket_interface explicitly", target.Name, target.Address)
	}
	return iface, nil
}

func printXCCLPlan(opts Options, plans []xcclTargetPlan) {
	for _, plan := range plans {
		fmt.Fprintf(opts.Output, "INFO xccl topology: %s xpus=%d socket_iface=%s rdma_nics=%s force_order=%s mapping=%s\n",
			plan.Target.Name, plan.XPUCount, firstNonEmpty(plan.SocketInterface, "auto"), strings.Join(plan.RDMANICs, ","),
			strings.Join(plan.RDMANICOrder, ","), strings.Join(plan.Mapping, ";"))
		for _, mapping := range plan.Mapping {
			if strings.HasSuffix(mapping, ",PIX)") {
				continue
			}
			fmt.Fprintf(opts.Output, "WARN xccl topology degraded: %s %s; PIX is unavailable and collective bandwidth may be PCIe/NUMA limited\n", plan.Target.Name, mapping)
		}
	}
}

func printXCCLDryRun(opts Options, plans []xcclTargetPlan, coordinator Target, workDir, marker string, totalRanks int, multiHost bool) {
	for _, plan := range plans {
		fmt.Fprintf(opts.Output, "dry-run xccl prepare %s: %s\n", plan.Target.Name, xcclPrepareDirectoriesCommand(workDir))
		fmt.Fprintf(opts.Output, "dry-run xccl copy %s: %s -> %s/incoming/mpich.tar.gz\n", plan.Target.Name, opts.Bundle.Check.XCCL.MPICHArchive, workDir)
		fmt.Fprintf(opts.Output, "dry-run xccl copy %s: %s -> %s/incoming/xccl.tar.gz\n", plan.Target.Name, opts.Bundle.Check.XCCL.XCCLArchive, workDir)
		fmt.Fprintf(opts.Output, "dry-run xccl rank environment %s:\n%s", plan.Target.Name, xcclRankScript(opts.Bundle.Check.XCCL, plan, workDir))
		if multiHost {
			fmt.Fprintf(opts.Output, "dry-run xccl temporary authorization %s: append one key marked %s to $HOME/.ssh/authorized_keys\n", plan.Target.Name, marker)
		}
	}
	if multiHost {
		fmt.Fprintf(opts.Output, "dry-run xccl launcher: temporary SSH mesh from coordinator %s\n", coordinator.Name)
	} else {
		fmt.Fprintf(opts.Output, "dry-run xccl launcher: local Hydra processes on %s; authorized_keys will not be modified\n", coordinator.Name)
	}
	fmt.Fprintf(opts.Output, "dry-run xccl mpirun coordinator=%s ranks=%d: %s\n", coordinator.Name, totalRanks, shellJoin(xcclMPIRunArgs(opts.Bundle.Check.XCCL, workDir, totalRanks, multiHost)))
	if multiHost {
		fmt.Fprintf(opts.Output, "dry-run xccl cleanup: remove only key marker %s, owned MPICH symlink, and %s on every target\n", marker, workDir)
	} else {
		fmt.Fprintf(opts.Output, "dry-run xccl cleanup: remove owned MPICH symlink and %s from %s; do not touch authorized_keys\n", workDir, coordinator.Name)
	}
}

func xcclPrepareDirectoriesCommand(workDir string) string {
	return fmt.Sprintf("umask 077; mkdir -p %s %s %s", shellQuote(filepath.Join(workDir, "incoming")), shellQuote(filepath.Join(workDir, "runtime")), shellQuote(filepath.Join(workDir, "ssh")))
}

func xcclInstallRuntimeCommand(cfg spec.CheckXCCLConfig, workDir string, withSSHAuthorization bool) string {
	runtimeDir := filepath.Join(workDir, "runtime")
	temporaryMPICH := filepath.Join(runtimeDir, "mpich-"+xcclMPICHVersion)
	xcclBinary := filepath.Join(runtimeDir, "xccl_Linux_x86_64", "perf", cfg.Test)
	ldPath := xcclLibraryPath(cfg, workDir)
	commands := []string{
		"set -eu",
		fmt.Sprintf("tar -xzf %s -C %s", shellQuote(filepath.Join(workDir, "incoming", "mpich.tar.gz")), shellQuote(runtimeDir)),
		fmt.Sprintf("tar -xzf %s -C %s", shellQuote(filepath.Join(workDir, "incoming", "xccl.tar.gz")), shellQuote(runtimeDir)),
		fmt.Sprintf("test -x %s", shellQuote(filepath.Join(temporaryMPICH, "bin", "mpiexec.hydra"))),
		fmt.Sprintf("test -x %s", shellQuote(filepath.Join(temporaryMPICH, "bin", "mpichversion"))),
		fmt.Sprintf("test -e %s", shellQuote(filepath.Join(temporaryMPICH, "lib", "libmpi.so.0"))),
		fmt.Sprintf("LD_LIBRARY_PATH=%s %s 2>/dev/null | grep -E %s >/dev/null", shellQuote(filepath.Join(temporaryMPICH, "lib")), shellQuote(filepath.Join(temporaryMPICH, "bin", "mpichversion")), shellQuote("^MPICH Version:[[:space:]]*5[.]0[.]1[[:space:]]*$")),
		fmt.Sprintf("test -x %s", shellQuote(xcclBinary)),
		fmt.Sprintf("mkdir -p %s", shellQuote(filepath.Dir(xcclMPICHPrefix))),
		fmt.Sprintf("if [ -L %s ]; then current=$(readlink %s); if [ \"$current\" != %s ]; then printf 'MPICH runtime prefix is busy: %%s -> %%s\\n' %s \"$current\" >&2; exit 1; fi; printf 'created\\n' > %s; elif [ -e %s ]; then test -x %s; test -e %s; %s 2>/dev/null | grep -E %s >/dev/null; printf 'reused\\n' > %s; else ln -s %s %s; printf 'created\\n' > %s; fi",
			shellQuote(xcclMPICHPrefix), shellQuote(xcclMPICHPrefix), shellQuote(temporaryMPICH), shellQuote(xcclMPICHPrefix),
			shellQuote(filepath.Join(workDir, "mpich-link-created")), shellQuote(xcclMPICHPrefix), shellQuote(filepath.Join(xcclMPICHPrefix, "bin", "mpiexec.hydra")), shellQuote(filepath.Join(xcclMPICHPrefix, "lib", "libmpi.so.0")),
			shellQuote(filepath.Join(xcclMPICHPrefix, "bin", "mpichversion")), shellQuote("^MPICH Version:[[:space:]]*5[.]0[.]1[[:space:]]*$"),
			shellQuote(filepath.Join(workDir, "mpich-reused")), shellQuote(temporaryMPICH), shellQuote(xcclMPICHPrefix), shellQuote(filepath.Join(workDir, "mpich-link-created"))),
		fmt.Sprintf("missing=$(LD_LIBRARY_PATH=%s ldd %s 2>&1 | grep 'not found' || true); if [ -n \"$missing\" ]; then printf 'missing XCCL runtime dependencies:\\n%%s\\n' \"$missing\" >&2; exit 1; fi", shellQuote(ldPath), shellQuote(xcclBinary)),
		fmt.Sprintf("chmod 700 %s", shellQuote(filepath.Join(workDir, "run-rank.sh"))),
	}
	if withSSHAuthorization {
		commands = append(commands, fmt.Sprintf("chmod 600 %s", shellQuote(filepath.Join(workDir, "ssh", "id_ed25519.pub"))))
	}
	return strings.Join(commands, "; ")
}

func xcclAuthorizeKeyCommand(workDir, marker string) string {
	publicKey := filepath.Join(workDir, "ssh", "id_ed25519.pub")
	return strings.Join([]string{
		"set -eu",
		"umask 077",
		fmt.Sprintf("if [ ! -d \"$HOME/.ssh\" ]; then mkdir -p \"$HOME/.ssh\"; touch %s; fi", shellQuote(filepath.Join(workDir, "ssh-dir-created"))),
		fmt.Sprintf("if [ ! -e \"$HOME/.ssh/authorized_keys\" ]; then touch \"$HOME/.ssh/authorized_keys\"; touch %s; fi", shellQuote(filepath.Join(workDir, "authorized-keys-created"))),
		"chmod 700 \"$HOME/.ssh\"",
		"chmod 600 \"$HOME/.ssh/authorized_keys\"",
		fmt.Sprintf("grep -F %s %s >/dev/null", shellQuote(marker), shellQuote(publicKey)),
		fmt.Sprintf("grep -F %s \"$HOME/.ssh/authorized_keys\" >/dev/null || cat %s >> \"$HOME/.ssh/authorized_keys\"", shellQuote(marker), shellQuote(publicKey)),
	}, "; ")
}

func xcclRankScript(cfg spec.CheckXCCLConfig, plan xcclTargetPlan, workDir string) string {
	env := map[string]string{
		"BKCL_TIMEOUT":               strconv.Itoa(cfg.Timeout),
		"BKCL_RDMA_NICS":             strings.Join(plan.RDMANICOrder, ","),
		"BKCL_FORCE_RDMA_NICS_ORDER": strings.Join(plan.RDMANICOrder, ","),
	}
	if plan.SocketInterface != "" {
		env["BKCL_SOCKET_IFNAME"] = plan.SocketInterface
	}
	if cfg.EnableXDR != nil && *cfg.EnableXDR {
		env["BKCL_ENABLE_XDR"] = "1"
	}
	if cfg.Supernode {
		env["BKCL_SWITCH_TOPO"] = "1"
		env["BKCL_RDMA_VERBS"] = "1"
		env["BKCL_TREE_THRESHOLD"] = "0"
	}
	for key, value := range cfg.Environment {
		env[key] = value
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := []string{
		"#!/bin/sh",
		"set -eu",
		"unset XPU_VISIBLE_DEVICES CUDA_VISIBLE_DEVICES BKCL_ENABLE_XDR BKCL_SWITCH_TOPO BKCL_RDMA_VERBS BKCL_TREE_THRESHOLD BKCL_DEBUG BKCL_FORCE_L3_RDMA 2>/dev/null || true",
		fmt.Sprintf("export XPU_HOME=%s", shellQuote(strings.TrimRight(cfg.XPUHome, "/"))),
		fmt.Sprintf("if [ -n \"${PATH:-}\" ]; then export PATH=%s:\"$PATH\"; else export PATH=%s; fi", shellQuote(xcclMPICHPrefix+"/bin:"+strings.TrimRight(cfg.XPUHome, "/")+"/bin"), shellQuote(xcclMPICHPrefix+"/bin:"+strings.TrimRight(cfg.XPUHome, "/")+"/bin")),
		fmt.Sprintf("if [ -n \"${LD_LIBRARY_PATH:-}\" ]; then export LD_LIBRARY_PATH=%s:\"$LD_LIBRARY_PATH\"; else export LD_LIBRARY_PATH=%s; fi", shellQuote(xcclLibraryPath(cfg, workDir)), shellQuote(xcclLibraryPath(cfg, workDir))),
	}
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("export %s=%s", key, shellQuote(env[key])))
	}
	lines = append(lines, fmt.Sprintf("exec %s \"$@\"", shellQuote(filepath.Join(workDir, "runtime", "xccl_Linux_x86_64", "perf", cfg.Test))))
	return strings.Join(lines, "\n") + "\n"
}

func xcclLibraryPath(cfg spec.CheckXCCLConfig, workDir string) string {
	xpuHome := strings.TrimRight(cfg.XPUHome, "/")
	paths := []string{
		filepath.Join(workDir, "runtime", "xccl_Linux_x86_64", "so"),
		xcclMPICHPrefix + "/lib",
		xpuHome + "/so",
		xpuHome + "/lib",
		xpuHome + "/lib64",
		"/usr/lib64",
		"/usr/lib/x86_64-linux-gnu",
	}
	return strings.Join(paths, ":")
}

func xcclHostFile(plans []xcclTargetPlan) string {
	var lines []string
	for _, plan := range plans {
		lines = append(lines, fmt.Sprintf("%s:%d", plan.Target.Address, plan.XPUCount))
	}
	return strings.Join(lines, "\n") + "\n"
}

func xcclSSHWrapper(cfg spec.CheckConfig, workDir string) string {
	args := []string{"ssh"}
	args = append(args, cfg.SSH.Options...)
	args = append(args,
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-i", filepath.Join(workDir, "ssh", "id_ed25519"),
	)
	if strings.TrimSpace(cfg.SSH.User) != "" {
		args = append(args, "-l", cfg.SSH.User)
	}
	return "#!/bin/sh\nexec " + shellJoin(args) + " \"$@\"\n"
}

func xcclCoordinatorPermissionsCommand(workDir string) string {
	return fmt.Sprintf("chmod 700 %s %s; chmod 600 %s %s", shellQuote(workDir), shellQuote(filepath.Join(workDir, "ssh-wrapper")), shellQuote(filepath.Join(workDir, "ssh", "id_ed25519")), shellQuote(filepath.Join(workDir, "hosts")))
}

func xcclTemporarySSHProbeCommand(plans []xcclTargetPlan, workDir string) string {
	addresses := make([]string, 0, len(plans))
	for _, plan := range plans {
		addresses = append(addresses, shellQuote(plan.Target.Address))
	}
	return fmt.Sprintf("set -eu; for host in %s; do %s \"$host\" true; done", strings.Join(addresses, " "), shellQuote(filepath.Join(workDir, "ssh-wrapper")))
}

func xcclMPIRunArgs(cfg spec.CheckXCCLConfig, workDir string, totalRanks int, multiHost bool) []string {
	args := []string{xcclMPICHPrefix + "/bin/mpiexec.hydra"}
	if multiHost {
		args = append(args,
			"-launcher", "ssh",
			"-launcher-exec", filepath.Join(workDir, "ssh-wrapper"),
			"-f", filepath.Join(workDir, "hosts"),
		)
	} else {
		args = append(args, "-launcher", "fork")
	}
	return append(args,
		"-n", strconv.Itoa(totalRanks),
		filepath.Join(workDir, "run-rank.sh"),
		"-b", cfg.MinBytes,
		"-e", cfg.MaxBytes,
		"-f", strconv.Itoa(cfg.StepFactor),
		"-w", strconv.Itoa(cfg.WarmupIterations),
		"-n", strconv.Itoa(cfg.Iterations),
		"-c", "1",
		"-d", cfg.DataType,
	)
}

func cleanupXCCLTargets(opts Options, plans []xcclTargetPlan, workDir, marker string, removeSSHAuthorization bool) error {
	var errs []error
	for _, plan := range plans {
		command := xcclCleanupCommand(workDir, marker, removeSSHAuthorization)
		if _, err := runCommand(opts.Bundle.Check, plan.Target, command); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", plan.Target.Name, err))
		}
	}
	return errors.Join(errs...)
}

func xcclCleanupCommand(workDir, marker string, removeSSHAuthorization bool) string {
	temporaryMPICH := filepath.Join(workDir, "runtime", "mpich-"+xcclMPICHVersion)
	commands := []string{}
	if removeSSHAuthorization {
		commands = append(commands,
			"authorized=\"$HOME/.ssh/authorized_keys\"",
			fmt.Sprintf("if [ -f \"$authorized\" ]; then tmp=\"$authorized.%s.tmp\"; awk -v marker=%s 'index($0, marker) == 0' \"$authorized\" > \"$tmp\"; chmod 600 \"$tmp\"; mv \"$tmp\" \"$authorized\"; fi", marker, shellQuote(marker)),
			fmt.Sprintf("if [ -f %s ] && [ ! -s \"$authorized\" ]; then rm -f \"$authorized\"; fi", shellQuote(filepath.Join(workDir, "authorized-keys-created"))),
			fmt.Sprintf("if [ -f %s ]; then rmdir \"$HOME/.ssh\" 2>/dev/null || true; fi", shellQuote(filepath.Join(workDir, "ssh-dir-created"))),
		)
	}
	commands = append(commands,
		fmt.Sprintf("if [ -f %s ] && [ -L %s ] && [ \"$(readlink %s)\" = %s ]; then rm -f %s; fi", shellQuote(filepath.Join(workDir, "mpich-link-created")), shellQuote(xcclMPICHPrefix), shellQuote(xcclMPICHPrefix), shellQuote(temporaryMPICH), shellQuote(xcclMPICHPrefix)),
		fmt.Sprintf("rm -rf -- %s", shellQuote(workDir)),
	)
	return strings.Join(commands, "; ")
}

func generateXCCLSSHKey(localTemp, marker string) (string, string, error) {
	privateKey := filepath.Join(localTemp, "id_ed25519")
	cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", marker, "-f", privateKey)
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("generate temporary XCCL SSH key: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return privateKey, privateKey + ".pub", nil
}

func newXCCLRunID() (string, error) {
	value := make([]byte, 8)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate XCCL run id: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func parseXCCLPerformanceRows(output string) []xcclPerformanceRow {
	var rows []xcclPerformanceRow
	for _, rawLine := range strings.Split(output, "\n") {
		fields := strings.Fields(strings.TrimSpace(rawLine))
		if len(fields) < 7 {
			continue
		}
		sizeBytes, err1 := strconv.ParseInt(fields[0], 10, 64)
		count, err2 := strconv.ParseInt(fields[1], 10, 64)
		timeUS, err3 := strconv.ParseFloat(fields[len(fields)-3], 64)
		algGBs, err4 := strconv.ParseFloat(fields[len(fields)-2], 64)
		busGBs, err5 := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil {
			continue
		}
		rows = append(rows, xcclPerformanceRow{
			SizeBytes: sizeBytes,
			Count:     count,
			DataType:  fields[2],
			Operation: fields[3],
			TimeUS:    timeUS,
			AlgGBs:    algGBs,
			BusGBs:    busGBs,
		})
	}
	return rows
}

func printXCCLResult(opts Options, plans []xcclTargetPlan, totalRanks int, rows []xcclPerformanceRow) error {
	selected := rows[0]
	for _, row := range rows[1:] {
		if row.SizeBytes > selected.SizeBytes || row.SizeBytes == selected.SizeBytes && row.BusGBs > selected.BusGBs {
			selected = row
		}
	}
	status := "PASS"
	if opts.Bundle.Check.XCCL.MinBusBandwidthGBs > 0 && selected.BusGBs < opts.Bundle.Check.XCCL.MinBusBandwidthGBs {
		status = "FAIL"
	}
	degraded := xcclPlansDegraded(plans)
	if status == "PASS" && degraded {
		status = "WARN"
	}
	hostNames := make([]string, 0, len(plans))
	for _, plan := range plans {
		hostNames = append(hostNames, plan.Target.Name)
	}
	topology := "PIX"
	if degraded {
		topology = "DEGRADED"
	}
	headers := []string{"STATUS", "TEST", "HOSTS", "RANKS", "TOPOLOGY", "SIZE(B)", "TIME(us)", "ALGBW(GB/s)", "BUSBW(GB/s)"}
	cells := []string{status, opts.Bundle.Check.XCCL.Test, strings.Join(hostNames, ","), strconv.Itoa(totalRanks), topology, strconv.FormatInt(selected.SizeBytes, 10), fmt.Sprintf("%.2f", selected.TimeUS), fmt.Sprintf("%.2f", selected.AlgGBs), fmt.Sprintf("%.2f", selected.BusGBs)}
	widths := make([]int, len(headers))
	for idx := range headers {
		widths[idx] = maxInt(len(headers[idx]), len(cells[idx]))
	}
	fmt.Fprintln(opts.Output, "XCCL result summary:")
	fmt.Fprintln(opts.Output, formatTableLine(headers, widths))
	fmt.Fprintln(opts.Output, formatTableSeparator(widths))
	line := formatTableLine(cells, widths)
	if status == "FAIL" {
		line = redText(line)
	}
	fmt.Fprintln(opts.Output, line)
	if degraded {
		fmt.Fprintln(opts.Output, "WARN xccl result topology: at least one XPU used a non-PIX RDMA mapping; collective bandwidth may be limited by the PCIe/NUMA path")
	}
	if status == "FAIL" {
		return fmt.Errorf("XCCL %s %.2f GB/s below %.2f GB/s at size %d", opts.Bundle.Check.XCCL.Test, selected.BusGBs, opts.Bundle.Check.XCCL.MinBusBandwidthGBs, selected.SizeBytes)
	}
	return nil
}

func xcclPlansDegraded(plans []xcclTargetPlan) bool {
	for _, plan := range plans {
		for _, mapping := range plan.Mapping {
			if !strings.HasSuffix(mapping, ",PIX)") {
				return true
			}
		}
	}
	return false
}

func validEnvironmentName(value string) bool {
	if value == "" {
		return false
	}
	for idx, r := range value {
		if idx == 0 {
			if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
				return false
			}
			continue
		}
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
