# `env_init config` 配置初始化 Pipeline 方案与决策记录

状态：用户已授权开始实施，源码已接入第一版 `env_init config`。使用和参数说明见 [config 模板准备](envinit-config.md)。各模块“本模块最终决定”保留设计选择；未勾选候选不作为实现要求。

## 1. 目标与边界

建议新增命令：

```bash
env_init config
```

**用户明确的使用场景**：目标服务器尚未装机时，`config` 用于产出配置模板，用户之后还会自行编辑、补充和修改配置文件。模板产出不等于现场配置已经完整，也不等于可以立即部署。

产物为：

```text
planning/
├── bundle.json
└── inventory.csv
```

`env_init config` 的候选职责：

- 从 downloader 交付目录或用户指定的模板建立配置文件；
- 引导用户设置项目级策略；
- 准备 inventory 模板，或导入已有规划；逐机内容通过 TUI 参数表编辑；
- 展示派生值、字段来源、warning 和待现场确认项；
- 复用公共解析与校验，区分模板格式问题、待填写内容和部署就绪问题，允许正常待填模板；
- 以事务方式保存规范化的 `bundle.json` 与 `inventory.csv`。

明确的职责边界：

```text
downloader
  -> profile 选择、模板下载、材料下载、校验、续传、交付目录组装

env_init config
  -> 装机前生成本地配置模板，交给用户继续编辑和补充

env_init discover
  -> 系统已装好、管理网已配置且能够登录后，发现现场事实、review、inventory 写回

env_init plan/apply/check
  -> 消费同一套 bundle/inventory，分别预览、执行和验证
```

`config` 在模板准备环境中运行，不依赖目标服务器在线、系统已安装或管理网可达。用户可在拿到模板后继续填写 IP/prefix/gateway/槽位等规划。`discover` 则要求系统已安装、管理网已配置，能够通过 SSH 或其他适用方式登录。两者服务于不同准备状态，`discover` 不是 config 的必经下一步。

```text
装机前准备：模板来源 -> config 产出模板 -> 用户自行编辑/补充 -> 后续部署准备
已装机现场：系统和管理网就绪、能够登录 -> discover -> review 现场信息
```

未来在现场条件满足后可以使用同一套配置，但不因此把两个场景串成自动执行链路。

## 2. 推荐的总体 Pipeline

```text
P0 项目路径确认
  -> P1 配置来源选择
  -> P2 bundle schema 检查
  -> P3 profile / 目标平台确认
  -> P4 项目级策略
  -> P5 inventory 来源选择
  -> P6 inventory 模板导入与字段检查
  -> P7 高级字段处理
  -> P8 规划与现场事实标记
  -> P9 规范化与派生值预览
  -> P10 统一校验
  -> P11 review / 用户确认
  -> P12 事务保存
  -> P13 discover 场景边界说明（独立场景）
  -> P14 模板交付与后续编辑说明
```

建议每个阶段遵循同一执行约定：

1. 读取阶段输入并显示来源；
2. 对可编辑字段提供文件中的当前值和逐项输入框；未显式配置时标记“未设置”；
3. 对派生值显示计算依据，不静默覆盖用户值；
4. 阶段结束时展示变更摘要；
5. 发生阻断错误时停在当前阶段，允许返回修改；
6. 只有最终 review 确认后才写入规划文件。

## 3. Pipeline 阶段逐项讨论

每个模块单独确认：勾选项及“本模块最终决定”记录已确认范围，未勾选项保留作为讨论记录。模块正文与早期候选冲突时，以最新最终决定为准。不同模块可以选择不同方案；本文记录设计决定，不表示功能已经实现。

### P0：项目路径确认

**目标**：确定 `bundle.json`、`inventory.csv`、模板和输出目录，避免路径来源互相覆盖。

**输入**：命令行参数、当前工作目录、可选项目目录。

**候选行为**：

- `--bundle` 和 `--inventory` 作为明确的文件级入口；
- `--project DIR` 便利地补全 `DIR/planning/bundle.json` 和 `DIR/planning/inventory.csv`；
- `--output-dir DIR` 仅用于新建配置时指定输出位置；
- 未指定路径时，在当前目录查找 `planning/`；
- 文件不存在时进入模板选择，而不是自动覆盖同名文件。

**输出**：解析后的项目路径、输入文件状态、待创建文件列表。

**依赖**：项目目录保护、路径规范化、锁文件策略。

**失败处理**：路径为空、目录不可写、路径穿越或父目录为符号链接时停止；不得创建半成品 planning。

**用户确认**：进入下一阶段前确认路径；已有文件必须显示“编辑已有配置”而不是“新建”。

**用户决定（本模块最终实现）**：

请从以下方案中选择一个，也可以组合后补充规则：

- [x] **A（推荐）**：支持 `--project`、`--bundle`、`--inventory`；没有参数时使用当前目录下的 `planning/`；同时提供多个路径来源时要求它们解析到同一项目。
- [ ] **B**：只支持明确的 `--bundle` 和 `--inventory`，不提供 `--project` 和默认路径。
- [ ] **C**：以 `--project` 为主，文件级参数只作为覆盖项；冲突时由命令行文件参数优先。

**本模块最终决定**：采用 **A：三种入口**。支持 `--project`、`--bundle`、`--inventory`；无参数时使用当前目录下的 `planning/`；同时提供多个路径来源时要求它们解析到同一项目。

### P1：配置来源选择

**目标**：决定从已有配置、profile 模板、单独 bundle 模板或 inventory 文件开始。

**输入**：已有 planning 文件、`--bundle-template`、`--inventory-template`、`--inventory` 指定的 CSV/TSV/TXT/XLSX。

**候选来源优先级**：

1. 已有目标文件作为当前配置；
2. 缺失文件由显式模板补齐；
3. 没有显式模板时使用 downloader 已下载的模板缓存或交付目录模板；
4. 不建议 `env_init` 内置随版本变化的 profile 副本。

**输出**：每个文件的来源、原始格式、是否新建、是否需要转换。

**依赖**：`bundle.Load`、`inventory.Load`、模板格式识别。

**失败处理**：来源不明确、多个来源冲突或模板无法读取时停止并要求选择；不静默选择较新的模板覆盖已有规划。

**用户确认**：显示“沿用 / 导入 / 从模板新建 / 只校验”四种入口。

**用户决定（本模块最终实现）**：

- [ ] **A（推荐）**：已有文件始终优先；模板只补缺失文件；XLSX/TSV/TXT 作为输入，统一保存为 CSV；暂不支持标准输入。
- [x] **B**：允许用户在明确确认后用模板替换已有文件；保留原文件备份；统一保存为 CSV。
- [ ] **C**：支持已有文件、模板和标准输入三类来源；由用户在启动时选择，仍要求显式确认覆盖。

**本模块最终决定**：采用 **B：允许确认覆盖**。默认已有文件优先；用户明确确认后可以用模板替换已有文件，并保留原文件备份；导入的 XLSX/TSV/TXT 统一保存为 CSV；暂不支持标准输入。

### P2：bundle schema 检查

**目标**：在编辑前确认 bundle 可解析，并将兼容字段转换为内存中的规范模型。

**输入**：bundle JSON。

**处理**：

- 严格 JSON 解码；
- 应用默认值；
- 读取旧字段并给出 deprecation warning；
- 检查显式 `false`、`0`、空值和字段冲突的语义；
- 不根据运行 `config` 的机器平台改写目标平台。

**输出**：规范化内存模型、warning、错误位置和字段来源。

**依赖**：`internal/bundle.Load`、`internal/spec`、`internal/configure.DecodeBundle`。

**失败处理**：JSON 错误、类型错误、规范字段和旧字段冲突时阻断；warning 可以进入 review，但不能掩盖阻断错误。

**用户确认**：查看 schema warning 后继续。

**用户决定（本模块最终实现）**：

- [x] **A（推荐）**：复用现有严格解析和公共校验；旧字段允许读取并告警，保存时输出规范字段；未知字段按现有 schema 规则处理，不私自扩展。
- [ ] **B**：只做读取和校验，不自动转换旧字段；用户必须手工修改后才能保存。
- [ ] **C**：增加“只修复 schema”模式，允许单独转换并保存 bundle，不进入完整配置向导。

**本模块最终决定**：采用 **A：兼容并规范化**。复用现有严格解析和公共校验；旧字段允许读取并告警，保存时输出规范字段；未知字段按现有 schema 规则处理，不私自扩展。

### P3：profile / 目标平台确认

**目标**：确认配置对应的 Ubuntu/Kylin、架构和材料布局，防止把运行机平台当成目标机平台。

**输入**：bundle 的 platform、profile 元数据、材料目录（只读取，不下载）。

**输出**：目标平台摘要、材料路径检查结果、不可由向导修改的字段列表。

**依赖**：`bundle.Load`、downloader 交付目录约定。

**失败处理**：目标平台缺失、架构不匹配或材料路径明显无效时阻断；不自动切换 profile。

**用户确认**：确认“该配置用于目标平台 X”。

**用户决定（本模块最终实现）**：

- [x] **A（推荐）**：平台和 profile 只读，由 downloader/template 决定；config 只校验并提示，不切换 profile；大材料 SHA256 由 downloader 负责。
- [ ] **B**：允许在 config 中重新选择 profile，但只允许选择已有交付目录中的 profile。
- [ ] **C**：config 只校验 bundle，不检查材料状态；材料完整性由后续 plan/apply 或人工验收负责。

**本模块最终决定**：采用 **A：只读确认**。平台和 profile 由 downloader/template 决定；config 只读取、校验并提示；大材料 SHA256 由 downloader 负责。

### P4：项目级策略

**目标**：编辑一批机器共享的执行策略，不混入目标机现场事实。

**候选字段**：

- `configure_management_network`、`apply_network_immediately`；
- 管理/RDMA MTU；
- `bond_mode`、LACP 和 active-backup 参数；
- `rdma_mode`、RDMA 网关探测；
- route priority、已有配置备份和软件源策略；
- check 的 auto 参数、SSH 连接参数、XCCL 开关；
- `post_power_action`。

**输出**：更新后的 bundle 策略、每项值的来源（模板/默认/用户）。

**依赖**：bundle 默认值和 `ValidateProject` 规则。

**失败处理**：枚举值、范围、互斥选项不合法时停留在当前策略；新项目电源动作建议默认 `none`，不能静默继承危险动作。

**用户确认**：每组策略可恢复模板值；离开阶段前显示差异。

**用户决定（本模块最终实现）**：

- [x] **A（推荐）**：普通向导只编辑常用项目策略；`post_tasks`、`mlxconfig`、artifacts、完整 XCCL 参数和材料版本保留给模板/高级编辑。
- [ ] **B**：普通向导只读 bundle 策略，不提供 bundle 编辑；用户通过外部 JSON 编辑器修改。
- [ ] **C**：将所有项目级字段都纳入向导，包括材料路径和高级执行参数。

**本模块最终决定**：采用 **A：常用策略**。普通向导编辑常用项目策略；`post_tasks`、`mlxconfig`、artifacts、完整 XCCL 参数和材料版本保留给模板或高级编辑。

### P5：inventory 来源选择

**目标**：确定逐机规划来自空表、已有表还是外部导入表。

**输入**：CSV/TSV/TXT/XLSX、已有 `inventory.csv`。

**输出**：统一的 `MachineRecord` 列表、原始表头映射、额外列清单。

**依赖**：`internal/inventory.Load`、表头别名和兼容规则。

**失败处理**：表头无法识别、hostname 重复、行结构不完整或 XLSX sheet 不明确时阻断并指出行列。

**用户确认**：导入前显示文件格式、sheet、机器数、可识别字段和额外列。

**用户决定（本模块最终实现）**：

- [x] **A（推荐）**：支持单个 CSV/TSV/TXT/XLSX；XLSX 默认使用第一张 sheet，也支持 `--sheet`；额外列原样保留；不支持多文件合并。
- [ ] **B**：XLSX 必须显式指定 sheet；额外列保留但在 review 中提示。
- [ ] **C**：支持多个 inventory 文件合并，并在合并前检查 hostname/IP 冲突。

**本模块最终决定**：采用 **A：单文件导入**。支持单个 CSV/TSV/TXT/XLSX；XLSX 默认使用第一张 sheet，也支持 `--sheet`；额外列原样保留；不支持多文件合并。

### P6：inventory 模板导入与字段检查

**目标**：准备或导入 inventory 模板，接在 bundle 参数表之后按机器分组逐项编辑，支持折叠、分页、新增机器及 RDMA 槽位列组。

**建议字段**：

```text
hostname
mgmt_mode, mgmt_target_name
mgmt_ip, mgmt_prefix, mgmt_gateway
mgmt_iface1, mgmt_mac1, mgmt_iface2, mgmt_mac2
rdmaN_name, rdmaN_ip, rdmaN_prefix, rdmaN_gateway, rdmaN_mac
```

**完整规划的校验规则**（按 P10，模板中的正常待填字段不阻断输出）：

- `hostname` 唯一且必填；
- `mgmt_mode` 为 `single`、`bond` 或 `auto`；
- 管理和 `rdma_mode=full` 的 IP/prefix/gateway 按规则校验；
- 空 RDMA 名称可按槽位派生 `xgbe0`、`xgbe1`；
- 空 MAC、接口名表示待现场发现或人工确认，不表示删除该规划槽位；
- 已有规划值不得被模板默认值静默替换。

**输出**：逐机目标规划和未完成字段列表。

**依赖**：`inventory.Load`、`spec.ResolveMachine`、项目级 bundle 策略。

**失败处理**：定位已填写内容中的地址、prefix/gateway、模式和槽位问题；待填写项与错误内容分别展示，模板保存门槛由 P10 确定。

**用户确认**：查看导入摘要和待填写字段；在 inventory 表格中逐项修改，再进入组合保存预览。

**用户决定（本模块最终实现）**：

- [ ] **A（推荐）**：第一阶段按机器逐项编辑普通字段；hostname 必填；保存前检查跨机器 hostname/IP 冲突。
- [ ] **B**：提供类似表格的批量编辑；允许暂存空 hostname 行，但最终保存前必须补齐。
- [ ] **C（旧决定，已替代）**：config 只负责导入和校验，不提供逐字段编辑；用户通过外部表格编辑 inventory。
- [x] **D（最新需求）**：提供逐机表格型 TUI，显示当前值、中文说明和输入框；允许空 hostname 等正常待填项保存，明确冲突和格式错误阻断。

**本模块最终决定**：采用 **D：导入 + 逐机表格编辑**，替代旧 C 决策。Bundle 页 Ctrl+S 进入 inventory 页，再 Ctrl+S 进入 review；每项修改复用模板校验，保存前重新生成规范化结果，接入原事务保存流程。

### P7：高级字段处理

**目标**：避免普通向导隐藏或破坏复杂配置，同时不把所有 bundle 字段塞进问答流程。

**候选高级字段**：

- `rdmaN_table`、`rdmaN_route_cidr`、`rdmaN_rail_id`；
- 非标准 repo entries；
- packages/artifacts 和特殊内核字段；
- `mlxconfig.settings`；
- 复杂 `post_tasks`；
- 完整 XCCL 环境变量、布局和阈值；
- 厂商 stage checks。

**输出**：保留的高级字段和字段摘要；修改通过外部文件编辑完成。

**依赖**：规范化序列化、未知字段保留策略。

**失败处理**：高级字段格式错误时阻断保存；普通向导不能用“未展示”掩盖错误。

**用户确认**：预览高级字段的保留结果；已有配置的显式值优先，模板只作为选定来源，缺省派生遵循公共规则。

**用户决定（本模块最终实现）**：

- [x] **A（推荐）**：普通向导不编辑高级字段；普通 inventory 不生成或展示 table/route CIDR；旧模板中的显式值仍兼容读取；route CIDR/table 缺失时按公共规则派生；暂不增加 `--advanced`。
- [ ] **B**：增加 `config --advanced`，允许编辑 route CIDR/table、rail ID 等 inventory 高级字段。
- [ ] **C**：高级字段完全由外部 JSON/CSV 编辑，config 只负责保留和校验。

**本模块最终决定（更新）**：复杂 bundle 对象继续保留并由外部文件编辑；普通 inventory 不生成或展示 RDMA 槽位的 route CIDR/table，旧模板中的显式值仍兼容读取。route CIDR/table 缺失时按公共规则派生；不增加 `--advanced`。

### P8：规划与现场事实标记

**目标**：在界面和输出中区分用户目标、模板值、派生值和现场事实。

**建议分类**：

| 类别 | 例子 | 初始来源 | config 是否可自动替换 |
| --- | --- | --- | --- |
| 规划目标 | IP、prefix、gateway、模式、槽位数 | 用户/网络规划 | 否 |
| 模板策略 | 平台、软件包、默认 check | profile 模板 | 仅用户确认后 |
| 派生值 | route CIDR、table、auto 参数说明 | 解析器 | 仅在无显式值时 |
| 现场事实 | MAC、接口名、PCI、carrier、当前 IP | discover/apply | 不由 config 自动采集 |

**输出**：待 discover 字段清单、字段来源和规划/事实差异模型。

**依赖**：当前读取来源和本轮内存状态；第一阶段不新增 inventory 列或 provenance 旁车文件。

**失败处理**：若现有 schema 无法表达来源，不应偷偷新增不兼容列；可以只在预览中显示，等待单独 schema 决策。

**用户确认**：明确哪些字段保存到 inventory，哪些只作为状态展示。

**用户决定（本模块最终实现）**：

- [x] **A（推荐）**：第一阶段不增加 inventory schema 列；来源和待确认状态只在界面/报告中展示；discover 只写回允许的现场事实。
- [ ] **B**：在 inventory 中增加来源/状态列，持久化模板、用户、discover 等来源。
- [ ] **C**：使用 planning 旁车文件保存 provenance，避免改变现有 inventory 列。

**本模块最终决定**：采用 **A：只展示不改 schema**。第一阶段不增加 inventory 列；来源和待确认状态只在界面或报告中展示；discover 只写回允许的现场事实。

### P9：规范化与派生值预览

**目标**：使用和 plan/apply/discover 相同的解析路径，展示最终有效配置。

**预览内容**：

- 每台机器的 hostname、管理模式和目标名；
- 管理地址、RDMA 槽位、名称、IP/prefix/gateway；
- 派生 route CIDR、table 和 auto 参数的解析时机；
- 即将规范化的表头、字段别名和 JSON 字段；
- warning、阻断错误、待 discover/review 项。

**输出**：规范化 bundle、规范化 inventory 的内存表示，以及可读 diff。

**依赖**：`ResolveMachine`、`ValidateProject` 和统一序列化函数。

**失败处理**：规范化前后语义不一致、字段丢失或解析结果不稳定时禁止进入保存。

**用户确认**：预览必须可返回编辑；不接受仅显示“校验通过”而不显示变更内容。

**用户决定（本模块最终实现）**：

- [x] **A（推荐）**：同时支持 TUI 和 `--plain`；预览显示规范化 diff、派生值和待 discover 字段；warning 分为提示、需 review、阻断三类。
- [ ] **B**：第一阶段只做纯文本预览，暂不实现 TUI。
- [ ] **C**：第一阶段只做 TUI，不提供独立报告导出。

**本模块最终决定**：采用 **A：TUI + 纯文本**。同时支持 TUI 和 `--plain`；显示规范化 diff、派生值和待 discover 字段；校验结果按提示、需 review、阻断三类展示。

### P10：统一校验

**目标**：检查模板格式与已填写内容，标出后续需要用户补充的规划；允许正常待填项，不以部署就绪作为模板输出前提。

**输入**：规范化内存模型。

**候选校验**：

- bundle schema、平台、默认值和高级字段；
- inventory 表头、hostname、地址、模式、槽位关系；
- bundle 策略与 inventory 字段的组合约束；
- 运行时 `ResolveMachine` 可否得到 plan/apply 所需结果；
- 已知的 warning 和现场待确认项。

**输出**：错误、warning、可保存状态和机器级定位信息。

**依赖**：现有 `internal/configure.ValidateProject`，必要时补充公共校验 API。

**失败处理**：JSON/表格格式错误、已填写值中的明确冲突阻止保存；hostname、IP、prefix、gateway 等正常待填项列入待填写清单，允许输出模板。完整部署校验不作为模板保存条件。

**用户确认**：显示校验摘要和错误列表；`--check-only` 在此阶段结束并且不写文件。

**用户决定（本模块最终实现）**：

- [x] **A（推荐）**：复用 `ValidateProject` 和 `ResolveMachine`；阻断错误禁止保存；warning 进入 review；`--check-only` 只读且不写文件。
- [ ] **B**：config 只做 schema 和字段校验，项目级校验留给 plan/apply。
- [x] **C（附加能力，可与 A 组合）**：check-only 支持 XLSX 导入并在输出中展示规范化 CSV 建议，不写任何文件。

**用户补充决定：装机前模板保存门槛**：

- [x] **允许待填模板（推荐）**：允许缺少现场规划，展示待填写清单并输出模板；阻止 JSON/表格格式错误和已填写值中的明确冲突。
- [ ] **保留示例值**：输出带示例机器和地址的模板，明确提示用户修改，主要检查结构和示例自身的一致性。
- [ ] **只检查文件格式**：只确认 JSON/表格可解析，字段取值、冲突和完整性留到后续校验。

**本模块最终决定**：采用 **允许待填模板**，修正此前 A + C 中的完整校验保存门槛。保留复用公共解析/校验和 `--check-only` 读取 XLSX、展示规范化 CSV 建议且不写文件的能力。待填写字段不阻止模板输出；格式错误和已填写值中的明确冲突阻止保存。完整部署校验不作为模板保存条件，`ValidateProject` / `ResolveMachine` 需区分模板检查和完整配置检查，不能将正常待填项直接判为保存失败。

### P11：预览与 review

**目标**：让用户在任何文件写入前确认完整变更。

**输入**：P9 预览和 P10 校验结果。

**输出**：确认、返回修改、取消三种结果。

**review 至少显示**：

- 新建、修改、删除的文件；
- bundle 策略差异；
- 每台机器的规划摘要；
- 旧字段转换和弃用提示；
- 待 discover 字段；
- warning、备份位置和保存目标。

**依赖**：稳定 diff、敏感字段脱敏、终端和 plain 输出。

**失败处理**：终端中断等同取消；review 数据不完整时不能直接保存。

**用户确认**：交互运行时 review 后按 Enter 确认，可选择只保存 bundle、只保存 inventory 或保存两者；自动化参数可以跳过交互 review，校验仍需通过。

**用户决定（本模块最终实现）**：

- [ ] **A（推荐）**：review 后要求输入 `SAVE`；bundle 和 inventory 作为一次变更一起确认；warning 必须明确接受后才能保存。
- [x] **B**：按 Enter 确认即可；允许只保存 bundle 或只保存 inventory。
- [x] **C**：提供自动化确认参数，供脚本跳过交互 review；阻断错误仍禁止保存。

**本模块最终决定**：采用 **B + C：Enter 确认、可单文件保存，并支持自动化确认**。交互运行时 review 后按 Enter 确认，可选择只保存 bundle、只保存 inventory 或保存两者；提供脚本用的确认参数以跳过交互 review。按 P10 的模板门槛检查，允许正常待填项；单文件保存需检查落盘后的文件组合，避免引入明确冲突。

### P12：事务保存

**目标**：将两个规划文件作为一个 generation 保存，保留恢复能力和用户文件。

**候选保存原则**：

- 保存到临时 generation 后再切换；
- 原 planning 进入 backup；
- 保存两份时 bundle 和 inventory 同一代生效；只保存一份时，另一份保留原内容，并校验最终文件组合；
- 内容未变化时不创建新 backup；
- 取消不改变原文件；
- 中断后下一次启动可恢复旧 generation；
- planning 中的额外文件和权限按约定保留；
- 使用项目锁防止并发编辑。

**输出**：新 planning generation、backup 记录、保存摘要。

**依赖**：待迁移或重构的保存事务代码、锁、路径保护和文件权限策略。

**失败处理**：任一步骤失败都恢复旧 planning；恢复失败必须明确指出 backup 和 transaction 记录位置，不得报告成功。

**用户确认**：保存前告知 backup 行为；恢复操作必须单独确认。

**用户决定（本模块最终实现）**：

- [x] **A（推荐）**：在 `internal/configure` 实现事务保存、锁、backup、恢复和路径保护，由 config 独立使用；downloader 只保留下载目录锁。
- [ ] **B**：先在 `cmd/envinit` 内独立实现保存，后续再抽公共包。
- [ ] **C**：第一阶段只做单文件原子写入，不实现整组 generation 和中断自动恢复；P1 已确认的模板覆盖备份仍保留。

**本模块最终决定**：采用 **A：公共保存模块**。在 `internal/configure` 实现事务、备份、恢复、锁和路径保护；保存两份时同一代生效，保存一份时保留另一份原内容并校验最终组合。downloader 保留下载目录锁。原 downloader 向导事务代码已移除，后续在公共包中重建所需能力。

### P13：discover 场景边界

**目标**：说明 config 与 discover 面向不同服务器状态，避免把 discover 当作模板生成后的必经步骤。

**流程**：

```text
系统已装好，管理网已配置，能够通过 SSH 或其他适用方式登录
  -> 用户独立运行 discover
  -> 采集 MAC/接口名/PCI/carrier/当前 IP 等事实
  -> review 映射
  -> 只写回获准的事实
  -> 用户核对和完善现场配置
```

**不得自动覆盖**：用户规划的 IP、prefix、gateway、route table、route CIDR、rail ID 和已确认槽位。

**输出**：场景说明。config 交付的是供用户继续编辑的模板，不承诺产物已满足 discover 的全部输入要求。

**依赖**：discover 的现场可登录条件及其现有输入要求；这些不是 config 生成模板的前置条件。

**失败处理**：歧义映射、IP 冲突、探测异常和写回失败必须阻断对应更新；不能用 `--yes` 把不唯一映射当作确认。

**用户确认**：discover 是否写回必须独立于 config 保存确认。

**用户决定（本模块最终实现）**：

- [ ] **A（推荐）**：config 与 discover 保持两个独立命令；config 展示待 discover 字段；discover review 后写回事实并生成 backup。
- [ ] **B**：config 保存后提供“继续 discover”快捷入口，但仍由独立 discover 流程确认和写回。
- [ ] **C**：config 不展示 provenance；discover 只更新允许的字段，不自动生成额外状态记录。

**本模块最终决定**：用户已澄清：**config 面向服务器尚未装机时的模板准备；discover 面向系统已装好、管理网已配置并能够登录的场景。**上面的 A/B/C 为此前候选，本轮未选定；不据此增加 config 自动进入 discover、修改 discover 写回或新增备份行为。

### P14：模板交付与后续编辑说明

**目标**：交付模板并说明哪些内容仍需用户自行填写。后续命令在配置补全且各自运行条件满足后使用，生成模板不代表已经达到执行条件。

**后续命令参考**（仅在用户补齐配置并满足相应运行条件后使用；是否在 config 末尾显示待确认）：

```bash
env_init plan  --bundle planning/bundle.json --inventory planning/inventory.csv
env_init apply --bundle planning/bundle.json --inventory planning/inventory.csv
env_init check --bundle planning/bundle.json --inventory planning/inventory.csv
```

**验收内容**：

- 完整配置下，`plan` 使用的 `ResolveMachine` 结果与 config 预览一致；未填模板明确展示未解析项；
- 用户在部署前补齐规划字段；
- discover 写回后仍能重新加载；
- check 能读取同一 bundle/inventory；
- config 生成的路径和材料布局符合 downloader 交付目录。

**失败处理**：下游加载失败时给出具体文件、字段和阶段，不在 config 中复制一套下游解析逻辑。

**用户确认**：是否在保存结束时打印下一步命令，或只输出文件位置。

**用户决定（本模块最终实现）**：

- [x] **A（推荐）：文件和待填清单**：显示模板路径、待填写字段和手工编辑说明；提醒这是模板，补齐规划后再用于部署。
- [ ] **B：再附后续命令参考**：在上述内容之外附 plan/apply/check/discover 参考命令及各自使用条件，不自动运行。
- [ ] **C：只显示保存结果**：只显示成功保存的文件路径和备份位置。

**本模块最终决定**：采用 **A：文件和待填清单**。结束页面展示模板路径、待填写清单和手工编辑说明，明确用户需要继续修改、补齐规划后再用于部署；不把下游命令串成固定下一步。

## 4. 命令形态候选

### 4.1 交互式

```bash
env_init config
env_init config --project ./env_tool
env_init config --bundle planning/bundle.json --inventory planning/inventory.csv
env_init config \
  --bundle-template ./examples/bundle.ubuntu22.sample.json \
  --inventory-template ./examples/inventory.sample.csv \
  --project ./env_tool
```

### 4.2 校验和纯文本

```bash
env_init config --check-only --bundle ./bundle.json --inventory ./inventory.csv
env_init config --plain --bundle ./bundle.json --inventory ./inventory.csv
```

第一阶段使用 `--yes` 自动化确认保存，`--save both|bundle|inventory` 选择保存对象；仍执行模板级校验。输入继续使用模板或配置文件。

## 5. 建议的代码拆分

现有公共能力可以继续扩展：

```text
internal/configure/
  bundle_editor.go
  inventory_import.go
  normalize.go
  preview.go
  save.go
  validate.go

cmd/envinit/
  config.go
  config_tui.go
```

建议原则：

- `internal/configure` 只处理模型、解析、规范化、校验和保存，不依赖 TUI；
- `cmd/envinit/config.go` 负责 flag、路径和命令生命周期；
- `config_tui.go` 负责交互，不直接修改文件；
- `plan/apply/discover/check` 继续使用已有公共解析器；
- 不把完整向导逻辑重新放回 downloader。

## 6. 分阶段实施顺序（候选）

用户已授权按已确认范围开始实施；下列顺序保留为实施组织记录。

1. **Pipeline 0：公共模型和路径**：确定路径来源、配置来源、锁和错误模型。
2. **Pipeline 1：只读 config**：实现 P0～P3、`--check-only` 和纯文本预览，不写文件。
3. **Pipeline 2：项目策略**：实现 P4 的有限字段编辑，复用 bundle schema 校验。
4. **Pipeline 3：inventory 模板导入与编辑**：实现 P5～P6，支持单个 CSV/TSV/TXT/XLSX，保留额外列，提供逐机表格编辑并规范化输出 CSV。
5. **Pipeline 4：预览和统一校验**：实现 P7～P11，接入 `ValidateProject` 和 `ResolveMachine`。
6. **Pipeline 5：事务保存**：实现 P12，迁移或重构锁、backup、恢复和额外文件保护。
7. **Pipeline 6：场景说明**：依据 P8、P13 显示待补充信息，说明 discover 的独立运行条件。
8. **Pipeline 7：高级能力**：高级编辑暂不纳入首阶段；XLSX 导入已在 P5 确认，不再作为未决功能。
9. **Pipeline 8：模板验收**：验证待填模板能够输出，格式错误和已填值的明确冲突被阻止，完整配置可被下游解析器消费。

## 7. 每阶段统一验收模板

实现某一阶段前，建议补齐以下表格并得到确认：

| 项目 | 决定 |
| --- | --- |
| 是否实施 | 实施 / 暂缓 / 取消 |
| 实施范围 | 明确字段、格式和入口 |
| 输入 | 文件、参数、模板和已有状态 |
| 输出 | 内存模型、文件、warning 或交接信息 |
| 依赖 | 公共包、已有命令和目录约定 |
| 用户确认点 | 继续、返回、保存、覆盖或取消 |
| 失败处理 | 阻断、回退、重试和恢复 |
| 不在范围内 | 明确不自动猜测或不编辑的内容 |
| 验收命令 | 测试、check-only、diff 或人工 review |

## 8. 当前已确认的边界

在各阶段尚未逐项批准前，以下内容作为讨论基线：

1. downloader 已恢复为纯下载器，不包含配置问答；
2. downloader 在 planning 文件缺失时可以安全复制模板，不覆盖已有 planning；
3. 配置初始化命令名为 `env_init config`；
4. config 第一阶段只处理本地 bundle/inventory，不主动 SSH/discover；
5. discover 保持独立，规划目标和现场事实分离；
6. 表格 TUI 编辑常用项目策略及 inventory 逐机规划，支持模板导入、预览、校验和组合保存；
7. 复杂 artifacts、mlxconfig、post_tasks、XCCL 高级参数保留给模板或高级编辑；
8. config 的预览和校验必须复用 `bundle.Load`、`inventory.Load`、`ResolveMachine`、`ValidateProject` 等公共逻辑；
9. 事务保存、备份、恢复和文件保护需要迁移或重构后再纳入实现。

## 9. 模块决策进度与剩余细节

P0～P12、P14 已记录用户选择；P13 根据用户澄清确认为两个独立使用场景，不采用原先的自动交接方案。

实现落位：待填项用空值；常用策略清单定义于 `internal/configure/editor.go` 并在界面展示；自动化确认使用 `--yes`；备份默认保留。用户追加确认两个输出文件同目录，外部输入用模板参数导入。config 使用公共目录锁并协调 downloader 项目锁；保存前检查 review 期间的外部编辑。两次目录 rename 之间可能短暂不可见，事务记录用于显式恢复。

### 最新场景澄清后的待确认项

- 已明确：config 用于装机前产出模板，用户会继续手工编辑；discover 用于已装好系统、已配置管理网且能登录的现场。
- P10 已重新确认：允许待填模板，展示待填写清单；格式错误和已填写值的明确冲突阻止保存；完整部署校验不作为模板保存条件。落位使用空字段表示待填。
- P11/P12：已选定的确认方式和保存机制保留，使用 P10 已修正的模板保存门槛。
- P13：记录上述场景边界，不把本次澄清当作授权修改 discover。
- P14 已确认：末尾显示模板路径、待填写清单和手工编辑说明。
