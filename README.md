# envinit

`envinit` is a Go CLI that consolidates the shell and Python initialization scripts in this repository into one workflow for offline machine bring-up.

`envinit` 是一个 Go 命令行工具，用来把仓库里分散的 shell 和 Python 初始化脚本收敛成一条统一的离线装机流程。

现场安装和规划文件编写请优先阅读：[中文使用手册](docs/USAGE.zh-CN.md)。

## Overview / 概览

Typical workflow:

1. Install the OS manually from a USB stick.
2. Prepare an inventory file with management IPs, RDMA IPs, and preferably interface MAC addresses.
3. Prepare an offline bundle file that points to the local apt mirror materials, XRE/XDR/FW packages, and container packages.
4. Run `envinit apply` on the target machine to configure networking, routes, udev, offline apt, drivers, firmware, containers, sysctl, IOMMU settings, and the final post action.

典型工作流：

1. 使用装机 U 盘手动安装操作系统。
2. 准备一份 inventory，记录管理口 IP、RDMA IP，最好同时记录每个口的 MAC。
3. 准备一份 bundle，指向离线 apt 源物料、XRE/XDR/FW 包和容器包。
4. 在目标机器上执行 `envinit apply`，完成网络、路由、udev、离线源、驱动、固件、容器、sysctl、IOMMU 以及最后的收尾动作。

Design goals:

- Go standard library only
- Inventory supports `.csv`, `.tsv`, `.txt`, and `.xlsx`
- No Python runtime dependency

设计目标：

- 仅使用 Go 标准库
- inventory 支持 `.csv`、`.tsv`、`.txt`、`.xlsx`
- 不依赖 Python 运行时

## What It Handles / 已整合能力

- Management bond configuration
- Static IP configuration for four RDMA interfaces
- RDMA policy route script generation
- RoCE adaptive routing enablement for the planned RDMA interfaces
- Persistent udev naming rules
- Copy offline apt materials into `/opt`, configure apt sources, and run `apt-get install`
- Mellanox OFED extraction and installation
- XRE installation
- XDR extraction, build, and installation
- Accelerator firmware upgrade
- Offline `xpu-container` package installation
- `mlxconfig` parameter programming
- Sysctl configuration appended into `/etc/sysctl.conf`
- Idempotent IOMMU / grub updates
- Post-boot systemd service for RDMA ring buffer tuning and RoCE adaptive routing
- Ordered post-stage installation for extra local `.deb` packages
- Optional final power-off action, confirmed by default

- 管理口 bond 配置
- 四张 RDMA 网卡静态 IP 配置
- RDMA policy route 脚本生成
- 基于规划表中的 RDMA 网卡名称启用 RoCE adaptive routing
- 持久化 udev 命名规则
- 将离线 apt 物料复制到 `/opt`，配置 apt 源并执行 `apt-get install`
- Mellanox OFED 解压与安装
- XRE 安装
- XDR 解压、编译、安装
- 算力卡固件升级
- `xpu-container` 离线包安装
- `mlxconfig` 参数下发
- 追加写入 `/etc/sysctl.conf` 的 sysctl 配置
- 幂等的 IOMMU / grub 更新
- 写入开机自启动服务，用于 RDMA ring buffer 调优和 RoCE adaptive routing
- 按 `bundle.json` 顺序在 post 阶段安装额外本地 `.deb` 包
- 可选的最终关机动作，默认先确认

## Key Files / 关键文件

- [cmd/envinit/main.go](/Users/billwang/Works/单品/昆仑芯/宁德时代/env_init/cmd/envinit/main.go)
- [internal/inventory/load.go](/Users/billwang/Works/单品/昆仑芯/宁德时代/env_init/internal/inventory/load.go)
- [internal/runner/app.go](/Users/billwang/Works/单品/昆仑芯/宁德时代/env_init/internal/runner/app.go)
- [examples/inventory.sample.csv](/Users/billwang/Works/单品/昆仑芯/宁德时代/env_init/examples/inventory.sample.csv)
- [examples/bundle.sample.json](/Users/billwang/Works/单品/昆仑芯/宁德时代/env_init/examples/bundle.sample.json)

## Inventory Format / Inventory 格式

Minimum required columns:

最少需要这些列：

| `host_id` | `hostname` | `mgmt_ip` | `mgmt_prefix` | `mgmt_gateway` | `rdma1_ip` | `rdma2_ip` | `rdma3_ip` | `rdma4_ip` |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Machine ID | Target hostname | Management IP | Management prefix | Management gateway | RDMA port 1 IP | RDMA port 2 IP | RDMA port 3 IP | RDMA port 4 IP |

Optional columns:

可选列：

- `mgmt_iface1`, `mgmt_iface2`
- `mgmt_mac1`, `mgmt_mac2`
- `mgmt_bond_name`
- `mgmt_nameservers`
- `rdma1_name` to `rdma4_name`
- `rdma1_mac` to `rdma4_mac`
- `rdma1_prefix` to `rdma4_prefix`
- `rdma1_gateway` to `rdma4_gateway`
- `rdma1_table` to `rdma4_table`

If optional fields are omitted, the tool falls back to defaults from `bundle.json`.

如果不填写可选字段，工具会退回到 `bundle.json` 中的默认值。

Recommended header:

推荐表头：

| 类型 | 推荐 CSV 列 |
| --- | --- |
| 机器信息 | `host_id`, `hostname` |
| 管理网 | `mgmt_ip`, `mgmt_prefix`, `mgmt_gateway` |
| 管理口 1 | `mgmt_iface1`, `mgmt_mac1` |
| 管理口 2 | `mgmt_iface2`, `mgmt_mac2` |
| RDMA 口 1 | `rdma1_name`, `rdma1_ip`, `rdma1_mac` |
| RDMA 口 2 | `rdma2_name`, `rdma2_ip`, `rdma2_mac` |
| RDMA 口 3 | `rdma3_name`, `rdma3_ip`, `rdma3_mac` |
| RDMA 口 4 | `rdma4_name`, `rdma4_ip`, `rdma4_mac` |

Resolution priority:

字段优先级：

- If `*_mac` is present, resolve the real interface by MAC first
- If `*_mac` is absent but `*_name` or `mgmt_iface*` is present, use the interface name directly
- If both are absent, fall back to the default interface names from `bundle.json`

- 有 `*_mac` 时，优先按 MAC 识别真实网卡
- 没有 `*_mac` 但有 `*_name` 或 `mgmt_iface*` 时，直接按接口名配置
- 两者都没有时，退回到 `bundle.json` 里的默认接口名

Using MAC addresses is strongly recommended because interface names can change across BIOS, kernel, and distro versions.

强烈建议记录 MAC，因为接口名可能会随着 BIOS、内核或发行版变化而变化。

## Bundle Format / Bundle 格式

`bundle.json` describes shared defaults and offline artifacts used during installation.

`bundle.json` 用来描述整批机器共用的默认参数，以及装机过程中要用到的离线资源。

Important fields:

关键字段：

- `offline_apt.material_path`: source file or directory to copy from the installation materials
- `offline_apt.copy_to`: destination path under the target machine, usually under `/opt`
- `offline_apt.entries`: offline apt source entries; supports `{{offline_apt_target}}`
- `packages`: packages installed with apt, supports `{{uname_r}}`
- `defaults.bond_primary`: primary management interface for `active-backup` bonds; must be one of `defaults.mgmt_interfaces` when set
- `defaults.rdma_exsist`: set to `false` to skip all 400G/RDMA-related actions
- `defaults.rdma_configure_ip_route`: set to `false` to skip only RDMA IP, route, and policy-rule configuration while keeping other RDMA actions enabled
- `defaults.rdma_interfaces[].table`: required only when `rdma_configure_ip_route` is enabled; when route configuration is disabled, `name` alone is enough
- `artifacts.xre_installer`: path to the XRE `.run` installer
- `artifacts.xre_args`: extra arguments appended to the XRE installer command, useful for non-interactive installation
- `xre.card_model`: required when `artifacts.xre_installer` is configured; supported values are `P800` and `P900`
- `artifacts.ofed_archive`: path to the Mellanox OFED archive
- `artifacts.xdr_archive`: path to the XDR archive
- `artifacts.firmware_archive`: path to the firmware archive
- `artifacts.container_packages`: offline `.deb` files installed with `dpkg -i`
- `mlxconfig.settings`: `mlxconfig` key/value settings to apply
- `check.iterations`: `ib_write_bw -n` iteration count for bandwidth checks; defaults to `100`
- `check.rdma_groups`: RDMA/XPU bandwidth check groups, each with an `ib_device`; `xpu_offsets` are used only when `--bandwidth-mmap xdr` is enabled
- `check.parallel`: when `true`, run the same bandwidth stream matrix in batches; each batch uses each client and server RDMA group at most once, so a single 400G port is not oversubscribed by multiple simultaneous streams
- `check.rdma_ping_payload_size`: jumbo RDMA ping payload size; defaults to `8972`, which validates a 9000-byte IPv4 MTU with `ping -M do`
- `check.min_gbits`: optional minimum acceptable bandwidth; `0` records results without failing on throughput
- `post_packages`: extra local `.deb` packages installed during the `post` stage, in the same order as the array
- `post_tasks`: ordered post-stage tasks after the built-in RDMA post-boot service; supports `copy`, `cmd`, `mv`, `rm`, and `mkdir`
- `post_power_action`: final `ipmitool power` action after all stages, such as `{ "action": "soft", "confirm": true }`, `{ "action": "off" }`, `{ "action": "cycle" }`, `{ "action": "reset" }`, `{ "action": "on" }`, `{ "action": "status" }`, or `{ "action": "none" }`

- `offline_apt.material_path`：从装机物料中复制的源文件或目录
- `offline_apt.copy_to`：目标机器上的落点路径，通常放在 `/opt` 下
- `offline_apt.entries`：离线 apt 源条目，支持 `{{offline_apt_target}}`
- `packages`：通过 apt 安装的包，支持 `{{uname_r}}`
- `defaults.bond_primary`：`active-backup` bond 的主管理口；配置时必须是 `defaults.mgmt_interfaces` 里的一个
- `defaults.rdma_exsist`：设置为 `false` 时跳过所有 400G/RDMA 相关动作
- `defaults.rdma_configure_ip_route`：设置为 `false` 时只跳过 RDMA IP、路由和 policy rule 配置，其它 RDMA 动作仍然执行
- `defaults.rdma_interfaces[].table`：只有启用 `rdma_configure_ip_route` 时才需要；禁用路由配置时只写 `name` 即可
- `artifacts.xre_installer`：XRE `.run` 安装包路径
- `artifacts.xre_args`：追加给 XRE 安装命令的参数，适合用来跳过交互
- `xre.card_model`：配置 `artifacts.xre_installer` 时必填；目前支持 `P800` 和 `P900`
- `artifacts.ofed_archive`：Mellanox OFED 压缩包路径
- `artifacts.xdr_archive`：XDR 压缩包路径
- `artifacts.firmware_archive`：固件升级包路径
- `artifacts.container_packages`：通过 `dpkg -i` 安装的离线 `.deb` 包
- `mlxconfig.settings`：需要下发的 `mlxconfig` 键值对
- `check.iterations`：带宽检查使用的 `ib_write_bw -n` 迭代次数；默认 `100`
- `check.rdma_groups`：RDMA/XPU 带宽检查组，每组包含一个 `ib_device`；`xpu_offsets` 只在启用 `--bandwidth-mmap xdr` 时使用
- `check.parallel`：为 `true` 时分批并发执行同一组带宽流矩阵；每个批次里 client 和 server 端的每个 RDMA group 最多只参与一条流，避免一张 400G 口被多条并发流抢带宽
- `check.rdma_ping_payload_size`：RDMA 大包 ping 的 payload 大小；默认 `8972`，配合 `ping -M do` 验证 IPv4 MTU 9000 是否端到端可用
- `check.min_gbits`：可选的最低带宽阈值；为 `0` 时只记录结果，不按吞吐失败
- `post_packages`：在 `post` 阶段额外安装的本地 `.deb` 包，安装顺序严格按照数组从上到下
- `post_tasks`：内置 RDMA 开机服务之后执行的有序 post 任务，支持 `copy`、`cmd`、`mv`、`rm` 和 `mkdir`
- `post_power_action`：所有阶段完成后的 `ipmitool power` 收尾动作，例如 `{ "action": "soft", "confirm": true }`、`{ "action": "off" }`、`{ "action": "cycle" }`、`{ "action": "reset" }`、`{ "action": "on" }`、`{ "action": "status" }` 或 `{ "action": "none" }`

Example `post_tasks`:

`post_tasks` 示例：

```json
"post_tasks": [
  {
    "name": "install service file",
    "type": "copy",
    "source": "/mnt/usb/env_tool/services/my-agent.service",
    "target": "/etc/systemd/system/my-agent.service",
    "mode": "0644"
  },
  {
    "name": "create config directory",
    "type": "mkdir",
    "path": "/etc/my-agent",
    "mode": "0755"
  },
  {
    "name": "move generated config",
    "type": "mv",
    "source": "/tmp/my-agent.conf",
    "target": "/etc/my-agent/my-agent.conf"
  },
  {
    "name": "remove stale state",
    "type": "rm",
    "path": "/var/lib/my-agent/stale"
  },
  {
    "name": "reload systemd",
    "type": "cmd",
    "command": "systemctl daemon-reload && systemctl enable --now my-agent.service"
  }
]
```

## Usage / 用法

Preview the plan:

先预览：

The `plan` output now includes a stage-by-stage action list, so you can review exactly which files will be written, which commands will run, and which parameters will be used before `apply`.

`plan` 现在会额外输出按 stage 展开的动作清单，你可以在真正执行 `apply` 之前，直接看到会写哪些文件、会跑哪些命令、关键参数是什么。

When `--host` is specified, the matched inventory row is also treated as the hostname source of truth. During `apply`, if the current system hostname differs from the matched row's `hostname` or `host_id`, the tool sets the system hostname before running stages.

当显式传入 `--host` 时，匹配到的规划表行也会作为 hostname 的准确信息。执行 `apply` 时，如果当前系统 hostname 和该行里的 `hostname` 或 `host_id` 不一致，工具会先修改系统 hostname，再继续执行各阶段。

```bash
go run ./cmd/envinit plan \
  --inventory ./examples/inventory.sample.csv \
  --bundle ./examples/bundle.sample.json \
  --host xpu11
```

Apply the changes:

确认后执行：

```bash
sudo go run ./cmd/envinit apply \
  --inventory ./examples/inventory.sample.csv \
  --bundle ./examples/bundle.sample.json \
  --host xpu11
```

For `.xlsx` inventories, the first worksheet is used by default. You can also specify a sheet explicitly.

如果 inventory 是 `.xlsx`，默认使用第一张表，也可以显式指定 sheet。

```bash
sudo go run ./cmd/envinit apply \
  --inventory ./machines.xlsx \
  --sheet Sheet1 \
  --bundle ./bundle.json \
  --host xpu11
```

Run the default full check across hosts:

跨机器执行默认完整检查：

```bash
sudo go run ./cmd/envinit check \
  --inventory ./machines.xlsx \
  --bundle ./bundle.json \
  --hosts xpu11,xpu12
```

By default, `check` runs both check stages:

- `bandwidth`: RDMA/XPU bandwidth pressure with `ib_write_bw`
- `rdma-ping`: jumbo ping over the configured RDMA IPs

`check` 默认执行两个检查阶段：

- `bandwidth`：使用 `ib_write_bw` 做 RDMA/XPU 带宽压测
- `rdma-ping`：使用 RDMA IP 做 400G 大包 ping 检查

Select one stage explicitly when needed:

需要时可以只执行某一个检查阶段：

```bash
sudo go run ./cmd/envinit check \
  --inventory ./machines.xlsx \
  --bundle ./bundle.json \
  --hosts xpu11,xpu12 \
  --check-stage bandwidth
```

```bash
sudo go run ./cmd/envinit check \
  --inventory ./machines.xlsx \
  --bundle ./bundle.json \
  --hosts xpu11,xpu12,xpu13 \
  --check-stage rdma-ping
```

Use `--check-stage all` to request the default full check explicitly. The old `--checks` flag remains as a compatibility alias, but new scripts should use `--check-stage`.

可以使用 `--check-stage all` 显式请求默认完整检查。旧的 `--checks` 参数仍保留为兼容别名，但新脚本建议使用 `--check-stage`。

Bandwidth check details:

带宽检查细节：

The `check` command resolves each host through inventory and uses `mgmt_ip` for SSH orchestration. For bandwidth checks, the `ib_write_bw` client uses the matching `rdmaN_ip` for the tested server-side RDMA group when it is present, falling back to `mgmt_ip` only when that RDMA IP is empty. Bandwidth commands use `ib_write_bw -n <check.iterations> -F -R --report_gbits`. Results are printed as a final `Bandwidth result summary` table; rows below `check.min_gbits` are printed in red and fail the check. By default, bandwidth checks do not pass `-s`, `--mmap`, or `--mmap-offset`; they run a cross matrix from every configured client RDMA group to every configured server RDMA group. With four groups on each side, each directed host pair runs 16 streams. `check.parallel` only controls scheduling: the same matrix is split into batches where each client and server RDMA group is used at most once per batch. Four groups therefore run four streams at a time over four batches, giving each stream a chance to reach the full single-port bandwidth.

`check` 命令会通过 inventory 解析主机，并使用 `mgmt_ip` 做 SSH 编排。带宽检查中，`ib_write_bw` client 会优先使用被测 server 端 RDMA group 对应的 `rdmaN_ip` 作为末尾对端地址；只有这个 RDMA IP 为空时才回退到 `mgmt_ip`。带宽命令使用 `ib_write_bw -n <check.iterations> -F -R --report_gbits`，结果会在最后以 `Bandwidth result summary` 表格打印；低于 `check.min_gbits` 的行会标红并让 check 失败。默认带宽检查不会传 `-s`、`--mmap` 或 `--mmap-offset`；它会把 client 端每个 RDMA group 交叉打到 server 端每个 RDMA group。两边各 4 个 group 时，每个有向机器对会跑 16 条流。`check.parallel` 只控制调度方式：同一套矩阵会被拆成多个批次，每个批次里 client 和 server 端的每个 RDMA group 最多只参与一条流。4 个 group 时就是每批并发 4 条流、共 4 批，让每条流都有机会跑满单端口带宽。

To emulate large KV cache transfer pressure, add `--emu-kv-transfer`; this sets `ib_write_bw -s 8388608`. To use the XDR mmap buffer path, add `--bandwidth-mmap xdr`; this sets `--mmap=/dev/xdrdrv` and expands each client/server RDMA group pair across the `xpu_offsets` bound to those groups. The local binding is preserved: an XPU offset is only driven through the RDMA group where it is configured, but that group is still cross-tested against every peer RDMA group and peer XPU offset. With four groups and two offsets per group on each side, each directed host pair runs 64 streams. In parallel mode these are still batched with one stream per RDMA group per side; with four groups, each batch has four streams, so the 64-stream matrix runs in 16 batches. You can use either flag alone, or both together.

如果要模拟大块 KV cache 传输压力，加 `--emu-kv-transfer`；它会设置 `ib_write_bw -s 8388608`。如果要走 XDR mmap buffer 路径，加 `--bandwidth-mmap xdr`；它会设置 `--mmap=/dev/xdrdrv`，并把每个 client/server RDMA group 组合展开成这些 group 绑定的 `xpu_offsets` 矩阵。本机绑定关系会保留：某个 XPU offset 只会通过它所在的 RDMA group 发流，但这个 group 仍会交叉测试对端每个 RDMA group 和对端每个 XPU offset。两边各 4 个 group、每个 group 2 个 offset 时，每个有向机器对会跑 64 条流。并发模式下仍然按每端每个 RDMA group 每批一条流来分批；4 个 group 时每批 4 条流，这 64 条流会分 16 批跑完。这两个开关可以单独使用，也可以一起使用。

Before and after each `check` run, the tool captures matching `ethtool -S` counters for each participating host’s RDMA interfaces and prints a final `NIC counter delta summary` table. Counters that are empty or zero on both sides are omitted. Nonzero unchanged counters are shown as `SAME`; ordinary traffic counters are shown as `INFO`; and PD/KV-over-RoCE risk counters such as `port_xmit_discards`, `port_rcv_errors`, `packet_seq_err`, `local_ack_timeout_err`, `out_of_sequence`, `port_xmit_wait`, `np_cnp_sent`, `rp_cnp_handled`, `rx_prio*_buf_discard`, `timeout`, `drop`, `discard`, `crc`, `err`, or `roce_adp_retrans` are printed in red as `FAIL` rows when they increase and fail the check.

每次 `check` 开始前和结束后，工具都会抓取参与节点 RDMA 网卡的匹配 `ethtool -S` 计数并在最后打印 `NIC counter delta summary` 表格。前后都是空或 0 的计数会被忽略；有值但没变化的计数显示为 `SAME`；普通流量计数显示为 `INFO`；而 `port_xmit_discards`、`port_rcv_errors`、`packet_seq_err`、`local_ack_timeout_err`、`out_of_sequence`、`port_xmit_wait`、`np_cnp_sent`、`rp_cnp_handled`、`rx_prio*_buf_discard`、`timeout`、`drop`、`discard`、`crc`、`err` 或 `roce_adp_retrans` 这类 PD/KV over RoCE 风险计数增长时，会以红色 `FAIL` 行输出，并让 check 失败。

Bandwidth checks also capture RDMA device sysfs counters from `/sys/class/infiniband/<device>/ports/*/counters` and `hw_counters` for every configured `check.rdma_groups[].ib_device`. These are reported in a separate `RDMA device counter delta summary` table with the same `SAME`/`INFO`/red `FAIL` behavior.

带宽检查还会为每个配置的 `check.rdma_groups[].ib_device` 采集 `/sys/class/infiniband/<device>/ports/*/counters` 和 `hw_counters` 下的 RDMA 设备计数器，并以单独的 `RDMA device counter delta summary` 表格展示，判断规则同样是 `SAME`、`INFO`、红色 `FAIL`。

```bash
sudo go run ./cmd/envinit check \
  --inventory ./machines.xlsx \
  --bundle ./bundle.json \
  --hosts xpu11,xpu12 \
  --check-stage bandwidth \
  --emu-kv-transfer \
  --bandwidth-mmap xdr
```

Jumbo RDMA ping details:

RDMA 大包 ping 检查细节：

```bash
sudo go run ./cmd/envinit check \
  --inventory ./machines.xlsx \
  --bundle ./bundle.json \
  --hosts xpu11,xpu12,xpu13 \
  --check-stage rdma-ping \
  --rdma-ping-count 10 \
  --rdma-ping-mtu 9000 \
  --rdma-ping-timeout 3
```

`rdma-ping` requires the inventory to contain RDMA IPs such as `rdma1_ip`, `rdma2_ip`, and so on. It pairs RDMA interfaces by inventory index, so `rdma1` pings `rdma1`, `rdma2` pings `rdma2`, and so on, in both directions for every host pair. If the RDMA IP fields are empty, the stage fails early with the missing field names.

`rdma-ping` 要求 inventory 中包含 `rdma1_ip`、`rdma2_ip` 这类 RDMA IP 字段。它会按 inventory 中的 RDMA 序号配对，也就是 `rdma1` 对 `rdma1`、`rdma2` 对 `rdma2`，并对每组机器做双向检查。如果 RDMA IP 为空，该阶段会提前失败并提示缺少哪些字段。

For each directed host pair, all configured RDMA interfaces are pinged concurrently. Results are printed as an `RDMA ping result summary` table. Failed ping rows are printed in red and fail the check.

对每个有向机器对，所有配置的 RDMA 网卡会并发 ping。检查结果会以 `RDMA ping result summary` 表格打印。失败的 ping 行会标红，并让 check 失败。

The default jumbo ping command uses `ping -M do -s 8972`, which validates a 9000-byte IPv4 MTU path without fragmentation. Command-line flags override bundle defaults:

默认大包 ping 命令使用 `ping -M do -s 8972`，用于验证 9000-byte IPv4 MTU 路径在不分片时是否可用。命令行参数会覆盖 bundle 默认值：

- `--rdma-ping-count 10`: sends 10 packets
- `--rdma-ping-mtu 9000`: converts MTU to IPv4 ping payload size `8972`
- `--rdma-ping-timeout 3`: waits 3 seconds for each reply

- `--rdma-ping-count 10`：发送 10 个包
- `--rdma-ping-mtu 9000`：按 IPv4 自动换算成 ping payload size `8972`
- `--rdma-ping-timeout 3`：每个回包等待 3 秒

Preview check commands without running them:

只预览 check 命令而不实际执行：

```bash
sudo go run ./cmd/envinit check \
  --inventory ./machines.xlsx \
  --bundle ./bundle.json \
  --hosts xpu11,xpu12 \
  --check-stage bandwidth \
  --dry-run
```

Run only selected stages:

只执行部分阶段：

```bash
sudo go run ./cmd/envinit apply \
  --inventory ./machines.xlsx \
  --bundle ./bundle.json \
  --host xpu11 \
  --stages network,udev,sysctl,iommu
```

Supported stages:

支持的阶段：

- `apt`
- `ofed`
- `udev`
- `network`
- `xre`
- `xdr`
- `firmware`
- `container`
- `mlxconfig`
- `sysctl`
- `iommu`
- `post`

## Current Defaults / 当前默认假设

- Stage execution order defaults to `apt -> ofed -> udev -> network -> xre -> xdr -> firmware -> container -> mlxconfig -> sysctl -> iommu -> post`, and the default `post` behavior is to ask before powering off
- Management interfaces default to `ens20f0np0` and `ens20f1np1`
- RDMA interfaces default to `ens11np0`, `ens13np0`, `ens15np0`, and `ens17np0`
- RDMA prefix defaults to `/24`
- RDMA gateway defaults to `.1` in each subnet
- RDMA policy route CIDR defaults to `11.1.0.0/21`
- RDMA is enabled by default; RDMA IP and policy-route configuration is enabled by default

- 默认阶段执行顺序为 `apt -> ofed -> udev -> network -> xre -> xdr -> firmware -> container -> mlxconfig -> sysctl -> iommu -> post`，且默认 `post` 行为会先询问是否执行关机
- 管理口默认是 `ens20f0np0` 和 `ens20f1np1`
- RDMA 口默认是 `ens11np0`、`ens13np0`、`ens15np0`、`ens17np0`
- RDMA 前缀默认是 `/24`
- RDMA gateway 默认是各自网段的 `.1`
- RDMA policy route 目标网段默认是 `11.1.0.0/21`
- RDMA 默认启用；RDMA IP 和 policy route 配置也默认启用

If the inventory contains `mgmt_mac*` or `rdma*_mac`, the tool prefers MAC-based resolution. If MAC is missing, it falls back to `mgmt_iface*` or `rdma*_name`, and then to bundle defaults.

如果 inventory 里提供了 `mgmt_mac*` 或 `rdma*_mac`，工具会优先按 MAC 识别接口；如果 MAC 为空，则退回到 `mgmt_iface*` 或 `rdma*_name`；再没有时才退回到 bundle 默认值。

If only `mgmt_iface1` or `mgmt_mac1` is provided and the second management slot is left empty, the tool treats the machine as a single-management-port host and configures the management IP directly on that interface without creating a bond.

如果只填写了 `mgmt_iface1` 或 `mgmt_mac1`，而第二个管理口留空，工具会把机器视为单管理口主机，直接把管理 IP 配到该接口上，不会创建 bond。

If `mgmt_iface2` or `mgmt_mac2` is explicitly provided in the inventory, the tool treats that host as a dual-management-port machine even when the bundle defaults only list one management interface.

如果 inventory 里明确填写了 `mgmt_iface2` 或 `mgmt_mac2`，工具会把该主机视为双管理口机器；即使 bundle 默认只列了一个管理口，也会按双管理口处理。

If you want to keep RDMA non-IP actions such as udev naming, RoCE adaptive routing, ring buffer tuning, and `mlxconfig`, but skip RDMA IP/routes/policy rules, set `rdma_configure_ip_route` to `false`. In that mode, `rdma_interfaces` only needs interface names.

如果只想保留 RDMA 的非 IP 动作，比如 udev 固定名、RoCE adaptive routing、ring buffer 调优和 `mlxconfig`，但不配置 RDMA IP、路由和 policy rule，可以把 `rdma_configure_ip_route` 设置为 `false`。这种模式下，`rdma_interfaces` 只需要写网卡名。

```json
"rdma_configure_ip_route": false,
"rdma_interfaces": [
  { "name": "ens11np0" },
  { "name": "ens13np0" },
  { "name": "ens15np0" },
  { "name": "ens17np0" }
]
```

For XRE, the tool runs `bash <xre_installer> <xre_args...>`. If the installer supports non-interactive flags, put them in `artifacts.xre_args`. `xre.card_model` is required when an XRE installer is configured.

对于 XRE，工具实际执行的是 `bash <xre_installer> <xre_args...>`。如果安装器支持跳过交互的参数，就写到 `artifacts.xre_args` 里。配置 XRE 安装包时必须填写 `xre.card_model`。

For `P900`, no additional post-install tuning is currently applied. For `P800`, after the driver is installed and ready, the tool reads `xpu-smi -q` and checks every `XPU Part Number`. `B00100300110112` is treated as VC and keeps the default configuration. `B00100300110312` is treated as VD; the tool backs up `/etc/modprobe.d/kunlun.conf`, overwrites it with `C2CHighSpeed=1`, kills processes using `/dev/xpu*`, and serially runs `rmmod kunlun_peermem`, `rmmod kunlun`, `modprobe kunlun`, and `modprobe kunlun_peermem`. Add `lsof` to `packages` for P800 installations. Unknown part numbers or mixed VC/VD cards fail the XRE stage.

对于 `P900`，目前安装后不做额外调优。对于 `P800`，工具会在驱动安装完成并等待就绪后读取 `xpu-smi -q`，检查每一张卡的 `XPU Part Number`。`B00100300110112` 识别为 VC，保持默认配置；`B00100300110312` 识别为 VD，工具会备份 `/etc/modprobe.d/kunlun.conf`，覆盖写入 `C2CHighSpeed=1`，清理占用 `/dev/xpu*` 的进程，然后严格串行执行 `rmmod kunlun_peermem`、`rmmod kunlun`、`modprobe kunlun` 和 `modprobe kunlun_peermem`。P800 安装时需要在 `packages` 中加入 `lsof`。遇到未知 PN 或 VC/VD 混插时，XRE 阶段会直接失败。

Example:

示例：

```json
"artifacts": {
  "xre_installer": "/mnt/usb/xre-Linux-x86_64-5.0.21.24.4.run",
  "xre_args": ["--silent", "--accept-license"]
},
"xre": {
  "card_model": "P800"
}
```

Before the `network` stage runs, the tool checks that the expected interface names already exist. The default order runs `ofed` before `udev`; the `udev` stage can discover RDMA devices after OFED, write MAC-based persistent naming rules, and temporarily rename RDMA interfaces for the current boot so the later network, route, NCCL, and sglang configuration can use the canonical names immediately.

在执行 `network` 阶段前，工具会先检查目标接口名是否已经真实存在。默认顺序会先执行 `ofed` 再执行 `udev`；`udev` 阶段会在 OFED 后发现 RDMA 设备，写入基于 MAC 的持久化命名规则，并在当前启动周期里临时 rename RDMA 网卡，这样后续网络、路由、NCCL 和 sglang 配置可以立刻使用统一网卡名。

## Relationship To The Old Scripts / 与旧脚本的关系

The old shell and Python scripts are still kept in the repository as references. The Go tool is intended to replace running those scripts one by one manually.

仓库里的旧 shell 和 Python 脚本仍然保留，作为参考和回溯材料。新的 Go 工具目标是替代逐个手动执行这些脚本。
