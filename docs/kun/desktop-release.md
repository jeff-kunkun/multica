# Desktop 发版手册（kun 魔改线）

发一版 Desktop 给 kk zi 真机用，要走完的全流程与红线。写这份文档的直接原因是 DENE-276：那一轮打包实际只花 7 分钟，却在用户那边表现为「一个小时没动静」，最后产物躺在本地硬盘上没有发出去。下面每条纪律都对应一次真实踩坑。

## 红线（违反其中任何一条，这一版等于没发）

1. **产物留在本地硬盘 = 没有发布。** 一次 run 必须把 tag 推送、Release 建好、下载链接贴回票里；只贴「构建成功」而没有下载入口，用户拿不到任何东西。
2. **一次 run 内跑完 tag → build → release → 评论。** run 退出时所有未完成的工作都会丢失，没有后台唤醒。不要把「等构建完再发」留到下一轮。
3. **构建基线必须是当下的 `kun` tip，且构建结束后要复核 tip 有没有前进。** DENE-276 的第一次构建出自 `b4865a77d`，落后 tip 4 个 commit，其中 `da4bc9bbc`(DENE-289) 恰好重写了导出路径 302 行——照那份产物发出去，用户重测会再次踩到同一个缺陷。
4. **不在 Release 说明、评论、产物里写任何 token / 凭据 / 环境变量值。**
5. **版本号只由 tag 决定。** `apps/desktop/scripts/package.mjs` 从 `git describe --tags --match 'v[0-9]*'` 推导版本，与 GoReleaser 给 CLI 的 `main.version` 同源。不要手改 `apps/desktop/package.json` 的 `version`。
6. **构建一出炉就把产物挪出 worktree。** run 结束时 worktree 会被回收，产物跟着一起没。DENE-329 连栽两次：两轮都成功构建出 DMG/ZIP，都因为 run 在上传途中终止而被清理掉，Release 上只剩 `latest-mac.yml` 和 blockmap。先 `cp` 到 `~/multica-releases/<tag>/`，再开始上传。
7. **先传两个大产物，最后传 `latest-mac.yml`。** 清单先上去而产物没传完，等于对着所有已装客户端广播一个指向 404 的自动更新地址——比不发版更糟。顺序错了就先把清单删掉，传完产物再补。

## 时间预期（先说，别让用户干等）

| 阶段 | 冷启动 | 复用缓存 |
| --- | --- | --- |
| `pnpm install --frozen-lockfile`（11 个 workspace 包，store 全冷） | ~70 分钟 | 数分钟 |
| `pnpm --filter @multica/desktop package` 实际打包 | ~7 分钟 | ~6 分钟 |
| 上传 DMG + ZIP 到 GitHub Release（约 450 MB） | 40–60 分钟 | 40–60 分钟 |

上传不会因为缓存变快：走代理到 uploads.github.com 实测单连接只有 ~110 KB/s，两个大产物并行推才勉强到 ~200 KB/s。并行推两个文件比串行快近一倍，**一定要并行**。`gh release upload` 卡住时不会报错也不会有进度，用 `nettop -P -p <curl-pid> -l 1 -J bytes_out` 看真实字节数，别靠感觉判断它是慢还是死了。

绝大部分等待时间在装依赖，不是构建。**开工第一条评论就要把这个预期说出去**，否则用户看到的就是「打包了一个小时没打包好」。

不要为了「干净」对已有 checkout 用 `multica repo checkout --fresh`：那会丢掉 pnpm store 与 Electron/Go 缓存，把 6 分钟的活变回 70 分钟。

## 沙箱与缓存（macOS，已验证可绕过，不需要额外权限）

- Electron / Go 的默认缓存目录在工作区沙箱外，会被拒写。把缓存目录指到**工作区内**再构建，后续发版沿用同一套路径即可复用。
- `hdiutil` 建 DMG 会被 workspace-write 沙箱拒绝，需要一次提权构建。
- 这两点都不需要改 harness 配置或加 runtime 权限。

## 流程

```bash
# 1. 对齐基线
git fetch origin --prune
git checkout kun && git merge --ff-only origin/kun
git rev-parse --short HEAD          # 记下这个 sha，它要进评论

# 2. 依赖（冷装很慢，见上表）
pnpm install --frozen-lockfile

# 3. 打 tag：上一版 patch +1
git tag v0.4.55 && git push origin v0.4.55

# 4. 构建（ad-hoc 签名，未公证）
CSC_IDENTITY_AUTO_DISCOVERY=false pnpm --filter @multica/desktop package -- --mac --arm64

# 5. 建 Release 并上传全部资产
gh release create v0.4.55 --repo jeff-kunkun/multica --title v0.4.55 --notes-file <notes>
gh release upload v0.4.55 <dmg> <zip> <两个 .blockmap> <latest-mac.yml>
```

`latest-mac.yml` 与两个 `.blockmap` 不是可选项——缺了自动更新链路就是断的。mac arm64 一版共 **5 个资产**。

## 发布后自检（每条都要贴进票里，而不是只说"已发布"）

```bash
# Release 不是 draft，tag 真的指向构建用的那个 commit
gh release view v0.4.55 --repo jeff-kunkun/multica --json isDraft,tagName,publishedAt
git ls-remote origin refs/tags/v0.4.55

# 本版声称修的 commit 确实在里面
git merge-base --is-ancestor <fix-sha> v0.4.55; echo exit=$?   # 期望 0

# 没有回退上一版的任何功能
git merge-base --is-ancestor v0.4.54 v0.4.55; echo exit=$?     # 期望 0

# 产物级证据：验装出来的 app，不是看源码
#（内置 CLI 随 asarUnpack 落在 Contents/Resources/app.asar.unpacked/resources/bin/ 下，
#  路径随打包配置变，别硬编码，find 一下）
CLI=$(find /Applications/Multica.app -type f -name multica -perm -u+x | head -1)
"$CLI" --version                    # 版本 + commit 应与上面一致
plutil -extract CFBundleShortVersionString raw /Applications/Multica.app/Contents/Info.plist
```

再加一条**产物级**（不是源码级）的证据：在打包出来的内置 CLI 二进制里 grep 本次修复引入的标记字串，证明修复真的进了包。例：DENE-274 的 `read_api_missing`、`resolve source workspace: %w`；DENE-289 的 `list_has_more`、`list_shape_unknown`。

最后核对本地产物与 Release 资产的 sha256 / digest 一致。

## 交付评论该写什么

顺序固定，用户只读前两屏：

1. **下载入口**（DMG / ZIP 直链），并说明是否 draft；
2. **本版修了什么**：一行一个 commit sha + 票号 + 一句人话；
3. **产物包含修复的证据**：上面自检的实际输出；
4. **用户要重跑哪一步**：编号步骤，写清判定标准（例：「先看包体积，还是 KB 级就别导入，直接回我」）；
5. **未决风险 / 边界**：本版没改什么、哪些算待人工测试。

Gatekeeper 提示要写进步骤里：本包 ad-hoc 签名未公证，首次打开需在「系统设置 → 隐私与安全性」放行，或 `xattr -cr /Applications/Multica.app`。
