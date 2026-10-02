聊天页项目栏「更多 (N)」的数量包含了侧栏置顶里没排上标签条的项目，但弹窗的「收起的项目」只列未置顶的，导致「更多 (6)」只看到 3 个，侧栏新置顶的 game-multica / bnb contract / game-relay 看起来没同步到聊天页。

- 「收起的项目」现在列出所有在「更多」后面的项目，置顶的排前面（可直接取消置顶，不可拖动）
- 「置顶」区仍是侧栏完整置顶列表，拖动排序不变
- 数据源未改：DENE-1121 (#462) 起三处已共用服务端置顶

三面齐：纯前端展示修正，服务端与 CLI 无变化。

验证：`vitest run chat/components/chat-project-bar.test.tsx` 7/7 通过（新增用例在回退修复时失败）；`pnpm --filter @multica/views typecheck` 通过。

🤖 Generated with [Claude Code](https://claude.com/claude-code)
