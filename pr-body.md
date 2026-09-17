## 问题

自托管实例的「添加电脑」弹窗打印的是上游安装命令
（`raw.githubusercontent.com/multica-ai/multica/main/scripts/install.sh`），
所以每台按它接入的机器装到的都是官方 CLI，而不是这个 fork。落地页、
onboarding 步骤、`install.sh` / `install.ps1`、CLI 自更新、Desktop 的托管
CLI 修复路径和文档，全都各自写死了同一个上游来源。

再往下一层，这个 fork 根本没有 CLI 可装：产出 CLI 归档的 GoReleaser 在
`release.yml` 的 `release` job 里，而那个 job 因为要发 `multica-ai/homebrew-tap`
被 `github.repository_owner == 'multica-ai'` 挡住了。所以 fork 的 release 只有
Desktop 安装包，光把 URL 改指过来只会下载 404。

## 改动

- `scripts/build-cli-archives.sh` + `.github/workflows/cli-release.yml`：
  在本 fork 发布 CLI 归档，名字与 GoReleaser 完全一致
  （`multica-cli-<version>-<os>-<arch>`、旧名 `multica_<os>_<arch>`、
  `checksums.txt`），这样 `multica update` 和 Desktop bootstrap 仍能按名字取到资产。
  workflow 逐个核对资产的 `state=uploaded` 与字节数，跟 `desktop-release.yml` 一致。
- `install.sh` / `install.ps1`：仓库来源改由 `MULTICA_REPO` 决定，默认本 fork；
  fork 下完全跳过 Homebrew（`multica-ai/tap` 只有上游构建）。
- 三处 UI 里重复的安装一行命令改读同一个常量
  `@multica/core/constants/distribution`。
- `multica update` 从 fork 读 release（可用 `MULTICA_RELEASE_REPO` 覆盖）；
  Desktop 托管 CLI bootstrap 同步改指 fork。
- 文档与 README 不再教人装上游 installer 和上游 tap。

## 验证

```
bash scripts/install.test.sh            # 通过，新增 fork 默认用例
bash scripts/selfhost-config.test.sh    # 通过
go test ./internal/cli/ -run TestReleaseRepo   # 通过
pnpm --filter @multica/views exec vitest run runtimes/components/connect-remote-dialog.test.tsx  # 通过
pnpm --filter @multica/desktop test     # 723 通过
pnpm --filter @multica/web build        # 通过
bash scripts/build-cli-archives.sh --version v0.4.60-test --out .scratch-dist \
  --targets "darwin/arm64 windows/amd64"   # 归档与 checksums.txt 产出正常
```

新增的 `install.sh` 用例是真守门：把默认仓库改回上游后该用例失败
（会走到 Homebrew）。本地实际解出 `multica-cli-0.4.60-test-darwin-arm64.tar.gz`
里的二进制并运行 `multica version`，输出 `0.4.60-test`。

`packages/views` 的 `modals/align-create-issue.test.tsx` 与 `apps/web` 的
`app/text-contrast.test.ts` 在 `kun` 基线上就是失败的，与本 PR 无关（已对照验证）。

Closes DENE-420

🤖 Generated with [Claude Code](https://claude.com/claude-code)
