# 要人决定的事：推给本人的 Bot（DENE-1933）

连了 Bot 的人，所有要他决定的事都由服务端主动推给他的 Bot，由 Bot 转达给本人；没连 Bot 的人保持原样（站内 @ 和收件箱）。

- 设置：账号设置里「把要我决定的事交给我的 Bot」，或 `multica bot bind/status/unbind/verify`。需要 Bot 回执 `binding.verify` 才算连通。
- 触发：@ 成员、`issue summon`/needs_kun、阻塞等人、待验收等人、`issue ask-human`、`chat ask-human`。统一走 `RouteHumanNeeded`，一个来源一条事件（去重键见 `service.HumanMentionKey`）。
- 送达：已发 → 已收 → 已转达 → 已回答，失败按退避重试；3 分钟没收到重发，收到后 5 分钟没转达则升级、站内回落并提醒 owner，之后每 30 分钟一次。
- 回答：Bot 以本人身份评论到票上（`multica bot wake answered`），平台叫醒提问的智能体并解除阻塞。
- 夜间：服务端始终立即推并带 `urgency`；Bot 自己决定夜间（默认 23:00–08:00 America/Chicago）是否暂缓 normal。
- 契约与回执细节：[Bot 唤醒 API 契约](../../server/docs/bot-wake-contract.md#human-needed-events-dene-1933)、[外部 Bot 说明](../../server/internal/service/builtin_skills/multica-platform/references/external-bots.md#decisions-that-need-a-person-dene-1933)。
- 智能体侧：要人决定用 `multica issue ask-human`，不要只 @；见 `multica-platform` 的 mentions 与 routing 说明。

## 全量动态流（DENE-1935）

同一条 Bot 通道上，本人可以再打开「动态流」：他名下和关注的票、聊天里的所有评论、聊天消息、状态变化都即时推给 Bot，每条带 `importance` / `categories` 标签，由 Bot 自己筛选；要人决定的事始终最优先。

- 设置：账号设置里「把所有动态推给我的 Bot」，或 `multica bot bind --activity owned,followed|off`。按人开启，默认关；没连 Bot 的人不受影响。
- 整个载荷发出前统一去掉密钥、token、密码（含摘要、标题、选项，补拉列表同理），短密码和带空格的密码也一样；不推附件和整段原文，只推摘要。评论被修改会再推一次。自己的动作、自己 Bot 写的内容、已经按「要人决定」推过的评论不再重复推。
- 发出前和补拉时都按当前状态重新判断：看不到这张票、取消关注、换了负责人、范围改窄、绑定被撤销，都不再发。
- 送达与 `human_needed` 共用外发队列，但没人追：HTTP 200 即送达，不重发、不升级。72 小时过期，14 天清理；低重要度积压超过 2000 条就不再入队。
- 第一步只做动态流；PR、运行、项目、设置、成员与智能体，以及 Bot 以主人权限代办，是第二步。
- 契约：[Bot 动态流契约](../../server/docs/bot-activity-contract.md)。
