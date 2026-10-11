验收不通过，PR #621 暂不合并。常规分组已做成，但发现长项目名会使自动同步无法收敛，交回孙悟空修复。

## Standards
未发现阻断项。改动限定 Desktop，项目归属数据从 Query 获取，分组偏好及顺序保存在 Desktop store；服务端、CLI 和手机端豁免已写进 PR。

## Spec
1. **[P2，阻断] 长项目名导致持续同步更新。** `apps/desktop/src/renderer/src/stores/tab-store.ts:1906-1910` 比较完整 `title` 与 `candidate.name`，写入却截成 `title.slice(0, 80)`。将已有项目改成 81 字名称后，相同输入永远被识别为变化。服务端项目创建/更新没有 80 字限制。`use-tab-project-sync.ts:29-32,102` 又把 tabs 派生的 subjects 作为 effect 依赖，每次同步的 map 生成新 tabs，再次触发 effect，形成反馈循环。

实际补测输出（exit 1）：
```text
{"nameLength":80,"sameWorkspace":false,"sameTabs":false}
FAIL project title sync converges after renaming to an 81-character title
AssertionError: expected { Object (tabs, activeTabId, ...) } to be { Object (tabs, activeTabId, ...) }
Received: serializes to the same string
Test Files 1 failed (1)
Tests 1 failed (1)
```
这是 store 层实测及 hook 调用链证明；本轮没有声称在 Desktop GUI 中复现崩溃。

**关闭条件**：比较和存储使用一致的名字规范；补长名称改名、持久化恢复后的同步幂等回归，以及 hook 不重复触发更新的验证。重跑相关测试后提交修复候选。附件是复现测试，放到 `apps/desktop/src/renderer/src/stores/dene1775-review.test.ts`，运行 `pnpm --filter @multica/desktop exec vitest run src/renderer/src/stores/dene1775-review.test.ts`。

本轮验证：`pnpm install --frozen-lockfile` 成功；现有 tab-store/tab-bar 两个测试文件 **142/142 通过**；`git diff origin/kun...HEAD --check` 通过。已查看提交内三个项目分组及重启截图；未重跑整个 Desktop 测试集、typecheck 或 GUI。审查固定点 `e2bbd251e746eef46a8a3be4f7876d8c2c75ebdf`，候选 `98dd216c782809c73fec7b483467adecfe4764aa`。

Jev finding 复核：按 skill 定位后脚本不存在，调用退出 2（can't open file），依技能的缺失回落规则独立判定；无模型 results/confidence/usage。

## 知识审计
无够格知识：本轮只验收及定位局部缺陷，不变更项目边界、术语或操作约定；未改实现与项目记忆。原有运行时 AGENTS.md 工作区差异保持不动。

Standards：0 项；Spec：1 项，最严重 P2 阻断。

要你做的：不用你做什么，修复由实现席继续。
