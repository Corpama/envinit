# Inventory 与 Bundle 参数精简决策稿

状态：本轮已按“最终决定”实施，本文作为 schema、兼容策略和验收记录。

本文基于当前仓库代码及 `examples/` 模板整理。第 5～8 节的“最终决定”是本轮实现依据；第 9～11 节记录统一规则、验收范围和后续专项。

## 1. 文档状态

本文是本轮重构的决策与验收记录，不再作为待讨论清单。各议题保留“当前/建议/最终决定”用于说明变更来源；空白项表示该项没有额外约束，不表示尚未授权。

---

## 2. 本轮目标与边界

### 2.1 建议目标

1. 减少普通用户必须手工填写的参数。
2. 项目共用参数只维护一次，逐机差异放在 inventory。
3. 可确定计算的值由程序派生，可采集的硬件事实由目标机发现。
4. 保留有实际用途的高级覆盖能力。
5. 让 examples、downloader 向导、plan/apply、check/discover 使用一致的规则。

### 2.2 用户填写

- 本轮最重要的目标：精简bundle.json以及inventory.csv中不必要的配置项，特别是bundle.json，可以将部分配置转为自动化处理，但要考虑所有的使用场景
- 本轮必须保留的使用场景：参考docs中的使用场景的相关描述
- 本轮不处理的内容：暂无
- 是否允许修改配置 schema：可以，具体情况具体讨论
- 是否必须兼容已有 inventory/bundle：不需要
- 是否继续以 CSV/XLSX 为主要 inventory 格式：继续支持 CSV/XLSX；CSV 为默认输出格式，XLSX 继续作为输入格式。

### 2.3 精简的三种含义

| 类型 | 含义 | 示例 |
| --- | --- | --- |
| 模板精简 | 字段仍支持，但普通模板不再展开 | 超时表、空数组、默认测试参数 |
| 自动化 | 未填写时由程序派生或探测 | route CIDR、接口物理绑定 |
| schema 合并 | 合并重复入口，需要修改解析及调用方 | `offline_apt` 与 `offline_repo` |

三者需要分别决定，不能把“从模板移除”直接理解成“从代码删除”。

---

## 3. 当前数据流及实现依据

### 3.1 主要路径

```text
bundle JSON
  -> bundle.Load：严格解码、本机平台探测、ApplyDefaults、Validate

inventory CSV/TSV/TXT/XLSX
  -> inventory.Load：解析 MachineRecord / RDMARecord

plan/apply
  -> 选择机器 -> ResolveMachine -> 目标机接口发现/绑定 -> 各 stage

check/discover
  -> 解析目标 -> 使用原始 inventory 记录及部分 bundle 默认值
  -> 远端发现、测试或 inventory 写回

env_init config（拟新增）
  -> configure.DecodeBundle / ValidateProject
  -> 离线校验 -> 保存 planning/bundle.json 与 inventory.csv
```

注意：check/discover 并不全部复用 `ResolveMachine`；改变默认接口名、prefix 或槽位规则时，需要检查这些调用方。

### 3.2 相关代码

| 文件或目录 | 作用 |
| --- | --- |
| `internal/spec/spec.go` | 配置结构、默认值、机器配置合并 |
| `internal/inventory/load.go` | inventory 文件解析和字段别名 |
| `internal/inventory/parse_bytes.go` | 离线规划解析、严格表头校验 |
| `internal/bundle/load.go` | bundle 加载 |
| `internal/bundle/platform_detect.go` | 加载时本机平台探测 |
| `internal/configure/validate.go` | downloader 离线项目校验 |
| `cmd/downloader/wizard.go` | 向导输入、配置生成、预览 |
| `cmd/downloader/project.go` | profile 模板及 planning 文件管理 |
| `internal/runner/nic_binding.go`、`internal/nicdetect/` | 管理口及 RDMA 口发现和绑定 |
| `internal/runner/render_config.go` | 网络文件、路由生成 |
| `internal/runner/platform.go`、`stage_install.go` | 网络后端、软件源及安装 |
| `internal/checker/targets.go`、`rdma_discovery.go` | 目标解析、发现及写回 |
| `internal/checker/rdma_ping.go`、`counters.go`、`xccl.go` | 测试参数、接口及拓扑消费 |
| `cmd/envinit/main.go`、`internal/checker/check_setup_tui.go` | CLI/TUI 对测试参数的覆盖 |
| `examples/inventory.sample.csv` | 当前普通 inventory 示例，30 列 |
| `examples/bundle.ubuntu22.sample.json` | Ubuntu profile 示例 |
| `examples/bundle.kylin10sp3.sample.json` | Kylin profile 示例 |

---

## 4. 需要先澄清的现状

以下事实影响精简是否能保持原有行为。

1. **prefix 是网络规划，不是可以从 IP 唯一推导的事实。** 规范 inventory 必须为启用配置的管理/RDMA 槽位提供 prefix；旧配置入口才保留 bundle 默认值兼容。
2. 旧兼容路径中的 `deriveGateway()` 只是把 IPv4 最后一段替换为 `1`，不是按 prefix 计算，也不是发现真实网关。规范 inventory 不依赖该逻辑；示例中的 RDMA 网关是 `.126`、`.254`。
3. 当前 examples 的 RDMA prefix 是 `/25`，并由 inventory 逐口提供；bundle 不再写 RDMA prefix 默认值。
4. `apply_network_immediately` 省略时为 `true`，但两个示例都显式设置为 `false`。不能当作普通默认值删除。
5. 示例 `backup_existing_netplan/network=true` 与省略后的回退值不同，不能直接删除以宣称行为等价。
6. `post_power_action` 省略时当前默认是 `soft` 且要求确认；downloader 新项目会设置为 `none`。需要统一决定，不能把省略理解成“不操作”。
7. `check.xccl.enabled` 省略后为 `false`，示例为 `true`；是否自动启用属于执行策略。
8. `check.xccl.environment` 在默认值函数中仅初始化为空 map；不能认为示例中全部环境变量已内置、可以无条件删除。需逐项核对运行时合并及覆盖。
9. examples 显式固定 repo 的 `copy_to=/opt/kunlun-apt-repo` 或 `/opt/kunlun-rpm-repo`；省略时才按 `/opt/<material_path 的目录名>` 派生。
10. `bond_mii_monitor_interval=100` 在当前代码中只针对 active-backup 自动补齐；示例为 802.3ad。删除前需确认生成结果。
11. `apply_timeouts.default_seconds` 不是当前所有 stage 的统一覆盖值：已知 stage 会补齐各自默认项，只有未命中 stage 表时才回退到它。
12. 规范 inventory 显式提供 `mgmt_mode`；离线 `ValidateProject` 要求管理网开启时填写 `single`、`bond` 或 `auto`，当前 examples 使用 `bond`。
13. 接口名和 MAC 不是同一信息。RDMA 名称表达目标命名，MAC 表达物理身份；管理口的 MAC 解析路径还可能使用当前真实接口名，不能假设两类接口语义完全一致。

---

## 5. Inventory 逐项决策

### I-01：`host_id` 与 `hostname`

- 当前：示例两列值相同；`ResolveMachine` 在 hostname 为空时回退到 host_id。
- 建议：普通模板只要求一个机器标识，hostname 作为不同名时的覆盖字段。
- 注意：显式选择机器执行 apply 时，会使用解析出的 hostname 做主机名设置；host_id 若是资产编号，不能默认认为它就是希望设置的 hostname。
- 最终决定：去掉host_id，没必要了。
- 目标身份字段：`hostname`。
- hostname 省略时的行为：规范 inventory 直接报错；旧表仅在兼容入口按 `host_id` 补齐并告警。

### I-02：管理网 prefix、gateway、DNS 的重复填写

- 当前：支持逐机值覆盖 bundle 默认值；当前示例的管理 prefix/gateway 两处重复。
- 建议：同一项目共用值写 bundle；inventory 只为例外机器提供覆盖。
- 保留条件：跨多个管理网段时允许逐机配置，不能强制所有机器共用一个网关。
- 最终决定：管理网 prefix、gateway这两项去掉bundle中的设置参数，利用inventory进行设置；DNS使用bundle中的值，我记得inventory中也没有这个值的设置吧
- 默认值保留位置：DNS 仍在 `defaults.mgmt_nameservers`；管理 prefix/gateway 只在 inventory 逐机填写。
- 是否允许逐机覆盖：允许，规范 inventory 的逐机值即为权威规划。

### I-03：`mgmt_mode`（single / bond）

- 当前：运行时允许缺省并尝试判断接口数量；离线向导校验要求显式模式。
- 建议：即使物理接口自动发现，也保留管理拓扑意图的表达。硬件有两个候选口，并不必然表示用户希望组 bond。
- 可选方向：保持显式 single/bond；或增加明确 auto 语义并规定歧义处理。
- 最终决定：在 `mgmt_ip` 左侧添加一列 `mgmt_mode`，支持 `single`、`bond`、`auto`，缺省为 `auto`。
- `auto` 根据 inventory 中管理接口槽位的填写情况决定：`mgmt_iface1/2` 或对应的 `mgmt_mac1/2` 任一有值即算一个已指定槽位；一个槽位为 `single`，两个槽位为 `bond`；两个槽位都为空时根据目标机现场发现的可用管理网卡数量决定。
- 显式 `single` 优先于现场发现数量，即使发现两张网卡也只配置单口；显式 `bond` 保留 bond 意图，即使只发现一张网卡也不静默降级。
- `single` 模式下填写两个管理槽位时报错。`bond` 或 auto 判定为 bond、但现场只发现一张可用网卡时，在网卡确认 TUI 中提示，必须由用户确认后才允许继续生成单成员 bond；拒绝确认则终止该机器操作。
- `auto` 模式下，如果现场发现超过两张管理网卡，进入网卡选择 TUI，由用户选择使用一张或两张网卡。TUI 优先推荐链路已插入（carrier/link up）的两张网卡；用户可以改选其他一张或两张网卡。未完成选择时直接报错，并提示用户补充或修正 inventory/现场绑定信息。
- 是否需要显式模式：需要，支持 `single`、`bond`、`auto`。
- 缺省模式：`auto`。


### I-04：管理口接口名与 MAC

- 当前：`mgmt_iface1/2`、`mgmt_mac1/2` 均可参与绑定；已有目标机发现流程。
- 建议：减少人工抄写，允许自动采集；已知 MAC 时继续支持精确指定物理端口。
- 需明确：接口名是当前名称、目标名称，还是兼容提示；所有路径使用一致规则。
- 最终决定：在 discover 时，`mgmt_iface1/2`、`mgmt_mac1/2` 可以为空，表示待发现、待确认的槽位；如果已有值，发现结果也允许覆盖，但必须经过 discover 写回确认流程。在 apply 时，它们表示目标接口名称和物理绑定信息；在 check 时，它们作为服务器现状或目标信息参与解析。
- discover 写回的管理接口名和 RDMA 当前接口名，直接作为下一次 apply 的目标名称；如果 RDMA `rdmaN_name` 仍为空且现场没有可写回的接口名，才使用 `xgbe0`、`xgbe1`……的默认规则。
- 普通模板展示 `mgmt_iface1/2` 和 `mgmt_mac1/2`，但 MAC 可以留空，由 discover/apply 采集。
- `single` 模式下 `mgmt_target_name` 必须等于唯一选中的管理接口名；如果只有 MAC 没有接口名，则允许将 `mgmt_target_name` 作为该接口的目标名。`single` 模式填写两个接口槽位或两个 MAC 槽位时报错。
- 保留管理口目标名称和物理绑定能力；自动采集结果写回 inventory，并保留运行时绑定状态供 apply 重跑校验。

### I-05：`mgmt_target_name`（管理接口或 bond 名称）

- 当前：可继承 bundle，内置默认 `bond0`；显式 single 模式会清空 bond 名。
- 建议：普通模板不逐机重复 bond0，仅特殊名称时覆盖。
- 最终决定：inventory 增加 `mgmt_target_name` 列，放在 `mgmt_ip` 左侧。该字段根据 `mgmt_mode` 解释：
  - `single`：填写目标管理接口名，必须是 `mgmt_iface1` 或 `mgmt_iface2` 中选定的那个接口名；
  - `bond`：填写目标 bond 名，例如 `bond0` 或用户自定义的 bond 名；留空时默认使用 `bond0`；
  - `auto`：先根据管理接口槽位数量解析为 `single` 或 `bond`，再按对应规则解释该字段。
- bundle 删除 `mgmt_bond_name`。如果 `single` 模式把 `mgmt_target_name` 填成 bond 名，或 `bond` 模式把它填成成员接口名，均报错。
- 默认 bond 名：`bond0`
- 是否允许逐机覆盖：允许；`bond` 模式可覆盖默认 `bond0`，`single` 模式填写目标管理接口名。

### I-06：RDMA 名称、IP 与 MAC

- 当前：`rdmaN_name` 可指定目标名称；未指定时 resolver 最终可生成 `rdmaN`，示例使用 `xgbeN`。MAC 用于物理绑定，IP 用于目标网络规划。
- 建议：统一默认命名规则；MAC 可自动采集，已知绑定可覆盖。规划新 IP 仍需人工或外部地址规划系统提供。
- 注意：discover 采集的是既有网络事实，不等同于为未配置服务器生成目标地址。修改名称缺省规则还需同步 checker。
- 最终决定：当前版本继续保留 inventory 中的 `rdmaN_name` 列，便于运维人员直接看到和确认每个 RDMA 槽位的目标名称。bundle 删除 `rdma_interfaces`，RDMA 接口名称由 inventory 和自动规则决定。
- 如果 `rdmaN_name` 留空，程序按槽位自动生成 `xgbeN` 形式的目标名称，并采用零基编号：第 1 个槽位默认 `xgbe0`，第 2 个槽位默认 `xgbe1`，依此类推。
- 后续版本可以在 inventory 中移除 `rdmaN_name` 列；届时继续使用相同的 `xgbe0`、`xgbe1`、`xgbe2`……自动命名规则。
- 当前版本是否要求逐机填写 RDMA IP：由 `rdmaN_ip` 规划决定；`rdma_mode=full` 时，参与 RDMA 配置的每个规划槽位都必须填写 `rdmaN_ip`。缺少 IP 时直接报错，并提示用户补充相关 inventory 信息。
- 当前版本是否要求填写 MAC：不要求；MAC 属于现场事实，可以由 discover 或 apply 自动采集和绑定。
- 现场探测与规划值冲突时：discover 提示用户，由用户决定是否覆盖；apply 使用解析后的目标规划和绑定结果。

### I-07：RDMA prefix 与 gateway

- 当前：prefix 支持全局默认与逐口覆盖；gateway 支持 bundle 对应槽位默认与逐口覆盖，之后回退到 `.1` 推导。
- 建议：统一 prefix 时在 bundle 写一次；按槽位共用网关时可在 bundle 维护，例外机器覆盖。
- 注意：不能从 IP 唯一推导 prefix 或真实 gateway；保留示例 `/25`、`.126/.254` 的网络含义。
- 最终决定：去掉 bundle 中的 `rdma_prefix`、RDMA gateway 默认配置，按照 inventory 中的逐口 `rdmaN_prefix` 和 `rdmaN_gateway` 配置。
- prefix 默认位置及覆盖规则：逐口 inventory 的 `rdmaN_prefix`，不再从 bundle 继承。
- gateway 默认位置及覆盖规则：逐口 inventory 的 `rdmaN_gateway`，不再从 bundle 继承。
- 是否保留 `.1` 推导：不保留。
- 全部缺省时如何处理：`rdma_mode=full` 且该槽位参与 RDMA 配置时，`rdmaN_ip`、`rdmaN_prefix`、`rdmaN_gateway` 均为必填；缺少任一字段直接报错，并提示用户补充相关 inventory 信息。`names_only`、`off` 不要求这些网络字段。

### I-08：`rdmaN_table`、`rdmaN_route_cidr`

- 当前：通常自动使用 101、102……；直连 route CIDR 可按 IP/prefix 派生。
- 建议：普通模板省略，保留特殊路由规划的覆盖能力。
- 实施细节：当前如果提供了 `defaults.rdma_interfaces` 的某项但未填 table，该对象会覆盖初始的 `101+index` 默认，需统一补默认的顺序。
- 最终决定：普通模板省略 `rdmaN_table` 和 `rdmaN_route_cidr`，保留旧/高级 inventory 中的显式覆盖读取能力。bundle 删除 `rdma_route_cidr`；未填写逐口 route CIDR 时，按该 RDMA 接口 IP 和 prefix 派生直连网段。路由表未填写时按 RDMA 槽位使用 `101`、`102`……。
- 路由表默认规则：第 1 个 RDMA 槽位为 101，之后按槽位递增。
- 是否保留显式覆盖：保留，作为高级 inventory 字段。

### I-09：`rdmaN_rail_id`

- 当前：示例预留四列但均为空；XCCL 支持基于网段、拓扑及槽位顺序推断，显式 ID 用于已知隔离 rail 对应关系。
- 建议：从普通模板移除空列，在高级示例中保留。
- 最终决定：从普通模板移除空列，在高级示例中保留。

### I-10：inventory 格式

- 当前：支持 CSV/TSV/TXT/XLSX；discover 写回仅支持 CSV/TSV/TXT。
- 建议：本轮优先精简表头及填写流程，继续利用现有表格格式。
- 如果需要嵌套数组格式，单独决定 JSON/YAML 支持、导入导出及 discover 写回方案。
- 最终决定：本轮优先精简表头及填写流程，继续利用现有表格格式。
- 需要支持的输入和输出格式：继续支持 CSV/TSV/TXT 输入，CSV 为默认输出；XLSX 继续支持输入，discover 不写回 XLSX。

---

## 6. Bundle 逐项决策

### B-01：默认值与空值从模板移除

- 建议：省略与代码默认行为一致的参数，完整字段放参考文档；有效配置预览仍展示最终值。

- 候选：默认超时表、backup_root、work_dir、空 post_tasks、空 mlxconfig、部分 check 默认参数。

- 注意：先对照第 4 节，不能按 `0/false/空` 或“看起来像默认值”批量删除。

- 最终决定：

  空值以及默认值的暂时不动，先把冲突、重复的处理了

- 普通模板必须显式展示的参数：平台、网络行为开关、MTU/bond 策略、离线仓库、物料路径和 check 配置；可自动派生的 target file、repo entries、路由表和 route CIDR 不要求重复填写。

### B-02：`defaults.mgmt_interfaces` / `defaults.rdma_interfaces`

- 当前：提供接口名回退；RDMA 对象还承载 table、gateway，并在无逐机 RDMA 项时影响槽位数。

- 建议：区分“项目共用槽位规划”和“单机物理接口事实”。后者交给 inventory/发现，共用 gateway 或命名规则仍可能有价值。

- 注意：整个删除 RDMA 数组会同时失去 gateway/table 的默认入口，必须一起决定替代方式。

- 最终决定：`mgmt_interfaces` 删除，由 inventory 的管理接口槽位和现场发现决定。

  `rdma_interfaces` 删除。RDMA 的目标名称、IP、prefix、gateway 和 MAC 按槽位由 inventory 表达；table、route CIDR、rail ID 等低频字段保留为高级 inventory 覆盖。

- 共用槽位规划保留方式：由每台机器 inventory 的 RDMA 槽位数量表达；route table/CIDR/rail ID 作为高级逐口覆盖。
- 接口名默认入口是否保留：规范 bundle 不保留管理/RDMA 接口数组；RDMA 空名称按 `xgbe0`、`xgbe1`……派生。
- 旧字段处理：入口兼容读取并告警，规范序列化删除 `mgmt_interfaces` 和 `rdma_interfaces`。

### B-03：platform 与自动探测

- 当前：`os_family`、`package_manager`、`network_backend` 存在可推导关系；bundle.Load 会探测加载进程所在机器，离线项目校验要求明确目标 os_family。
- 建议：交付 profile 固定目标平台；包管理器通常由目标平台派生；目标机根据实际服务判断可自动选择的网络后端，保留显式覆盖。
- 注意：下载机或 check 控制机的系统不能代表目标机。省略 package_manager 前需处理现有 loader 可能按本机补值的问题。
- 最终决定：保持现状，交付 profile 固定目标平台；包管理器通常由目标平台派生；目标机根据实际服务判断可自动选择的网络后端，保留显式覆盖。
- 平台信息权威来源（profile / bundle / 目标机）：bundle/profile；网络后端为 `auto` 时由目标机运行时选择。
- 必须显式填写的字段：离线项目校验要求 `platform.os_family` 明确；交付 examples 同时明确 package manager 和网络后端。
- 配置与目标机不一致时的行为：目标平台不匹配时由平台校验或 stage 前置检查失败，不套用下载机/控制机平台。

### B-04：kernel headers

- 当前：yum 系列可派生 `kernel-devel-{{uname_r}}` 及 `/usr/src/kernels/{{uname_r}}`；Ubuntu 示例在 packages 中列出 headers 包。
- 建议：统一按目标系统及运行内核解析，保留特殊内核覆盖；空覆盖字段从普通模板移除。
- 最终决定：统一按目标系统及运行内核解析，保留特殊内核覆盖；空覆盖字段从普通模板移除。

### B-05：网络备份、软件源策略的重复入口

- 当前：`defaults` 和 `platform_options` 都能表达备份及禁用旧软件源；还包含 RedHat/Kylin 回退关系。
- 建议：新配置只提供一套权威入口，旧字段在兼容层规范化。
- 注意：网络配置备份和禁用旧软件源是不同动作，不宜合并为一个总开关。
- 最终决定：保留 `defaults` 作为备份和禁用旧软件源策略的权威入口，删除 `platform_options` 中对应的重复字段。网络配置备份与禁用旧软件源仍是两个独立开关。
- 当前重复关系：例如 Ubuntu 的 `backup_existing_netplan` 既可以写在 `defaults.backup_existing_netplan`，也可以写在 `platform_options.ubuntu.backup_existing_netplan`；禁用 APT/YUM 旧源的字段也存在相同的两处入口。
- 最终字段名及位置：保留 `defaults.backup_existing_netplan`、`defaults.backup_existing_network`、`defaults.disable_existing_apt_sources`、`defaults.disable_existing_repos`。
- 两个入口同时存在时如何处理：新 schema 不再生成或接受 platform_options 中的这些重复字段；读取旧字段时提示弃用并转换到 defaults。
- 默认行为：继续使用当前 defaults 的默认值和平台执行逻辑。

### B-06：统一 `offline_apt` 与 `offline_repo`

- 当前：二者使用相同结构，但 APT stage 仍直接读取 OfflineAPT；不能仅把 Ubuntu 示例字段改名。
- 建议：统一为 `offline_repo`，根据目标平台生成配置文件和格式；安装、plan、备份路径全部使用同一解析结果。
- 可以派生：target_file、标准 entries、copy_to；但需要明确目录约定和 APT 仓库布局，当前默认 APT entry 写死 jammy。
- 最终决定：统一为 `offline_repo`，根据目标平台生成配置文件和格式；安装、plan、备份路径全部使用同一解析结果。
- 最终配置块名称：`offline_repo`。
- enabled 缺省行为：默认 `false`；examples 显式 `enabled: true`。
- copy_to 默认规则：由 `material_path` 末级目录派生 `/opt/<目录名>`；examples 显式固定目标路径。
- entries 的生成依据：按 package manager 生成 apt 或 yum 格式；新配置使用 `{{offline_repo_target}}` 占位符，旧 apt 占位符仍兼容。
- 旧字段兼容规则：`offline_apt` 合并到 `offline_repo` 并输出弃用告警；规范序列化只写 `offline_repo`。

### B-07：网络行为开关

- 字段：`configure_management_network`、`apply_network_immediately`、`rdma_mode`。
- 建议：保留对目标配置范围及生效时机的明确表达。它们是执行意图，不属于重复硬件事实。
- 最终决定：保留现状，这三个配置项很重要。
- 管理网默认是否配置：默认配置；规范 inventory 在开启时要求填写管理 IP、prefix 和网关，examples 显式开启。
- 网络默认是否立即生效：默认 `true`；examples 显式设置 `apply_network_immediately: false`。
- RDMA 默认范围（full / names_only / off）：默认 `full`，examples 显式使用 `full`。
- 是否更改字段名或合并表达：不合并；三个字段分别表达配置范围和生效时机。

### B-08：bond、MTU、路由优先级及网关探测

- 当前：多数已有默认值，包括 MTU、bond 模式相关值、route_priority、探测次数和超时。
- 建议：普通模板只保留现场确实需要决定的策略；低频参数作为高级覆盖。
- 注意：bond 模式是交换机及服务器的共同规划；不能仅靠识别两张网卡自动决定 LACP。MTU 也不能仅以网卡支持上限决定。
- 最终决定：保留现状，所有参数都保留在bundle中，这些参数每个项目都可能不一样。
- 普通模板保留参数：
- 高级覆盖参数：
- 固定内置、删除外部配置的参数：

### B-09：apply 超时

- 当前：代码会补齐完整 stage 表，示例重复列出；0 在 stage 项中表示不限时。
- 建议：模板仅写覆盖项，明确总默认值与 stage 默认值优先级。
- 最终决定：所有超时选项保留不删除或隐藏。
- 是否改变 default_seconds 对已知 stage 的作用：
- 0 与未填写的语义：

### B-10：bandwidth 测试参数

- 当前：大量参数有默认值；`report_gbits` 被 ApplyDefaults 无条件设为 true；gid_index=0 被改为 3；CLI 和 TUI 还会覆盖部分参数。
- 建议：统一配置优先级，省略常规默认值；固定输出单位可考虑删除外部开关。
- 自动化候选：按目标设备/IP/RoCE 类型解析 GID index，避免把全局固定 3 视为可靠自动探测。该能力需要实现，当前不是已具备行为。
- 最终决定：改成自动化探测，优先直接读取 sysfs GID 表，支持 `show_gids` 和 `ibv_devinfo` 作为探测方式或 fallback。按目标、RDMA 设备和端口分别解析，不再把全局固定 3 作为自动结果。
- report_gbits（固定 true / 支持显式 false）：固定 true
- GID index（手工 / 自动并允许覆盖）：支持字符串 `"auto"` 或整数；`auto` 自动探测，整数作为 bundle 全局人工覆盖。
- 自动探测失败：进入 TUI，由用户查看候选项并人工选择 GID index；不自动回退到固定 3。
- duration 与 iterations 的选择规则：
- CLI/TUI/bundle 优先级：

### B-11：RDMA ping payload 与 MTU

- 当前：缺省 payload 固定为 8972；downloader 编辑 RDMA MTU 时另写 `payload_size=MTU-28`，CLI 也有相关转换。
- 建议：默认按明确的 IPv4 测试 MTU 派生 payload，消除两处手工同步；保留诊断用显式覆盖。
- 注意：检查既有网络时，bundle 的目标 MTU 不一定是当前实际 MTU，需明确测试以规划值还是现场值为准。
- 最终决定：`payload_size` 支持 `"auto"` 或整数。使用 `auto` 时按每个 bandwidth/RDMA ping 测试 stream，分别从参测两端实际 RDMA 测试接口读取 MTU，取两端最低值并计算 `MTU - 28`；例如 MTU 为 4200 和 9000 时 payload 为 4172。多 rail 或多 stream 分别计算。
- 自动读取失败或 MTU 不合法时直接报错，不静默回退到固定 payload。
- 是否保留显式 payload_size：保留，整数值用于人工覆盖；`auto` 用于现场 MTU 自动计算。

### B-12：XCCL 参数及环境变量

- 当前：很多算法参数已有默认；接口可按管理接口回退；部分拓扑信息可自动推导；environment 示例较长。
- 建议：普通模板保留启用策略、物料路径及实际业务覆盖；逐项区分环境变量的内置值、profile 值和用户覆盖。
- 注意：socket_interface 与 mpi_interface 可相同，但控制不同层，不能只因为示例相同就合并。machine_class、ranks、layout 等应按具体语义决定是否可自动化。
- 最终决定：保持现状，这个位置经常要调整。
- 普通模板保留参数：
- 环境变量默认来源：
- 用户环境覆盖优先级：
- enabled 是否继续显式配置：

### B-13：软件物料、packages、XRE 型号与 mlxconfig

- 当前：profile 模板指定物料路径；packages、container_packages、post_packages 分属不同安装流程；card_model 参与安装行为，mlxconfig.settings 表达期望设备配置。
- 建议：物料清单由 release/profile 生成，减少人工改路径；保留版本选择和型号策略的明确来源。
- 注意：不能仅凭目录中存在某个文件就自动选择最新固件或执行 mlxconfig；三个包列表也不是仅凭名称相似就可合并。
- 最终决定：保持现状，在bundle中显示配置可以提醒用户物料位置以及方便排错。
- 物料选择来源：
- 是否允许手工覆盖路径：
- card_model 的来源：
- 包列表是否重组：

### B-14：post tasks 与电源动作

- 当前：空 tasks 可省略；电源动作省略默认为 soft，而新项目向导设为 none。
- 建议：统一电源缺省行为并明确展示；是否确认与执行何种动作分别定义。不能在清理模板时改变动作。
- 最终决定：保持现状。
- 默认 action：
- confirm 是否保留及缺省值：

### B-15：旧字段及别名

- 当前：check 支持旧扁平字段及新嵌套结构，inventory 支持大量表头别名。
- 建议：只在入口兼容转换，内部统一使用一套结构；输出统一规范字段名。
- 最终决定：新版本输出只使用新结构和规范字段名；入口仍接受旧字段及旧别名，并提示弃用，内部立即转换为新结构。
- 旧字段继续接受 / 提示弃用 / 直接拒绝：接受并提示弃用。
- 同义字段同时出现时的处理：规范字段优先；旧字段仅用于补充缺失值；如果两者都填写且值相同，正常读取并提示旧字段弃用；如果两者值冲突，直接报错并指出冲突字段。该规则同时适用于 JSON 旧字段、inventory 旧表头和 `platform_options` 重复字段。

---

## 7. 模板和向导联动

### T-01：普通示例与完整参考

- 建议：普通 examples 展示常用输入；完整字段及高级覆盖放文档或专门高级示例。
- 当前 inventory 的 rail 空列、重复 hostname 等可减少；管理模式是否加入取决于 I-03。
- 最终决定：普通 inventory 使用人类可读的宽表格式，hostname 必填；不再使用 `host_id`。管理目标名称列统一命名为 `mgmt_target_name`。当前版本保留 `rdmaN_name` 列，留空时按槽位自动生成 `xgbe0`、`xgbe1`、`xgbe2`……；discover 发现到现场接口名时，写回的现场接口名优先作为下一次 apply 的目标名。普通表不展示 `rdmaN_table`、`rdmaN_route_cidr`、`rdmaN_rail_id`，这些作为高级覆盖字段保留。
- 希望普通 inventory 包含的完整表头：

```csv
hostname,mgmt_mode,mgmt_target_name,mgmt_ip,mgmt_prefix,mgmt_gateway,mgmt_iface1,mgmt_mac1,mgmt_iface2,mgmt_mac2,rdma1_name,rdma1_ip,rdma1_prefix,rdma1_gateway,rdma1_mac,rdma2_name,rdma2_ip,rdma2_prefix,rdma2_gateway,rdma2_mac,rdma3_name,rdma3_ip,rdma3_prefix,rdma3_gateway,rdma3_mac,rdma4_name,rdma4_ip,rdma4_prefix,rdma4_gateway,rdma4_mac
```

### T-02：Ubuntu / Kylin 公共模板

- 当前：两份 bundle 复制了大量相同网络、超时及 check 配置，差异主要在平台和物料。
- 建议：若采用公共配置生成，在构建或发布阶段合并并输出独立可用的 bundle，避免运行时新增隐式文件依赖。
- 最终决定：若采用公共配置生成，在构建或发布阶段合并并输出独立可用的 bundle，避免运行时新增隐式文件依赖。
- 保留两个独立手写模板 / 构建时生成 / 其他：构建时生成，但是需要根据此次变更以及精简的结果分别给出两种系统的全量参数的bundle和inventory作为examples

### T-03：downloader 向导

- 当前：新建 inventory 时逐机询问 prefix/gateway，生成逐口 name；导入时补问 mgmt_mode；编辑 MTU 时同步 ping payload。
- 建议：共用参数只询问一次，逐机询问例外；派生字段在预览中展示而非要求重复填写。
- 最终决定：downloader另设专项进行，此次不考虑，但是需要输出一个需求文档以供后续专享变更订立原则。
- 共用参数问项：
- 逐机参数问项：
- 高级问项：

### T-04：发现和写回

- 当前：discover 主要写回管理 IP、RDMA name/IP，以及表头已有的相关扩展字段；并非所有物理信息都会自动增列，也不验证目标 gateway。
- 建议：明确哪些字段属于规划、哪些属于采集结果，以及缺列时是否新增。采集当前地址不能静默覆盖用户计划的新地址。
- 最终决定：discover 不验证 gateway，只采集 hostname、IP、MAC 和接口名等现场信息。已有字段允许覆盖，但写回前必须通过 TUI 逐项或按组确认。
- 允许自动新增的列：根据现场网卡数量自动扩充 RDMA 槽位列；若现场数量少于 inventory 规划数量，先在网卡确认 TUI 之后提供删除确认界面。
- 删除确认：用户同意时才删除多余的空槽位列；用户拒绝时保留全部规划槽位及其值，不因现场暂时未发现而清空或删除。
- 允许覆盖的字段：hostname、管理/RDMA 接口名、MAC，以及现场确实发现到的 IP；inventory 中已有 IP 与现场 IP 不一致时，单独提示用户确认是否覆盖。
- 必须保留的规划字段：用户拒绝删除时保留的 RDMA 槽位，以及未被用户确认覆盖的 prefix、gateway、table、route CIDR、rail ID 等规划或高级覆盖字段。

---

## 8. 精简后示例草案

以下是精简规则的说明性片段；可直接使用的完整安装 bundle 和 inventory 以 `examples/` 中的文件为准。

### 8.1 保留网络规划、自动采集物理绑定

示例保留 hostname、管理模式、管理网络规划、逐口 RDMA 网络规划和 RDMA 目标名称；MAC 可由 discover/apply 采集，普通表省略 table、route CIDR 和 rail 字段：

```csv
hostname,mgmt_mode,mgmt_target_name,mgmt_ip,mgmt_prefix,mgmt_gateway,mgmt_iface1,mgmt_mac1,mgmt_iface2,mgmt_mac2,rdma1_name,rdma1_ip,rdma1_prefix,rdma1_gateway,rdma1_mac,rdma2_name,rdma2_ip,rdma2_prefix,rdma2_gateway,rdma2_mac
xpu11,bond,bond0,10.101.9.11,26,10.101.9.1,eth0,,eth1,,xgbe0,172.18.12.10,25,172.18.12.126,,xgbe1,172.18.12.138,25,172.18.12.254,
xpu12,bond,bond0,10.101.9.12,26,10.101.9.1,eth0,,eth1,,xgbe0,172.18.12.20,25,172.18.12.126,,xgbe1,172.18.12.148,25,172.18.12.254,
```

配套 bundle 网络片段：

```json
{
  "defaults": {
    "configure_management_network": true,
    "apply_network_immediately": false,
    "bond_mode": "802.3ad",
    "rdma_mode": "full",
    "rdma_mtu": 9000
  }
}
```

说明：这份草案依赖目标机能完成物理绑定；普通模板中的 MAC 可以为空，不意味着程序无需确认实际端口。`rdmaN_name` 当前作为可读的目标名称列保留，留空时自动使用 `xgbeN`。

### 8.2 用户期望的最终示例

最终 inventory：

```csv
hostname,mgmt_mode,mgmt_target_name,mgmt_ip,mgmt_prefix,mgmt_gateway,mgmt_iface1,mgmt_mac1,mgmt_iface2,mgmt_mac2,rdma1_name,rdma1_ip,rdma1_prefix,rdma1_gateway,rdma1_mac,rdma2_name,rdma2_ip,rdma2_prefix,rdma2_gateway,rdma2_mac,rdma3_name,rdma3_ip,rdma3_prefix,rdma3_gateway,rdma3_mac,rdma4_name,rdma4_ip,rdma4_prefix,rdma4_gateway,rdma4_mac
```

最终 bundle（完整可用版本见 Ubuntu/Kylin sample 文件）：

```json
{}
```

---

## 9. 配置解析的统一规则

以下规则是本轮实现的统一解析规则。

- 普通字段优先级：用户逐机覆盖 > 项目共用规划 > 确定性派生 > 内置默认。
- 自动探测提供现场事实，不直接替代目标规划；两者冲突应有明确处理。
- 未填写、空字符串、0、false 必须逐字段定义，不使用统一的“零值即缺省”推断。
- 规范化尽量集中，避免 downloader、runner、checker 各自实现不同默认规则。
- 输出有效值及其来源，便于确认省略参数之后到底生效了什么。

最终决定：采用本节规则。规范字段优先，旧字段仅在入口兼容层读取并告警；同义字段冲突直接报错。字段缺省、自动派生、现场发现、规划值保护和写回行为以第 5～7 节的最终决定及当前实现为准。

用户指定的规则：已纳入第 5～7 节及第 10 节验收记录。

---

## 10. 兼容、实施顺序与验收

### 10.1 兼容策略

- 已有配置是否必须无需修改即可加载：旧 bundle、旧 inventory 表头和旧 platform options 在入口兼容读取；规范输出使用新 schema。
- 是否提供旧配置转换：提供内存归一化和弃用告警；序列化时输出规范字段。
- 是否允许新版本改变缺省行为：允许本轮明确批准的行为变化，例如 hostname 必填、逐口 prefix/gateway 必填和 auto 探测；legacy provenance 保留旧配置兼容行为。
- 字段冲突时采用报错还是固定优先级：规范字段优先补充旧字段；双方同时填写且值不同直接报错，相同值允许并告警旧字段。
- 是否需要配置版本字段：本轮不增加版本字段，以入口兼容和规范序列化区分 schema。

### 10.2 建议实施顺序

1. 确认本文中的字段决定和最终示例。
2. 统一默认值、缺省语义、schema 转换及校验。
3. 修改 runner/checker/discover 的消费路径。
4. 按 [`env_init config` pipeline 方案](envinit-configure-design.md)实现配置问项、保存及预览。
5. 更新 examples、文档与对应验证。

如果只批准模板精简，则仅实施能够证明有效行为不变的模板项。

最终实施范围与顺序：已完成 schema 解析与兼容层、ResolveMachine、runner/checker/discover、examples 和离线校验；downloader 向导已移除，`env_init config` 的实现范围按 pipeline 讨论稿逐阶段确认。

### 10.3 建议验收要求

- 两个平台示例与 `examples/inventory.sample.csv` 能按最终规则通过离线项目校验。
- 模板精简项在省略前后生成相同有效配置；有意变更的默认值单独列明。
- 共用参数和逐机覆盖优先级一致。
- 单口、bond、部分物理绑定未知的规划均符合最终决定。
- `/25` 与 `.126/.254` 网关示例保持原有网络含义。
- checker 与 apply 使用一致的接口命名、槽位和 prefix 解析规则。
- discover 写回遵循规划值保护及增列决定。
- Ubuntu/Kylin 的软件源 plan 与 apply 使用同一解析结果。
- 显式 false、0、未填写及旧字段冲突按最终规则处理。
- 配置在下载机、控制机和目标机上使用时，不错误套用执行机平台。
- 与改动相关的测试通过，格式和构建检查通过。

用户新增或修改的验收要求：已合并至上方验收条目，并由相关单元测试覆盖。

---

## 11. 用户最终确认区

- 本轮批准实施的议题编号：I-01～I-07、I-09～I-11、I-13～I-14、B-02～B-06、B-10～B-11、B-15、T-01～T-02、T-04。
- 本轮明确不实施的议题编号：T-03 的向导行为改造；仅输出专项需求文档。
- 仍需讨论的议题编号：无阻塞实现的议题；新增需求另行立项。
- 对以上建议的其他修改：旧 JSON、旧 inventory 表头和 platform options 重复字段保留读取兼容并告警；规范输出只写新 schema。
- 最终示例是否为实现依据：是；examples 中的完整 bundle/inventory 是可直接校验的实现依据。

后续读取本文时，先依据本节和各项“最终决定”确定范围；发现相互冲突的规则应先讨论，不自行扩大实施范围。

## 12. Session 交接记录（2026-11-17）

### 12.1 本轮交付状态

本轮完成 inventory/bundle schema 收口、Ubuntu/Kylin examples 组合离线验证、相关用户文档同步，以及内核头文件重复配置精简。当前没有正在执行的任务或阻塞项。

- `examples/bundle.ubuntu22.sample.json` 与 `examples/bundle.kylin10sp3.sample.json` 分别保留完整平台配置，统一使用 `offline_repo`，不再生成旧 `platform_options`、管理/RDMA 接口数组及重复 prefix/gateway 默认项。
- 公共模板为 `examples/inventory.sample.csv`：30 列、2 台机器、每台 4 个 RDMA 槽位。旧 `examples/inventory.csv` 已删除；发布脚本从公共 sample 复制交付 inventory。
- 规范身份字段为必填 `hostname`；管理网使用 `mgmt_mode`、`mgmt_target_name` 和逐机地址规划；`rdma_mode=full` 时逐槽位提供 IP、prefix、gateway。
- 普通 inventory 不展开 table、route CIDR、rail ID；高级覆盖入口仍支持。
- examples 的 `gid_index`、`min_gbits`、`rdma_ping.payload_size` 使用 `"auto"`；运行时按实际目标设备/接口解析。
- `offline_repo.target_file` 和默认 entries 由平台派生，`copy_to` 可由物料目录名派生；examples 显式固定 `/opt/kunlun-apt-repo` 或 `/opt/kunlun-rpm-repo`。
- 旧字段仍通过兼容入口读取并告警；不要因源码或兼容测试中仍有旧名称而重新加入规范 examples。

### 12.2 最后追加的内核头文件精简

用户指出 `platform.kernel_headers_package`、`platform.kernel_headers_dir` 与 `packages` 中的内核头文件项看起来重复。核对实现后，已从两个 sample 删除两个空 platform 覆盖项，从 Ubuntu sample 删除 `linux-headers-{{uname_r}}`。

实际语义与实现位置：

- `internal/runner/system_ops.go` 的 `requiredPackagesWithUname` 会自动加入 `kernelHeadersPackage()` 返回的包，展开当前内核版本并去重。因此 `software` 本身即可安装内核开发包，不依赖 OFED 阶段。
- Ubuntu 默认包为 `linux-headers-{{uname_r}}`；Kylin/yum 默认包为 `kernel-devel-{{uname_r}}`。
- `kernel_headers_dir` 是 XRE 的 `KERNELDIR` 路径覆盖，不是另一个软件包。默认路径分别是 `/usr/src/linux-headers-<uname_r>` 与 `/usr/src/kernels/<uname_r>`，yum 默认值由 `internal/spec/spec.go` 补齐。
- 两个 platform 字段继续支持非标准包名和目录的高级覆盖；此次只精简 examples，没有修改运行时安装逻辑。
- `internal/runner/stage_install.go` 中 Ubuntu OFED 前置检查仍单独使用标准 `linux-headers-{{uname_r}}`，未改成消费 platform 包名覆盖。后续若处理定制内核场景，需要核对这一路径，不能把平台覆盖理解成已统一控制所有 stage。

### 12.3 验证依据与边界

- `internal/configure/examples_test.go`：使用两个真实 bundle 与公共 inventory 运行 `ValidateProject`，核对 30 列表头、2 台机器/8 个槽位、规范身份，以及与运行时 `ResolveMachine` 的结果一致性。
- 已核对管理 bond 为 `bond0`；RDMA `/25`、`.126/.254` 网关保持规划值；路由表自动为 `101..104`，route CIDR 按 IP/prefix 派生。
- `internal/bundle/examples_test.go`：校验平台、auto 参数、仓库目标文件及条目派生、旧字段缺席，并检查 sample 不再重复填写默认内核头文件配置。
- schema 收口阶段已通过 `go test ./...`、`go build ./...`、`git diff --check`。最后的内核头文件模板精简后再次通过 `go test ./...`、两个 JSON 的 `jq empty` 和 `git diff --check`；此后仅补充交接文档与 README 说明。
- 离线 examples 校验的 warning 为预期的 stage_checks 缺失、目标机物理网卡核对和 post 调优提示，没有 deprecation warning。
- 上述为离线/单元测试证据，不代表已完成 Ubuntu/Kylin 双系统、真实网卡和交换机上的安装及重启验收。

### 12.4 后续 session 的入口

1. 先阅读本文第 11～12 节，再查看两个 bundle sample 与公共 inventory。
2. `env_init config` 的 schema 编辑和保存能力留待 pipeline 各阶段确认后实现；downloader 不再生成旧向导输出。旧字段仍由公共解析兼容层按现有规则读取，规范输出以 examples 和 `internal/configure` 校验规则为准。
3. README 及网络规划/配置、NIC discovery、XCCL rail 文档已同步规范字段。本文逐项议题中的“当前/建议”含历史背景，应以“最终决定”和本节实施记录为准。
4. 当前工作区有大量跨 session 的未提交修改、未跟踪源码/文档及构建产物。没有执行 commit、push 或清理；`git diff` 不能直接视为本 session 的变更清单，提交前也需注意纳入必要的未跟踪文件。
5. 本轮修改 examples 不代表根目录 `env_init`、`envinit` 或其他构建产物已重建。交付构建流程见 [编译与发布文档](build-and-release.md)。
