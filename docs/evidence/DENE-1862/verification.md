# 外部 Bot 事件送达修复：本地验证

本次验证使用任务 worktree 的受管环境与每轮新建、迁移后销毁的测试数据库；接收端是本地 HTTP 服务。没有修改线上 Grok 的规则，也没有部署。

## 行为证据

- 原版本的 `TestBotWakeLocalHTTP` 通过，实际记录 task/status/chat 来源不明时停在 pending，以及提及被小时额度压住。
- 修改后的 `TestBotWakeReliableDelivery` 通过：task / done / needs_kun / mention 各一条在 5 秒内完成本地发送；无来源时 author type/id 留空并附 note；有 source_task_id 时按工作区恢复实际运行的智能体并保存。
- 上限 1，三个普通事件：第一条发出，另两条 pending，GET 读出 hourly_limit=1、limited_count=2；提及继续送达且普通额度计数仍为 1。测试推进小时窗口并推进一秒重查时钟，未将事件手工改为 queued；两条按顺序补发，pending 清空。
- 关闭静默并提高额度后，保存接口立即重排等待时间，下一次 worker 检查完成发送。
- 推送 ack_url 与 CLI ack 使用同一个 /pending/ack 地址和 event_ids 请求体；woken_url / result_url 与旧的裸事件回执地址均返回 200。确认与发送状态分别保存。
- 回执测试覆盖 sending 阶段先收到确认，再完成 HTTP 200，确认记录与 reason 不被覆盖；重复确认幂等，跨 Bot、私有来源、失败发送语义保持原有保护。

## 已运行

```sh
pnpm install --frozen-lockfile
make sqlc
bash scripts/test-db.sh --quiet -- sh -c 'cd server && go test ./internal/handler -run TestBotWake -count=1 -v && go test ./cmd/multica ./cmd/server -run "TestBotWake|TestBotReceipt" -count=1'
pnpm --filter @multica/views exec vitest run settings/components/bots-tab.test.tsx --testTimeout 20000
pnpm --filter @multica/core exec vitest run api/bot-wake-stats.test.ts
pnpm --filter @multica/core typecheck
pnpm --filter @multica/views exec tsc --noEmit --incremental --tsBuildInfoFile /tmp/dene-1862-views.tsbuildinfo
pnpm --filter @multica/views exec eslint settings/components/bot-wake-page.tsx settings/components/bots-tab.test.tsx
pnpm --filter @multica/core exec eslint api/client.ts api/schemas.ts api/bot-wake-stats.test.ts bots/queries.ts types/api.ts
git diff --check -- . ':!AGENTS.md'
```

Go 路径通过；cmd/server 编译通过，该筛选下无测试。设置页 17 个测试、统计字段兼容 7 个测试通过。设置页测试默认 5 秒首次有 4 个超时，使用 20 秒后通过；新增统计断言也通过。旧服务器缺少或返回畸形 limited_count 时保持已保存规则并将数量降级为 0。

基线与回滚分别在独立代码副本、独立测试数据库运行原版本的 TestBotWakeLocalHTTP，均通过；回滚副本恢复了原来的来源卡住与提及共用额度行为。任务分支保留修复。

## 待验证与边界

- Ego 的本轮 Task Space 24 在本地页面首次编译期间连续无响应，出现 PageNavigationTimeoutError、Target.getTargets 超时、PageEvaluationTimeoutError；随后已关闭页面和空间。没有取得 375 / 390 / 768 / 1280 真实截图，不能把单测作为手机视觉验收。
- 共享设置页同时供 Web/Desktop 使用，沿用现有行式布局、触控尺寸；原生 apps/mobile 没有外部 Bot 推送设置页或同名入口，本次未新增原生页面。
- 尚未验证生产 Grok 的实际执行、生产旧事件清空、上线后的外部接收程序是否使用 ack_body。旧 payload 的裸事件回执路径继续可用。
- 没有运行全量 make test、前端全量测试或全量端到端。没有修改 daemon 心跳、离线判定、令牌实现，也没有重做内部运行叫醒链或运行合并。
- 旧版本记录的提及尝试在升级后一小时内仍可能占用历史计数，届时自然过期；新版本提及不再计入额度。0 仍暂停全部推送，静默时段仍适用于提及。
- 本轮测试服务已用 make down 停止，数据库与环境槽位保留。
