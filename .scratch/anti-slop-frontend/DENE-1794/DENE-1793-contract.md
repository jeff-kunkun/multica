Kun 在父票 DENE-1791 明确点名「前端按 DENE-1788 的约定交给孙悟空写」。你负责前端，先完整读父票、复用你在 DENE-1788 / PR #626 的外部 Bot 设置页。新增每Bot叫醒地址和key（只写不回显，已有配置只显示已配置）、事件开关 mention/started/done/blocked/needs_kun/task/chat、每小时默认6、静音默认23:00–08:00 Asia/Shanghai可关闭、最近推送状态/失败原因/待看记录；聊天和任务评论 @ 补全显示外部Bot并写 mention://bot/<id>。服务端/CLI由兄弟票完成，前端只写 packages/、必要的apps路由与截图证据，不碰server。初始契约 GET/PUT /api/workspaces/{id}/bots/{botId}/wake；GET 同路径 /events 与 /pending；响应不含 URL/key，仅 webhook_configured、events、hourly_limit、quiet_hours{enabled,start,end,timezone}。服务端同时提供受访问权限约束的Bot mention目录，具体字段以兄弟票最终契约为准，主动核对后联调。加载 anti-slop-frontend 和 jev-ego；桌面1280、768、375/390截图，实际表单保存、权限、失败态、秘密不回显验证；不能用mock截图冒充真接口。说明原生手机是否有同入口。按子票交付线提交由父票统一PR，不单开PR（若无delivery line先核实）。交付可审查diff、实际检查和未测项。

## 父票已明确的交付线（2026-10-11）
已登记canonical并推送：jeff-kunkun/multica 的 agent/agent/73f2403f736d，基准 9a8df7897（含PR #626）。执行 multica repo checkout https://github.com/jeff-kunkun/multica.git 后，在daemon给你的任务分支 git fetch origin agent/agent/73f2403f736d 并 git merge --no-edit FETCH_HEAD，再实现。保留自己的任务分支，禁止reset/rebase。若本票仍没有delivery_line字段，也不要停：提交并push自己的任务分支，交回提交SHA和分支名，由父票显式merge进入canonical；不另开PR。此前「没有delivery line则报告后再决定」现已由本段决定取代，不再是阻塞条件。兄弟票：DENE-1792服务端/CLI，DENE-1793前端；前端已handoff孙悟空，避免重复写入。

## 父票核对 design.md 后的前端增量（2026-10-11）
DENE-1792 正续做契约，保留已有前端工作。最终补收件方「谁能叫醒」规则、是否继承主人关注（默认否）、独立显示HTTP接收/已开跑/有结果及各时间和事件到开跑时延、待看处理确认。未回执不能显示已开跑或已完成。默认总预算仍6/小时，时区仍Asia/Shanghai、23:00–08:00；不采用附件的默认20次@预算或自动静音豁免。先核对服务端最终契约再接，不做第二份客户端规则。Grok的增量扫描不能漏旧事件延迟失败，页面保留对应状态/原因。与DENE-1794协调「经Bot」署名，不重复修改其负责面；孙悟空按既定分工负责前端。附件 design.md 位于父票评论01a129bf-10d1-70d0-9da0-d618a2d10857。

## DENE-1792 候选契约同步（1cd42aac6）
服务端候选已推送 agent/agent/4cf0013d1239，父票集成，不是可上线完成版。server/docs/bot-wake-contract.md 为详细契约。新增 wake_from={member,agent,bot}，每项 {mode:all|none|list,ids:[]}，Bot 默认 none，member/agent 默认 all，PUT 整个 map；未新增继承主人关注开关，默认不继承。事件新增 woken_at/result_at/result_url/acknowledged_at，运输 status/received_at 独立不变。POST /wake/events/{eventId}/woken body={}；POST /result body={result_url:事件源规范API URL}；POST /wake/pending/ack body={event_ids:[1..100个]}，批量事务确认。pending 仅返回未确认项，before 为本次分页游标，扫完必须重新从第一页开始，不能记 sequence 高水位。未知操作者留待看。来源识别和持久四跳链路仍等 DENE-1794 集成，web_url/author.name/hop 尚未交付；不要显示为已验证。

## DENE-1794 来源契约已交付候选（PR #627）
候选提交 a68a393db1695e2ccb73ecf6c370f4232566749c，分支 agent/agent/dene-1794；PR https://github.com/jeff-kunkun/multica/pull/627。#626 已合并，当前候选已 merge origin/kun。迁移使用 662_bot_write_provenance，明确保留 DENE-1792 的 660/661，不改其 worker/trigger 写入面。契约单源 server/docs/bot-write-provenance.md。
- 评论/聊天/活动保存并返回 via_bot_id、via_bot_name；任务运行在 attribution 下返回同名字段，直接 Bot 来源为 source=via_bot，委派保留正常 source 并携带 Bot 快照。issue 创建快照支撑后续后台唤醒，task token 恢复快照到回复。
- status/assignment 当前操作者：CreateIssue/CreateIssueWithOrigin/UpdateIssue/UpdateIssueStatus 在同条 SQL 的 MATERIALIZED CTE 设置事务局部 multica.actor_type、multica.actor_id、multica.via_bot_id、multica.via_bot_name。trigger 必须在该语句内用 NULLIF(current_setting(key,true),'') 读取并持久化到 outbox，不得用 issue.creator 补当前操作者。comment/chat trigger 读取 NEW.via_bot_id/name。来源只从真实鉴权 context 建立，客户端同名头/字段无效。
- DENE-1792 继续负责 outbox 源字段/触发器消费、source_task_id 因果链和四跳限制。事件总线 events.Event 同时有 ViaBotID/Name 与实际 ActorType/ID，但不能替代 DB trigger 的同事务来源。现有 660 trigger 尚未接入不算闭环。
- DeleteStoppedBotAccessToken 已改墓碑：revoked=true、deleted_at 非空，保留凭据行，避免 CASCADE 丢历史发件箱；Bot 目录及 worker 收件人查询请同时排除 revoked/deleted_at。个人写入不带 Bot 来源，Bot 留言不得算原话。
- 部署先满足 #626 + 本票 662 + 1792 触发器消费者，再启用推送；滚动期间不能把 Bot 写入/删除交给旧副本。真实 PostgreSQL/Redis、原话拒绝、后台来源和活动触发器探针通过；集成 outbox/真实 Grok、完整聊天视觉验收仍未宣称通过。
