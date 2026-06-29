package spec

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
)

type Bundle struct {
	Defaults                     Defaults         `json:"defaults"`
	Platform                     PlatformConfig   `json:"platform"`
	PlatformOptions              PlatformOptions  `json:"platform_options"`
	OfflineAPT                   OfflineAPTConfig `json:"offline_apt"`
	OfflineRepo                  OfflineAPTConfig `json:"offline_repo"`
	Packages                     []string         `json:"packages"`
	Artifacts                    Artifacts        `json:"artifacts"`
	XRE                          XREConfig        `json:"xre"`
	MlxConfig                    MlxConfig        `json:"mlxconfig"`
	Check                        CheckConfig      `json:"check"`
	TopLevelRDMAExsist           *bool            `json:"rdma_exsist,omitempty"`
	TopLevelRDMAExist            *bool            `json:"rdma_exist,omitempty"`
	TopLevelRDMAConfigureIPRoute *bool            `json:"rdma_configure_ip_route,omitempty"`
	PostPackages                 []string         `json:"post_packages"`
	PostTasks                    []PostTask       `json:"post_tasks"`
	PostPowerAction              PostPowerAction  `json:"post_power_action"`
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
	RDMAExsist                 *bool                  `json:"rdma_exsist,omitempty"`
	RDMAExist                  *bool                  `json:"rdma_exist,omitempty"`
	RDMAConfigureIPRoute       *bool                  `json:"rdma_configure_ip_route,omitempty"`
	ConfigureManagementNetwork *bool                  `json:"configure_management_network,omitempty"`
	ApplyNetworkImmediately    *bool                  `json:"apply_network_immediately,omitempty"`
	BackupExistingNetplan      bool                   `json:"backup_existing_netplan"`
	BackupExistingNetwork      bool                   `json:"backup_existing_network"`
	DisableExistingAptSources  bool                   `json:"disable_existing_apt_sources"`
	DisableExistingRepos       bool                   `json:"disable_existing_repos"`
	RDMAInterfaces             []RDMAInterfaceDefault `json:"rdma_interfaces"`
}

type PlatformOptions struct {
	Ubuntu UbuntuPlatformOptions `json:"ubuntu"`
	RedHat RedHatPlatformOptions `json:"redhat"`
}

type UbuntuPlatformOptions struct {
	BackupExistingNetplan     *bool `json:"backup_existing_netplan,omitempty"`
	DisableExistingAptSources *bool `json:"disable_existing_apt_sources,omitempty"`
}

type RedHatPlatformOptions struct {
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
	Duration            int              `json:"duration"`
	GIDIndex            int              `json:"gid_index"`
	Iterations          int              `json:"iterations"`
	BandwidthQPs        int              `json:"bandwidth_qps"`
	MessageSize         int              `json:"message_size"`
	ReportGBits         bool             `json:"report_gbits"`
	MmapDevice          string           `json:"mmap_device"`
	MinGBits            float64          `json:"min_gbits"`
	Parallel            bool             `json:"parallel"`
	BasePort            int              `json:"base_port"`
	RDMAPingCount       int              `json:"rdma_ping_count"`
	RDMAPingPayloadSize int              `json:"rdma_ping_payload_size"`
	RDMAPingTimeout     int              `json:"rdma_ping_timeout"`
	SSHUser             string           `json:"ssh_user"`
	SSHOptions          []string         `json:"ssh_options"`
	RDMAGroups          []CheckRDMAGroup `json:"rdma_groups"`
}

type CheckRDMAGroup struct {
	IBDevice   string   `json:"ib_device"`
	XPUOffsets []string `json:"xpu_offsets"`
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
	Name    string
	MAC     string
	IP      string
	Prefix  string
	Gateway string
	Table   string
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
	Name    string
	MAC     string
	IP      string
	Prefix  int
	Gateway string
	Table   int
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
	if b.Defaults.RDMARouteCIDR == "" {
		b.Defaults.RDMARouteCIDR = "11.1.0.0/21"
	}
	if b.Defaults.RoutePriority == 0 {
		b.Defaults.RoutePriority = 32761
	}
	if b.RDMAExists() && len(b.Defaults.RDMAInterfaces) == 0 {
		b.Defaults.RDMAInterfaces = []RDMAInterfaceDefault{
			{Name: "ens11np0", Table: 101},
			{Name: "ens13np0", Table: 102},
			{Name: "ens15np0", Table: 103},
			{Name: "ens17np0", Table: 104},
		}
	}
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
	if b.Check.Duration == 0 {
		b.Check.Duration = 1
	}
	if b.Check.GIDIndex == 0 {
		b.Check.GIDIndex = 3
	}
	if b.Check.Iterations == 0 {
		b.Check.Iterations = 100
	}
	b.Check.ReportGBits = true
	if b.Check.BasePort == 0 {
		b.Check.BasePort = 18515
	}
	if b.Check.RDMAPingCount == 0 {
		b.Check.RDMAPingCount = 3
	}
	if b.Check.RDMAPingPayloadSize == 0 {
		b.Check.RDMAPingPayloadSize = 8972
	}
	if b.Check.RDMAPingTimeout == 0 {
		b.Check.RDMAPingTimeout = 2
	}
	if b.Check.SSHUser == "" {
		b.Check.SSHUser = "root"
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
	return b.Platform.Validate()
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
	case "redhat", "rhel", "centos", "kylin", "rocky", "almalinux", "anolis":
		return true
	default:
		return false
	}
}

func (b Bundle) RDMAExists() bool {
	return firstConfiguredBool(true, b.TopLevelRDMAExist, b.TopLevelRDMAExsist, b.Defaults.RDMAExist, b.Defaults.RDMAExsist)
}

func (b Bundle) RDMAConfigureIPRoute() bool {
	if !b.RDMAExists() {
		return false
	}
	return firstConfiguredBool(true, b.TopLevelRDMAConfigureIPRoute, b.Defaults.RDMAConfigureIPRoute)
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
	return firstConfiguredBool(b.Defaults.BackupExistingNetwork, b.PlatformOptions.RedHat.BackupExistingNetwork)
}

func (b Bundle) DisableExistingRepos() bool {
	return firstConfiguredBool(b.Defaults.DisableExistingRepos || b.Defaults.DisableExistingAptSources, b.PlatformOptions.RedHat.DisableExistingRepos)
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

	rdma := make([]RDMAConfig, 0, len(bundle.Defaults.RDMAInterfaces))
	if bundle.RDMAExists() {
		for idx, def := range bundle.Defaults.RDMAInterfaces {
			item := RDMAConfig{
				Name:    def.Name,
				Prefix:  bundle.Defaults.RDMAPrefix,
				Gateway: strings.TrimSpace(def.Gateway),
				Table:   def.Table,
			}
			if idx < len(record.RDMA) {
				row := record.RDMA[idx]
				item.MAC = strings.TrimSpace(row.MAC)
				name, mac, err := resolveInterfaceName(row.Name, row.MAC, item.Name, ifaceByMAC, fmt.Sprintf("rdma%d", idx+1))
				if err != nil {
					return MachineConfig{}, err
				}
				item.Name = firstNonEmpty(row.Name, item.Name, name)
				item.MAC = mac
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

func stringInSlice(needle string, haystack []string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}
