package checker

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"envinit/internal/spec"
	"envinit/internal/xpuvariant"
)

const (
	xcclMPICHVersion = "5.0.1"
	xcclMPICHPrefix  = "/var/lib/envinit/check-runtime/mpich-5.0.1"
)

func runXCCLCheck(opts Options, targets []Target, groupsByTarget resolvedRDMAGroups) (runErr error) {
	cfg := opts.Bundle.Check.XCCL
	if err := validateXCCLConfig(cfg); err != nil {
		showXCCLTUIInitialFailure(opts, cfg, nil, targets, err)
		return err
	}
	plans, err := resolveXCCLTargetPlans(opts, targets, groupsByTarget)
	if err != nil {
		showXCCLTUIInitialFailure(opts, cfg, nil, targets, err)
		return err
	}
	multiHost := len(plans) > 1
	cfg, err = resolveXCCLMachineClass(opts, cfg, targets, multiHost)
	if err != nil {
		showXCCLTUIInitialFailure(opts, cfg, plans, targets, err)
		return err
	}
	if err := validateXCCLExecutionConfig(cfg, multiHost); err != nil {
		showXCCLTUIInitialFailure(opts, cfg, plans, targets, err)
		return err
	}
	cfg = effectiveXCCLConfig(cfg, multiHost)
	opts.Bundle.Check.XCCL = cfg
	if err := validateConfiguredXCCLPlanConsistency(cfg, plans); err != nil {
		showXCCLTUIInitialFailure(opts, cfg, plans, targets, err)
		return err
	}
	if multiHost {
		for _, warning := range xcclRailInferenceWarnings(plans) {
			fmt.Fprintln(opts.Output, warning)
		}
	}
	if multiHost && !cfg.TopologyValidationEnabled() {
		fmt.Fprintln(opts.Output, "WARN xccl topology validation disabled: cross-host XPU count, XPU/NIC sharing, link class, and RDMA rail order were not checked")
	}

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
	discoveredRanks := 0
	for _, plan := range plans {
		discoveredRanks += plan.XPUCount
	}
	totalRanks, rankSource, err := resolveXCCLRanks(cfg, discoveredRanks)
	if err != nil {
		showXCCLTUIInitialFailure(opts, cfg, plans, targets, err)
		return err
	}
	plans, err = limitXCCLPlansForRanks(cfg, plans, totalRanks)
	if err != nil {
		showXCCLTUIInitialFailure(opts, cfg, plans, targets, err)
		return err
	}
	coordinator = plans[0].Target
	fmt.Fprintf(opts.Output, "INFO xccl ranks: np=%d source=%s discovered_xpus=%d\n", totalRanks, rankSource, discoveredRanks)
	printXCCLPlan(opts, plans)
	printXCCLRankEnvironments(opts, cfg, plans, workDir, multiHost)

	if opts.DryRun {
		printXCCLDryRun(opts, plans, coordinator, workDir, marker, totalRanks, multiHost)
		return nil
	}
	live := newXCCLLiveTracker(opts.Output, opts.LiveOutput, cfg, plans, totalRanks)
	if live != nil {
		defer func() {
			if runErr != nil {
				live.Fail(runErr)
			}
		}()
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
		if _, err := runCheckCommand(opts, plan.Target, xcclPrepareDirectoriesCommand(workDir)); err != nil {
			return fmt.Errorf("prepare XCCL runtime on %s: %w", plan.Target.Name, err)
		}
		rankScript := filepath.Join(localTemp, fmt.Sprintf("rank-%d.sh", idx))
		if err := os.WriteFile(rankScript, []byte(xcclRankScript(cfg, plan, workDir, multiHost)), 0o700); err != nil {
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
		if _, err := runCheckCommand(opts, plan.Target, xcclInstallRuntimeCommand(cfg, workDir, multiHost)); err != nil {
			return fmt.Errorf("install temporary XCCL runtime on %s: %w", plan.Target.Name, err)
		}
		if multiHost {
			if _, err := runCheckCommand(opts, plan.Target, xcclAuthorizeKeyCommand(workDir, marker)); err != nil {
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
		if _, err := runCheckCommand(opts, coordinator, xcclCoordinatorPermissionsCommand(workDir)); err != nil {
			return fmt.Errorf("secure XCCL coordinator files: %w", err)
		}
		if _, err := runCheckCommand(opts, coordinator, xcclTemporarySSHProbeCommand(plans, workDir)); err != nil {
			return fmt.Errorf("verify temporary XCCL SSH mesh from %s: %w", coordinator.Name, err)
		}
	}

	mpirunArgs := xcclMPIRunArgs(cfg, workDir, totalRanks, multiHost)
	mpirunCommand := xcclTrackedMPIRunCommand(workDir, mpirunArgs)
	fmt.Fprintf(opts.Output, "INFO xccl mpirun coordinator=%s ranks=%d: %s\n", coordinator.Name, totalRanks, mpirunCommand)
	var liveWriter *lineCallbackWriter
	var liveStdout io.Writer
	if live != nil {
		liveWriter = &lineCallbackWriter{callback: live.ConsumeLine}
		liveStdout = liveWriter
	}
	output, err := runCheckCommandStreaming(opts, coordinator, mpirunCommand, liveStdout, nil)
	if opts.checkTUI != nil && strings.TrimSpace(output) != "" {
		opts.checkTUI.AppendLog(checkTUIStageXCCL, strings.TrimRight(output, "\n"))
	}
	if liveWriter != nil {
		liveWriter.Flush()
	} else if strings.TrimSpace(output) != "" {
		fmt.Fprintln(opts.Output, "XCCL raw output:")
		fmt.Fprintln(opts.Output, strings.TrimRight(output, "\n"))
	}
	if err != nil {
		if live != nil && strings.TrimSpace(output) != "" {
			fmt.Fprintln(opts.Output, "XCCL failure output:")
			fmt.Fprintln(opts.Output, strings.TrimRight(output, "\n"))
		}
		return fmt.Errorf("run XCCL %s on %d ranks: %w", cfg.Test, totalRanks, err)
	}
	rows := parseXCCLPerformanceRows(output)
	if len(rows) == 0 {
		return fmt.Errorf("XCCL %s completed but no performance rows were parsed", cfg.Test)
	}
	fmt.Fprintln(opts.Output, "INFO xccl accuracy check: disabled (-c 0 performance mode); bandwidth results exclude accuracy-check overhead")
	evaluation := evaluateXCCLResult(opts, plans, rows)
	live.Finalize(evaluation)
	return printXCCLResultEvaluation(opts, totalRanks, rows, evaluation)
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
	if _, ok := xcclCollectiveName(cfg.Test); !ok {
		return fmt.Errorf("bundle check.xccl.test %q is not supported", cfg.Test)
	}
	if cfg.StepFactor <= 0 || cfg.WarmupIterations < 0 || cfg.Iterations <= 0 || cfg.Timeout <= 0 || cfg.MinBusBandwidthGBs < 0 || cfg.Ranks < 0 {
		return errors.New("bundle check.xccl step_factor, iterations, and timeout must be positive; warmup_iterations and min_bus_bandwidth_gbs must not be negative")
	}
	if strings.TrimSpace(cfg.MinBytes) == "" || strings.TrimSpace(cfg.MaxBytes) == "" || strings.TrimSpace(cfg.DataType) == "" {
		return errors.New("bundle check.xccl min_bytes, max_bytes, and data_type must not be empty")
	}
	layout := normalizedXCCLLayout(cfg.Layout)
	if layout != "full_ring" && layout != "same_index" {
		return fmt.Errorf("bundle check.xccl.layout %q is not supported; use full_ring or same_index", cfg.Layout)
	}
	ordering := normalizedXCCLXPUOrdering(cfg.XPUOrdering)
	if ordering != "auto" && ordering != "rail_aligned" && ordering != "physical" {
		return fmt.Errorf("bundle check.xccl.xpu_ordering %q is not supported; use auto, rail_aligned, or physical", cfg.XPUOrdering)
	}
	if layout == "same_index" && cfg.SplitStep <= 0 {
		return errors.New("bundle check.xccl.split_step must be positive for same_index layout")
	}
	if cfg.SplitOperation < 0 {
		return errors.New("bundle check.xccl.split_operation must not be negative")
	}
	evaluationMode := normalizedXCCLEvaluationMode(cfg.EvaluationMode)
	if evaluationMode != "auto" && evaluationMode != "manual" && evaluationMode != "disabled" {
		return fmt.Errorf("bundle check.xccl.evaluation_mode %q is not supported; use auto, manual, or disabled", cfg.EvaluationMode)
	}
	if evaluationMode == "manual" && cfg.MinBusBandwidthGBs <= 0 {
		return errors.New("bundle check.xccl.min_bus_bandwidth_gbs must be positive in manual evaluation mode")
	}
	protected := map[string]bool{
		"PATH": true, "LD_LIBRARY_PATH": true, "XPU_HOME": true, "XPU_VISIBLE_DEVICES": true, "CUDA_VISIBLE_DEVICES": true,
		"BKCL_TIMEOUT": true, "BKCL_ENABLE_XDR": true,
		"BKCL_RDMA_NICS": true, "BKCL_FORCE_RDMA_NICS_ORDER": true, "BKCL_SOCKET_IFNAME": true,
		"BKCL_SWITCH_TOPO": true, "BKCL_RDMA_VERBS": true,
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

func validateXCCLExecutionConfig(cfg spec.CheckXCCLConfig, multiHost bool) error {
	if !multiHost || normalizedXCCLEvaluationMode(cfg.EvaluationMode) != "auto" {
		return nil
	}
	if cfg.Test != "all_reduce" {
		return fmt.Errorf("automatic XCCL evaluation is currently defined only for all_reduce; set check.xccl.evaluation_mode to manual or disabled for %s", cfg.Test)
	}
	if normalizedXCCLLayout(cfg.Layout) != "full_ring" {
		return nil
	}
	machineClass := normalizedXCCLMachineClass(cfg.MachineClass)
	if machineClass != "vc" && machineClass != "vd" {
		return fmt.Errorf("bundle check.xccl.machine_class %q is not supported for multi-host full_ring auto evaluation; use VC or VD", cfg.MachineClass)
	}
	return nil
}

func resolveXCCLMachineClass(opts Options, cfg spec.CheckXCCLConfig, targets []Target, multiHost bool) (spec.CheckXCCLConfig, error) {
	if !multiHost || normalizedXCCLEvaluationMode(cfg.EvaluationMode) != "auto" || normalizedXCCLLayout(cfg.Layout) != "full_ring" {
		return cfg, nil
	}
	configured := normalizedXCCLMachineClass(cfg.MachineClass)
	if configured == "vc" || configured == "vd" {
		fmt.Fprintf(opts.Output, "INFO xccl machine class: configured=%s; automatic host classification skipped\n", strings.ToUpper(configured))
		return cfg, nil
	}
	if configured != "" && configured != "auto" {
		return cfg, fmt.Errorf("bundle check.xccl.machine_class %q is not supported; use auto, VC, or VD", cfg.MachineClass)
	}
	selected := "vd"
	var vcHosts, vdHosts []string
	for _, target := range targets {
		output, err := runDiscoveryCommand(opts, target, "xpu-smi -q")
		if err != nil {
			return cfg, fmt.Errorf("detect XCCL machine class for %s: %w", target.Name, err)
		}
		variant, partNumbers, err := xpuvariant.ClassifyPartNumbers(output)
		if err != nil {
			return cfg, fmt.Errorf("detect XCCL machine class for %s: %w", target.Name, err)
		}
		fmt.Fprintf(opts.Output, "INFO xccl machine class: %s detected=%s xpus=%d part_numbers=%s\n", target.Name, variant, len(partNumbers), strings.Join(partNumbers, ","))
		if variant == "VC" {
			selected = "vc"
			vcHosts = append(vcHosts, target.Name)
		} else {
			vdHosts = append(vdHosts, target.Name)
		}
	}
	if len(vcHosts) > 0 && len(vdHosts) > 0 {
		fmt.Fprintf(opts.Output, "WARN xccl machine class downgrade: VC hosts=%s VD hosts=%s; applying VC full-ring baseline to all hosts by weakest-link policy\n", strings.Join(vcHosts, ","), strings.Join(vdHosts, ","))
	} else {
		fmt.Fprintf(opts.Output, "INFO xccl machine class selected=%s source=auto weakest-link\n", strings.ToUpper(selected))
	}
	cfg.MachineClass = selected
	return cfg, nil
}

func effectiveXCCLConfig(cfg spec.CheckXCCLConfig, multiHost bool) spec.CheckXCCLConfig {
	if multiHost {
		return cfg
	}
	cfg.Layout = "single_host"
	cfg.XPUOrdering = "physical"
	if normalizedXCCLEvaluationMode(cfg.EvaluationMode) == "auto" {
		// The supplied automatic baselines apply only to the two multi-host
		// layouts. Preserve the historical single-host behavior unless the user
		// explicitly selects a manual threshold.
		cfg.EvaluationMode = "disabled"
	}
	return cfg
}

func normalizedXCCLLayout(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "full_ring"
	}
	return strings.ReplaceAll(value, "-", "_")
}

func normalizedXCCLXPUOrdering(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "auto"
	}
	return strings.ReplaceAll(value, "-", "_")
}

func resolvedXCCLXPUOrdering(cfg spec.CheckXCCLConfig) string {
	ordering := normalizedXCCLXPUOrdering(cfg.XPUOrdering)
	if ordering != "auto" {
		return ordering
	}
	if normalizedXCCLLayout(cfg.Layout) == "same_index" {
		return "physical"
	}
	return "rail_aligned"
}

func normalizedXCCLMachineClass(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "auto"
	}
	return value
}

func normalizedXCCLEvaluationMode(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "auto"
	}
	return value
}

func resolveXCCLRanks(cfg spec.CheckXCCLConfig, discovered int) (int, string, error) {
	if discovered <= 0 {
		return 0, "", errors.New("XCCL rank discovery found no XPUs")
	}
	if cfg.Ranks == 0 {
		return discovered, "auto", nil
	}
	if cfg.Ranks > discovered {
		return 0, "", fmt.Errorf("bundle check.xccl.ranks=%d exceeds %d discovered XPUs; use 0 for automatic rank selection", cfg.Ranks, discovered)
	}
	return cfg.Ranks, "manual", nil
}

func limitXCCLPlansForRanks(cfg spec.CheckXCCLConfig, plans []xcclTargetPlan, totalRanks int) ([]xcclTargetPlan, error) {
	capacity := 0
	for _, plan := range plans {
		capacity += plan.XPUCount
	}
	if totalRanks == capacity {
		return plans, nil
	}
	if len(plans) > 1 && totalRanks < len(plans) {
		return nil, fmt.Errorf("XCCL ranks=%d cannot include all %d selected hosts; use at least one rank per host", totalRanks, len(plans))
	}
	if len(plans) > 1 && normalizedXCCLLayout(cfg.Layout) == "same_index" && totalRanks%len(plans) != 0 {
		return nil, fmt.Errorf("XCCL same_index ranks=%d must be divisible by %d selected hosts", totalRanks, len(plans))
	}
	counts := make([]int, len(plans))
	for assigned := 0; assigned < totalRanks; {
		progress := false
		for index := range plans {
			if assigned >= totalRanks {
				break
			}
			if counts[index] >= plans[index].XPUCount {
				continue
			}
			counts[index]++
			assigned++
			progress = true
		}
		if !progress {
			return nil, fmt.Errorf("XCCL ranks=%d exceeds usable per-host XPU capacity", totalRanks)
		}
	}
	limited := make([]xcclTargetPlan, len(plans))
	for index, plan := range plans {
		count := counts[index]
		plan.XPUCount = count
		plan.XPUOrder = append([]int(nil), plan.XPUOrder[:count]...)
		plan.RDMANICOrder = append([]string(nil), plan.RDMANICOrder[:count]...)
		plan.RDMADeviceOrder = append([]string(nil), plan.RDMADeviceOrder[:count]...)
		plan.RDMALinkOrder = append([]string(nil), plan.RDMALinkOrder[:count]...)
		plan.RDMARailOrder = append([]string(nil), plan.RDMARailOrder[:count]...)
		plan.Mapping = append([]string(nil), plan.Mapping[:count]...)
		plan.RDMANICs = uniqueStringsInOrder(plan.RDMANICOrder)
		limited[index] = plan
	}
	return limited, nil
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
		plans = append(plans, plan)
	}
	if len(plans) > 1 && resolvedXCCLXPUOrdering(opts.Bundle.Check.XCCL) == "rail_aligned" {
		alignXCCLPlansByRail(plans)
	}
	for idx := range plans {
		socketInterface, err := resolveXCCLSocketInterface(opts, plans[idx].Target, plans[idx])
		if err != nil {
			return nil, err
		}
		plans[idx].SocketInterface = socketInterface
	}
	return plans, nil
}

func alignXCCLPlansByRail(plans []xcclTargetPlan) {
	if len(plans) < 2 {
		return
	}
	baseline := plans[0]
	for planIndex := 1; planIndex < len(plans); planIndex++ {
		plan := plans[planIndex]
		if len(plan.RDMARailOrder) != len(baseline.RDMARailOrder) {
			continue
		}
		used := make([]bool, len(plan.RDMARailOrder))
		order := make([]int, 0, len(baseline.RDMARailOrder))
		for logicalRank, rail := range baseline.RDMARailOrder {
			match := -1
			for idx := range plan.RDMARailOrder {
				if used[idx] || plan.RDMARailOrder[idx] != rail {
					continue
				}
				if match < 0 {
					match = idx
				}
				if logicalRank < len(baseline.RDMALinkOrder) && idx < len(plan.RDMALinkOrder) && plan.RDMALinkOrder[idx] == baseline.RDMALinkOrder[logicalRank] {
					match = idx
					break
				}
			}
			if match < 0 {
				order = nil
				break
			}
			used[match] = true
			order = append(order, match)
		}
		if len(order) == len(plan.RDMARailOrder) {
			plans[planIndex] = reorderXCCLPlan(plan, order)
		}
	}
}

func reorderXCCLPlan(plan xcclTargetPlan, order []int) xcclTargetPlan {
	reorderStrings := func(values []string) []string {
		out := make([]string, 0, len(order))
		for _, idx := range order {
			if idx >= 0 && idx < len(values) {
				out = append(out, values[idx])
			}
		}
		return out
	}
	reorderInts := func(values []int) []int {
		out := make([]int, 0, len(order))
		for _, idx := range order {
			if idx >= 0 && idx < len(values) {
				out = append(out, values[idx])
			}
		}
		return out
	}
	plan.XPUOrder = reorderInts(plan.XPUOrder)
	plan.RDMANICOrder = reorderStrings(plan.RDMANICOrder)
	plan.RDMADeviceOrder = reorderStrings(plan.RDMADeviceOrder)
	plan.RDMALinkOrder = reorderStrings(plan.RDMALinkOrder)
	plan.RDMARailOrder = reorderStrings(plan.RDMARailOrder)
	plan.Mapping = reorderStrings(plan.Mapping)
	plan.RDMANICs = uniqueStringsInOrder(plan.RDMANICOrder)
	return plan
}

func uniqueStringsInOrder(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
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
		baselineShape := xcclDeviceSharingShape(baseline.RDMADeviceOrder)
		planShape := xcclDeviceSharingShape(plan.RDMADeviceOrder)
		if strings.Join(planShape, ",") != strings.Join(baselineShape, ",") {
			return fmt.Errorf("XCCL XPU/NIC sharing differs across hosts: %s=%s, %s=%s", baseline.Target.Name, strings.Join(baselineShape, ","), plan.Target.Name, strings.Join(planShape, ","))
		}
		if strings.Join(plan.RDMALinkOrder, ",") != strings.Join(baseline.RDMALinkOrder, ",") {
			return fmt.Errorf("XCCL XPU/RDMA topology link classes differ across hosts: %s=%s, %s=%s", baseline.Target.Name, strings.Join(baseline.RDMALinkOrder, ","), plan.Target.Name, strings.Join(plan.RDMALinkOrder, ","))
		}
		if strings.Join(plan.RDMARailOrder, ",") != strings.Join(baseline.RDMARailOrder, ",") {
			return fmt.Errorf("XCCL XPU/RDMA rail order differs across hosts: %s=%s, %s=%s; review the per-host mapping or set check.xccl.validate_topology=false to force the run", baseline.Target.Name, strings.Join(baseline.RDMARailOrder, ","), plan.Target.Name, strings.Join(plan.RDMARailOrder, ","))
		}
	}
	return nil
}

func xcclRailInferenceWarnings(plans []xcclTargetPlan) []string {
	var warnings []string
	for _, plan := range plans {
		warnings = append(warnings, xcclRailInferenceWarningsForPlan(plan)...)
	}
	return warnings
}

func xcclRailInferenceWarningsForPlan(plan xcclTargetPlan) []string {
	devicesByRail := map[string]map[string]bool{}
	for idx, rail := range plan.RDMARailOrder {
		if strings.HasPrefix(rail, "slot:") {
			return []string{fmt.Sprintf("WARN xccl rail inference: %s has no RDMA IP/prefix for %s; retaining topology/inventory order. rdmaN_rail_id is optional and should be filled only to force a known cross-host physical rail mapping", plan.Target.Name, strings.TrimPrefix(rail, "slot:"))}
		}
		device := ""
		if idx < len(plan.RDMADeviceOrder) {
			device = plan.RDMADeviceOrder[idx]
		}
		if devicesByRail[rail] == nil {
			devicesByRail[rail] = map[string]bool{}
		}
		devicesByRail[rail][device] = true
	}
	var warnings []string
	for rail, devices := range devicesByRail {
		if !strings.HasPrefix(rail, "explicit:") && len(devices) > 1 {
			warnings = append(warnings, fmt.Sprintf("WARN xccl rail inference: %s has %d physical RDMA devices in shared fabric %s; retaining topology-derived order. Leave rdmaN_rail_id empty for normal shared-fabric operation, or fill it on every host only to force known isolated-rail correspondence", plan.Target.Name, len(devices), rail))
		}
	}
	sort.Strings(warnings)
	return warnings
}

func validateConfiguredXCCLPlanConsistency(cfg spec.CheckXCCLConfig, plans []xcclTargetPlan) error {
	if !cfg.TopologyValidationEnabled() {
		return nil
	}
	return validateXCCLPlanConsistency(plans)
}

func xcclDeviceSharingShape(devices []string) []string {
	ids := make(map[string]int)
	next := 0
	shape := make([]string, 0, len(devices))
	for _, device := range devices {
		id, ok := ids[device]
		if !ok {
			id = next
			ids[device] = id
			next++
		}
		shape = append(shape, strconv.Itoa(id))
	}
	return shape
}

type xcclNICCandidate struct {
	iface  string
	device string
	nic    string
	rail   string
}

func xcclPlanFromTopology(bundle spec.Bundle, target Target, groups []spec.CheckRDMAGroup, topology xpuTopology) (xcclTargetPlan, error) {
	if len(groups) == 0 {
		return xcclTargetPlan{}, fmt.Errorf("resolve XCCL RDMA mapping for %s: no RDMA groups", target.Name)
	}
	deviceToNIC := map[string]string{}
	for nic, device := range topology.NICDevices {
		deviceToNIC[strings.TrimSpace(device)] = nic
	}
	candidates := make([]xcclNICCandidate, 0, len(groups))
	for idx, group := range groups {
		iface := strings.TrimSpace(targetRDMAInterfaceName(bundle, target, idx))
		device := strings.TrimSpace(group.IBDevice)
		nic := deviceToNIC[device]
		if iface == "" || device == "" || nic == "" {
			return xcclTargetPlan{}, fmt.Errorf("resolve XCCL RDMA mapping for %s rdma%d: iface=%q ib_device=%q is incomplete or absent from xpu-smi topology NIC columns/mapping", target.Name, idx+1, iface, device)
		}
		candidates = append(candidates, xcclNICCandidate{iface: iface, device: device, nic: nic, rail: xcclRDMARail(bundle, target, idx)})
	}

	xpuIndexes := make([]int, 0, len(topology.Links))
	for xpu := range topology.Links {
		xpuIndexes = append(xpuIndexes, xpu)
	}
	sort.Ints(xpuIndexes)
	plan := xcclTargetPlan{Target: target, XPUCount: len(xpuIndexes)}
	seenNICs := map[string]bool{}
	assignments, err := assignXCCLCandidates(topology, xpuIndexes, candidates)
	if err != nil {
		return xcclTargetPlan{}, fmt.Errorf("resolve XCCL RDMA mapping for %s: %w", target.Name, err)
	}
	for row, xpu := range xpuIndexes {
		best := assignments[row]
		bestLink := strings.ToUpper(strings.TrimSpace(topology.Links[xpu][candidates[best].nic]))
		item := candidates[best]
		plan.XPUOrder = append(plan.XPUOrder, xpu)
		plan.RDMANICOrder = append(plan.RDMANICOrder, item.iface)
		plan.RDMADeviceOrder = append(plan.RDMADeviceOrder, item.device)
		plan.RDMALinkOrder = append(plan.RDMALinkOrder, bestLink)
		plan.RDMARailOrder = append(plan.RDMARailOrder, item.rail)
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

// assignXCCLCandidates performs a global assignment. Topology distance is the
// primary objective (PIX before PXB/PHB/NODE/SYS); load balancing is considered
// only among assignments with the same aggregate topology quality.
func assignXCCLCandidates(topology xpuTopology, xpuIndexes []int, candidates []xcclNICCandidate) ([]int, error) {
	if len(xpuIndexes) == 0 || len(candidates) == 0 {
		return nil, errors.New("no XPU or participating NIC candidates")
	}
	type slot struct {
		candidate int
		load      int
	}
	slots := make([]slot, 0, len(xpuIndexes)*len(candidates))
	for candidate := range candidates {
		for load := 0; load < len(xpuIndexes); load++ {
			slots = append(slots, slot{candidate: candidate, load: load})
		}
	}
	const balanceWeight = 1000
	rankWeight := (len(xpuIndexes)*len(xpuIndexes)+1)*balanceWeight + len(candidates)*100 + 1
	const unavailable = int(^uint(0) >> 3)
	costs := make([][]int, len(xpuIndexes))
	for row, xpu := range xpuIndexes {
		costs[row] = make([]int, len(slots))
		reachable := false
		for column, currentSlot := range slots {
			link := topology.Links[xpu][candidates[currentSlot.candidate].nic]
			rank, ok := topologyLinkRank(link)
			if !ok {
				costs[row][column] = unavailable
				continue
			}
			reachable = true
			costs[row][column] = rank*rankWeight + currentSlot.load*balanceWeight + currentSlot.candidate*10 + currentSlot.load
		}
		if !reachable {
			return nil, fmt.Errorf("XPU%d has no reachable participating NIC", xpu)
		}
	}
	columns, err := minimumCostColumns(costs, unavailable)
	if err != nil {
		return nil, err
	}
	assignments := make([]int, len(columns))
	for row, column := range columns {
		assignments[row] = slots[column].candidate
	}
	return assignments, nil
}

// minimumCostColumns is the rectangular Hungarian algorithm. It assigns each
// row to one unique column and returns the selected column for every row.
func minimumCostColumns(costs [][]int, unavailable int) ([]int, error) {
	n := len(costs)
	if n == 0 {
		return nil, nil
	}
	m := len(costs[0])
	if m < n {
		return nil, errors.New("assignment has fewer slots than XPUs")
	}
	for _, row := range costs {
		if len(row) != m {
			return nil, errors.New("assignment cost matrix is not rectangular")
		}
	}
	u := make([]int, n+1)
	v := make([]int, m+1)
	p := make([]int, m+1)
	way := make([]int, m+1)
	for i := 1; i <= n; i++ {
		p[0] = i
		j0 := 0
		minv := make([]int, m+1)
		used := make([]bool, m+1)
		for j := 1; j <= m; j++ {
			minv[j] = unavailable
		}
		for {
			used[j0] = true
			i0 := p[j0]
			delta := unavailable
			j1 := 0
			for j := 1; j <= m; j++ {
				if used[j] {
					continue
				}
				cur := costs[i0-1][j-1] - u[i0] - v[j]
				if cur < minv[j] {
					minv[j] = cur
					way[j] = j0
				}
				if minv[j] < delta {
					delta = minv[j]
					j1 = j
				}
			}
			if delta >= unavailable || j1 == 0 {
				return nil, errors.New("no complete reachable XPU/NIC assignment")
			}
			for j := 0; j <= m; j++ {
				if used[j] {
					u[p[j]] += delta
					v[j] -= delta
				} else {
					minv[j] -= delta
				}
			}
			j0 = j1
			if p[j0] == 0 {
				break
			}
		}
		for {
			j1 := way[j0]
			p[j0] = p[j1]
			j0 = j1
			if j0 == 0 {
				break
			}
		}
	}
	assignment := make([]int, n)
	for j := 1; j <= m; j++ {
		if p[j] > 0 {
			assignment[p[j]-1] = j - 1
		}
	}
	return assignment, nil
}

func xcclRDMARail(bundle spec.Bundle, target Target, index int) string {
	if index < 0 || index >= len(target.RDMA) {
		return fmt.Sprintf("slot:rdma%d", index+1)
	}
	record := target.RDMA[index]
	if railID := strings.TrimSpace(record.RailID); railID != "" {
		return "explicit:" + railID
	}
	ip := net.ParseIP(strings.TrimSpace(record.IP))
	if ip == nil {
		return fmt.Sprintf("slot:rdma%d", index+1)
	}
	prefix := strings.TrimSpace(record.Prefix)
	if prefix == "" && bundle.Defaults.RDMAPrefix > 0 {
		prefix = strconv.Itoa(bundle.Defaults.RDMAPrefix)
	}
	if prefix == "" && ip.To4() != nil {
		prefix = "24"
	}
	if prefix != "" {
		_, network, err := net.ParseCIDR(ip.String() + "/" + prefix)
		if err == nil {
			return network.String()
		}
	}
	return ip.String()
}

func resolveXCCLSocketInterface(opts Options, target Target, plan xcclTargetPlan) (string, error) {
	if value := strings.TrimSpace(opts.Bundle.Check.XCCL.SocketInterface); value != "" {
		command := fmt.Sprintf("test -d /sys/class/net/%s && printf '%%s\\n' %s", shellQuote(value), shellQuote(value))
		output, err := runDiscoveryCommand(opts, target, command)
		if err != nil {
			return "", fmt.Errorf("verify XCCL socket interface %s on %s: %w", value, target.Name, err)
		}
		return strings.TrimSpace(strings.SplitN(output, "\n", 2)[0]), nil
	}
	if len(plan.RDMANICs) == 0 || strings.TrimSpace(plan.RDMANICs[0]) == "" {
		return "", fmt.Errorf("discover XCCL socket interface for %s: no participating RDMA interface is available", target.Name)
	}
	return strings.TrimSpace(plan.RDMANICs[0]), nil
}

func printXCCLPlan(opts Options, plans []xcclTargetPlan) {
	for _, plan := range plans {
		fmt.Fprintf(opts.Output, "INFO xccl topology: %s xpus=%d xpu_order=%s socket_iface=%s unique_rdma_nics(%d)=%s rdma_nics(%d)=%s force_order(%d)=%s rail_order=%s mapping=%s\n",
			plan.Target.Name, plan.XPUCount, xcclPlanVisibleDevices(plan), firstNonEmpty(plan.SocketInterface, "auto"), len(plan.RDMANICs), strings.Join(plan.RDMANICs, ","),
			len(plan.RDMANICOrder), strings.Join(plan.RDMANICOrder, ","), len(plan.RDMANICOrder), strings.Join(plan.RDMANICOrder, ","), strings.Join(plan.RDMARailOrder, ","), strings.Join(plan.Mapping, ";"))
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
		fmt.Fprintf(opts.Output, "dry-run xccl rank environment %s:\n%s", plan.Target.Name, xcclRankScript(opts.Bundle.Check.XCCL, plan, workDir, multiHost))
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
	xcclBinary := filepath.Join(runtimeDir, "xccl_Linux_x86_64", "systest", "xccl_perf")
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

func xcclRankScript(cfg spec.CheckXCCLConfig, plan xcclTargetPlan, workDir string, multiHost bool) string {
	env := xcclManagedRankEnvironment(cfg, plan, multiHost)
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := []string{
		"#!/bin/sh",
		"set -eu",
		"unset XPU_VISIBLE_DEVICES CUDA_VISIBLE_DEVICES BKCL_ENABLE_XDR BKCL_USE_XDR_COPY BKCL_SWITCH_TOPO BKCL_RDMA_VERBS BKCL_TREE_THRESHOLD BKCL_DEBUG BKCL_PERF_DEBUG BKCL_MPIRUN_PRINT_MISMATCH BKCL_RDMA_PROXY_DISABLE BKCL_RING_BUFFER_SIZE BKCL_RING_BUFFER_GM BKCL_RING_OPT BKCL_FLAT_RING BKCL_USE_AR BKCL_USE_RDMA BKCL_FORCE_L3_RDMA BKCL_XLINK_D2D BKCL_XLINK_C2C BKCL_XLINK_ETH XPU_ZEBU_MODE BCCL_TRACE_HANG_ENABLE BCCL_UNIX_SOCKET_PATH BCCL_ERROR_FILE 2>/dev/null || true",
		fmt.Sprintf("export XPU_HOME=%s", shellQuote(strings.TrimRight(cfg.XPUHome, "/"))),
		fmt.Sprintf("if [ -n \"${PATH:-}\" ]; then export PATH=%s:\"$PATH\"; else export PATH=%s; fi", shellQuote(xcclMPICHPrefix+"/bin:"+strings.TrimRight(cfg.XPUHome, "/")+"/bin"), shellQuote(xcclMPICHPrefix+"/bin:"+strings.TrimRight(cfg.XPUHome, "/")+"/bin")),
		fmt.Sprintf("if [ -n \"${LD_LIBRARY_PATH:-}\" ]; then export LD_LIBRARY_PATH=%s:\"$LD_LIBRARY_PATH\"; else export LD_LIBRARY_PATH=%s; fi", shellQuote(xcclLibraryPath(cfg, workDir)), shellQuote(xcclLibraryPath(cfg, workDir))),
	}
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("export %s=%s", key, shellQuote(env[key])))
	}
	lines = append(lines, fmt.Sprintf("exec %s \"$@\"", shellQuote(filepath.Join(workDir, "runtime", "xccl_Linux_x86_64", "systest", "xccl_perf"))))
	return strings.Join(lines, "\n") + "\n"
}

func xcclManagedRankEnvironment(cfg spec.CheckXCCLConfig, plan xcclTargetPlan, multiHost bool) map[string]string {
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
	multiHostDefaults := xcclMultiHostEnvironmentDefaults()
	if multiHost {
		for key, value := range multiHostDefaults {
			env[key] = value
		}
		visibleDevices := xcclPlanVisibleDevices(plan)
		env["CUDA_VISIBLE_DEVICES"] = visibleDevices
		env["XPU_VISIBLE_DEVICES"] = visibleDevices
		if cfg.EnableXDR != nil && *cfg.EnableXDR {
			env["BKCL_USE_XDR_COPY"] = "1"
		}
	}
	if cfg.Supernode {
		env["BKCL_SWITCH_TOPO"] = "1"
		env["BKCL_RDMA_VERBS"] = "1"
		if !multiHost {
			env["BKCL_TREE_THRESHOLD"] = "0"
		}
	}
	for key, value := range cfg.Environment {
		if !multiHost {
			if _, multiHostOnly := multiHostDefaults[key]; multiHostOnly {
				continue
			}
			if key == "BKCL_USE_XDR_COPY" {
				continue
			}
		}
		env[key] = value
	}
	return env
}

func xcclMultiHostEnvironmentDefaults() map[string]string {
	return map[string]string{
		"BKCL_USE_AR":                "1",
		"BKCL_RING_OPT":              "1",
		"BKCL_DEBUG":                 "0",
		"BKCL_PERF_DEBUG":            "0",
		"BKCL_MPIRUN_PRINT_MISMATCH": "0",
		"BKCL_RDMA_PROXY_DISABLE":    "1",
		"BKCL_RING_BUFFER_SIZE":      "2097152",
		"XPU_ZEBU_MODE":              "1",
		"BKCL_XLINK_D2D":             "0",
		"BKCL_XLINK_C2C":             "1",
		"BKCL_XLINK_ETH":             "0",
		"BKCL_TREE_THRESHOLD":        "1",
		"BKCL_RING_BUFFER_GM":        "1",
		"BKCL_FLAT_RING":             "1",
		"BKCL_USE_RDMA":              "1",
		"BKCL_FORCE_L3_RDMA":         "0",
		"BCCL_TRACE_HANG_ENABLE":     "1",
		"BCCL_UNIX_SOCKET_PATH":      "/var/bccl/sockets",
		"BCCL_ERROR_FILE":            "/var/bccl/logs/err.%h.%p.log",
	}
}

func xcclPreviewRankEnvironment(cfg spec.CheckXCCLConfig, multiHost bool) map[string]string {
	plan := xcclTargetPlan{XPUCount: 0}
	env := xcclManagedRankEnvironment(cfg, plan, multiHost)
	env["XPU_HOME"] = strings.TrimRight(cfg.XPUHome, "/")
	env["PATH"] = xcclMPICHPrefix + "/bin:" + strings.TrimRight(cfg.XPUHome, "/") + "/bin:${PATH:-}"
	env["LD_LIBRARY_PATH"] = "<run>/runtime/xccl_Linux_x86_64/so:" + xcclMPICHPrefix + "/lib:" + strings.TrimRight(cfg.XPUHome, "/") + "/so:...:${LD_LIBRARY_PATH:-}"
	dynamicKeys := []string{"BKCL_RDMA_NICS", "BKCL_FORCE_RDMA_NICS_ORDER", "BKCL_SOCKET_IFNAME"}
	if multiHost {
		dynamicKeys = append(dynamicKeys, "CUDA_VISIBLE_DEVICES", "XPU_VISIBLE_DEVICES")
	}
	for _, key := range dynamicKeys {
		if _, ok := env[key]; !ok || env[key] == "" {
			env[key] = "<resolved per host after topology discovery>"
		}
	}
	return env
}

func printXCCLRankEnvironments(opts Options, cfg spec.CheckXCCLConfig, plans []xcclTargetPlan, workDir string, multiHost bool) {
	for _, plan := range plans {
		env := xcclManagedRankEnvironment(cfg, plan, multiHost)
		env["XPU_HOME"] = strings.TrimRight(cfg.XPUHome, "/")
		env["PATH"] = xcclMPICHPrefix + "/bin:" + strings.TrimRight(cfg.XPUHome, "/") + "/bin:${PATH:-}"
		env["LD_LIBRARY_PATH"] = xcclLibraryPath(cfg, workDir) + ":${LD_LIBRARY_PATH:-}"
		keys := make([]string, 0, len(env))
		for key := range env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		fmt.Fprintf(opts.Output, "INFO xccl environment: host=%s variables=%d\n", plan.Target.Name, len(keys))
		for _, key := range keys {
			fmt.Fprintf(opts.Output, "ENV xccl %s %s=%s\n", plan.Target.Name, key, env[key])
		}
	}
}

func xcclVisibleDevices(order []int) string {
	devices := make([]string, 0, len(order))
	for _, index := range order {
		devices = append(devices, strconv.Itoa(index))
	}
	return strings.Join(devices, ",")
}

func xcclPlanVisibleDevices(plan xcclTargetPlan) string {
	if len(plan.XPUOrder) == plan.XPUCount {
		return xcclVisibleDevices(plan.XPUOrder)
	}
	order := make([]int, plan.XPUCount)
	for index := range order {
		order[index] = index
	}
	return xcclVisibleDevices(order)
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
		lines = append(lines, fmt.Sprintf("%s:%d", targetControlAddress(plan.Target), plan.XPUCount))
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
		addresses = append(addresses, shellQuote(targetControlAddress(plan.Target)))
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
	collective, _ := xcclCollectiveName(cfg.Test)
	args = append(args,
		"-np", strconv.Itoa(totalRanks),
		"-wdir", workDir,
		filepath.Join(workDir, "run-rank.sh"),
		"-O", collective,
		"-x", "1",
		"-b", cfg.MinBytes,
		"-e", cfg.MaxBytes,
		"-f", strconv.Itoa(cfg.StepFactor),
	)
	if cfg.WarmupIterations > 0 {
		args = append(args, "-w", strconv.Itoa(cfg.WarmupIterations))
	}
	args = append(args,
		"-n", strconv.Itoa(cfg.Iterations),
		"-c", "0",
		"-d", cfg.DataType,
	)
	if normalizedXCCLLayout(cfg.Layout) == "same_index" {
		args = append(args,
			"--split_mode",
			"--split_step", strconv.Itoa(cfg.SplitStep),
			"--split_op", strconv.Itoa(cfg.SplitOperation),
		)
	}
	return args
}

func xcclCollectiveName(test string) (string, bool) {
	collectives := map[string]string{
		"all_reduce":     "allReduce",
		"all_gather":     "allGather",
		"reduce_scatter": "reduceScatter",
		"reduce":         "reduce",
		"broadcast":      "broadcast",
		"all_to_all":     "alltoall",
		"sendrecv":       "sendrecv",
	}
	name, ok := collectives[strings.TrimSpace(test)]
	return name, ok
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
	mpirunPID := filepath.Join(workDir, "mpirun.pid")
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
		fmt.Sprintf("if [ -f %s ]; then p=$(cat %s 2>/dev/null || true); case \"$p\" in ''|*[!0-9]*) ;; *) if [ -r \"/proc/$p/cmdline\" ] && tr '\\0' ' ' < \"/proc/$p/cmdline\" | grep -F -q -- %s; then kill \"$p\" >/dev/null 2>&1 || true; fi ;; esac; fi", shellQuote(mpirunPID), shellQuote(mpirunPID), shellQuote(workDir)),
		fmt.Sprintf("if [ -f %s ] && [ -L %s ] && [ \"$(readlink %s)\" = %s ]; then rm -f %s; fi", shellQuote(filepath.Join(workDir, "mpich-link-created")), shellQuote(xcclMPICHPrefix), shellQuote(xcclMPICHPrefix), shellQuote(temporaryMPICH), shellQuote(xcclMPICHPrefix)),
		fmt.Sprintf("rm -rf -- %s", shellQuote(workDir)),
	)
	return strings.Join(commands, "; ")
}

func xcclTrackedMPIRunCommand(workDir string, args []string) string {
	pidPath := filepath.Join(workDir, "mpirun.pid")
	return fmt.Sprintf("printf '%%s\\n' \"$$\" > %s; exec %s", shellQuote(pidPath), shellJoin(args))
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
		// systest/xccl_perf prints both out-of-place and in-place metrics:
		// size count type redop root out_time out_alg out_bus out_wrong in_time in_alg in_bus in_wrong.
		// Preserve both triplets so operators can inspect the complete curve. The
		// delivery SOP evaluation later selects the in-place triplet explicitly.
		if len(fields) >= 13 {
			for _, metrics := range []struct {
				mode                          string
				timeIndex, algIndex, busIndex int
			}{
				{mode: "out-of-place", timeIndex: 5, algIndex: 6, busIndex: 7},
				{mode: "in-place", timeIndex: 9, algIndex: 10, busIndex: 11},
			} {
				timeUS, err3 := strconv.ParseFloat(fields[metrics.timeIndex], 64)
				algGBs, err4 := strconv.ParseFloat(fields[metrics.algIndex], 64)
				busGBs, err5 := strconv.ParseFloat(fields[metrics.busIndex], 64)
				if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil {
					continue
				}
				rows = append(rows, xcclPerformanceRow{
					SizeBytes: sizeBytes,
					Count:     count,
					DataType:  fields[2],
					Operation: fields[3],
					Mode:      metrics.mode,
					TimeUS:    timeUS,
					AlgGBs:    algGBs,
					BusGBs:    busGBs,
				})
			}
			continue
		}
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
			Mode:      "out-of-place",
			TimeUS:    timeUS,
			AlgGBs:    algGBs,
			BusGBs:    busGBs,
		})
	}
	return rows
}

type xcclEvaluation struct {
	Status              string
	Selected            xcclPerformanceRow
	Degraded            bool
	Hosts               []string
	Topology            string
	Layout              string
	MachineClass        string
	EvaluationMode      string
	BaselineGBs         float64
	RequiredUtilization float64
	MeasuredUtilization float64
	ManualMinimumBusGBs float64
}

func evaluateXCCLResult(opts Options, plans []xcclTargetPlan, rows []xcclPerformanceRow) xcclEvaluation {
	cfg := opts.Bundle.Check.XCCL
	selected := selectXCCLPerformanceRow(rows)
	status := "PASS"
	mode := normalizedXCCLEvaluationMode(cfg.EvaluationMode)
	baseline, required := xcclAutomaticEvaluationRule(cfg)
	utilization := 0.0
	if baseline > 0 {
		utilization = selected.BusGBs / baseline
	}
	switch mode {
	case "auto":
		if utilization <= required {
			status = "FAIL"
		}
	case "manual":
		if selected.BusGBs < cfg.MinBusBandwidthGBs {
			status = "FAIL"
		}
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
	return xcclEvaluation{
		Status: status, Selected: selected, Degraded: degraded, Hosts: hostNames, Topology: topology,
		Layout: normalizedXCCLLayout(cfg.Layout), MachineClass: normalizedXCCLMachineClass(cfg.MachineClass), EvaluationMode: mode,
		BaselineGBs: baseline, RequiredUtilization: required, MeasuredUtilization: utilization, ManualMinimumBusGBs: cfg.MinBusBandwidthGBs,
	}
}

func xcclAutomaticEvaluationRule(cfg spec.CheckXCCLConfig) (float64, float64) {
	if normalizedXCCLLayout(cfg.Layout) == "same_index" {
		return 200, 0.90
	}
	if normalizedXCCLMachineClass(cfg.MachineClass) == "vd" {
		return 150, 0.60
	}
	return 100, 0.60
}

func printXCCLResult(opts Options, plans []xcclTargetPlan, totalRanks int, rows []xcclPerformanceRow) error {
	return printXCCLResultEvaluation(opts, totalRanks, rows, evaluateXCCLResult(opts, plans, rows))
}

func printXCCLResultEvaluation(opts Options, totalRanks int, rows []xcclPerformanceRow, evaluation xcclEvaluation) error {
	selected := evaluation.Selected
	status := evaluation.Status
	headers := []string{"STATUS", "TEST", "LAYOUT", "HOSTS", "RANKS", "TOPOLOGY", "MODE", "SIZE(B)", "TIME(us)", "ALGBW(GB/s)", "BUSBW(GB/s)"}
	cells := []string{status, opts.Bundle.Check.XCCL.Test, evaluation.Layout, strings.Join(evaluation.Hosts, ","), strconv.Itoa(totalRanks), evaluation.Topology, firstNonEmpty(selected.Mode, "out-of-place"), strconv.FormatInt(selected.SizeBytes, 10), fmt.Sprintf("%.2f", selected.TimeUS), fmt.Sprintf("%.2f", selected.AlgGBs), fmt.Sprintf("%.2f", selected.BusGBs)}
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
	printXCCLEvaluationRule(opts.Output, evaluation)
	printXCCLSizeResults(opts.Output, rows, selected)
	if evaluation.Degraded {
		fmt.Fprintln(opts.Output, "WARN xccl result topology: at least one XPU used a non-PIX RDMA mapping; collective bandwidth may be limited by the PCIe/NUMA path")
	}
	if status == "FAIL" {
		if evaluation.EvaluationMode == "auto" {
			return fmt.Errorf("XCCL %s %s utilization %.2f%% is not greater than %.2f%% (%.2f/%.2f GB/s) at size %d", opts.Bundle.Check.XCCL.Test, evaluation.Layout, evaluation.MeasuredUtilization*100, evaluation.RequiredUtilization*100, selected.BusGBs, evaluation.BaselineGBs, selected.SizeBytes)
		}
		return fmt.Errorf("XCCL %s %.2f GB/s below %.2f GB/s at size %d", opts.Bundle.Check.XCCL.Test, selected.BusGBs, evaluation.ManualMinimumBusGBs, selected.SizeBytes)
	}
	return nil
}

func printXCCLEvaluationRule(output io.Writer, evaluation xcclEvaluation) {
	switch evaluation.EvaluationMode {
	case "auto":
		machine := "n/a"
		if evaluation.Layout == "full_ring" {
			machine = strings.ToUpper(evaluation.MachineClass)
		}
		fmt.Fprintf(output, "XCCL evaluation: mode=auto layout=%s machine_class=%s measured=%.2f GB/s baseline=%.2f GB/s utilization=%.3f%% requirement=utilization>%.2f%%\n",
			evaluation.Layout, machine, evaluation.Selected.BusGBs, evaluation.BaselineGBs, evaluation.MeasuredUtilization*100, evaluation.RequiredUtilization*100)
	case "manual":
		fmt.Fprintf(output, "XCCL evaluation: mode=manual measured=%.2f GB/s minimum=%.2f GB/s\n", evaluation.Selected.BusGBs, evaluation.ManualMinimumBusGBs)
	default:
		fmt.Fprintln(output, "XCCL evaluation: mode=disabled (result recorded without a bandwidth pass/fail threshold)")
	}
}

func printXCCLSizeResults(output io.Writer, rows []xcclPerformanceRow, selected xcclPerformanceRow) {
	ordered := append([]xcclPerformanceRow(nil), rows...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].SizeBytes != ordered[j].SizeBytes {
			return ordered[i].SizeBytes < ordered[j].SizeBytes
		}
		return xcclModeOrder(ordered[i].Mode) < xcclModeOrder(ordered[j].Mode)
	})
	headers := []string{"EVAL", "SIZE(B)", "COUNT", "TYPE", "OP", "MODE", "TIME(us)", "ALGBW(GB/s)", "BUSBW(GB/s)"}
	data := make([][]string, 0, len(ordered))
	for _, row := range ordered {
		marker := ""
		if row.SizeBytes == selected.SizeBytes && row.Mode == selected.Mode {
			marker = "*"
		}
		data = append(data, []string{
			marker,
			strconv.FormatInt(row.SizeBytes, 10),
			strconv.FormatInt(row.Count, 10),
			row.DataType,
			row.Operation,
			firstNonEmpty(row.Mode, "out-of-place"),
			fmt.Sprintf("%.2f", row.TimeUS),
			fmt.Sprintf("%.2f", row.AlgGBs),
			fmt.Sprintf("%.2f", row.BusGBs),
		})
	}
	widths := make([]int, len(headers))
	for idx, header := range headers {
		widths[idx] = len(header)
	}
	for _, cells := range data {
		for idx, cell := range cells {
			widths[idx] = maxInt(widths[idx], len(cell))
		}
	}
	fmt.Fprintln(output, "XCCL size result details (* = SOP evaluation row):")
	fmt.Fprintln(output, formatTableLine(headers, widths))
	fmt.Fprintln(output, formatTableSeparator(widths))
	for _, cells := range data {
		fmt.Fprintln(output, formatTableLine(cells, widths))
	}
}

func xcclModeOrder(mode string) int {
	if mode == "out-of-place" || mode == "" {
		return 0
	}
	if mode == "in-place" {
		return 1
	}
	return 2
}

func selectXCCLPerformanceRow(rows []xcclPerformanceRow) xcclPerformanceRow {
	if len(rows) == 0 {
		return xcclPerformanceRow{}
	}
	uniqueSizes := map[int64]bool{}
	for _, row := range rows {
		uniqueSizes[row.SizeBytes] = true
	}
	sizes := make([]int64, 0, len(uniqueSizes))
	for size := range uniqueSizes {
		sizes = append(sizes, size)
	}
	sort.Slice(sizes, func(i, j int) bool { return sizes[i] < sizes[j] })
	selectedSize := sizes[len(sizes)-1]
	if len(sizes) >= 2 {
		selectedSize = sizes[len(sizes)-2]
	}
	var selected xcclPerformanceRow
	found := false
	for _, row := range rows {
		if row.SizeBytes != selectedSize {
			continue
		}
		if row.Mode == "in-place" {
			selected = row
			return selected
		}
		if !found || row.BusGBs > selected.BusGBs {
			selected = row
			found = true
		}
	}
	return selected
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
