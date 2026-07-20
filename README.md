# envinit 现场使用手册

## 1. 用途

`envinit` 用于在离线环境中初始化昆仑芯服务器。它读取两份规划文件：

- `bundle.json`：一批机器共用的安装参数、离线物料路径和默认值。
- `planning.csv`：逐台机器的管理网和 RDMA 网卡规划。

代码仓库当前提供的规划表文件名是 `env_tool/planning/inventory.csv`。它和本文所说的 `planning.csv` 是同一种文件，命令行参数统一写为 `--inventory`。现场可以继续使用现有文件名，也可以复制后改名为 `planning.csv`。

当前交付只保留两个 x86_64 profile：

| Profile | 系统 | 包管理 | 网络后端 | Bundle 样例 |
| --- | --- | --- | --- | --- |
| `ubuntu22.04-x86_64` | Ubuntu 22.04 | `apt` | `netplan` | `examples/bundle.ubuntu22.sample.json` |
| `kylin10sp3-x86_64` | Kylin V10 SP3 | `yum` | `auto`，运行时在 NetworkManager 和 legacy network 间选择 | `examples/bundle.kylin10sp3.sample.json` |

工具提供四个子命令：

| 子命令 | 用途 | 是否修改系统 |
| --- | --- | --- |
| `plan` | 解析规划文件，通过按 stage 浏览的 TUI 预览目标机器、写入文件和执行动作；非交互终端或 `--plain` 时输出纯文本 | 否 |
| `apply` | 按 stage 执行初始化 | 是，需要 `root` |
| `discover` | 在本机或通过 SSH 自动发现管理网和 RDMA 网络信息，并写回规划表 | 是，修改规划表文件，不修改目标机系统配置 |
| `check` | 执行 RDMA 带宽、大包 ping，以及可选的单机/多机 XCCL 集合通信检查 | 标准 bandwidth/ping 只产生测试流量；XCCL 会创建并在结束时清理临时运行时、临时 SSH key 和本轮授权行 |

推荐阅读顺序：第一次交付先看第 2～4 节完成下载和规划，再看第 5～7 节理解 apply 和配置字段，最后按第 8 节执行 discover/check；遇到报错先查第 9 节。所有命令都可以使用 `--help` 查看当前二进制实际支持的参数。

## 2. 文件放置

U 盘挂载到 `/mnt/usb` 后，推荐保留以下结构。`data/` 必须和执行目录同级，原因是 bundle 样例中的物料路径使用了 `data/...` 相对路径。

```text
/mnt/usb/
└── env_tool/
    ├── env_init
    ├── env_init_arch
    ├── run1.sh
    ├── run2.sh
    ├── planning/
    │   ├── bundle.json
    │   └── inventory.csv
    └── data/
        ├── apt-repo/              # Ubuntu profile
        ├── rpm-repo/              # Kylin profile
        ├── hca/mellanox/
        ├── xpu_driver/
        ├── xpu_firmware/p800/
        ├── xpu_container_toolkit/
        ├── xpu_exporter/
        ├── misc/
        ├── single_deb/            # Ubuntu 可选单包
        └── single_rpm/            # Kylin 可选单包
```

二进制选择：

- `env_init`：x86-64 机器。
- `env_init_arch`：ARM64 / aarch64 兼容构建。当前 release 没有 ARM64 系统物料 profile，不能把 x86-64 OFED/XRE/XDR 包直接用于 ARM 服务器；只有另行准备匹配物料和 bundle 后才能使用该二进制。

首次执行前确认二进制可执行：

```bash
cd /mnt/usb/env_tool
chmod +x env_init run1.sh run2.sh
```

必须从 `env_tool/` 目录执行：

```bash
cd /mnt/usb/env_tool
sudo ./env_init apply --inventory planning/inventory.csv --bundle planning/bundle.json --host node1
```

如果在其他目录执行绝对路径命令，例如 `/mnt/usb/env_tool/env_init --bundle /mnt/usb/env_tool/planning/bundle.json`，当前工作目录下没有 `data/` 时，`data/...` 物料路径会找不到。

仓库本地准备 profile 物料时，目录结构建议和发布 profile 保持一致：

```text
data/profiles/
├── ubuntu22.04-x86_64/
│   ├── apt-repo/
│   ├── hca/mellanox/
│   ├── xpu_driver/
│   ├── xpu_firmware/p800/
│   ├── xpu_container_toolkit/
│   ├── xpu_exporter/
│   ├── misc/
│   └── single_deb/
└── kylin10sp3-x86_64/
    ├── rpm-repo/
    ├── hca/mellanox/
    ├── xpu_driver/
    ├── xpu_firmware/p800/
    ├── xpu_container_toolkit/
    ├── xpu_exporter/
    ├── misc/
    └── single_rpm/
```

`data/profiles/` 是长期维护的物料区，不属于每个 GitHub release 的版本包。发版脚本不会读取、下载或重新打包这些大文件，只发布轻量的 base、各 profile 的 bundle、`inventory.csv`、manifest 和跨平台 downloader。manifest 为每个 profile 记录稳定物料目录，例如 `/data/profiles/ubuntu22.04-x86_64`。

downloader 执行时才登录 AList，递归读取所选 profile 的物料目录，并把该目录下面的内容直接组装到输出目录的 `data/`。因此发布新版本不会为了几 GB 的离线源和驱动包触发一次完整物料读取，交付人员也不需要手工解包或搬运 profile 归档。

## 3. 下载器和 profile 选择

发布包中的下载器会读取内置 manifest，让用户选择本次交付要拉取的系统 profile。交互式环境下直接运行下载器，会看到类似选择界面：

```text
请选择本次交付系统 profile:
1. Ubuntu 22.04 x86_64 - Ubuntu apt/deb material profile
2. Kylin V10 SP3 x86_64 - Kylin yum/rpm material profile
请输入序号或 profile ID:
```

先根据执行 downloader 的电脑选择发布资产。这里选择的是下载和组装物料所用电脑的系统，不是最终服务器系统；最终服务器系统由 `--profile` 决定：

| 执行电脑 | 发布资产 |
| --- | --- |
| Linux x86-64 | `env_tool_downloader-linux-amd64` |
| Linux ARM64 | `env_tool_downloader-linux-arm64` |
| Intel Mac | `env_tool_downloader-darwin-amd64` |
| Apple Silicon Mac | `env_tool_downloader-darwin-arm64` |
| Windows x86-64 | `env_tool_downloader-windows-amd64.exe` |
| Windows ARM64 | `env_tool_downloader-windows-arm64.exe` |

Linux/macOS 可以保留发布文件名，也可以重命名为 `downloader`。首次运行前增加执行权限：

```bash
chmod +x env_tool_downloader-linux-amd64
./env_tool_downloader-linux-amd64 --list-profiles
```

运行前应把文件摘要与 release 中的 `SHA256SUMS` 对照。Linux 使用 `sha256sum <文件>`，macOS 使用 `shasum -a 256 <文件>`，Windows PowerShell 使用 `Get-FileHash <文件> -Algorithm SHA256`。macOS 若在摘要确认无误后仍因下载隔离属性拒绝运行，可执行 `xattr -d com.apple.quarantine <文件>`，再重新运行；这会修改本地下载文件属性，不影响目标服务器。

也可以用命令行直接指定：

```bash
./downloader --profile ubuntu22.04-x86_64 --output-dir /mnt/usb/env_tool
./downloader --profile kylin10sp3-x86_64 --output-dir /mnt/usb/env_tool

# 网络或存储端承载较低时可降低并发数
./downloader --profile kylin10sp3-x86_64 --output-dir /mnt/usb/env_tool --jobs 3
```

常用参数：

| 参数 | 说明 |
| --- | --- |
| `--list-profiles` | 只列出内置 manifest 中支持的 profile，不下载 |
| `--profile <id>` | 指定 profile，适合非交互环境 |
| `--output-dir <dir>` | 下载和解包输出目录，通常为 U 盘上的 `env_tool` |
| `--jobs <n>` | profile 物料并发下载数，默认 6，最大 32 |
| `--output <path>` | 仅兼容旧版“单归档 downloader”；当前内置 manifest/profile 的 release 不使用该参数，请使用 `--output-dir` |

下载完成后只保留一个 `planning/` 目录，其中 `planning/bundle.json` 是 downloader 根据所选系统 profile 单独下载的模板，`planning/inventory.csv` 是一份可编辑的规划表。交付目录不再携带 `examples/`、`planning/templates/`、`inventory.sample.csv`，也不会同时放入两个系统的 bundle。`data/` 来自所选 profile 根目录，不会再携带另一个 profile 的目录。downloader 的筛选粒度到 profile 为止：除 `.DS_Store`、AppleDouble、`Thumbs.db` 外，它会递归拉取该根目录下的全部文件，不会再根据 bundle 路径自动过滤历史版本、源码展开目录或备用包。因此 AList 的 `/data/profiles/<profile>` 本身必须整理成可直接交付的最终物料集合。

`--output-dir` 建议指向一个新的空目录，或者此前由同一 profile 组装的目录。同一 profile 重复运行是安全的，下载器会续传或跳过已完成文件；不要直接在 Ubuntu 已完成目录上改选 Kylin，下载器不会删除新 profile 中不存在的旧文件，否则两个系统的物料会混在一起。需要切换 profile 时请使用另一个输出目录，或先人工确认并清空旧交付目录。

下载器会先校验并自动解包轻量 base，再把所选 profile 的 bundle 写为 `planning/bundle.json`、把统一规划表写为 `planning/inventory.csv`，最后递归组装 profile 物料。它不依赖目标电脑预装 `tar`；Linux、macOS 和 Windows 分别使用对应的权限或文件属性处理逻辑，脚本和 `.run` 文件会恢复为可执行文件。目录遍历会忽略 `.DS_Store`、AppleDouble 和 `Thumbs.db` 等系统杂项文件。

base、bundle 和 inventory 使用 manifest 中的 SHA256 校验。bundle 本身是 JSON 文件，即使 COS 返回 `Content-Type: application/json` 也会作为正常资产下载；只有响应同时具有 AList 的 `code/message/data` 错误信封且 `code != 200` 时才按接口错误终止。profile 物料如果 AList 提供 SHA256，也逐文件校验；否则使用远端路径、大小和修改时间形成完成标记。记录保存在 `.envinit-downloads/`，中断下载会保留 `.part` 文件供续传，使用同一输出目录重复运行时会跳过未变化且已完成的文件。

profile 物料开始组装后会显示聚合进度，而不是为 1000 多个文件分别刷屏：

```text
Material [===========                   ]  37.42%  2.67 GiB/7.14 GiB  183.60 MiB/s  641/1529 files
```

百分比和容量以 AList 列出的整个 profile 总字节数为分母；文件数表示已处理条目和总条目。速度只统计本轮实际从网络收到的字节，不把已校验文件或已有 `.part` 算入下载速度。交互终端中进度条会在同一行刷新；输出重定向到日志文件时只打印起始和最终状态，避免产生大量重复行。

如果 AList 目录缓存中残留一个“大小为 0、没有 SHA256、实际 COS 对象已经不存在”的悬空条目，downloader 会输出 `WARNING ... skipping stale entry` 并跳过；本地正式 profile 不应包含这类条目，仍建议从 COS/AList 清理。任何非零物料或带 SHA256 的物料返回 404 都会继续作为错误终止，避免静默生成缺包的交付目录。

版本边界需要特别区分：base、bundle 和 inventory 固定属于 downloader 对应的 release；`material_root` 指向长期维护的 profile 目录，物料本身不随 GitHub release tag 再复制一份。因此以后再次运行旧 downloader 时，会读取该 profile 目录当时可见的物料。需要完整复现某次历史交付时，应保留当时已经组装好的 `env_tool` 目录，不能只保留 downloader。

## 4. 推荐工作流

### 4.1 准备规划文件

先编辑：

```text
/mnt/usb/env_tool/planning/bundle.json
/mnt/usb/env_tool/planning/inventory.csv
```

编写原则：

- 整批机器一致的参数写入 `bundle.json`，例如默认网卡名、MTU、离线包路径和固件路径。
- 每台机器不同的参数写入 `planning.csv`，例如 hostname、管理 IP、RDMA IP 和 MAC。
- 网卡名可能随 BIOS、内核和发行版变化。正式交付时建议在 `planning.csv` 中填写 MAC。
- 同一批机器中，`rdma1` 到 `rdmaN` 的物理端口顺序必须保持一致。

### 4.2 先执行预览

在每台目标机上运行：

```bash
cd /mnt/usb/env_tool
./env_init plan \
  --inventory /mnt/usb/env_tool/planning/inventory.csv \
  --bundle /mnt/usb/env_tool/planning/bundle.json \
  --host node1
```

将 `node1` 替换为当前机器在规划表中的 `host_id`、`hostname` 或 `mgmt_ip`。`plan` 默认打开交互式预览界面：

- `Up/Down` 或 `j/k`：切换 stage
- `PgUp` 或 `b`：向上滚动当前 stage 的动作列表
- `PgDn`、`f` 或空格：向下滚动当前 stage 的动作列表
- `Home/End`：跳到当前 stage 动作列表开头或结尾
- `q` 或 `Esc`：退出预览

需要一次性打印文本时加 `--plain`：

```bash
./env_init plan \
  --inventory /mnt/usb/env_tool/planning/inventory.csv \
  --bundle /mnt/usb/env_tool/planning/bundle.json \
  --host node1 \
  --plain
```

`plan` 参数：

| 参数 | 必需/默认 | 说明 |
| --- | --- | --- |
| `--inventory <path>` | 必需 | CSV/TSV/TXT/XLSX 规划表 |
| `--bundle <path>` | 必需 | bundle JSON |
| `--host <id>` | 自动匹配本机 | 可写 `host_id`、`hostname` 或 `mgmt_ip`；现场建议显式指定 |
| `--sheet <name>` | 第一张表 | XLSX 工作表名 |
| `--stages <list>` | `all` | 只预览指定 stage，支持逗号或空格分隔 |
| `--plain` | `false` | 输出纯文本，不打开 Plan Preview TUI |

不传 `--host` 时工具会尝试根据当前 hostname、IP 或本机 MAC 自动匹配。现场首次操作建议显式传入 `--host`。`plan` 本身不会修改 hostname，只会显示 apply 将执行的 `hostnamectl set-hostname` 动作；真正修改发生在 `apply`。

### 4.3 执行初始化

完整执行：

```bash
sudo ./env_init apply \
  --inventory /mnt/usb/env_tool/planning/inventory.csv \
  --bundle /mnt/usb/env_tool/planning/bundle.json \
  --host node1
```

默认顺序固定为：

```text
software -> ofed -> network -> xre -> xdr -> firmware
-> container -> mlxconfig -> sysctl -> kernel -> post
```

仓库也提供了分两段执行脚本：

```bash
sudo ./run1.sh
sudo ./run2.sh
```

`run1.sh` 执行 `software ofed`，`run2.sh` 执行剩余 stage。适合先安装依赖和 OFED，确认状态后再继续。也可以手工选择 stage：

```bash
sudo ./env_init apply \
  --inventory /mnt/usb/env_tool/planning/inventory.csv \
  --bundle /mnt/usb/env_tool/planning/bundle.json \
  --host node1 \
  --stages network sysctl kernel
```

`post` 默认会询问是否执行 `ipmitool power soft`。非交互环境无法确认时会跳过关机。

`apply` 必须在交互式 TTY 中运行。不要使用不分配 TTY 的 SSH 远程命令或批处理方式直接执行初始化；工具会在启动阶段拒绝这类运行方式，避免 MST 设备确认、网卡绑定确认等高风险步骤在无人确认时继续执行。

### 4.4 发现并补齐 check 网络信息

`discover` 用于读取目标机当前管理网和 RDMA 网络状态，经过自动筛选与人工确认后，补齐 inventory 中供 `check` 使用的 `mgmt_ip`、`rdmaN_name`、`rdmaN_ip`。允许一次处理一台或多台机器：

```bash
sudo ./env_init discover \
  --inventory /mnt/usb/env_tool/planning/inventory.csv \
  --bundle /mnt/usb/env_tool/planning/bundle.json \
  --hosts node1=192.168.32.11
```

左侧 `node1` 是要更新的 inventory 身份，右侧是本次可达的 SSH 地址。只有临时 IP 时也可直接写 `--hosts 192.168.32.11`，工具会读取远端 hostname：存在对应行就更新，没有对应行就以 hostname 自动新增。无人值守自动接受候选时追加 `--yes`；只验证发现结果、不写文件时追加 `--dry-run`。完整的目标绑定、候选来源、排序规则、review 操作和写回范围见 8.1 节。

多台机器示例：

```bash
sudo ./env_init discover \
  --inventory /mnt/usb/env_tool/planning/inventory.csv \
  --bundle /mnt/usb/env_tool/planning/bundle.json \
  --hosts node1=192.168.32.11,node2=192.168.32.12 \
  --yes
```

### 4.5 初始化后检查

默认检查带宽、RDMA 大包连通性，并在 bundle 启用 XCCL 时执行集合通信检查：

```bash
sudo ./env_init check \
  --inventory /mnt/usb/env_tool/planning/inventory.csv \
  --bundle /mnt/usb/env_tool/planning/bundle.json \
  --hosts node1,node2
```

只检查某一项：

```bash
sudo ./env_init check \
  --inventory /mnt/usb/env_tool/planning/inventory.csv \
  --bundle /mnt/usb/env_tool/planning/bundle.json \
  --hosts node1,node2 \
  --check-stage rdma-ping
```

`bandwidth` 和 `rdma-ping` 至少需要两台机器；`xccl` 支持单机 smoke test。完整的总体流程、参数优先级、拓扑映射、交叉矩阵、计数器判定和 XCCL 临时运行时原理见第 8 节。

## 5. apply 具体执行内容

### 5.1 执行规则

`apply` 会先读取 `bundle.json` 和规划表，匹配当前机器，再按固定顺序执行所选 stage：

```text
software -> ofed -> network -> xre -> xdr -> firmware
-> container -> mlxconfig -> sysctl -> kernel -> post
```

使用 `--host node1` 显式指定机器时，工具会把规划表中的 `hostname` 作为准确信息。如果当前 hostname 不一致，会先执行：

```bash
hostnamectl set-hostname node1
```

如果只希望执行部分操作，可以使用 `--stages`：

```bash
sudo ./env_init apply \
  --inventory /mnt/usb/env_tool/planning/inventory.csv \
  --bundle /mnt/usb/env_tool/planning/bundle.json \
  --host node1 \
  --stages network sysctl
```

`apply` 必须使用 `root` 权限。正式执行前建议先将 `apply` 替换为 `plan`，在预览界面中逐个 stage 检查将要执行的动作。

#### 5.1.1 apply 参数

| 参数 | 必需/默认 | 说明 |
| --- | --- | --- |
| `--inventory <path>` | 必需 | CSV/TSV/TXT/XLSX 规划表 |
| `--bundle <path>` | 必需 | bundle JSON |
| `--host <id>` | 自动匹配本机 | 可写 `host_id`、`hostname` 或 `mgmt_ip`；现场建议显式指定，避免选错行 |
| `--sheet <name>` | 第一张表 | XLSX 工作表名 |
| `--stages <list>` | `all` | 执行全部或指定 stage，支持逗号或空格分隔；`udev` 是单独修复持久化网卡命名规则的 stage |
| `--restart` | `false` | 仅可和 `--stages all` 使用；删除当前全流程 checkpoint 后从 `software` 重跑 |
| `--root <path>` | `/` | 测试用的文件系统根前缀；正式交付不要设置，否则写入位置不是真实宿主机根目录 |

#### 5.1.2 失败断点续跑

默认执行或使用 `--stages all` 时，`apply` 会把全流程进度原子写入：

```text
/var/lib/envinit/apply-progress.json
```

每个 stage 开始前先记录 `current_stage`，成功后再加入 `completed_stages`。某个 stage 返回错误、进程异常退出或机器重启后，下一次执行同一条默认 `apply` 命令会跳过此前成功的 stage，从未完成的 stage 重新执行，然后继续后续流程。例如第一次在 `xdr` 失败：

```text
software -> ofed -> network -> xre -> xdr (FAIL)
```

再次运行时会得到：

```text
resume: skip completed stage software
resume: skip completed stage ofed
resume: skip completed stage network
resume: skip completed stage xre
==> stage: xdr
```

状态文件包含 host、解析后配置的 SHA256、已完成 stages、当前 stage、最后错误和更新时间，权限为 `0600`。bundle、inventory 解析结果、目标 host 或状态版本变化时，旧进度会自动失效，并从 `software` 重新开始，避免在配置已经改变后错误跳过操作。

需要主动从头完整重跑时使用：

```bash
sudo ./env_init apply \
  --inventory planning/inventory.csv \
  --bundle planning/bundle.json \
  --host node1 \
  --restart
```

显式指定部分 stage，例如 `--stages network sysctl`，表示人工强制执行这些 stage：不会读取、跳过或改写全流程 checkpoint。`plan` 也不会读取或修改 apply 进度，始终展示所选 stage 的完整动作。

### 5.2 Stage 总览

| Stage | 主要操作 | 作用 | 典型写入位置或命令 |
| --- | --- | --- | --- |
| `software` | 复制离线软件源、配置 apt/yum 源、安装依赖包 | 在无外网环境中准备后续编译和安装需要的软件 | `/opt/kunlun-apt-repo`、`/opt/kunlun-rpm-repo`、`apt-get install`、`yum install` |
| `ofed` | 解压并安装 Mellanox OFED | 安装 RDMA 网卡驱动和用户态工具，使 400G 网卡可用于 RoCE/RDMA | `mlnxofedinstall --add-kernel-support` |
| `network` | 确认网卡绑定、临时重命名、写入并应用管理网/RDMA 网络配置、写入持久化命名规则 | 配置管理面连通性，让每张 RDMA 网卡使用独立地址和路由表，并固化规划网卡名 | `/etc/netplan/`、`/etc/sysconfig/network-scripts/`、`/etc/udev/rules.d/70-kunlun-management-net.rules`、`/etc/udev/rules.d/71-kunlun-rdma-net.rules`、`netplan apply`、`nmcli`、`ifup` |
| `udev` | 兼容/修复 stage，单独重新生成持久化命名规则 | 在需要修复规则文件时复用 NIC Binding Review TUI，不负责临时重命名和网络配置；管理网和 RDMA 规则使用独立文件 | `/etc/udev/rules.d/70-kunlun-management-net.rules`、`/etc/udev/rules.d/71-kunlun-rdma-net.rules`、`udevadm control --reload-rules` |
| `xre` | 安装 XRE 驱动，并按卡型执行必要调优 | 让操作系统识别和管理昆仑芯 XPU | `bash <xre_installer>` |
| `xdr` | 编译并安装 XDR 内核模块 | 提供 XDR 数据通路，支持相关高速传输能力 | `build.sh`、`install.sh` |
| `firmware` | 解压并升级算力卡固件 | 将算力卡固件更新到配套版本 | `bash auto_update.sh` |
| `container` | 安装 XPU 容器相关离线包 | 让容器运行时能够向容器暴露 XPU 设备和工具 | `dpkg -i`、`yum localinstall -y` |
| `mlxconfig` | 配置 Mellanox 网卡参数 | 将网卡固件参数设置为集群要求的值 | `mst start`、`mlxconfig set` |
| `sysctl` | 追加内核网络参数并立即生效 | 增大网络缓冲区并调整多网卡主机的 ARP、反向路径检查行为 | `/etc/sysctl.conf`、`sysctl -p` |
| `kernel` | 补充 grub 内核启动参数并刷新 grub | 固化启动参数，为设备访问和性能调优准备内核启动环境 | `/etc/default/grub`、`update-grub` |
| `post` | 写入开机 RDMA 调优服务，执行附加任务和可选电源动作 | 保证重启后自动恢复 RDMA 性能参数，并完成现场收尾 | `kunlun-post-boot.service`、`ipmitool power` |

Kylin 和 Ubuntu 使用同一组 stage。差异只在包管理器、网络后端和少数平台命令：

| Stage/功能 | Kylin yum 路径 | Ubuntu apt 路径 |
| --- | --- | --- |
| `software` | 写入/启用 `offline_repo`，执行 `yum makecache`、`yum install -y` | 写入/启用 `offline_apt`，执行 `apt-get update`、`apt-get install -y` |
| `ofed` 前置依赖 | 内置检查 `elfutils-devel`，缺失时 `yum install -y elfutils-devel` | 内置检查 `linux-headers-$(uname -r)`、`build-essential`、`debhelper`、`fakeroot`，缺失时安装 |
| `network` 管理网/RDMA | 自动选择 NetworkManager 或 legacy `network`；写 ifcfg、route、rule；NetworkManager 路径写 dispatcher 脚本 | 使用 netplan/networkd；写 netplan 和 `/etc/networkd-dispatcher/routable.d/` 路由重放脚本，并确保 `networkd-dispatcher` 已安装启用 |
| `container`/`post_packages` | `yum localinstall -y <rpm>` | `dpkg -i <deb>` |
| `xre`/`xdr`/`firmware`/`mlxconfig`/`sysctl`/`kernel`/`post` | 功能一致，按平台默认内核头文件路径和 grub 命令执行 | 功能一致，按平台默认内核头文件路径和 grub 命令执行 |

### 5.3 software：配置离线软件源和软件包

`software` 会按当前平台自动选择 apt 或 yum 路径。

Ubuntu/Debian 路径下，当 `offline_apt.enabled=true` 时，工具会：

1. 将 U 盘中的离线仓库复制到目标机，例如 `data/apt-repo -> /opt/kunlun-apt-repo`。
2. 按需备份已有 apt 源。
3. 写入离线 apt 源文件。
4. 执行 `apt-get update` 和 `apt-get install -y`。

生成的 `/etc/apt/sources.list.d/kunlun-offline.list` 类似：

```text
deb [trusted=yes] file:/opt/kunlun-apt-repo ./
```

`packages` 中的 `linux-headers-{{uname_r}}` 会自动展开为当前内核版本，例如：

```text
linux-headers-5.15.0-100-generic
```

关键命令作用：

| 命令 | 作用 |
| --- | --- |
| `apt-get update` | 从离线 apt 源刷新软件包索引 |
| `apt-get install -y <packages>` | 安装内核头文件、编译工具和后续 stage 依赖的软件 |

Kylin 路径下，工具会使用 `offline_repo` 生成 yum repo 文件，执行 `yum makecache` 和 `yum install -y <packages>`。因此命令行 stage 名统一使用 `software`，不再暴露成某个发行版专属的名称。

### 5.4 ofed 和 network：安装驱动并固定网卡名

`ofed` 会解压 `artifacts.ofed_archive`，然后执行类似命令：

```bash
./mlnxofedinstall \
  --without-fw-update \
  --add-kernel-support \
  -k "$(uname -r)" \
  --skip-distro-check \
  --force
```

`ofed` 阶段会先内置检查并安装构建 OFED 所需的前置包，不需要在 `bundle.json` 里额外控制：

- Ubuntu/apt 路径：检查 `linux-headers-$(uname -r)`、`build-essential`、`debhelper`、`fakeroot`；缺失时先执行 `apt-get update`，再 `apt-get install -y <缺失包>`。
- Kylin/yum 路径：检查 `elfutils-devel`；缺失时执行 `yum install -y elfutils-devel`。

`network` 阶段会先根据规划表和本机 `/sys/class/net` 自动发现物理网卡，再打开 NIC Binding Review TUI 让用户确认管理网和 RDMA 网卡绑定。确认后，工具按是否立即应用网络配置决定本轮需要临时重命名的接口，再写入管理网/RDMA 网络配置，最后根据同一份确认结果写入持久化 udev 命名规则。

#### 5.4.1 apply 网卡自动发现与绑定逻辑

评审结论：该逻辑可以用于现场交付，但自动发现只能作为确定性推荐，不能替代最终确认。`apply` 本身要求交互式 TTY；只要存在管理网或 RDMA 命名目标，正式执行都会进入 NIC Binding Review TUI，现场必须核对每个逻辑槽位与物理网卡。能够提前采集 MAC 时，应优先在 inventory 中填写 `mgmtN_mac`、`rdmaN_mac`，这是最可靠的绑定方式。

完整流程如下：

1. **确定规划槽位。** 管理口目标名来自 inventory 的 `mgmt_iface1/2` 或 bundle defaults；RDMA 目标名和数量来自 inventory 的 `rdma1..rdmaN` 或 defaults。`rdma_mode=off` 时不生成 RDMA 槽位；`configure_management_network=false` 时不生成管理网配置和管理网命名动作。
2. **优先按 MAC 精确匹配。** inventory 已填写 MAC 时，通过本机 MAC 索引找到当前接口名。MAC 格式非法、重复选择同一物理网卡或确认后仍有槽位未选择都会直接失败。
3. **采集统一网卡事实。** 从 `/sys/class/net`、PCI 和 `ethtool` 读取接口名、MAC、PCI 地址、驱动、PCI `vendor/device` 型号、当前速率、最大支持速率、MTU、`phys_port_name`、`dev_port`、carrier、operstate 和 RDMA 能力。忽略 `lo`、bond/team、容器 veth、Calico/CNI、bridge、隧道、OVS、虚拟 overlay 等不应参与物理绑定的接口。
4. **先分组再判断角色。** apply 和 discover 共用同一个纯判断模块。它先按 PCI 型号、最大支持速率和 RDMA 能力形成硬件组，再用规划数量选择完整组；当前速率、MTU、链路和已有地址只作为附加依据。断链导致当前速率未知时，仍使用最大支持速率，所以一张未接线的 400G 卡不会被排除出其余 400G 同型号卡组成的组。
5. **精确绑定优先。** 已有 MAC、当前接口名或规划 IP 能精确匹配时直接形成确定性绑定；其他分组依据不能覆盖精确绑定。全新安装且没有任何 IP、默认路由或控制连接证据时不会扣分，仍可根据硬件组和规划数量判断。
6. **处理数量和歧义。** 例如规划一个管理口和四个 RDMA 口、机器呈现 `1 x 100G + 4 x 400G` 时，完整的四卡高速组推荐为 RDMA，剩余单卡推荐为管理口。如果五张卡的型号、最大速率和能力完全相同，又没有 MAC/IP 可以区分“一管理 + 四 RDMA”，结果标记为 `ambiguous`，不能任选四张。
7. **稳定排列逻辑槽位。** 候选组确定后，绑定到 `mgmt1..N`、`rdma1..N` 的顺序固定按 PCI 地址、`dev_port`、`phys_port_name`、当前接口名排列。link/carrier 是辅助依据，不改变已选完整硬件组的稳定顺序。
8. **TUI 最终确认。** TUI 展示规划目标、当前接口名、MAC、PCI、驱动、最大/当前速率、MTU、链路状态，以及简短的 `Why [confidence]`。用户选择的绑定拥有最高优先级，可以覆盖自动推荐；同一物理网卡不能重复使用。确认结果会同步到本轮运行内存，并写入 `/var/lib/envinit/selected_interfaces` 和分离的 management/RDMA udev 规则，但不会反向修改 inventory。为兼容现有 RDMA 服务脚本，`/etc/rdma/rdma_conf/selected_interfaces` 会保留为指向新路径的软链接。
9. **当前启动周期与重启。** RDMA 接口需要供后续 stage 使用，因此确认后会通过临时名中转，安全处理接口名称互换，再改成规划名。管理接口只有在 `apply_network_immediately=true` 时才会在本轮临时改名；为 `false` 时不会 down/rename 当前管理口，只写配置和持久化规则，等重启后生效。

#### 5.4.2 NIC Binding Review 操作

`Why` 后面的置信度帮助判断自动推荐的依据：`exact` 表示 MAC、已有接口名或规划 IP 精确命中；`strong` 表示硬件分组和规划数量能够形成唯一完整组；`weak` 表示只有不足以独立确认的辅助依据；`ambiguous` 表示存在多个等价选择；`conflict` 表示精确线索彼此冲突。人工改选后显示为 `manual`，其优先级最高。

| 按键 | 作用 |
| --- | --- |
| 上下方向键或 `j/k` | 在逻辑槽位、候选网卡或下拉项之间移动 |
| 空格或 `n` | 打开当前槽位的网卡候选；下拉打开时确认当前候选 |
| `1`～`9` | 把画面中对应编号的候选网卡直接绑定到当前槽位 |
| `t` | 打开目标槽位选择，用于把当前绑定切换到另一个 mgmt/RDMA 逻辑槽位 |
| `p` | 对当前选中物理口执行/停止端口闪灯，便于现场定位插槽 |
| `r` | 恢复本机的自动推荐结果 |
| Enter | 校验所有槽位互不重复且完整后接受 |
| Esc | 关闭当前下拉选择，不退出整个 review |
| `q` / Ctrl-C | 放弃本次 apply |

人工确认可以覆盖自动判断，但不会扩大 inventory 槽位数。TUI 中没有“自动把四卡规划扩成八卡”的操作；需要扩容规划时使用 discover 的 `+` 操作并写回 inventory，或者先人工增加 `rdma5..rdma8` 列。

apply 只消费 inventory 中已经规划的槽位，不扩展 inventory。例如机器有八张高速卡但只规划 `rdma1..rdma4` 时，本轮只能绑定四个已有 IP，其余卡保留为未使用候选；需要八卡规划时先补充 inventory，或者在网卡已有地址后使用 discover 确认并扩容。

现场安全约束：

- 带内 SSH 初始化推荐保持 `configure_management_network=false`；工具不会发现、备份、改名、改写或持久化管理网配置。
- 如果确实需要配置管理网，带内环境应设置 `apply_network_immediately=false`。此时管理口不会在运行中临时改名或 reload，但重启后会按已确认规则和新配置生效。
- `apply_network_immediately=true` 会允许管理口临时改名并 reload 网络，可能中断当前 SSH；只建议在本地控制台或具备带外管理时使用。
- 自动排序表达的是稳定硬件顺序，不代表交换机布线或项目规划顺序。涉及固定 leaf、rail、子网或端口语义时必须填写 MAC，或在 TUI 中逐项核对。

单独运行 `udev` 仍然保留为兼容/修复入口，会在需要时进入 NIC Binding Review TUI 并重新生成持久化规则，但常规流程不再需要把 `udev` 作为 `network` 后面的独立步骤。

生成的规则类似：

```text
SUBSYSTEM=="net", ACTION=="add", ATTR{address}=="aa:bb:cc:dd:ee:11", NAME="ens11np0"
```

`network` 会在写入规则后重新加载 udev 规则；当前启动周期的临时重命名也由 `network` 阶段完成。

关键命令作用：

| 命令 | 作用 |
| --- | --- |
| `mlnxofedinstall --add-kernel-support -k <版本>` | 为当前内核安装或构建匹配的 Mellanox OFED 驱动 |
| `udevadm control --reload-rules` | 重新加载 udev 规则，使新的持久化命名配置进入系统 |
| `ip link set <旧名称> name <新名称>` | 在不重启的情况下临时切换 RDMA 接口名，让后续 stage 立即使用统一名称 |

### 5.5 network：配置管理网和 RDMA 网络

启用管理网配置时，Ubuntu/netplan 路径会写入 `/etc/netplan/00-kunlun-bond.yaml`。如果规划表只填写一个管理口，则直接配置单口：

```yaml
network:
  version: 2
  renderer: networkd
  ethernets:
    enp6s0f0np0:
      addresses:
        - 172.19.37.126/24
      mtu: 1500
      routes:
        - to: default
          via: 172.19.37.1
```

如果填写两个管理口，则会根据 bundle 配置 `active-backup` 或 `802.3ad` bond。

当 `rdma_mode` 为 `full` 时，每个 RDMA 口都会生成一份 netplan，例如 `/etc/netplan/10-kunlun-ens11np0.yaml`：

```yaml
network:
  version: 2
  renderer: networkd
  ethernets:
    ens11np0:
      addresses:
        - 10.247.1.11/24
      ignore-carrier: true
      mtu: 9000
```

同时生成 policy route 脚本，例如 `/etc/networkd-dispatcher/routable.d/config_rt_ens11np0.sh`。工具会先确保 `networkd-dispatcher` 已安装并启用，保证接口进入 routable 状态或机器重启后能够重放这些规则。脚本会为对应网卡维护独立路由表：

```bash
ip route replace default via 10.247.1.1 dev ens11np0 table 101
ip route replace 10.247.0.0/21 dev ens11np0 scope link table 101 src 10.247.1.11 proto static
ip rule add from all oif ens11np0 table 101 priority 32761
ip rule add from 10.247.1.11 table 101 priority 32761
```

最后执行：

```bash
netplan generate
netplan apply
```

关键命令作用：

| 命令 | 作用 |
| --- | --- |
| `ip route replace default via ... table 101` | 为某张 RDMA 网卡建立独立路由表，避免多卡流量走错出口 |
| `ip route replace <CIDR> dev ... scope link table 101 src ...` | 在独立路由表中把 RDMA 网段视为二层直连，命中该网段时直接 ARP 查找邻居，不经网关 |
| `ip rule add from all oif ... table 101` | 按出口网卡选择对应路由表 |
| `ip rule add from <RDMA IP> table 101` | 按源 RDMA IP 选择对应路由表，保持回程路径一致 |
| `netplan generate` | 检查 netplan 配置并生成后端网络配置 |
| `netplan apply` | 立即应用管理网和 RDMA 网络配置 |

如果设置 `"rdma_mode": "names_only"`，工具仍会保留 RDMA 命名、RoCE adaptive routing 和后续调优，但跳过 RDMA IP、netplan 和 policy route。

### 5.6 xre、xdr、firmware 和 container：安装算力卡软件

| Stage | 操作说明 |
| --- | --- |
| `xre` | 使用 `KERNELDIR=/usr/src/linux-headers-<内核版本>` 执行 XRE `.run` 安装包 |
| `xdr` | 解压源码，运行 `build.sh`，刷新模块依赖和 initramfs，再运行 `install.sh` |
| `firmware` | 解压固件包，在物料目录中执行 `bash auto_update.sh` |
| `container` | 按 `artifacts.container_packages` 数组顺序执行 `dpkg -i` |

`P900` 安装完成后不执行额外 XRE 调优。`P800` 会读取 `xpu-smi -q` 判断卡型；对于 VD 卡，工具会写入 `/etc/modprobe.d/kunlun.conf` 并重新加载 `kunlun` 模块。

关键命令作用：

| 命令 | 作用 |
| --- | --- |
| `KERNELDIR=... bash <xre_installer>` | 使用当前内核头文件安装 XRE 驱动 |
| `xpu-smi -q` | 查询 XPU 信息；P800 场景下用于识别 VC 或 VD 卡型 |
| `rmmod` / `modprobe` | 卸载并重新加载内核模块，使 P800 VD 调优配置生效 |
| `depmod` | 刷新内核模块依赖关系 |
| `dracut -f` 或 `update-initramfs -u` | 刷新 initramfs，保证模块在后续启动时可正确加载 |
| `dpkg -i <deb...>` | 安装 XPU 容器运行时相关离线软件包 |

### 5.7 mlxconfig、sysctl 和 kernel：系统调优

`mlxconfig` 会执行 `mst start`，自动扫描 `/dev/mst/*_pciconf*`。如果能通过 RDMA 网卡 PCI 地址关联到 MST 设备，会默认推荐这些设备；关联不上或现场需要调整时，会进入 MST Device Review TUI 让用户确认。确认后仅修改不一致的参数。例如：

```bash
mlxconfig -y -d /dev/mst/mt4129_pciconf0 set LINK_TYPE_P1=2
```

上述示例表示工具会按 `mlxconfig.settings` 中的键值逐项设置。CNP DSCP 不需要写在 `mlxconfig.settings` 中，工具会在 post boot 脚本中固定写入 `48`。

`sysctl` 会将缺少的网络参数追加到 `/etc/sysctl.conf`，再执行 `sysctl -p`。部分示例：

```text
net.core.rmem_max = 212992000
net.core.wmem_max = 212992000
net.ipv4.conf.ens11np0.arp_ignore=2
net.ipv4.conf.ens11np0.rp_filter=2
```

部分 sysctl 参数作用：

| 参数 | 作用 |
| --- | --- |
| `net.core.rmem_*`、`net.core.wmem_*` | 增大 socket 收发缓冲区，避免高速网络场景下缓冲空间过小 |
| `net.ipv4.tcp_rmem`、`net.ipv4.tcp_wmem` | 调整 TCP 自动调优的收发缓冲区范围 |
| `arp_ignore=2`、`arp_announce=1` | 降低多网卡同网段环境中的 ARP 混乱和错误应答 |
| `rp_filter=2` | 使用宽松反向路径检查，适配多张 RDMA 网卡和策略路由 |
| `disable_ipv6=0` | 保持指定 RDMA 接口的 IPv6 能力启用 |

`kernel` 会确保 `/etc/default/grub` 包含以下内核参数：

```text
rw biosdevname=0 iommu=pt mitigations=off nokaslr
```

然后执行 `update-grub`；如果系统没有该命令，则使用 `grub2-mkconfig`。

内核参数作用：

| 参数 | 作用 |
| --- | --- |
| `rw` | 以可读写模式挂载根文件系统 |
| `biosdevname=0` | 关闭基于 BIOS 的网卡命名，减少接口名变化来源 |
| `iommu=pt` | 让 IOMMU 使用 passthrough 模式，降低设备访问开销 |
| `mitigations=off` | 关闭部分 CPU 安全缓解措施以降低性能开销；使用前应确认符合现场安全要求 |
| `nokaslr` | 关闭内核地址空间布局随机化，便于某些驱动或现场调试场景保持内核地址稳定 |

旧的 `--stages iommu` 仍作为兼容别名可用，内部会按 `kernel` stage 执行。

### 5.8 post：开机服务、附加任务和电源动作

当 RDMA 启用时，工具会写入并启用：

```text
/usr/local/sbin/kunlun-post-boot.sh
/etc/systemd/system/kunlun-post-boot.service
```

该服务在开机后遍历 RDMA 网卡，执行 ACSCtl、MaxReadReq、ring buffer、RoCE adaptive routing 和 CNP DSCP 等调优。典型命令包括：

```bash
setpci -s <PCI 地址> ECAP_ACS+06.w=0000
setpci -s <PCI 地址> CAP_EXP+8.w=<目标值>
ethtool -G ens11np0 rx 8192 tx 8192
mlxreg -d <PCI 地址> --reg_name ROCE_ACCL --set adaptive_routing_forced_en=0x1 --yes
echo 48 > /sys/class/net/ens11np0/ecn/roce_np/cnp_dscp
```

关键命令作用：

| 命令 | 作用 |
| --- | --- |
| `ethtool -G ens11np0 rx 8192 tx 8192` | 将网卡接收和发送 ring buffer 深度设置为 `8192`。更深的 ring buffer 可以在突发流量或 CPU 短暂繁忙时容纳更多待处理报文，降低高速 RDMA 场景下因队列不足造成丢包的风险 |
| `ethtool -i ens11np0` | 查询网卡驱动信息和 PCI `bus-info`，供后续 `mlxreg` 精确定位硬件设备 |
| `setpci ... ECAP_ACS+06.w=0000` | 按项目要求关闭 ACSCtl 相关控制位 |
| `setpci ... CAP_EXP+8.w=...` | 设置 PCIe MaxReadReq |
| `mlxreg -d <PCI 地址> --reg_name ROCE_ACCL --set adaptive_routing_forced_en=0x1 --yes` | 修改 Mellanox 网卡的 `ROCE_ACCL` 寄存器，强制启用 RoCE adaptive routing。其目的是允许网络根据路径状态进行自适应路由，降低热点链路对 RDMA 流量的影响 |
| `echo 48 > .../cnp_dscp` | 固定 RoCE CNP DSCP 为项目要求的 `48` |
| `systemctl enable kunlun-post-boot.service` | 将上述调优注册为开机服务，确保服务器重启后自动重新应用 |

`post` 阶段会先按顺序安装 `post_packages`，再写入上述开机服务，然后按顺序执行 `post_tasks`。最后根据 `post_power_action` 决定是否执行类似命令：

```bash
ipmitool power soft
```

默认会先要求人工确认。详细配置示例见 [7.9 post_packages、post_tasks 和 post_power_action](#79-post_packagespost_tasks-和-post_power_action)。

### 5.9 宿主机上的持久化和临时内容

完整 apply 后，宿主机可能留下以下内容。实际写入项由所选 stage、系统 profile、`rdma_mode` 和是否配置管理网决定：

| 位置 | 来源 | 生命周期和用途 |
| --- | --- | --- |
| `/var/lib/envinit/apply-progress.json` | 默认全流程 apply | 失败续跑 checkpoint，配置或目标变化时自动失效；`--restart` 删除后重建 |
| `/var/lib/envinit/selected_interfaces` | `network` / `udev` | 人工确认后的管理网和 RDMA 物理口绑定，供后续运行和启动时复用 |
| `/etc/rdma/rdma_conf/selected_interfaces` | `network` / `udev` | 指向 `/var/lib/envinit/selected_interfaces` 的兼容软链接，不再单独维护第二份选择数据 |
| `/var/lib/envinit/mst-devices.json` | `mlxconfig` | 已确认的 `/dev/mst/*_pciconf*` 设备列表；后续运行优先复用，设备状态不一致时重新确认 |
| `/opt/kunlun-apt-repo`、`/opt/kunlun-rpm-repo` 或 bundle 的 `copy_to` | `software` | 从交付目录复制到本机的离线软件源 |
| `/etc/apt/sources.list.d/kunlun-offline.list` 或 `/etc/yum.repos.d/kunlun-offline.repo` | `software` | envinit 管理的本地软件源配置；是否禁用其他源由 `platform_options` 控制 |
| `/opt/kunlun` 或 `artifacts.work_dir` | `ofed`、`xre`、`xdr`、`firmware` | 安装包解压和执行工作目录，不是 apply checkpoint |
| `/etc/netplan/` 或 `/etc/sysconfig/network-scripts/` 中 envinit 管理的文件 | `network` | 管理网/RDMA 地址、路由和 policy rule；关闭对应网络配置时不会写该侧文件 |
| `/etc/udev/rules.d/70-kunlun-management-net.rules`、`71-kunlun-rdma-net.rules` | `network` / `udev` | 重启后保持确认过的接口名；管理网和 RDMA 分开维护 |
| NetworkManager dispatcher 或 networkd-dispatcher 脚本 | `network` | 接口重新 up 后重放 RDMA 路由和 rule，具体路径随系统后端变化 |
| `/etc/sysctl.conf`、`/etc/default/grub` | `sysctl` / `kernel` | 持久化内核网络参数和启动参数；工具只追加缺少项或更新所管理的参数 |
| `/usr/local/sbin/kunlun-post-boot.sh`、`/etc/systemd/system/kunlun-post-boot.service` | `post` | 每次开机恢复 ACS、MaxReadReq、ring buffer、adaptive routing 和 CNP DSCP 调优 |
| `post_packages`、`post_tasks` 指定的目标 | `post` | 属于用户在 bundle 中明确要求安装、复制、移动、创建或执行的内容 |

`check` 不使用 apply checkpoint。bandwidth 和 rdma-ping 只产生测试流量与输出；XCCL 会在 `check.xccl.work_root/<run-id>` 创建本轮运行时，并可能临时创建 `/var/lib/envinit/check-runtime/mpich-5.0.1` 软链接和带本轮标记的 `authorized_keys` 行。正常结束或中途失败都会进入清理，只删除本轮拥有的目录、软链接和授权行；已有系统 MPICH、其他 SSH key 和 SSH 配置不会被删除。

## 6. planning.csv 结构

### 6.1 推荐表头

| 类型 | 推荐 CSV 列 |
| --- | --- |
| 机器信息 | `host_id`, `hostname` |
| 管理网 | `mgmt_ip`, `mgmt_prefix`, `mgmt_gateway`, `mgmt_bond_name`, `mgmt_nameservers` |
| 管理口 1 | `mgmt_iface1`, `mgmt_mac1` |
| 管理口 2 | `mgmt_iface2`, `mgmt_mac2` |
| RDMA 口 1 | `rdma1_name`, `rdma1_ip`, `rdma1_mac`, `rdma1_prefix`, `rdma1_gateway`, `rdma1_table`, `rdma1_route_cidr` |
| RDMA 口 2 | `rdma2_name`, `rdma2_ip`, `rdma2_mac`, `rdma2_prefix`, `rdma2_gateway`, `rdma2_table`, `rdma2_route_cidr` |
| RDMA 口 3 | `rdma3_name`, `rdma3_ip`, `rdma3_mac`, `rdma3_prefix`, `rdma3_gateway`, `rdma3_table`, `rdma3_route_cidr` |
| RDMA 口 4 | `rdma4_name`, `rdma4_ip`, `rdma4_mac`, `rdma4_prefix`, `rdma4_gateway`, `rdma4_table`, `rdma4_route_cidr` |
| 更多 RDMA 口 | `rdma5_name`, `rdma5_ip` ... |

工具也支持 `.tsv`、`.txt` 和 `.xlsx`。使用 `.xlsx` 时默认读取第一张表，也可以增加 `--sheet Sheet1`。

### 6.2 字段说明

| 字段 | 必填条件 | 说明 |
| --- | --- | --- |
| `host_id` | 建议填写 | 机器标识，可用于 `--host`；discover 中可作为 `--hosts host_id=SSH地址` 的左侧写回身份 |
| `hostname` | 建议填写 | 目标 hostname；显式传入 `--host` 执行 `apply` 时会自动修正 |
| `mgmt_ip` | 可选 | 管理网 IPv4 地址；为空表示该机器不由工具配置管理网；执行 `discover` 时可自动写回 |
| `mgmt_prefix` | 配置管理网时可选 | 管理网前缀，例如 `23`；为空时使用 bundle 默认值 |
| `mgmt_gateway` | 配置管理网时可选 | 管理网网关；为空时依次使用 bundle 默认值或按管理 IP 推导 `.1` |
| `mgmt_iface1`、`mgmt_iface2` | 配置管理网时可选 | 管理口名；为空时使用 bundle 默认值或自动发现/TUI 绑定 |
| `mgmt_mac1`、`mgmt_mac2` | 配置管理网时建议填写 | 管理口 MAC；填写后优先按 MAC 找真实网卡 |
| `mgmt_bond_name` | 可选 | 管理 bond 名称；为空时使用 bundle 默认值 |
| `mgmt_nameservers` | 可选 | DNS，可使用逗号、分号、竖线或空格分隔 |
| `rdmaN_name` | 建议填写 | 第 N 个 RDMA 口的目标接口名；执行 `discover` 时可由 `show_gids` 自动写回 |
| `rdmaN_ip` | 按模式填写 | 开启 RDMA IP 路由配置或执行 `rdma-ping` 时必须填写；执行 `discover` 时可由 `show_gids` 自动写回 |
| `rdmaN_mac` | 强烈建议填写 | 第 N 个 RDMA 口 MAC |
| `rdmaN_prefix` | 可选 | 第 N 个 RDMA 前缀；为空时使用 bundle 默认值 |
| `rdmaN_gateway` | 可选 | 第 N 个 RDMA 网关；为空时按 RDMA IP 推导 `.1` |
| `rdmaN_table` | 可选 | 第 N 个 RDMA 路由表号；为空时使用 bundle 对应项，仍未配置时按顺序使用 `101、102、103...` |
| `rdmaN_route_cidr` | 可选 | 第 N 个 RDMA 直连网段；通常按 `rdmaN_ip/prefix` 自动推导，仅特殊路由规划时覆盖 |

其中 `N` 从 `1` 开始，可按机器实际 RDMA 口数量扩展，例如 8 卡机器可填写到 `rdma8_name` / `rdma8_ip`。

接口解析优先级为：

```text
MAC -> planning.csv 中的接口名 -> bundle.json 中的默认接口名
```

如果只填写 `mgmt_iface1` 或 `mgmt_mac1`，并把第二个管理口留空，工具会配置单管理口，不创建 bond。

### 6.3 示例：不配置 RDMA IP

当 `bundle.json` 中设置 `"rdma_mode": "names_only"` 时，可以只规划 RDMA 网卡名：

基础与管理网：

| `host_id` | `hostname` | `mgmt_ip` | `mgmt_prefix` | `mgmt_gateway` | `mgmt_iface1` | `mgmt_mac1` | `mgmt_iface2` | `mgmt_mac2` |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `node1` | `node1` | `10.157.5.207` | `23` | `10.157.4.1` | `ens12f0np0` | 留空 | `ens12f1np1` | 留空 |

RDMA 网：

| 端口 | `rdmaN_name` | `rdmaN_ip` | `rdmaN_mac` |
| --- | --- | --- | --- |
| `rdma1` | `ens15np0` | 留空 | 留空 |
| `rdma2` | `ens16np0` | 留空 | 留空 |
| `rdma3` | `ens13np0` | 留空 | 留空 |
| `rdma4` | `ens14np0` | 留空 | 留空 |

这种模式仍会执行 RDMA udev 命名、RoCE adaptive routing、ring buffer 调优和 `mlxconfig`，但不会写 RDMA netplan、路由和 policy rule，也不能做 `rdma-ping`。

### 6.4 示例：配置 RDMA IP 和路由

基础与管理网：

| `host_id` | `hostname` | `mgmt_ip` | `mgmt_prefix` | `mgmt_gateway` | `mgmt_iface1` | `mgmt_mac1` | `mgmt_iface2` | `mgmt_mac2` |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `xpu11` | `xpu11` | `10.101.9.11` | `26` | `10.101.9.1` | `ens20f0np0` | `aa:bb:cc:dd:ee:01` | `ens20f1np1` | `aa:bb:cc:dd:ee:02` |

RDMA 网：

| 端口 | `rdmaN_name` | `rdmaN_ip` | `rdmaN_mac` |
| --- | --- | --- | --- |
| `rdma1` | `ens11np0` | `11.1.1.11` | `aa:bb:cc:dd:ee:11` |
| `rdma2` | `ens13np0` | `11.1.2.11` | `aa:bb:cc:dd:ee:12` |
| `rdma3` | `ens15np0` | `11.1.3.11` | `aa:bb:cc:dd:ee:13` |
| `rdma4` | `ens17np0` | `11.1.4.11` | `aa:bb:cc:dd:ee:14` |

### 6.5 如何写规划表

建议按以下顺序整理：

1. 为每台机器确定唯一的 `host_id` 和 `hostname`。
2. 记录管理 IP、前缀和网关。
3. 如果现场已经有明确端口信息，记录管理口和 RDMA 口的 MAC；没有 MAC 时可留空，由工具在 `network` 阶段自动发现并进入 TUI 复核。
4. 固定物理端口到 `rdma1` 至 `rdmaN` 的规划含义。同一列必须表示同一类物理端口。
5. 如果需要 RDMA 三层网络，填写每个 `rdmaN_ip` 并使用 `rdma_mode=full`；如果只需要 RDMA 命名和调优，使用 `rdma_mode=names_only`。
6. 每台机器先运行一次 `plan --host <host_id>`，确认解析结果再执行 `apply`。

## 7. bundle.json 结构

bundle 使用严格 JSON 字段校验：字段名拼写错误、已经删除的字段或其他未知字段都会直接报错，不会静默采用默认值。修改配置后应先运行 `env_init plan`，确认文件能够加载且网络安全选项符合预期。

### 7.1 顶层结构

```json
{
  "defaults": {},
  "platform": {},
  "platform_options": {},
  "offline_apt": {},
  "offline_repo": {},
  "packages": [],
  "artifacts": {},
  "xre": {},
  "mlxconfig": {},
  "check": {},
  "post_packages": [],
  "post_tasks": [],
  "post_power_action": {}
}
```

### 7.2 defaults

`defaults` 存放整批机器共用的网络默认值：

| 字段 | 说明 |
| --- | --- |
| `mgmt_bond_name` | 管理 bond 名，默认 `bond0` |
| `mgmt_interfaces` | 默认管理口目标名列表；通常可省略，由规划表和自动发现/TUI 绑定生成 |
| `mgmt_prefix`、`mgmt_gateway`、`mgmt_nameservers`、`mgmt_mtu` | 管理网默认值 |
| `bond_mode` | 常用值为 `active-backup` 或 `802.3ad` |
| `bond_primary` | `active-backup` 的主口，必须在管理口列表中 |
| `bond_mii_monitor_interval` | `active-backup` 链路探测间隔，默认 `100` |
| `bond_lacp_rate`、`bond_transmit_hash_policy` | `802.3ad` 参数 |
| `configure_management_network` | 是否配置管理网；默认 `true`。关闭后只配置 RDMA 网络 |
| `apply_network_immediately` | 是否立即应用网络配置；默认 `true`。关闭后只落文件，不执行 `netplan apply`、`nmcli up` 或 `ifup`。示例配置默认关闭；对已经通过带内网络接入的机器，强烈建议保持为 `false`，避免工具运行过程中重载当前网络导致失联 |
| `rdma_mode` | RDMA 工作模式：`full`、`names_only` 或 `off`，默认 `full` |
| `rdma_prefix`、`rdma_mtu`、`route_priority` | RDMA 三层网络默认值；`rdma_prefix` 未填写时默认为 `/24`，单机可由规划表的 `rdmaN_prefix` 覆盖 |
| `rdma_interfaces` | RDMA 默认目标名、路由表号和可选网关；通常可省略，由规划表和自动发现/TUI 绑定生成 |

平台专属的备份、禁用源策略建议放到 `platform_options`。旧配置里放在 `defaults` 下的 `backup_existing_netplan`、`backup_existing_network`、`disable_existing_apt_sources`、`disable_existing_repos` 仍然兼容。

如果某台机器不需要工具配置管理网，可以在 inventory 中留空 `mgmt_ip`。此时该机器会自动跳过管理网配置，也不会要求选择管理网卡；RDMA 网络、udev 持久化命名和其他 stage 仍按规划执行。若只是整批关闭管理网，也可以在 bundle 中设置 `"configure_management_network": false`。

RDMA 工作模式：

```json
"rdma_mode": "full"
```

`full` 表示存在 RDMA 网卡，并配置 RDMA IP、路由、policy rule、命名、`mlxconfig` 和 post 调优。`names_only` 表示存在 RDMA 网卡，但只做发现、绑定、命名、`mlxconfig` 和 post 调优，不配置 RDMA IP/路由。`off` 表示没有 RDMA 网卡，跳过所有 RDMA 相关动作。

`rdma_interfaces` 仅作为兼容旧配置或在 inventory 完全没有 RDMA 条目时提供回退。新配置建议由 inventory 的 `rdmaN_name`、`rdmaN_ip`、`rdmaN_table` 描述实际网卡；只要某台机器存在 RDMA 条目，它的条目数量就是该机器的 RDMA 数量，bundle 不会再把它扩展成固定四卡。未显式填写路由表号时仍按逻辑顺序自动使用 `101、102、103...`。

RDMA 直连路由默认按每张 RDMA 口的 `rdmaN_ip` 和 `rdmaN_prefix` 自动推导，例如 `172.18.12.10/25` 会生成 `172.18.12.0/25` 的 scope link 路由。旧配置中的 `rdma_route_cidr` 和 `rdmaN_route_cidr` 仍然兼容，但新配置通常不需要填写。

`post` 阶段会写入并启用 `/usr/local/sbin/kunlun-post-boot.sh` 和 `kunlun-post-boot.service`，并在工具运行当次立即重启一次该 service，确保脚本当场执行。该脚本会在开机后重放 RDMA 调优，包括 ACSCtl、MaxReadReq、ring buffer、RoCE adaptive routing，以及固定写入 `CNP_DSCP=48`。

只保留 RDMA 非 IP 动作：

```json
"rdma_mode": "names_only",
"rdma_interfaces": [
  { "name": "ens15np0" },
  { "name": "ens16np0" },
  { "name": "ens13np0" },
  { "name": "ens14np0" }
]
```

完全没有 RDMA 网卡：

```json
"rdma_mode": "off"
```

### 7.3 platform

不配置 `platform`，或将 `platform.os_family`、`package_manager`、`network_backend` 写成 `auto` 时，工具会根据当前系统自动选择 Ubuntu/Debian 或 yum 系路径。当前交付样例建议显式填写 Ubuntu 或 Kylin，减少现场自动探测带来的歧义。

Kylin V10 SP3 建议显式启用 yum 路径：

```json
"platform": {
  "os_family": "kylin",
  "package_manager": "yum",
  "network_backend": "auto"
}
```

说明：

- `package_manager=yum` 时，依赖安装使用 `yum makecache` 和 `yum install -y`。
- `network_backend=auto` 时，运行时优先检测 `network` 服务；若 legacy `network` 正在管理网络，则写入 `NM_CONTROLLED=no` 的 ifcfg 并执行 `ifup`。否则检测 `NetworkManager`，写入 `NM_CONTROLLED=yes` 的 ifcfg，执行 `nmcli connection reload/up`，并写入 `/etc/NetworkManager/dispatcher.d/90-kunlun-rdma-routes` 用于开机或连接 up 后重放 RDMA 路由规则。
- `kernel_headers_package` 与 `kernel_headers_dir` 通常无需配置，工具会按平台生成默认值；只有现场包名或内核源码目录非标准时才需要覆盖。
- `software` 是软件源和依赖安装的标准 stage 名；`apt`、`yum`、`packages`、`repo` 作为兼容别名继续可用。

### 7.4 platform_options

`platform_options` 存放平台专属的保护和清理策略。工具会按当前 `platform` 选择对应子项，子项缺省时回退到旧的 `defaults` 同名字段。

```json
"platform_options": {
  "ubuntu": {
    "backup_existing_netplan": true,
    "disable_existing_apt_sources": false
  },
  "kylin": {
    "backup_existing_network": true,
    "disable_existing_repos": false
  }
}
```

| 字段 | 说明 |
| --- | --- |
| `ubuntu.backup_existing_netplan` | Ubuntu 路径：是否在重写 envinit 管理的 mgmt/RDMA netplan 目标文件前备份原文件；不会备份或移动其他 netplan YAML |
| `ubuntu.disable_existing_apt_sources` | Ubuntu 路径：是否备份并禁用已有 apt 源 |
| `kylin.backup_existing_network` | Kylin ifcfg 路径：是否在重写 envinit 管理的 mgmt/RDMA ifcfg、route、rule 目标文件前备份原文件；不会备份或移动其他网络脚本 |
| `kylin.disable_existing_repos` | Kylin yum 路径：是否备份并禁用已有 `.repo` 文件 |

旧配置中的 `platform_options.redhat` 仍会作为兼容回退读取；新配置统一使用 `platform_options.kylin`。

### 7.5 offline_apt / offline_repo 和 packages

```json
"offline_apt": {
  "enabled": true,
  "material_path": "data/apt-repo",
  "copy_to": "/opt/kunlun-apt-repo",
  "target_file": "/etc/apt/sources.list.d/kunlun-offline.list",
  "entries": []
},
"packages": [
  "linux-headers-{{uname_r}}",
  "ipmitool",
  "bzip2",
  "gcc"
]
```

说明：

- `material_path` 是 U 盘中的离线源目录。推荐写成 `data/...` 相对路径，并从 `env_tool/` 目录执行工具。
- `copy_to` 是复制到目标机后的路径。
- `entries` 为空时，工具会为 apt 自动生成默认本地源条目。需要自定义时支持占位符 `{{offline_apt_target}}`，运行时替换为 `copy_to`。
- `packages` 通过 `apt-get install -y` 安装。
- `packages` 支持占位符 `{{uname_r}}`，运行时替换为当前内核版本。

Kylin yum 路径建议使用 `offline_repo`：

```json
"offline_repo": {
  "enabled": true,
  "material_path": "data/rpm-repo",
  "copy_to": "/opt/kunlun-rpm-repo",
  "target_file": "",
  "entries": []
}
```

`target_file` 和 `entries` 通常无需配置，工具会按 yum 平台生成 `/etc/yum.repos.d/kunlun-offline.repo` 和默认 repo 内容。若现场有特殊 repo 模板，`offline_repo.entries` 仍支持 `{{offline_repo_target}}`，运行时替换为 `copy_to`。

### 7.6 artifacts 和 xre

```json
"artifacts": {
  "work_dir": "/opt/kunlun",
  "ofed_archive": "data/hca/mellanox/MLNX_OFED_LINUX-24.01-0.3.3.1-ubuntu22.04-x86_64.tgz",
  "xre_installer": "data/xpu_driver/xre-Linux-x86_64-5.19.0.0.run",
  "xre_args": ["-q"],
  "xdr_archive": "data/xpu_driver/xdr_copy-x86_64_1.1.0.6.tar.gz",
  "firmware_archive": "data/xpu_firmware/p800/update_fw_p800_2.15_1.48.tar.gz",
  "container_packages": [
    "data/xpu_container_toolkit/libxpu-container-tools_1.0.5-1_amd64.deb",
    "data/xpu_container_toolkit/libxpu-container1_1.0.5-1_amd64.deb",
    "data/xpu_container_toolkit/xpu-container-toolkit_1.0.13-1_amd64.deb"
  ]
},
"xre": {
  "card_model": "P800"
}
```

注意：

- JSON 中必须填写真实完整路径，不能使用通配符。上例是 Ubuntu profile，Kylin 应使用其 bundle 样例中的 `.tar` OFED 和 `.rpm` 容器包路径。
- 这些路径同样按当前工作目录解析。使用 `data/...` 时必须先 `cd /mnt/usb/env_tool`。
- 配置 `xre_installer` 时，必须填写 `xre.card_model`，可选值为 `P800`、`P900`。
- `P800` 安装建议在 `packages` 中增加 `lsof`。
- Ubuntu 路径下 `container_packages` 按数组顺序传给 `dpkg -i`；Kylin/yum 路径下按数组顺序传给 `yum localinstall -y`，因此 Kylin 需要提供 `.rpm` 制品。

### 7.7 mlxconfig

```json
"mlxconfig": {
  "settings": {}
}
```

工具会运行 `mst start`，自动扫描 `/dev/mst/*_pciconf*`，让用户确认要配置的 MST 设备，并把选择持久化到 `/var/lib/envinit/mst-devices.json`。后续运行会优先复用该持久化选择。`device_glob` 仍作为兼容旧配置的强制覆盖项保留，通常不需要填写。

MST Device Review 中使用上下方向键或 `j/k` 移动，空格切换当前设备是否选中，`r` 恢复推荐/默认选择，Enter 接受，`q` 或 Ctrl-C 放弃。至少要选择一个设备。TUI 会展示 MST 路径、PCI 地址、设备类型和当前 NET/PCI 状态；不要把与本项目 RDMA 数据面无关的 Mellanox 设备仅因为路径相似就一起选中。

`CNP_DSCP` 不再需要放在 `mlxconfig.settings` 中；工具会在 post boot 脚本中按项目要求固定写入 `48`，确保当前运行和重启后都生效。

### 7.8 check

`check` 是检查功能的顶层配置，按职责拆成四个子对象：

| 子对象 | 功能 |
| --- | --- |
| `check.ssh` | `discover`、`check`、远端命令和物料分发共用的 SSH 控制通道 |
| `check.bandwidth` | `ib_write_bw` 带宽流、XDR mmap 和吞吐门槛 |
| `check.rdma_ping` | 绑定源 RDMA 网卡的大包 IPv4 ping |
| `check.xccl` | MPICH/Hydra 启动的 XCCL 集合通信测试 |

```json
"check": {
  "bandwidth": {
    "duration": 0,
    "gid_index": 0,
    "iterations": 100,
    "bandwidth_qps": 0,
    "message_size": 0,
    "report_gbits": true,
    "mmap_device": "",
    "min_gbits": "auto",
    "parallel": false,
    "base_port": 0
  },
  "rdma_ping": {
    "count": 3,
    "payload_size": 8972,
    "timeout": 2
  },
  "ssh": {
    "user": "root",
    "options": []
  },
  "xccl": {
    "enabled": true,
    "mpich_archive": "data/misc/mpich-5.0.1-ubuntu22.04-x86_64.tar.gz",
    "xccl_archive": "data/misc/xccl_Linux_x86_64-3.2.2.0.tar.gz"
  }
}
```

`check.ssh` 参数：

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `user` | `root` | SSH/SCP 登录用户 |
| `options` | `[]` | 原样追加到 `ssh` 和 `scp` 的公共参数；端口建议写成 `["-o","Port=22"]`，固定私钥建议使用 `["-o","IdentityFile=/path/key"]`，避免直接使用只属于 ssh 的 `-p` 或只属于 scp 的 `-P` |

远端目标如果被识别为本机，会直接执行本地 shell 或本地复制，不再套 SSH。远端 SSH/SCP 遇到握手重置、连接超时、`MaxStartups` 等暂态错误时最多尝试三次。

`check.bandwidth` 参数：

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `iterations` | `100` | 传给 `ib_write_bw -n` 的迭代次数 |
| `run_by_duration` | `false` | `true` 时使用 `-D duration -f 2 -N`，不再使用 `-n`；交互向导默认启用 |
| `bandwidth_qps` | `0` | QP 数；正数传给 `-q`，`0` 使用 perftest 默认值；命令行 `--bandwidth-qps` 优先 |
| `min_gbits` | `"auto"` | `"auto"` 按两端网卡最大支持速率的较小值乘以 70% 逐流判定；`0` 只记录；正数使用固定 Gbps 门槛 |
| `parallel` | `false` | 是否按“不让同一批中的同一客户端/服务端 RDMA 口重复占用”的规则分批并发 |
| `base_port` | `18515` | 第一条 `ib_write_bw` 流端口，后续流依次递增 |
| `message_size` | `0` | 运行时派生字段；当前命令行会先清零，仅 `--emu-kv-transfer` 设置为 8 MiB |
| `mmap_device` | 空 | 运行时派生字段；当前命令行会先清空，仅 `--bandwidth-mmap xdr` 设置为 `/dev/xdrdrv` |
| `report_gbits` | `true` | 输出使用 `--report_gbits`，汇总单位为 `Gbps` |
| `duration` | `1` | `run_by_duration=true` 时传给 `ib_write_bw -D`；交互向导默认调整为 10 秒 |
| `gid_index` | `3` | Verbs 模式传给 `ib_write_bw -x`；RDMA-CM 模式由对端 RDMA IP 选路 |
| `rdma_groups` | 省略 | 旧 bundle 兼容字段；新配置从 inventory/defaults 的 `rdmaN_name`、sysfs 和 topo 动态生成 |

`check.rdma_ping` 参数：

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `count` | `3` | 每条路径发送的 ping 包数；可由 `--rdma-ping-count` 覆盖 |
| `payload_size` | `8972` | IPv4 payload；`8972 + 20 字节 IP + 8 字节 ICMP = MTU 9000` |
| `timeout` | `2` | 每个 ping 包的等待秒数；可由 `--rdma-ping-timeout` 覆盖 |

`check.xccl` 的完整参数、拓扑生成、环境变量、单机/多机启动和清理机制见 8.6 节。

新 bundle 应使用上述嵌套结构。旧 bundle 中扁平放在 `check` 下的 bandwidth、RDMA ping、SSH 字段仍可读取；新旧结构同时存在时以嵌套结构为准。旧 SSH 字段为 `ssh_user`、`ssh_options`，旧 ping 字段为 `rdma_ping_count`、`rdma_ping_payload_size`、`rdma_ping_timeout`；它们只用于兼容已有 bundle，新配置不要继续新增这些扁平字段。

### 7.9 post_packages、post_tasks 和 post_power_action

`post_packages` 用于按顺序安装额外本地包。Ubuntu 路径使用 `dpkg -i`，yum 路径使用 `yum localinstall -y`：

```json
"post_packages": [
  "data/single_deb/extra-package.deb"
]
```

`post_tasks` 用于执行安装后的附加动作，支持 `copy`、`cmd`、`mv`、`rm` 和 `mkdir`，下列以安装 `xpu_exporter` 为例：

```json
"post_tasks": [
  {
    "name": "install xpu_exporter",
    "type": "copy",
    "source": "data/xpu_exporter/xpu_exporter",
    "target": "/usr/local/bin/xpu_exporter",
    "mode": "0755"
  },
  {
    "name": "install xpu_exporter service",
    "type": "copy",
    "source": "data/xpu_exporter/xpu_exporter.service",
    "target": "/etc/systemd/system/xpu_exporter.service",
    "mode": "0644"
  },
  {
    "name": "enable and start xpu_exporter",
    "type": "cmd",
    "command": "systemctl daemon-reload && systemctl enable xpu_exporter && systemctl restart xpu_exporter"
  }
]
```

各任务类型使用的字段：

| `type` | 必填字段 | 可选字段 | 行为 |
| --- | --- | --- | --- |
| `copy` | `source`、`target` | `name`、`mode` | 把交付物料复制到目标路径；`mode` 使用 `0644`、`0755` 这类八进制字符串 |
| `cmd` | `command` | `name` | 以 root 运行 `bash -lc <command>`；可执行任意命令，交付前必须先在 plan 中核对展开结果 |
| `mv` | `source`、`target` | `name` | 移动宿主机路径 |
| `rm` | `path` | `name` | 删除指定宿主机路径 |
| `mkdir` | `path` | `name`、`mode` | 创建目录，默认权限 `0755` |

`copy.source` 和 bundle 里的其他物料路径一样，`data/...` 相对当前执行目录解析。`mv/rm/mkdir` 操作的是宿主机目标路径，不会自动限制在 `artifacts.work_dir` 内；这些任务应视为 bundle 明确授权的 root 操作。

最终电源动作：

```json
"post_power_action": {
  "action": "soft",
  "confirm": true
}
```

`action` 支持 `soft`、`off`、`cycle`、`reset`、`on`、`status` 和 `none`。不希望自动操作电源时使用：

```json
"post_power_action": {
  "action": "none"
}
```

`confirm=true` 时必须在交互终端输入完整的 `yes` 才执行；非交互输入会跳过。整个 `post_power_action` 省略时默认是 `soft + confirm=true`；如果显式配置了某个 action 但省略 `confirm`，则不会再次询问，因此新配置建议始终明确写出 `confirm`，避免误解。

## 8. discover 和 check：网络发现与验收

### 8.1 discover：发现并写回检查网络

#### 8.1.1 功能和边界

`env_init discover` 解决的是“现场没有完整 inventory，后续 check 不知道通过哪个管理地址登录、也不知道哪些 RDMA 网卡和地址应参测”的问题。它通过本地执行或 SSH 运行只读命令，发现候选网络信息，然后按 inventory 的逻辑槽位进行确认和写回。

它只写 inventory 文件，不修改 bundle，也不会修改目标机的网卡名、IP、路由、SSH 配置或其他系统状态。真正修改目标机网络的是 `apply --stages network`，两者不要混淆。

最基本的调用：

```bash
sudo ./env_init discover \
  --inventory planning/inventory.csv \
  --bundle planning/bundle.json \
  --hosts node-a=192.168.32.11,node-b=192.168.32.12
```

discover 把“写 inventory 的目标身份”和“本次 SSH 控制地址”分开处理。`--hosts` 支持三种形式：

| 输入 | 解析方式 | 适用场景 |
| --- | --- | --- |
| `192.168.32.11` | 先用该地址 SSH，读取远端 `hostname`，再匹配 inventory 的 `hostname`，其次匹配 `host_id`；没有匹配行时以远端 hostname 新增一行 | 装机后只知道临时接入 IP |
| `node-a` | 兼容原有方式；先匹配 inventory 的 `host_id`/`hostname`/`mgmt_ip`，有 `mgmt_ip` 就用它 SSH，没有则尝试直接连接 `node-a` | inventory 已基本完整或现场 DNS 可解析 |
| `node-a=192.168.32.11` | 强制把 SSH 地址绑定到指定 inventory 身份；身份可以是已有行，也可以是准备新增的 `host_id` | 最可靠的现场用法，尤其适合未配置正式管理网的机器 |

通过 IP 直连时，如果远端 hostname 没有匹配 inventory，discover 会用该 hostname 同时作为新行的 `host_id` 和 `hostname`，再写入发现并确认的 `mgmt_ip`、`rdmaN_name`、`rdmaN_ip` 等字段。如果 hostname 同时匹配多行，工具会停止并提示改用 `inventory-id=SSH地址`，避免误写。显式映射具有最高优先级；远端 hostname 与已有行不一致时会输出警告，但仍只更新明确指定的行。

SSH 控制地址在本次 discover 全程保持不变。即使发现阶段选出了新的 `mgmt_ip`，后续采集仍走最初可达的控制地址，避免尚未配置的新地址导致连接中断；最终只把确认后的管理地址写回 inventory。

#### 8.1.2 发现流程和选择原理

每台目标机依次经过以下流程：

1. **建立控制连接**：解析普通目标或 `inventory身份=SSH地址`。对未直接命中 inventory 的地址，先通过 `hostnamectl --static`（失败时回退 `hostname`）取得远端身份；本机直接运行命令，远端使用 `check.ssh`。
2. **锁定写回身份**：远端 hostname 唯一匹配已有行时更新该行，没有匹配时以 hostname 新增一行，匹配多行时拒绝继续；显式映射只认左侧 inventory 身份。控制地址与待写回 `mgmt_ip` 分离，整个发现过程不会中途切换 SSH 地址。
3. **采集地址和 RDMA 能力**：读取默认路由及所有 global IPv4，并执行 `show_gids`。候选入口统一过滤 `169.254.0.0/16` link-local、loopback、unspecified、multicast 和广播地址，即使这些地址被标成 `scope global` 也不会进入 TUI。一个接口出现多条 GID 时优先 RoCE v1 记录。`show_gids` 只说明接口具备 RDMA 能力，不直接决定它属于数据面；因此像 `eth0/mlx5_0` 这样有正常业务地址且有 GID 的管理口仍会保留在管理候选中。
4. **采集硬件事实**：读取每张接口的 MAC、PCI、驱动、PCI 型号、最大/当前速率、MTU、链路和物理端口信息。discover 与 apply 把这些信息转换成同一事实模型，区别只在于 discover 通过本地命令或 SSH 远端采集。
5. **统一分组判断**：先应用 inventory 中已有 MAC、接口名和 IP 的精确匹配，再按“型号 + 最大速率 + RDMA 能力”分组，结合规划数量选择完整的管理/RDMA 硬件组。默认路由、当前地址、MTU 和链路只增强判断，不会让一个弱信号覆盖完整硬件组。
6. **review 或自动接受**：默认打开 Network Discovery Review，展示每个槽位和候选的最大/当前速率、MTU、链路及简短 `Why [confidence]`。数字键选择是人工硬覆盖；同一网卡不能同时绑定管理网和 RDMA。`weak`、`ambiguous`、`conflict` 结果不能被 `--yes` 静默接受，必须交互确认。
7. **按最终结果写回**：更新 `mgmt_ip` 和每个已确认 RDMA 槽位的 name、IP、prefix、MAC，bundle 保持不变。扩容依据是自动强推荐或 TUI 最终确认的数量，不是原始 `show_gids` 条目数。
8. **动态扩展表头**：模板只有四个槽位而最终确认六卡或八卡时，追加到 `rdma6`/`rdma8`。新增槽位复用模板已有字段布局，例如 `name、ip、prefix、gateway、mac、table、route_cidr`；没有可靠采集值的 gateway/table/route CIDR 保持空白。多机器以最大确认数量作为公共表头，卡数较少的行留空。
9. **安全写回和缩容**：写入使用同目录临时文件和原子替换，并保留原文件权限。数量减少时清空该机器多余尾部槽位，但不删除公共表头。日志和 dry-run 会显示例如 `inventory RDMA slots: 4 -> 8`。

这里的 `rdma1`、`rdma2` 是项目逻辑顺序，不是 `mlx5_1`、`mlx5_2`。discover 会展示 `show_gids` 返回的 IB device 供人判断，但不会把固定 `mlx5_N` 写入 bundle。check 会在每台机器上根据最终确认的 `rdmaN_name` 从 sysfs 重新解析实际 IB device。inventory 已有 RDMA 条目时，apply 和 check 都以该机器的实际条目数量为准；只有完全没有 RDMA 条目时才回退到 bundle 的 `defaults.rdma_interfaces`。

#### 8.1.3 review 操作

Review 左侧是当前机器的 `mgmt`、`rdma1..rdmaN` 槽位，右侧是管理网或 RDMA 候选：

| 按键 | 作用 |
| --- | --- |
| `Tab` / `Shift+Tab`、左右方向键 | 切换目标机器 |
| 上下方向键、`j` / `k` | 切换当前逻辑槽位 |
| 数字键 | 把右侧对应候选绑定到当前槽位 |
| `+` / `-` | 增加一个空 RDMA 槽位，或删除最后一个 RDMA 槽位 |
| 空格、Backspace、Delete | 清空当前槽位 |
| `r` | 恢复当前机器的自动选择 |
| Enter | 校验全部机器并确认 |
| `q` / Ctrl-C | 放弃本次发现 |

确认前必须为每台机器选择管理网，并为所有 RDMA 槽位选择互不重复的候选。这样可以在没有 MAC 的场景下由现场人员明确决定逻辑端口顺序。

#### 8.1.4 discover 参数

| 参数 | 必需 | 说明 |
| --- | --- | --- |
| `--inventory <path>` | 是 | 输入 inventory；支持读取 CSV/TSV/TXT/XLSX |
| `--bundle <path>` | 是 | 主要读取 `check.ssh` 作为控制通道配置 |
| `--hosts <list>` | 是 | 逗号、空格、分号或竖线分隔；每项可写 inventory 身份、SSH 地址或 `inventory身份=SSH地址` |
| `--sheet <name>` | 否 | XLSX 工作表名；缺省读取第一张表 |
| `--yes` | 否 | 不打开 review；只接受 `exact/strong` 自动结论，歧义或冲突仍会失败 |
| `--dry-run` | 否 | 完成发现和可选 review，但不写 inventory |

常见模式：

```bash
# 只有装机后的临时 IP：SSH 后按远端 hostname 唯一匹配 inventory
./env_init discover --inventory planning/inventory.csv --bundle planning/bundle.json --hosts 192.168.32.11

# 显式指定 inventory 身份和 SSH 地址；推荐用于批量现场交付
./env_init discover --inventory planning/inventory.csv --bundle planning/bundle.json --hosts node-a=192.168.32.11,node-b=192.168.32.12 --yes

# 非交互环境只预览；--yes 用于跳过必须依赖 TTY 的 review
./env_init discover --inventory planning/inventory.csv --bundle planning/bundle.json --hosts node-a=192.168.32.11,node-b=192.168.32.12 --yes --dry-run
```

写回仅支持 `.csv`、`.tsv`、`.txt`。工具会保留原分隔符和文件权限，缺少 `mgmt_ip` 或 RDMA 槽位列时自动追加；裸 SSH 地址取得的远端 hostname 没有匹配行时自动新增一行，显式 `新身份=SSH地址` 也可以指定新行的 `host_id`；同一 hostname 或目标匹配多行时拒绝写入。XLSX 当前只支持读取，不支持 discover 写回。

如果某台机器没有任何 IPv4 `show_gids` 候选，discover 直接失败。如果没有合法管理网候选，默认 review 无法确认该机器；工具不会退回使用 RDMA IP 冒充管理 IP。

### 8.2 check：统一检查入口

#### 8.2.1 功能、子检查和执行顺序

`env_init check` 是验收入口。它负责选择目标、生成真实测试拓扑、执行一个或多个子检查，并把吞吐、连通性、拓扑退化和硬件错误计数统一汇总为进程退出状态。

| 子检查 | 验证内容 | 主机数量 |
| --- | --- | --- |
| `bandwidth` | `ib_write_bw` 点对点 RDMA 写带宽；可选 XDR mmap/KV cache 模式 | 至少 2 台 |
| `rdma-ping` | 所有参测 RDMA 口之间的 IPv4 大包、无分片连通性 | 至少 2 台 |
| `xccl` | 单机或多机 XPU 集合通信、XCCL/MPICH 运行时和 RoCE 数据路径 | 允许 1 台 |

交互终端中省略 `--hosts` 会先进入 `Hosts -> Checks -> Parameters -> Review` 配置向导：从 inventory 勾选机器，选择 Ping/Bandwidth/XCCL，并对本轮参数做临时覆盖；Review 会显示双向交叉矩阵数量，确认后才开始远端发现和测试。向导不会写回 bundle。Bandwidth 默认同时生成独立的 `BW Verbs` 和 `BW RDMA-CM` stage：Verbs 使用管理地址交换控制信息、通过两端 `IB device/GID index` 建立数据路径且不传 `-R`；RDMA-CM 使用对端规划 RDMA IP 并传 `-R`。两种模式分别展示结果、热力图、RDMA Counter Delta 和 Raw Logs。

显式 CLI 模式保持兼容：默认 `--check-stage all` 执行 bandwidth 和 rdma-ping；只有 `check.xccl.enabled=true` 时才把 XCCL 加入默认流程。未通过向导指定 bandwidth mode 的旧命令仍只运行原有 RDMA-CM 模式。显式使用 `--check-stage xccl` 时会直接执行 XCCL，用于单机 smoke test 或临时覆盖默认开关。

一次非 dry-run 的总体顺序是：

1. 从配置向导或 `--hosts` 解析目标并识别本地目标。通常使用 inventory 的 `mgmt_ip`；也可以用 `inventory身份=SSH地址` 只覆盖本次控制地址。
2. 对目标机实际 hostname 与 inventory 不一致的情况打印警告，但仍按选定的管理 IP 继续。
3. bandwidth/XCCL 按每个 `rdmaN_name` 从 `/sys/class/net/<iface>/device/infiniband/` 解析本机真实 `mlx5_N`；XDR bandwidth 在起流前读取 `xpu-smi topo -m`，XCCL 在自身 stage 内读取 topo。
4. 采集所有参测 RDMA 网卡的 NIC 计数器 before 快照。
5. 执行 rdma-ping；交互终端的 check TUI 会先显示完整双向交叉矩阵，初始状态为 `WAIT`，起测后改为 `RUNNING`，每条路径完成后在对应条目填入 `PASS/FAIL`。若启用 bandwidth/XCCL，再采集 IB device 计数器 before 快照。
6. 执行 bandwidth，再执行 XCCL。bandwidth 同样预先生成完整流矩阵；命令失败时，选中对应条目并按 `Space` 可分别查看客户端和服务端的完整错误与原始输出。XCCL 会预先按消息 size 和 out-of-place/in-place 模式生成条目，收到性能行后填入 time、algbw 和 busbw。
7. 采集 IB device 和 NIC after 快照，计算 delta。
8. 每个参测检查在 TUI 中固定复用 `Results / Counter Delta / Raw Logs` 三个选项卡。Results 保留原汇总表的全部列，`Up/Down` 选择行，`PgUp/PgDown` 翻阅主列表；`Space` 打开或关闭右侧/下侧完整详情，`Ctrl+U/Ctrl+D` 单独翻阅详情；`Left/Right` 可横向查看超宽表。Counter Delta 和 Raw Logs 同样使用 `PgUp/PgDown` 翻阅主内容。Bandwidth Results 页按 `p` 可切换列表/热力图，热力图中按 `m` 切换测试方向并用方向键选择单元格；XCCL Results 页按 `p` 切换表格/折线图、按 `m` 切换 out-of-place/in-place。Counter Delta 直接展示原有 NIC/RDMA compare 逻辑生成的完整 summary，不另做统计；Raw Logs 保存该检查的逐项结果及命令原始输出。使用 `Tab/Shift+Tab` 切换三个页面，使用 `[`/`]` 切换 Ping/Bandwidth/XCCL stage，底部会明确显示 `[/]: switch stage`。测试运行中，Results 页按 `q` 只中止光标选中的 Ping/Bandwidth 项，已完成项不会响应；由于 XCCL 的全部 size/mode 共用同一个 mpirun 进程，XCCL 页按 `q` 会中止当前整个 XCCL stage，并给出明确提示。按一次 `Esc` 中止当前 stage；1.5 秒内连续按两次 `Esc` 中止整条 check。若本次只选择了一个 stage，一次 `Esc` 中止该 stage 后，check 会在清理完成后自然结束。`Ctrl-C` 仍用于立即中止整条 check 并退出 TUI。所有中止都会终止对应本地/SSH 进程并进入已有的 bandwidth/XCCL 清理流程，最终返回非零。整轮 check 完成后，可在 Ping 或 Bandwidth 的 Results 列表/热力图中选中一条链路并按 `Enter` 单独重测；该行会回到 `RUNNING` 并在完成后原位更新，最新详情和带 `RETEST` 前缀的记录写入 Raw Logs。为避免重测流量污染首次检查的并发性能与 counter 快照，运行过程中不会立即重测；人工重测是诊断复核，不覆盖首次检查的总退出结论和 Counter Delta，按 `q` 退出并中止仍在执行的重测。TUI 只改变展示方式，不改变门槛判断和退出状态；重定向到文件或非交互环境时仍输出普通汇总表和完整错误。任一命令失败、吞吐低于门槛、ping 丢包或异常计数器增长，最终 check 返回非零。

#### 8.2.2 check 公共参数

| 参数 | 必需/默认 | 说明 |
| --- | --- | --- |
| `--inventory <path>` | 必需 | 读取目标管理 IP、RDMA 逻辑顺序、接口名和地址 |
| `--bundle <path>` | 必需 | 读取 `check.ssh/bandwidth/rdma_ping/xccl` |
| `--hosts <list>` | 交互可省略 | 交互终端省略时进入配置向导；非交互、`--dry-run` 或 `--no-tui` 时必需。值支持 `host_id`、`hostname`、`mgmt_ip` 或 `inventory身份=SSH地址` |
| `--sheet <name>` | 第一张表 | XLSX inventory 工作表 |
| `--check-stage <list>` | `all` | `bandwidth`、`rdma-ping`、`xccl` 或组合；`--checks` 是弃用别名 |
| `--dry-run` | `false` | 不启动测试流量；不同子检查的只读发现边界见下文 |
| `--no-tui` | `false` | 即使 stdout 是交互终端也禁用 check TUI，改用普通文本结果；适合脚本和伪终端 |
| `--bandwidth-qps <n>` | bundle 值 | 覆盖 `check.bandwidth.bandwidth_qps`，必须非负 |
| `--emu-kv-transfer` | `false` | 把 bandwidth message size 设置为 8 MiB |
| `--bandwidth-mmap xdr` | 关闭 | 启用 `/dev/xdrdrv` mmap，并按真实 XPU/NIC 拓扑生成 offset |
| `--rdma-ping-count <n>` | bundle 值 | 覆盖 `check.rdma_ping.count` |
| `--rdma-ping-mtu <n>` | 未指定 | 按 `MTU-28` 覆盖 IPv4 payload；必须大于 28 |
| `--rdma-ping-timeout <s>` | bundle 值 | 覆盖每个 ping 包超时 |

完整检查示例：

```bash
sudo ./env_init check \
  --inventory planning/inventory.csv \
  --bundle planning/bundle.json
```

上述命令进入配置向导。自动化和非交互环境继续显式提供 `--hosts node-a,node-b`。

`--dry-run` 的含义不是所有模式都完全不访问远端：

- 普通 bandwidth dry-run 不解析远端 IB device，用 `<resolve-ib-device:接口名>` 打印命令。
- XDR bandwidth dry-run 必须通过 SSH 只读查询 sysfs 和 `xpu-smi topo -m`，否则无法生成真实 mmap offset。
- XCCL dry-run 同样执行 sysfs、管理接口和 topo 的只读发现，但不分发文件、不生成临时密钥、不修改 `authorized_keys`、不启动流量。
- dry-run 不采集 before/after 计数器，也不产生真实吞吐结果。

check 的显式映射只覆盖控制通道，不会像 discover 一样按远端 hostname 新增或补齐 inventory。例如 `--hosts node-a=192.168.32.11` 要求左侧 `node-a` 已能唯一匹配 inventory，这样工具才能取得该机器的 `rdmaN_name`、`rdmaN_ip` 和逻辑顺序。不要只给一个未匹配 inventory 的裸 IP 直接执行 check；应先 discover 写回，或使用已有 inventory 身份进行显式映射。

#### 8.2.3 SSH 控制通道

`check.ssh.user` 和 `check.ssh.options` 同时服务于 discover、远端命令、SCP 分发和 XCCL 的外层控制连接。例如：

```json
"ssh": {
  "user": "root",
  "options": [
    "-o", "Port=22",
    "-o", "BatchMode=yes",
    "-o", "ConnectTimeout=10",
    "-o", "IdentityFile=/root/.ssh/id_ed25519"
  ]
}
```

这里应使用 ssh/scp 都支持的 `-o Key=Value` 形式。目标被识别为执行机本机时不会经过 SSH。连接重置、握手 banner、连接超时等暂态错误最多重试三次；认证失败等确定性错误不会盲目重试。

#### 8.2.4 前置条件

| 功能 | 执行机需要 | 目标机需要 | inventory 需要 |
| --- | --- | --- | --- |
| `discover` | `ssh`；本机目标无需 SSH | `hostnamectl` 或 `hostname`、`ip`、`show_gids` | 可按远端 hostname 匹配或新增；匹配多行时使用 `inventory身份=SSH地址`；允许暂时没有 `mgmt_ip` |
| `bandwidth` | 能控制所有目标 | `ib_write_bw`、`ethtool`、可读取 RDMA sysfs | 必须有 `rdmaN_name`；正式 RDMA 验收应同时有 `rdmaN_ip` |
| `rdma-ping` | 能控制所有目标 | IPv4 `ping`、`ethtool` | 每个参测槽位必须同时有 `rdmaN_name`、`rdmaN_ip` |
| XDR bandwidth | 同 bandwidth | 支持 mmap 的 `ib_write_bw`、`xpu-smi`、`/dev/xdrdrv` | `rdmaN_name`，topo 的直接 `mlx5_N` 列或 NIC legend 必须包含对应 IB device |
| `xccl` | 多机模式需要 `ssh-keygen`；物料路径从当前工作目录读取 | `xpu-smi`、XRE/XPU 动态库、tar；可运行临时 MPICH/XCCL | 所有机器的 XPU 数和按 XPU 排列的 RDMA 接口顺序必须一致 |

`check` 默认使用 inventory 中的管理 IP 建立控制连接；管理 IP 不可达或尚未写入时，可以用 `inventory身份=SSH地址` 临时覆盖，但 inventory 仍必须存在该机器的 RDMA 规划。标准 bandwidth 在某个目标缺少 `rdmaN_ip` 时会回退到该目标的控制地址作为 peer address，这只是一项兼容行为，通常不能代表规划的 RDMA 数据面；正式验收应先通过 discover 或人工方式补齐 RDMA IP。bundle 中使用 `data/...` 的 MPICH/XCCL 路径时，仍必须从 `env_tool/` 目录启动命令。

### 8.3 bandwidth：标准 RDMA 带宽检查

#### 8.3.1 功能和测试矩阵

标准 bandwidth 使用 OFED/perftest 自带的 `ib_write_bw`。每条流在服务端后台启动一个 server，在客户端用对应 RDMA 地址连接；命令使用 `-R` 通过 RDMA CM 建链、`-F` 忽略 CPU frequency 警告，并通过两端各自的真实 IB device 绑定端口。

对于每一对机器，工具执行两个方向。若客户端有 N 个逻辑 RDMA 口、服务端有 M 个逻辑 RDMA 口，则每个方向生成 `N × M` 条流，不仅测试同编号端口。因此两台 4 卡机器共有 `2 × 4 × 4 = 32` 条标准带宽流。

`parallel=false` 时逐流执行。`parallel=true` 时自动分批：同一批内一个客户端 RDMA group 和一个服务端 RDMA group 都只出现一次，避免多条流同时争抢同一张 400G 卡；不同批次仍覆盖完整交叉矩阵。

#### 8.3.2 参数和判定

主要 bundle 参数是：

| 参数 | 对测试的影响 |
| --- | --- |
| `check.bandwidth.iterations` | `ib_write_bw -n` |
| `check.bandwidth.bandwidth_qps` | 正数时添加 `-q` |
| `check.bandwidth.base_port` | 第一条 server 监听端口，后续流递增 |
| `check.bandwidth.parallel` | 启用无端口冲突的分批并发 |
| `check.bandwidth.min_gbits` | `"auto"` 时探测两端最大支持速率并按木桶效应逐流计算 70% 下限；`0` 不限制；正数使用固定 `BW average[Gb/sec]` 门槛 |

临时使用 4 个 QP：

```bash
sudo ./env_init check \
  --inventory planning/inventory.csv \
  --bundle planning/bundle.json \
  --hosts node-a,node-b \
  --check-stage bandwidth \
  --bandwidth-qps 4
```

#### 8.3.3 示例环境与动态 IB device

下面用两台测试机举例。每台机器有四张 400G RDMA 网卡，单卡正常带宽约为 `390 Gbps`：

| 主机 | 管理 IP | `rdma1_ip` | `rdma2_ip` | `rdma3_ip` | `rdma4_ip` |
| --- | --- | --- | --- | --- | --- |
| `node-a` | `192.168.50.11` | `10.80.1.11` | `10.80.2.11` | `10.80.3.11` | `10.80.4.11` |
| `node-b` | `192.168.50.12` | `10.80.1.12` | `10.80.2.12` | `10.80.3.12` | `10.80.4.12` |

RDMA 网卡与 IB 设备的对应关系：

| 规划表字段 | RDMA 网卡 | 运行时解析示例 | 预期单流带宽 |
| --- | --- | --- | --- |
| `rdma1_ip` | `ens11np0` | `mlx5_1` | 约 `390 Gbps` |
| `rdma2_ip` | `ens13np0` | `mlx5_2` | 约 `390 Gbps` |
| `rdma3_ip` | `ens15np0` | `mlx5_3` | 约 `390 Gbps` |
| `rdma4_ip` | `ens17np0` | `mlx5_4` | 约 `390 Gbps` |

实际执行带宽测试时，工具不会假设所有机器的 `mlx5_N` 编号完全一致。它会按第 N 个 RDMA 网卡名解析本机 IB device，例如某台机器可能是 `rdma1_name=ens11np0 -> mlx5_1`，另一台机器也可能是 `rdma1_name=ens11np0 -> mlx5_2`；带宽命令会分别使用各自解析出的实际设备。这样可以避免 PCI 探测顺序不同导致 `mlx5_N` 整体偏移时测错卡。

对应的规划表示例：

| `host_id` | `hostname` | `mgmt_ip` | `rdma1_name` | `rdma1_ip` | `rdma2_name` | `rdma2_ip` | `rdma3_name` | `rdma3_ip` | `rdma4_name` | `rdma4_ip` |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `node-a` | `node-a` | `192.168.50.11` | `ens11np0` | `10.80.1.11` | `ens13np0` | `10.80.2.11` | `ens15np0` | `10.80.3.11` | `ens17np0` | `10.80.4.11` |
| `node-b` | `node-b` | `192.168.50.12` | `ens11np0` | `10.80.1.12` | `ens13np0` | `10.80.2.12` | `ens15np0` | `10.80.3.12` | `ens17np0` | `10.80.4.12` |

如果希望低于 `380 Gbps` 时直接判定失败，可以在 `bundle.json` 中设置：

```json
"check": {
  "bandwidth": {
    "iterations": 100,
    "bandwidth_qps": 0,
    "min_gbits": "auto",
    "parallel": true
  },
  "rdma_ping": {
    "count": 3,
    "payload_size": 8972,
    "timeout": 2
  },
  "ssh": {
    "user": "root",
    "options": []
  }
}
```

`min_gbits` 默认使用 `"auto"`。Bandwidth stage 开始时会在每台目标机一次性读取所有参测 RDMA 网卡的当前速率和 `ethtool` Supported link modes 最大速率；每条流取客户端与服务端最大速率的较小值作为 baseline，并以 baseline 的 70% 为通过门槛。例如 400G ↔ 200G 的 baseline 是 200 Gbps、门槛是 140 Gbps。若任一端无法取得最大速率，该流仍会执行并保留实测值，但判定为 `FAIL auto threshold unavailable`，详情会给出两端探测值。显式设置 `0` 可只记录、不按吞吐失败；设置正数（例如 `380`）则保持固定门槛模式。

#### 8.3.4 执行命令与结果

```bash
sudo ./env_init check \
  --inventory /mnt/usb/env_tool/planning/inventory.csv \
  --bundle /mnt/usb/env_tool/planning/bundle.json \
  --hosts node-a,node-b \
  --check-stage bandwidth
```

正式执行时，输出中的 `CLIENT_NIC` / `SERVER_NIC` 是规划表或 discover 得到的实际网卡名，`CLIENT_IP` / `SERVER_IP` 是该流两端的 RDMA IP，`CLIENT_DEV` / `SERVER_DEV` 是每台机器按网卡名解析后的实际 `mlx5_N`。

正常输出示意：

```text
Bandwidth result summary:
STATUS  CLIENT  SERVER  CLIENT_NIC  SERVER_NIC  CLIENT_IP     SERVER_IP     CLIENT_DEV  SERVER_DEV  PORT   CLIENT_XPU  SERVER_XPU  CLIENT_TOPO  SERVER_TOPO  BANDWIDTH
PASS    node-a  node-b  ens11np0    ens11np0    10.247.1.11  10.247.1.12  mlx5_1      mlx5_2      18515  -           -           -            -            391.42 Gbps
PASS    node-a  node-b  ens13np0    ens13np0    10.247.2.11  10.247.2.12  mlx5_2      mlx5_3      18520  -           -           -            -            389.87 Gbps
PASS    node-a  node-b  ens15np0    ens15np0    10.247.3.11  10.247.3.12  mlx5_3      mlx5_4      18525  -           -           -            -            390.16 Gbps
PASS    node-a  node-b  ens17np0    ens17np0    10.247.4.11  10.247.4.12  mlx5_4      mlx5_5      18530  -           -           -            -            388.94 Gbps
```

实际输出会包含完整交叉矩阵。只要某一行低于该行解析出的 auto/固定门槛，或 auto 模式无法取得两端最大速率，该行会标记为 `FAIL`，整个 `check` 返回失败。交互终端在 Results 页按 `p` 可在列表和 Bandwidth 热力图之间切换；热力图按客户端 NIC/XPU 为行、服务端 NIC/XPU 为列，按 `m` 切换方向，方向键移动单元格，`Space` 查看两端速率、baseline、门槛、利用率和原始输出。auto 模式下 ≥90% baseline 显示绿色、70%～90% 显示黄色、低于 70% 显示红色；`min_gbits=0` 使用中性色。

### 8.4 rdma-ping：RDMA 大包连通性检查

#### 8.4.1 功能和矩阵

rdma-ping 不测试 RDMA verbs 吞吐，而是验证承载 RoCE 的 IPv4 网络是否满足大 MTU、源接口选择和双向可达。实际命令为：

```text
ping -c <count> -W <timeout> -M do -s <payload_size> -I <source-rdma-nic> <destination-rdma-ip>
```

`-M do` 禁止 IPv4 分片，因此 payload 8972 能验证端到端 MTU 9000；`-I` 强制从指定 RDMA 接口发包，可以暴露策略路由、源地址选择、VLAN/交换网络或跨子网配置问题。

和 bandwidth 一样，每对机器执行两个方向，并执行源端 N 张网卡到目标端 M 个 RDMA IP 的完整 `N × M` 交叉矩阵。两台 4 卡机器共 32 条 ping 路径。成功条件是命令输出明确包含 `0% packet loss`，并非只看进程是否返回 0。

#### 8.4.2 参数、并发和结果

| 参数 | 作用 |
| --- | --- |
| `check.rdma_ping.count` | `ping -c`，默认 3 |
| `check.rdma_ping.payload_size` | `ping -s`，默认 8972 |
| `check.rdma_ping.timeout` | `ping -W`，默认 2 秒 |
| `--rdma-ping-mtu 9000` | 自动计算 payload 为 `9000-28=8972`，覆盖 bundle |

一个机器对、一个方向内并发执行；源目标为本机时最多 8 条，远端通过 SSH 时最多 4 条，降低 sshd `MaxStartups` 压力。远端握手暂态错误还会按公共 SSH 规则重试。

```bash
sudo ./env_init check \
  --inventory /mnt/usb/env_tool/planning/inventory.csv \
  --bundle /mnt/usb/env_tool/planning/bundle.json \
  --hosts node-a,node-b \
  --check-stage rdma-ping \
  --rdma-ping-count 10 \
  --rdma-ping-mtu 9000 \
  --rdma-ping-timeout 3
```

rdma-ping 要求所有参测逻辑槽位同时具备 `rdmaN_name` 和 `rdmaN_ip`。任何字段缺失都会在起测前列出具体机器和字段，而不会缩小矩阵后继续。

正常输出示意：

```text
RDMA ping result summary:
STATUS  SOURCE  DEST    SOURCE_NIC  DEST_NIC  SOURCE_IP     DEST_IP      PAYLOAD  RESULT
PASS    node-a  node-b  ens11np0    ens11np0  10.247.1.11  10.247.1.12  8972     ok
PASS    node-a  node-b  ens13np0    ens13np0  10.247.2.11  10.247.2.12  8972     ok
PASS    node-b  node-a  ens11np0    ens11np0  10.247.1.12  10.247.1.11  8972     ok
PASS    node-b  node-a  ens13np0    ens13np0  10.247.2.12  10.247.2.11  8972     ok
```

### 8.5 XDR mmap：按 XPU/NIC 拓扑模拟 KV cache 传输

#### 8.5.1 功能和命令参数

模拟 8 MiB KV cache 传输并启用 XDR mmap：

```bash
sudo ./env_init check \
  --inventory /mnt/usb/env_tool/planning/inventory.csv \
  --bundle /mnt/usb/env_tool/planning/bundle.json \
  --hosts node-a,node-b \
  --check-stage bandwidth \
  --emu-kv-transfer \
  --bandwidth-mmap xdr
```

使用 `--bandwidth-mmap xdr` 时，不再需要手工维护网卡与 `xpu_offsets` 的对应关系；真实运行和 `--bandwidth-mmap xdr --dry-run` 都会通过 SSH 执行只读发现：从 sysfs 解析实际 IB 设备，并在每台机器执行一次 `xpu-smi topo -m`，因此 dry-run 能生成包含真实 `mlx5_N` 和 mmap offset 的完整 `ib_write_bw` 命令。dry-run 仍不会启动带宽流、采集前后计数器或修改目标机。普通非 mmap dry-run 不执行远端发现，会用 `<resolve-ib-device:网卡名>` 标记运行时才会解析的 IB 设备。

`--emu-kv-transfer` 把 `ib_write_bw -s` 设置为 8 MiB；`--bandwidth-mmap xdr` 增加 `--mmap=/dev/xdrdrv` 和每个 XPU 对应的 `--mmap-offset`。两个参数可独立使用，但 PD 分离/KV cache 场景通常同时指定。

#### 8.5.2 topo 映射和流生成

每台机器独立完成以下映射：

1. 只在 inventory 参测的 `rdmaN_name` 中选择网卡，并从 sysfs 解析其真实 `mlx5_N`。
2. 解析 `xpu-smi topo -m` 的 XPU/NIC 矩阵；同时兼容 `NIC0... + NIC Legend` 和直接以 `mlx5_0...` 作为矩阵列名的输出。
3. 对每个 XPU 按 `PIX -> PXB -> PHB -> NODE -> SYS` 选择最近等级；同一等级存在多个等距网卡时，bandwidth 会保留所有等距映射，用于覆盖全部合理路径。
4. 根据 XPU 编号生成 offset：`(xpu_index << 60) + 0x90001000`。
5. 只对“本机 XPU + 本机最近网卡”的合法组合生成流，再对客户端合法组合与服务端合法组合做交叉测试。不会生成某个 XPU 强行使用本机远端 NIC 的无意义路径。

机器间 `mlx5_N` 可以不同，因为两端分别解析；机器有 4 卡或 8 卡也不需要修改 bundle。参测 IB device 不在 topo 的直接网卡列或 NIC legend 中、或某张参测网卡没有任何最近 XPU 时直接失败，防止静默使用错误 offset。

#### 8.5.3 退化提示

正常映射在 `CLIENT_TOPO`、`SERVER_TOPO` 显示为 `PIX`。如果某个 XPU 没有 PIX，只能使用 `PXB`、`PHB`、`NODE` 或 `SYS`，解析阶段打印 `WARN xdr topology degraded`，结果行状态为 `WARN`，拓扑列显示例如 `NODE(DEGRADED)`。

退化本身不等于测试失败，因为链路仍可能可用；但结果可能受 PCIe bridge 或 NUMA 限制，不能和正常 PIX 流直接比较。若该流同时低于 `min_gbits`，状态仍为 `FAIL`。

例如服务端 XPU 的最近可用网卡关系退化为 `NODE` 时，会看到：

```text
WARN xdr topology degraded: node-b rdma2 ib_device=mlx5_2 offset=0x1000000090001000 link=NODE; PIX is unavailable, bandwidth may be limited by the PCIe/NUMA path

Bandwidth result summary:
STATUS  CLIENT  SERVER  CLIENT_NIC  SERVER_NIC  CLIENT_IP     SERVER_IP     CLIENT_DEV  SERVER_DEV  PORT   CLIENT_XPU          SERVER_XPU          CLIENT_TOPO  SERVER_TOPO    BANDWIDTH
WARN    node-a  node-b  ens11np0    ens13np0    10.247.1.11  10.247.2.12  mlx5_1      mlx5_2      18515  0x0000000090001000  0x1000000090001000  PIX          NODE(DEGRADED)  82.50 Gbps
WARN bandwidth topology: 1 completed stream(s) used non-PIX XPU/NIC mappings; bandwidth may be limited by the PCIe/NUMA path
```

### 8.6 XCCL：单机/多机集合通信检查

#### 8.6.1 功能、模式和边界

XCCL 检查使用交付物中的预编译 MPICH runtime 和原版 XCCL perf，不使用容器，也不会在现场编译 MPI：

```bash
sudo ./env_init check \
  --inventory /mnt/usb/env_tool/planning/inventory.csv \
  --bundle /mnt/usb/env_tool/planning/bundle.json \
  --hosts node-a,node-b \
  --check-stage xccl
```

也支持先用一台场内机器做单机 XCCL smoke test：

```bash
sudo ./env_init check \
  --inventory /mnt/usb/env_tool/planning/inventory.csv \
  --bundle /mnt/usb/env_tool/planning/bundle.json \
  --hosts node-a \
  --check-stage xccl \
  --dry-run

sudo ./env_init check \
  --inventory /mnt/usb/env_tool/planning/inventory.csv \
  --bundle /mnt/usb/env_tool/planning/bundle.json \
  --hosts node-a \
  --check-stage xccl
```

单机模式仍会完成真实 XPU/RDMA 拓扑发现、运行时分发、动态库检查、逐 rank 环境注入、XCCL perf 结果解析和清理；MPI rank 数等于该机发现到的 XPU 数量，例如 8 卡启动 8 个 rank。Hydra 使用本地 `fork` launcher，不生成临时 SSH 密钥、不创建 hostfile，也不会读取或修改目标机的 `authorized_keys`。执行机远程控制该节点时，最外层物料分发和命令执行仍使用 `check.ssh.user`/`check.ssh.options`。

单机模式只允许 `--check-stage xccl`。`bandwidth`、`rdma-ping` 以及包含它们的默认 `--check-stage all` 仍要求至少两台机器。单机结果可以验证 MPICH、XCCL、XRE/XPU 动态库、rank 启动和机内集合通信，但不能替代跨机 RoCE、交换网络、PFC/CNP 和实际 PD 分离链路验证。

#### 8.6.2 参数和交付运行时

bundle 配置示例：

```json
"xccl": {
  "enabled": true,
  "mpich_archive": "data/misc/mpich-5.0.1-ubuntu22.04-x86_64.tar.gz",
  "xccl_archive": "data/misc/xccl_Linux_x86_64-3.2.2.0.tar.gz",
  "work_root": "/tmp/envinit-xccl-check",
  "xpu_home": "/usr/local/xpu",
  "test": "all_reduce",
  "min_bytes": "1024",
  "max_bytes": "256m",
  "step_factor": 2,
  "warmup_iterations": 5,
  "iterations": 20,
  "data_type": "float",
  "timeout": 120,
  "enable_xdr": true,
  "supernode": false,
  "socket_interface": "",
  "min_bus_bandwidth_gbs": 0,
  "environment": {}
}
```

参数说明：

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `enabled` | `false` | 是否加入默认 `check --check-stage all`；显式指定 `--check-stage xccl` 时仍会执行 |
| `mpich_archive` | 无 | 当前 profile 的预编译 MPICH 5.0.1 runtime，执行 XCCL 时必需 |
| `xccl_archive` | 无 | 包含 `systest/xccl_perf` 和 `so/` 的 XCCL 原始包，执行 XCCL 时必需 |
| `work_root` | `/tmp/envinit-xccl-check` | 每轮远端临时目录的父目录；必须是 `/tmp` 或 `/var/tmp` 下的专用绝对路径 |
| `xpu_home` | `/usr/local/xpu` | XRE/XPU 用户态安装根目录，必须是绝对路径 |
| `test` | `all_reduce` | 支持 `all_reduce`、`all_gather`、`all_to_all`、`broadcast`、`reduce`、`reduce_scatter`、`sendrecv` |
| `min_bytes` / `max_bytes` | `1024` / `256m` | 传给 `systest/xccl_perf` 的 `-b/-e` 消息范围；默认覆盖交付测试用例的完整消息曲线 |
| `step_factor` | `2` | 消息大小步进因子 `-f` |
| `warmup_iterations` | `5` | 预热次数 `-w`，可为 0 |
| `iterations` | `20` | 正式迭代次数 `-n`，必须为正数 |
| `data_type` | `float` | XCCL perf 数据类型 `-d` |
| `timeout` | `120` | `BKCL_TIMEOUT` 秒数 |
| `enable_xdr` | `true` | 是否注入 `BKCL_ENABLE_XDR=1` |
| `supernode` | `false` | 是否额外设置 switch topology/RDMA verbs/tree threshold 变量 |
| `socket_interface` | 自动 | `BKCL_SOCKET_IFNAME`；为空时按管理 IP 查找承载接口 |
| `min_bus_bandwidth_gbs` | `0` | 倒数第二个消息档位的最低 in-place `busbw`，单位 `GB/s`；0 只记录，只有一个档位时使用该档位 |
| `environment` | `{}` | 追加没有专用字段的环境变量；不能覆盖 envinit 管理的 PATH、XPU、BKCL 拓扑变量 |

Ubuntu 和 Kylin 必须使用各自 profile 中的 MPICH 包，不能混用。XCCL 原包可以共用。工具使用统一入口 `systest/xccl_perf`，将 bundle 的 `all_reduce`、`all_gather`、`reduce_scatter`、`all_to_all` 分别转换为程序要求的 `allReduce`、`allGather`、`reduceScatter`、`alltoall`，并固定传入 `-x 1`，即每个 MPI rank 使用一张 XPU。`min_bus_bandwidth_gbs` 的单位是 `GB/s`；设置为 `0` 时只记录结果，设置为正数时按照交付用例取倒数第二个消息档位的 in-place `busbw(GB/s)` 判定 PASS/FAIL；只有一个消息档位时使用该唯一档位。

以单机 8 卡、默认参数为例，最终核心命令等价于：

```bash
mpirun -np 8 systest/xccl_perf \
  -O allReduce -x 1 -b 1024 -e 256m -f 2 \
  -w 5 -n 20 -c 0 -d float
```

envinit 实际使用随包交付的 `mpiexec.hydra`，单机增加本地 `fork` launcher，多机增加临时 hostfile 和 SSH launcher；测试程序及参数语义与上述交付用例一致。mpiexec 会显式使用 `-wdir <本轮临时目录>`，该目录已在所有目标机创建，避免 Hydra 把协调机当前目录传播到其他机器后因路径不存在而在创建远端 rank 前失败。XCCL 自带文档定义 `-c 0` 为性能模式、`-c 1` 为精度模式，因此 bandwidth 判定固定使用 `-c 0`，避免把精度校验开销计入通信耗时。精度检查后续应作为独立测试执行，不能用其带宽结果评价性能。

当前交付物料为 MPICH `5.0.1` 和 XCCL `3.2.2.0`。MPICH 使用 `ch3:sock + Hydra`、共享库模式构建，安装前缀固定为 `/var/lib/envinit/check-runtime/mpich-5.0.1`，并随包提供 XCCL 原版 perf 所需的 `libmpi.so.0` 兼容链接。两个 profile 的构建入口分别是：

```bash
./build/mpich-5.0.1/build-ubuntu.sh
./build/mpich-5.0.1/build-kylin.sh
```

构建脚本会校验 MPICH 源码包 SHA256，在对应用户态中编译，并执行本机 2-rank smoke test。产物、版本输出、构建 manifest 和 SHA256 文件写入 `dist/mpich-runtimes/<profile>/`；交付使用的压缩包复制到对应 `data/profiles/<profile>/misc/`。Kylin 构建依赖已经准备好的 `dist/kylin-v10-sp3-2403-x86_64-rpm-repo` 离线 RPM 仓库。

#### 8.6.3 拓扑、进程数量和一致性校验

工具会在每台机器执行只读的 `xpu-smi topo -m`，并从 inventory 的 `rdmaN_name` 解析该机真实 `mlx5_N`。拓扑解析同时支持 `NIC0... + NIC Legend` 和现场常见的直接 `mlx5_0...` 列名格式。每个 XPU 只选择 inventory 参测网卡中距离最近的一张，优先级为 `PIX -> PXB -> PHB -> NODE -> SYS`。由此自动生成：

- 每台机器的 XPU 数量，也就是该机 MPI slot 数；两台 8 卡机器最终使用 16 个 rank。
- `BKCL_FORCE_RDMA_NICS_ORDER`：严格按 XPU0、XPU1……排列，四张网卡对应八张 XPU 时网卡名会按拓扑重复。
- `BKCL_RDMA_NICS`：同样按 XPU0、XPU1……生成完整映射，作为兼容旧版 XCCL 的回退值；新版优先使用 `BKCL_FORCE_RDMA_NICS_ORDER`。

每台机器都有独立的 rank wrapper，因此各机 `mlx5_N` 编号可以不同；但 XCCL 要求每台机器的 XPU 数量、RDMA 接口名及其 XPU 顺序、`BKCL_SOCKET_IFNAME` 一致，工具会在起流前逐项校验，不一致时直接报错。正常情况下 `apply` 会把项目内接口名固化为相同命名。某个 XPU 没有 PIX 路径、只能选择更远网卡时会打印 `WARN xccl topology degraded`，避免把受 PCIe/NUMA 限制的结果误判为正常 RoCE 性能。

#### 8.6.4 mpirun 前设置的环境变量

环境变量不是只在发起 `mpirun` 的机器设置。工具会为每台机器生成同一路径、但内容按本机拓扑定制的 `run-rank.sh`，Hydra 启动每个远端 rank 后先执行该脚本，再 `exec` 原版 XCCL perf：

| 变量 | 设置方式 |
| --- | --- |
| `XPU_HOME` | 默认 `/usr/local/xpu`，可由 `xpu_home` 修改 |
| `PATH` | 自动加入 MPICH `bin` 和 `${XPU_HOME}/bin` |
| `LD_LIBRARY_PATH` | 自动加入 XCCL `so`、MPICH `lib`、`${XPU_HOME}/so`、`lib`、`lib64` 以及 Ubuntu/Kylin 系统库目录 |
| `BKCL_TIMEOUT` | 来自 `timeout`，默认 120 秒 |
| `BKCL_SOCKET_IFNAME` | `socket_interface` 非空时使用指定值；为空时根据每台机器的管理 IP 自动发现承载接口 |
| `BKCL_RDMA_NICS` | 根据 inventory 和真实拓扑按 XPU 顺序自动生成，作为兼容回退 |
| `BKCL_FORCE_RDMA_NICS_ORDER` | 根据每个 XPU 的最近网卡自动生成，一项对应一个 XPU |
| `BKCL_ENABLE_XDR` | `enable_xdr=true` 时设置为 `1`，让数据直接走 XPU/RDMA 路径 |
| `BKCL_SWITCH_TOPO`、`BKCL_RDMA_VERBS`、`BKCL_TREE_THRESHOLD` | 仅 `supernode=true` 时分别设置为 `1`、`1`、`0` |
| `environment` | 追加项目需要的变量，例如 `BKCL_DEBUG=1`、`BKCL_FORCE_L3_RDMA=1` |

表中已有专用字段或由工具自动生成的变量均受保护，不能在 `environment` 中重复覆盖；`environment` 只用于追加没有专用字段的项目变量，例如 `BKCL_DEBUG` 和 `BKCL_FORCE_L3_RDMA`。wrapper 会先清除可能从远端登录环境残留的可选 BKCL 变量，再按 bundle 重建，并清除 `XPU_VISIBLE_DEVICES`/`CUDA_VISIBLE_DEVICES`；XCCL perf 采用一个 MPI rank 对应一张 XPU，由 MPI local rank 完成设备选择。

#### 8.6.5 临时 SSH、运行时分发与清理

执行机仍需先能按 `check.ssh.user` 和 `check.ssh.options` 登录所有目标机器，这是 envinit 分发物料的控制通道。之后工具自动完成：

1. 在执行机生成仅供本轮使用的 Ed25519 密钥。
2. 只向每台目标机的 `authorized_keys` 追加一行带唯一 `envinit-xccl-<run-id>` 标记的公钥。
3. 将私钥只发到第一台参测机，由它作为 mpirun coordinator，通过专用 SSH wrapper 启动所有 rank；wrapper 强制使用临时密钥、不写 known_hosts。
4. 将 MPICH/XCCL 解包到 `/tmp/envinit-xccl-check/<run-id>`。因为 MPICH 按固定前缀编译，会临时建立 `/var/lib/envinit/check-runtime/mpich-5.0.1` 软链接；如果该位置已经有可用的真实安装则只复用、不删除。
5. 测试成功或中途失败都会进入清理：只移除带本轮标记的公钥行、本轮拥有的 MPICH 软链接和本轮 `/tmp` 目录。已有 SSH key、`sshd_config`、防火墙和系统 MPICH 不会被修改。

运行前会用 `ldd` 检查 `libbkcl.so`、`libxpurt.so.2`、`libmpi.so.0` 等动态依赖，并先验证 coordinator 到所有目标机的临时 SSH。任一步失败都不会继续启动 XCCL 流量。

`--check-stage xccl --dry-run` 仍会执行 sysfs、管理接口和 `xpu-smi topo -m` 的只读发现，以便打印真实 rank 数、网卡顺序、逐机环境脚本和最终 mpirun 命令；不会生成密钥、分发文件、修改 `authorized_keys` 或启动流量。

#### 8.6.6 结果和失败条件

正常结果示意：

```text
XCCL result summary:
STATUS  TEST        HOSTS          RANKS  TOPOLOGY  MODE      SIZE(B)    TIME(us)  ALGBW(GB/s)  BUSBW(GB/s)
PASS    all_reduce  node-a,node-b  16     PIX       in-place  134217728  2081.00   64.47         112.82
XCCL size result details (* = SOP evaluation row):
EVAL  SIZE(B)    COUNT     TYPE   OP   MODE          TIME(us)  ALGBW(GB/s)  BUSBW(GB/s)
----  ---------  --------  -----  ---  ------------  --------  -----------  -----------
      1024       256       float  sum  out-of-place  47.00     0.02         0.04
      1024       256       float  sum  in-place      46.00     0.02         0.04
      134217728  33554432  float  sum  out-of-place  2073.00   64.73        113.29
*     134217728  33554432  float  sum  in-place      2081.00   64.47        112.82
      268435456  67108864  float  sum  out-of-place  4186.00   64.11        112.20
      268435456  67108864  float  sum  in-place      4205.00   63.83        111.70
```

`systest/xccl_perf` 同时输出 out-of-place 和 in-place 两组数据；交互终端的 Results 页不再显示额外的 `SUMMARY` 行，只保留 `STATUS/EVAL/MODE/SIZE/TYPE/OP/TIME/ALGBW/BUSBW` 性能结果，并按 `OUT-OF-PLACE` 全部档位、`IN-PLACE` 全部档位分组展示，不把同一 size 的两种模式交错排列。收到性能行后在原位置填入 time、algbw 和 busbw；按 `Space` 可在详情中查看 test、hosts、ranks、topology 和带宽门槛，按 `p` 可在表格与折线图模式间切换，折线图模式分别绘制 AlgBW、BusBW，按 `m` 切换 out-of-place/in-place，按 `Left/Right` 移动数据点并查看对应 message size 和精确带宽值。最终判定完成时，被选中的 size 行更新为 `PASS/FAIL/WARN`，并用 `EVAL=*` 标出门槛采用行。准备、分发或 mpirun 在产生性能数据前失败时，Results 页会追加一条独立 `FAIL` 错误行，完整错误保留在详情和 Raw Logs。envinit 同时保留完整 stdout，并把结果整理到 `XCCL size result details`，方便比较完整性能曲线；非交互输出仍在命令结束后打印原始输出和汇总。交付判定按照 SOP 读取倒数第二个消息档位的 in-place 数据，并在汇总中明确显示 `MODE=in-place`。默认 `1024 -> 256m`、步进 2 时判定档位是 `128m`；只有一个消息档位时使用该唯一档位。如果任一 XPU 只能使用非 PIX 网卡，最终采用行会显示 `STATUS=WARN`，详情及非交互汇总会显示 `TOPOLOGY=DEGRADED`，并额外打印 PCIe/NUMA 限速提示；如果同时低于配置的带宽门槛则仍显示 `FAIL`。性能阶段会明确输出 `validation: disabled (-c 0 performance mode)`，原始表格中的 `#wrong`/`Out of bounds` 不作为精度通过依据。

### 8.7 汇总结果、计数器和退出状态

#### 8.7.1 汇总表

检查结束后重点查看以下汇总表：

| 汇总表 | 说明 |
| --- | --- |
| `Bandwidth result summary` | 每条 RDMA 带宽流的结果。示例环境中单流应接近 `390 Gbps` |
| `RDMA ping result summary` | 所有参测 RDMA 网络路径的大包 ping 是否成功 |
| `XCCL result summary` | 全部参测 XPU 的集合通信带宽；单位为 `GB/s` |
| `NIC counter delta summary` | 网卡计数器在检查前后的变化 |
| `RDMA device counter delta summary` | IB 设备 sysfs 计数器在带宽检查前后的变化 |

#### 8.7.2 计数器原理

NIC 计数器来自各 `rdmaN_name` 的 `ethtool -S`，会在整个 check 前后各采集一次，因此即使只跑 rdma-ping 也会检查 NIC delta。bandwidth 或 XCCL 还会按动态解析出的 `mlx5_N` 读取 `/sys/class/infiniband/<device>/ports/*/{counters,hw_counters}`，在 RDMA 流量前后计算 device/port delta。

mlx5 的 `rx_err_lane_N_phy` 名称虽然包含 `err`，Linux 驱动实际将它映射为每条物理 lane 的 FEC corrected bits。PAM4 链路上该值增长并不等价于未纠正包错误，因此汇总中保留为 `INFO`，不单独导致 check 失败。CRC、symbol error、uncorrectable FEC、drop、discard、timeout、retrans 等异常增量仍然显示 `FAIL` 并影响最终退出状态。

状态含义：

| 状态 | 含义 |
| --- | --- |
| `SAME` | before/after 没有变化 |
| `INFO` | 普通流量或非风险计数增长，仅记录 |
| `WARN` | 计数器回退/重置，无法把负 delta 当成新增错误 |
| `FAIL` | 丢包、discard、CRC、timeout、sequence、retrans、CNP/等待等风险计数增长 |

计数器采集命令本身失败也会让 check 失败，而不是只打印警告后忽略。即使带宽达到门槛，也必须同时确认 NIC 和 RDMA device 汇总中没有异常增长。

#### 8.7.3 最终退出状态

以下任一条件都会使 `env_init check` 返回非零：

- 目标解析、SSH、sysfs/topo 发现或测试命令失败；
- rdma-ping 任一路径不是 `0% packet loss`；
- bandwidth 任一完成流低于逐流 auto/固定门槛、auto 模式无法探测两端最大速率，或无法解析需要判定的吞吐；
- XCCL 没有解析到性能行，或 SOP 判定档位的 in-place busbw 低于 `check.xccl.min_bus_bandwidth_gbs`；
- NIC/RDMA device 风险计数器出现正 delta；
- XCCL 运行时分发、依赖检查、临时 SSH 或清理失败。

单纯的非 PIX 拓扑退化显示 `WARN`，不会单独令 check 失败；但它明确表示结果可能受 PCIe/NUMA 路径限制。进入测试执行阶段后的失败会在各自汇总表后合并到最终 `check failed: ...` 错误中；目标解析、配置校验或 topo 生成这类前置错误会立即返回，避免在错误拓扑上继续起流。

## 9. 常见问题

### 9.1 `inventory row missing rdmaN_ip`

原因：`rdma_mode=full`，但规划表没有填写对应 RDMA IP。

处理：补齐 `rdmaN_ip`，或者在不需要 RDMA 三层网络时将 `rdma_mode` 设置为 `names_only`。

### 9.2 `expected interfaces are not present yet`

原因：目标接口名尚未出现，通常是当前系统网卡名还没有按规划名绑定，或 OFED 后 RDMA 网卡尚未暴露。

处理：先执行 `software ofed network`。`network` 阶段会先自动发现物理网卡并打开 NIC Binding Review TUI；确认后临时重命名本轮后续 stage 必须使用的 RDMA 网卡，并在允许立即应用时处理管理网卡，再写入网络配置和持久化命名规则，重启后保持规划名。

### 9.3 MAC 找不到

原因：规划表中的 MAC 与本机实际网卡不一致。

处理：重新采集接口 MAC，检查是否填错机器或填错物理端口。

### 9.4 `rdma-ping` 不能运行

原因：规划表没有 RDMA IP，或者 RDMA IP 网络未配置。

处理：补齐 `rdmaN_ip` 并配置 RDMA 网络；只做带宽检查时可使用 `--check-stage bandwidth`。

### 9.5 `kex_exchange_identification: read: connection reset by peer`

原因：这是 SSH 握手阶段被对端断开，不是 RDMA ping 包超时。常见原因是同一时间连接数过多，触发了目标机 `sshd` 的 `MaxStartups` 或现场安全策略。

处理：工具会限制 `rdma-ping` 远端 SSH 并发并自动重试这类暂态错误。如果仍频繁出现，检查目标机 `/etc/ssh/sshd_config` 中的 `MaxStartups`、`MaxSessions`，以及安全组、防火墙或堡垒机连接限制。

### 9.6 如何降低首次执行风险

建议每台机器按以下顺序操作：

```bash
./env_init plan ... --host node1
sudo ./env_init apply ... --host node1 --stages software ofed
sudo ./env_init apply ... --host node1 --stages network
sudo ./env_init apply ... --host node1 --stages xre xdr firmware container mlxconfig sysctl kernel post
```

每一步完成后检查输出，再进入下一步。

### 9.7 downloader 中断后是否需要重新开始

不需要。保持相同的 `--profile` 和 `--output-dir`，重新执行原命令即可。已完成且远端信息未变化的文件会根据 SHA256 或完成标记跳过，未完成的 `.part` 文件会尝试断点续传。

如果上一次选择的是另一个 profile，不要直接在原目录上继续。downloader 不会主动删除旧 profile 独有的文件，应改用新的输出目录，或先人工备份并清空旧目录，避免 Ubuntu/Kylin 物料混合。

### 9.8 apply 重跑后全部 stage 都被跳过

这是默认全流程 checkpoint 在生效，表示相同 host 和相同解析配置此前已经完成。需要从头重跑完整流程时使用 `--restart`：

```bash
sudo ./env_init apply --inventory planning/inventory.csv --bundle planning/bundle.json --host node1 --restart
```

只想强制重做某几个 stage 时显式使用 `--stages network sysctl`；部分 stage 模式不会读取或改写全流程 checkpoint。不要为了重跑单个 stage 手工编辑 `/var/lib/envinit/apply-progress.json`。

### 9.9 XCCL 多机启动在 Hydra 阶段失败

先保留以下完整输出，不要只截取其中一条 SSH 公钥或 warning：

- `INFO xccl topology`：确认每台机器的 XPU 数、RDMA 网卡顺序和拓扑；
- `INFO xccl mpirun`：确认 rank 数、launcher 和 `-wdir`；
- Hydra stderr 及最后一行 `error:`。

当前工具会先在所有目标机创建同一个本轮工作目录，并向 `mpiexec.hydra` 显式传入 `-wdir <check.xccl.work_root>/<run-id>`。如果仍出现 `launch_procs`、`pmip_cb.c` 或远端进程未创建，检查 `work_root` 是否位于允许的 `/tmp`/`/var/tmp`、所有目标是否可写、目标机是否使用了同一套 MPICH/XCCL 物料，以及 coordinator 能否使用日志中显示的临时 SSH wrapper 登录所有目标。失败后工具仍会尝试清理本轮目录和授权行；清理失败会合并到最终错误中。

### 9.10 downloader 列出了物料但 COS 下载返回 404

先看报错对象在 AList 目录列表中的大小。若它是零字节、无 SHA256，而且本地正式 profile 中不存在，通常是历史软链接、目录占位或已删除对象留下的 AList/COS 残留。新版 downloader 会打印 WARNING 并跳过这种不可下载的空条目；仍应在存储侧删除残留并刷新 AList 缓存。

如果对象大小非零或带有 SHA256，downloader 会保持失败，因为这表示正式物料缺失，不能通过跳过来掩盖。此时应恢复 COS 对象或修正 AList profile 内容，再使用原输出目录重跑；已经校验成功的文件不会重复下载。

## 10. 编译可执行文件

项目使用 Go 编写，TUI 使用 Bubble Tea/Lip Gloss，依赖版本由 `go.mod`、`go.sum` 固定。可以在安装了 `go.mod` 所要求 Go 版本并已准备模块缓存的开发机上交叉编译 Linux 可执行文件；离线构建时需要提前准备 Go module cache。

### 10.1 编译 x86_64 版本

适用于常见的 x86_64 / AMD64 Linux 服务器。生成文件名为 `env_init`：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -o env_init ./cmd/envinit
```

### 10.2 编译 ARM64 版本

适用于 ARM64 / aarch64 Linux 服务器。生成文件名为 `env_init_arch`：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -o env_init_arch ./cmd/envinit
```

### 10.3 检查生成结果

```bash
file env_init env_init_arch
```

预期结果：

```text
env_init:      ELF 64-bit LSB executable, x86-64, statically linked
env_init_arch: ELF 64-bit LSB executable, ARM aarch64, statically linked
```

交付到 U 盘时，可以将对应架构的文件复制到 `/mnt/usb/env_tool/`，并确保文件可执行：

```bash
chmod +x env_init env_init_arch
```
