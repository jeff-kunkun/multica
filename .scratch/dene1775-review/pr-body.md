Closes DENE-1775

## 做了什么
Desktop 标签栏默认按项目自动分组：
- 聊天跟会话的项目，任务跟任务的项目，项目页就是该项目；收件箱、设置等没项目的页排在所有组之后。
- 组标签显示项目名和项目图标，颜色按项目 id 固定（重启不变）；组内标签去掉重复的「项目名 · 」前缀。
- 排序：固定标签 → 项目组 → 无项目标签。新项目的组排在已有组之后，同项目新标签进组尾。组和组内标签都能拖，顺序持久化。
- 右键菜单新增 Group tabs（By project / Manually，默认按项目）和 Sort by project（组按名字排，组内按打开先后）。
- 手动优先：用户拖进/拖出组、新建组、移出组的标签打上锁，自动分组不再移动它；手建组和项目组共存。
- 当前标签所在的组不能折叠（菜单置灰并说明原因）。
- 持久化升到 v7，迁移把老用户已有的手建组上锁，默认切到按项目分组。

## 三面齐
这是 Desktop 本地显示偏好，只存在桌面端的标签状态里：**无服务端、无 CLI**（Agent 不操作桌面标签栏）。
手机端：手机网页和 `apps/mobile` 都没有标签栏，本次无改动。

## 验证
- `pnpm --filter @multica/desktop typecheck`、`lint`（0 error；1 个 warning 是 `tab-content.tsx` 里已有的）
- `apps/desktop` 全量 vitest：76 个文件、901 个用例通过（新增分组、排序、手动优先、迁移、组标签、菜单相关用例）
- 实机 Desktop（Electron dev + 本地后端，3 个项目）：自动分组截图、重启前后顺序/颜色/折叠一致、切手动后新标签不进组、切回按项目后归组
- 预览稿与截图：`docs/evidence/DENE-1775/`

![分组](https://github.com/jeff-kunkun/multica/blob/agent/agent/dene-1775/docs/evidence/DENE-1775/desktop-project-groups.png?raw=1)

🤖 Generated with [Claude Code](https://claude.com/claude-code)

## 验收结论
暂不合并：长项目名（超过 80 字）同步无法收敛；详见 DENE-1775 验收评论。现有相关测试 142/142 通过，新增幂等复现失败。Jev 脚本未分发，退出 2；独立审查判定阻断，无 confidence/usage。

## 知识审计
无够格知识：本轮定位局部同步缺陷，不改变稳定项目规则。
