# DENE-1787 验证记录

隔离环境：dene_1787_1e9676b73c19-510；Web 13510 / API 18590。运行时为本地数据库测试夹具，无模型执行、无外部模型消费。

## 实际功能
- 新建 Claude CLI 和 Codex CLI 席位，无指定 model：分别返回 Claude/auto、Codex/auto。
- CLI 修改 Codex 为破甲系列，读回 manual，清空恢复 Codex/auto。
- 浏览器设置页把 Claude 改为破甲系列，出现“手动 / 已保存”；CLI 读回 series=破甲系列, series_source=manual, automatic_series=Claude。
- 375px 下输入框清空并失焦，CLI 读回 Claude/auto，series_override 为空。
- 手机点击面包屑返回列表；显示菜单选择“系列”，关闭菜单后 Claude 与 Codex 各成一组。
- 设置页/列表覆盖 CSS viewport 375、390、768、1280，截图逐张打开检查。截图按浏览器 110% 缩放采集实际完整画布，因此像素宽度为 CSS 视口的 1.1 倍。设置页四档 document.scrollWidth == innerWidth。列表 768 保留既有横向表格承载；系列标题未截断。
- HTML 预览另附 1280/390 截图。预览为模拟稿，不冒充真实产品。
- Ego Task Space 120 已关闭。

## 自动验证
- pnpm install --frozen-lockfile 成功。
- views typecheck / 定向 eslint 成功；UI/列表相关 58 tests，core schema 235 tests 通过（子执行面运行）。
- make migration-lint 通过；make sqlc 已重新生成。
- 带独立测试数据库的 handler/service：TestAgentSeries、TestAgentRouting、Test.*Relay、TestCapacityAvoid 通过。新测试覆盖 API 设置/清空/超长与控制字符拒绝。
- routing 与 quotarelay 全套通过；覆盖同档偏好、无可用席回落、不越档、默认席离线、手动系列不改变家族。LLM/SystemOne 请求合同测试通过，未调用真实判断模型。
- CLI 相关旧测试 TestAgentUpdateRoutingUsageFlag 等通过；完整 cmd/multica 测试集在任务工作目录触发既有任务令牌保护，多个非本功能测试报 requires MULTICA_TOKEN to be a task-scoped mat_ token。未声称完整套件通过；实际新 CLI 对隔离 API 的操作通过。
- git diff --check 排除启动时已有的 AGENTS.md 注入片段后通过。

## CLI 回滚事务
transaction/ 下保留四角色产物，BASELINE / MODIFIED / ROLLBACK 都真实构建 CLI 并运行同一 agent update --help。旧副本没有 --series；修改副本有 --series；回滚副本恢复没有 --series。恢复哈希与初始原件一致；patch 可重建修改副本。此事务只覆盖 CLI 源文件，项目回退应撤销 PR；数据库列可保留供旧版本兼容，不建议部署时主动删除手动分类数据。

## 限制
原生 apps/mobile 的智能体页仅 coming_soon 占位，没有对应设置屏，未创建原生新流程。未启动 Electron 独立进程；Web/Desktop 复用同一共享页面。未执行真实模型派单；以受控判断答案走路由完整分配流程验证。
