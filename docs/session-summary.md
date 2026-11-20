# 会话交接索引

2026-11-14：网络配置演进已开始落位，使用原有参数自动识别改配；事务恢复、拓扑核验、sysctl ownership 和 SSH network worker 见 [网络配置演进](network-evolution.md)。此前文档中的旧行为以该进展说明为准，实机验收仍未完成。网卡网关辅助探测已确定为默认开启、可显式关闭，详见下方本次会话交接。

整理日期：2026-10-22。范围：本会话中的网络配置、NIC 发现、重跑专项、参数设计与发布讨论。

本文档组结合会话记录与整理当日工作区源码。状态分为“源码已有”“会话方案”“待核实/验收”；源码存在不代表已提交、已发布或已部署。当前工作区存在大量已修改与未跟踪文件，不能把本次文档整理当作一次功能发布。

后续进展（2026-11-18）：downloader 配置向导已移除，downloader 恢复为纯下载器，仅负责 profile、模板、材料下载、校验、续传和交付目录组装。配置初始化改由拟新增的 `env_init config` 负责，当前以 [`env_init config` pipeline 方案](envinit-configure-design.md) 作为讨论稿，尚未作为实现依据。

本次交接补充（2026-11-11）：本会话处理了 check 的 SSH/SCP 端口参数问题，并清理了示例目录中的历史规划表，详见[发布与待办](session-release-handoff.md)的“本次会话补充”。
本 session 的配置精简专项新增现场使用背景（2026-11-12）：支持“只有网络规划表、从本机初始化”和“管理网已通、通过 SSH discover 后远程初始化/补配置”两类入口；规划目标与现场事实必须分离，详见[网络发现与规划](session-network-planning.md)。
本 session 的重跑核查新增配置演进专项（2026-11-13）：覆盖改 IP/网段/网关、bond↔单口、换卡和 RDMA 槽位改绑的现有能力与边界，详见[重复执行专项](session-rerun-acceptance.md)。

本次会话交接（2026-11-14）：网关辅助探测字段统一为 `defaults.rdma_gateway_probe`。默认配置为开启；省略字段或省略 `enabled` 都按开启处理，只有显式 `"enabled": false` 才关闭。Ubuntu/Kylin bundle 模板和 downloader 向导均已展示该配置项。探测不能形成完整唯一映射时保留 MAC/当前接口名，按 PCI/硬件顺序预填，标记 `ambiguous`、`NeedsReview=true` 和 `connectivity unverified`，并在 NIC Binding Review 顶部显示回退原因；用户可以直接按 Enter 接受预填结果。真实 IP 冲突、探测命令异常、接口清理失败等仍直接阻断。实现与限制见[网络发现与规划](session-network-planning.md)。

关闭本 session 前的后续事项：

1. 在真实 Linux 主机、实际交换机/VLAN 和三种网络后端上验收网关探测的 ARP/ICMP、临时拉起/恢复、取消恢复、IP 冲突阻断以及 PCI/硬件顺序回退；当前自动化测试不能替代实机验收。
2. 重点验证同一网段、同一网关的多张 RoCE 网卡会显示“非唯一/连通性未验证”并回退，而不会把连通性误当成端口对应关系。
3. 补充“部分 MAC 已知、其余槽位未填写”的稳定性测试，确认已有 MAC 槽位不会被重新排序、网关探测或回退逻辑覆盖。
4. 网络配置演进文档列出的三后端切换、SSH 断线后的 worker、重启持久化和换卡验收仍未完成；发布前需要保留现场日志、配置指纹和实际二进制来源。
5. 当前工作区有大量既有修改、未跟踪文件和二进制变化。本轮没有提交、推送或发布；后续提交必须按 diff 逐项选择，不能直接使用 `git add -A`。

本轮最后一次验证：`go test ./...` 通过，`git diff --check` 通过；未以此替代真实网络环境验收。

## 分类入口

| 主题 | 文档 | 用途 |
| --- | --- | --- |
| 网卡角色、接线、规划数量、主动探测 | [网络发现与规划](session-network-planning.md) | 明确自动绑定的依据和边界 |
| prefix、route CIDR、网关、QoS、麒麟路由 | [网络参数与现场问题](session-network-configuration.md) | 区分当前行为和待实施建议 |
| 全阶段重跑、备份、恢复、验收矩阵 | [重复执行专项](session-rerun-acceptance.md) | 保留原始验收目标与未完成事项 |
| 提交、远端、release、下一步交接 | [发布与待办](session-release-handoff.md) | 避免把历史检查当作当前发布事实 |

## 现有正式文档

- [现场使用手册](../README.md)：命令、bundle 字段与交付操作。
- [编译与完整发版操作手册](build-and-release.md)：env_init/downloader 手动构建、正式下载元数据、Actions/人工发布及验收恢复。
- [网卡自动发现设计](nic-discovery-design.md)：共享判断引擎、TUI、discover 写回与扩容。
- [Apply 执行架构设计](apply-execution-design.md)：统一 TUI、无人值守与结构化事件的规划基线。
- [XCCL 拓扑与 rail 匹配](xccl-topology-rail-matching.md)：独立的拓扑身份推导，不等同于物理接线或连通性验证。

## 证据与附件

会话使用过交接说明、交换机/网卡现场截图，以及《布线表-V1.3-20260831-新增GPU的MPO线及IP.xlsx》。截图说明了现场现象，但不能代替运行时路由、服务日志和交换机配置。本次没有重新解析该 Excel，也没有复制其中的端口或 IP 映射作为已确认规划。

会话曾报告本地测试、静态检查和交叉编译通过；这些属于当时验证。本次仅整理文档和核对部分源码，没有重新执行全量功能测试或实机验收。
