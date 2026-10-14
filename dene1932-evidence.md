聊天里「改主意」已做好，等验收（PR 已开，合并后服务端随部署生效）。

**做成了什么**
- 发消息选“替换”：正在回的那一轮立刻停下，排队的旧消息全部作废（聊天里变灰，写“已被新指令替换”），新消息马上开跑；没有任何事在跑时同样作废旧排队、直接跑新的。
- 命令行：`multica chat send --mode replace`（返回新任务、被取消的任务、被作废的消息，`--wait` 可用）；新增 `multica chat clear-queue --session <id>` 只清排队。个人令牌和 Grok Bot 这类外部调用都能用。
- 新一轮的智能体会被告知“前面的指令作废”；技能说明里已写“意图改变用 --mode replace”。

**验到什么程度**
- 本机真实接口实测：1 条在跑 + 2 条排队 → 发替换后 3 条旧任务全取消、3 条旧消息标作废、新任务排队；网页 390/375/768/1280 截图都看到“已被新指令替换”标记。
- 自动测试：4 个数据库级用例 + 前端/手机端测试均通过。
- 没验到：原生手机 App 的标记没在模拟器/真机看过；“新任务 30 秒内被真实守护进程领起”没在本机验（本机不能起守护进程），只验到新任务已排在队首。

**还差的（已知）**
- 网页和手机 App 输入框里还没有“替换”按钮，目前由命令行/Bot 发起，界面只负责显示作废标记；建议后续单独开票做按钮。

**要你做的**
不用你做什么。合并后可在 Mac 版里让智能体发一次替换试试。

<details><summary>工程师看</summary>

PR: https://github.com/jeff-kunkun/multica/pull/666 （目标 kun，迁移 682 `chat_message.superseded_at/superseded_by_message_id`，`make migration-lint` 通过）
手机/多尺寸截图页：https://ai.example.com/api/attachments/01a12fad-fcb5-7c97-acbb-ee37d2036093/download
测试：handler 4 个 replace 用例 + 既有 restart/clear-queue 用例通过；views 668、core 600、mobile 262 通过；views/mobile typecheck 通过；core typecheck 有基线既有的 routing-settings.test.ts 一处报错，与本改动无关。
</details>
