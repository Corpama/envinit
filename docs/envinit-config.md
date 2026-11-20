# config：装机前配置模板准备

下载电脑上的 `downloader config` 和目标机上的 `env_init config` 共用编辑器，在本地准备 `bundle.json` 和 `inventory.csv`，无需连接 AList 或目标服务器。输出是供用户继续编辑的模板：空 hostname、IP、prefix、gateway 等正常待填项会列出清单，格式错误和已填值的明确冲突阻止保存。

## 常用命令

```bash
# 下载电脑：指向默认交付目录；若下载时指定了 --output-dir，此处使用同一路径
./env_tool_downloader-darwin-arm64 config --project ./kunlun_env_init

# 尚未下载材料，也可用本地 bundle 模板新建规划；inventory 可留空或用 --inventory-template 导入
./env_tool_downloader-darwin-arm64 config --project ./new-project --bundle-template ./bundle.json

# 当前目录的 planning/；交互终端默认 TUI
env_init config
env_init config --project ./env_tool

# 纯文本交互；EOF/中断取消，不视为确认
env_init config --project ./env_tool --plain

# 使用指定模板新建项目；已有文件默认优先
env_init config --project ./env_tool \
  --bundle-template ./examples/bundle.ubuntu22.sample.json \
  --inventory-template ./machines.xlsx --sheet Sheet1

# 校验和规范化 CSV 建议，全程不写文件、不创建锁或目录
env_init config --project ./env_tool --check-only
env_init config --bundle ./planning/bundle.json \
  --inventory ./planning/machines.xlsx --check-only

# 自动化保存；仍执行模板级校验
env_init config --project ./env_tool --yes

# 明确选择外部模板替换已有 inventory；bundle 保持原字节
env_init config --project ./env_tool --inventory-template ./new.xlsx \
  --replace --save inventory --yes

# 检查提示的事务和备份后，显式恢复，再重新运行 config
env_init config --project ./env_tool --recover
```

开发环境可用 `go run ./cmd/envinit config ...` 运行源码中的命令；仓库已有二进制需自行重新构建后才能使用新增命令。

## 路径和来源

- 默认使用当前目录 `planning/bundle.json`、`planning/inventory.csv`。
- 支持 `--project`、`--bundle`、`--inventory`。两个输出文件必须同目录；同时传 `--project` 时，显式路径必须位于该项目 `planning/`。
- `--inventory` 指向 XLSX/TSV/TXT 时，在同目录输出 `inventory.csv`；已有 CSV 优先。外部目录的表格用 `--inventory-template` 导入。
- 缺失文件优先使用显式模板，再使用 downloader `project.json` 记录的 release/profile 模板缓存；不会猜测其他 profile。缺少 inventory 来源时创建仅含管理规划表头的空模板，用户按需添加机器及 `rdmaN_*` 列。
- 已有文件默认沿用。交互运行可输入 `replace` 选择显式模板；脚本使用 `--replace --yes`。覆盖仍会备份。
- 单次只导入一份表；支持 CSV/TSV/TXT/XLSX，XLSX 默认第一张 sheet，可用 `--sheet` 指定。额外列和值保留；不读取标准输入表格。

## 编辑、预览与确认

TUI 按 **Bundle 参数表 → Inventory 规划表 → 保存预览与确认** 连续编辑。两页均显示参数名称、通俗中文说明、**当前值**和**修改为**。当前值来自加载的配置；bundle 未显式配置的字段显示“未设置”，不会把运行时派生默认值冒充当前配置。修改为一列平时留空，Enter 编辑时显示输入框；Enter 应用到内存，Esc 放弃本次输入。

Bundle 按默认网络、物料、超时时间、check、部署后分类；只有“默认网络”默认展开，其他分类一律默认折叠。作用列最多占 40 个终端字符，扩宽窗口时额外空间分给当前值和修改为。Inventory 按机器分组，默认展开待填字段，支持编辑 hostname、管理网络、RDMA 槽位及额外列。按 `a` 新增机器行，按 `+` 给表格新增一个 RDMA 槽位列组（所有机器共享表头），`Tab/Shift+Tab` 或 `]/[` 切换机器。

Inventory 页顶部提供默认折叠的“初始化”分类，按 `i` 可直接展开。可逐项设置 `mgmt_mode`、`mgmt_target_name`（管理网目标接口名）、`mgmt_prefix`、`mgmt_gateway` 和 RDMA 最大槽位数（0..64）。应用初始化时补齐管理网表头，生成 `rdma1_*` 至 `rdmaN_*` 的 name/IP/prefix/gateway/MAC/rail_id 列组；table 按槽位使用 `101、102…`，route CIDR 按 IP/prefix 自动计算，不写入普通 inventory。管理网初始值只填已有机器的空字段，并用于本次会话新增机器。已有非空逐机值和额外列保留；缩减槽位数只允许移除全空槽位，否则提示冲突。空表也能先初始化表头再添加机器。初始值最终写入机器单元格；未创建机器时保存表头，本次新增机器用的初始值不另存为配置项。

通用快捷键：方向键或 `j/k` 移动，Enter 编辑当前行或切换分类，Space 折叠/展开，右/左方向键强制展开/折叠，PgUp/PgDn 或 `Ctrl+U/Ctrl+D` 翻页，`gg` 回到第一项，`G`（Shift+G）跳到最后一项。Bundle 页按 `Ctrl+S` 进入 inventory 页；inventory 页按 `Ctrl+S` 进入保存预览，最终确认后才写文件。非输入状态下 Esc/q 取消整个流程。窗口缩放时自动调整列宽；窄屏保留分类标题、参数、当前值和输入列，底部显示选中字段的说明。`--plain` 保留来源选择、预览和确认的文本流程，输入配置通过模板或文件提供。

参数表中选中一行后按 Enter，只输入该项的值；布尔/数字直接输入，列表使用 JSON。例如：

```text
RDMA 配置模式 → names_only
配置管理网 → false
管理网 MTU → 1500
GID 索引 → auto
SSH 端口 → 22
部署后电源动作 → none
```

页面列出完整可编辑字段：管理/RDMA 策略、MTU、bond 参数、route priority、网络备份与软件源策略、check auto 参数、SSH 和 XCCL 开关、电源动作。`reset` 恢复初始来源策略，`back` 从 review 返回编辑。

平台/profile 只读。复杂材料结构、`post_tasks`、`mlxconfig` 和完整 XCCL 配置保留，通过外部文件编辑。Inventory 修改逐项复用模板校验：格式错误或明确冲突不应用，输入框保留供修正；正常空值允许应用，并显示待填项数量。额外列和值继续保留。

预览展示来源、待填项、派生 name/table/route CIDR、提示、需 review 项以及原内容/拟保存内容差异；table/route CIDR 仅作为运行时派生结果展示，不回写普通 inventory。JSON 中敏感配置组在预览中脱敏。按 Enter 进入保存对象选择（`both`、`bundle`、`inventory`），最终 review 后再按 Enter 保存。

空值表示待填，不写入 `TODO` 等假地址；空表头模板也可保存。完整部署校验不是模板保存门槛。只保存一份文件时，使用另一份原文件检查最终组合；另一份文件必须已存在。

旧 bundle 中的逐机默认规划会先迁移到 inventory，再保存规范字段。如果本次涉及此类迁移，需选择 `both` 保存，以免只保存 bundle 后丢失原规划语义。

## 保存和恢复

- 使用目录 generation 保存，保留额外普通文件、子目录和权限；拒绝符号链接及特殊文件。
- 原目录保存在同级 `.envinit-config-<id>-backup-*`，默认保留；无内容变化不新增备份。
- config 之间使用目录锁，并与 downloader 的项目锁协调。review 后检测外部编辑，发现变化要求重新加载。
- 目录通过两次 rename 切换：短暂窗口内目录可能不可见，但不会逐文件安装出混合版本。失败回滚；中断后有事务记录时阻止继续，使用相同路径参数加 `--recover` 显式恢复。已安装的新一代会另行保留供检查。
- `--check-only` 不恢复事务、不创建备份或锁，也不输出新文件。

结束页面显示模板路径、备份和待填清单。用户应继续手工编辑并补齐规划。`discover` 是系统已装好、管理网已配置且能够登录后的独立现场工具。
