package spec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"
)

type Bundle struct {
	Warnings        []string          `json:"-"`
	StageChecks     map[string]string `json:"stage_checks,omitempty"`
	ApplyTimeouts   ApplyTimeouts     `json:"apply_timeouts"`
	Defaults        Defaults          `json:"defaults"`
	Platform        PlatformConfig    `json:"platform"`
	PlatformOptions PlatformOptions   `json:"platform_options"`
	OfflineAPT      OfflineAPTConfig  `json:"offline_apt"`
	OfflineRepo     OfflineAPTConfig  `json:"offline_repo"`
	Packages        []string          `json:"packages"`
	Artifacts       Artifacts         `json:"artifacts"`
	XRE             XREConfig         `json:"xre"`
	MlxConfig       MlxConfig         `json:"mlxconfig"`
	Check           CheckConfig       `json:"check"`
	PostPackages    []string          `json:"post_packages"`
	PostTasks       []PostTask        `json:"post_tasks"`
	PostPowerAction PostPowerAction   `json:"post_power_action"`
}

// ApplyTimeouts bounds how long a single command inside each apply stage may run
// before envinit terminates it. Values are in seconds; 0 means no timeout, which
// is the default for long vendor operations (ofed/xre/xdr/firmware) and the
// user-controlled post stage. DefaultSeconds applies to any stage not listed in
// Stages.
type ApplyTimeouts struct {
	DefaultSeconds int            `json:"default_seconds"`
	Stages         map[string]int `json:"stages,omitempty"`
}

// applyTimeoutStages enumerates every stage that carries an explicit default so
// ApplyDefaults can prefill a complete, self-documenting table.
var applyTimeoutStageDefaults = map[string]int{
	"software":  360,
	"ofed":      0,
	"network":   360,
	"xre":       0,
	"xdr":       0,
	"firmware":  0,
	"container": 360,
	"mlxconfig": 360,
	"sysctl":    360,
	"kernel":    360,
	"post":      0,
	"udev":      360,
}

const defaultApplyStageTimeoutSeconds = 360

// applyDefaults prefills a complete stage table so a delivered bundle documents
// every adjustable timeout. Explicitly provided values are preserved.
func (t *ApplyTimeouts) applyDefaults() {
	if t.DefaultSeconds == 0 {
		t.DefaultSeconds = defaultApplyStageTimeoutSeconds
	}
	if t.Stages == nil {
		t.Stages = map[string]int{}
	}
	for stage, seconds := range applyTimeoutStageDefaults {
		if _, ok := t.Stages[stage]; !ok {
			t.Stages[stage] = seconds
		}
	}
}

// StageTimeout returns the configured per-command timeout for a stage. A zero
// duration means no timeout. Unknown stages fall back to DefaultSeconds.
func (t ApplyTimeouts) StageTimeout(stage string) time.Duration {
	if seconds, ok := t.Stages[stage]; ok {
		return time.Duration(seconds) * time.Second
	}
	return time.Duration(t.DefaultSeconds) * time.Second
}

func (t ApplyTimeouts) validate() error {
	if t.DefaultSeconds < 0 {
		return fmt.Errorf("apply_timeouts.default_seconds must not be negative")
	}
	for stage, seconds := range t.Stages {
		if _, ok := applyTimeoutStageDefaults[stage]; !ok {
			return fmt.Errorf("apply_timeouts.stages contains unknown stage %q", stage)
		}
		if seconds < 0 {
			return fmt.Errorf("apply_timeouts.stages.%s must not be negative", stage)
		}
	}
	return nil
}

type Defaults struct {
	RDMAGatewayProbe           RDMAGatewayProbeConfig `json:"rdma_gateway_probe"`
	BackupRoot                 string                 `json:"backup_root"`
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

type RDMAGatewayProbeConfig struct {
	Enabled         *bool `json:"enabled,omitempty"`
	Attempts        int   `json:"attempts,omitempty"`
	TimeoutSeconds  int   `json:"timeout_seconds,omitempty"`
	LinkWaitSeconds int   `json:"link_wait_seconds,omitempty"`
}

// IsEnabled defaults to true because gateway probing provides useful
// connectivity and conflict evidence. It remains supplemental evidence and
// cannot establish a physical port mapping when several NICs share the same
// subnet and gateway; set enabled to false to disable it.
func (p RDMAGatewayProbeConfig) IsEnabled() bool { return p.Enabled == nil || *p.Enabled }
func (p RDMAGatewayProbeConfig) Effective() RDMAGatewayProbeConfig {
	if p.Attempts == 0 {
		p.Attempts = 3
	}
	if p.TimeoutSeconds == 0 {
		p.TimeoutSeconds = 3
	}
	if p.LinkWaitSeconds == 0 {
		p.LinkWaitSeconds = 5
	}
	return p
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
	GIDIndexAuto  bool             `json:"-"`
	GIDIndexSet   bool             `json:"-"`
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
	gidAuto := false
	if gid, ok := raw["gid_index"]; ok && strings.TrimSpace(string(gid)) == "null" {
		return errors.New("check.bandwidth.gid_index must be \"auto\" or a non-negative integer")
	}
	if gid, ok := raw["gid_index"]; ok && len(gid) > 0 && gid[0] == '"' {
		var mode string
		if err := json.Unmarshal(gid, &mode); err != nil || !strings.EqualFold(strings.TrimSpace(mode), "auto") {
			return fmt.Errorf("check.bandwidth.gid_index must be \"auto\" or a non-negative integer")
		}
		gidAuto = true
		raw["gid_index"] = json.RawMessage("0")
	}
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
	if decoded.GIDIndex < 0 {
		return errors.New("check.bandwidth.gid_index must be \"auto\" or a non-negative integer")
	}
	*c = CheckBandwidthConfig(decoded)
	c.GIDIndexAuto = gidAuto
	_, c.GIDIndexSet = raw["gid_index"]
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
	if c.GIDIndexAuto {
		fields["gid_index"] = json.RawMessage(`"auto"`)
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
	Count       int  `json:"count"`
	PayloadSize int  `json:"payload_size"`
	PayloadAuto bool `json:"-"`
	Timeout     int  `json:"timeout"`
}

func (c *CheckRDMAPingConfig) UnmarshalJSON(data []byte) error {
	var raw struct {
		Count       int             `json:"count"`
		PayloadSize json.RawMessage `json:"payload_size"`
		Timeout     int             `json:"timeout"`
	}
	if err := decodeStrictJSON(data, &raw); err != nil {
		return err
	}
	*c = CheckRDMAPingConfig{}
	c.Count, c.Timeout = raw.Count, raw.Timeout
	if len(raw.PayloadSize) == 0 {
		return nil
	}
	if string(raw.PayloadSize) == "null" {
		return errors.New("check.rdma_ping.payload_size must be \"auto\" or a positive integer")
	}
	if raw.PayloadSize[0] == '"' {
		var mode string
		if err := json.Unmarshal(raw.PayloadSize, &mode); err != nil || !strings.EqualFold(strings.TrimSpace(mode), "auto") {
			return errors.New("check.rdma_ping.payload_size must be \"auto\" or a positive integer")
		}
		c.PayloadAuto = true
		return nil
	}
	if err := json.Unmarshal(raw.PayloadSize, &c.PayloadSize); err != nil || c.PayloadSize <= 0 {
		return errors.New("check.rdma_ping.payload_size must be \"auto\" or a positive integer")
	}
	return nil
}

func (c CheckRDMAPingConfig) MarshalJSON() ([]byte, error) {
	value := any(c.PayloadSize)
	if c.PayloadAuto {
		value = "auto"
	}
	return json.Marshal(struct {
		Count       int `json:"count"`
		PayloadSize any `json:"payload_size"`
		Timeout     int `json:"timeout"`
	}{c.Count, value, c.Timeout})
}

type CheckSSHConfig struct {
	User    string   `json:"user"`
	Port    int      `json:"port,omitempty"`
	Options []string `json:"options"`
}

// validate rejects the classic ssh/scp port-flag mix-up early, because the
// same options are passed to both ssh (lowercase -p) and scp (uppercase -P),
// so a bare port flag cannot work for both at once.
func (c CheckSSHConfig) validate() error {
	if c.Port < 0 || c.Port > 65535 {
		return fmt.Errorf("check.ssh.port must be between 1 and 65535, got %d", c.Port)
	}
	for _, option := range c.Options {
		if isBarePortFlag(option) {
			return fmt.Errorf("check.ssh.options entry %q is not supported: the port flag differs between ssh (-p) and scp (-P) and these options are passed to both; set check.ssh.port or use \"-o Port=<port>\" instead", option)
		}
	}
	return nil
}

// isBarePortFlag reports whether an ssh options token is a bare -p/-P port
// flag rather than an option both ssh and scp understand.
func isBarePortFlag(option string) bool {
	option = strings.TrimSpace(option)
	if len(option) < 2 || option[0] != '-' || option[1] == '-' {
		return false
	}
	return option[1] == 'p' || option[1] == 'P'
}

func (c *CheckConfig) UnmarshalJSON(data []byte) error {
	var decoded CheckConfig
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
		"ssh_user": true, "ssh_options": true, "ssh_port": true,
	}
	for key := range raw {
		if !allowed[key] {
			return fmt.Errorf("json: unknown field %q", key)
		}
	}
	for _, group := range []struct {
		name, legacyPrefix string
		fields             []string
		target             any
	}{
		{"bandwidth", "", []string{"duration", "run_by_duration", "gid_index", "iterations", "bandwidth_qps", "message_size", "report_gbits", "mmap_device", "min_gbits", "parallel", "base_port", "rdma_groups"}, &decoded.Bandwidth},
		{"rdma_ping", "rdma_ping_", []string{"count", "payload_size", "timeout"}, &decoded.RDMAPing},
		{"ssh", "ssh_", []string{"user", "port", "options"}, &decoded.SSH},
		{"xccl", "", nil, &decoded.XCCL},
	} {
		values := map[string]json.RawMessage{}
		if nested, ok := raw[group.name]; ok {
			if group.fields == nil {
				if err := decodeStrictJSON(nested, group.target); err != nil {
					return err
				}
				continue
			}
			if err := json.Unmarshal(nested, &values); err != nil {
				return err
			}
			if values == nil {
				values = map[string]json.RawMessage{}
			}
		}
		for _, field := range group.fields {
			old := group.legacyPrefix + field
			if value, ok := raw[old]; ok {
				if current, exists := values[field]; exists && !equalJSON(current, value) {
					return fmt.Errorf("conflicting check.%s.%s and check.%s", group.name, field, old)
				}
				values[field] = value
			}
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			return err
		}
		if err := decodeStrictJSON(encoded, group.target); err != nil {
			return err
		}
	}
	*c = decoded
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
	MPIInterface       string            `json:"mpi_interface"`
	MinBusBandwidthGBs float64           `json:"min_bus_bandwidth_gbs"`
	Environment        map[string]string `json:"environment"`
}

func (c CheckXCCLConfig) TopologyValidationEnabled() bool {
	return c.ValidateTopology == nil || *c.ValidateTopology
}

type CheckRDMAGroup struct {
	IBDevice         string            `json:"ib_device"`
	IBPort           int               `json:"ib_port,omitempty"`
	GIDIndex         *int              `json:"-"`
	XPUOffsets       []string          `json:"xpu_offsets"`
	XPUTopologyLinks map[string]string `json:"-"`
}

type PostTask struct {
	CheckCommand string `json:"check_command,omitempty"`
	Name         string `json:"name,omitempty"`
	Type         string `json:"type"`
	Source       string `json:"source,omitempty"`
	Target       string `json:"target,omitempty"`
	Path         string `json:"path,omitempty"`
	Command      string `json:"command,omitempty"`
	Mode         string `json:"mode,omitempty"`
}

type PostPowerAction struct {
	Action  string `json:"action"`
	Confirm *bool  `json:"confirm,omitempty"`
}

type MachineRecord struct {
	HostID         string
	Hostname       string
	LegacySchema   bool
	FromInventory  bool
	MgmtMode       string
	MgmtTargetName string
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
	HostID         string
	Hostname       string
	MgmtMode       string
	MgmtTargetName string
	MgmtBondName   string
	MgmtIP         string
	MgmtPrefix     int
	MgmtGateway    string
	MgmtIfaces     []string
	MgmtMACs       []string
	MgmtDNS        []string
	MgmtMTU        int
	BondMode       string
	BondLACPRate   string
	BondXmitHash   string
	BondMII        int
	BondPrimary    string
	RDMAMTU        int
	RouteCIDR      string
	RoutePriority  int
	RDMA           []RDMAConfig
}

const (
	MgmtModeSingle = "single"
	MgmtModeBond   = "bond"
	MgmtModeAuto   = "auto"
)

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
	b.ApplyTimeouts.applyDefaults()
	legacyOffline := reflect.DeepEqual(b.OfflineRepo, OfflineAPTConfig{}) && !reflect.DeepEqual(b.OfflineAPT, OfflineAPTConfig{})
	if legacyOffline {
		b.OfflineRepo = b.OfflineAPT
	}
	if strings.TrimSpace(b.Defaults.BackupRoot) == "" {
		b.Defaults.BackupRoot = "/var/lib/envinit/backups"
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
	if b.Defaults.RDMAMTU == 0 {
		b.Defaults.RDMAMTU = 9000
	}
	if b.Defaults.RoutePriority == 0 {
		b.Defaults.RoutePriority = 32761
	}
	b.Defaults.RDMAMode = normalizeRDMAMode(b.Defaults.RDMAMode)
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
	if legacyOffline {
		// Directly constructed legacy bundles retain their in-memory alias;
		// JSON decoding and serialization use only OfflineRepo.
		b.OfflineAPT = b.OfflineRepo
	}
	if b.Artifacts.WorkDir == "" {
		b.Artifacts.WorkDir = "/opt/kunlun"
	}
	if b.Check.Bandwidth.Duration == 0 {
		b.Check.Bandwidth.Duration = 1
	}
	if b.Check.Bandwidth.GIDIndex == 0 && !b.Check.Bandwidth.GIDIndexSet {
		b.Check.Bandwidth.GIDIndexAuto = true
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
	if b.Check.RDMAPing.PayloadSize == 0 && !b.Check.RDMAPing.PayloadAuto {
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
	p := b.Defaults.RDMAGatewayProbe.Effective()
	if p.Attempts < 1 || p.Attempts > 10 || p.TimeoutSeconds < 1 || p.TimeoutSeconds > 30 || p.LinkWaitSeconds < 1 || p.LinkWaitSeconds > 60 {
		return fmt.Errorf("invalid defaults.rdma_gateway_probe limits: attempts 1..10, timeout_seconds 1..30, link_wait_seconds 1..60")
	}
	if err := b.ApplyTimeouts.validate(); err != nil {
		return err
	}
	if err := b.Check.SSH.validate(); err != nil {
		return err
	}
	if err := b.Platform.Validate(); err != nil {
		return err
	}
	if backupRoot := strings.TrimSpace(b.Defaults.BackupRoot); backupRoot == "" || !filepath.IsAbs(backupRoot) {
		return fmt.Errorf("defaults.backup_root %q must be an absolute path", b.Defaults.BackupRoot)
	} else if filepath.Clean(backupRoot) == string(filepath.Separator) {
		return fmt.Errorf("defaults.backup_root must not be the filesystem root")
	} else {
		for _, activeConfigDir := range []string{
			"/etc/netplan",
			"/etc/sysconfig/network-scripts",
			"/etc/apt/sources.list.d",
			"/etc/yum.repos.d",
			"/etc/systemd/system",
			"/etc/udev/rules.d",
			"/etc/modprobe.d",
			"/etc/sysctl.d",
			"/etc/networkd-dispatcher",
			"/etc/NetworkManager/dispatcher.d",
			"/usr/local/sbin",
		} {
			if pathsOverlap(backupRoot, activeConfigDir) {
				return fmt.Errorf("defaults.backup_root %q must not overlap active configuration directory %s", b.Defaults.BackupRoot, activeConfigDir)
			}
		}
	}
	allowedChecks := map[string]bool{"ofed": true, "xre": true, "xdr": true, "firmware": true}
	for key, command := range b.StageChecks {
		normalized := strings.TrimSpace(strings.ToLower(key))
		if !allowedChecks[normalized] || normalized != key {
			return fmt.Errorf("stage_checks key %q is unsupported; use ofed, xre, xdr, or firmware", key)
		}
		if strings.TrimSpace(command) == "" {
			return fmt.Errorf("stage_checks.%s must not be empty", key)
		}
	}
	switch normalizeRDMAMode(b.Defaults.RDMAMode) {
	case RDMAModeFull, RDMAModeNamesOnly, RDMAModeOff:
		return nil
	default:
		return fmt.Errorf("defaults.rdma_mode %q is not supported; use full, names_only, or off", b.Defaults.RDMAMode)
	}
}

func pathsOverlap(left, right string) bool {
	contains := func(parent, child string) bool {
		rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
		return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
	}
	return contains(left, right) || contains(right, left)
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
			"deb [trusted=yes] file:{{offline_apt_target}} jammy main",
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
	legacyRecord := record.LegacySchema || (!record.FromInventory && strings.TrimSpace(record.HostID) != "")
	if strings.TrimSpace(record.Hostname) == "" && strings.TrimSpace(record.HostID) != "" {
		record.Hostname = strings.TrimSpace(record.HostID)
	}
	if strings.TrimSpace(record.Hostname) == "" {
		return MachineConfig{}, errors.New("hostname is required; fill hostname in inventory")
	}
	if record.MgmtBondName != "" {
		if record.MgmtTargetName != "" && record.MgmtTargetName != record.MgmtBondName {
			return MachineConfig{}, errors.New("conflicting mgmt_target_name and deprecated mgmt_bond_name")
		}
		record.MgmtTargetName = record.MgmtBondName
	}
	mgmtMode := strings.ToLower(strings.TrimSpace(record.MgmtMode))
	switch mgmtMode {
	case "":
		mgmtMode = MgmtModeAuto
	case MgmtModeSingle, MgmtModeBond, MgmtModeAuto:
	default:
		return MachineConfig{}, fmt.Errorf("invalid mgmt_mode %q: use single, bond, or auto", record.MgmtMode)
	}
	if mgmtMode == MgmtModeAuto {
		slots := 0
		if hasExplicitMgmtSlot(record, 1) {
			slots++
		}
		if hasExplicitMgmtSlot(record, 2) {
			slots++
		}
		switch slots {
		case 1:
			mgmtMode = MgmtModeSingle
		case 2:
			mgmtMode = MgmtModeBond
		}
	}
	if mgmtMode == MgmtModeSingle {
		if hasExplicitMgmtSlot(record, 1) && hasExplicitMgmtSlot(record, 2) {
			return MachineConfig{}, errors.New("mgmt_mode single must specify only one management slot")
		}
		if hasExplicitMgmtSlot(record, 2) {
			record.MgmtIface1, record.MgmtMAC1 = record.MgmtIface2, record.MgmtMAC2
			record.MgmtIface2, record.MgmtMAC2 = "", ""
		}
		if record.MgmtIface1 == "" && record.MgmtMAC1 != "" {
			record.MgmtIface1 = record.MgmtTargetName
		}
	}
	if !legacyRecord && bundle.ConfigureManagementNetwork() && strings.TrimSpace(record.MgmtIP) == "" {
		return MachineConfig{}, errors.New("mgmt_ip is required when mgmt_mode is specified and management configuration is enabled")
	}
	configureMgmt := bundle.ConfigureManagementNetwork() && strings.TrimSpace(record.MgmtIP) != ""

	mgmtPrefix := 0
	if configureMgmt && record.MgmtPrefix != "" {
		p, err := strconv.Atoi(strings.TrimSpace(record.MgmtPrefix))
		if err != nil {
			return MachineConfig{}, fmt.Errorf("invalid mgmt_prefix %q: %w", record.MgmtPrefix, err)
		}
		mgmtPrefix = p
	}
	if configureMgmt && (mgmtPrefix < 1 || mgmtPrefix > 32) {
		if legacyRecord {
			mgmtPrefix = firstNonZero(bundle.Defaults.MgmtPrefix, 26)
		} else {
			return MachineConfig{}, errors.New("mgmt_prefix (1..32) is required in inventory when management network configuration is enabled")
		}
	}

	mgmtIfaces := make([]string, 0, 2)
	mgmtMACs := make([]string, 0, 2)
	if configureMgmt {
		resolveMgmtCount := 0
		explicitMgmt1 := hasExplicitMgmtSlot(record, 1)
		explicitMgmt2 := hasExplicitMgmtSlot(record, 2)
		if explicitMgmt2 && resolveMgmtCount < 2 {
			resolveMgmtCount = 2
		}
		if explicitMgmt1 && !explicitMgmt2 {
			resolveMgmtCount = 1
		}
		switch mgmtMode {
		case MgmtModeSingle:
			resolveMgmtCount = 1
		case MgmtModeBond:
			resolveMgmtCount = 2
		}
		if legacyRecord && resolveMgmtCount == 0 {
			resolveMgmtCount = len(bundle.Defaults.MgmtInterfaces)
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
			if legacyRecord && idx < len(bundle.Defaults.MgmtInterfaces) {
				defaultName = bundle.Defaults.MgmtInterfaces[idx]
			}
			// An explicit mode defines the slot count even before the target
			// hardware is available. Empty slots are resolved by the runner;
			// they are not invented interface names or one-member bonds.
			if mgmtMode != "" && (ifaceByMAC == nil || (strings.TrimSpace(configuredName) == "" && strings.TrimSpace(configuredMAC) == "" && strings.TrimSpace(defaultName) == "")) {
				mac, err := NormalizeMAC(configuredMAC)
				if err != nil {
					return MachineConfig{}, fmt.Errorf("invalid mgmt%d mac %q: %w", idx+1, configuredMAC, err)
				}
				mgmtIfaces = append(mgmtIfaces, firstNonEmpty(configuredName, defaultName))
				mgmtMACs = append(mgmtMACs, mac)
				continue
			}
			name, mac, err := resolveInterfaceName(configuredName, configuredMAC, defaultName, ifaceByMAC, fmt.Sprintf("mgmt%d", idx+1))
			if err != nil {
				return MachineConfig{}, err
			}
			mgmtIfaces = append(mgmtIfaces, name)
			mgmtMACs = append(mgmtMACs, mac)
		}
		if len(mgmtIfaces) == 0 && (explicitMgmt1 || explicitMgmt2) {
			return MachineConfig{}, errors.New("need at least 1 management interface")
		}
	}

	mgmtGW := ""
	if configureMgmt {
		mgmtGW = strings.TrimSpace(record.MgmtGateway)
		if mgmtGW == "" {
			if legacyRecord && bundle.Defaults.MgmtGateway != "" {
				mgmtGW = strings.TrimSpace(bundle.Defaults.MgmtGateway)
			} else if legacyRecord {
				mgmtGW = deriveGateway(record.MgmtIP)
			} else {
				return MachineConfig{}, errors.New("mgmt_gateway is required in inventory when management network configuration is enabled")
			}
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
	if legacyRecord && rdmaCount == 0 {
		rdmaCount = len(bundle.Defaults.RDMAInterfaces)
	}
	rdma := make([]RDMAConfig, 0, rdmaCount)
	if bundle.RDMAExists() {
		for idx := 0; idx < rdmaCount; idx++ {
			item := RDMAConfig{}
			// Full route configuration always needs a stable per-slot table. For
			// names-only inventories, apply the same derived table when the slot
			// is explicitly present in inventory; bundle-only legacy interface
			// defaults remain table-less because no route will be configured.
			if bundle.RDMAConfigureIPRoute() || idx < len(record.RDMA) {
				item.Table = 101 + idx
			}
			if legacyRecord && idx < len(bundle.Defaults.RDMAInterfaces) && bundle.Defaults.RDMAInterfaces[idx].Table > 0 {
				item.Table = bundle.Defaults.RDMAInterfaces[idx].Table
			}
			if idx < len(record.RDMA) {
				row := record.RDMA[idx]
				item.MAC = strings.TrimSpace(row.MAC)
				if strings.TrimSpace(row.Name) != "" || strings.TrimSpace(row.MAC) != "" {
					resolvedName, mac, err := resolveInterfaceName(firstNonEmpty(row.Name, fmt.Sprintf("xgbe%d", idx)), row.MAC, "", ifaceByMAC, fmt.Sprintf("rdma%d", idx+1))
					if err != nil {
						return MachineConfig{}, err
					}
					item.Name = strings.TrimSpace(row.Name)
					if item.Name == "" && legacyRecord {
						item.Name = resolvedName
					}
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
				if legacyRecord {
					item.Name = fmt.Sprintf("rdma%d", idx+1)
				} else {
					item.Name = fmt.Sprintf("xgbe%d", idx)
				}
			}
			if bundle.RDMAConfigureIPRoute() && item.IP == "" {
				return MachineConfig{}, fmt.Errorf("inventory row missing rdma%d_ip", idx+1)
			}
			if bundle.RDMAConfigureIPRoute() && (item.Prefix <= 0 || item.Gateway == "") {
				if legacyRecord {
					if item.Prefix == 0 {
						item.Prefix = firstNonZero(bundle.Defaults.RDMAPrefix, 24)
					}
					if item.Gateway == "" && item.IP != "" {
						item.Gateway = deriveGateway(item.IP)
					}
				} else {
					return MachineConfig{}, fmt.Errorf("inventory row missing rdma%d_prefix or rdma%d_gateway; fill inventory", idx+1, idx+1)
				}
			}
			if item.Prefix < 0 || item.Prefix > 32 {
				return MachineConfig{}, fmt.Errorf("invalid rdma%d_prefix: use 1..32", idx+1)
			}
			if item.RouteCIDR == "" && item.IP != "" && item.Prefix > 0 {
				if _, network, err := net.ParseCIDR(fmt.Sprintf("%s/%d", item.IP, item.Prefix)); err == nil {
					item.RouteCIDR = network.String()
				}
			}
			rdma = append(rdma, item)
		}
	}

	targetName := strings.TrimSpace(record.MgmtTargetName)
	bondName := ""
	if mgmtMode == MgmtModeBond {
		bondName = targetName
		if bondName == "" {
			bondName = "bond0"
		}
	}
	bondPrimary := strings.TrimSpace(bundle.Defaults.BondPrimary)
	if mgmtMode == MgmtModeSingle {
		bondPrimary = ""
	}
	if strings.EqualFold(bundle.Defaults.BondMode, "active-backup") && bondPrimary != "" && len(mgmtIfaces) > 0 && !stringInSlice("", mgmtIfaces) && !stringInSlice(bondPrimary, mgmtIfaces) {
		return MachineConfig{}, fmt.Errorf("bond_primary %q is not one of management interfaces: %s", bondPrimary, strings.Join(mgmtIfaces, ","))
	}
	if mgmtMode == MgmtModeBond && bondName != "" && stringInSlice(bondName, mgmtIfaces) {
		return MachineConfig{}, fmt.Errorf("mgmt_target_name %q must differ from its member interface names", bondName)
	}
	if mgmtMode == MgmtModeSingle && targetName != "" && len(mgmtIfaces) == 1 && targetName != mgmtIfaces[0] {
		return MachineConfig{}, fmt.Errorf("mgmt_target_name %q must equal selected management interface %q in single mode", targetName, mgmtIfaces[0])
	}
	if mgmtMode == MgmtModeBond {
		targetName = bondName
	} else if mgmtMode == MgmtModeSingle && targetName == "" && len(mgmtIfaces) == 1 {
		targetName = mgmtIfaces[0]
	}

	cfg := MachineConfig{
		HostID:         strings.TrimSpace(record.Hostname),
		Hostname:       strings.TrimSpace(record.Hostname),
		MgmtMode:       mgmtMode,
		MgmtTargetName: targetName,
		MgmtBondName:   bondName,
		MgmtIP:         strings.TrimSpace(record.MgmtIP),
		MgmtPrefix:     mgmtPrefix,
		MgmtGateway:    mgmtGW,
		MgmtIfaces:     mgmtIfaces,
		MgmtMACs:       mgmtMACs,
		MgmtDNS:        dns,
		MgmtMTU:        bundle.Defaults.MgmtMTU,
		BondMode:       bundle.Defaults.BondMode,
		BondLACPRate:   bundle.Defaults.BondLACPRate,
		BondXmitHash:   bundle.Defaults.BondTransmitHashPolicy,
		BondMII:        bundle.Defaults.BondMIIMonitorInterval,
		BondPrimary:    bondPrimary,
		RDMAMTU:        bundle.Defaults.RDMAMTU,
		RoutePriority:  bundle.Defaults.RoutePriority,
		RDMA:           rdma,
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

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
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
