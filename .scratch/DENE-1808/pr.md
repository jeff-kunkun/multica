## What does this PR do?

可托管连通增加向本次 run 发起人自己的源方聊天发送文本：`multica chat list|send --linked <source>`。消息显示“发起人 经 看方·智能体 代发”，进入智能体上下文时包为外部内容，并沿现有 `managed_write` 记聊天 ID。新建连通默认开启托管，可在表单或 `--managed=false` 关闭；旧连通不变，无新增迁移。

Closes DENE-1808

## Changes Made

服务端集中在现有 Managed 判定和本人聊天边界；Web/Desktop 默认开关与消息来源同步，原生 App 同步来源展示（无对应连通设置屏）。CLI 支持目录与排队发送，JSON 返回消息、目标聊天及代发来源；overview 既有 `managed` 字段继续表达开关，Bot 能力文档已同步。

## How to Test

隔离数据库 workspacelink 全包通过；CLI 相关测试通过；views/mobile typecheck、相关 lint 通过，设置/消息/复制/实时缓存测试通过。已验证 managed.go 的 baseline / modified / rollback 行为和恢复哈希。未跑仓库全量检查。

[HTML 预览（含手机）](https://ai.ferryway.cc/api/attachments/01a12a2c-418e-78c0-bef9-e084235a21f1/download)，375/390/768/1280 预览截图已查看。**真实设置/聊天路由尚未截图验收**：本地登录后停在工作区创建流程。ai-multica 已升级到 0.6.3-test.1；本功能还需 PR 合入后自动跟进服务端，并发布新测试版、升级两个运行环境，才能验证 WD→kunkunya 投递/叫醒。未修改线上连通。

## 合并风险

涉及跨工作区权限，交验收席复核后再合并。只有发起人本人聊天能读/发送，共享聊天不纳入；停用托管、断开、访客或已结束 run 拒绝。只开放排队文本，未开放附件、创建聊天、交棒、重启。回退应用代码不需要回退 schema；本 PR 不改变已存在连通值。

## 知识审计

- 术语/ADR：CONTEXT.md、ADR-0004 与权限文档更新可托管聊天代发及新连通默认值。
- AGENTS.md：不改；运行时注入片段不提交。
- DESIGN.md / INTERACTION.md：不改，复用既有页面。
- Skill：builtin multica-platform workspace-links / external-bots / 入口同步。
- Multica Project Context：不改。
- 删减：撤掉已作废的独立接收名单方向；不清理其他任务分支。

## AI Disclosure

Codex：按任务最新方向实现并完成聚焦验证；高风险权限改动交验收席复核。

## 续跑拆分

1. 已保存：上轮实现提交 09e06ff81c；中断快照中的证据已恢复，未夹带运行时文件。
2. 本轮完成块：托管投递唤醒时保留接收智能体按发起人权限加载的集成配置；补充数据库回归测试。
3. 后续验收块：真实 Web/Desktop 设置与聊天路由的窄屏截图；权限终审。
4. 发布后验收块：新测试版覆盖 CLI 参数后，执行 WD 到源方聊天的投递与唤醒验收。

本轮隔离数据库验证：service（0.821s）、handler（0.856s）通过，退出 0；覆盖集成配置落库、托管发送/读取、外部内容及事务回滚。首次测试有方法名及缺事件总线的 fixture 错误，修正后通过。完整结果见 docs/evidence/DENE-1808/README.md。

!file[review-evidence.zip](https://ai.ferryway.cc/api/attachments/01a12b1c-d060-7bc5-ae6b-48509cf1004e/download)
