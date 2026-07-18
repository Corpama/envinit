# 网卡自动发现与绑定设计

状态：已实现。公共判断模块位于 `internal/nicdetect`，apply 和 discover 分别负责本地与远端事实采集。

## 1. 目标与边界

`apply` 和 `discover` 使用同一套网卡判断引擎，避免对同一台机器产生不同的管理网/RDMA 结论。两者的数据采集方式不同，但统一转换成相同的网卡事实模型：

- `apply` 从本机 sysfs、PCI、`ethtool`、IP 和 RDMA 信息采集。
- `discover` 通过本地命令或 SSH 采集同类信息。
- 判断引擎只接收规划和网卡事实，不执行本地/远端命令，也不写文件。
- 自动判断只生成推荐；TUI 中的用户选择拥有最高优先级。

`discover` 可以在用户确认后更新和扩展 inventory。`apply` 消费 inventory 规划并配置系统，不自动改变 inventory 的槽位数量。

### 1.1 discover 的目标身份和控制地址

远端发现必须区分两个概念：

- **inventory 身份**：最终需要更新的 `host_id`、`hostname` 或既有 `mgmt_ip` 对应行。
- **控制地址**：本次执行 SSH/SCP 时实际可达的 IP 或主机名。

`--hosts` 支持以下解析方式：

- `192.168.32.11`：用该地址 SSH，读取远端静态 hostname，再匹配 inventory 的 `hostname`，其次匹配 `host_id`；没有匹配行时以 hostname 自动新增一行。
- `node1`：兼容已有行为；先匹配 inventory，有 `mgmt_ip` 时用它连接，没有时尝试直接连接 `node1`。
- `node1=192.168.32.11`：显式把控制地址绑定到 inventory 身份；左侧可以指定已有行，也可以声明要新增的 `host_id`。

直接地址得到的 hostname 无匹配时，以它同时作为新行的 `host_id` 和 `hostname`；匹配多行时必须失败，并要求使用显式映射。显式映射优先于远端 hostname；两者不一致时输出警告，但写回只能命中左侧指定行。控制地址在一次 discover 中固定不变，选出的新 `mgmt_ip` 只进入最终 inventory，不用于中途切换 SSH 连接。

## 2. 统一事实与规划模型

网卡事实至少包含：

- 当前名称、MAC、PCI BDF、driver、PCI vendor/device 型号。
- 当前速率和最大支持速率。断链导致当前速率未知时，仍使用最大支持速率参与硬件分组。
- MTU、carrier、operstate、物理端口号。
- RDMA/InfiniBand 能力、IB device、GID。
- 当前 IPv4 地址和 prefix。
- 可选的默认路由、控制连接地址等已联网证据。

进入判断模型前，discover 会从管理网和 RDMA 地址候选中统一排除 IPv4 link-local（`169.254.0.0/16`）、loopback、unspecified、multicast 和广播地址。过滤依据是地址属性，不依赖 `nodelocaldns` 等特定接口名，因此即使 link-local 地址被系统标记为 `scope global` 也不会参与绑定。

规划至少包含：

- 管理网逻辑槽位数量，以及已有 name、MAC、IP。
- RDMA 逻辑槽位 `rdma1..rdmaN`，以及已有 name、MAC、IP、prefix。

全新安装、尚未配置网络的机器不会提供当前 IP、默认路由或控制地址。这些字段缺失时不扣分，只是不参与判断。

## 3. 判断流程

### 3.1 硬绑定

按以下顺序处理确定性绑定：

1. 当前 TUI 会话中用户已经确认的选择。
2. inventory MAC 精确匹配。
3. inventory 中已存在且可确认的接口名称。
4. 当前 IP 与规划 IP 精确匹配。

硬绑定不参与后续普通评分，其他弱证据不能覆盖它。

### 3.2 硬件分组

首先按 PCI vendor/device 型号、最大支持速率和 RDMA 能力形成主要硬件组。PCI BDF、`phys_port_name` 和 `dev_port` 用于组内稳定排序。

MTU、当前地址、当前速率和链路状态作为组特征，不作为必须相等的硬分组键。这样一张未接线、MTU 尚未配置或当前速率未知的卡不会被错误排除出同型号高速网卡组。

### 3.3 规划数量匹配

优先寻找数量与规划槽位完全相同的同型号组：

- RDMA 规划四个槽位时，完整的 `4 x 400G` 同型号 RDMA-capable 组优先于混合选择。
- 管理网规划一个槽位时，RDMA 组选定后剩余的单卡组可以成为管理网强推荐。
- 管理网规划双口 bond 时，优先寻找数量为二的同型号组。

规划数量、型号和最大速率一致性优先于当前链路状态。断链卡不会因为 `Speed: Unknown` 或 carrier down 被自动丢弃。

### 3.4 分层评分

判断采用“硬约束 + 分组 + 分层评分”，不允许多个弱信号累加后覆盖强信号：

1. 用户选择、MAC、名称、IP 精确匹配：确定性证据。
2. 网卡组数量与规划数量完全匹配：强证据。
3. 型号、最大速率、RDMA 能力一致：强证据。
4. 当前地址分布与规划的管理/RDMA 地址集合吻合：中等证据。
5. MTU 形成一致组：中等证据。
6. 已联网环境中的控制地址和默认路由：管理网附加证据。
7. 当前速率、carrier 和 operstate：弱证据，只用于组内排序和提示。

`show_gids` 只证明接口具备 RDMA 能力，不直接证明它在项目中承担 RDMA 数据面角色。

### 3.5 冲突和歧义

判断结果需要包含 `exact`、`strong`、`weak`、`ambiguous` 或 `conflict` 状态，并保留简短原因。

- 五张完全相同的网卡需要分成一个管理口和四个 RDMA 口，但没有 MAC、IP或其他差异时，结果必须是 `ambiguous`，不能任选四张。
- 一个接口同时承载控制地址并具有 GID 时，结果需要显示角色冲突，不能无条件让 RDMA 覆盖管理网。
- `weak`、`ambiguous` 和 `conflict` 不能被非交互 `--yes` 静默接受。

## 4. TUI 与人工覆盖

TUI 使用统一的“逻辑槽位绑定物理网卡”模型，显示规划 IP、推荐网卡、置信度和不超过两三个要点的 `Why`：

```text
Slot   Planned IP     NIC   Confidence  Why
mgmt   192.168.32.11  eth0  strong      1x100G remaining
rdma1  25.16.2.2      eth1  strong      4x400G exact group
rdma2  25.16.2.18     eth2  strong      4x400G exact group
```

选中某项时，详情区显示型号、最大/当前速率、MTU、链路、RDMA 能力和 PCI。用户可以：

- 强制指定哪张物理卡是管理网。
- 把任意高速卡绑定到具体 `rdmaN` 规划 IP。
- 交换 RDMA 逻辑顺序。
- 覆盖程序的角色和顺序推荐。

用户选择后，该选择成为本次判断的硬绑定。程序可以重新计算未绑定槽位，但不能改回用户已确认的槽位。

仍需强制以下安全约束：

- 同一物理网卡不能绑定到多个逻辑槽位。
- 同一物理网卡不能同时承担管理网和 RDMA 角色。
- IP 不能重复。
- 必需槽位未完成绑定时不能确认。

NIC Binding Review、Network Discovery Review 和 MST Device Review 统一使用简短 `Why/Source` 风格。MST TUI 继续使用 RDMA NET/PCI 关联作为推荐依据。

## 5. inventory 动态扩容

### 5.1 discover 可以扩容

如果模板只有 `rdma1..rdma4`，但统一判断和 TUI 最终确认了六张或八张 RDMA 网卡，`discover` 应把 CSV/TSV 表头扩展到确认数量：

```text
rdma5_name,rdma5_ip,...,rdma8_name,rdma8_ip,...
```

扩容依据必须是最终确认的 RDMA 绑定数量，不能使用原始 `show_gids` 条目数或未经确认的候选数。

扩展新槽位时，优先复用 inventory 已有 RDMA 字段布局。例如模板每个槽位包含 `name、ip、prefix、gateway、mac`，新槽位也追加相同字段；如果模板还包含 `table、route_cidr`，同样复制字段结构。

只填写实际采集并确认的事实：

- name、IP、prefix、MAC 可以从机器读取后写入。
- gateway、table、route CIDR 没有可靠来源时保持空白，由已有默认规则或后续规划补充。

### 5.2 apply 不扩容

全新机器执行 `apply` 时，即使检测到八张高速卡，而 inventory 只规划四个 RDMA IP，也只能把其中四张绑定到现有 `rdma1..rdma4`。其余卡显示为未使用候选并给出提示，不能自动创建没有规划 IP 的 `rdma5..rdma8`，也不能修改 inventory。

需要使用全部八张卡时，应先补充规划，或者在网卡已配置地址后运行 `discover` 并确认扩容结果。

### 5.3 多机器和缩容

同一 inventory 中不同机器的 RDMA 数量可以不同：

- node-a 确认六张、node-b 确认八张时，公共表头扩展到 `rdma8`。
- node-a 的 `rdma7/rdma8` 保持空白，加载时不会形成虚假网卡槽位。
- 某台机器重新发现的网卡数量减少时，清空该行多余尾部槽位的值，但不删除公共表头，因为其他机器可能仍在使用这些列。

写回前 TUI/日志需要明确显示结构变化，例如：

```text
inventory RDMA slots: 4 -> 8
```

写回应使用临时文件加原子替换，避免扩展表头过程中损坏原 inventory。

## 6. 示例

机器包含一张 100G 管理卡和四张 400G RDMA 卡，其中一张 400G 卡未接线：

```text
eth0       model=A max=100G current=100G mtu=1500 link=up   rdma-capable
eth1-eth4  model=B max=400G mtu=4200                        rdma-capable
eth1       current=unknown link=down
```

规划为一个管理口和四个 RDMA 槽位时：

- `eth1..eth4` 因数量、型号和最大速率一致，形成强 RDMA 推荐；`eth1` 不因断链被排除。
- `eth0` 作为唯一剩余单卡组，形成管理网推荐。
- 已联网 discover 还可以用当前地址分布、MTU、控制地址和默认路由增强结论，但这些不是全新 apply 的前置条件。

如果五张卡完全同型号、同最大速率且没有任何已有绑定，判断返回 `ambiguous`，必须由用户在 TUI 中完成角色和逻辑顺序选择。
