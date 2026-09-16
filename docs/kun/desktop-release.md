# Desktop 发版手册（kun 魔改线）

发一版 Desktop 给 kk zi 真机用，要走完的全流程与红线。写这份文档的直接原因有两轮真实踩坑：DENE-276（打包只花 7 分钟，但产物躺在本地硬盘上没发出去，用户看到的是「一个小时没动静」）和 DENE-353（v0.4.56 / v0.4.57 的 macOS 资产**从来没上传成功**，`latest-mac.yml` 指向的 dmg 是 404，自动更新链路断了）。

**从 DENE-353 起，macOS 不再手工打包上传**：tag 一推，`.github/workflows/release.yml` 的 `desktop` job 在 GitHub runner 上打 mac / linux / windows 并上传。本机出站带宽只有 90 KB/s 左右，一个 231 MB 的 dmg 根本传不完，这是搬进 CI 的唯一原因。

## 正常路径（CI 自动，首选）

```bash
# 1. 对齐基线：tag 必须打在当下的 kun tip 上
git fetch origin --prune
git rev-parse --short origin/kun        # 记下这个 sha，它要进评论
# 2. 打 tag：上一版 patch +1
git tag v0.4.58 <origin/kun>
git push origin v0.4.58
```

推送 tag 后 `Release` workflow 依次跑：

| job | 做什么 |
| --- | --- |
| `verify` | Go 测试 + govulncheck（上游线才走 goreleaser，见下） |
| `release` | GoReleaser，**只在 `multica-ai/multica` 跑**（要 Homebrew tap token）；私有线上是 skipped |
| `desktop` | linux / windows / macos 三个矩阵项：先确保本 tag 的 GitHub Release 存在，再 `node scripts/package.mjs --<target> <archs> --publish always` 打包并上传，最后逐个核对资产 |

私有线上 Release 由 `desktop` job 自己在 `gh release create` 里建（`--generate-notes`），因为 goreleaser 不在这条线上跑。macOS 只出 arm64：真机是 Apple Silicon，装着的客户端读的是 `latest-mac.yml`，Intel 包走另一个 feed（`publish.channel=latest-x64`），按需再加。

## 红线（违反其中任何一条，这一版等于没发）

1. **产物没进 Release = 没有发布，而且不能靠 `gh` 的退出码判断。** 上传断线时 `gh release upload` / electron-builder 仍以成功退出，v0.4.56 / v0.4.57 就是这么「说齐了其实没齐」的。收尾必须核资产（见下）。
2. **一次 run 内跑完 tag → 等 CI → 核资产 → 评论。** run 退出时所有未完成的工作都会丢失，没有后台唤醒。不要把「等构建完再发」留到下一轮。
3. **构建基线必须是当下的 `kun` tip，且构建结束后要复核 tip 有没有前进。** DENE-276 的第一次构建出自 `b4865a77d`，落后 tip 4 个 commit，其中 `da4bc9bbc`(DENE-289) 恰好重写了导出路径 302 行——照那份产物发出去，用户重测会再次踩到同一个缺陷。
4. **不在 Release 说明、评论、产物里写任何 token / 凭据 / 环境变量值。**
5. **版本号只由 tag 决定。** `apps/desktop/scripts/package.mjs` 从 `git describe --tags --match 'v[0-9]*'` 推导版本，与 GoReleaser 给 CLI 的 `main.version` 同源。不要手改 `apps/desktop/package.json` 的 `version`。
6. **缺失资产的旧版不回补 tag，直接出下一版。** 理由：回补要删/挪已经推出去的 tag，已安装的客户端和 Release 说明都会被改写；而缺资产的版本本身没有任何东西可救——新版一旦发齐，`latest-mac.yml` 就指向新版，自动更新立刻恢复。DENE-353 的 v0.4.56 / v0.4.57 就是这么处理的。

## 发布后核资产（不许跳过）

CI 的 `desktop` job 每个矩阵项都会在打包后自动跑一次；手工上传时也必须自己跑同一个脚本：

```bash
# 在 apps/desktop 下，产物在 dist/
GH_TOKEN=$(gh auth token) node scripts/verify-release-assets.mjs \
  --repo jeff-kunkun/multica --tag v0.4.58 --dist dist \
  --require latest-mac.yml
```

它读 `GET /repos/{owner}/{repo}/releases/tags/{tag}`，对**本次构建出来的每一个产物**断言：存在于 Release、`state == "uploaded"`（`starter` 就是断线的残骸）、`size` 与本地产物逐字节一致；再断言指定的更新 feed 在 Release 上。任一条不符就退出码 1 并逐条打印缺哪个。`--response-file <path>` 可以用一份抓下来的 API 响应离线复核（单测就是这么喂假响应的）。

注意 `--require` 只认 electron-builder 实际会写的 feed 名：mac `latest-mac.yml`、linux `latest-linux.yml`、windows `latest.yml`（arm64 的 windows / linux 会另有 `latest-arm64.yml` / `latest-linux-arm64.yml`）。这些名字由 electron-builder 的 `${channel}${osSuffix}${archPrefix}.yml` 决定，改 channel 配置就得同步改。

## 手工路径（仅应急 / 本机 smoke）

CI 挂到没法发版时才走，且第 3 步不许省。这条路上还有两条 DENE-329 用两轮失败换来的纪律，CI 路径不受影响，手工路径必须守：

- **构建一出炉就把产物挪出 worktree**（`cp` 到 `~/multica-releases/<tag>/` 再开始上传）。run 结束时 worktree 会被回收，产物跟着一起没——DENE-329 连栽两次都是这么丢的。
- **先传两个大产物，最后传 `latest-mac.yml`。** 清单先上去而产物没传完，等于对所有已装客户端广播一个指向 404 的自动更新地址，比不发版更糟。顺序错了就先把清单删掉，传完产物再补。

本机上传很慢：走代理到 `uploads.github.com` 实测单连接只有 ~110 KB/s，约 450 MB 的 dmg+zip 要 40–60 分钟，并行推两个文件能快近一倍。`gh release upload` 卡住时不报错也没有进度，用 `nettop -P -p <curl-pid> -l 1 -J bytes_out` 看真实字节数判断它是慢还是死了。**这就是 DENE-353 把 macOS 搬进 CI 的原因——能走 CI 就别走这里。**

```bash
# 1. 依赖（pnpm store 暖的话是分钟级，冷装十几分钟起）
pnpm install --frozen-lockfile

# 2. 本机打包：不签名、不公证，产物级别与 CI 出的包一致
CSC_IDENTITY_AUTO_DISCOVERY=false pnpm --filter @multica/desktop package -- --mac --arm64 --publish never

# 3. 上传后立刻核资产（用上面的脚本），再贴下载入口
gh release upload v0.4.58 <dmg> <zip> <两个 .blockmap> <latest-mac.yml>
```

`latest-mac.yml` 与两个 `.blockmap` 不是可选项——缺了自动更新链路就是断的。mac arm64 一版共 **5 个资产**。

不要为了「干净」对已有 checkout 用 `multica repo checkout --fresh`：那会丢掉 pnpm store 与 Electron/Go 缓存，把几分钟的活变回一小时。本机打包时 `--publish never` 也不能省，否则会把本机重打的包覆盖到已发布的 Release 上。

## 本机打包的沙箱与缓存（macOS）

- Electron / Go 的默认缓存目录在工作区沙箱外，会被拒写。把缓存目录指到**工作区内**再构建，后续发版沿用同一套路径即可复用。
- `hdiutil` 建 DMG 会被 workspace-write 沙箱拒绝，需要一次提权构建。
- 这两点都不需要改 harness 配置或加 runtime 权限。

## 产物级自检（证明修复真的进了包）

核资产只证明「文件传上去了」，不证明「这个包里有本版要修的东西」。DENE-276 就是产物内容对不上：

```bash
# tag 真的指向构建用的那个 commit，且没有回退上一版
git ls-remote origin refs/tags/v0.4.58
git merge-base --is-ancestor <fix-sha> v0.4.58; echo exit=$?   # 期望 0
git merge-base --is-ancestor v0.4.57 v0.4.58; echo exit=$?     # 期望 0

# 装出来的 app 自报版本（内置 CLI 随 asarUnpack 落在 app.asar.unpacked 下，
# 路径随打包配置变，别硬编码）
CLI=$(find /Applications/Multica.app -type f -name multica -perm -u+x | head -1)
"$CLI" --version
plutil -extract CFBundleShortVersionString raw /Applications/Multica.app/Contents/Info.plist
```

再加一条产物级（不是源码级）的证据：在打包出来的内置 CLI 二进制里 grep 本次修复引入的标记字串。例：DENE-274 的 `read_api_missing`、`resolve source workspace: %w`；DENE-289 的 `list_has_more`、`list_shape_unknown`。

## 交付评论该写什么

顺序固定，用户只读前两屏：

1. **下载入口**（DMG / ZIP 直链），并说明是否 draft；
2. **本版修了什么**：一行一个 commit sha + 票号 + 一句人话；
3. **核资产的实际输出**：`verify-release-assets` 的逐条 `ok` 行，而不是「已发布」；
4. **产物包含修复的证据**：上面产物级自检的实际输出；
5. **用户要重跑哪一步**：编号步骤，写清判定标准（例：「先看包体积，还是 KB 级就别导入，直接回我」）；
6. **未决风险 / 边界**：本版没改什么、哪些算待人工测试。

Gatekeeper 提示要写进步骤里：mac 包是 ad-hoc 签名、未公证（CI 与手工路径都一样），首次打开需在「系统设置 → 隐私与安全性」放行，或 `xattr -cr /Applications/Multica.app`。Developer ID 签名 + 公证是独立的后续项，需要 Apple 凭据进 CI secret，本仓现在没有。
