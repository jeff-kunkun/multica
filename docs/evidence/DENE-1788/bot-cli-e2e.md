# DENE-1788 外部 Bot 钥匙：命令行全链路实测（本地环境）

2026-10-11，本地 worktree 环境（API 18781）+ 单独 profile 起的 daemon，智能体「小助手」跑在 Claude runtime 上。
Bot 侧只用设置页发出的那把钥匙（`multica login --token mul_…`，下文略去钥匙）。

| 步骤 | 命令（Bot 钥匙） | 结果 |
| --- | --- | --- |
| 查任务 | `multica issue list` | 列出 BOT-1 |
| 看完整描述 | `multica issue get BOT-1` | 标题、完整描述 |
| 评论 + 附件 | `multica issue comment add BOT-1 --content-file c.md --attachment note.txt` | 评论落下，带 1 个附件，网页可见 |
| 改状态 | `multica issue status BOT-1 in_progress` | 网页同步变「进行中」 |
| 新开聊天并等回复 | `multica chat new --agent 小助手 --title "Bot 验收 · 新开聊天" "1+1 等于几？只回答结果" --wait` | 6 秒后回复「1+1 等于 2。」，回复的 task_id 与发送一致 |
| 接着聊 | `multica chat send --session <聊天编号> "那 2+2 呢？" --wait --output table` | 回复「2+2 等于 4。」 |
| 看记录 | `multica chat history --session <聊天编号>` | 4 条消息（两问两答） |
| 最近使用 | 管理员 `multica bot list` | 测试 Bot 显示 LAST USED 2026-10-11 00:51 |
| 停用 | 管理员 `multica bot stop "测试 Bot"` | `Stopped 测试 Bot; its key now fails with 401.` |
| 停用后 | Bot `multica issue list` / `chat new` | 立即被拒：会话失效（服务端 401） |

`chat new` 回包节选：

```json
{
  "session_id": "01a12983-f12f-7af8-86a9-1ca296ca31d2",
  "title": "Bot 验收 · 新开聊天",
  "task_id": "01a12983-f142-7196-861d-59d4efbbda5a",
  "reply": {
    "role": "assistant",
    "content": "1+1 等于 2。",
    "task_id": "01a12983-f142-7196-861d-59d4efbbda5a",
    "elapsed_ms": 6011,
    "failure_reason": null
  }
}
```

`chat new` 走的是网页新建聊天的同一条接口（`POST /api/chat/sessions` + 发消息），智能体运行里调用会被拒并指向 `chat open`；`chat open` 的 agent-only 限制未改动。
