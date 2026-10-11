# DENE-1811 交付检查点

当前分支：`agent/agent/dene-1811`。中断实现已从 `1ade82b31821298d72018ca180e6061f36818cc8` 恢复并提交到 `d8ecb98efb`，未恢复平台运行时文件。任务尚未完成，不应合并或置 done。

## 已完成：恢复与代码自检

- 服务端：连通修改成功后向双方发失效事件；看方 owner/admin 同时是源方 owner 时可改项目，双边审计；两边显示项目名单，看方过滤私密项目；pending 禁止开启托管。
- 查询：审计按时间与 UUID 游标分页，显示对方工作区、路由、对象；SQL 已经 `make sqlc` 重新生成。
- Web/Desktop：事件及重连刷新连通和总览；项目编辑、待接受筛选、审计分页、阅读页项目上下文及窄屏任务状态。
- CLI：看方更新项目时从源方解析项目；新增 `workspace link audit`。
- 文档：ADR-0004、权限表及内置 workspace-links skill 已同步。

本轮已通过：

- `bash scripts/test-db.sh --quiet -- go -C server test ./internal/workspacelink -count=1`，使用自动创建并删除的数据库，覆盖权限、私密项目拒绝、双边审计、同时间戳分页和 pending 托管拒绝。
- `go test ./internal/workspacelink ./internal/handler ./cmd/multica -run 'WorkspaceLink|Decide|Managed' -count=1`，未提供数据库的 handler 测试不算数据库验收。
- core 实时同步专项测试 1 条；views 设置与阅读页测试 12 条。
- core 与 views 的 `typecheck`。
- 任务代码 `git diff --check`；平台注入的 AGENTS.md 有尾随空白，不归本任务改动。

自检修正了事件 payload 的 unknown 类型访问、审计 SQL 的 `ap.name`（正确是 `ap.title`）及旧文案/旧权限断言。所有失败原始输出与复验输出留在任务附件。

## 下一段：边界与命令验收

1. 补 CLI 看方更新项目、审计分页的真实 HTTP 命令测试；补 handler 双边事件及审计返回字段测试。
2. 审核删除自动化后的审计对象名称：当前查询可读现存对象，删除后可能只剩 UUID；需保存操作时名称。
3. 核对外部 bot 能力地图是否发现新 audit 命令，全景是否还需审计入口；不另起入口。
4. 核对旧服务端能力字段缺省、编辑中远端更新、权限变化和 pending 已开启历史值等边界。

## 最后一段：真实浏览器与交付

1. `make up` 启动本 checkout 的隔离环境，仅创建新的测试工作区/连通，不改既有连通。
2. 同一设置页开着，用本地编译 CLI 修改项目和托管，验证无刷新变化；阅读页及总览同步检查，重连补拉。
3. 用 jev-ego 在 375 / 390 / 768 / 1280 截图并打开验收，覆盖项目编辑、关闭、分页和阅读状态；附含手机版的单文件 HTML 预览。
4. 处理最终检查，再按 end-work 开 PR 到 kun 并交验收。原生 App 按任务范围留给另票。

## 知识审计

- 术语/ADR：ADR-0004 与权限表已补双向编辑、pending 托管规则和同步契约。
- AGENTS.md：不改。
- DESIGN.md / INTERACTION.md：不改，复用既有页面。
- Skill：内置 multica-platform 的 workspace-links.md 已补新命令与字段。
- Multica Project Context：不改。
- 删减：去掉查询注释中“没有实时推送”的过时描述；未做跨任务分支清理。
