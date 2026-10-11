## 知识审计
- 术语/ADR：不改；run/status/phase/coverage 沿用父单已确认协议，无新增架构决定。
- AGENTS.md：不改；开场已有 runtime 注入及空白问题不属于本票，不提交。
- DESIGN.md / INTERACTION.md：不改；本票限 CLI，UI 与原生手机交代由服务端/UI 兄弟单负责。
- Skill：改了 server/internal/service/builtin_skills/multica-platform/references/overview.md，补变化/运行命令、作用域、游标、JSON、预算、错误恢复；沿用现有 routing entry。内置 skill 由服务器发布，不改 mono 或线上角色绑定。
- Multica Project Context：不改；依赖、环境和主线流程没有变化。
- 删减：无；未形成已合并分支，本任务分支保留；本轮不清理其他工作分支。
- Jev：按 skill 定位无可用物化路径，命令报 can't open file '//skills/general/jev/scripts/jev'，按其 fallback 自行审计；无新增地图级知识。
