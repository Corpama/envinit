# 会话记录：发布、证据与后续交接

整理日期：2026-10-22；本次补充：2026-11-11、2026-11-12、2026-11-14。

操作流程更新（2026-10-23）：[编译与完整发版操作手册](build-and-release.md) 已按当前脚本整理。以下仍是历史交接和验收边界，不代表新版本已发布。

## 历史请求与当前证据

用户曾要求比较本地与 GitHub remote、确认 release 是否最新，并在备份修复后提出提交和发布要求。这些是历史请求，不足以证明当前工作区的所有后续改动已经提交或发布。

本地存在 `release-notes/v0.3.16.md`，记录集中备份修复；本次未查询 GitHub、远端分支、tags、Actions 或发布资产，因此不能据此断言 v0.3.16 是当前最新 release，或当前二进制对应这些源码。

该历史 release 说明描述备份使用同文件系统原子移动；当前工作区实现与 README 已描述复制备份及跨文件系统支持。两者属于不同阶段，不能把历史说明直接用于解释所有当前行为，也不应改写旧说明冒充当时已交付能力。

## 工作区保护

本次整理开始时已有大量修改和未跟踪源码，以及已修改的 `env_init`、未跟踪的 `env_init-linux-amd64`。这些内容不能全部假定为本次会话产物或可发布文件。

- 后续提交应按文件和 diff 确认范围，避免 `git add -A`。
- 保留用户已有二进制与无关改动；构建验证使用明确的临时输出路径。
- 发布前核对实际提交、tag、构建来源、Actions 结果、资产、manifest 和校验和，再用新下载的交付包验证。
- 发布说明只描述已交付内容，并写清实机验收边界。

## 按优先级继续

1. 根据 [重复执行专项](session-rerun-acceptance.md) 逐项对照源码和测试，验证历史备份归属判定、并发锁覆盖、配置变更回收及非幂等操作恢复。
2. 补充“部分 MAC 已知”槽位稳定性验证，避免已确认绑定被排序或探测覆盖。
3. 将 [网络参数建议](session-network-configuration.md) 作为待决策变更：取消静默 `/24`、明确网关来源，以及可选 IP/prefix 输入支持。目前仅讨论，未落地。
4. 完成三条网络后端、真实网卡/交换机、取消恢复及重启验收；记录环境、输入、期望、实际日志和结果。
5. 所有已发现重跑阻断问题处理并完成验收后，再确定发布范围和版本，整理新的 release 说明。

## 本次会话补充（2026-11-14）：RDMA 网关辅助探测

本次将 bundle 配置字段确定为 `defaults.rdma_gateway_probe`，不再使用 `rdma_probe`。默认行为是开启网关辅助探测；配置中省略该对象或省略 `enabled` 时也保持开启，现场需要关闭时使用：

```json
"rdma_gateway_probe": {
  "enabled": false
}
```

Ubuntu/Kylin 示例 bundle 已显式写入完整参数，downloader 向导也默认选择开启。网关探测不是网卡身份的唯一来源：原有 MAC、RDMA 能力、型号、速率和 PCI/硬件顺序发现仍是主路径；只有 ARP+ICMP 结果形成完整且唯一的一对一映射时才采用探测映射。不可达、非唯一、候选未全部测试或证据不完整时，程序回退到 PCI/硬件顺序，保留 MAC/当前接口名，标记连通性未验证并在 NIC Binding Review 顶部警告。回退后允许用户直接按 Enter 接受预填候选。IP 冲突、命令异常、接口清理失败和无法保证探测隔离等错误不会静默回退。

发布/现场交接需要特别注意：

- 同一网段同一网关的多张 RoCE 网卡通常会产生多对多连通性矩阵；这属于预期的非唯一回退，不是探测故障，也不能据此确认布线顺序。
- 当前已通过 `go test ./...` 和 `git diff --check`，但尚未完成真实 Linux 网卡、交换机/VLAN、三种网络后端、重启和异常中断验收。
- `session-network-planning.md` 中列出的“部分 MAC 已知”槽位稳定性仍需专项确认，尤其要验证既有 MAC 绑定不会被自动排序或探测覆盖。
- 工作区不是干净发布树，存在大量历史修改、未跟踪文件、删除文件和二进制变化。本次没有提交、推送、tag 或发布；发布前必须按文件审查 diff，并核对构建二进制与源码提交来源。

统一 Apply TUI、无人值守、结构化事件和稳定结果文件属于另一个演进主题，详见 [Apply 设计](apply-execution-design.md)。已有锁或局部超时实现不能等同于整个设计完成。

## 配置精简专项新增背景（2026-11-12）

现场有两种正式入口：

1. 开局只有网络规划表，MAC 通常未知，管理网和 RoCE/RDMA 可能分别处于未插线、已插线但交换机未配置、已完成交换机配置等状态。工具需要从本机/控制台采集硬件事实，绑定规划槽位，初始化管理网和 RoCE/RDMA，并继续执行后续 stages；没有链路或网关可达证据不能直接被解释为“不需要配置”。
2. 管理网已配置并可 SSH 登录，RoCE/RDMA 可能尚未配置 IP，也可能已有 IP 但缺少特定路由、policy rule、持久化命名或后续调优。此时使用 discover 收集 MAC、接口、RDMA 能力、链路和当前地址等事实，再由 apply 按规划目标补齐配置。

后续配置设计必须遵循：规划表保存目标状态，discover 保存/补充现场事实，人工 Review 确认物理槽位绑定。不能要求初始规划表预先拥有 MAC，也不能让 discover 读到的当前 IP、网关或接口顺序无条件覆盖已经确认的规划目标。现有 discover 对“RoCE 已插线但主机尚未配置 RoCE IP”的远程场景仍可能因没有 IPv4 `show_gids` 条目而失败，这是后续实现和验收重点。

## 本次会话补充（2026-11-11）

### SSH/SCP 端口配置

现场在分发 XCCL runtime 时出现 `scp: unknown option --`，用户随后确认在 `check.ssh.options` 中填写了 `-p 9527`。该 options 会同时传给 `ssh` 和 `scp`，但两者的短参数含义不同：`ssh` 使用小写 `-p`，`scp` 使用大写 `-P`（小写 `-p` 是保留时间戳）。若把 `"-p 9527"` 写成一个数组元素，空格也会进入参数解析；不能将 shell 命令片段当作 argv 元素使用。

已在源码中增加统一的全局字段：

```json
"check": {
  "ssh": {
    "user": "root",
    "port": 9527,
    "options": []
  }
}
```

实现位置和行为：

- `spec.CheckSSHConfig` 增加 `port`，允许 `0` 表示未设置，实际端口范围为 `1..65535`；同时兼容旧版平铺字段 `ssh_port`。
- `internal/checker/command.go` 将端口统一拼为 `-o Port=9527`，因此同时适用于 SSH、SCP 和 discover 的环境探测。
- XCCL 的 `ssh-wrapper` 也使用同一端口设置。
- `options` 中出现 `-p`、`-P`、`-p9527`、`-P9527` 等端口短参数时，`Bundle.Validate` 会在加载阶段拒绝，并提示使用 `check.ssh.port` 或 `-o Port=<port>`。
- 如果 `options` 中显式存在 `-o Port=...`，它优先于 `check.ssh.port`。

这里的“全局”指上述场景共用现有的 `check.ssh`，并未新增 bundle 顶层 `ssh` 对象。端口未设置或为 `0` 时不注入 `Port`，遵循 SSH 配置及默认端口（通常为 22）。使用 options 配置端口时推荐 `["-o", "Port=9527"]`；先前会话中关于所有带空格的 `-o` 单元素形式必然无效的解释过于绝对，不应作为参数解析规则。

涉及场景包括 `discover`、check 远程命令、SCP 物料分发和 XCCL 多机启动。真实部署机器上的 bundle 需要使用 `"port": 9527`（按现场端口填写），并重新部署包含本次代码的 `env_init` 二进制；本仓库中的示例使用端口 `22` 作为默认示例。

### 示例文件清理

- 保留 `examples/inventory.sample.csv`：它是发布脚本和 downloader 使用的公共规划模板。
- 删除 `examples/inventory.csv`：它是旧的、带有 `node1`～`node10` 数据的历史示例；当前代码、测试和发布流程不依赖它，README 中的相关引用已同步删除。
- 删除本地生成的 `dist/my-delivery-preview/`：该目录属于 downloader 验证输出，整个 `dist/` 已被 `.gitignore` 忽略，不是源码仓库内容；需要时根据 README 重新下载模板，再按 [`env_init config` pipeline 方案](envinit-configure-design.md)初始化规划。不要把缓存的 `bundle.json` 当作源码模板。
- 源码模板保留为 `examples/bundle.kylin10sp3.sample.json`、`examples/bundle.ubuntu22.sample.json`，二者均已增加 `check.ssh.port` 示例字段。

### 本次验证与交接边界

会话中执行并报告通过的检查包括：

- `go build ./...`
- `go vet ./...`
- `go test ./...`
- 两个示例 bundle 的 JSON 解析检查

上述测试是在删除旧 `examples/inventory.csv` 前完成的；删除该历史示例后未重新执行全量测试。其中 spec/checker 测试输出明确显示 `ok`，全量测试命令曾通过过滤管道输出，未独立保留 `go test` 的退出码。正式提交或发布前应直接运行 `go test ./...` 并检查退出码。本次没有非标准 SSH 端口下的实机 discover/SCP/XCCL 验收，也没有构建部署新二进制、提交或发布。

下一 session 优先根据需要确认现场 bundle 路径、构建部署新二进制，并验证端口 9527 的 discover、SCP 分发和 XCCL 多机连接。当前工作区原本就存在大量其他修改和未跟踪文件，不能把所有工作区改动都归因于本次会话，也不要直接使用 `git add -A` 提交。
