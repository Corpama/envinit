# envinit 现场使用手册

## 1. 用途

`envinit` 用于在离线环境中初始化昆仑芯服务器。它读取两份规划文件：

- `bundle.json`：一批机器共用的安装参数、离线物料路径和默认值。
- `planning.csv`：逐台机器的管理网和 RDMA 网卡规划。

代码仓库当前提供的规划表文件名是 `env_tool/planning/inventory.csv`。它和本文所说的 `planning.csv` 是同一种文件，命令行参数统一写为 `--inventory`。现场可以继续使用现有文件名，也可以复制后改名为 `planning.csv`。

工具提供三个子命令：

| 子命令 | 用途 | 是否修改系统 |
| --- | --- | --- |
| `plan` | 解析规划文件，打印目标机器、写入文件和执行动作 | 否 |
| `apply` | 按 stage 执行初始化 | 是，需要 `root` |
| `check` | 在两台或更多机器之间执行 RDMA 带宽和大包 ping 检查 | 否，但需要 SSH 和 RDMA 环境 |

## 2. 文件放置

U 盘挂载到 `/mnt/usb` 后，推荐保留以下结构：

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
        ├── repo/
        ├── MLNX_OFED_*.tgz
        ├── xre-*.run
        ├── xdr_*.tar.gz
        ├── update_fw_*.tar.gz
        └── *.deb
```

二进制选择：

- `env_init`：x86-64 机器。
- `env_init_arch`：ARM64 / aarch64 机器。

首次执行前确认二进制可执行：

```bash
cd /mnt/usb/env_tool
chmod +x env_init run1.sh run2.sh
```

## 3. 推荐工作流

### 3.1 准备规划文件

先编辑：

```text
/mnt/usb/env_tool/planning/bundle.json
/mnt/usb/env_tool/planning/inventory.csv
```

编写原则：

- 整批机器一致的参数写入 `bundle.json`，例如默认网卡名、MTU、离线包路径和固件路径。
- 每台机器不同的参数写入 `planning.csv`，例如 hostname、管理 IP、RDMA IP 和 MAC。
- 网卡名可能随 BIOS、内核和发行版变化。正式交付时建议在 `planning.csv` 中填写 MAC。
- 同一批机器中，`rdma1` 到 `rdma4` 的物理端口顺序必须保持一致。

### 3.2 先执行预览

在每台目标机上运行：

```bash
cd /mnt/usb/env_tool
./env_init plan \
  --inventory /mnt/usb/env_tool/planning/inventory.csv \
  --bundle /mnt/usb/env_tool/planning/bundle.json \
  --host node1
```

将 `node1` 替换为当前机器在规划表中的 `host_id`、`hostname` 或 `mgmt_ip`。重点检查输出中的：

- `Target machine`
- `Hostname`
- `Management network`
- RDMA 网卡顺序
- `Stages`
- `Files to be written`
- `Detailed actions`

不传 `--host` 时工具会尝试根据当前 hostname、IP 或本机 MAC 自动匹配。现场首次操作建议显式传入 `--host`，这样也会按规划表修正系统 hostname。

### 3.3 执行初始化

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

### 3.4 初始化后检查

至少提供两台机器：

```bash
sudo ./env_init check \
  --inventory /mnt/usb/env_tool/planning/inventory.csv \
  --bundle /mnt/usb/env_tool/planning/bundle.json \
  --hosts node1,node2
```

默认同时执行：

- `bandwidth`：使用 `ib_write_bw` 做 RDMA 带宽测试。
- `rdma-ping`：在参与机器的所有 400G RDMA 网卡之间执行交叉大包 ping。

只检查某一项：

```bash
sudo ./env_init check \
  --inventory /mnt/usb/env_tool/planning/inventory.csv \
  --bundle /mnt/usb/env_tool/planning/bundle.json \
  --hosts node1,node2 \
  --check-stage rdma-ping
```

如果 `planning.csv` 没有填写 RDMA IP，不能执行 `rdma-ping`。

## 4. apply 具体执行内容

### 4.1 执行规则

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

`apply` 必须使用 `root` 权限。正式执行前建议先将 `apply` 替换为 `plan`，检查工具打印的 `Files to be written` 和 `Detailed actions`。

### 4.2 Stage 总览

| Stage | 主要操作 | 作用 | 典型写入位置或命令 |
| --- | --- | --- | --- |
| `software` | 复制离线软件源、配置 apt/yum 源、安装依赖包 | 在无外网环境中准备后续编译和安装需要的软件 | `/opt/repo`、`apt-get install`、`yum install` |
| `ofed` | 解压并安装 Mellanox OFED | 安装 RDMA 网卡驱动和用户态工具，使 400G 网卡可用于 RoCE/RDMA | `mlnxofedinstall --add-kernel-support` |
| `network` | 确认网卡绑定、临时重命名、写入并应用管理网/RDMA 网络配置、写入持久化命名规则 | 配置管理面连通性，让每张 RDMA 网卡使用独立地址和路由表，并固化规划网卡名 | `/etc/netplan/`、`/etc/sysconfig/network-scripts/`、`/etc/udev/rules.d/70-persistent-net.rules`、`netplan apply`、`nmcli`、`ifup` |
| `udev` | 兼容/修复 stage，单独重新生成持久化命名规则 | 在需要修复规则文件时复用 NIC Binding Review TUI，不负责临时重命名和网络配置 | `/etc/udev/rules.d/70-persistent-net.rules`、`udevadm control --reload-rules` |
| `xre` | 安装 XRE 驱动，并按卡型执行必要调优 | 让操作系统识别和管理昆仑芯 XPU | `bash <xre_installer>` |
| `xdr` | 编译并安装 XDR 内核模块 | 提供 XDR 数据通路，支持相关高速传输能力 | `build.sh`、`install.sh` |
| `firmware` | 解压并升级算力卡固件 | 将算力卡固件更新到配套版本 | `bash auto_update.sh` |
| `container` | 安装 XPU 容器相关离线 `.deb` | 让容器运行时能够向容器暴露 XPU 设备和工具 | `dpkg -i` |
| `mlxconfig` | 配置 Mellanox 网卡参数 | 将网卡固件参数设置为集群要求的值 | `mst start`、`mlxconfig set` |
| `sysctl` | 追加内核网络参数并立即生效 | 增大网络缓冲区并调整多网卡主机的 ARP、反向路径检查行为 | `/etc/sysctl.conf`、`sysctl -p` |
| `kernel` | 补充 grub 内核启动参数并刷新 grub | 固化启动参数，为设备访问和性能调优准备内核启动环境 | `/etc/default/grub`、`update-grub` |
| `post` | 写入开机 RDMA 调优服务，执行附加任务和可选电源动作 | 保证重启后自动恢复 RDMA 性能参数，并完成现场收尾 | `kunlun-post-boot.service`、`ipmitool power` |

### 4.3 software：配置离线软件源和软件包

`software` 会按当前平台自动选择 apt 或 yum 路径。

Ubuntu/Debian 路径下，当 `offline_apt.enabled=true` 时，工具会：

1. 将 U 盘中的离线仓库复制到目标机，例如 `/mnt/usb/env_tool/data/repo -> /opt/repo`。
2. 按需备份已有 apt 源。
3. 写入离线 apt 源文件。
4. 执行 `apt-get update` 和 `apt-get install -y`。

生成的 `/etc/apt/sources.list.d/kunlun-offline.list` 类似：

```text
deb [trusted=yes] file:/opt/repo jammy main
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

RedHat/麒麟路径下，工具会使用 `offline_repo` 生成 yum repo 文件，执行 `yum makecache` 和 `yum install -y <packages>`。因此命令行 stage 名统一使用 `software`，不再暴露成某个发行版专属的名称。

### 4.4 ofed 和 network：安装驱动并固定网卡名

`ofed` 会解压 `artifacts.ofed_archive`，然后执行类似命令：

```bash
./mlnxofedinstall \
  --without-fw-update \
  --add-kernel-support \
  -k "$(uname -r)" \
  --skip-distro-check \
  --force
```

`network` 阶段会先根据规划表和本机 `/sys/class/net` 自动发现物理网卡，再打开 NIC Binding Review TUI 让用户确认管理网和 RDMA 网卡绑定。确认后，工具会先在当前启动周期把实际网卡临时重命名为规划名称，再写入并应用管理网/RDMA 网络配置，最后根据同一份确认结果写入持久化 udev 命名规则。

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

### 4.5 network：配置管理网和 RDMA 网络

管理网始终会写入 `/etc/netplan/00-kunlun-bond.yaml`。如果规划表只填写一个管理口，则直接配置单口：

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

当 `rdma_configure_ip_route=true` 时，每个 RDMA 口都会生成一份 netplan，例如 `/etc/netplan/10-kunlun-ens11np0.yaml`：

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

同时生成 policy route 脚本，例如 `/etc/networkd-dispatcher/routable.d/config_rt_ens11np0.sh`。脚本会为对应网卡维护独立路由表：

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

如果设置 `"rdma_configure_ip_route": false`，工具仍会保留 RDMA 命名、RoCE adaptive routing 和后续调优，但跳过 RDMA IP、netplan 和 policy route。

### 4.6 xre、xdr、firmware 和 container：安装算力卡软件

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

### 4.7 mlxconfig、sysctl 和 kernel：系统调优

`mlxconfig` 会执行 `mst start`，自动扫描 `/dev/mst/*_pciconf*`，确认要配置的设备后仅修改不一致的参数。例如：

```bash
mlxconfig -y -d /dev/mst/mt4129_pciconf0 set CNP_DSCP_P1=48
```

上述示例将 `CNP_DSCP_P1` 设置为 `48`。该参数用于配置网卡拥塞通知报文的 DSCP 标记，使交换网络能够按集群 QoS 规划识别和处理相应流量。

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

### 4.8 post：开机服务、附加任务和电源动作

当 RDMA 启用时，工具会写入并启用：

```text
/usr/local/sbin/kunlun-post-boot.sh
/etc/systemd/system/kunlun-post-boot.service
```

该服务在开机后遍历 RDMA 网卡，执行两类调优：

```bash
ethtool -G ens11np0 rx 8192 tx 8192
mlxreg -d <PCI 地址> --reg_name ROCE_ACCL --set adaptive_routing_forced_en=0x1 --yes
```

关键命令作用：

| 命令 | 作用 |
| --- | --- |
| `ethtool -G ens11np0 rx 8192 tx 8192` | 将网卡接收和发送 ring buffer 深度设置为 `8192`。更深的 ring buffer 可以在突发流量或 CPU 短暂繁忙时容纳更多待处理报文，降低高速 RDMA 场景下因队列不足造成丢包的风险 |
| `ethtool -i ens11np0` | 查询网卡驱动信息和 PCI `bus-info`，供后续 `mlxreg` 精确定位硬件设备 |
| `mlxreg -d <PCI 地址> --reg_name ROCE_ACCL --set adaptive_routing_forced_en=0x1 --yes` | 修改 Mellanox 网卡的 `ROCE_ACCL` 寄存器，强制启用 RoCE adaptive routing。其目的是允许网络根据路径状态进行自适应路由，降低热点链路对 RDMA 流量的影响 |
| `systemctl enable kunlun-post-boot.service` | 将上述调优注册为开机服务，确保服务器重启后自动重新应用 |

`post` 阶段会先按顺序安装 `post_packages`，再写入上述开机服务，然后按顺序执行 `post_tasks`。最后根据 `post_power_action` 决定是否执行类似命令：

```bash
ipmitool power soft
```

默认会先要求人工确认。详细配置示例见 [6.7 post_packages、post_tasks 和 post_power_action](#67-post_packagespost_tasks-和-post_power_action)。

## 5. planning.csv 结构

### 5.1 推荐表头

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

工具也支持 `.tsv`、`.txt` 和 `.xlsx`。使用 `.xlsx` 时默认读取第一张表，也可以增加 `--sheet Sheet1`。

### 5.2 字段说明

| 字段 | 必填条件 | 说明 |
| --- | --- | --- |
| `host_id` | 建议填写 | 机器标识，可用于 `--host` 和 `--hosts` |
| `hostname` | 建议填写 | 目标 hostname；显式传入 `--host` 执行 `apply` 时会自动修正 |
| `mgmt_ip` | 可选 | 管理网 IPv4 地址；为空表示该机器不由工具配置管理网 |
| `mgmt_prefix` | 配置管理网时可选 | 管理网前缀，例如 `23`；为空时使用 bundle 默认值 |
| `mgmt_gateway` | 配置管理网时可选 | 管理网网关；为空时依次使用 bundle 默认值或按管理 IP 推导 `.1` |
| `mgmt_iface1`、`mgmt_iface2` | 配置管理网时可选 | 管理口名；为空时使用 bundle 默认值或自动发现/TUI 绑定 |
| `mgmt_mac1`、`mgmt_mac2` | 配置管理网时建议填写 | 管理口 MAC；填写后优先按 MAC 找真实网卡 |
| `mgmt_bond_name` | 可选 | 管理 bond 名称；为空时使用 bundle 默认值 |
| `mgmt_nameservers` | 可选 | DNS，可使用逗号、分号、竖线或空格分隔 |
| `rdmaN_name` | 建议填写 | 第 N 个 RDMA 口的目标接口名 |
| `rdmaN_ip` | 按模式填写 | 开启 RDMA IP 路由配置或执行 `rdma-ping` 时必须填写 |
| `rdmaN_mac` | 强烈建议填写 | 第 N 个 RDMA 口 MAC |
| `rdmaN_prefix` | 可选 | 第 N 个 RDMA 前缀；为空时使用 bundle 默认值 |
| `rdmaN_gateway` | 可选 | 第 N 个 RDMA 网关；为空时按 RDMA IP 推导 `.1` |
| `rdmaN_table` | 可选 | 第 N 个 RDMA 路由表号；为空时使用 bundle 默认值 |

其中 `N` 为 `1` 到 `4`。

接口解析优先级为：

```text
MAC -> planning.csv 中的接口名 -> bundle.json 中的默认接口名
```

如果只填写 `mgmt_iface1` 或 `mgmt_mac1`，并把第二个管理口留空，工具会配置单管理口，不创建 bond。

### 5.3 示例：不配置 RDMA IP

当 `bundle.json` 中设置 `"rdma_configure_ip_route": false` 时，可以只规划 RDMA 网卡名：

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

### 5.4 示例：配置 RDMA IP 和路由

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

### 5.5 如何写规划表

建议按以下顺序整理：

1. 为每台机器确定唯一的 `host_id` 和 `hostname`。
2. 记录管理 IP、前缀和网关。
3. 如果现场已经有明确端口信息，记录管理口和 RDMA 口的 MAC；没有 MAC 时可留空，由工具在 `network` 阶段自动发现并进入 TUI 复核。
4. 固定物理端口到 `rdma1` 至 `rdma4` 的规划含义。同一列必须表示同一类物理端口。
5. 如果需要 RDMA 三层网络，填写每个 `rdmaN_ip`；否则在 bundle 中关闭 `rdma_configure_ip_route`。
6. 每台机器先运行一次 `plan --host <host_id>`，确认解析结果再执行 `apply`。

## 6. bundle.json 结构

### 6.1 顶层结构

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

### 6.2 defaults

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
| `apply_network_immediately` | 是否立即应用网络配置；默认 `true`。关闭后只落文件，不执行 `netplan apply`、`nmcli up` 或 `ifup` |
| `rdma_exist` | 是否存在 RDMA 网卡；默认 `true` |
| `rdma_configure_ip_route` | 是否配置 RDMA IP、路由和 policy rule；默认 `true` |
| `rdma_prefix`、`rdma_mtu`、`rdma_route_cidr`、`route_priority` | RDMA 三层网络默认值 |
| `rdma_interfaces` | RDMA 默认目标名、路由表号和可选网关；通常可省略，由规划表和自动发现/TUI 绑定生成 |

平台专属的备份、禁用源策略建议放到 `platform_options`。旧配置里放在 `defaults` 下的 `backup_existing_netplan`、`backup_existing_network`、`disable_existing_apt_sources`、`disable_existing_repos` 仍然兼容。

如果某台机器不需要工具配置管理网，可以在 inventory 中留空 `mgmt_ip`。此时该机器会自动跳过管理网配置，也不会要求选择管理网卡；RDMA 网络、udev 持久化命名和其他 stage 仍按规划执行。若只是整批关闭管理网，也可以在 bundle 中设置 `"configure_management_network": false`。

历史配置中使用过拼写 `"rdma_exsist"`。当前程序同时兼容 `"rdma_exist"` 和 `"rdma_exsist"`，新配置建议使用正确拼写 `"rdma_exist"`。

开启 RDMA IP 路由配置：

```json
"rdma_exist": true,
"rdma_configure_ip_route": true
```

`rdma_interfaces` 仅作为兼容旧配置或强制覆盖目标名/路由表时使用。新配置建议优先从规划表读取 `rdmaN_name`、`rdmaN_ip`、`rdmaN_table`，缺失时再由默认规则补齐。

只保留 RDMA 非 IP 动作：

```json
"rdma_exist": true,
"rdma_configure_ip_route": false,
"rdma_interfaces": [
  { "name": "ens15np0" },
  { "name": "ens16np0" },
  { "name": "ens13np0" },
  { "name": "ens14np0" }
]
```

完全没有 RDMA 网卡：

```json
"rdma_exist": false
```

### 6.3 platform

不配置 `platform`，或将 `platform.os_family`、`package_manager`、`network_backend` 写成 `auto` 时，工具会根据当前系统自动选择 Ubuntu/Debian 或 RedHat/麒麟路径。

RedHat 类系统，例如 CentOS 7、麒麟 V10 SP3，可显式启用新路径：

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

### 6.4 platform_options

`platform_options` 存放平台专属的保护和清理策略。工具会按当前 `platform` 选择对应子项，子项缺省时回退到旧的 `defaults` 同名字段。

```json
"platform_options": {
  "ubuntu": {
    "backup_existing_netplan": true,
    "disable_existing_apt_sources": false
  },
  "redhat": {
    "backup_existing_network": true,
    "disable_existing_repos": false
  }
}
```

| 字段 | 说明 |
| --- | --- |
| `ubuntu.backup_existing_netplan` | Ubuntu 路径：是否备份已有 netplan YAML |
| `ubuntu.disable_existing_apt_sources` | Ubuntu 路径：是否备份并禁用已有 apt 源 |
| `redhat.backup_existing_network` | RedHat/麒麟 ifcfg 路径：是否备份已有 ifcfg、route、rule 文件 |
| `redhat.disable_existing_repos` | RedHat/麒麟 yum 路径：是否备份并禁用已有 `.repo` 文件 |

### 6.5 offline_apt / offline_repo 和 packages

```json
"offline_apt": {
  "enabled": true,
  "material_path": "/mnt/usb/env_tool/data/repo",
  "copy_to": "/opt/repo",
  "target_file": "/etc/apt/sources.list.d/kunlun-offline.list",
  "entries": [
    "deb [trusted=yes] file:{{offline_apt_target}} jammy main"
  ]
},
"packages": [
  "linux-headers-{{uname_r}}",
  "ipmitool",
  "bzip2",
  "gcc"
]
```

说明：

- `material_path` 是 U 盘中的离线源目录。
- `copy_to` 是复制到目标机后的路径。
- `entries` 支持占位符 `{{offline_apt_target}}`，运行时替换为 `copy_to`。
- `packages` 通过 `apt-get install -y` 安装。
- `packages` 支持占位符 `{{uname_r}}`，运行时替换为当前内核版本。

RedHat/麒麟 yum 路径建议使用 `offline_repo`：

```json
"offline_repo": {
  "enabled": true,
  "material_path": "/mnt/usb/rpm-repo",
  "copy_to": "/opt/rpm-repo"
}
```

`target_file` 和 `entries` 通常无需配置，工具会按 yum 平台生成 `/etc/yum.repos.d/kunlun-offline.repo` 和默认 repo 内容。若现场有特殊 repo 模板，`offline_repo.entries` 仍支持 `{{offline_repo_target}}`，运行时替换为 `copy_to`。

### 6.6 artifacts 和 xre

```json
"artifacts": {
  "work_dir": "/opt/kunlun",
  "ofed_archive": "/mnt/usb/env_tool/data/MLNX_OFED_LINUX-*.tgz",
  "xre_installer": "/mnt/usb/env_tool/data/xre-Linux-x86_64-*.run",
  "xre_args": ["-q"],
  "xdr_archive": "/mnt/usb/env_tool/data/xdr_copy-*.tar.gz",
  "firmware_archive": "/mnt/usb/env_tool/data/update_fw_*.tar.gz",
  "container_packages": [
    "/mnt/usb/env_tool/data/libxpu-container-tools_1.0.2-1_amd64.deb",
    "/mnt/usb/env_tool/data/libxpu-container1_1.0.2-1_amd64.deb",
    "/mnt/usb/env_tool/data/xpu-container-toolkit_1.0.5-1_amd64.deb"
  ]
},
"xre": {
  "card_model": "P900"
}
```

注意：

- 示例中的 `*` 仅表示需要按现场版本选择文件。JSON 中必须填写真实完整路径，不能直接使用通配符。
- 配置 `xre_installer` 时，必须填写 `xre.card_model`，可选值为 `P800`、`P900`。
- `P800` 安装建议在 `packages` 中增加 `lsof`。
- Ubuntu 路径下 `container_packages` 按数组顺序传给 `dpkg -i`；yum 路径下按数组顺序传给 `yum localinstall -y`，因此 RedHat/麒麟需要提供 `.rpm` 制品。

### 6.7 mlxconfig

```json
"mlxconfig": {
  "settings": {
    "CNP_DSCP_P1": "48"
  }
}
```

工具会运行 `mst start`，自动扫描 `/dev/mst/*_pciconf*`，让用户确认要配置的 MST 设备，并把选择持久化到 `/var/lib/envinit/mst-devices.json`。后续运行会优先复用该持久化选择。`device_glob` 仍作为兼容旧配置的强制覆盖项保留，通常不需要填写。

### 6.8 check

```json
"check": {
  "iterations": 100,
  "bandwidth_qps": 0,
  "min_gbits": 0,
  "parallel": true,
  "rdma_ping_count": 3,
  "rdma_ping_payload_size": 8972,
  "rdma_ping_timeout": 2,
  "ssh_user": "root",
  "ssh_options": [],
  "rdma_groups": [
    {
      "ib_device": "mlx5_1",
      "xpu_offsets": [
        "0x0000000090001000",
        "0x1000000090001000"
      ]
    },
    {
      "ib_device": "mlx5_2",
      "xpu_offsets": [
        "0x2000000090001000",
        "0x3000000090001000"
      ]
    },
    {
      "ib_device": "mlx5_3",
      "xpu_offsets": [
        "0x4000000090001000",
        "0x5000000090001000"
      ]
    },
    {
      "ib_device": "mlx5_4",
      "xpu_offsets": [
        "0x6000000090001000",
        "0x7000000090001000"
      ]
    }
  ]
}
```

说明：

- `iterations`：`ib_write_bw -n` 次数，默认 `100`。
- `bandwidth_qps`：`ib_write_bw -q` 的 QP 数；`0` 表示不显式传 `-q`，使用 perftest 默认值。
- `min_gbits`：最低带宽门槛；`0` 表示只记录，不按吞吐失败。
- `parallel`：是否按批次并发跑流。
- `rdma_ping_payload_size`：默认 `8972`，用于验证 IPv4 MTU 9000。
- `rdma_groups`：带宽检查的逻辑 RDMA 分组列表。运行时会优先按规划表或 defaults 中的 `rdmaN_name`，在每台目标机器上通过 `/sys/class/net/<iface>/device/infiniband/*` 自动解析实际 `mlx5_N`；这里的 `ib_device` 保留为 fallback 和 dry-run 预览值。
- `xpu_offsets`：仅在 `check --bandwidth-mmap xdr` 时使用。

### 6.9 post_packages、post_tasks 和 post_power_action

`post_packages` 用于按顺序安装额外本地包。Ubuntu 路径使用 `dpkg -i`，yum 路径使用 `yum localinstall -y`：

```json
"post_packages": [
  "/mnt/usb/env_tool/data/extra-package.deb"
]
```

`post_tasks` 用于执行安装后的附加动作，支持 `copy`、`cmd`、`mv`、`rm` 和 `mkdir`，下列以安装 `xpu_exporter` 为例：

```json
"post_tasks": [
  {
    "name": "install xpu_exporter",
    "type": "copy",
    "source": "/mnt/usb/env_tool/data/xpu_exporter",
    "target": "/usr/local/bin/xpu_exporter",
    "mode": "0755"
  },
  {
    "name": "install xpu_exporter service",
    "type": "copy",
    "source": "/mnt/usb/env_tool/data/xpu_exporter.service",
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

## 7. 常用检查命令

### 7.1 示例环境

下面用两台测试机举例。每台机器有四张 400G RDMA 网卡，单卡正常带宽约为 `390 Gbps`：

| 主机 | 管理 IP | `rdma1_ip` | `rdma2_ip` | `rdma3_ip` | `rdma4_ip` |
| --- | --- | --- | --- | --- | --- |
| `node-a` | `192.168.50.11` | `10.80.1.11` | `10.80.2.11` | `10.80.3.11` | `10.80.4.11` |
| `node-b` | `192.168.50.12` | `10.80.1.12` | `10.80.2.12` | `10.80.3.12` | `10.80.4.12` |

RDMA 网卡与 IB 设备的对应关系：

| 规划表字段 | RDMA 网卡 | `check.rdma_groups[].ib_device` fallback | 预期单流带宽 |
| --- | --- | --- | --- |
| `rdma1_ip` | `ens11np0` | `mlx5_1` | 约 `390 Gbps` |
| `rdma2_ip` | `ens13np0` | `mlx5_2` | 约 `390 Gbps` |
| `rdma3_ip` | `ens15np0` | `mlx5_3` | 约 `390 Gbps` |
| `rdma4_ip` | `ens17np0` | `mlx5_4` | 约 `390 Gbps` |

实际执行带宽测试时，工具不会假设所有机器的 `mlx5_N` 编号完全一致。它会按第 N 个 RDMA 网卡名解析本机 IB device，例如某台机器可能是 `rdma1_name=ens11np0 -> mlx5_1`，另一台机器也可能是 `rdma1_name=ens11np0 -> mlx5_2`；带宽命令会分别使用各自解析出的实际设备。这样可以避免 PCI 探测顺序不同导致 `mlx5_N` 整体偏移时测错卡。`xpu_offsets` 仍然绑定在第 N 个逻辑 RDMA group 上，例如第一个 group 的 offsets 始终跟随 `rdma1_name` 对应的网卡，而不是跟随某个固定的 `mlx5_N` 字符串。

对应的规划表示例：

| `host_id` | `hostname` | `mgmt_ip` | `rdma1_name` | `rdma1_ip` | `rdma2_name` | `rdma2_ip` | `rdma3_name` | `rdma3_ip` | `rdma4_name` | `rdma4_ip` |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `node-a` | `node-a` | `192.168.50.11` | `ens11np0` | `10.80.1.11` | `ens13np0` | `10.80.2.11` | `ens15np0` | `10.80.3.11` | `ens17np0` | `10.80.4.11` |
| `node-b` | `node-b` | `192.168.50.12` | `ens11np0` | `10.80.1.12` | `ens13np0` | `10.80.2.12` | `ens15np0` | `10.80.3.12` | `ens17np0` | `10.80.4.12` |

如果希望低于 `380 Gbps` 时直接判定失败，可以在 `bundle.json` 中设置：

```json
"check": {
  "iterations": 100,
  "bandwidth_qps": 0,
  "min_gbits": 380,
  "parallel": true,
  "rdma_ping_count": 3,
  "rdma_ping_payload_size": 8972,
  "rdma_ping_timeout": 2,
  "ssh_user": "root",
  "ssh_options": [],
  "rdma_groups": [
    { "ib_device": "mlx5_1" },
    { "ib_device": "mlx5_2" },
    { "ib_device": "mlx5_3" },
    { "ib_device": "mlx5_4" }
  ]
}
```

`min_gbits` 是每一条带宽流的最低门槛。示例中正常值约为 `390 Gbps`，因此使用 `380` 留出少量波动空间。首次摸底时也可以先设置为 `0`，只记录结果，不按吞吐失败。

### 7.2 完整检查

同时执行带宽检查和 RDMA 大包 ping：

```bash
sudo ./env_init check \
  --inventory /mnt/usb/env_tool/planning/inventory.csv \
  --bundle /mnt/usb/env_tool/planning/bundle.json \
  --hosts node-a,node-b
```

默认会双向检查，也就是同时覆盖 `node-a -> node-b` 和 `node-b -> node-a`。

### 7.3 只检查带宽

```bash
sudo ./env_init check \
  --inventory /mnt/usb/env_tool/planning/inventory.csv \
  --bundle /mnt/usb/env_tool/planning/bundle.json \
  --hosts node-a,node-b \
  --check-stage bandwidth
```

四组 RDMA group 会形成交叉矩阵。每个方向共有 `4 x 4 = 16` 条流。设置 `"parallel": true` 后，每批并发执行 4 条流，并确保一张 400G 卡在同一批中只参与一条流，避免同一端口被多条流同时抢带宽。正式执行时，输出中的 `CLIENT_RDMA` / `SERVER_RDMA` 是逻辑分组，`CLIENT_DEV` / `SERVER_DEV` 是每台机器按网卡名解析后的实际 `mlx5_N`。

临时指定每条 `ib_write_bw` 流使用 4 个 QP：

```bash
sudo ./env_init check \
  --inventory /mnt/usb/env_tool/planning/inventory.csv \
  --bundle /mnt/usb/env_tool/planning/bundle.json \
  --hosts node-a,node-b \
  --check-stage bandwidth \
  --bandwidth-qps 4
```

`--bandwidth-qps` 会覆盖 `check.bandwidth_qps`。服务端和客户端使用相同的 `-q` 值；未指定命令行参数且 bundle 值为 `0` 时不传 `-q`。

正常输出示意：

```text
Bandwidth result summary:
STATUS  CLIENT  SERVER  CLIENT_RDMA  SERVER_RDMA  CLIENT_DEV  SERVER_DEV  PORT   CLIENT_XPU  SERVER_XPU  BANDWIDTH
PASS    node-a  node-b  rdma1        rdma1        mlx5_1      mlx5_2      18515  -           -           391.42 Gbps
PASS    node-a  node-b  rdma2        rdma2        mlx5_2      mlx5_3      18520  -           -           389.87 Gbps
PASS    node-a  node-b  rdma3        rdma3        mlx5_3      mlx5_4      18525  -           -           390.16 Gbps
PASS    node-a  node-b  rdma4        rdma4        mlx5_4      mlx5_5      18530  -           -           388.94 Gbps
```

实际输出会包含完整交叉矩阵。只要某一行低于 `check.min_gbits`，该行会标记为 `FAIL`，整个 `check` 返回失败。

### 7.4 只检查 RDMA 大包 ping

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

工具会执行完整交叉矩阵：源机器的每个 `rdmaN_name` 都会 ping 目标机器的每个 `rdmaN_ip`。例如两台机器各有四张 400G 网卡时，`node-a -> node-b` 有 `4 x 4 = 16` 条，反向再执行 16 条，总计 32 条。一个机器对、一个方向内的 ping 会并发执行，但远端 SSH 执行会限制并发并对 `kex_exchange_identification`、`Connection reset by peer` 这类握手暂态错误自动重试。

`--rdma-ping-mtu 9000` 会自动换算为 IPv4 ping payload `8972`，用于确认 MTU 9000 链路没有分片。命令仍使用 `ping -I <源 RDMA 网卡> <目标 RDMA IP>`，因此也会暴露策略路由、源地址选择或交换网络跨子网连通性问题。

### 7.5 预览命令

预览检查命令，不实际跑流：

```bash
sudo ./env_init check \
  --inventory /mnt/usb/env_tool/planning/inventory.csv \
  --bundle /mnt/usb/env_tool/planning/bundle.json \
  --hosts node-a,node-b \
  --check-stage bandwidth \
  --dry-run
```

### 7.6 模拟 KV cache 传输

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

使用 `--bandwidth-mmap xdr` 时，需要在每个 `check.rdma_groups` 中填写对应的 `xpu_offsets`。每个 offset 只会通过其所属 RDMA group 发流。

### 7.7 如何判断结果

检查结束后重点查看以下汇总表：

| 汇总表 | 说明 |
| --- | --- |
| `Bandwidth result summary` | 每条 RDMA 带宽流的结果。示例环境中单流应接近 `390 Gbps` |
| `RDMA ping result summary` | 四组 RDMA 网络的大包 ping 是否成功 |
| `NIC counter delta summary` | 网卡计数器在检查前后的变化 |
| `RDMA device counter delta summary` | IB 设备 sysfs 计数器在带宽检查前后的变化 |

普通流量计数增长会显示为 `INFO`。丢包、超时、CRC、重传等风险计数增长会显示为 `FAIL`，并让检查失败。即使带宽接近 `390 Gbps`，也应确认没有异常计数增长。

## 8. 常见问题

### 8.1 `inventory row missing rdmaN_ip`

原因：`rdma_configure_ip_route=true`，但规划表没有填写对应 RDMA IP。

处理：补齐 `rdmaN_ip`，或者在不需要 RDMA 三层网络时将 `rdma_configure_ip_route` 设置为 `false`。

### 8.2 `expected interfaces are not present yet`

原因：目标接口名尚未出现，通常是当前系统网卡名还没有按规划名绑定，或 OFED 后 RDMA 网卡尚未暴露。

处理：先执行 `software ofed network`。`network` 阶段会先自动发现物理网卡并打开 NIC Binding Review TUI；确认后先临时重命名网卡，再写入并应用网络配置，并根据同一份确认结果写入持久化命名规则，重启后保持规划名。

### 8.3 MAC 找不到

原因：规划表中的 MAC 与本机实际网卡不一致。

处理：重新采集接口 MAC，检查是否填错机器或填错物理端口。

### 8.4 `rdma-ping` 不能运行

原因：规划表没有 RDMA IP，或者 RDMA IP 网络未配置。

处理：补齐 `rdmaN_ip` 并配置 RDMA 网络；只做带宽检查时可使用 `--check-stage bandwidth`。

### 8.5 `kex_exchange_identification: read: connection reset by peer`

原因：这是 SSH 握手阶段被对端断开，不是 RDMA ping 包超时。常见原因是同一时间连接数过多，触发了目标机 `sshd` 的 `MaxStartups` 或现场安全策略。

处理：工具会限制 `rdma-ping` 远端 SSH 并发并自动重试这类暂态错误。如果仍频繁出现，检查目标机 `/etc/ssh/sshd_config` 中的 `MaxStartups`、`MaxSessions`，以及安全组、防火墙或堡垒机连接限制。

### 8.6 如何降低首次执行风险

建议每台机器按以下顺序操作：

```bash
./env_init plan ... --host node1
sudo ./env_init apply ... --host node1 --stages software ofed
sudo ./env_init apply ... --host node1 --stages network
sudo ./env_init apply ... --host node1 --stages xre xdr firmware container mlxconfig sysctl kernel post
```

每一步完成后检查输出，再进入下一步。

## 9. 编译可执行文件

项目使用 Go 编写，仅依赖标准库。可以在安装了 Go 的开发机上交叉编译 Linux 可执行文件。

### 9.1 编译 x86_64 版本

适用于常见的 x86_64 / AMD64 Linux 服务器。生成文件名为 `env_init`：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -o env_init ./cmd/envinit
```

### 9.2 编译 ARM64 版本

适用于 ARM64 / aarch64 Linux 服务器。生成文件名为 `env_init_arch`：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -o env_init_arch ./cmd/envinit
```

### 9.3 检查生成结果

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
