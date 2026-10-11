DENE-1794 终审：hold，交回执行席修正；PR #627 未合并。

## Standards
未发现阻断性的包边界或状态管理违规。实现复用共享 Web/Desktop 视图，并同步了原生手机端。Standards：0 项。

## Spec
F1（阻断，P1）：实时聊天缓存丢失可信 Bot 来源。
位置：`packages/core/realtime/use-realtime-sync.ts:220`、`packages/core/types/events.ts:437`；关联本次新增 `packages/views/chat/components/chat-message-list.tsx:614` 和服务端 `publishChatWithSource`。
服务端事件已带 `via_bot_id` / `via_bot_name`，但 `applyChatMessageToCache` 重新构造用户消息时未复制它们，事件类型也未声明。实际运行该函数：输入 Bot event = `bot-pat-id / Grok`，flat 和 paged cache 均只有成员身份、没有 Bot 字段。`ViaBot` 依赖该名称，因此另一个打开的聊天窗口在实时写入时会把 Bot 留言显示成成员本人，至少持续到补充请求成功。该函数已有注释说明补充 refetch 可被 quick_actions 取消，且缓存 staleTime 为 Infinity，不能把重取当可靠保护。
反证核对：API schema、服务端事件和历史数据库均有字段；消息合并函数只填传入字段；这里的 cache 转换确实丢了字段。原有 67 项 core 测试和 14 项 view 测试通过，新增忠实来源回归探针失败（exit 1），已附源文件和原始输出。
关闭条件：扩展 `ChatMessageEventPayload`，把两字段传入两套缓存，覆盖 Bot 的 second-window/取消refetch/重放路径及普通成员对照。恢复后实页确认已打开的聊天即时显示「经 Grok」。Spec：1 项，最高 P1。

其他交付前置：
- `git merge-tree --write-tree HEAD origin/kun` 已证实 `server/cmd/multica/cmd_chat.go` 与当前主线冲突；GitHub PR 同时报告 `CONFLICTING / DIRTY`。在自己的任务分支 merge origin/kun，保留 CLI 来源展示与主线行为，推送后重新验证。
- 上一棒的任务页截图裁切右缘，聊天没有有效截图。修复后补实页任务/聊天的完整 375、390、768、1280 截图，含长 Bot 名、普通成员和手机可关闭、不截断；本轮发现阻断后未新做视觉放行。
- DENE-1792 的 outbox/worker 消费和四跳链仍是该票的集成验收，本票不冒充已经验证；保持既有来源契约和部署顺序交接。

## 验证
- 当前候选 HEAD `db5d030313`（后于 PR head `a68a393db1` 的提交仅为上一棒 scratch 证据），固定点 `origin/kun = 48197c4750`，diff 命令 `git diff origin/kun...HEAD`；实现主提交 `2715f07a4e`。
- `pnpm --filter @multica/core exec vitest run realtime/use-realtime-sync.test.ts api/bot-attribution.test.ts`：67 passed，exit 0。
- `pnpm --filter @multica/views exec vitest run common/via-bot.test.tsx issues/components/attribution-badge.test.tsx`：14 passed，exit 0。
- `pnpm --filter @multica/core exec vitest run realtime/bot-provenance.review.test.ts`：1 failed，exit 1；临时测试从产品目录移除，源文件留在附件，未修改实现。
- 通过 `scripts/test-db.sh` 建立并清理独立 PostgreSQL 测试数据库，执行 Bot handler/service/attribution/activity/trigger/auth 专项：已执行的测试通过，exit 0。Redis hit 与 daemon-cache 三个子用例因 Redis 未配置跳过；本轮不将它们算通过。auth 和 CLI 包该筛选下无测试运行。
- 未重跑全量前端/Go、原生真机、CLI 独立 PAT 或整条 Grok 推送验收。
- `git diff --check origin/kun...HEAD` 在上一棒自动提交的 scratch 契约文件发现一处尾空格；交付时整理证据文件。
- Jev 判定层已回落，本次由我自判：运行时规定的两个分发入口均缺失，未得到概率判断；F1 按真实失败证据定为阻断。

## 知识审计
本轮仅终审，没有新增稳定知识或修改产品代码。原契约 `server/docs/bot-write-provenance.md` 及 docs 索引保持；AGENTS.md 原有运行时注入保持。知识清单无缺项。分支未合并，保留交执行席；无删减。

要你做的：不用你做什么；由平台退回孙悟饭修正并重新交验收。
