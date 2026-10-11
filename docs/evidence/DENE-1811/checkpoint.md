# DENE-1811 验收记录

## 交付范围

服务端向两边广播连通变化；设置、阅读与全景查询收到事件及重连后刷新。两边都显示共享项目，看方 owner/admin 同时是源方 owner 时可以改项目；Agent 不能管理连通。待接受连通不能新开托管，已有历史值仍可关闭。不迁移或修改任何现有连通数据。

审计支持对方工作区、路由、对象名称和稳定游标分页；新增 `multica workspace link audit`，看方 `update --project` 从源方解析项目。操作时保存对象名称，以免自动化删除后只剩 UUID。全景帮助、外部 bot 能力地图及内置 skill 已同步。

Web/Desktop 复用共享页面。阅读页展示描述、资源、项目记忆和手机任务状态。手机设置操作按钮有 44px 触控区。原生 `apps/mobile` 按票面范围由另一张票交付。

## 真实运行证据

- [四档实页 HTML 预览，含手机](https://ai.ferryway.cc/api/attachments/01a12b28-d999-7c01-83c9-d62126515601/download)
- [截图、CLI 读回、测试日志与权限回滚验证](https://ai.ferryway.cc/api/attachments/01a12b28-db43-7118-bed3-9c246e5d3039/download)

2026-10-11 在本 worktree 的隔离环境新建测试源方、看方和连通。CLI 改项目为「发布计划」、开启托管后，设置页同一 document 自动变化（timeOrigin 未变）；390px 网页改回「路线图」、关闭托管后 CLI 读回一致。断开确认框打开后取消成功。375 / 390 / 768 / 1280 下设置与阅读页实际截图均已打开检查，document scrollWidth 等于 innerWidth；移动任务状态、描述、资源和项目记忆可见。

## 检查结果

- 隔离数据库：`bash scripts/test-db.sh --quiet -- go -C server test ./internal/workspacelink ./internal/handler ./cmd/multica -run 'WorkspaceLink|Decide|Managed' -count=1` 通过；含权限、双边事件、私密项目拒绝、审计字段与同时间戳分页。
- core 实时同步测试 1 条、views 设置和阅读页测试 12 条通过；core/views 类型检查通过。
- 独立终审 PASS：Standards 0 项、Spec 0 项阻断；另跑 Decide 和 CLI HTTP 回归通过。
- `make sqlc` 已生成查询；任务 diff 空白检查通过。没有 schema migration。
- 未跑仓库全量测试、原生 App 测试或完整 Playwright 套件；范围内真实浏览器操作已覆盖。

初始测试登录缺少 CSRF cookie，正常登录修正 fixture 后操作通过；合并主线与一次 Go 编译并发造成瞬时类型错误，合并完成后同一检查通过。两者原始输出保留在附件，不视为产品失败。

## 知识审计

ADR-0004、权限表记录双向编辑、pending 托管及同步契约；内置 workspace-links skill 记录 CLI audit 和字段。证据索引增加本记录。AGENTS、CONTEXT、DESIGN、INTERACTION 和项目描述无稳定知识变化；未修改运行时块。删除旧检查点中已完成的待办与“没有实时推送”的过时注释；原始日志和截图仅留票据附件。
