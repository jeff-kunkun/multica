## DENE-1293 移动端验收证据

视口：390px。共享 `BreadcrumbHeader` 为所有资源详情提供左上角返回；有浏览历史时回到真实来源，无历史时回退到最近列表。详情操作区、宽表与日志使用可触摸横向滚动，长内容不会压缩页面。

| 页面 | 结论 | 证据/处理 |
| --- | --- | --- |
| 聊天 | 已核对 | 聊天页已有来源回退；消息区域保持纵向滚动。 |
| 智能体（含新建） | 已核对 | 新建流程已有返回/取消入口；详情沿用共享返回。 |
| 小队 | 已修 | 详情使用共享返回；操作区可在窄屏触摸滚动。 |
| 技能 | 已修 | 详情使用共享返回；长说明在内容区换行。 |
| 自动化 | 已修 | 详情使用共享返回；操作区不挤压标题。 |
| 运行时 | 已修 | 共享返回；加载骨架手机单列；删除级联 Agent 表可横向滚动。 |
| 成员 | 已核对 | 成员详情沿用导航回退；列表可纵向滚动。 |
| 用量 | 已修 | 每日明细表保留 600px 内容轨道并支持横向触摸滚动。 |
| 账单 | 已核对 | 账单设置页沿用列表返回；金额列不强制压缩。 |
| 设置 | 已核对 | 设置分组可纵向滚动，控件保持触摸尺寸。 |
| 关联工作区 | 已核对 | 关联详情沿用共享返回；长名称允许换行。 |
| 附件预览 | 已核对 | 预览关闭/返回入口可触摸；内容区独立滚动。 |

代码验证：

- `pnpm --filter @multica/views typecheck`
- `pnpm --filter @multica/views exec vitest run projects/components/project-detail.test.tsx runtimes/components/usage-section.test.tsx runtimes/components/runtime-detail-visibility.test.tsx`（3 files / 26 tests passed）
- `git diff --check`

`apps/mobile`：本票涉及的协作与资源管理详情均为 Web/Desktop 视图，原生 App 没有对应详情屏，因此本轮不改原生 App。
