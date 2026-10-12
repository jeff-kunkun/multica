# 要人决定的事：推给本人的 Bot（DENE-1933）

连了 Bot 的人，所有要他决定的事都由服务端主动推给他的 Bot，由 Bot 转达给本人；没连 Bot 的人保持原样（站内 @ 和收件箱）。

- 设置：账号设置里「把要我决定的事交给我的 Bot」，或 `multica bot bind/status/unbind/verify`。需要 Bot 回执 `binding.verify` 才算连通。
- 触发：@ 成员、`issue summon`/needs_kun、阻塞等人、待验收等人、`issue ask-human`、`chat ask-human`。统一走 `RouteHumanNeeded`，一个来源一条事件（去重键见 `service.HumanMentionKey`）。
- 送达：已发 → 已收 → 已转达 → 已回答，失败按退避重试；3 分钟没收到重发，收到后 5 分钟没转达则升级、站内回落并提醒 owner，之后每 30 分钟一次。
- 回答：票上的提问由 Bot 以本人身份评论（`multica bot wake answered`）；选择题卡片用 `--answers` 填卡，平台留一条「Bot 代表本人」的评论、解除阻塞并只叫醒提问的智能体一次；聊天里的选择题同样是共享卡片（收件箱、`multica ask`、Bot 都能答），谁答都把所选项写回聊天并只叫醒提问的智能体一次，只有聊天主人能答，在聊天里直接打字回复会关掉卡片；自由文本的聊天提问仍是一条聊天消息。已连 Bot 的人的卡片，无论在 Bot、站内还是 CLI 回答（含镜像模式、通道断开时），都会叫醒提问的智能体一次。
- 卡片：`needs_you` 卡片和带 2–6 个选项的 `ask-human` 都是同一张 `agent_ask`（事件带 `ask_id`），`/api/asks` 与 `multica ask` 看到的是同一张。
- 通道坏了：状态变坏时只通知本人一次，所有还没送出的调用回落站内，Bot 停用后也会继续回落。
- 夜间：服务端始终立即推并带 `urgency`；Bot 自己决定夜间（默认 23:00–08:00 America/Chicago）是否暂缓 normal。
- 契约与回执细节：[Bot 唤醒 API 契约](../../server/docs/bot-wake-contract.md#human-needed-events-dene-1933)、[外部 Bot 说明](../../server/internal/service/builtin_skills/multica-platform/references/external-bots.md#decisions-that-need-a-person-dene-1933)。
- 智能体侧：要人决定用 `multica issue ask-human`，不要只 @；见 `multica-platform` 的 mentions 与 routing 说明。
