# jeff-kunkun / multica 魔改分支

这是 [jeff-kunkun/multica](https://github.com/jeff-kunkun/multica) 的魔改主线，fork 自官方 [multica-ai/multica](https://github.com/multica-ai/multica)。

本地工作副本：`~/.agents/multica`。

## 远端

- `origin` = https://github.com/jeff-kunkun/multica（我们的 fork）
- `upstream` = https://github.com/multica-ai/multica（官方）

## 分支约定

- `main` 只做官方镜像，永远不直接提交，只 fast-forward 到 `upstream/main`。
- `kun` 是魔改主线，也是 GitHub 上的默认分支。所有功能分支从 `kun` 切出、PR 回 `kun`。
- 永远不要 rebase `kun`，不要 force-push `kun` 或 `main`。
- 魔改增量随时可查：`git log --oneline upstream/main..kun`。

## 上游同步流程

每次上游有更新时执行：

1. `git fetch upstream --prune && git fetch origin --prune`
2. 看有什么新东西：`git log --oneline kun..upstream/main`。为空则「上游无更新」，结束。
3. 先更新镜像：`git checkout main && git merge --ff-only upstream/main && git push origin main`
4. 冲突预检（不碰 kun）：`git checkout -b sync/upstream-$(date +%Y%m%d) kun && git merge --no-ff upstream/main`
   - 有冲突：`git diff --name-only --diff-filter=U` 列出冲突文件，`git merge --abort`，交给人决策，不要自行猜着解冲突。
   - 无冲突：跑构建和测试（Go 与前端各跑一次，命令以仓库 README / Makefile 为准）。
5. 推送同步分支并开 PR：`git push -u origin sync/upstream-<日期>`，`gh pr create --base kun --title "sync: upstream main <日期>"`。PR 必须经 Reviewer 审查，由人合并。

## DeepSeek Harness

官方已支持 `dsh` 运行时，但桥接包还没上公共 npm（[upstream #6936](https://github.com/multica-ai/multica/issues/6936)）。自托管请用本 fork 的一键脚本：

```bash
bash scripts/setup-dsh-runtime.sh
```

说明、已知坑、给上游的反馈建议见 [docs/kun/dsh-runtime.md](docs/kun/dsh-runtime.md)。

## Desktop 发版

打包发布给真机用的 Desktop 版本，走 [docs/kun/desktop-release.md](docs/kun/desktop-release.md)：版本号由 tag 推导、必须从当前 `kun` tip 构建、产物没推上 Release 就等于没发。

## 自建实例自动跟随 `kun`

自建实例（`ai.ferryway.cc`）曾经落后 `kun` 70 个提交才被发现。现在用 systemd timer 每 15 分钟比一次 `/health` 自报的 commit 和 `origin/kun`，有漂移就重建、失败就回滚：

```bash
sudo scripts/install-selfhost-autoupdate.sh
```

装、停、查状态、手工回滚见 [docs/kun/selfhost-autoupdate.md](docs/kun/selfhost-autoupdate.md)。

## 边界

- 不向 `multica-ai/multica` 开 PR 或 push。
- 不把任何 token、凭据、环境变量值写进提交、评论或 PR。
- 不删除远端分支、不改 GitHub 默认分支、不合并 PR；这些需要人确认。
