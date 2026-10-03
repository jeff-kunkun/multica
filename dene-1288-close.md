## 验收结论

通过，PR #503 的实现符合 DENE-1288：

- 跟随中的分身行在名称下显示跟随原因，并提供“关闭跟随后可改”入口，链接到该分身的智能体详情；非分身行保持原编辑行为。
- 中英文、法文、日文、韩文文案已补齐；HTML 预览同时覆盖桌面和 390px 手机宽度，手机表格可横向滚动。
- 验收按 Standards / Spec 双轴检查，无阻断发现。

验证：`routing-seats-table.test.tsx` 8/8 通过；`pnpm --filter @multica/views typecheck` 通过；`pnpm --filter @multica/views lint` 通过（仅既有警告）；`git diff --check` 通过；390×844 Chrome 预览实测通过。
