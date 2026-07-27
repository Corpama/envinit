# XCCL XPU/NIC 拓扑分配与跨主机 Rail 对齐算法

本文说明 envinit 在 XCCL 检查中如何完成以下工作：

1. 从 inventory 和目标机运行状态中识别参测 RDMA 网卡。
2. 根据 `xpu-smi topo -m` 为每张 XPU 选择本机 RDMA 网卡。
3. 为本机映射结果生成 rail 身份。
4. 在多台机器之间按 rail 重排逻辑 XPU/rank 顺序。
5. 校验多机拓扑是否具备可比性。
6. 在信息不足或拓扑不理想时决定告警、退化或停止。

## 1. 核心原则

当前实现不是“topo 匹配失败后退化为 rail 匹配”。topo 和 rail 是连续执行、职责不同的两层算法：

```text
inventory 中的 rdmaN
        |
        v
解析 Linux 网卡对应的 RDMA device
        |
        v
解析每台机器的 xpu-smi topo -m
        |
        v
本机 XPU <-> NIC 全局最优分配
        |
        v
为每个 XPU/NIC 结果附加 rail 身份
        |
        v
以第一台机器为基准进行跨主机 rail 对齐
        |
        v
校验 XPU 数量、共享结构、链路等级和 rail 顺序
        |
        v
生成每台机器独立且值相同的 XPU_VISIBLE_DEVICES、CUDA_VISIBLE_DEVICES 和 BKCL 网卡顺序
```

- topo 层解决：一台机器内部，每张 XPU 应使用哪张本地 RDMA 网卡。
- rail 层解决：多台机器的物理网卡和 XPU 顺序不同时，逻辑 rank 应如何重排。
- rail 不替代 topo。无法解析本机 topo 或参测 RDMA device 不在 topo 中时，XCCL 检查会停止。

主要实现位于：

- `internal/checker/topology.go`
- `internal/checker/xccl.go`
- `internal/checker/counters.go`

## 2. 第一阶段：确定参测 RDMA 网卡

程序首先读取每台机器 inventory 中实际存在的动态 RDMA 字段：

```text
rdma1_name
rdma2_name
...
rdmaN_name
```

参测数量不固定为 4，inventory 可以扩展到 `rdma8_*` 或更多。对于 8 张 XPU、8 张 RoCE 网卡的一对一测试，实际 planning inventory 必须包含全部 8 张参测网卡。

程序对每个 Linux 接口查询 sysfs：

```bash
for d in /sys/class/net/<iface>/device/infiniband/*; do
    [ -e "$d" ] || continue
    basename "$d"
done
```

从而获得该机器本地的真实映射，例如：

```text
ens11f0np0 -> mlx5_0
ens11f1np1 -> mlx5_1
ens13f0np0 -> mlx5_4
```

每个 Linux 接口必须恰好解析到一个 RDMA device：

- 没找到 RDMA device：停止。
- 同一个接口找到多个 RDMA device：停止。
- bundle 中旧的 `ib_device` 与实际 sysfs 结果不同：使用现场实际结果并输出告警。

因此，多机 XCCL 不要求不同机器具有相同的 Linux 接口名或 `mlx5_N` 编号。

## 3. 第二阶段：解析 xpu-smi topo

程序在每台参测机器执行只读命令：

```bash
xpu-smi topo -m
```

解析结果包含两类信息。

### 3.1 NIC 列与 RDMA device 对应关系

例如：

```text
NIC0 -> mlx5_0
NIC1 -> mlx5_1
NIC2 -> mlx5_4
```

当前解析器支持：

- topo 表头使用 `NIC0`、`NIC1`，并在 `NIC Legend` 中给出 `mlx5_N`。
- topo 表头直接使用 `mlx5_0`、`mlx5_1`。
- `hns_0`、`bnxt_re1`、`irdma2` 等包含数字的通用 RDMA device 名称。

### 3.2 XPU 到 NIC 的链路等级

例如：

```text
XPU0 -> NIC0=PIX, NIC1=PIX, NIC2=SYS
XPU1 -> NIC0=PIX, NIC1=PIX, NIC2=SYS
```

程序只接受以下链路等级，并按从优到劣赋予 rank：

| topo 值 | rank | 含义 |
| --- | ---: | --- |
| `PIX` | 0 | 最多经过一个 PCIe bridge |
| `PXB` | 1 | 经过多个 PCIe bridge，但不经过 PCIe Host Bridge |
| `PHB` | 2 | 经过 PCIe Host Bridge |
| `NODE` | 3 | 跨同一 NUMA 节点内的 Host Bridge |
| `SYS` | 4 | 跨 NUMA/CPU 互联 |

未知、空缺或无法识别的链路不作为可用候选。

如果无法找到完整的 XPU/NIC 矩阵或 NIC legend/mapping，程序停止，不使用 rail 绕过本机 topo 发现。

## 4. 第三阶段：本机 XPU/NIC 全局分配

每个 inventory RDMA 接口形成一个候选项：

```text
候选 = Linux iface + RDMA device + topo NIC 列 + rail 身份
```

例如：

```text
ens11f0np0 + mlx5_0 + NIC0 + 10.61.11.0/24
```

### 4.1 为什么不是逐 XPU 贪心选择

考虑以下 topo：

```text
XPU0: NIC0=PIX, NIC1=PIX
XPU1: NIC0=PIX, NIC1=SYS
```

如果先让 XPU0 选择 NIC0，XPU1 可能只能选择 NIC1/SYS。全局最优结果应为：

```text
XPU0 -> NIC1/PIX
XPU1 -> NIC0/PIX
```

当前实现使用矩形匈牙利算法一次性求解所有 XPU 的分配，避免早期选择破坏后续受限 XPU 的最优路径。

### 4.2 代价优先级

算法构造的代价依次包含：

1. topo 链路 rank，权重最高。
2. 同一张网卡的重复使用次数。
3. inventory 候选顺序，作为稳定的最终 tie-break。

概念上的代价可表示为：

```text
cost = link_rank * topology_weight
     + current_nic_load * balance_weight
     + inventory_index_tiebreak
```

`topology_weight` 被设置为足够大，保证整体链路等级优先于负载均衡。只有拓扑质量相同的方案才进一步比较网卡负载。

### 4.3 网卡数与 XPU 数不同的处理

算法为每张候选网卡构造多个带递增 load 的虚拟 slot，所以允许多个 XPU 共用同一张网卡，但重复使用会增加代价。

8 XPU、4 NIC 的典型结果可能是：

```text
XPU0 -> eth1/PIX
XPU1 -> eth1/PIX
XPU2 -> eth2/PIX
XPU3 -> eth2/PIX
XPU4 -> eth3/PIX
XPU5 -> eth3/PIX
XPU6 -> eth4/PIX
XPU7 -> eth4/PIX
```

8 XPU、8 NIC 且存在一对一等价 PIX 路径时，负载均衡代价会推动 8 张网卡各使用一次。

### 4.4 topo 退化规则

如果某张 XPU 没有 PIX，但存在 PXB、PHB、NODE 或 SYS 路径，程序会选择其中全局最优的路径，并输出：

```text
WARN xccl topology degraded
```

如果某张 XPU 对所有参测 NIC 都没有可识别路径，则停止：

```text
XPU<n> has no reachable participating NIC
```

这是 topo 层真正的退化；rail 层不会替代不可用的本机拓扑。

## 5. 第四阶段：为每张网卡确定 rail 身份

rail 是“跨主机如何判断两个本地接口代表同一条逻辑通信路径”的稳定身份。它不要求两台机器使用相同接口名或 RDMA device 编号。

### 5.1 显式 rail_id

如果 inventory 的 `rdmaN_rail_id` 非空：

```csv
rdma1_rail_id=fabric-a
```

程序使用：

```text
explicit:fabric-a
```

作为该接口的 rail 身份。不同机器上完全相同的 rail ID 被视为同一条 rail。

显式 rail ID 的优先级最高，不再根据 IP 网段推导。

### 5.2 使用 IP 网段自动推导

`rdmaN_rail_id` 为空时，程序读取：

```text
rdmaN_ip
rdmaN_prefix
```

例如：

```text
10.61.13.43/24 -> 10.61.13.0/24
10.61.13.27/24 -> 10.61.13.0/24
```

两者归一化后相同，因此被认为属于同一 rail。

prefix 的获取顺序为：

1. inventory 中的 `rdmaN_prefix`。
2. bundle 中的默认 `rdma_prefix`。
3. IPv4 最后默认使用 `/24`。

这里进行的是身份推导，不是网络连通性探测。程序不会通过 ping、LLDP、交换机信息或路由表证明两个接口物理上属于同一条 rail。

### 5.3 槽位顺序回退

如果 rail ID 为空，并且 IP 也无法解析，程序使用：

```text
slot:rdma1
slot:rdma2
...
```

这种情况下只能保留本机 topo 结果和 inventory 槽位顺序，并输出警告：

```text
WARN xccl rail inference: ... no RDMA IP/prefix ...
retaining topology/inventory order
```

槽位回退可以支持信息不完整时的诊断试跑，但不能准确表达复杂的跨主机物理布线。

## 6. 第五阶段：跨主机 rail 对齐

rail 对齐仅在以下条件同时成立时执行：

- 选择了多台机器。
- `xpu_ordering` 最终解析为 `rail_aligned`。

默认决策规则：

```text
layout=full_ring + xpu_ordering=auto
=> 先比较物理 rail 顺序
=> 已一致：physical
=> 不一致：rail_aligned

layout=same_index + xpu_ordering=auto
=> physical
```

因此：

- 多机大环默认先保留物理 XPU 顺序，仅在跨主机 rail 顺序不一致时执行重排。
- 多机同号卡默认保持物理 XPU 编号。
- 用户可以显式设置 `rail_aligned` 或 `physical`。

实现层会对大环 `auto` 调用 rail 对齐算法，但 rail 已一致时得到的是恒等置换，实际可见卡顺序仍为 `0,1,...`。TUI 参数页只显示尚未解析的策略 `auto (physical first; rail fallback)`；结果详情根据最终置换显示实际的 `physical` 或 `rail_aligned`，并附带判定原因。

### 6.1 基准节点

第一台选中的机器是基准节点。它在本机 topo 分配后形成的 rail 顺序成为所有其他机器的目标逻辑顺序。

例如基准节点：

```text
物理 XPU:   0  1  2  3  4  5  6  7
rail:      11 12 13 14 15 16 17 18
```

第二台机器：

```text
物理 XPU:   0  1  2  3  4  5  6  7
rail:      13 14 11 12 17 18 15 16
```

程序依次为基准 rail 查找第二台机器中尚未使用的相同 rail：

```text
11 -> 原位置 2
12 -> 原位置 3
13 -> 原位置 0
14 -> 原位置 1
15 -> 原位置 6
16 -> 原位置 7
17 -> 原位置 4
18 -> 原位置 5
```

最终得到置换：

```text
2,3,0,1,6,7,4,5
```

### 6.2 重排的字段

对齐不是只修改可见设备变量，而是同时重排以下关联数据：

```text
XPUOrder
RDMANICOrder
RDMADeviceOrder
RDMALinkOrder
RDMARailOrder
Mapping
```

因此第二台机器最终会并行设置两个值完全相同的可见设备变量：

```text
XPU_VISIBLE_DEVICES=2,3,0,1,6,7,4,5
CUDA_VISIBLE_DEVICES=2,3,0,1,6,7,4,5
BKCL_RDMA_NICS=<按逻辑 rail 对齐后的本机接口列表>
BKCL_FORCE_RDMA_NICS_ORDER=<相同的逐 XPU 接口列表>
```

昆仑芯运行时对 `XPU_VISIBLE_DEVICES` 的优先级高于 `CUDA_VISIBLE_DEVICES`。envinit 仍同时设置两者，以便兼容已有脚本和运行时，但不允许它们出现不同的排列。

这样可以得到：

```text
逻辑 rank 0:
  节点 A 物理 XPU0 -> rail11
  节点 B 物理 XPU2 -> rail11

逻辑 rank 1:
  节点 A 物理 XPU1 -> rail12
  节点 B 物理 XPU3 -> rail12
```

而不是强制要求所有机器的物理 XPU0 互相对应。

### 6.3 同一 rail 重复出现

4 NIC 服务 8 XPU 时，rail 顺序可能为：

```text
11,11,13,13,15,15,17,17
```

匹配规则为：

1. rail 身份必须相同。
2. 对端每个位置最多使用一次。
3. 同一 rail 有多个候选时，优先选择与基准位置 topo link class 相同的候选。
4. rail 和 link class 都相同时，保持稳定的原始顺序。

## 7. 同网段多 HCA 的处理

假设同一台机器存在：

```text
eth1 10.61.10.11/24
eth2 10.61.10.12/24
eth3 10.61.10.13/24
eth4 10.61.10.14/24
```

自动推导的 rail 全部是：

```text
10.61.10.0/24
```

程序可以判断：

- 这些接口处于同一个 IP fabric。
- 每张 XPU 到每张网卡的 topo 距离。
- 本机如何进行 PIX 优先的最优分配。

但仅凭这些信息，程序无法证明：

```text
node-a eth1 必须对应 node-b eth3
```

当前策略是：

- 不因为同网段多 HCA 直接失败。
- 视为共享 fabric。
- 保留本机 topo 分配和 inventory 稳定顺序。
- 输出 shared-fabric rail inference 警告。

如果这些网卡确实属于同一个完全互通的共享 RoCE fabric，应保持 `rdmaN_rail_id` 为空。

如果相同网段下实际存在彼此隔离的物理 rail，则必须人工提供对应关系，例如：

```csv
host_id,rdma1_name,rdma1_ip,rdma1_prefix,rdma1_rail_id,rdma2_name,rdma2_ip,rdma2_prefix,rdma2_rail_id
node-a,ens11np0,10.61.10.11,24,fabric-a,ens13np0,10.61.10.12,24,fabric-b
node-b,ens7f0np0,10.61.10.21,24,fabric-b,ens3f0np0,10.61.10.22,24,fabric-a
```

相同的 `fabric-a` 表示跨主机对应的同一条物理 rail，与本地 `rdmaN` 序号或接口名无关。

## 8. 第六阶段：跨主机一致性校验

完成 rail 对齐后，程序以第一台机器为基准进行以下检查。

### 8.1 XPU 数量

每台机器的 XPU 数量必须相同，例如：

```text
xpu-07=8
xpu-23=8
```

### 8.2 XPU/NIC 共享结构

校验不比较本地设备名，而是把设备复用关系转换成抽象形状。

例如：

```text
节点 A: mlx5_0,mlx5_0,mlx5_4,mlx5_4 -> 0,0,1,1
节点 B: mlx5_8,mlx5_8,mlx5_2,mlx5_2 -> 0,0,1,1
```

虽然网卡名和 RDMA device 编号不同，但共享结构一致，因此可以通过。

下面两种结构不同：

```text
0,0,1,1
0,1,2,3
```

### 8.3 topo link class 顺序

重排后的每个逻辑位置必须具有相同链路等级，例如：

```text
节点 A: PIX,PIX,PIX,PIX
节点 B: PIX,PIX,PIX,PIX
```

如果一个位置在基准节点为 PIX、另一节点为 SYS，默认校验会失败。

### 8.4 rail 顺序

重排后的 rail 序列必须完全相同，例如：

```text
11,12,13,14,15,16,17,18
```

找不到完整 rail 对应关系时，对齐不会产生完整置换，最终由一致性校验明确报错。

### 8.5 关闭校验的边界

设置：

```json
"validate_topology": false
```

只会跳过以上跨主机一致性比较，不会跳过：

- sysfs RDMA device 解析。
- `xpu-smi topo -m` 解析。
- 本机 XPU/NIC 分配。
- XCCL 运行物料和参数校验。
- 每台机器环境变量生成。

关闭校验只应用于已知风险下的诊断试跑，不能修复错误布线或错误的 XPU/NIC 映射。

## 9. xpu-07 与 xpu-23 示例

根据当前现场拓扑和 IP 规划，两台机器的本机 topo 分配可抽象为：

### 9.1 xpu-07

```text
XPU0 -> ens11f0np0 -> 10.61.11.0/24
XPU1 -> ens11f1np1 -> 10.61.12.0/24
XPU2 -> ens13f0np0 -> 10.61.13.0/24
XPU3 -> ens13f1np1 -> 10.61.14.0/24
XPU4 -> ens15f0np0 -> 10.61.15.0/24
XPU5 -> ens15f1np1 -> 10.61.16.0/24
XPU6 -> ens17f0np0 -> 10.61.17.0/24
XPU7 -> ens17f1np1 -> 10.61.18.0/24
```

### 9.2 xpu-23

```text
XPU0 -> ens3f0np0 -> 10.61.13.0/24
XPU1 -> ens3f1np1 -> 10.61.14.0/24
XPU2 -> ens1f0np0 -> 10.61.11.0/24
XPU3 -> ens1f1np1 -> 10.61.12.0/24
XPU4 -> ens7f0np0 -> 10.61.17.0/24
XPU5 -> ens7f1np1 -> 10.61.18.0/24
XPU6 -> ens5f0np0 -> 10.61.15.0/24
XPU7 -> ens5f1np1 -> 10.61.16.0/24
```

以 xpu-07 为基准，xpu-23 的逻辑顺序应重排为：

```text
XPU_VISIBLE_DEVICES=2,3,0,1,6,7,4,5
CUDA_VISIBLE_DEVICES=2,3,0,1,6,7,4,5
```

重排后，两台机器的逻辑 rail 顺序都是：

```text
10.61.11.0/24
10.61.12.0/24
10.61.13.0/24
10.61.14.0/24
10.61.15.0/24
10.61.16.0/24
10.61.17.0/24
10.61.18.0/24
```

这组环境具有八个不同网段，可以直接自动推导 rail，`rdmaN_rail_id` 应保持为空。

## 10. 运行日志的核对方法

8 XPU、8 NIC、多机大环环境中，Raw Logs 应至少满足：

```text
xpu-07 unique_rdma_nics(8)=...
xpu-23 unique_rdma_nics(8)=...

xpu-07 xpu_order=0,1,2,3,4,5,6,7
xpu-23 xpu_order=2,3,0,1,6,7,4,5

rail_order=10.61.11.0/24,...,10.61.18.0/24
INFO xccl ranks: np=16 source=auto discovered_xpus=16
```

重点检查：

- `unique_rdma_nics(8)`：实际使用了 8 张不同物理网卡。
- `rdma_nics(8)`：逐 XPU 展开的接口映射有 8 项。
- `force_order(8)`：注入 BKCL 的逐 XPU 强制顺序有 8 项。
- `xpu_order`：显示该节点最终使用的物理 XPU 置换。
- `rail_order`：所有节点重排后完全一致。
- `mapping`：每张物理 XPU 对应的本机接口、RDMA device 和 link class。

如果 8 NIC 环境仍显示：

```text
unique_rdma_nics(4)
```

说明实际 planning inventory 只提供了 4 张参测网卡，或者另外 4 张网卡没有成功解析为候选。此时程序可能形成 4 NIC 服务 8 XPU 的映射，但不属于预期的 8 卡一对一测试。

## 11. 算法边界

当前算法能够自动处理：

- 不同机器 Linux 网卡名不同。
- 不同机器 `mlx5_N` 等 RDMA device 编号不同。
- 不同机器物理 XPU/rail 顺序不同。
- 4 NIC/8 XPU 和 8 NIC/8 XPU 等不同共享结构。
- PIX 不可用时选择 PXB、PHB、NODE 或 SYS 并明确告警。
- 通过 IP 网段或显式 rail ID 建立跨主机逻辑对应关系。

当前算法不能仅凭本机信息证明：

- 相同 IP 网段中的多张网卡分别连接到哪个隔离交换 fabric。
- 交换机端口、VLAN、PFC/ECN、ACL 或物理链路是否真正互通。
- rail IP 可达是否等同于 RDMA 数据面可用。
- XCCL/BKCL 运行时、固件和驱动组合是否一定能完成 collective。

因此，topo 和 rail 算法保证的是“命令生成和逻辑映射正确且可解释”，最终数据面是否跑通仍需要实际 XCCL 测试验证。

## 12. 一句话总结

envinit 先根据每台机器自己的 `xpu-smi topo -m`，以 PIX 优先和全局负载均衡方式确定物理 XPU 到本机 RDMA 网卡的映射；再根据显式 `rail_id` 或 RDMA IP 网段构造跨主机 rail 身份，以第一台机器为基准重排其他机器的逻辑 XPU/rank 顺序，最后校验共享结构、链路等级和 rail 顺序是否一致。
