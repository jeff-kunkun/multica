## 结论
按 2026-10-11 08:47 的最新决定交付候选：复用可托管向 run 发起人自己在源方的聊天代发文本，新建连通默认开托管、可关；既有连通没有回写。没有新增接收名单、独立投递出口或数据库迁移。

## 状态
待验收。跨工作区权限与外部消息信任边界属于高风险改动，交验收席复核；真实设置/聊天页面的浏览器验收及包含本功能的新测试版发布后的 WD 线上验收尚未完成。

## 证据
- 托管白名单加入目录、本人聊天读取和文本排队发送；源方聊天显示“发起人 经 看方·智能体 代发”，每条成功发送沿 managed_write 记录 chat_id。
- 所有代发进入简报、历史、交接、回顾时包为外部内容；实时缓存、Web/Desktop 与原生手机显示保留来源。原生 App 无工作区连通设置屏，因此只同步聊天消息列表。
- 隔离 PostgreSQL 上 workspacelink 包测试通过，覆盖原有拒绝矩阵、聊天本人边界、托管关/断开、默认 true、显式 false、老行不变。
- CLI 聚焦测试通过：chat list/send --linked 的请求头与回执、文本排队限制、create managed=true/false。
- views/mobile typecheck 与相关 lint 通过；设置 9、消息列表 41、复制 12、实时缓存 64 项测试通过。未跑仓库全量检查。
- 四档 HTML 预览截图已查看（375/390/768/1280）。真实本地应用成功登录，但在创建工作区流程未确认完成，不能当实际设置/聊天路由验收。
- 路由回滚探针：BASELINE 不放行聊天目录/发送；MODIFIED 放行目录/发送且仍拒绝创建聊天；ROLLBACK 恢复原行为和原 SHA-256。此探针只验证 managed.go，不声称整套功能已经部署或回滚。

!file[dene-1808-managed-delivery.html](https://ai.ferryway.cc/api/attachments/01a12a2c-418e-78c0-bef9-e084235a21f1/download)

线上验收：已有升级前提解除，但 0.6.3-test.1 不含本票的新 CLI 参数。PR 合入并确认服务端自动跟进的版本后，先发新测试版并升级 grok生产服、deepseek生产服（不手工部署、不改连通设置）。随后 WD 智能体运行 `multica chat send --linked kunkunya --session <孙悟空游戏聊天ID> --content-file note.md --mode queue --output json`。预期源方聊天收到代发标识并产生接收智能体任务；若不符，保留命令 JSON、目标聊天 ID 与任务 ID。

## 下一责任人
验收席：复核权限边界、默认值与证据缺口。

## 唤醒动作
通过 issue close 的 in_review 路由交验收席，不另发重复 @。

## 知识审计
- 术语/ADR：更新 CONTEXT.md 的聊天代发定义及 ADR-0004 可托管白名单/新连通默认值，同步权限文档。
- AGENTS.md：不改，运行时注入内容保留在本地、不提交。
- DESIGN.md / INTERACTION.md：沿用现有表单与聊天消息布局，无新增跨页契约。
- Skill：更新 builtin multica-platform 的 workspace-links、external-bots 与技能入口。
- Multica Project Context：不改。
- 删减：撤除旧独立投递方案；未清理其他任务分支。

## 续跑拆分

1. 已保存：上轮实现提交 09e06ff81c；中断快照中的证据已恢复，未夹带运行时文件。
2. 本轮完成块：托管投递唤醒时保留接收智能体按发起人权限加载的集成配置；补充数据库回归测试。
3. 后续验收块：真实 Web/Desktop 设置与聊天路由的窄屏截图；权限终审。
4. 发布后验收块：新测试版覆盖 CLI 参数后，执行 WD 到源方聊天的投递与唤醒验收。

本轮隔离数据库验证：service（0.821s）、handler（0.856s）通过，退出 0；覆盖集成配置落库、托管发送/读取、外部内容及事务回滚。首次测试有方法名及缺事件总线的 fixture 错误，修正后通过。完整结果见 docs/evidence/DENE-1808/README.md。

!file[review-evidence.zip](https://ai.ferryway.cc/api/attachments/01a12b1c-d060-7bc5-ae6b-48509cf1004e/download)
