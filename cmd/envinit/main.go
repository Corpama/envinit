package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"envinit/internal/bundle"
	"envinit/internal/checker"
	"envinit/internal/inventory"
	"envinit/internal/runner"
	"envinit/internal/spec"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "plan":
		if err := run(true, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "apply":
		if err := run(false, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "check":
		if err := runCheck(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintln(os.Stderr, "unknown subcommand:", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func runCheck(args []string) error {
	fs := flag.NewFlagSet("envinit check", flag.ContinueOnError)
	inventoryPath := fs.String("inventory", "", "Path to the inventory file (.csv/.tsv/.txt/.xlsx)")
	bundlePath := fs.String("bundle", "", "Path to the offline installation bundle JSON")
	hostsRaw := fs.String("hosts", "", "Host IDs, hostnames, or IPs separated by commas or spaces")
	sheet := fs.String("sheet", "", "Worksheet name for .xlsx inventories, defaults to the first sheet")
	checkStageRaw := fs.String("check-stage", "all", "Check stages to run: bandwidth, rdma-ping, or all")
	checksRaw := fs.String("checks", "", "Deprecated alias for --check-stage")
	emuKVTransfer := fs.Bool("emu-kv-transfer", false, "Enable 8MiB ib_write_bw message size to emulate KV cache transfer")
	bandwidthMmap := fs.String("bandwidth-mmap", "", "Enable bandwidth mmap mode; supported value: xdr")
	rdmaPingCount := fs.Int("rdma-ping-count", 0, "Override RDMA ping packet count")
	rdmaPingMTU := fs.Int("rdma-ping-mtu", 0, "Override RDMA ping MTU; payload is calculated as MTU-28 for IPv4")
	rdmaPingTimeout := fs.Int("rdma-ping-timeout", 0, "Override RDMA ping timeout in seconds")
	dryRun := fs.Bool("dry-run", false, "Print ssh/ib_write_bw commands without executing them")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*inventoryPath) == "" {
		return fmt.Errorf("--inventory is required")
	}
	if strings.TrimSpace(*bundlePath) == "" {
		return fmt.Errorf("--bundle is required")
	}
	if strings.TrimSpace(*hostsRaw) == "" {
		return fmt.Errorf("--hosts is required")
	}
	rawCheckStage := *checkStageRaw
	if strings.TrimSpace(*checksRaw) != "" {
		rawCheckStage = *checksRaw
	}
	runBandwidth, runRDMAPing, err := parseCheckStages(rawCheckStage)
	if err != nil {
		return err
	}
	b, err := bundle.Load(*bundlePath)
	if err != nil {
		return err
	}
	if err := applyCheckOverrides(&b, checkOverrideOptions{
		emuKVTransfer:   *emuKVTransfer,
		bandwidthMmap:   *bandwidthMmap,
		rdmaPingCount:   *rdmaPingCount,
		rdmaPingMTU:     *rdmaPingMTU,
		rdmaPingTimeout: *rdmaPingTimeout,
	}); err != nil {
		return err
	}
	records, err := inventory.Load(*inventoryPath, *sheet)
	if err != nil {
		return err
	}
	return checker.Run(checker.Options{
		Bundle:       b,
		Records:      records,
		Hosts:        []string{*hostsRaw},
		RunBandwidth: runBandwidth,
		RunRDMAPing:  runRDMAPing,
		DryRun:       *dryRun,
		Output:       os.Stdout,
	})
}

type checkOverrideOptions struct {
	emuKVTransfer   bool
	bandwidthMmap   string
	rdmaPingCount   int
	rdmaPingMTU     int
	rdmaPingTimeout int
}

func applyCheckOverrides(b *spec.Bundle, opts checkOverrideOptions) error {
	if opts.rdmaPingCount < 0 {
		return fmt.Errorf("--rdma-ping-count must be greater than 0")
	}
	if opts.rdmaPingTimeout < 0 {
		return fmt.Errorf("--rdma-ping-timeout must be greater than 0")
	}
	if opts.rdmaPingMTU < 0 {
		return fmt.Errorf("--rdma-ping-mtu must be greater than 28")
	}

	b.Check.MessageSize = 0
	b.Check.MmapDevice = ""
	if opts.emuKVTransfer {
		b.Check.MessageSize = 8388608
	}
	switch strings.ToLower(strings.TrimSpace(opts.bandwidthMmap)) {
	case "":
	case "none", "off", "false":
	case "xdr":
		b.Check.MmapDevice = "/dev/xdrdrv"
	default:
		return fmt.Errorf("--bandwidth-mmap supports only xdr")
	}

	if opts.rdmaPingCount > 0 {
		b.Check.RDMAPingCount = opts.rdmaPingCount
	}
	if opts.rdmaPingTimeout > 0 {
		b.Check.RDMAPingTimeout = opts.rdmaPingTimeout
	}
	if opts.rdmaPingMTU > 0 {
		if opts.rdmaPingMTU <= 28 {
			return fmt.Errorf("--rdma-ping-mtu must be greater than 28")
		}
		b.Check.RDMAPingPayloadSize = opts.rdmaPingMTU - 28
	}
	return nil
}

func parseCheckStages(raw string) (bool, bool, error) {
	runBandwidth := false
	runRDMAPing := false
	normalized := strings.NewReplacer(",", " ", ";", " ", "|", " ").Replace(raw)
	for _, item := range strings.Fields(normalized) {
		switch strings.ToLower(strings.TrimSpace(item)) {
		case "all":
			runBandwidth = true
			runRDMAPing = true
		case "bandwidth", "bw", "ib", "ib_write_bw":
			runBandwidth = true
		case "rdma-ping", "rdma_ping", "ping":
			runRDMAPing = true
		default:
			return false, false, fmt.Errorf("unknown check-stage %q; use bandwidth, rdma-ping, or all", item)
		}
	}
	if !runBandwidth && !runRDMAPing {
		return false, false, fmt.Errorf("--check-stage requires at least one value")
	}
	return runBandwidth, runRDMAPing, nil
}

func run(dryRun bool, args []string) error {
	normalizedArgs, err := normalizeArgsForStages(args)
	if err != nil {
		return err
	}

	fs := flag.NewFlagSet("envinit", flag.ContinueOnError)
	inventoryPath := fs.String("inventory", "", "Path to the inventory file (.csv/.tsv/.txt/.xlsx)")
	bundlePath := fs.String("bundle", "", "Path to the offline installation bundle JSON")
	host := fs.String("host", "", "host_id/hostname/mgmt_ip from the inventory; auto-detect the current machine when omitted")
	root := fs.String("root", "/", "Alternate filesystem root for testing, defaults to /")
	sheet := fs.String("sheet", "", "Worksheet name for .xlsx inventories, defaults to the first sheet")
	stagesRaw := fs.String("stages", "all", "Stages separated by commas or spaces: all,apt,ofed,udev,network,xre,xdr,firmware,container,mlxconfig,sysctl,iommu,post")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(normalizedArgs); err != nil {
		return err
	}
	if strings.TrimSpace(*inventoryPath) == "" {
		return fmt.Errorf("--inventory is required")
	}
	if strings.TrimSpace(*bundlePath) == "" {
		return fmt.Errorf("--bundle is required")
	}

	b, err := bundle.Load(*bundlePath)
	if err != nil {
		return err
	}
	records, err := inventory.Load(*inventoryPath, *sheet)
	if err != nil {
		return err
	}

	stages, err := parseStages(*stagesRaw)
	if err != nil {
		return err
	}
	app, err := runner.New(b, records, *host, *root, dryRun, stages, os.Stdout)
	if err != nil {
		return err
	}

	description, err := app.Describe()
	if err != nil {
		return err
	}
	fmt.Print(description)
	if dryRun {
		return nil
	}
	if err := app.Apply(); err != nil {
		return err
	}
	fmt.Println("Initialization completed.")
	return nil
}

func parseStages(raw string) (map[string]bool, error) {
	out := map[string]bool{}
	normalized := strings.ReplaceAll(raw, ",", " ")
	for _, item := range strings.Fields(normalized) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if !runner.IsKnownStage(item) {
			return nil, fmt.Errorf("unknown stage %q", item)
		}
		out[item] = true
	}
	if len(out) == 0 {
		out["all"] = true
	}
	return out, nil
}

func normalizeArgsForStages(args []string) ([]string, error) {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--stages" || arg == "-stages":
			values, next, err := collectStageValues(args, i+1)
			if err != nil {
				return nil, err
			}
			out = append(out, arg, strings.Join(values, ","))
			i = next - 1
		case strings.HasPrefix(arg, "--stages=") || strings.HasPrefix(arg, "-stages="):
			idx := strings.IndexByte(arg, '=')
			base := arg[:idx+1]
			initial := arg[idx+1:]
			values := []string{}
			if strings.TrimSpace(initial) != "" {
				values = append(values, initial)
			}
			collected, next, err := collectStageValues(args, i+1)
			if err != nil && len(values) == 0 {
				return nil, err
			}
			values = append(values, collected...)
			if len(values) == 0 {
				return nil, fmt.Errorf("--stages requires at least one value")
			}
			out = append(out, base+strings.Join(values, ","))
			i = next - 1
		default:
			out = append(out, arg)
		}
	}
	return out, nil
}

func collectStageValues(args []string, start int) ([]string, int, error) {
	values := make([]string, 0, 2)
	i := start
	for ; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			break
		}
		values = append(values, args[i])
	}
	if len(values) == 0 {
		return nil, start, fmt.Errorf("--stages requires at least one value")
	}
	return values, i, nil
}

func usage() {
	fmt.Fprintf(os.Stderr, `envinit

Usage:
  envinit plan  --inventory ./machines.xlsx --bundle ./bundle.json [--host xpu11]
  envinit apply --inventory ./machines.xlsx --bundle ./bundle.json [--host xpu11]
  envinit check --inventory ./machines.xlsx --bundle ./bundle.json --hosts xpu11,xpu12 [--check-stage bandwidth|rdma-ping|all]

Notes:
  plan   Parse the inventory and print the planned network and installation actions
  apply  Write files and execute commands; root privileges are required
  check  Run RDMA/XPU bandwidth and jumbo RDMA ping checks across two or more hosts
`)
}
