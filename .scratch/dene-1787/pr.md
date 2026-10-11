## 改了什么
智能体默认按配置模型、绑定 CLI、旧阶梯兜底自动归类为 Claude / Codex / DeepSeek / Grok / Gemini。设置页或 `multica agent create/update --series` 可填写自定义系列，清空恢复自动值；列表支持按系列分组，API/CLI JSON 同时返回有效系列、手动值、自动值与来源。

路由判断模型可按 policy_prompt 返回 preferred_series，在已确定档位内优先可用席位；该系列不可用时保持普通选择并说明原因。保留人工档位选择与原有上调规则。自动模型家族来自同一解析源，手动系列不改变验收和额度接力的换家族判断。系列手动值是席位自身标签，不随基角色继承；跟随运行时的特化自动系列随模型/CLI 改变。

## 改前改后
- 原来没有系列读写与列表分组；现在新建无模型的 Claude/Codex 测试席位直接返回 Claude/auto、Codex/auto。
- 隔离环境实际 CLI 创建、破甲系列设置/读回/清空通过；浏览器手改后 CLI 读回一致，手机网页清空恢复 Claude/auto。
- 设置与列表已截图并检查 CSS 375/390/768/1280；HTML 预览含手机。截图和验证日志附在 DENE-1787，预览源为 docs/design/prototypes/agent-series.html。
- views typecheck、定向 eslint、58 项 UI/列表测试、235 项 schema 测试通过；routing/quotarelay 全套、带独立数据库的 handler/service 定向测试、迁移检查通过。sqlc 已生成。
- 完整 cmd/multica 测试集存在任务目录令牌保护导致的失败（requires MULTICA_TOKEN to be a task-scoped mat_ token），没有声称全套通过；实际新 CLI 已对隔离 API 跑通。未运行真实付费判断模型，路由采用受控判断输出验证。

## 合并风险
迁移 658 只新增默认空字符串的系列覆盖列（NOT NULL，空字符串表示自动），旧版本可继续工作。撤销应用提交即可退回旧行为并保留已有分类；down migration 会删除手动分类数据。路由候选增加同档备用席，避免首选席离线遮蔽同档其他可用席，已补回归。涉及自动选派，交验收席复核后合并。

Web/Desktop 共用页面，本次真实浏览器验证 Web；未启动 Electron。原生 apps/mobile 智能体页仍为 coming_soon 占位，没有同屏设置，故未新增原生流程。

## 知识审计
- 术语/ADR：CONTEXT.md 新增智能体系列、模型家族、系列偏好定义；未新增 ADR。
- AGENTS.md：不改，运行时注入片段不交付。
- DESIGN.md / INTERACTION.md：不改，复用既有设置与列表页面。
- Skill：multica-platform/references/agents.md 同步系列 CLI、来源、清空与路由边界。
- Multica Project Context：不改。
- 证据索引：docs/evidence/INDEX.md 新增预览及任务附件入口。
- 删减：无；本分支未合并，保留工作树。

Closes DENE-1787
