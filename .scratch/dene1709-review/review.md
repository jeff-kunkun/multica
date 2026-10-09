验收不通过：手机任务返回仍有明确的兼容性缺口，PR #607 暂不合并，请原执行席修复后重新提交验收。

## Standards
未发现本轮需要阻断的仓库标准问题（0 项）。

## Spec
**[P1 / F1] 浏览器没有 Navigation API 时，聊天 → 任务 → 返回必定跳到任务列表。**

位置：`packages/views/issues/components/issue-detail.tsx:3554`（本次新增的手机 fallback），调用链为 `BreadcrumbBackButton` → `useBackOrReplace` → Web adapter 的 `canGoBackInApp`。

目标第 4 条要求有来源历史时回原页面，只有无历史时才回任务列表。实际 `apps/web/platform/in-app-history.ts:38` 在 `window.navigation` 不存在时无条件返回 false，`packages/views/navigation/use-back-or-replace.ts:37` 随即 replace 到列表；已有聊天历史也不会被识别。PR 自身已声明部分 Safari 不满足这个行为，该限制没有经过用户同意。新增 issue-detail 测试 mock 了 useBackOrReplace，只证明传入列表路径，不能证明返回原聊天。已检查调用链，无其他兼容回退保护。

关闭条件：在没有 Navigation API 的浏览器上也能可靠识别本应用内已提交的来源历史；390 宽下从同一聊天进入任务后返回原聊天，直接打开任务仍回任务列表且不退出应用。补充覆盖这两个场景的回归测试，并提供对应真实浏览器路径证据。不要仅用 history.length 判定站内历史，也不要只统计 router.push 调用次数。

## 验证与边界
比较 `git diff 79903f485b6821649a7f9e593fffc79ae567ebc8...31e66f5fe0dd593b411b7b4ea5fb602b02731411`。

本轮执行 `pnpm install --frozen-lockfile` 后，8 个文件共 239 个测试通过：Web 的 in-app-history/navigation/proxy 48 项，views 的 use-back-or-replace/mobile-tab-bar/issue-detail/create-issue 187 项，core landing 4 项。现有测试明确验证缺失 Navigation API 会返回 false、随后 replace 列表，结合调用链即可证明 F1。提交差异 `git diff origin/kun...HEAD --check` 通过。

未重跑全量 typecheck/test，也未重新进行浏览器验收；提交者提供了 Chrome 截图和路径说明，全量测试 7 个失败属于基线的说法本轮没有独立核实，不将其另列阻断。工作区原有 AGENTS.md 运行时改动未修改或提交。

Jev review-finding：按技能指定的两处定位均缺失脚本，工具未能启动；results/confidence/usage 不可得，依技能回落为当前验收席人工判断，F1 为阻断。

Standards：0 项；Spec：1 项，最高 P1。

要你做的：不用你做什么，平台将叫醒原执行席继续修复。
