# 运行时掉线与高时效智能体

接码、验证码、上线窗口这类任务需要随时有人接住。不要只绑定会睡眠的笔记本；防睡眠能减少中断，但断网、关机和外出仍会中断执行。

## 当前改绑候选（2026-10-11）

通过 `multica runtime list --output json` 核对 Kun 本人名下的在线运行时：

| 目标 | 适合的任务 | 理由与限制 |
| --- | --- | --- |
| Grok (ai-multica) | 接码、验证码、上线提醒等能由 Grok 执行的高时效任务 | 当前在线，主人是 Kun，离开 Mac 仍有独立执行目标；当前只注册了 Grok。迁移 Codex / Claude 等席位前要先补齐对应 CLI、登录和额度。在线快照不代表已验证全天在线。 |
| MacBook-Pro.local 上的运行时 | 依赖本机文件、桌面操作或已有本机登录态的任务 | Kun 本人拥有，CLI 种类齐全；睡眠、合盖或断网会让整机停摆，适合本机工作，不宜成为高时效任务的唯一目标。 |

其他成员名下的机器不列入 Kun 的改绑候选。这次没有调整任何智能体绑定。
推荐先选出接码、验证码、上线类智能体，再由 Kun 决定哪些改到 ai-multica，哪些需要先在常在线机器补齐原提供商环境。保留需要本机桌面的任务在 Mac。

## Mac 防睡眠

先查看并保存原值：`pmset -g custom`。临时需要保持供电运行时，开一个终端执行 `caffeinate -i -s`，完成后按 Ctrl-C 结束。`-s` 的系统防睡眠只在接电源时生效；合盖另有系统限制，不把它当作服务器在线保证（本机 `man caffeinate`）。

持久设置可用 `sudo pmset -c sleep 0`，仅修改接电源时的空闲睡眠。需要恢复时，用 `sudo pmset -c sleep <原先记录的分钟数>`。不要猜原值，也不要把电池模式一并禁用（本机 `man pmset`）。本票只提供做法，没有执行这些设置。

图形入口：系统设置中打开“接入电源且显示器关闭时防止自动进入睡眠”；位置随系统版本变化，见 [Apple 睡眠设置说明](https://support.apple.com/en-euro/guide/mac-help/mchle41a6ccd/27/mac/27)。也可以使用 [Mac App Store 的 Amphetamine](https://apps.apple.com/us/app/amphetamine/id937984704) 设置接电源或某个进程运行期间保持唤醒。上线窗口内保持电源和网络连接，并实测合盖后的运行时状态。

## 告警规则与查询

运行时页的“机器掉线告警”控制当前工作区里属于自己的机器。手机网页同一入口，原生 App 在设置 → 通知里也有此开关。CLI：

```bash
multica runtime alerts --output json
multica runtime alerts on
multica runtime outages --output json
multica overview --output json
```

机器按主人及设备名聚合；同一物理主机的多个守护进程也合并，没有设备名时用守护进程标识。曾被监测为 online 的运行时进入 offline 后开始计时，超过 3 分钟才提醒；同机多个运行时合并为一条，短暂重启不提醒。受影响的运行时全部重新 online 后结束掉线记录，有过掉线提醒才发恢复通知。首次启动只建立基线，不给历史离线注册补发告警。监测每 30 秒一次，告警窗口从确认 offline 开始，心跳失联到确认 offline 还需要现有存活检查的时间。

记录、收件箱和 Bot 队列在一个数据库事务里写入；重复扫描和服务端重启不会重复发提醒。关闭开关会同时停止新的收件箱和 Bot 告警，掉线历史仍可查询。Grok Bot 接收 `runtime.offline` / `runtime.online` 并转发 Telegram；现有 Bot 的事件订阅、静默时段、限额和回执机制继续生效。被挡住的推送可从 `multica bot wake pending <bot-id>` 查看，不自动重放不确定投递。

上线后要在独立测试守护进程上做一次真实演练：确认在线基线，停掉测试进程，等待 offline 后超过 3 分钟，检查一条机器提醒和 Telegram 推送；再启动该测试进程，检查一条恢复提醒。不要停运行当前任务的守护进程，也不要用改绑制造故障。
