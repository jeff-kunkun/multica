## 改了什么

新增 `multica overview`：一次服务端请求返回智能体、项目、活跃聊天与挂票、自动化、Skills、小队、运行时、关联工作区和外部 Bot 摘要。此前需要分别查询多个列表；现在 CLI 和 Web/Desktop 的「设置 → 工作区全景」复用 `GET /api/overview`，支持项目/智能体筛选。权限沿用现有列表与模块规则，无权区域返回空数组并列入 `omitted_sections`；摘要白名单排除提示词、环境配置、MCP 与 webhook 密钥。

三面位置：服务端 `handler/overview.go` 与工作区成员路由；CLI `cmd_overview.go`；共享设置 `overview-tab.tsx`。Agent 使用说明已进入内置 `multica-platform` Skill。原生 `apps/mobile` 没有相应全景页，本次新增入口只在共享 Web/Desktop；手机网页已实测，原生独立入口不属于本次范围。

Closes DENE-1803

## 改前改后

新增接口测试对照每个原有列表的数量，覆盖隐藏模块、私密聊天、任务发起人视角、项目/智能体关系、附加聊天项目、紧凑输出。真实路由测试通过实际铸造的 Bot 钥匙与主人 PAT，比较两种模式相同，并验证跨工作区拒绝及停用钥匙失效。

实测命令 `multica overview --output json` 输出 **3457 字节**，`--compact --output json` **1730 字节**（减少 50%）；隔离工作区的 agent/project/autopilot/skill/squad 均与单项 list 数量相同。数据库测试的另一组较丰富样本为 3902/1588 字节。不是生产数据快照：尚未部署，因此未宣称已核对线上约 52/14/13/74 的数量。

实际运行：
- 隔离 DB runner 下 `go test ./internal/handler ./cmd/server -run 'TestOverview|TestListModuleVisibility|TestChatDirectory' -count=1 -v`。
- `go test ./cmd/multica -run TestOverview -count=1`（help、JSON、单请求、参数与文本格式）。
- `pnpm install --frozen-lockfile`；views typecheck；针对性 ESLint；4 个 core API 测试和 39 个现有设置测试。
- 真实浏览器 375/390/768/1280，无水平溢出；筛选与刷新各一次聚合请求，手机下拉框可关闭、设置可切出返回。所有截图逐张打开检查。
- staged `git diff --check` 通过；未运行全仓测试、原生移动构建或生产部署。

[完整截图和命令证据](https://ai.ferryway.cc/api/attachments/01a12a17-15bf-79fb-8d0f-bcd0526d52f6/download) · [手机 390px](https://ai.ferryway.cc/api/attachments/01a12a17-4464-710c-bebd-a6f44acc6e92/download) · [桌面 1280px](https://ai.ferryway.cc/api/attachments/01a12a17-47cb-72c7-a509-02a26c9187b2/download)

## 合并风险

只读新增路径，无数据库迁移，可整体 revert。服务端在进程内复用原有列表处理器并提取摘要，避免另一套授权规则；聊天挂票、附加项目与自动化触发器仍有逐项数据库读取，大工作区需要关注延迟。过滤只保留可见关联，不把不受限派单席位视为所有项目负责人。关联工作区遵循原有 link list 可见字段，不展开其他工作区内部数据。

Jev 风险结论 `reviewer_pass`：交验收席复核共享契约与权限边界后合并。

## 知识审计

- 术语/ADR：不改，复用现有对象和权限语义。
- AGENTS.md：不改；运行时注入的未提交内容保留原位，不纳入 PR。
- DESIGN.md / INTERACTION.md：不改，沿用共享设置页及导航。
- Skill：内置 multica-platform 新增开场 overview 指引和 references/overview.md，记录字段、筛选和权限语义。
- 文档/证据：docs/README.md 新增使用入口；docs/evidence/INDEX.md 指向任务附件。
- Multica Project Context：不改，构建与工作树流程未改变。
- 删减：无；本任务分支保持原始 baseline 祖先，未重置/变基。待合并，未执行合并后分支清理。
