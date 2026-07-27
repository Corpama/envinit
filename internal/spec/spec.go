package spec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
)

type Bundle struct {
	Defaults        Defaults         `json:"defaults"`
	Platform        PlatformConfig   `json:"platform"`
	PlatformOptions PlatformOptions  `json:"platform_options"`
	OfflineAPT      OfflineAPTConfig `json:"offline_apt"`
	OfflineRepo     OfflineAPTConfig `json:"offline_repo"`
	Packages        []string         `json:"packages"`
	Artifacts       Artifacts        `json:"artifacts"`
	XRE             XREConfig        `json:"xre"`
	MlxConfig       MlxConfig        `json:"mlxconfig"`
	Check           CheckConfig      `json:"check"`
	PostPackages    []string         `json:"post_packages"`
	PostTasks       []PostTask       `json:"post_tasks"`
	PostPowerAction PostPowerAction  `json:"post_power_action"`
}

type Defaults struct {
	MgmtBondName               string                 `json:"mgmt_bond_name"`
	MgmtInterfaces             []string               `json:"mgmt_interfaces"`
	MgmtPrefix                 int                    `json:"mgmt_prefix"`
	MgmtGateway                string                 `json:"mgmt_gateway"`
	MgmtNameservers            []string               `json:"mgmt_nameservers"`
	MgmtMTU                    int                    `json:"mgmt_mtu"`
	BondMode                   string                 `json:"bond_mode"`
	BondLACPRate               string                 `json:"bond_lacp_rate"`
	BondTransmitHashPolicy     string                 `json:"bond_transmit_hash_policy"`
	BondMIIMonitorInterval     int                    `json:"bond_mii_monitor_interval"`
	BondPrimary                string                 `json:"bond_primary"`
	RDMAPrefix                 int                    `json:"rdma_prefix"`
	RDMAMTU                    int                    `json:"rdma_mtu"`
	RDMARouteCIDR              string                 `json:"rdma_route_cidr"`
	RoutePriority              int                    `json:"route_priority"`
	RDMAMode                   string                 `json:"rdma_mode,omitempty"`
	ConfigureManagementNetwork *bool                  `json:"configure_management_network,omitempty"`
	ApplyNetworkImmediately    *bool                  `json:"apply_network_immediately,omitempty"`
	BackupExistingNetplan      bool                   `json:"backup_existing_netplan"`
	BackupExistingNetwork      bool                   `json:"backup_existing_network"`
	DisableExistingAptSources  bool                   `json:"disable_existing_apt_sources"`
	DisableExistingRepos       bool                   `json:"disable_existing_repos"`
	RDMAInterfaces             []RDMAInterfaceDefault `json:"rdma_interfaces"`
}

const (
	RDMAModeFull      = "full"
	RDMAModeNamesOnly = "names_only"
	RDMAModeOff       = "off"
)

type PlatformOptions struct {
	Ubuntu UbuntuPlatformOptions `json:"ubuntu"`
	Kylin  YumPlatformOptions    `json:"kylin"`
	RedHat YumPlatformOptions    `json:"redhat"`
}

type UbuntuPlatformOptions struct {
	BackupExistingNetplan     *bool `json:"backup_existing_netplan,omitempty"`
	DisableExistingAptSources *bool `json:"disable_existing_apt_sources,omitempty"`
}

type YumPlatformOptions struct {
	BackupExistingNetwork *bool `json:"backup_existing_network,omitempty"`
	DisableExistingRepos  *bool `json:"disable_existing_repos,omitempty"`
}

type PlatformConfig struct {
	OSFamily             string `json:"os_family"`
	PackageManager       string `json:"package_manager"`
	NetworkBackend       string `json:"network_backend"`
	KernelHeadersPackage string `json:"kernel_headers_package"`
	KernelHeadersDir     string `json:"kernel_headers_dir"`
}

type RDMAInterfaceDefault struct {
	Name    string `json:"name"`
	Table   int    `json:"table"`
	Gateway string `json:"gateway"`
}

type OfflineAPTConfig struct {
	MaterialPath string   `json:"material_path"`
	CopyTo       string   `json:"copy_to"`
	Enabled      bool     `json:"enabled"`
	TargetFile   string   `json:"target_file"`
	Entries      []string `json:"entries"`
}

type Artifacts struct {
	WorkDir           string   `json:"work_dir"`
	OFEDArchive       string   `json:"ofed_archive"`
	XREInstaller      string   `json:"xre_installer"`
	XREArgs           []string `json:"xre_args"`
	XDRArchive        string   `json:"xdr_archive"`
	FirmwareArchive   string   `json:"firmware_archive"`
	ContainerPackages []string `json:"container_packages"`
}

type XREConfig struct {
	CardModel string `json:"card_model"`
}

type MlxConfig struct {
	DeviceGlob string            `json:"device_glob"`
	Settings   map[string]string `json:"settings"`
}

type CheckConfig struct {
	Bandwidth CheckBandwidthConfig `json:"bandwidth"`
	RDMAPing  CheckRDMAPingConfig  `json:"rdma_ping"`
	SSH       CheckSSHConfig       `json:"ssh"`
	XCCL      CheckXCCLConfig      `json:"xccl"`
}

type CheckBandwidthConfig struct {
	Duration      int              `json:"duration"`
	RunByDuration bool             `json:"run_by_duration,omitempty"`
	GIDIndex      int              `json:"gid_index"`
	Iterations    int              `json:"iterations"`
	BandwidthQPs  int              `json:"bandwidth_qps"`
	MessageSize   int              `json:"message_size"`
	ReportGBits   bool             `json:"report_gbits"`
	MmapDevice    string           `json:"mmap_device"`
	MinGBits      float64          `json:"min_gbits"`
	MinGBitsAuto  bool             `json:"-"`
	MinGBitsSet   bool             `json:"-"`
	Parallel      bool             `json:"parallel"`
	BasePort      int              `json:"base_port"`
	RDMAGroups    []CheckRDMAGroup `json:"rdma_groups,omitempty"`
}

func (c *CheckBandwidthConfig) UnmarshalJSON(data []byte) error {
	type bandwidthAlias CheckBandwidthConfig
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	allowed := map[string]bool{
		"duration": true, "run_by_duration": true, "gid_index": true, "iterations": true, "bandwidth_qps": true,
		"message_size": true, "report_gbits": true, "mmap_device": true, "min_gbits": true,
		"parallel": true, "base_port": true, "rdma_groups": true,
	}
	for key := range raw {
		if !allowed[key] {
			return fmt.Errorf("json: unknown field %q", key)
		}
	}
	minimum, hasMinimum := raw["min_gbits"]
	auto := false
	if hasMinimum && strings.TrimSpace(string(minimum)) == "null" {
		return errors.New("check.bandwidth.min_gbits must be \"auto\", 0, or a positive number")
	}
	if hasMinimum && len(minimum) > 0 && minimum[0] == '"' {
		var mode string
		if err := json.Unmarshal(minimum, &mode); err != nil {
			return fmt.Errorf("check.bandwidth.min_gbits: %w", err)
		}
		if !strings.EqualFold(strings.TrimSpace(mode), "auto") {
			return fmt.Errorf("check.bandwidth.min_gbits must be \"auto\", 0, or a positive number, got %q", mode)
		}
		auto = true
		raw["min_gbits"] = json.RawMessage("0")
	}
	normalized, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	var decoded bandwidthAlias
	if err := json.Unmarshal(normalized, &decoded); err != nil {
		return err
	}
	if decoded.MinGBits < 0 {
		return errors.New("check.bandwidth.min_gbits must be \"auto\", 0, or a positive number")
	}
	*c = CheckBandwidthConfig(decoded)
	c.MinGBitsAuto = auto
	c.MinGBitsSet = hasMinimum
	return nil
}

func (c CheckBandwidthConfig) MarshalJSON() ([]byte, error) {
	type bandwidthAlias CheckBandwidthConfig
	raw, err := json.Marshal(bandwidthAlias(c))
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if c.MinGBitsAuto {
		fields["min_gbits"] = json.RawMessage(`"auto"`)
	}
	return json.Marshal(fields)
}

func (c CheckBandwidthConfig) MinGBitsMode() string {
	if c.MinGBitsAuto {
		return "auto"
	}
	if c.MinGBits > 0 {
		return "manual"
	}
	return "disabled"
}

type CheckRDMAPingConfig struct {
	Count       int `json:"count"`
	PayloadSize int `json:"payload_size"`
	Timeout     int `json:"timeout"`
}

type CheckSSHConfig struct {
	User    string   `json:"user"`
	Options []string `json:"options"`
}

func (c *CheckConfig) UnmarshalJSON(data []byte) error {
	type checkConfigAlias CheckConfig
	var decoded checkConfigAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	allowed := map[string]bool{
		"bandwidth": true, "rdma_ping": true, "ssh": true, "xccl": true,
		"duration": true, "run_by_duration": true, "gid_index": true, "iterations": true, "bandwidth_qps": true,
		"message_size": true, "report_gbits": true, "mmap_device": true, "min_gbits": true,
		"parallel": true, "base_port": true, "rdma_groups": true,
		"rdma_ping_count": true, "rdma_ping_payload_size": true, "rdma_ping_timeout": true,
		"ssh_user": true, "ssh_options": true,
	}
	for key := range raw {
		if !allowed[key] {
			return fmt.Errorf("json: unknown field %q", key)
		}
	}
	for key, target := range map[string]any{
		"bandwidth": &decoded.Bandwidth,
		"rdma_ping": &decoded.RDMAPing,
		"ssh":       &decoded.SSH,
		"xccl":      &decoded.XCCL,
	} {
		value, ok := raw[key]
		if !ok {
			continue
		}
		if err := decodeStrictJSON(value, target); err != nil {
			return err
		}
	}
	if _, nested := raw["bandwidth"]; !nested {
		legacyFields := map[string]json.RawMessage{}
		for _, key := range []string{"duration", "gid_index", "iterations", "bandwidth_qps", "message_size", "report_gbits", "mmap_device", "min_gbits", "parallel", "base_port", "rdma_groups"} {
			if value, ok := raw[key]; ok {
				legacyFields[key] = value
			}
		}
		legacyData, err := json.Marshal(legacyFields)
		if err != nil {
			return err
		}
		var legacy CheckBandwidthConfig
		if err := json.Unmarshal(legacyData, &legacy); err != nil {
			return err
		}
		decoded.Bandwidth = legacy
	}
	var legacy struct {
		RDMAPingCount       int      `json:"rdma_ping_count"`
		RDMAPingPayloadSize int      `json:"rdma_ping_payload_size"`
		RDMAPingTimeout     int      `json:"rdma_ping_timeout"`
		SSHUser             string   `json:"ssh_user"`
		SSHOptions          []string `json:"ssh_options"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return err
	}
	if _, nested := raw["rdma_ping"]; !nested {
		decoded.RDMAPing = CheckRDMAPingConfig{
			Count:       legacy.RDMAPingCount,
			PayloadSize: legacy.RDMAPingPayloadSize,
			Timeout:     legacy.RDMAPingTimeout,
		}
	}
	if _, nested := raw["ssh"]; !nested {
		decoded.SSH = CheckSSHConfig{
			User:    legacy.SSHUser,
			Options: legacy.SSHOptions,
		}
	}
	*c = CheckConfig(decoded)
	return nil
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

type CheckXCCLConfig struct {
	Enabled            bool              `json:"enabled"`
	MPICHArchive       string            `json:"mpich_archive"`
	XCCLArchive        string            `json:"xccl_archive"`
	WorkRoot           string            `json:"work_root"`
	XPUHome            string            `json:"xpu_home"`
	Test               string            `json:"test"`
	MinBytes           string            `json:"min_bytes"`
	MaxBytes           string            `json:"max_bytes"`
	StepFactor         int               `json:"step_factor"`
	WarmupIterations   int               `json:"warmup_iterations"`
	Iterations         int               `json:"iterations"`
	DataType           string            `json:"data_type"`
	Timeout            int               `json:"timeout"`
	Layout             string            `json:"layout"`
	XPUOrdering        string            `json:"xpu_ordering"`
	MachineClass       string            `json:"machine_class"`
	Ranks              int               `json:"ranks"`
	SplitStep          int               `json:"split_step"`
	SplitOperation     int               `json:"split_operation"`
	EvaluationMode     string            `json:"evaluation_mode"`
	EnableXDR          *bool             `json:"enable_xdr,omitempty"`
	ValidateTopology   *bool             `json:"validate_topology,omitempty"`
	Supernode          bool              `json:"supernode"`
	SocketInterface    string            `json:"socket_interface"`
	MinBusBandwidthGBs float64           `json:"min_bus_bandwidth_gbs"`
	Environment        map[string]string `json:"environment"`
}

func (c CheckXCCLConfig) TopologyValidationEnabled() bool {
	return c.ValidateTopology == nil || *c.ValidateTopology
}

type CheckRDMAGroup struct {
	IBDevice         string            `json:"ib_device"`
	XPUOffsets       []string          `json:"xpu_offsets"`
	XPUTopologyLinks map[string]string `json:"-"`
}

type PostTask struct {
	Name    string `json:"name,omitempty"`
	Type    string `json:"type"`
	Source  string `json:"source,omitempty"`
	Target  string `json:"target,omitempty"`
	Path    string `json:"path,omitempty"`
	Command string `json:"command,omitempty"`
	Mode    string `json:"mode,omitempty"`
}

type PostPowerAction struct {
	Action  string `json:"action"`
	Confirm *bool  `json:"confirm,omitempty"`
}

type MachineRecord struct {
	HostID         string
	Hostname       string
	MgmtIP         string
	MgmtPrefix     string
	MgmtGateway    string
	MgmtBondName   string
	MgmtIface1     string
	MgmtIface2     string
	MgmtMAC1       string
	MgmtMAC2       string
	MgmtNameserver string
	RDMA           []RDMARecord
}

type RDMARecord struct {
	Name      string
	MAC       string
	IP        string
	Prefix    string
	RailID    string
	Gateway   string
	Table     string
	RouteCIDR string
}

type MachineConfig struct {
	HostID        string
	Hostname      string
	MgmtBondName  string
	MgmtIP        string
	MgmtPrefix    int
	MgmtGateway   string
	MgmtIfaces    []string
	MgmtMACs      []string
	MgmtDNS       []string
	MgmtMTU       int
	BondMode      string
	BondLACPRate  string
	BondXmitHash  string
	BondMII       int
	BondPrimary   string
	RDMAMTU       int
	RouteCIDR     string
	RoutePriority int
	RDMA          []RDMAConfig
}

type RDMAConfig struct {
	Name      string
	MAC       string
	IP        string
	Prefix    int
	Gateway   string
	Table     int
	RouteCIDR string
}

func (b *Bundle) ApplyDefaults() {
	b.Platform.ApplyDefaults()
	if b.Defaults.MgmtBondName == "" {
		b.Defaults.MgmtBondName = "bond0"
	}
	if b.Defaults.MgmtPrefix == 0 {
		b.Defaults.MgmtPrefix = 26
	}
	if b.Defaults.MgmtMTU == 0 {
		b.Defaults.MgmtMTU = 1500
	}
	if b.Defaults.BondMode == "" {
		b.Defaults.BondMode = "802.3ad"
	}
	if strings.EqualFold(b.Defaults.BondMode, "802.3ad") && b.Defaults.BondLACPRate == "" {
		b.Defaults.BondLACPRate = "slow"
	}
	if strings.EqualFold(b.Defaults.BondMode, "802.3ad") && b.Defaults.BondTransmitHashPolicy == "" {
		b.Defaults.BondTransmitHashPolicy = "layer3+4"
	}
	if strings.EqualFold(b.Defaults.BondMode, "active-backup") && b.Defaults.BondMIIMonitorInterval == 0 {
		b.Defaults.BondMIIMonitorInterval = 100
	}
	if b.Defaults.RDMAPrefix == 0 {
		b.Defaults.RDMAPrefix = 24
	}
	if b.Defaults.RDMAMTU == 0 {
		b.Defaults.RDMAMTU = 9000
	}
	if b.Defaults.RoutePriority == 0 {
		b.Defaults.RoutePriority = 32761
	}
	b.Defaults.RDMAMode = normalizeRDMAMode(b.Defaults.RDMAMode)
	if b.OfflineAPT.TargetFile == "" {
		b.OfflineAPT.TargetFile = "/etc/apt/sources.list.d/kunlun-offline.list"
	}
	if b.OfflineAPT.CopyTo == "" && strings.TrimSpace(b.OfflineAPT.MaterialPath) != "" {
		b.OfflineAPT.CopyTo = "/opt/" + strings.Trim(filepath.Base(strings.TrimSpace(b.OfflineAPT.MaterialPath)), "/")
	}
	if b.OfflineAPT.Enabled && len(b.OfflineAPT.Entries) == 0 && strings.TrimSpace(b.OfflineAPT.MaterialPath) != "" {
		b.OfflineAPT.Entries = defaultOfflineRepoEntries("apt")
	}
	if b.OfflineRepo.TargetFile == "" && (b.OfflineRepo.Enabled || strings.TrimSpace(b.OfflineRepo.MaterialPath) != "" || len(b.OfflineRepo.Entries) > 0) {
		switch b.Platform.PackageManager {
		case "yum":
			b.OfflineRepo.TargetFile = "/etc/yum.repos.d/kunlun-offline.repo"
		default:
			b.OfflineRepo.TargetFile = "/etc/apt/sources.list.d/kunlun-offline.list"
		}
	}
	if b.OfflineRepo.CopyTo == "" && strings.TrimSpace(b.OfflineRepo.MaterialPath) != "" {
		b.OfflineRepo.CopyTo = "/opt/" + strings.Trim(filepath.Base(strings.TrimSpace(b.OfflineRepo.MaterialPath)), "/")
	}
	if b.OfflineRepo.Enabled && len(b.OfflineRepo.Entries) == 0 && strings.TrimSpace(b.OfflineRepo.MaterialPath) != "" {
		b.OfflineRepo.Entries = defaultOfflineRepoEntries(b.Platform.PackageManager)
	}
	if b.Artifacts.WorkDir == "" {
		b.Artifacts.WorkDir = "/opt/kunlun"
	}
	if b.Check.Bandwidth.Duration == 0 {
		b.Check.Bandwidth.Duration = 1
	}
	if b.Check.Bandwidth.GIDIndex == 0 {
		b.Check.Bandwidth.GIDIndex = 3
	}
	if b.Check.Bandwidth.Iterations == 0 {
		b.Check.Bandwidth.Iterations = 100
	}
	if !b.Check.Bandwidth.MinGBitsSet && b.Check.Bandwidth.MinGBits == 0 {
		b.Check.Bandwidth.MinGBitsAuto = true
	}
	b.Check.Bandwidth.ReportGBits = true
	if b.Check.Bandwidth.BasePort == 0 {
		b.Check.Bandwidth.BasePort = 18515
	}
	if b.Check.RDMAPing.Count == 0 {
		b.Check.RDMAPing.Count = 3
	}
	if b.Check.RDMAPing.PayloadSize == 0 {
		b.Check.RDMAPing.PayloadSize = 8972
	}
	if b.Check.RDMAPing.Timeout == 0 {
		b.Check.RDMAPing.Timeout = 2
	}
	if b.Check.SSH.User == "" {
		b.Check.SSH.User = "root"
	}
	if b.Check.XCCL.WorkRoot == "" {
		b.Check.XCCL.WorkRoot = "/tmp/envinit-xccl-check"
	}
	if b.Check.XCCL.XPUHome == "" {
		b.Check.XCCL.XPUHome = "/usr/local/xpu"
	}
	if b.Check.XCCL.Test == "" {
		b.Check.XCCL.Test = "all_reduce"
	}
	if b.Check.XCCL.MinBytes == "" {
		b.Check.XCCL.MinBytes = "1m"
	}
	if b.Check.XCCL.MaxBytes == "" {
		b.Check.XCCL.MaxBytes = "2g"
	}
	if b.Check.XCCL.StepFactor == 0 {
		b.Check.XCCL.StepFactor = 2
	}
	if b.Check.XCCL.Iterations == 0 {
		b.Check.XCCL.Iterations = 20
	}
	if b.Check.XCCL.DataType == "" {
		b.Check.XCCL.DataType = "fp16"
	}
	if b.Check.XCCL.Timeout == 0 {
		b.Check.XCCL.Timeout = 120
	}
	if b.Check.XCCL.EnableXDR == nil {
		enabled := true
		b.Check.XCCL.EnableXDR = &enabled
	}
	if b.Check.XCCL.ValidateTopology == nil {
		enabled := true
		b.Check.XCCL.ValidateTopology = &enabled
	}
	if b.Check.XCCL.Layout == "" {
		b.Check.XCCL.Layout = "full_ring"
	}
	if b.Check.XCCL.XPUOrdering == "" {
		b.Check.XCCL.XPUOrdering = "auto"
	}
	if b.Check.XCCL.SplitStep == 0 {
		b.Check.XCCL.SplitStep = 8
	}
	if b.Check.XCCL.EvaluationMode == "" {
		b.Check.XCCL.EvaluationMode = "auto"
	}
	if b.Check.XCCL.Environment == nil {
		b.Check.XCCL.Environment = map[string]string{}
	}
	if b.PostPowerAction.Action == "" {
		confirm := true
		b.PostPowerAction = PostPowerAction{
			Action:  "soft",
			Confirm: &confirm,
		}
	}
}

func (b Bundle) Validate() error {
	if err := b.Platform.Validate(); err != nil {
		return err
	}
	switch normalizeRDMAMode(b.Defaults.RDMAMode) {
	case RDMAModeFull, RDMAModeNamesOnly, RDMAModeOff:
		return nil
	default:
		return fmt.Errorf("defaults.rdma_mode %q is not supported; use full, names_only, or off", b.Defaults.RDMAMode)
	}
}

func (p *PlatformConfig) ApplyDefaults() {
	p.OSFamily = normalizeAutoPlatformValue(p.OSFamily)
	p.PackageManager = normalizeAutoPlatformValue(p.PackageManager)
	p.NetworkBackend = strings.TrimSpace(strings.ToLower(p.NetworkBackend))
	if p.OSFamily == "" {
		p.OSFamily = inferOSFamily(p.PackageManager, p.NetworkBackend)
	}
	if p.PackageManager == "" && (p.OSFamily == "ubuntu" || p.OSFamily == "debian") {
		p.PackageManager = "apt"
	}
	if p.PackageManager == "" && isRedHatFamily(p.OSFamily) {
		p.PackageManager = "yum"
	}
	if p.NetworkBackend == "" && (p.PackageManager == "apt" || p.OSFamily == "ubuntu" || p.OSFamily == "debian") {
		p.NetworkBackend = "netplan"
	}
	if p.NetworkBackend == "" && isRedHatFamily(p.OSFamily) {
		p.NetworkBackend = "auto"
	}
	if p.KernelHeadersPackage == "" && p.PackageManager == "yum" {
		p.KernelHeadersPackage = "kernel-devel-{{uname_r}}"
	}
	if p.KernelHeadersDir == "" && p.PackageManager == "yum" {
		p.KernelHeadersDir = "/usr/src/kernels/{{uname_r}}"
	}
}

func normalizeAutoPlatformValue(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "auto" {
		return ""
	}
	return value
}

func defaultOfflineRepoEntries(packageManager string) []string {
	switch strings.TrimSpace(strings.ToLower(packageManager)) {
	case "yum":
		return []string{
			"[kunlun-offline]",
			"name=Kunlun Offline",
			"baseurl=file://{{offline_repo_target}}",
			"enabled=1",
			"gpgcheck=0",
		}
	default:
		return []string{
			"deb [trusted=yes] file:{{offline_apt_target}} ./",
		}
	}
}

func (p PlatformConfig) Validate() error {
	osFamily := strings.TrimSpace(strings.ToLower(p.OSFamily))
	packageManager := strings.TrimSpace(strings.ToLower(p.PackageManager))
	networkBackend := strings.TrimSpace(strings.ToLower(p.NetworkBackend))

	if packageManager == "apt" || osFamily == "ubuntu" || osFamily == "debian" {
		switch networkBackend {
		case "", "netplan":
			return nil
		default:
			return fmt.Errorf("platform.network_backend %q is not supported with os_family=%q package_manager=%q; use netplan or leave it empty for Ubuntu/Debian systems", networkBackend, osFamily, packageManager)
		}
	}

	if packageManager == "yum" || isRedHatFamily(osFamily) {
		switch networkBackend {
		case "", "auto", "ifcfg", "network", "network-scripts", "networkmanager", "network-manager", "nm":
			return nil
		default:
			return fmt.Errorf("platform.network_backend %q is not supported with os_family=%q package_manager=%q; use auto, network, or networkmanager for RedHat-family systems", networkBackend, osFamily, packageManager)
		}
	}

	if networkBackend != "" && networkBackend != "netplan" {
		return fmt.Errorf("platform.network_backend %q requires a matching platform; use netplan for apt/Ubuntu or auto/network/networkmanager for yum/RedHat-family systems", networkBackend)
	}
	return nil
}

func inferOSFamily(packageManager string, networkBackend string) string {
	switch strings.TrimSpace(strings.ToLower(packageManager)) {
	case "apt":
		return "ubuntu"
	case "yum":
		return "redhat"
	}
	switch strings.TrimSpace(strings.ToLower(networkBackend)) {
	case "auto", "network", "network-scripts", "networkmanager", "network-manager", "nm", "ifcfg":
		return "redhat"
	}
	return ""
}

func isRedHatFamily(osFamily string) bool {
	switch strings.TrimSpace(strings.ToLower(osFamily)) {
	case "redhat", "rhel", "kylin", "rocky", "almalinux", "anolis":
		return true
	default:
		return false
	}
}

func (b Bundle) RDMAExists() bool {
	return normalizeRDMAMode(b.Defaults.RDMAMode) != RDMAModeOff
}

func (b Bundle) RDMAConfigureIPRoute() bool {
	return normalizeRDMAMode(b.Defaults.RDMAMode) == RDMAModeFull
}

func normalizeRDMAMode(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return RDMAModeFull
	}
	return value
}

func (b Bundle) ConfigureManagementNetwork() bool {
	return firstConfiguredBool(true, b.Defaults.ConfigureManagementNetwork)
}

func (b Bundle) ApplyNetworkImmediately() bool {
	return firstConfiguredBool(true, b.Defaults.ApplyNetworkImmediately)
}

func (b Bundle) BackupExistingNetplan() bool {
	return firstConfiguredBool(b.Defaults.BackupExistingNetplan, b.PlatformOptions.Ubuntu.BackupExistingNetplan)
}

func (b Bundle) DisableExistingAptSources() bool {
	return firstConfiguredBool(b.Defaults.DisableExistingAptSources, b.PlatformOptions.Ubuntu.DisableExistingAptSources)
}

func (b Bundle) BackupExistingNetwork() bool {
	return firstConfiguredBool(b.Defaults.BackupExistingNetwork, b.yumPlatformOptions().BackupExistingNetwork)
}

func (b Bundle) DisableExistingRepos() bool {
	return firstConfiguredBool(b.Defaults.DisableExistingRepos || b.Defaults.DisableExistingAptSources, b.yumPlatformOptions().DisableExistingRepos)
}

func (b Bundle) yumPlatformOptions() YumPlatformOptions {
	switch strings.TrimSpace(strings.ToLower(b.Platform.OSFamily)) {
	case "kylin":
		return mergeYumPlatformOptions(b.PlatformOptions.RedHat, b.PlatformOptions.Kylin)
	default:
		return b.PlatformOptions.RedHat
	}
}

func mergeYumPlatformOptions(fallback YumPlatformOptions, override YumPlatformOptions) YumPlatformOptions {
	if override.BackupExistingNetwork == nil {
		override.BackupExistingNetwork = fallback.BackupExistingNetwork
	}
	if override.DisableExistingRepos == nil {
		override.DisableExistingRepos = fallback.DisableExistingRepos
	}
	return override
}

func firstConfiguredBool(defaultValue bool, values ...*bool) bool {
	for _, value := range values {
		if value != nil {
			return *value
		}
	}
	return defaultValue
}

func ResolveMachine(bundle Bundle, record MachineRecord, ifaceByMAC map[string]string) (MachineConfig, error) {
	configureMgmt := bundle.ConfigureManagementNetwork() && strings.TrimSpace(record.MgmtIP) != ""

	mgmtPrefix := bundle.Defaults.MgmtPrefix
	if configureMgmt && record.MgmtPrefix != "" {
		p, err := strconv.Atoi(strings.TrimSpace(record.MgmtPrefix))
		if err != nil {
			return MachineConfig{}, fmt.Errorf("invalid mgmt_prefix %q: %w", record.MgmtPrefix, err)
		}
		mgmtPrefix = p
	}

	mgmtIfaces := make([]string, 0, 2)
	mgmtMACs := make([]string, 0, 2)
	if configureMgmt {
		resolveMgmtCount := len(bundle.Defaults.MgmtInterfaces)
		explicitMgmt1 := hasExplicitMgmtSlot(record, 1)
		explicitMgmt2 := hasExplicitMgmtSlot(record, 2)
		if explicitMgmt2 && resolveMgmtCount < 2 {
			resolveMgmtCount = 2
		}
		if explicitMgmt1 && !explicitMgmt2 {
			resolveMgmtCount = 1
		}
		for idx := range resolveMgmtCount {
			var configuredName, configuredMAC, defaultName string
			switch idx {
			case 0:
				configuredName = record.MgmtIface1
				configuredMAC = record.MgmtMAC1
			case 1:
				configuredName = record.MgmtIface2
				configuredMAC = record.MgmtMAC2
			}
			if idx < len(bundle.Defaults.MgmtInterfaces) {
				defaultName = bundle.Defaults.MgmtInterfaces[idx]
			}
			name, mac, err := resolveInterfaceName(configuredName, configuredMAC, defaultName, ifaceByMAC, fmt.Sprintf("mgmt%d", idx+1))
			if err != nil {
				return MachineConfig{}, err
			}
			mgmtIfaces = append(mgmtIfaces, name)
			mgmtMACs = append(mgmtMACs, mac)
		}
		if len(mgmtIfaces) == 0 && (explicitMgmt1 || explicitMgmt2 || len(bundle.Defaults.MgmtInterfaces) > 0) {
			return MachineConfig{}, errors.New("need at least 1 management interface")
		}
	}

	mgmtGW := ""
	if configureMgmt {
		mgmtGW = strings.TrimSpace(record.MgmtGateway)
		if mgmtGW == "" {
			mgmtGW = strings.TrimSpace(bundle.Defaults.MgmtGateway)
		}
		if mgmtGW == "" {
			mgmtGW = deriveGateway(record.MgmtIP)
		}
	}

	var dns []string
	if configureMgmt {
		dns = parseList(record.MgmtNameserver)
		if len(dns) == 0 {
			dns = append(dns, bundle.Defaults.MgmtNameservers...)
		}
	}

	rdmaCount := len(record.RDMA)
	if rdmaCount == 0 {
		rdmaCount = len(bundle.Defaults.RDMAInterfaces)
	}
	rdma := make([]RDMAConfig, 0, rdmaCount)
	if bundle.RDMAExists() {
		for idx := 0; idx < rdmaCount; idx++ {
			def := RDMAInterfaceDefault{
				Table: 101 + idx,
			}
			if idx < len(bundle.Defaults.RDMAInterfaces) {
				def = bundle.Defaults.RDMAInterfaces[idx]
			}
			item := RDMAConfig{
				Name:      def.Name,
				Prefix:    bundle.Defaults.RDMAPrefix,
				Gateway:   strings.TrimSpace(def.Gateway),
				Table:     def.Table,
				RouteCIDR: strings.TrimSpace(bundle.Defaults.RDMARouteCIDR),
			}
			if idx < len(record.RDMA) {
				row := record.RDMA[idx]
				item.MAC = strings.TrimSpace(row.MAC)
				if strings.TrimSpace(row.Name) != "" || strings.TrimSpace(row.MAC) != "" || strings.TrimSpace(item.Name) != "" {
					name, mac, err := resolveInterfaceName(row.Name, row.MAC, item.Name, ifaceByMAC, fmt.Sprintf("rdma%d", idx+1))
					if err != nil {
						return MachineConfig{}, err
					}
					item.Name = firstNonEmpty(row.Name, item.Name, name)
					item.MAC = mac
				}
				item.IP = strings.TrimSpace(row.IP)
				if row.Prefix != "" {
					p, err := strconv.Atoi(strings.TrimSpace(row.Prefix))
					if err != nil {
						return MachineConfig{}, fmt.Errorf("invalid rdma%d_prefix %q: %w", idx+1, row.Prefix, err)
					}
					item.Prefix = p
				}
				if strings.TrimSpace(row.Gateway) != "" {
					item.Gateway = strings.TrimSpace(row.Gateway)
				}
				if row.Table != "" {
					t, err := strconv.Atoi(strings.TrimSpace(row.Table))
					if err != nil {
						return MachineConfig{}, fmt.Errorf("invalid rdma%d_table %q: %w", idx+1, row.Table, err)
					}
					item.Table = t
				}
				if strings.TrimSpace(row.RouteCIDR) != "" {
					item.RouteCIDR = strings.TrimSpace(row.RouteCIDR)
				}
			}
			if item.Name == "" {
				item.Name = fmt.Sprintf("rdma%d", idx+1)
			}
			if bundle.RDMAConfigureIPRoute() && item.IP == "" {
				return MachineConfig{}, fmt.Errorf("inventory row missing rdma%d_ip", idx+1)
			}
			if item.Gateway == "" && item.IP != "" {
				item.Gateway = deriveGateway(item.IP)
			}
			rdma = append(rdma, item)
		}
	}

	bondName := strings.TrimSpace(record.MgmtBondName)
	if bondName == "" {
		bondName = bundle.Defaults.MgmtBondName
	}
	bondPrimary := strings.TrimSpace(bundle.Defaults.BondPrimary)
	if strings.EqualFold(bundle.Defaults.BondMode, "active-backup") && bondPrimary != "" && len(mgmtIfaces) > 0 && !stringInSlice(bondPrimary, mgmtIfaces) {
		return MachineConfig{}, fmt.Errorf("bond_primary %q is not one of management interfaces: %s", bondPrimary, strings.Join(mgmtIfaces, ","))
	}

	cfg := MachineConfig{
		HostID:        firstNonEmpty(record.HostID, record.Hostname, record.MgmtIP),
		Hostname:      firstNonEmpty(record.Hostname, record.HostID),
		MgmtBondName:  bondName,
		MgmtIP:        strings.TrimSpace(record.MgmtIP),
		MgmtPrefix:    mgmtPrefix,
		MgmtGateway:   mgmtGW,
		MgmtIfaces:    mgmtIfaces,
		MgmtMACs:      mgmtMACs,
		MgmtDNS:       dns,
		MgmtMTU:       bundle.Defaults.MgmtMTU,
		BondMode:      bundle.Defaults.BondMode,
		BondLACPRate:  bundle.Defaults.BondLACPRate,
		BondXmitHash:  bundle.Defaults.BondTransmitHashPolicy,
		BondMII:       bundle.Defaults.BondMIIMonitorInterval,
		BondPrimary:   bondPrimary,
		RDMAMTU:       bundle.Defaults.RDMAMTU,
		RouteCIDR:     bundle.Defaults.RDMARouteCIDR,
		RoutePriority: bundle.Defaults.RoutePriority,
		RDMA:          rdma,
	}

	if configureMgmt {
		if err := validateIPv4(cfg.MgmtIP); err != nil {
			return MachineConfig{}, fmt.Errorf("invalid mgmt_ip: %w", err)
		}
		if err := validateIPv4(cfg.MgmtGateway); err != nil {
			return MachineConfig{}, fmt.Errorf("invalid mgmt_gateway: %w", err)
		}
		for _, ip := range cfg.MgmtDNS {
			if err := validateIPv4(ip); err != nil {
				return MachineConfig{}, fmt.Errorf("invalid nameserver %q: %w", ip, err)
			}
		}
	}
	for _, item := range cfg.RDMA {
		if item.IP != "" {
			if err := validateIPv4(item.IP); err != nil {
				return MachineConfig{}, fmt.Errorf("invalid %s IP: %w", item.Name, err)
			}
		}
		if item.Gateway != "" {
			if err := validateIPv4(item.Gateway); err != nil {
				return MachineConfig{}, fmt.Errorf("invalid %s gateway: %w", item.Name, err)
			}
		}
		if item.RouteCIDR != "" && !strings.EqualFold(item.RouteCIDR, "auto") {
			if err := validateIPv4CIDR(item.RouteCIDR); err != nil {
				return MachineConfig{}, fmt.Errorf("invalid %s route CIDR: %w", item.Name, err)
			}
		}
	}
	if err := validateUniqueStrings(cfg.MgmtIfaces, "management interface"); err != nil {
		return MachineConfig{}, err
	}
	if err := validateUniqueStrings(cfg.MgmtMACs, "management MAC"); err != nil {
		return MachineConfig{}, err
	}
	rdmaNames := make([]string, 0, len(cfg.RDMA))
	rdmaMACs := make([]string, 0, len(cfg.RDMA))
	for _, item := range cfg.RDMA {
		rdmaNames = append(rdmaNames, item.Name)
		rdmaMACs = append(rdmaMACs, item.MAC)
	}
	if err := validateUniqueStrings(rdmaNames, "RDMA interface"); err != nil {
		return MachineConfig{}, err
	}
	if err := validateUniqueStrings(rdmaMACs, "RDMA MAC"); err != nil {
		return MachineConfig{}, err
	}

	return cfg, nil
}

func hasExplicitMgmtSlot(record MachineRecord, slot int) bool {
	switch slot {
	case 1:
		return strings.TrimSpace(record.MgmtIface1) != "" || strings.TrimSpace(record.MgmtMAC1) != ""
	case 2:
		return strings.TrimSpace(record.MgmtIface2) != "" || strings.TrimSpace(record.MgmtMAC2) != ""
	default:
		return false
	}
}

func resolveInterfaceName(configuredName string, configuredMAC string, defaultName string, ifaceByMAC map[string]string, label string) (string, string, error) {
	name := strings.TrimSpace(configuredName)
	mac, err := NormalizeMAC(configuredMAC)
	if err != nil {
		return "", "", fmt.Errorf("invalid %s mac %q: %w", label, configuredMAC, err)
	}
	if mac != "" {
		resolved := strings.TrimSpace(ifaceByMAC[mac])
		if resolved == "" {
			if len(ifaceByMAC) == 0 {
				if name != "" {
					return name, mac, nil
				}
				if defaultName != "" {
					return strings.TrimSpace(defaultName), mac, nil
				}
			}
			return "", "", fmt.Errorf("%s mac %s not found on current machine", label, mac)
		}
		return resolved, mac, nil
	}
	if name != "" {
		return name, "", nil
	}
	if defaultName != "" {
		return strings.TrimSpace(defaultName), "", nil
	}
	return "", "", fmt.Errorf("%s has neither interface name nor mac", label)
}

func deriveGateway(ip string) string {
	parts := strings.Split(strings.TrimSpace(ip), ".")
	if len(parts) != 4 {
		return ""
	}
	return fmt.Sprintf("%s.%s.%s.1", parts[0], parts[1], parts[2])
}

func validateIPv4(ip string) error {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil || parsed.To4() == nil {
		return fmt.Errorf("%q is not a valid IPv4 address", ip)
	}
	return nil
}

func validateIPv4CIDR(cidr string) error {
	parsedIP, parsedCIDR, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil || parsedIP == nil || parsedIP.To4() == nil || parsedCIDR == nil {
		return fmt.Errorf("%q is not a valid IPv4 CIDR", cidr)
	}
	return nil
}

func NormalizeMAC(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	hw, err := net.ParseMAC(raw)
	if err != nil {
		return "", err
	}
	return strings.ToLower(hw.String()), nil
}

func validateUniqueStrings(values []string, kind string) error {
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if seen[value] {
			return fmt.Errorf("duplicate %s: %s", kind, value)
		}
		seen[value] = true
	}
	return nil
}

func parseList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '|' || r == ' '
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		out = append(out, field)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func maxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

func stringInSlice(needle string, haystack []string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}
