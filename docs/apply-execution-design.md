# Apply 执行架构设计

> 状态：规划基线。统一 Apply TUI、无人值守 Apply 和结构化事件流尚未实现。
>
> 最近核对：2026-08-07，基于 `v0.3.15`（`main`：`2ca1366`）。

本文记录 `envinit apply` 后续演进方向，目标是同时支持两类场景：

1. 单机现场交付使用的统一 Apply TUI；
2. 百台及以上机器批量初始化使用的 fail-closed 无人值守模式。

二者必须共用同一个执行器、stage 顺序、checkpoint 语义、安全校验和最终结果格式。TUI 只负责展示和收集决策，不能成为另一套执行引擎；无人值守模式也不能绕过交互模式已有的安全检查。

## 1. 当前实现边界

### 1.1 已经实现

当前代码已经具备以下基础能力：

- 固定的 stage 顺序：`software`、`ofed`、`network`、`xre`、`xdr`、`firmware`、`container`、`mlxconfig`、`sysctl`、`kernel`、`post`；
- `plan` 与正式 `apply` 共用规划和 describe 数据；
- 默认全流程 Apply 使用 `/var/lib/envinit/apply-progress.json` 保存 checkpoint；
- checkpoint 原子写入，包含配置指纹，并在目标或配置变化时自动失效；
- 支持失败后续跑和显式 `--restart`；显式 `--stages` 不读写默认全流程 checkpoint；
- 集中的命令执行、日志和远端文件操作辅助函数；
- 独立的 NIC Binding Review 和 MST Device Review TUI；
- NIC 自动发现逻辑已经收敛到共享实现，供 Discover 与 Apply 复用；
- `network` stage 已使用两阶段临时名完成名称互换，并处理 `altname` 冲突；失败时执行回滚；
- `apply_network_immediately=false` 时可延后管理网改名和网络重载，降低带内 SSH 失联风险；
- 当前正式 Apply 在执行前要求交互式 TTY。

### 1.2 尚未实现

以下内容仍属于本文规划，不应当被理解为当前 CLI 已经支持：

- 统一的 Apply TUI；
- `--unattended` 和 `--preflight-only`；
- Runner 的结构化事件流和 JSONL 输出；
- 统一的 decision provider；
- Apply 进程锁，例如 `/var/lib/envinit/apply.lock`；
- 稳定的最终结果文件和错误分类；
- 可配置且区分 stage 风险的超时、停止和取消策略；
- 面向编排系统的稳定退出码约定。

### 1.3 为什么不能直接增加“静默开关”

当前 NIC、MST、checkpoint 重启、网络切换和电源动作的决策分散在不同流程中。如果只是关闭提示，程序可能在信息不足时默认选择网卡、MST 设备或破坏性动作，这不适合批量交付。

因此，无人值守 Apply 的语义必须是：**所有必要决策在修改系统前都能从配置和探测结果中唯一证明；只要存在歧义，就在第一次变更前失败。**

## 2. 目标架构

```text
                 +----------------------+
                 |    Shared Runner     |
                 | stages/checkpoint/   |
                 | validation/events    |
                 +----------+-----------+
                            |
             +--------------+---------------+
             |              |               |
     +-------v------+ +-----v---------+ +---v-------------+
     | Apply TUI    | | Plain adapter | | Unattended      |
     | display +    | | text output + | | config decision |
     | decisions    | | compatibility | | + JSONL/result  |
     +--------------+ +---------------+ +-----------------+
```

Runner 是唯一能够推进 stage 和写入 checkpoint 的组件。所有表现层只能：

- 订阅结构化事件；
- 提供经过校验的决策；
- 请求“当前 stage 完成后停止”；
- 展示最终结果。

不得在 TUI、普通文本模式或无人值守适配器中复制 stage 执行逻辑。

## 3. 共享 Runner 契约

### 3.1 结构化事件

事件模型至少应覆盖：

- Apply 开始、完成、失败；
- checkpoint 加载、失效、保存和清理；
- stage 的 `PENDING`、`RUNNING`、`PASS`、`FAIL`、`SKIP`、`RESUME`；
- 命令开始、结束和失败；
- stdout/stderr 数据块；
- 决策请求、候选项、依据和最终选择；
- “当前 stage 完成后停止”请求；
- 最终结果文件写入。

事件必须携带稳定的类型、时间、host、stage 和关联 ID。人类可读日志、TUI 和 JSONL 都消费同一批事件，不通过重新解析文本日志推断状态。

### 3.2 决策提供器

将分散的交互收敛为统一接口，至少覆盖：

- NIC 绑定；
- MST 设备关联；
- checkpoint 继续或重启；
- 管理网立即切换授权；
- stage 选择和依赖确认；
- 重启、关机等 post 动作。

交互式 provider 可以打开模态窗口让用户选择；无人值守 provider 只能接受配置中明确授权且通过唯一性校验的答案，否则返回安全拒绝，不得使用“第一个候选”等隐式默认值。

### 3.3 交互模式

使用单一枚举表达运行方式，不继续叠加互相影响的布尔参数：

- `interactive`：统一 Apply TUI 独占终端；
- `plain-interactive`：保留文本输出与显式提示，作为兼容模式；
- `unattended`：不依赖 TTY 或 stdin，严格 fail-closed。

### 3.4 状态、互斥和结果

- checkpoint 继续使用 `/var/lib/envinit/apply-progress.json`，并保留现有指纹失效和续跑语义；
- 增加本机 Apply 锁，避免两个编排任务同时修改同一台机器；
- 每次执行持久化最终结果，建议默认为 `/var/lib/envinit/apply-result.json`；
- 结果至少包含 schema 版本、host、配置 SHA256、起止时间、stage 结果、最后失败位置、安全拒绝原因和最终错误；
- 进程异常退出后，锁必须能够依据 PID/进程存活状态安全判定，而不是永久阻塞后续执行。

## 4. 统一 Apply TUI

### 4.1 启动流程

建议使用 `Target -> Stages -> Resume -> Review`：

1. `Target`：显示 inventory 身份、hostname、管理地址、平台、包管理器和网络后端；
2. `Stages`：选择全流程或显式 stage，并说明显式 stage 不更新全流程 checkpoint；
3. `Resume`：显示 checkpoint 指纹、已完成 stage、当前/失败 stage、最后错误和更新时间；从头执行必须明确确认；
4. `Review`：复用现有 plan/describe 数据，在用户确认后才开始修改系统。

### 4.2 执行页面

建议提供三个页面：

- `Overview`：展示全部 stage 的状态、总体进度、当前 stage 和耗时；
- `Current Stage`：展示结构化步骤、当前命令、真实可计算进度或 spinner，以及实时输出；
- `Raw Logs`：完整 stdout/stderr，支持翻页、横向滚动和错误跳转。

底部按键说明固定在终端最后一行，并随窗口尺寸自适应。主内容使用 `PgUp/PgDown` 翻页，详情窗使用 `Ctrl+U/Ctrl+D` 翻页，保持与 Check TUI 一致。

### 4.3 模态决策

统一 Apply TUI 必须成为 raw mode 和 alternate screen 的唯一所有者。现有 NIC Binding Review、MST Device Review、checkpoint 重启确认和 post 电源确认应改造成同一程序内的模态页面，不能嵌套启动多个争抢 `/dev/tty` 的 TUI。

### 4.4 安全停止

Apply 会修改系统，不能照搬 Check 的即时 abort：

- 普通退出只提供“当前 stage 完成后停止”；
- 完成或失败后可以安全退出；
- 紧急中断必须经过高风险二次确认；
- OFED/XRE 安装、固件更新、网络切换、内核配置和电源动作期间，不提供普通硬取消；
- 失败 stage 的重试必须等价于使用同一配置从 checkpoint 再次执行。

不得显示伪造百分比。只有 stage 暴露真实步骤总数时才展示确定进度，否则展示状态、当前命令、spinner 和耗时。

## 5. 无人值守 Apply

### 5.1 CLI 形态

建议命令：

```bash
sudo ./env_init apply \
  --inventory planning/inventory.csv \
  --bundle planning/bundle.json \
  --host node-001 \
  --unattended \
  --result-file /var/log/envinit/apply-result.json
```

预检命令建议使用相同参数并增加：

```bash
  --preflight-only
```

`unattended` 只表示“不提示、不启动 TUI”，不表示“不输出”。每次执行仍必须保留人类可读日志，并可选输出 JSONL 事件。

单机执行器继续只处理一个 host。批量并发、分批、SSH 传输、熔断和回滚策略由 Ansible 或其他编排系统负责，不把集群调度逻辑塞进本地 Runner。

### 5.2 修改前预检

无人值守模式必须在第一次系统变更前完成所有所选 stage 的决策和依赖校验，至少包括：

- root 权限、支持的平台、所需文件、制品、校验和、磁盘空间和命令；
- `--host` 与 inventory 中唯一目标精确匹配；
- 管理网和 RDMA 网卡能够唯一绑定，优先使用 inventory 中的 MAC；
- MST 设备能够与已确认的 RDMA 接口确定关联；
- checkpoint 所属目标和配置指纹一致；
- 管理网切换策略不会在未授权时切断控制 SSH；
- post 电源动作有明确授权；
- 选择的 stage 依赖完整；
- 本机没有另一个正在执行的 Apply。

`--preflight-only` 不得写入系统配置或 checkpoint。批量交付应先对全部节点完成预检，再开始任何节点的正式 Apply。

### 5.3 禁止猜测的决策

| 场景 | 无人值守行为 |
| --- | --- |
| NIC 候选不唯一 | 失败；要求补充 `mgmtN_mac`、`rdmaN_mac` 或其他唯一映射 |
| MST 设备关联不唯一 | 失败；不得默认选择第一项 |
| checkpoint 与目标或配置不一致 | 失败；要求显式 `--restart`，不得自动清理 |
| 管理网需要立即改名或 reload | 只有显式授权才执行；否则要求延后或失败 |
| post 要求重启/关机 | 只有专用无人值守策略允许时执行，普通 `confirm=true` 不等于授权 |
| stage 依赖不完整 | 失败并列出缺失依赖 |
| 已有 Apply 正在运行 | 失败并报告锁持有者 |

### 5.4 日志、退出码和超时

- 定义稳定的退出码类别：成功、预检失败、安全拒绝、stage 失败、内部错误；
- JSONL 和最终结果使用带版本的 schema；
- SSH 或终端断开后，完整日志仍保存在目标机；
- 不对包管理器、驱动、固件、网络、bootloader 或电源操作使用统一硬超时；
- 超时策略必须按命令或 stage 分类，并区分“停止等待”“请求终止”和“允许强杀”；
- 破坏性 stage 不自动重试，由编排系统重新执行同一命令并利用 checkpoint 续跑。

### 5.5 推荐批量流程

1. 分发带版本号的二进制、bundle 和 inventory；
2. 对所有目标执行 unattended preflight，汇总结构化结果；
3. 在任何系统变更前修正全部 inventory 和安全策略问题；
4. 先做 canary，再按受控批次执行，不一次性并发全部节点；
5. 以后置健康检查作为下一批放行条件；
6. 失败节点保留现场和 checkpoint，修正原因后继续执行，不自动从头重装。

## 6. 实施顺序

### Phase A：收敛 Runner 契约

- 清点并收敛所有直接读取 stdin、`/dev/tty` 和直接启动 TUI 的位置；
- 定义结构化事件、JSONL schema 和兼容文本适配器；
- 定义 decision provider；
- 导出只读 checkpoint 检查接口；
- 保持当前命令行为不变，先建立回归基线。

### Phase B：安全状态与预检

- 增加 Apply 锁和最终结果文件；
- 实现严格、无副作用的共享预检 API；
- 为 NIC、MST、checkpoint、网络和电源决策定义显式配置；
- 增加稳定错误分类和退出码。

### Phase C：无人值守模式

- 实现 `--preflight-only` 和 `--unattended`；
- 验证 stdin 关闭且没有 `/dev/tty` 时仍可执行；
- 增加 JSONL、断线日志、checkpoint 续跑和并发锁回归测试；
- 使用 Ansible 或等价编排器完成 canary 和分批测试。

### Phase D：统一 Apply TUI

- 实现启动流程和三个执行页面；
- 将 NIC、MST、checkpoint、网络和电源决策改为内部模态页面；
- 实现当前 stage 完成后停止和失败 stage 续跑；
- 覆盖终端 resize、窄屏、SSH 和 WebRelay 场景。

无人值守能力优先于统一 TUI 落地，但两者必须建立在同一套 Runner 契约之上。不能为了尽快支持批量执行而临时跳过现有交互检查。

## 7. 验收标准

实现完成至少应满足：

1. 相同 bundle、inventory、host 和 stage 在三种交互模式下产生一致的执行计划和 checkpoint；
2. TUI 和 JSONL 展示来自同一结构化事件，不解析文本日志还原状态；
3. 无人值守模式在 stdin 关闭、没有 TTY 时正常运行；
4. 任一必要决策不唯一时，在第一次变更前失败，并给出可操作的补充字段；
5. 同一主机并发启动第二个 Apply 时被锁拒绝；
6. 终端或 SSH 断开不丢失最终日志和结果；
7. 失败后使用相同配置可以从 checkpoint 继续，配置变化会使旧 checkpoint 明确失效；
8. NIC 名称互换、`altname` 冲突、网络切换失败和回滚均有回归覆盖；
9. 不对高风险 stage 执行无条件强杀或自动重试；
10. 当前交互式 Apply 的既有行为在迁移期间保持兼容。

## 8. 非目标

- 本地 Runner 不负责百台机器的 SSH 编排、并发调度和批次放行；
- 不在无人值守模式中提供“关闭全部安全校验”的总开关；
- 不以固定 sleep 或统一 timeout 代替 stage 的真实完成判断；
- 不在 TUI 中展示无法从执行器获得的伪进度；
- 不通过复制 Runner 逻辑实现第二套图形化 Apply。

当前正式操作方法仍以 [README](../README.md) 为准。本文描述的是下一阶段架构和验收边界；在对应功能真正落地并通过回归前，示例中的新参数不可用于生产。
