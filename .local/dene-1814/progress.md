已从中断提交恢复本票代码并提交 b938382dae；本轮完成下一块 CLI 验证，测试提交 9aef957d94，续跑检查点已提交到 docs/evidence/DENE-1814/checkpoint.md。

已通过：列出可挂项目、挂上、清空、断开后显示失效（模拟服务端返回）、带参考项目建聊天、非法参数拒绝；实际 CLI --help 可用。命令沿用 DENE-1806 的 linked-projects / --reference / --clear。原有 AGENTS.md 运行时修改未纳入提交。

剩余分两块：①本地真实 CLI 挂载后下一次聊天简报验收，以及全景投影验证；②对齐最新主线、PR 和合并收口。本票仍 in_progress，本轮不宣称整体完成。测试是模拟 HTTP 契约测试，不能替代真实 run。上一轮系统盘不足、worktree 无受管环境；编译临时目录已改用有空间的数据盘，本轮构建成功。

源码回退检查：BASELINE 无 --reference/linked_projects 新字段，MODIFIED 有该字段，ROLLBACK 恢复原始哈希；此项仅是源码检查，不是运行集成证明。命令输出见附件。

## 知识审计
内置 workspace-links.md 已同步新命令；术语/ADR、AGENTS、设计/交互、项目描述不改；无删减。续跑事实已提交到 docs/evidence/DENE-1814/checkpoint.md。

要你做的：不用你做什么。
