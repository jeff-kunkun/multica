# DENE-1808 验证记录

实现按任务 2026-10-11 的改向：复用可托管，只向 run 发起人本人的源方聊天代发。旧连通不回写，无迁移。

## 续跑完成块

上轮实现保存在 `09e06ff81c`，中断快照只补回本地证据。此次补齐接收智能体的集成配置：在事务外按发起人与接收智能体加载，在同一消息/任务事务内保存。测试验证落库配置和身份，不调用真实外部集成。

2026-10-11，在脚本创建并清理的隔离 PostgreSQL 数据库执行：

```sh
bash scripts/test-db.sh --quiet -- sh -c 'cd server && go test ./internal/service ./internal/handler -run "TestManagedChat|TestLinkedChatDelivery|TestLinkedDeliveryHandoff" -count=1'
```

结果：service 通过（0.821s），handler 通过（0.856s），命令退出 0。涵盖托管发送/读取、本人聊天目录、来源与外部内容包裹、消息/唤醒事务回滚、接收集成配置。首次新增测试编译时方法名写错，修正后测试 fixture 缺事件总线导致 panic；补齐 fixture 后上述命令通过。生产逻辑未因 fixture 失败改变。

上轮记录：workspacelink 全包与 CLI 聚焦测试、views/mobile typecheck、相关 lint、设置/消息/复制/实时缓存测试通过；未执行仓库全量检查。

路由探针：BASELINE 拒绝聊天目录/发送；MODIFIED 允许目录/发送且拒绝创建聊天；ROLLBACK 恢复原行为与原 SHA-256。它仅证明 managed.go 的路由变化，不是整体部署回滚演练。

## 剩余小块

1. **真实界面验收**：已有 375/390/768/1280 HTML 预览及截图；真实应用上轮登录后停在工作区创建，尚未取得实际设置/聊天页面截图。需确认手机开关可操作、代发标识完整。
2. **权限终审**：跨工作区身份与外部内容边界交验收席复核，再合并。
3. **发布后验收**：已有运行环境升级完成，但 0.6.3-test.1 不含本票 CLI 参数。合入后等待自建服务自动跟进并核对版本；发新测试版并升级 grok生产服、deepseek生产服，再执行 WD → kunkunya 聊天发送。不得手工部署或改现有连通设置。

原生 App 无连通设置屏；聊天消息列表已同步来源显示。
