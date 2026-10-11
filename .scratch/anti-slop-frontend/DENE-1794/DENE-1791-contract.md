## 背景

Kun 拍板（2026-10-11，经 Grok 转达）：Grok ↔ Multica 的互通**整体照郭宇 Yobiru 的思路做**：
- 双方可以互相 @；
- 有消息就直接叫醒对方，不靠定时巡检；
- 不经中转、直接连；
- 收件方决定谁能叫醒自己；
- 文件只传地址和 sha256，由收件方自己拉。

本票合并了 Kun 原先开的「主动叫醒 Grok Bot」需求（原文附在最后）和 DENE-1790 的研究结论（设计仓 `virtual-bot-connect/research/yobiru.md`）。

## 设计（照 Yobiru 映射到我们的系统）

| Yobiru 做法 | 在我们这里怎么落地 |
|---|---|
| 收件方常驻进程，消息一到就叫醒 | **Grok 方向**：Multica 服务端（魔改 `jeff-kunkun/multica`）在事件发生时直接 POST Grok routine 的 Webhook（Grok 已建好接收端「Multica 通知」），不经 Mac、不靠轮询。**Multica 方向**：Grok 用 CLI 直连（DENE-1786），@ 智能体即叫醒，已具备 |
| `机器/agent` 点名 | Grok Bot 在 Multica 里**可被 @**：聊天和任务评论的 @ 补全里出现外部 Bot（DENE-1788 登记的那些），@ 谁只叫醒谁（多个 Bot 各自配叫醒地址） |
| 收件方决定叫醒规则，只降不升 | 每个外部 Bot 有自己的**叫醒规则**：哪些事件能叫醒（mention / started / done / blocked / needs_kun / task / chat）、每小时上限（默认 6）、夜间静音（默认 23:00–08:00 Asia/Shanghai，可关）。规则归收件方：Bot 能用自己的钥匙经 CLI 读写，Kun 也能在设置页改。超出上限或在静音时段的事件**不丢**，降级为"等兜底巡检时一起看" |
| 消息只带地址 + sha256，收件方自己拉 | Webhook 请求体只放**事件摘要**：事件 id、类型、票或聊天、作者、时间、一两句摘要、链接，正文不放进去。Grok 收到后用 CLI 自己拉完整内容。附件以 `{地址, 大小, sha256}` 列出；Multica 附件补存 sha256，CLI 下载时校验，不一致就删掉并报错。Grok 上传的文件同样带 sha256 |
| 送达 ≠ 叫醒 ≠ 完成；不确定不重发 | 每次推送都记录状态：已发送 / 已被 Grok 接收（HTTP 200）/ 失败（含原因）。按事件 id 去重，失败按退避重试有限几次；仍失败就留在 Bot 的"待看"列表，等兜底巡检。设置页能看到最近的推送记录 |
| 无中心 | 叫醒链路不经 Mac 信箱桥接。Multica 仍然负责记账（票、聊天、审计），这是 Kun 要在手机上看全局的基础，不改 |

钥匙存放：Webhook 的 URL 和 key 由服务端加密保存，只能写入，不回显；不进票、仓库、日志或评论。（原需求建议存在 Mac 侧，但推送由服务端发起，Mac 睡眠时也要能推，所以改为存服务端。）

信箱（DENE-1765 / 1786）退到只负责"派执行任务到云电脑"；派出执行任务时也走同一个 webhook 叫醒。Grok 的 30 分钟巡检保留作兜底，推送稳定一周后再降频。

## 验收（真 Grok + 真 Multica）

1. 在聊天和任务评论里 @Grok Bot，Grok 1 分钟内被叫醒，能读到对应内容并回复；记录从事件发生到 Grok 开跑的时延。
2. Grok 负责或关注的票出现开工、完成、阻塞、需要 Kun 拍板、派给 Grok 的执行任务这几类事件时，各推送一次并被接收。
3. 叫醒规则生效：超过每小时上限、在静音时段、或关闭某类事件后，事件不叫醒 Grok，但下次巡检能看到；Grok 自己用 CLI 改规则后立即生效。
4. 同一事件重复触发只推送一次；把 webhook 地址故意配错，能看到失败记录和重试，现有流程不受影响。
5. 附件：Multica → Grok、Grok → Multica 各传一个文件，收件方核对 sha256；故意改坏一份，确认被拒收。
6. 验证 webhook 能否唤醒已暂停的 Grok 云电脑，并记录每次调用是否计费、计多少。
7. 设置页（外部 Bot）能配叫醒地址和规则、能看推送记录；桌面和窄屏各截一组图。前端按 DENE-1788 的约定交给孙悟空写。
8. 更新 Grok 长期指令和 `virtual-bot-connect` 文档；按 Kun 的汇报规则用人话汇报，最后单列「要你做的」。

Webhook 的 URL 和 key：先让 Grok 在自己的 routine「Multica 通知」里取出来，经 CLI 写进它自己那条 Bot 设置；取不到再请 Kun 一次性复制。

---

<details><summary>Kun 原需求（Grok 转达）</summary>

📮 **来自 Grok Bot**（消息 `grok-1791698244186991498-ca0c`，经信箱桥接转达；以下为 Bot 原话，未经核验）

> Kun 原话：现在 Grok 只能定期巡检去看 Multica 做没做完、进度怎样、聊天情况。开个魔改票：无论是聊天还是任务，弄完以后都可以 @ grok bot；你们两个可以主动激活就主动激活，无论做完、做前还是什么，主动激活肯定比定期巡检更方便、更直接。
> 
> 需求：
> 1. 在 Multica 里把 Grok Bot 当作可 @ 的成员：聊天或任务评论里 @grok bot、任务开始/完成/阻塞/需要 Kun 拍板、交给 Grok 的 task，都立即推送给 Grok。
> 2. 推送方式：HTTP POST 到 Grok Bot 的 routine webhook（Grok 已建好接收端「Multica 通知」）。Webhook URL 和 key 由 Kun 提供；key 只存在 Mac 侧的安全位置（如 Keychain 或桥接配置，0600），不要写进票或仓库。请求头按 Grok webhook 的 Authorization 要求带 key。
> 3. payload 建议：issue、事件类型（mention/started/done/blocked/needs_kun/task/chat）、作者、时间、一两句摘要、链接；详细内容仍可同时写进信箱 inbox，Grok 收到推送后会读信箱。
> 4. 去重、失败重试（退避），推送失败时回退到信箱，不影响现有流程。
> 5. Grok 侧的定时巡检保留作兜底，推送稳定后再降频。
> 6. 和 DENE-1786 / 1788 及 Yobiru 研究（DENE-1790）里「收件方决定谁能叫醒、可 @ 具体 bot」的思路对齐；完成后按 Kun 的汇报规则用人话汇报，最后单列「要你做的」。

</details>

!file[grok-webhook-instructions.md](https://ai.ferryway.cc/api/attachments/01a1298e-b440-736b-b6f8-61d8ddbd8b53/download)

## 2026-10-11 design.md 源码核对后的集成约束
已核对提交9a8df7897...bae90bca9及附件design.md。DENE-1792保留原骨架续做：发件人规则、抑制自身事件和可信因果跳数、默认不继承主人的全部关注、回执和可靠待看确认、提及解析、payload来源字段与bot mentions。DENE-1793同步界面。DENE-1794已提前进入开发准备，提供可信Bot来源/原话隔离及删除后的历史保留；可叠加#626开发，正式上线仍依赖#626与来源能力部署。
默认总上限仍6/小时、静音仍23:00–08:00 Asia/Shanghai。不采纳默认@20次、默认静音豁免、uncertain自动重投或未计费实测前引入30秒合并窗口。回执的HTTP接收、Grok开跑、结果独立记录；待看增量必须覆盖旧事件稍后失败及乱序提交，不能单靠创建sequence游标。源码审查不是真Grok验收。
正文“Grok直连已具备”仅指CLI/API已有路径；实际Bot钥匙种类、routine环境注入、安装与重启可用性仍由DENE-1786实测。DENE-1786现行正文已明确允许指定受保护配置并取代“不保存token”的旧规则；并不强迫把平台密钥环境变量另落盘。沿用受保护注入方式，验证routine/重启持续可用，勿从Bot转述推导新的授权。

## DENE-1794 来源契约已交付候选（PR #627）
候选提交 a68a393db1695e2ccb73ecf6c370f4232566749c，分支 agent/agent/dene-1794；PR https://github.com/jeff-kunkun/multica/pull/627。#626 已合并，当前候选已 merge origin/kun。迁移使用 662_bot_write_provenance，明确保留 DENE-1792 的 660/661，不改其 worker/trigger 写入面。契约单源 server/docs/bot-write-provenance.md。
- 评论/聊天/活动保存并返回 via_bot_id、via_bot_name；任务运行在 attribution 下返回同名字段，直接 Bot 来源为 source=via_bot，委派保留正常 source 并携带 Bot 快照。issue 创建快照支撑后续后台唤醒，task token 恢复快照到回复。
- status/assignment 当前操作者：CreateIssue/CreateIssueWithOrigin/UpdateIssue/UpdateIssueStatus 在同条 SQL 的 MATERIALIZED CTE 设置事务局部 multica.actor_type、multica.actor_id、multica.via_bot_id、multica.via_bot_name。trigger 必须在该语句内用 NULLIF(current_setting(key,true),'') 读取并持久化到 outbox，不得用 issue.creator 补当前操作者。comment/chat trigger 读取 NEW.via_bot_id/name。来源只从真实鉴权 context 建立，客户端同名头/字段无效。
- DENE-1792 继续负责 outbox 源字段/触发器消费、source_task_id 因果链和四跳限制。事件总线 events.Event 同时有 ViaBotID/Name 与实际 ActorType/ID，但不能替代 DB trigger 的同事务来源。现有 660 trigger 尚未接入不算闭环。
- DeleteStoppedBotAccessToken 已改墓碑：revoked=true、deleted_at 非空，保留凭据行，避免 CASCADE 丢历史发件箱；Bot 目录及 worker 收件人查询请同时排除 revoked/deleted_at。个人写入不带 Bot 来源，Bot 留言不得算原话。
- 部署先满足 #626 + 本票 662 + 1792 触发器消费者，再启用推送；滚动期间不能把 Bot 写入/删除交给旧副本。真实 PostgreSQL/Redis、原话拒绝、后台来源和活动触发器探针通过；集成 outbox/真实 Grok、完整聊天视觉验收仍未宣称通过。
