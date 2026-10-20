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

机器掉线告警（`multica runtime alerts` / `runtime outages`）已在 DENE-2315 删除；运行时在线状态看运行时页或 `multica runtime list --output json`。
