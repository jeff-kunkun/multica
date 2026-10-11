落实父票 DENE-1791 的服务端与 CLI 写入面，完整阅读父票。基于 DENE-1788 的 Bot PAT 身份（PR #626），不要另建身份体系。负责：持久化加密 webhook URL/key（只写不回显）；Bot 自己与管理员读写规则，默认每小时6次、23:00–08:00 Asia/Shanghai，禁用/限流/静音转待看；持久事件发件箱、事件ID去重、有限退避、超时不确定不盲目重投、收件状态/待看查询；聊天与评论 mention://bot/<id> 及 started/done/blocked/needs_kun/task/chat 事件接线；附件服务端计算保存 sha256、上传校验、CLI 下载校验失败删除；CLI bot wake get/set/events/pending，密钥只走文件或stdin；更新内置平台技能命令说明。和父票前端兄弟票共享契约：GET/PUT /api/workspaces/{id}/bots/{botId}/wake，GET 同路径 /events 与 /pending；响应不含 URL/key，仅 webhook_configured、events、hourly_limit、quiet_hours{enabled,start,end,timezone}；事件列表须分页且只返回已授权内容。补 Bot mention 目录端点供有访问权限成员补全。必要的最终字段差异及时在交付契约说明清楚。运行真实本地HTTP事件推送、限流静音去重错误路径、附件好/坏样本；保留命令和原始输出，不能将mock当真Grok。仅写 server/ 与服务端契约文档，不写 packages/ 前端。遵循仓库 migration/sqlc 和授权边界；不泄漏凭据、不降低现有权限。按子票交付线提交，父票集成统一PR；没有delivery line则报告后再决定。验收交回具体命令、原始证据、所有未测项。

## 父票已明确的交付线（2026-10-11）
已登记canonical并推送：jeff-kunkun/multica 的 agent/agent/73f2403f736d，基准 9a8df7897（含PR #626）。执行 multica repo checkout https://github.com/jeff-kunkun/multica.git 后，在daemon给你的任务分支 git fetch origin agent/agent/73f2403f736d 并 git merge --no-edit FETCH_HEAD，再实现。保留自己的任务分支，禁止reset/rebase。若本票仍没有delivery_line字段，也不要停：提交并push自己的任务分支，交回提交SHA和分支名，由父票显式merge进入canonical；不另开PR。此前「没有delivery line则报告后再决定」现已由本段决定取代，不再是阻塞条件。兄弟票：DENE-1792服务端/CLI，DENE-1793前端；前端已handoff孙悟空，避免重复写入。

## DENE-1791 对 Grok design.md 的源码核对与续做（2026-10-11）
依据 9a8df7897...bae90bca9 的源码检查；附件在父票触发评论 01a129bf-10d1-70d0-9da0-d618a2d10857，attachment 01a129bf-0fb9-7369-9610-1c6b7199c80d。保留 bae90bc 已交付骨架及原验证，不推翻。继续在本票原任务分支补齐下列闭环，父票集成，勿新开PR。
1. 收件方 wake_from：成员/agent/Bot 按全部、无、名单控制；Bot 默认无。鉴权、读源权限不变；拒绝推送进待看并有原因。作者真实身份依赖 DENE-1794，不得相信客户端自报头或仅凭继承主人的ID。状态变更目前author_type=system/author_id空也必须取到真实触发源，不能从issue.creator推断操作者。
2. 自己写入不唤醒自己；跨Bot/agent链路使用服务端持久因果关联/跳数限制（4为本次实现上限），到限停止自动唤醒并可见。不把客户端可省略/重置的hop作为唯一保护；传播到agent run及其回复。DENE-1794负责来源字段；本票负责事件/worker/CLI接线及链路验证。
3. 不默认继承主人全部指派/订阅，保留明确Bot关注/提及及真正指给Bot的任务。@/回复优先或预留额度，但总默认上限仍6/小时，不能静默新增20次预算，静音仍23:00–08:00 Asia/Shanghai且可改。若预算细节影响契约先给一个不增加总量的具体实现方案。避免以纯类型task就假定是云端执行任务。
4. 增加鉴权且幂等的woken/result回执与CLI；服务端记接收时间，保留HTTP received与实际开跑/结果独立。result链接校验归属/可见性；不让另一Bot确认别人的事件；重复/乱序不能回退状态。待看处理确认与执行结果回执分开。
5. 待看增加批量确认、增量读取。不能只sequence > cursor筛选：序号小的queued事件可能在游标前进后才变failed/pending/uncertain，会漏读。用状态变化游标或稳定未确认扫描，定义分页、确认事务与重启恢复；同时bigserial是全表序号且不是提交顺序，不能承诺每Bot连续或按提交严格有序。用乱序提交、旧事件延迟失败、权限过滤空页、重启场景验证不漏。
6. 排除Markdown围栏/缩进代码、行内代码、引用中的示例@；触发器与服务端解析契约一致，避免双套正则不一致。增加bot mentions CLI，支持有权限的source过滤。
7. payload兼容增加source/parent标识（Grok原处回复所必需）、web_url、author.name/来源、sequence/安全增量游标、ack地址与hop；保留现有url字段，不用改名破坏现有接收方。不带正文、不泄漏不可见作者或源资源。
8. 生产部署明确配置独立MULTICA_BOT_WAKE_SECRET_KEY并验证跨重启/副本稳定；维持现有固定错误原因不泄密，增加轮换/解密失败处理说明。不能只改强制启动就使现存密文失效。
明确不采纳：uncertain自动重投一次（父票已要求不确定不重发，且业务去重不能证明routine计费/启动去重）；默认@20次；默认静音例外；无费用证据的30秒合并窗口。继续保持uncertain待看。migration用make migration-new分配，不照抄附件661。
更新server/docs/bot-wake-contract.md并同步DENE-1793字段；少量核心测试+真实本地HTTP/DB行为验证；真Grok和费用依旧父票验收，不能记为已通过。

## DENE-1794 来源契约已交付候选（PR #627）
候选提交 a68a393db1695e2ccb73ecf6c370f4232566749c，分支 agent/agent/dene-1794；PR https://github.com/jeff-kunkun/multica/pull/627。#626 已合并，当前候选已 merge origin/kun。迁移使用 662_bot_write_provenance，明确保留 DENE-1792 的 660/661，不改其 worker/trigger 写入面。契约单源 server/docs/bot-write-provenance.md。
- 评论/聊天/活动保存并返回 via_bot_id、via_bot_name；任务运行在 attribution 下返回同名字段，直接 Bot 来源为 source=via_bot，委派保留正常 source 并携带 Bot 快照。issue 创建快照支撑后续后台唤醒，task token 恢复快照到回复。
- status/assignment 当前操作者：CreateIssue/CreateIssueWithOrigin/UpdateIssue/UpdateIssueStatus 在同条 SQL 的 MATERIALIZED CTE 设置事务局部 multica.actor_type、multica.actor_id、multica.via_bot_id、multica.via_bot_name。trigger 必须在该语句内用 NULLIF(current_setting(key,true),'') 读取并持久化到 outbox，不得用 issue.creator 补当前操作者。comment/chat trigger 读取 NEW.via_bot_id/name。来源只从真实鉴权 context 建立，客户端同名头/字段无效。
- DENE-1792 继续负责 outbox 源字段/触发器消费、source_task_id 因果链和四跳限制。事件总线 events.Event 同时有 ViaBotID/Name 与实际 ActorType/ID，但不能替代 DB trigger 的同事务来源。现有 660 trigger 尚未接入不算闭环。
- DeleteStoppedBotAccessToken 已改墓碑：revoked=true、deleted_at 非空，保留凭据行，避免 CASCADE 丢历史发件箱；Bot 目录及 worker 收件人查询请同时排除 revoked/deleted_at。个人写入不带 Bot 来源，Bot 留言不得算原话。
- 部署先满足 #626 + 本票 662 + 1792 触发器消费者，再启用推送；滚动期间不能把 Bot 写入/删除交给旧副本。真实 PostgreSQL/Redis、原话拒绝、后台来源和活动触发器探针通过；集成 outbox/真实 Grok、完整聊天视觉验收仍未宣称通过。
