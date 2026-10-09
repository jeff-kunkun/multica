Closes DENE-1709

## 做了什么

- **识别手机**：窄屏（< 768px）且触屏（`pointer: coarse`）才算手机，不看 UA；平板、桌面、桌面窄窗口行为不变。
- **底部导航**：聊天 / 任务 / 新建（居中，打开现有新建弹窗）/ 收件箱 / 更多（打开原侧栏）。iOS 安全区留白、每格 ≥ 44px、聊天和收件箱带未读数、输入框获得焦点时让出位置；手机上不再显示悬浮聊天按钮。
- **默认落点**：手机登录后 / 打开工作区进聊天，桌面仍进任务。根路径改为先到 `/{slug}`，由浏览器判断后替换到落点页；登录后跳转、建工作区、接受邀请、切换工作区都走同一个 `workspaceLandingPath`。
- **任务页返回**：手机上任务详情左上角总有返回；从聊天进来按历史退回聊天，没有历史退到任务列表。
- **顺手修**：手机上「手动新建」弹窗固定 384px 高，属性和底栏换行后「创建任务」按钮被挤出卡片看不见；改为手机上按内容高度、最多 85% 屏高，`sm` 以上不变。
- 新文案 5 种语言齐全。

## 截图（真实浏览器）

预览稿：`docs/evidence/DENE-1709/preview.html`（含手机版）

| 打开即进聊天 | 聊天点任务卡 | 任务页返回 | 新建派给智能体 | 打字时让位 |
| --- | --- | --- | --- | --- |
| <img src="https://raw.githubusercontent.com/jeff-kunkun/multica/agent/agent/dene-1709/docs/evidence/DENE-1709/390-landing-chat.png" width="160"> | <img src="https://raw.githubusercontent.com/jeff-kunkun/multica/agent/agent/dene-1709/docs/evidence/DENE-1709/390-chat-session.png" width="160"> | <img src="https://raw.githubusercontent.com/jeff-kunkun/multica/agent/agent/dene-1709/docs/evidence/DENE-1709/390-issue-detail.png" width="160"> | <img src="https://raw.githubusercontent.com/jeff-kunkun/multica/agent/agent/dene-1709/docs/evidence/DENE-1709/390-new-issue-assigned.png" width="160"> | <img src="https://raw.githubusercontent.com/jeff-kunkun/multica/agent/agent/dene-1709/docs/evidence/DENE-1709/390-composer-focused.png" width="160"> |

768 / 1280 不变：

<img src="https://raw.githubusercontent.com/jeff-kunkun/multica/agent/agent/dene-1709/docs/evidence/DENE-1709/768-issues.png" width="380"> <img src="https://raw.githubusercontent.com/jeff-kunkun/multica/agent/agent/dene-1709/docs/evidence/DENE-1709/1280-issues.png" width="480">

Playwright（Chrome，390×844 触屏 / 768 / 1280）走通：
- 390：`/` → 聊天；聊天 → 任务卡 → 返回，回到同一聊天 URL；输入框聚焦时底栏消失；「新建」→ 手动 → 创建，库里 `assignee_type=agent` 且为 Wukong，创建按钮在卡片内。
- 768 / 1280：`/` → 任务，无底栏。

## 验证

- `pnpm typecheck --force`：全绿
- `pnpm lint`：0 error
- `pnpm test`：除 `projects/components/project-detail.test.tsx`、`settings/components/billing-tab.test.tsx` 共 7 例外全绿，这两个文件本 PR 未触碰，失败与本改动无关
- 针对性单测：`paths/landing.test.ts`、`layout/mobile-tab-bar.test.tsx`、`issue-detail.test.tsx`（手机有返回且兜底到任务列表 / 桌面窄窗口无返回）、`create-issue.test.tsx`（手机弹窗高度）、`apps/web/proxy.test.ts`

## 三面齐 / 手机端

- 纯前端导航与落点，不涉及服务端规则，也没有 Agent 需要操作的东西，故不改服务端与 CLI。
- **`apps/mobile` 未改**：按 Kun 的决定只做手机网页，原生 App 不跟。
- iOS 26.2 以前的 Safari 不支持 Navigation API 的 `canGoBack`，从聊天进任务页再返回会落到任务列表而不是聊天。

🤖 Generated with [Claude Code](https://claude.com/claude-code)


## 终审（孙悟饭，2026-10-09）

暂不通过：F1（P1）——手机任务页新返回入口复用了只依赖 Navigation API 的历史判断；API 缺失时，即使从聊天进入也会 replace 到任务列表，违反目标第 4 条。需补充可靠的兼容处理、无 Navigation API 的来源返回与直接打开回退测试，以及真实浏览器证据。详见 DENE-1709 验收评论。

本轮针对性测试 8 文件 / 239 项通过；提交差异检查通过。未重跑全量检查或浏览器验收。Standards 0 项，Spec 1 项（P1）。

Jev review-finding：两处指定脚本路径均不存在，未启动；results、confidence、usage 不可得。按技能回落人工审查，F1 判定 blocking。
