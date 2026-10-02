已完成 DENE-1160 的会话参数隔离。

- Claude、CodeBuddy 现在会过滤 `-c/--continue/-r/--resume/--session-id/--fork-session`，并保留 daemon 注入的会话参数；同类 daemon 会话参数也补到了 Antigravity、Grok、Qwen、Cursor、OpenCode、DevEco、CodeArts。
- 服务端创建和更新 agent 时，会按目标 runtime provider 拒绝 Codex 风格的 `-c key=value`，错误会明确说明 `-c` 在该 provider 中用于会话续接，并建议使用 agent 的 model/reasoning 配置。Web 自定义参数保存和 `multica agent create/update` 都复用这条服务端错误。
- 新增了 provider 校验、Claude 参数过滤、Cursor resume 覆盖和创建/更新 API 的单测；CLI help 与 multica-platform agent 文档同步说明了限制。

验证：

- `go test ./pkg/agent ./internal/handler -count=1` 通过。
- CLI 定向参数测试通过：`go test ./cmd/multica -run 'TestAgent(Create|Update).*Custom|TestParseCustomArgs' -count=1`。
- `go test ./cmd/multica -count=1` 未能全量通过，测试被当前 worktree 已存在的 daemon task marker 拦截（与本次改动无关）。
- `git diff --check origin/kun...HEAD` 通过。

提交：`f9eb25b910`（连同实现提交 `64f88e4c3a`）。
