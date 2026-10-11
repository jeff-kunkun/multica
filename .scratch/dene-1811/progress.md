上轮工作已恢复并提交为 d8ecb98efb，本轮代码自检与修正已提交为 ea73ae9a40。任务尚未完成，保持 in_progress。

本轮完成：真实隔离数据库的 workspacelink 测试通过（权限、双边审计、分页、pending 托管拒绝）；实时同步测试 1 条、页面测试 12 条通过；core/views 类型检查通过。修正了审计 SQL 字段名、事件类型检查与过时测试断言。现有连通数据未修改。

剩余已拆成两段，详见附件 checkpoint.md：① CLI/handler 联动测试、删除对象的审计名称保留与 bot 能力地图核对；② 浏览器 CLI 实时联动，375/390/768/1280 截图及 HTML 预览，随后 PR 与验收。

附件 verification.zip 包含失败原始输出、修正后通过日志，以及 Decide(OpUpdateProjects) 的基线/修改/回滚验证。基线不允许看方修改，修改后允许看方 admin 且源方 owner 的本人修改，回滚恢复拒绝；智能体三种状态均拒绝。恢复文件 hash 与原始一致。

## 知识审计
- 术语/ADR：已同步 ADR-0004 与权限表。
- AGENTS.md：不改。
- DESIGN.md / INTERACTION.md：不改。
- Skill：内置 workspace-links.md 已同步。
- Multica Project Context：不改。
- 删减：更新“没有实时推送”的过时注释；未清理其他任务分支。

要你做的：不用你做什么。本次按要求完成下一小段，尚未申请终验或标完成。
