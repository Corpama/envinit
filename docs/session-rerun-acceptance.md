# 会话记录：全阶段重复执行专项

2026-11-14 实现进展：已接入网络事务、运行时核验、旧拓扑退出、sysctl ownership/恢复和 SSH 独立 network worker，详见 [网络配置演进](network-evolution.md)。下文 2026-11-13 核查结论保留为实施前记录；旧 sysctl 不清理等描述已由新实现部分解决，三后端/硬件/重启验收尚待完成。

整理日期：2026-10-22；本次补充：2026-11-13。用户已要求实施本专项；目标尚不能标记为全部完成或实机验收通过。

## 现场触发与验收目标

Kylin 重跑时在 `network-scripts` 同目录留下 `ifcfg-*.bak.<时间>`，服务再次扫描备份并尝试拉起旧配置，现场出现地址冲突和 `RTNETLINK answers: File exists`。用户要求 Ubuntu、Kylin 及其他备份路径统一修复。

“100% 可重跑”对应可验证行为：单 stage、全流程 `--restart`、失败续跑、系统重启后再跑都应配置收敛、不累计重复对象、失败可恢复，不盲目重复安装或刷写。厂商操作状态不明时暂停处理，不能视作成功。

## 工作区已有基础

| 能力 | 源码入口 | 状态边界 |
| --- | --- | --- |
| 主机执行锁 | `execution_lock.go`、`app.go` | 已有 flock；共享状态覆盖范围仍需并发验收 |
| 操作记录与完成检查 | `operation_state.go`、`vendor_state.go` | 有指纹及 verified/unknown/unverified 等状态；实际判断依赖完成检查质量 |
| 集中备份与原子替换 | `file_ops.go`、`atomic_file.go` | 当前方案支持跨文件系统备份；故障边界需要专项验证 |
| 主机绑定持久化 | `binding_state.go` | 需验证设备变化和规划指纹失配 |
| 网络文件及运行时对象所有权 | `network_ownership.go` | 需验证缩容、改名、换 IP、换后端及外部配置保护 |
| 探测恢复 | `probe_recovery_linux.go` | 当前临时修改主要是接口拉起状态，不安装临时 IP/路由 |
| 包状态检查 | `package_state.go` | 需验证版本满足、半安装及版本变化 |
| 路由重放去重 | `render_config.go` | 精确删除工具规则再添加，路由重放使用独立锁；需验证与各后端触发器协调 |

源码入口均位于 `internal/runner/`。这份表记录基础实现，不宣称全项验收完成。

## 配置和恢复要求

- 默认备份根目录 `/var/lib/envinit/backups`，保留时间及原路径层级；禁止置于会被系统扫描的配置/脚本目录。
- 内容和元数据满足时不备份、不重写；变更顺序是完成备份、准备并验证新文件、原子替换。
- 旧网络备份在加载网络前处理。只有能确认归属的工具备份可迁出；来源不明但可能被加载的文件应列出要求处理。仅符合时间戳命名不应被视为足够的归属证明，需专项复核现有识别逻辑。
- 只回收工具拥有且身份匹配的文件、地址、规则和路由，不整表 flush，不覆盖外部新增配置。
- `--restart` 重新检查阶段，不删除未恢复事务，不绕过安装或固件状态判断。
- 正常退出、失败、取消与下次启动恢复均要覆盖；恢复失败必须阻断。

## 本次核查：重复执行与错误目标

本次对“正常重跑”和“错误指定 `--host/--hosts`”分别核对源码。当前结论是：正常重跑已有基础保护，但不能把这些保护解释为“错误目标也会被阻止”。

### 已覆盖的重复执行路径

| 检查项 | 当前行为 | 结论 |
| --- | --- | --- |
| 同一台机器同时执行两个 apply | 非 dry-run 的 apply 获取 `/var/lib/envinit/apply.lock`，第二个进程失败 | 已有并发互斥；需要实机确认锁文件目录和异常退出后的恢复 |
| 全流程失败后继续执行 | `apply-progress.json` 保存已完成阶段和失败阶段，下一次 `--stages all` 从未完成阶段继续 | 已有阶段续跑；阶段完成后进度写盘失败时会报错，不静默继续 |
| 正常完成后再次执行全流程 | 同一 `HostID` 和 resolved configuration fingerprint 下跳过已完成阶段 | 已覆盖；使用 `--restart` 会有意清除进度并重新走全流程 |
| 配置发生变化后再次执行 | inventory 源记录或 bundle 变化会改变 fingerprint，旧进度不复用 | 已覆盖；需继续验证运行时生成字段不会造成无意义 fingerprint 变化 |
| 单 stage 重跑 | 显式单 stage 不使用全流程 checkpoint | 语义明确，但必须依赖各 stage 自身幂等 |
| 文件配置重复写入 | 内容和元数据未变化时不重写、不生成备份；变更先备份再原子替换 | 已有基础保护 |
| 旧网络对象回收 | network manifest 只回收仍能证明由 envinit 拥有的文件、规则和路由；外部修改会阻断 | 已有归属保护；仍需三种网络后端实机验收 |
| 驱动/固件等非幂等操作 | `running`、`unknown`、`unverified` 且未通过完成检查时阻止盲目重放 | 已有恢复闸门；完成检查本身必须足够强 |

### 部分覆盖或仍有边界的重复执行路径

1. `apply-progress.json` 只绑定 `HostID` 和配置 fingerprint，不绑定当前机器的管理 MAC、全部物理 MAC、机器序列号或可信硬件身份。若把相同 host 记录、相同 bundle 和已有 checkpoint 搬到另一台机器，理论上可能复用阶段进度；当前没有跨机器复用 checkpoint 的阻断测试。
2. checkpoint 是“阶段调用完成”的记录，不是每个阶段目标状态的完整证明。正常同配置重跑会跳过已完成阶段，因此可以避免重复执行，但不能自动修复外部后来改坏的配置；这类漂移需要显式 `--restart` 或指定 stage。网络、udev、sysctl、kernel 等依赖各自的差异检查；进程在实际变更完成、进度落盘前崩溃时，下一次可能再次调用该阶段。对幂等阶段通常可收敛，对非幂等操作依赖 operation state 和完成检查。
3. `--restart` 是显式的强制全流程重跑入口，不应作为普通恢复手段。它不会绕过非幂等操作的 `unknown/unverified` 闸门，但会重新执行所有可执行阶段，必须继续验证不会重复添加规则、路由、备份或服务对象。
4. apply 的执行锁只覆盖 apply，不覆盖 discover 对同一 inventory 的并发读改写；两个 discover 同时写回时，当前原子替换能避免半文件，但没有 inventory 级互斥或版本冲突检查，存在后写覆盖先写的风险。

## 配置演进：网络改配与物理网卡变更

服务器生命周期中可能出现以下变更：

- 管理网或各个 RDMA 口更换 IP、prefix、网关，甚至整体迁移到新网段；
- 管理网从 bond 改为单口，或从单口改为 bond；
- bond 成员增加、减少或更换；
- 物理网卡更换，或 `rdmaN` 从物理口 A 改绑定到物理口 B；
- RDMA 槽位数量减少、增加或重新排序；
- 网络后端从 netplan/networkd 切换到 legacy network-scripts 或 NetworkManager，反之亦然。

这类变更不能只看“是否重复执行”，而要看新旧目标的归属、绑定和阶段依赖。当前实现的覆盖情况如下：

| 变更 | 当前覆盖 | 需要注意 |
| --- | --- | --- |
| 管理/RDMA IP、prefix、网关改变 | network stage 按新 inventory/bundle 渲染文件；配置 fingerprint 变化后全流程 checkpoint 不复用 | 旧运行时路由只有在旧 `network-files.json` 中有准确 ownership 时才会删除；管理网连接可能立即中断 |
| 整体切换新网段 | 新地址、prefix、直连 CIDR 和网关会生成新配置；旧 manifest 中的运行时路由会按旧对象清理 | 未被 envinit manifest 记录的外部旧路由不会被猜测删除；需要现场检查主路由和交换机配置 |
| bond 改单口 | 新配置只生成单口文件；旧 envinit 管理的 bond 文件和成员文件会按 manifest 回收 | 需要确认网络后端已加载新配置；远程执行时管理连接可能断开，建议使用本地控制台、带外或维护窗口 |
| 单口改 bond | 新配置生成 bond 和成员文件，`mgmt_mode`/成员数量变化会使绑定状态失效并重新 Review | 两个成员必须有明确且不冲突的物理绑定，不能自动把一张卡降级为单成员 bond |
| bond 成员变化 | 新旧 managed 文件会按路径和内容归属处理，新的成员通过 MAC/名称/Review 绑定 | 外部修改过的旧文件不会被强行覆盖或删除，会阻断并要求人工处理 |
| RDMA 从 A 口换到 B 口 | 修改 `rdmaN_mac` 或接口规划后，binding fingerprint 变化，按新 MAC/PCI/接口重新确认；udev 和网络文件按新绑定重写 | 如果 MAC 未提供且名称/硬件分组仍有歧义，必须进入 TUI Review；不能仅凭排序把 B 口当成 A 口 |
| 物理网卡更换 | 已保存 binding 会因配置 fingerprint 或当前 MAC/PCI 不匹配而失效，重新发现/确认；MST selection 也校验 NET/PCI | 需要核对新卡是否具备 RDMA、MST 设备和目标速率；固件/驱动状态不能仅凭旧卡记录继承 |
| RDMA 槽位减少 | 当前网络 manifest 会尝试回收旧 envinit 文件和运行时对象；udev/selected interfaces/post 脚本按当前槽位重写 | sysctl 按 ownership 和物理 MAC 回收；历史 ownership 不足时保留旧 `rdmaN` 参数并打印人工确认提示 |
| RDMA 槽位增加或重新排序 | 新槽位从新规划生成；已有 MAC 的槽位保持逻辑槽位绑定，Review 可确认交换顺序 | 必须同时核对 IP、gateway、route table、rail 和 XCCL 拓扑语义，不能只依赖接口名字变化 |
| network backend 切换 | network manifest 支持对旧 backend 的 envinit 文件做归属回收，再生成新 backend 文件；服务切换有显式处理 | 三种后端、重载和重启仍需要实机验收；未知 `.bak*` 文件会阻断，避免被系统重新加载 |

### 这些变更与 stages 的关系

不是每一种网络改动都需要重新执行所有 stages，但在配置发生变化后使用全流程 apply 时，当前 checkpoint 会失效，阶段顺序会重新走一遍；各阶段再根据目标状态决定实际是否变更：

| stage | 网络演进时的作用 |
| --- | --- |
| `network` | 写入新管理/RDMA 地址、prefix、网关、路由、policy rule、后端配置；清理有 ownership 证据的旧文件和运行时对象；处理当前启动的接口临时改名与网络加载 |
| `udev` | 按新的 MAC→逻辑接口绑定重写持久化命名规则；当前网络 stage 已包含该动作，单独运行 `udev` 时才需要显式选择它 |
| `mlxconfig` | 与 IP/网关无直接关系，但物理卡替换、RDMA 槽位或 PCI 映射变化时会重新校验 MST/NET/PCI 选择，并按设备当前值设置目标值 |
| `sysctl` | 按当前 RDMA 接口名维护接口级参数；接口名称或槽位变化会生成新参数。仅在 ownership、原运行值和当前 envinit 值证据充分时回收旧接口参数 |
| `kernel` | 维护固定的 GRUB 内核参数，与 IP、网关和接口槽位通常无关；配置变化触发全流程时会重新检查，已满足则跳过 `update-grub` |
| `post` | 按当前 RDMA 接口名重写 post-boot 脚本并在脚本变化时 reload/restart 服务；物理卡更换后需要验证新卡的 `ethtool`、PCI、`mlxreg` 和 sysfs 能力 |

因此，建议把变更分成两类执行：

1. **仅地址/网段/网关变化：** 至少执行 `network`；如果接口名、MAC、bond 模式或 RDMA 槽位没有变化，`udev`、`mlxconfig`、`sysctl`、`kernel`、`post` 通常会检查后跳过或保持满足。
2. **接口绑定、物理网卡、bond 模式或 RDMA 槽位变化：** 执行 `network,udev,mlxconfig,sysctl,kernel,post`，或在维护窗口执行 `--stages all`，并完成重新 Review、重启后检查和 `check` 验收。

### 当前不能承诺自动完成的边界

- 管理网改 IP、网段或 bond 结构可能使当前 SSH 会话中断；apply 本身是本地执行模型，远程场景需要保持带外/控制台/备用管理路径。
- 旧配置只有在 envinit 的 manifest、文件内容和 runtime ownership 都能证明归属时才会清理；工具不会为了“看起来像旧配置”而 flush 全部地址、路由或 policy rule。
- 缺少历史 ownership 的旧接口 `sysctl` 参数会保留在 `/etc/sysctl.conf`，并在 plan/apply 中提示人工确认；不能根据参数名称或相同值猜测归属。
- `post_tasks` 是用户自定义动作，只有 copy/mkdir/move/remove 的文件语义和带 `check_command` 的命令具备可判断的重跑基础；没有完成检查的命令不会因为网络改配而自动安全重放。
- `post_power_action` 属于有副作用的操作。网络改配触发 `post` 时，不能把 `reboot`/`power_cycle` 当作普通幂等动作；必须经过明确确认和状态处理。

### 推荐的网络改配流程

```text
1. 备份并修改 inventory/bundle 中的目标规划
2. 如果是远程机器，先确认带外或备用管理路径可用
3. 使用 plan 检查新旧接口、MAC、bond、IP、网关、路由和待写文件
4. 对换卡、换槽位、bond 成员变化执行 NIC Binding Review
5. 执行 network；涉及物理绑定时同时执行 udev、mlxconfig、sysctl、kernel、post
6. 网络恢复后重启或按后端要求 reload
7. 检查新接口/MAC/IP/路由/policy rule/udev/MST/调优状态
8. 执行 discover/check，记录新 inventory 事实与连通性验收结果
```

### 当前没有兜底的错误 `apply --host`

`internal/runner/machine_match.go` 在显式提供 `--host` 时，只检查参数是否匹配 inventory 的 `host_id`、`hostname`、`mgmt_ip` 或记录中的 MAC，然后直接返回该行。它没有继续核对：

- 当前系统 hostname 是否与该行对应；
- 当前管理 IP 或 RDMA IP 是否与该行对应；
- 当前物理 MAC 是否至少命中该行已知 MAC；
- 当前机器是否具有该行规划的硬件身份。

因此，部署人员在目标机上误执行另一台机器的合法 `--host` 时，当前程序可能把另一台机器的网络和 hostname 规划应用到本机。已有的 `apply-progress` fingerprint 不能阻止这个问题：它最多会因为配置不同而从头执行，不能证明目标机器身份正确。`ensureHostname` 甚至可能随后把本机 hostname 改成错误 inventory 行的 hostname。

这属于 P0 安全缺口，不能用“已有 TTY”“已有执行锁”或“已有 checkpoint”替代。建议增加 apply 前的目标身份闸门：至少使用本机 hostname、当前 IP、已知 MAC 中的可用证据进行交叉核对；证据不足时进入明确的首次初始化确认流程或要求显式的一次性强制选项，并把确认结果记录到主机绑定状态。不能把任意 `--host` 当作身份证明。

另外，inventory loader 当前没有对重复 `host_id`、重复 hostname 或重复管理 IP 做全局唯一性校验；显式 `--host` 命中重复值时可能按文件顺序选中第一行。重复身份应在加载或目标解析阶段直接阻断。

### 当前没有兜底或只告警的错误 `discover --hosts`

1. `node1=endpoint` 的显式映射会固定写回 `node1` 对应行；如果远端实际 hostname 是另一台机器，当前只输出 warning 并继续。这保护了“用户明确要求写哪一行”的映射一致性，但没有保护“endpoint 确实是 node1”这一机器身份事实。对于生产模式，建议默认阻断 hostname 不一致，另提供明确的人工确认选项。
2. 普通 `check` 的 hostname mismatch 当前也是 warning 后继续，因此不能作为错误目标阻断。
3. `ResolveDiscoveryTargets` 按控制地址去重。两个不同 inventory 身份被错误地指定到同一个 SSH 地址时，当前会静默丢弃后一个目标，而不是报告身份冲突；这会导致预期的重复目标没有执行、写回或验收。相同身份的重复输入可以去重，不同身份共享控制地址应报错。
4. discover 写回使用原子替换，并已有重复行冲突保护；显式身份写回主要按稳定的 inventory identity 命中，已有测试证明 hostname 不一致不会改写另一行。但普通 inventory 匹配目标的 key 集合还包含可变的 `Address`/发现结果，且没有 inventory 文件锁或版本检查；管理 IP 变化和并发 discover 组合时仍应改为稳定行身份优先并增加冲突保护。
5. 当前 `discover` 发现到的接口/IP 结果可能更新 inventory 的网络字段。对于本专项的规划目标/现场事实分离要求，发现事实不能无条件覆盖已确认的目标 IP、prefix、网关和槽位；这一点与重跑安全直接相关，尤其是第一次发现到错误网卡时。

### 建议的阻断优先级

**P0：**

- apply 显式 host 的本机身份交叉核对，证据不足时不得静默执行；
- inventory 中 `host_id`、hostname、管理 IP 的重复值阻断；
- 不同 inventory 身份共享同一 discover 控制地址时阻断；
- 显式 discover 身份与远端 hostname 不一致时默认阻断，人工确认必须显式记录。

**P1：**

- checkpoint 增加机器身份指纹或绑定凭据，防止跨机器复制后复用；
- discover inventory 写回增加文件锁/版本校验，并只使用稳定 inventory identity 映射行；
- 将“规划目标”和“发现事实”分开存储或至少按字段来源保护，禁止事实覆盖已确认目标。

**P2：**

- 为每个阶段记录目标状态摘要和验证结果，而不仅是 completed stage；
- 增加错误 host、重复控制地址、hostname 不一致、跨机器 checkpoint 复制、并发 discover 写回的回归测试和实机验收记录。

## 各阶段的收敛要求

| 阶段 | 验收标准 |
| --- | --- |
| software / container | 查询安装状态与版本，满足则跳过；半安装进入明确修复流程 |
| ofed / xre / xdr | 结合材料摘要、版本、内核、模块和设备状态判断；未知暂停 |
| firmware | 读取设备身份与实际版本；目标或中断结果不明时禁止自动重刷 |
| mlxconfig | 分开判断当前值和下次启动值；已写入但待重启时报告待生效 |
| network / udev / sysctl / kernel | 去重、差异更新、失败恢复；无变化不做无必要的重载或重建 |
| post | 逐项管理文件、目录、服务；自定义命令可配置完成检查；未知状态暂停；电源动作单独记录确认 |

`stage_checks` 和 `post_tasks.check_command` 已有接口。没有完成检查的厂商操作执行后可能记录为未验证，后续重跑要求处理。不能只凭上次退出码或“文件存在”证明安装、内核模块和固件目标已满足。

用户期望统一结果为“已满足、已变更、待重启、未验证、需要处理”。完整展示一致性仍需核实。

## 必须保留的验收矩阵

| 类别 | 场景与通过条件 |
| --- | --- |
| 三条后端 | Ubuntu netplan/networkd、Kylin legacy network、Kylin NetworkManager；首次配置后连续执行三次、重载网络、机器重启再执行 |
| 探测接线 | 正确/交叉接线、网关禁 ping、多个口同网段、不可达、断链、IP 冲突、已有业务接口；各结果与阻断/人工流程匹配 |
| 流量隔离 | 管理网或其他口可到网关时，不能使错误候选探测通过 |
| 中断恢复 | 取消、强制终止及下次执行后，无本轮临时对象残留，原接口状态正确恢复 |
| 故障注入 | 每个文件替换、探测修改边界失败后，旧配置可用或明确提示恢复步骤 |
| 配置演进 | 改名、改 IP、减少接口、切换后端、历史备份；旧工具对象回收，非工具配置保留 |
| 并发与重复 | 同时 apply/单 stage/路由重放，不竞争共享状态；工具规则每条仅一份 |
| 非幂等操作 | 厂商驱动、固件及自定义 post 中断，使用可控模拟验证决策，再到对应硬件验证状态读取 |
| MAC 混合规划 | 部分明确 MAC、部分空白，已绑定槽位不被排序或探测覆盖 |

会话历史报告 `go test ./...`、`go vet ./...`、Linux 构建和格式检查通过；当时 Podman 环境未能完成预期启动，未完成双系统、交换机及重启验收。模拟通过不能替代实机签收。

原定实施顺序：执行锁与恢复基础 → 配置/备份 → 各 stage 收敛 → 主动探测 → 双系统实机验收 → 整理正式发布说明。
