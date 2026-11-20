# 会话记录：网络参数与现场问题

整理日期：2026-10-22；本次补充：2026-11-12。本页区分源码当前行为与讨论建议，不改变配置契约。

## 现场输入与配置来源

网络配置需要支持两类现场状态：

1. 只有网络规划表，目标机可能没有 RoCE 链路、没有交换机配置或没有任何当前 IP；
2. 管理网已经可以 SSH，RoCE 可能尚未配置，或已经有 IP 但缺少路由和调优。

规划表中的 IP、prefix、网关和逻辑槽位是**目标状态**。目标机当前采集到的 MAC、接口名、carrier、当前 IP 和 RDMA device 是**现场事实**。discover 的职责是收集和核对事实，apply 的职责是按目标状态初始化或补齐配置；不能因为当前事实为空，就把规划目标当作不存在，也不能把当前事实未经核对直接写成新的目标。

特别是 RoCE 口未插线、交换机未配置或尚未有当前 RoCE IP 时，仍可以根据规划继续建立主机侧配置，但物理连通性和交换机侧状态必须标记为未验证，并在后续链路/交换机就绪后重新验收。

## 后期网络改配与配置演进

inventory/bundle 描述的是可持续演进的目标状态，不仅用于首次初始化。后期可以修改管理网或各个 RDMA 口的 IP、prefix、网关、网段、bond 模式、bond 成员、物理 MAC 和 `rdmaN` 槽位绑定。network stage 会根据新目标重写 envinit 管理的配置，并依据 network manifest 回收能够确认归属的旧文件、路由和 policy rule。

其中，地址/网关变化主要属于 `network`；物理网卡替换、RDMA 从 A 口切换到 B 口、bond 成员变化或槽位重排还会影响 `udev`、MST/`mlxconfig`、接口级 `sysctl` 和 post-boot RDMA 调优。配置变更会改变 apply fingerprint；全流程 `--stages all` 不会继续沿用旧 checkpoint，而是重新检查各阶段，满足目标的阶段会自行跳过。

管理网改 IP、改网段或 bond/单口模式可能立即中断 SSH，因此远程改配必须预先准备带外、控制台或备用管理路径。当前 apply 的实际修改仍在目标机本地完成，不能依赖控制连接在网络切换后保持不变。

旧对象的清理遵循 ownership 保护：manifest 能证明由 envinit 生成且内容未被外部修改的文件、路由和规则才会被回收；外部修改、来源不明的备份或未被 manifest 记录的旧对象不会被猜测删除，必要时会阻断并要求人工处理。sysctl 现在记录受管 key、原始行、原运行值和物理 MAC；历史 ownership 缺失时同名旧 key 只保留并提示人工确认，不会仅凭相同模板值自动纳管或删除。

## prefix 与 route CIDR

两者含义不同：`rdma_prefix` 是地址的前缀长度，如整数 `25`；`rdma_route_cidr` 是完整路由目标网段，如字符串 `172.18.12.0/25`，不能拿裸数字 `24` 代替。

当前源码行为（`internal/spec/spec.go`、`internal/runner/render_config.go`）：

1. 规范 inventory 逐口提供 `rdmaN_prefix` 和 `rdmaN_gateway`；它们不从 bundle defaults 回退。
2. 旧 inventory/bundle 兼容入口保留历史默认行为并输出弃用提示。
3. route CIDR 省略、为空或为 `auto` 时，按最终 IP/prefix 计算直连网段。
4. 显式逐口 route CIDR 优先于自动派生结果。

例如 `172.18.12.10/25` 推导为 `172.18.12.0/25`。IP 本身不能唯一决定掩码，网关 ping 成功也不足以反推出规划掩码。

用户明确纠正：现场不一定使用 `/24`，因此不能无条件从模板删除 prefix 后依赖隐式 `/24`。

## 本轮实施后的 prefix 与 gateway 规则

- `rdmaN_prefix`、`rdmaN_gateway` 是规范 inventory 逐口规划字段；`rdma_mode=full` 时参与配置的槽位必须填写 IP、prefix 和 gateway。
- 管理网启用时，inventory 必须填写 `mgmt_ip`、`mgmt_prefix` 和 `mgmt_gateway`；规范 inventory 不再静默回退到 `/24`、`/26` 或把网关最后一段猜成 `.1`。
- 普通模板可以省略 `rdmaN_route_cidr`，由确定后的 IP/prefix 自动生成；显式高级覆盖优先。
- `rdmaN_ip` 仍是独立 IPv4 字段，不把 `IP/prefix` 混写格式作为新的输入契约。
- 旧 inventory/bundle 通过兼容入口读取时保留旧默认和派生行为，并输出弃用提示；canonical inventory 不依赖 bundle 的管理/RDMA接口、prefix 或 gateway defaults。

discover 采集现场 hostname、IP、MAC 和接口名，经过确认后写回；它不替代 inventory 中的 prefix、gateway、table、route CIDR 和 rail ID 等规划值。

## 麒麟策略路由争议

截图中的 `rule-xgbe1` 内容是规则参数，而不是独立 shell 命令，例如：

```text
from all oif xgbe1 table 101 priority 32761
from 40.1.1.11 table 101 priority 32761
```

不能仅凭其中的 `oif` 判断文件错误，也不能因 route 文件看着正确就认定规则已加载。需检查目标 Kylin 的 network-scripts 是否加载 `rule-*`、实际 `ip rule`、表 101 路由及 `network.service` 日志。Ubuntu 路径正常不等于已验证 Kylin 的加载链。

用户随后表示该问题暂不继续，转向网卡发现；未收集到足以确认现场失败原因的完整运行时证据。不要在后续交接中把这一讨论写成已确认语法缺陷。

## post 开机固化的 QoS 与硬件调优

用户要求检查 post 写入的自启动服务，不能只看 bundle 的显式设置。源码 `renderPostBootScript` 的确包含固定配置：

| 项目 | 当前生成脚本的行为 |
| --- | --- |
| CNP DSCP | `CNP_DSCP=48`，写入 `ecn/roce_np/cnp_dscp` |
| RoCE 优先级 | `RDMA_PRIO=5`，启用对应 NP/RP ECN 路径 |
| RoCE adaptive routing | `mlxreg` 设置 `adaptive_routing_forced_en=0x1` |
| ring buffer | 尝试设置 RX/TX 为 8192 |
| PCIe | 尝试禁用 ACSCtl，MaxReadReq 设置为 4096 |

生成目标为 `/usr/local/sbin/kunlun-post-boot.sh` 和 `kunlun-post-boot.service`。具体分支可能因命令、接口或 sysfs 路径不可用而跳过，脚本存在不代表每项都成功。

这里的 CNP DSCP 不能等同于所有 RDMA 数据包的 DSCP；ECN NP/RP 开关也不能等同于 PFC 配置。主机 adaptive routing 开关不能证明交换机 NSLB-S+ 已配置。交换机 PFC、DSCP 到优先级/队列映射及 NSLB-S+ 仍需交换机侧证据与实测。
