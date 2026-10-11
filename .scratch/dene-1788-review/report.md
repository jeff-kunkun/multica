验收不通过：设置页和钥匙管理已交付，但 Bot 新建聊天这一项明确验收仍未实现。PR #626 本轮不放行。

## Standards

本轮已读服务端、SQL、CLI、共享查询及设置页关键改动，未发现需要单独阻断的包边界或状态归属违规；不代表全部验收已通过。`git diff origin/kun...12b87fffea --check` 通过。审查固定点 `b699205331acd530de1bb8c72bb3bfda3662dc66`，PR head `12b87fffea65c3388c72795a81eb4dcc9cf8ebd8`。未改实现文件，未重跑整套测试和浏览器；沿用上一棒证据的部分没有冒充本轮实测。

## Spec

**F1 / P1 / 阻断：Bot 无法通过 CLI 新建聊天并拿到回复。**

- 依据：本票验收第二项明确要求“开聊天并拿到回复”，不是尚待 Kun 决定是否需要的新需求。
- 新增接入文案 `server/internal/handler/bot_access_token.go:406` 只给出 `chat list/history/send`，只能使用已有聊天。
- 调用链核对：`server/cmd/multica/cmd_chat.go:760` 的 `runChatOpen` 固定调用 `/api/chat/sessions/spawn`；`server/internal/handler/chat_spawn.go:77` 在 actor 不是 agent 时返回 `chat_spawn_agent_only`（403）。新 Bot 复用个人 PAT，因此没有能走通的新建聊天路径。现有 `TestChatSpawn_PersonCannotCall` 正是这条拒绝行为的测试；PR Known gap 和交付评论也确认未实现。
- 关闭条件：提供个人 PAT 可调用的 CLI 新建聊天入口，复用人类聊天接口和权限校验，不要拆掉既有 agent spawn 的守卫；接入指令给出可执行步骤，证明这把 Bot 钥匙可新建聊天、发送内容并读到回复。

真实 Grok 云电脑验收仍缺证据：列票、完整描述及评论、新建聊天与回复、改状态、上传附件各一次，网页可见，并核对最近使用与停用后 401。代码链路补好后再安排；本地 API smoke 不能代替这一项，不能只让 Kun 测列票和评论就宣布全部完成。

判断层：按 code-review / jev 技能尝试批量复核；本 run 的约定位置没有 `scripts/jev`，命令退出 2（文件不存在），按技能缺分发的回落规则由本席人工判断，无模型 results/confidence/usage 可报告。安装脚本地址匿名读取实际返回 200，因此没有将“私有仓库下载失败”的猜测列为 finding。

[@孙悟空](mention://agent/73f4e44b-2a52-43c4-b957-ea8b1f8b2aba) 请接回唯一实现席：补齐 F1、更新接入指令和相关 CLI skill，再带新建聊天→回复的证据交验；补上仓库要求的 375/768 宽度证据（已有 390/1280）。真实 Grok 如确实需要 Kun 操作，明确列出完整步骤与等待项，保留未完成验收，不能自行降标。

两轴汇总：Standards 0 项阻断；Spec 1 项阻断，最高 P1（Bot 新建聊天缺失）。

要你做的：Kun 现在不用操作；先由孙悟空补齐能测通的接入链路。
