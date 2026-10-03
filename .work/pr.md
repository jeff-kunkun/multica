## 为什么

DENE-1291：守护进程重启后的重跑把代码写完了，但票一直停在 `todo`，也没收口。停摆判定（parking）已经把它算作「停了没交代」，可「跑完没收口 → 补一轮收尾」的恢复逻辑只认 `in_progress`，所以没人被叫醒。（DENE-1286 是 `in_progress`，恢复照常触发了，只是那一轮又碰上重启。这是按「只补一次」的设计走的，这里不改。）

## 改了什么

- `completionStallEligible`：除了 `in_progress`，`todo` 也算，但前提是刚跑完的运行属于这张票自己的智能体执行人。别的智能体被 @ 进来回答问题的运行不算，不会叫醒执行人。
- 系统提示和恢复说明里写出实际状态，不再固定写 `in_progress`。

## 三面

只改服务端内部调度，界面和 CLI 都不用动：停摆标签与收件箱本来就把 `todo` 计入，Agent 收到的是普通恢复运行。

## 验证

`scripts/test-db.sh -- go test ./internal/service/ -run 'CompletionStall|HandleCompletedTasks'`：全部通过，包括新加的两条 todo 用例（执行人自己的运行会被叫醒，别人的运行不叫醒）。`./internal/handler/` 里的 CompleteTask 用例也通过。

🤖 Generated with [Claude Code](https://claude.com/claude-code)
