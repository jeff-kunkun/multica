已实现智能体系列，PR：https://github.com/jeff-kunkun/multica/pull/625 （提交 8e7b38bd01，目标 kun）。交验收席复核自动选派边界后合并。

现在新席位能按模型/CLI 自动归类，也能在设置页和 CLI 改成自定义系列；清空回到自动值。列表可以按系列分组。路由在已选档位内优先偏好系列，该系列不可用就正常回落；手动系列不改变验收和额度接力使用的模型家族。

已验证：真实 CLI 创建 Claude/Codex 席位、设置/读回/清空；真实设置页修改“破甲系列”后 CLI 读回一致；手机清空恢复自动、返回列表、选择系列分组并关闭菜单。设置和列表已检查 375/390/768/1280 截图。views typecheck、定向 lint、58 项 UI/列表与 235 项 schema 测试、routing/quotarelay 全套、独立数据库 handler/service 相关测试及迁移检查通过。CLI 回滚事务观察到 BASELINE 无 --series，MODIFIED 有 --series，ROLLBACK 恢复无 --series，原件和恢复件哈希相同。

限制：完整 CLI 测试集的一批既有用例被任务目录的 task-scoped token 保护拦住，未声称全套通过；新 CLI 对隔离 API 的实际操作通过。路由测试用受控判断输出，未调用真实付费判断模型；未启动独立 Electron。原生手机 App 无智能体设置同屏（仍为占位），本次未新建原生流程。

!file[agent-series.html](https://ai.ferryway.cc/api/attachments/01a12967-e027-7671-a6bf-2dceff0ba454/download)

!file[DENE-1787-evidence.zip](https://ai.ferryway.cc/api/attachments/01a12968-2864-72af-b64a-a3e50b6c0e55/download)

## 知识审计
- CONTEXT.md：新增智能体系列、模型家族、系列偏好。
- docs/evidence/INDEX.md：新增预览与本票附件入口。
- multica-platform/references/agents.md：同步 CLI 与来源/继承/路由边界。
- AGENTS.md、DESIGN.md、INTERACTION.md、项目配置：不改；启动注入片段未提交。
- 删减：无；任务分支未合并，保留。浏览器空间和本任务开发服务已停止。

要你做的：不用你做什么，下一步由验收席复核。
