# Multica 设计契约

> status: accepted
> 决策负责人: Kun；以下规则提取自现有共享界面。
> token 真理源: `packages/ui/styles/tokens.css`
> 视觉证据: 本地真实浏览器验收附件；持续入口见 `docs/evidence/INDEX.md`。

## 概览

Web/Desktop 使用共享页面和组件。手机网页沿用同一交互，原生手机界面使用同一服务端动作。

## 设计原则

- 沿用名称、简短说明与控件的布局。颜色表示状态，文案直接说明动作与范围。
- 停止前允许人选择范围；提交期间显示等待，成功后关闭，失败时保留当前界面。
- 聊天转为任务后，原聊天显示任务入口；当前屏只保留一个执行处。
- 命名和中英文文案遵循 `apps/docs/content/docs/developers/conventions.mdx` 与 `conventions.zh.mdx`。

## Token 真理源

规范来源：`packages/ui/styles/tokens.css`；`packages/ui/styles/base.css` 负责基础样式。共享界面消费 `background`、`foreground`、`muted-foreground`、`border`、`destructive` 与排版语义类，不复制颜色数值。

## 共享组件

| 组件 | Anatomy | 允许的 variants | 必备状态 | 源码或 story |
|---|---|---|---|---|
| 停止确认 | 标题、范围说明、连带复选框、保留与停止按钮 | 当前对象、关联对象 | 默认未勾选；提交时禁用；成功关闭；失败保留 | `packages/views/common/stop-work-dialog.tsx` |
| 已转任务入口 | 简短说明、任务链接 | 聊天页、聊天窗口 | 转换完成且刷新后仍可见 | `packages/views/chat/components/converted-chat-notice.tsx` |

## 交互模式

| Pattern | 触发条件 | 组合方式 | 不变量 | 证据 |
|---|---|---|---|---|
| 停止执行 | 任务或聊天有排队、运行中的执行 | 停止入口与共享确认弹窗 | 默认只停当前对象；连带范围由同一服务端接口校验 | `packages/views/issues/components/execution-log-section.tsx`、`packages/views/chat/components/chat-input.tsx` |
| 转换后前往任务 | 聊天已有 converted_issue_id | 任务链接替换消息输入区 | 链接由 NavigationAdapter 生成 | `packages/views/chat/chat-page.tsx`、`packages/views/chat/components/chat-window.tsx` |

## 页面族

| Page family | 页面开头 anatomy | 必备 slots | 可选 slots | 响应式行为 |
|---|---|---|---|---|
| 任务详情 | 返回、任务标题、属性 | 动态、执行记录 | 子单、附件 | 手机复用共享返回栈和安全区 |
| 聊天 | 返回、会话标题、消息 | 消息区、输入区或已转任务入口 | 排队执行、侧栏 | 窄屏收起侧栏，内容不横向溢出 |

## Route / Surface 契约

| Route 或 surface | Family | 首屏目的 | 开头 slots | 允许的差异 | Fixture |
|---|---|---|---|---|---|
| 工作区任务详情 | 任务详情 | 查看任务并控制执行 | 标题、属性、动态 | Desktop 使用共享页面或覆盖层 | `execution-log-section.test.tsx` |
| 工作区聊天页及聊天窗口 | 聊天 | 继续聊天或前往已转换任务 | 会话标题、消息区 | 窗口保留现有关闭入口 | `chat-input.test.tsx`、`chat-thread-list.test.tsx` |

## 例外与变更协议

手机网页返回栈、安全区和触控规则以 `docs/adr/0005-mobile-web-shell.md` 为准。原生手机使用系统 Alert 提供当前与连带停止选项。跨页面规则变化时同步修改此文件与共享实现。

## 验证矩阵

| Surface | Viewport | 状态 | 命令或 fixture | 证据路径 |
|---|---:|---|---|---|
| 任务停止确认、聊天停止确认、已转任务入口 | 375 / 390 / 768 / 1280 | 默认、连带勾选、转为任务 | 本地 managed 环境的隔离数据 | `docs/evidence/INDEX.md` 中指向的交付附件 |
| 共享界面 | 当前 TypeScript 契约 | 停止、发送、列表 | `pnpm --filter @multica/views typecheck`；针对性 vitest | 测试输出 |
| 原生手机 | 当前 TypeScript 契约 | 停止选项、转为任务、发送模式 | `pnpm --filter @multica/mobile typecheck` | 测试输出 |
