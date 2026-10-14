# DENE-1852：失效凭据导致本机任务排队

日期：2026-10-11，时间均为 America/Chicago（UTC-05:00）。

本记录覆盖 DENE-1852 的认证恢复部分，不关闭整张工作单。原始日志、旧配置与待补交结果保留在本机权限受限的备份目录；不把凭据或任务输出提交到仓库。

## 现场与根因

现场为 Desktop `0.6.3-test.5`，构建提交 `9b1a6d72f6`，活动 profile 为 `desktop-origin.ai.example.com`。实际执行的是应用内捆绑 CLI；PATH 中另一个旧 CLI 不能代表 Desktop 的行为。源码候选基于 `kun` 的 `de44a71f3f609b82ce4b46153449b44ccc4302c0`。

| 时间 | 证据 |
| --- | --- |
| 09:17 | 既有 DENE-1852 原话记录删除旧个人密钥、创建新钥匙。 |
| 09:17:24.410 | 本机日志首次出现失效凭据 HTTP 401。到 11:01 超过 3.3 万条，任务开始、完成和失败回报均受影响。 |
| 11:01:55 | 原守护进程已停止；后续 Desktop 启动在一秒内失败。界面显示本机 54 项排队、0 项运行。 |
| 11:05:33 | 备份活动配置、用户 sidecar、日志及完整结果队列，再仅移除服务端明确拒绝的缓存 token。49 条待补交结果中，7 条完成、42 条失败。 |
| 11:06:05 | 重开 Desktop，现有登录会话签发新 PAT，守护进程恢复启动，49 条结果全部补交，隔离失败队列为 0。 |
| 11:21 | 健康端点仍为 running，运行约 15 分钟，21 项活动任务；待补交与隔离失败计数均为 0。活动任务不等于已完成任务。 |

这是一条跨三层的失效链：

1. 服务端仅在 WebSocket 升级时认证。删除 PAT 后，HTTP 请求被拒绝，但原长连接仍以握手时的身份发心跳、处理 RPC，机器继续显得在线。
2. daemon 对自身凭据的 401 只记录/重试，未停止接单或主动断开长连接。无法写回终态的任务积累。
3. Desktop 仅在 starting 持续 10 秒后探测认证。快速失败先把状态置为 stopped，错过探测；同账号登录又无条件复用缓存 PAT，形成重启仍失败的循环。

本机恢复只解决活动凭据与结果补交。恢复后还有大量任务同时构建，系统负载很高；这支持另行处理 DENE-1852 的并发上限部分，但不是本次 401 链路的替代解释。

恢复后另有目录身份回填返回 `401 daemon token required`：该调用使用 PAT，而接口要求 daemon token。这与 `invalid token` 不同，不能断言“所有 401 已消失”。回填是 best-effort；本次把这条真实边界加入停机防误判测试，其接口/令牌适配需要另行处理。

## 源码候选

- `server/internal/handler/daemon_ws.go`：为 PAT 连接绑定只保留哈希的重新认证函数，直接查权威 token 表，避免握手缓存掩盖撤销、删除、轮换或到期。
- `server/internal/daemonws/hub.go`：每条连接启动时及每 15 秒复验一次；失效关闭 1008，存储异常关闭 1013；取消该连接 RPC 并清理 presence 索引。不按每个 runtime 重复查库。
- `server/internal/daemon/{client.go,daemon.go,wakeup.go}`：自身凭据的 HTTP/握手 401 经 `/api/me` 确认后取消 daemon 生命周期，停止接单与连接；保留结果队列。认证探测限制为同时一个、最长 5 秒。单任务令牌、端点令牌类型限制和 403/429/5xx 不误触发。
- `apps/desktop/src/main/daemon-manager.ts`：启动快速失败也立即探测认证；登录时仅替换被明确拒绝的缓存 PAT；同账号凭据改变也重启存量 daemon，使进程读取新钥匙。

沿用现有服务端在线租约、Desktop 重新认证入口和 CLI 登录命令，不新增用户操作流程。Web 与原生手机端不管理本机 daemon 进程，因此无需新增对应页面。未改页面布局。

## 验证

在独立 worktree 内验证，不修改正在运行的主 checkout。数据库用 `scripts/test-db.sh` 在已核实的本机 PostgreSQL 上创建独立随机测试库，迁移后运行 handler 测试并自动删除。

- 将 Desktop 实现临时还原到基线运行新增 IPC 回归测试：13 项中 3 项按预期失败，分别重现快速失败无认证提示、复用失效 PAT、同账号换钥匙未重启；随后恢复候选实现。
- 候选 Desktop 的新增 IPC 回归及既有认证探测、恢复策略、profile、OS 边界测试共 5 个文件、80 项全部通过。
- Go daemon/daemonws 定向测试通过：自身/任务令牌边界、健康长连接在确认 401 后断开、握手 401、端点自身 401 防误停、结果队列保留、连接认证复验、临时存储错误、连接关闭后的探测取消。
- handler 定向测试通过，实际测试库验证 PAT 撤销、到期、删除与轮换。
- Desktop 主进程类型检查、修改文件 ESLint 和 `git diff --check` 通过。没有在高负载本机运行整个 monorepo 测试、打发行包或对生产做撤销实验。

具体命令（从仓库根目录或对应 workspace 执行）：

```sh
pnpm --filter @multica/desktop exec vitest run src/main/daemon-manager.auth.test.ts --maxWorkers=1
pnpm --filter @multica/desktop exec vitest run src/main/daemon-manager.auth.test.ts src/main/daemon-auth-probe.test.ts src/main/daemon-recovery.test.ts src/main/daemon-profile.test.ts src/main/daemon-os.test.ts --maxWorkers=1
pnpm --filter @multica/desktop run typecheck:node
pnpm --filter @multica/desktop exec eslint src/main/daemon-manager.ts src/main/daemon-manager.auth.test.ts
cd server
bash ../scripts/go-test-with-agent-cli-guard.sh -- go test ./internal/daemon ./internal/daemonws -run 'TestDaemonAuthRejection|TestWebSocket.*(Credential|AuthProbe)' -count=1
GOMAXPROCS=2 bash ../scripts/go-test-with-agent-cli-guard.sh -- go test -p 2 ./internal/daemon ./internal/daemonws -run 'Test.*(Auth|Token|Wakeup|TerminalReport|Heartbeat|RPC|WebSocket|Hub)' -count=1
bash ../scripts/test-db.sh --quiet -- bash ../scripts/go-test-with-agent-cli-guard.sh -- go test ./internal/handler -run 'TestDaemonWebSocket|TestBuildDaemonWebSocketIdentity' -count=1
```

## 集成边界

本次现场恢复已生效；源码候选仍需经 `kun` 集成及对应 backend、CLI/Desktop 发版，不能把本机恢复当成候选已部署。服务端独立复验可防旧客户端保持失效长连接；新 CLI/Desktop 进一步提供快速停止与恢复。

没有在活动生产凭据上做撤销实验，没有新增任务自动改派、用户通知、开机自启、并发上限，也没有改变非 PAT 的 WebSocket 认证。这些仍属于 DENE-1852 的其他范围。断连后的在线状态变化遵循原服务端存活/租约规则，不宣称所有界面在 15 秒内完成离线与改派。
