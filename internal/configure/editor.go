package configure

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// PolicyFields is the finite ordinary editor surface; advanced data is retained.
var PolicyFields = []string{
	"defaults.configure_management_network", "defaults.apply_network_immediately",
	"defaults.mgmt_mtu", "defaults.rdma_mtu", "defaults.bond_mode",
	"defaults.bond_lacp_rate", "defaults.bond_transmit_hash_policy",
	"defaults.bond_mii_monitor_interval", "defaults.bond_primary",
	"defaults.rdma_mode", "defaults.rdma_gateway_probe.enabled", "defaults.route_priority",
	"defaults.backup_existing_netplan", "defaults.backup_existing_network",
	"defaults.disable_existing_apt_sources", "defaults.disable_existing_repos",
	"check.bandwidth.gid_index", "check.bandwidth.min_gbits", "check.rdma_ping.payload_size",
	"check.ssh.user", "check.ssh.port", "check.ssh.options", "check.xccl.enabled",
	"post_power_action.action",
}

// PolicyEditorRow is the small, ordinary configuration surface shown by the
// interactive editor. Advanced objects remain file-editable by design.
type PolicyEditorRow struct {
	Category    string
	Path        string
	Label       string
	Description string
	Current     string
	Explicit    bool
}

type PolicyEditorData struct {
	Rows     []PolicyEditorRow
	Expanded map[string]bool
}

type policyFieldSpec struct {
	category, path, label, description string
}

var policyFieldSpecs = []policyFieldSpec{
	{"默认网络", "defaults.configure_management_network", "配置管理网", "是否由 env_init 配置服务器的管理网络。"},
	{"默认网络", "defaults.apply_network_immediately", "立即应用网络", "网络文件写入后是否马上切换网络，远程机器请谨慎。"},
	{"默认网络", "defaults.mgmt_mtu", "管理网 MTU", "管理网数据包的最大长度，通常保持 1500。"},
	{"默认网络", "defaults.rdma_mtu", "RDMA MTU", "RDMA 网卡使用的数据包长度，常见值为 9000。"},
	{"默认网络", "defaults.bond_mode", "Bond 模式", "两张管理网卡如何绑定：802.3ad 或 active-backup。"},
	{"默认网络", "defaults.bond_lacp_rate", "LACP 发送频率", "802.3ad 模式下协商频率：slow 更稳，fast 更快发现变化。"},
	{"默认网络", "defaults.bond_transmit_hash_policy", "Bond 分流策略", "决定 bond 如何把连接分配到不同网卡。"},
	{"默认网络", "defaults.bond_mii_monitor_interval", "网卡链路检查间隔", "active-backup 检查网卡链路的毫秒数。"},
	{"默认网络", "defaults.bond_primary", "主网卡", "active-backup 优先使用的接口名，可留空自动选择。"},
	{"默认网络", "defaults.rdma_mode", "RDMA 配置模式", "full 配置 RDMA 地址和路由，names_only 只准备名称，off 关闭。"},
	{"默认网络", "defaults.rdma_gateway_probe.enabled", "探测 RDMA 网关", "是否在现场用网关连通性辅助判断 RDMA 网卡。"},
	{"默认网络", "defaults.route_priority", "路由优先级", "RDMA 路由的优先级，数字越小通常越优先。"},
	{"默认网络", "defaults.backup_existing_netplan", "备份已有 Netplan", "改写 Ubuntu 网络配置前是否保留旧文件。"},
	{"默认网络", "defaults.backup_existing_network", "备份已有网络配置", "改写 Kylin/RedHat 网络配置前是否保留旧文件。"},
	{"默认网络", "defaults.disable_existing_apt_sources", "停用已有 APT 源", "使用离线源时是否停用系统原有 APT 源。"},
	{"默认网络", "defaults.disable_existing_repos", "停用已有 Yum 源", "使用离线源时是否停用系统原有 Yum 源。"},

	{"物料", "offline_repo.enabled", "启用离线软件源", "是否从交付目录中的离线软件源安装系统包。"},
	{"物料", "offline_repo.material_path", "离线源路径", "交付目录内离线软件源的相对路径。"},
	{"物料", "offline_repo.copy_to", "离线源目标目录", "部署时复制到目标服务器的目录。"},
	{"物料", "offline_repo.target_file", "软件源配置文件", "目标机上生成的 APT/Yum 配置文件路径。"},
	{"物料", "artifacts.work_dir", "材料工作目录", "目标服务器上保存驱动和安装中间文件的目录。"},
	{"物料", "artifacts.ofed_archive", "OFED 安装包", "RDMA 驱动安装包在交付目录中的路径。"},
	{"物料", "artifacts.xre_installer", "XRE 安装包", "XRE 驱动安装程序在交付目录中的路径。"},
	{"物料", "artifacts.xdr_archive", "XDR 安装包", "XDR 驱动压缩包在交付目录中的路径。"},
	{"物料", "artifacts.firmware_archive", "固件包", "加速卡固件包在交付目录中的路径。"},
	{"物料", "artifacts.container_packages", "容器组件包列表", "容器运行时安装需要的离线软件包列表；请输入 JSON 数组。"},

	{"超时时间", "apply_timeouts.default_seconds", "通用超时（秒）", "没有单独设置的安装步骤最多运行多久。0 表示不设超时。"},
	{"超时时间", "apply_timeouts.stages.software", "软件安装超时", "系统软件包安装最多运行多久。"},
	{"超时时间", "apply_timeouts.stages.ofed", "OFED 超时", "OFED 驱动安装最多运行多久。"},
	{"超时时间", "apply_timeouts.stages.network", "网络配置超时", "网络配置步骤最多运行多久。"},
	{"超时时间", "apply_timeouts.stages.xre", "XRE 超时", "XRE 驱动安装最多运行多久。"},
	{"超时时间", "apply_timeouts.stages.xdr", "XDR 超时", "XDR 驱动安装最多运行多久。"},
	{"超时时间", "apply_timeouts.stages.firmware", "固件超时", "固件升级步骤最多运行多久。"},
	{"超时时间", "apply_timeouts.stages.container", "容器组件超时", "容器运行时组件安装最多运行多久。"},
	{"超时时间", "apply_timeouts.stages.mlxconfig", "mlxconfig 超时", "网卡参数设置最多运行多久。"},
	{"超时时间", "apply_timeouts.stages.sysctl", "内核参数超时", "sysctl 参数设置最多运行多久。"},
	{"超时时间", "apply_timeouts.stages.kernel", "内核模块超时", "内核模块安装最多运行多久。"},
	{"超时时间", "apply_timeouts.stages.post", "收尾任务超时", "部署后收尾任务最多运行多久。"},
	{"超时时间", "apply_timeouts.stages.udev", "udev 规则超时", "udev 规则安装最多运行多久。"},

	{"check", "check.bandwidth.duration", "带宽测试时长", "带宽检查按时长运行的秒数。"},
	{"check", "check.bandwidth.run_by_duration", "按时长测试", "打开后按上面的时长测试，否则按迭代次数测试。"},
	{"check", "check.bandwidth.gid_index", "GID 索引", "RDMA 测试使用的 GID；auto 表示自动选择。"},
	{"check", "check.bandwidth.iterations", "带宽测试次数", "带宽测试重复次数。"},
	{"check", "check.bandwidth.bandwidth_qps", "带宽 QP 数", "带宽测试使用的队列对数量。"},
	{"check", "check.bandwidth.message_size", "带宽消息大小", "带宽测试单条消息大小，0 表示工具默认值。"},
	{"check", "check.bandwidth.report_gbits", "显示 Gbit/s", "是否把带宽结果同时显示为 Gbit/s。"},
	{"check", "check.bandwidth.min_gbits", "最低带宽阈值", "低于该值时检查失败；auto 表示按环境判断。"},
	{"check", "check.rdma_ping.count", "RDMA Ping 次数", "RDMA 连通性测试发送多少次探测包。"},
	{"check", "check.rdma_ping.payload_size", "Ping 包大小", "RDMA Ping 的负载大小；auto 表示按 MTU 计算。"},
	{"check", "check.rdma_ping.timeout", "Ping 超时", "单次 RDMA Ping 等待响应的秒数。"},
	{"check", "check.ssh.user", "SSH 用户", "check/discover 连接目标服务器使用的用户。"},
	{"check", "check.ssh.port", "SSH 端口", "check/discover 连接目标服务器使用的端口。"},
	{"check", "check.xccl.enabled", "启用 XCCL 检查", "是否执行 XCCL 集合通信检查。"},

	{"部署后", "post_power_action.action", "部署后电源动作", "部署完成后的动作：none 不操作，reboot 重启等。"},
}

func PolicyEditor(data []byte) (PolicyEditorData, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return PolicyEditorData{}, err
	}
	if _, err := DecodeBundle(data); err != nil {
		return PolicyEditorData{}, err
	}
	rows := make([]PolicyEditorRow, 0, len(policyFieldSpecs))
	needed := map[string]bool{"默认网络": true}
	for _, field := range policyFieldSpecs {
		currentValue, explicit := jsonPath(raw, field.path)
		row := PolicyEditorRow{
			Category: field.category, Path: field.path, Label: field.label, Description: field.description,
			Current: displayJSON(currentValue), Explicit: explicit,
		}
		rows = append(rows, row)
	}
	return PolicyEditorData{Rows: rows, Expanded: needed}, nil
}

func jsonPath(root map[string]any, path string) (any, bool) {
	var current any = root
	for _, part := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func displayJSON(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func PolicyCategories(data PolicyEditorData) []string {
	seen := map[string]bool{}
	var categories []string
	for _, row := range data.Rows {
		if !seen[row.Category] {
			seen[row.Category] = true
			categories = append(categories, row.Category)
		}
	}
	return categories
}

func sortPolicyStageRows(rows []PolicyEditorRow) {
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Path < rows[j].Path })
}

func EditPolicy(data []byte, path, value string) ([]byte, error) {
	allowed := false
	for _, field := range PolicyFields {
		if path == field {
			allowed = true
			break
		}
	}
	if !allowed {
		for _, field := range policyFieldSpecs {
			if path == field.path {
				allowed = true
				break
			}
		}
	}
	if !allowed {
		return nil, fmt.Errorf("%s is not an ordinary editable policy", path)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		decoded = value
	}
	if decoded == nil {
		return nil, fmt.Errorf("use a concrete value, not null")
	}
	parts := strings.Split(path, ".")
	current := root
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	current[parts[len(parts)-1]] = decoded
	result, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	b, err := DecodeBundle(result)
	if err != nil {
		return nil, err
	}
	b.ApplyDefaults()
	if err := validateTemplatePolicies(b); err != nil {
		return nil, err
	}
	return result, nil
}

func PolicySummary(data []byte) string {
	var root map[string]any
	_ = json.Unmarshal(data, &root)
	var out strings.Builder
	for _, field := range PolicyFields {
		var value any = root
		for _, part := range strings.Split(field, ".") {
			object, _ := value.(map[string]any)
			value = object[part]
		}
		encoded, _ := json.Marshal(value)
		if strings.Contains(field, "options") {
			encoded = []byte("[redacted; edit with a JSON array]")
		}
		fmt.Fprintf(&out, "%s = %s\n", field, encoded)
	}
	return out.String()
}
