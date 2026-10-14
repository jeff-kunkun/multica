# jeff-kunkun / multica 魔改分支

这是 [jeff-kunkun/multica](https://github.com/jeff-kunkun/multica) 的魔改主线，fork 自官方 [multica-ai/multica](https://github.com/multica-ai/multica)。

本地工作副本：`~/.agents/multica`。

## 远端

- `origin` = https://github.com/jeff-kunkun/multica（我们的 fork）
- `upstream` = https://github.com/multica-ai/multica（官方）

## 分支约定

jeff-kunkun/multica 只留两条长期分支：

| 分支 | 职责 | 谁往里写 |
| --- | --- | --- |
| `kun` | 团队魔改主线（旧办公室 ai.example.com 跟它）。功能分支从这里切出、PR 打回这里。也是 GitHub 默认分支。 | 所有开发 |
| `kun-main` | Kun 自己的主力线（新办公室 office.example.com 跟它），以官方原版为底座做减法，规矩在它自己的 `AGENTS.md`。 | 只走 `kun-main` 的 PR |

- 原来的 `main`（官方镜像）和 `release`（发布线）已删，最后的位置留在 `archive/main`、`archive/release` 两个标签上。
- 永远不要 rebase `kun`，不要 force-push `kun` 或 `kun-main`；两条线互不合并。
- 魔改增量随时可查：`git log --oneline upstream/main..kun`。

## 上游同步流程

每次上游有更新时执行一条命令：

```bash
# 预检 / 模拟运行（不写远端、不开 PR，检查是否有更新与冲突）
bash scripts/sync-upstream.sh --dry-run

# 执行同步（切分支合并官方、检查迁移编号、推分支开 PR）
bash scripts/sync-upstream.sh
```

该命令封装了以下标准流程：

1. `git fetch upstream --prune && git fetch origin --prune`
2. 检查更新：`git log --oneline kun..upstream/main`。为空则提示「上游无更新」并退出。
3. 冲突预检与合并：从 `kun` 切出 `sync/upstream-<日期>` 分支并直接执行 `git merge --no-ff upstream/main`（不再经过本仓库的 `main` 镜像）。
   - 有冲突：脚本自动停下并列出所有冲突文件，执行 `git merge --abort` 保持工作区干净，不自行乱解，等待人工或专门决策介入。
   - 无冲突：自动执行 `make migration-lint` 检查迁移编号是否重复冲突。
4. 推送与开 PR：推送 `sync/upstream-<日期>` 到 `origin`，并调用 `gh pr create --base kun --title "sync: upstream main <日期>"`。Reviewer 通过后直接合并；没有 Reviewer 结论时，Agent 完成按风险自检并认为可接受也可合并，不等待本人再次确认。

### 已提前照搬到 `kun`、上游还没合并的 PR

官方 PR 还开着时我们就先照搬了，上游随后合并会在同一处产生 diff，同步时按这里处理：

- [upstream #8385](https://github.com/multica-ai/multica/pull/8385)（Antigravity 工具事件实时转发，DENE-723）：`server/pkg/agent/antigravity.go` 与官方逐行一致，不会冲突；`server/pkg/agent/antigravity_tools_test.go` 是 add/add 冲突点，取官方版本（官方那个 POSIX sh 版 live fixture 在 macOS 和 Linux 上都成立）。上游合并后删掉这一条。

## DeepSeek Harness

官方已支持 `dsh` 运行时，但桥接包还没上公共 npm（[upstream #6936](https://github.com/multica-ai/multica/issues/6936)）。自托管请用本 fork 的一键脚本：

```bash
bash scripts/setup-dsh-runtime.sh
```

模型能不能读图，由 `~/.dsh/settings.yaml` 的声明决定，而且 pi-ai 路由的键是 `input` 不是 `inputModalities`。用探测脚本实测，不要手写：

```bash
node scripts/dsh-vision-probe.mjs --apply
```

说明、已知坑、给上游的反馈建议见 [docs/kun/dsh-runtime.md](docs/kun/dsh-runtime.md)。

## Desktop 发版：双通道

打包发布给真机用的 Desktop 版本，走 [docs/kun/desktop-release.md](docs/kun/desktop-release.md)：版本号由 tag 推导、产物没推上 Release 就等于没发。

两条发布通道，客户端装上哪条就只收哪条的包：

| 通道 | 从哪个分支打 tag | tag 形状 | 谁装 |
| --- | --- | --- | --- |
| 测试版（test） | `kun` tip | `v0.5.5-test.1` | kun 本机；愿意先吃 bug 的用户 |
| 正式版（stable） | `kun` tip（须已发过测试版） | `v0.5.5` | kk、zi 以及默认用户 |

测试版在 `kun` 上跑稳之后，在同一个 `kun` 提交上打正式 tag——正式版的每一行代码都必须先在测试通道上出现过。用户在「设置 → 更新」里自己选通道，切换立即生效。

## Desktop 默认入口 = 自建实例

本 fork 的 Desktop **入口默认连自建实例**：`~/.multica/desktop.json` 不存在时用 `DEFAULT_ENTRY_RUNTIME_CONFIG`（`apps/desktop/src/shared/runtime-config.ts`，即 `https://ai.example.com`），不再回落官方云。官方云仍然可达——设置 → 服务器里切到「官方云」会**显式写一份** `desktop.json`，而不是删文件（删文件已经等于自建）。想改默认入口只改这一个常量。

## 自建实例自动跟随 `kun`

自建实例（`ai.example.com`）曾经落后 `kun` 70 个提交才被发现。现在用 systemd timer 每 5 分钟比一次 `/health` 自报的 commit 和 `origin/kun`，有漂移就在旁边起新版本、就绪后切 nginx 再停旧版本（DENE-1617），失败就回滚：

```bash
sudo scripts/install-selfhost-autoupdate.sh
```

装、停、查状态、手工回滚见 [docs/kun/selfhost-autoupdate.md](docs/kun/selfhost-autoupdate.md)。

jeff-kunkun/multica 的 Actions 停用期间（2026-10-08 起），镜像和 Linux CLI/守护进程包都由借用仓库 [kkunkunya/multica-image-builder](https://github.com/kkunkunya/multica-image-builder) 出：镜像跟 `kun` tip，CLI 包跟最新 `v*` tag、落在该仓库自己的 Release，服务器用 `MULTICA_RELEASE_REPO=kkunkunya/multica-image-builder multica update` 升级。细节见 [docs/kun/desktop-release.md](docs/kun/desktop-release.md) 的「Linux CLI / 守护进程包」一节。

## 迁移编号

并行分支各自取号会撞号（DENE-730、DENE-763），撞了运行时不报错。`make migration-lint` 是 PR CI 与 release 必经门禁，468–501 的历史双号是冻结白名单、不再扩。取号、改号与门禁细节见 [docs/kun/migration-numbering.md](docs/kun/migration-numbering.md)。

## 边界

- 不向 `multica-ai/multica` 开 PR 或 push。
- 不把任何 token、凭据、环境变量值写进提交、评论或 PR。
- 不删除远端分支、不改 GitHub 默认分支。合并 PR 按上面的审查与自检规则执行，不要求本人再次确认。
