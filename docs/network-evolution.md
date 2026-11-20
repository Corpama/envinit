# 网络配置演进与失败续跑

更新日期：2026-11-14。实现位于当前工作区，尚未完成 Linux 三后端、真实 RDMA 硬件及重启验收。

## 使用入口

不增加“改配模式”参数。修改 inventory/bundle 后，继续使用 `plan` 和 `apply --stages network`。物理绑定变化时选择需要的 `mlxconfig,sysctl,post` 等阶段；network 包含 udev 持久化。工具不会自动扩大到 `all` 或自动执行 post 电源动作。

网络阶段对比上次成功状态、本次目标和现场状态。文件未变化时也检查 MAC→名称、地址、MTU、master、bond 模式/成员及 RDMA 路由/规则；后端命令失败会返回错误，不再以文件已写入代表网络成功。全流程 checkpoint 中已经完成的 network 仍重新核验运行状态。

## 状态与恢复

| 路径 | 用途 |
| --- | --- |
| `/var/lib/envinit/network-files.json` | 成功状态，v2 包含受管文件、RDMA 路由、地址、物理绑定和 bond 拓扑 |
| `/var/lib/envinit/network-transaction.json` | 本次旧/新状态、文件写入意图、阶段及被暂停的旧路由脚本内容 |
| `/run/envinit-network-changing` | 在线切换期间暂停自动路由重放；重启后自然消失 |
| `/var/lib/envinit/sysctl-ownership.json` | 受管参数、原配置行、已知原运行值及物理 MAC |
| `/var/lib/envinit/sysctl-transaction.json` | sysctl 文件/运行值更新的恢复依据 |

执行顺序：确认绑定 → 校验 ownership/目标路径 → 预渲染（netplan 在隔离 root 中 generate）→ 保存事务 → 暂停旧路由重放 → 旧拓扑退出 → 临时改名 → 配置写入/旧文件回收 → 后端激活 → 残留清理 → 验证 → 提交成功 manifest。

文件替换前记录期望摘要。同路径覆盖和旧路径删除都检查外部修改。新 manifest 在运行时核验通过后提交；中断后使用相同 inventory/bundle 重跑。恢复阶段保留新旧证据，不能通过删除事务文件或 `--restart` 绕过归属检查。

`apply_network_immediately=false` 不改动管理/RDMA 运行时名称，保留 `awaiting-runtime`。随后可以将该字段改成 true 来继续在线验证；它不参与网络事务目标 fingerprint。其他目标或已进入切换阶段的物理绑定发生变化时，需要先完成原事务。

## 拓扑切换

- 地址/prefix/网关：按具体对象退出旧配置；共享 source/oif rule、连接路由不因网关变化而删除。
- bond↔单口：退出受管 profile、精确删除旧地址、解除旧成员关系、删除退役受管 bond，再激活新配置。检查外部成员和额外地址，避免随 bond 删除其他配置。
- RDMA A→B：按 MAC 重新定位现场设备；旧口占用目标名时使用确定性的 `ei-old…` 名称释放；中断后的 `ei-tmp…` 通过 MAC 重新定位。逻辑名字不作为物理身份依据。
- NetworkManager：生成稳定 UUID，按 UUID 激活/删除受管 profile，bond 成员显式激活。
- legacy network：旧文件仍匹配摘要时先 ifdown；新配置执行 ifup，错误不再被吞掉。
- netplan/networkd：预校验后 generate/apply，应用后检查内核对象，回收仍残留的受管地址/关联/退役 bond。
- 路由脚本与清理使用共享锁。自动重放在在线事务期间暂停，主进程显式重放；规则有额外选择条件或路由属性有歧义时阻断删除。

## SSH 执行

SSH 会话中在线运行 network 时，NIC Review 完成后自动交给目标机 `systemd-run` 的 `envinit-network-apply` 单元执行，不新增用户参数。worker 获取同一 apply 锁，独立于等待中的 SSH 客户端。

- 日志：`/var/lib/envinit/network-worker.log`
- 结果：`/var/lib/envinit/network-worker-result.json`
- systemd 状态：`systemctl status envinit-network-apply`

断线后通过新管理地址或控制台读取结果并重跑原命令。独立 worker 只覆盖 network 阶段；其后的交互 stages 仍由原 apply 流程执行。后台工作不能代替带外恢复路径。SSH 场景缺少 systemd-run 时会在切换前报错。

## sysctl 与后续阶段

sysctl 记录受管 key 和可获取的原值，旧 key 退出时恢复原配置行；来源不明的历史参数不会根据名字猜测删除。外部修改过的受管参数会阻断。运行值逐项读取，仅对不符合目标的项执行 `sysctl -w` 并复查，不再依赖文件变更后才 `sysctl -p`。

旧物理口仍存在时按 MAC 定位（包括接口改名），有明确原值且当前值仍等于 envinit 写入值才恢复；已经由另一个 RDMA 角色接管的 key 不恢复。无原值证据时只清理持久配置并记录日志。

post-boot 服务另外记录脚本和 MAC/PCI 指纹，因此同名换卡也能触发重新调优。现有 mlxconfig 继续验证 MST/NET/PCI；kernel 继续按固定参数检查。

## 历史部署和验收边界

v1 manifest 可基于摘要完全一致的旧 envinit netplan/ifcfg 和保存的 NIC bindings 迁移；仅解析旧生成格式，不把任意现场配置自动纳管。旧 NetworkManager profile 未记录 UUID、旧绑定缺失、文件有外部修改等情况会阻断，需要核实归属后迁移。

已加入模拟回归：事务中断、同路径外部修改、延迟应用、A→B 名称释放、临时名恢复、bond→单口及重复退出、旧 netplan 迁移、三后端失败后相同文件重试、共享路由保留、sysctl 旧项恢复和外部修改保护。

仍需现场验收：三种后端的单口→bond/成员替换/槽位缩容，真实连接失联后 worker 完成，服务重载并发，Linux 网络命名和重启持久化，换卡后的驱动/MST/调优能力。当前测试通过不代表这些场景已完成实机验收。
