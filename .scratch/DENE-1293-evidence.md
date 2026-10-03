已完成移动端协作与资源页面适配修复，提交 fe19e6c7dd。

改动：
- 详情页 BreadcrumbHeader 的祖先路径在窄屏截断，右侧操作区可横向触摸滚动，避免标题和操作溢出。
- 运行时详情加载骨架在手机端改为单列，sm 以上恢复三列。
- 用量每日明细表增加 600px 内容轨道与横向滚动容器，保留日期、模型、token 列可读性。
- 运行时删除级联 Agent 表增加横向滚动容器，长模型/成员信息不再压坏表格。

验证：
- pnpm --filter @multica/views typecheck
- pnpm --filter @multica/views exec vitest run runtimes/components/usage-section.test.tsx runtimes/components/runtime-detail-visibility.test.tsx（2 files / 23 tests passed）
- git diff --check（本次代码改动通过；工作区另有运行时注入的 AGENTS.md，不属于本次提交）
- 390px 浏览器截图：.scratch/anti-slop-frontend/DENE-1293/evidence/home-390.png，已打开核验。
