# DENE-1808 验证记录

实现按任务 2026-10-11 的改向：复用可托管，只向 run 发起人本人的源方聊天代发。旧连通不回写，无迁移。

## 续跑完成块

上轮实现保存在 `09e06ff81c`，中断快照只补回本地证据。此次补齐接收智能体的集成配置：在事务外按发起人与接收智能体加载，在同一消息/任务事务内保存。测试验证落库配置和身份，不调用真实外部集成。

2026-10-11，在脚本创建并清理的隔离 PostgreSQL 数据库执行：

```sh
bash scripts/test-db.sh --quiet -- sh -c 'cd server && go test ./internal/service ./internal/handler -run "TestManagedChat|TestLinkedChatDelivery|TestLinkedDeliveryHandoff" -count=1'
```

结果：service 通过（0.821s），handler 通过（0.856s），命令退出 0。涵盖托管发送/读取、本人聊天目录、来源与外部内容包裹、消息/唤醒事务回滚、接收集成配置。首次新增测试编译时方法名写错，修正后测试 fixture 缺事件总线导致 panic；补齐 fixture 后上述命令通过。生产逻辑未因 fixture 失败改变。

上轮记录：workspacelink 全包与 CLI 聚焦测试、views/mobile typecheck、相关 lint、设置/消息/复制/实时缓存测试通过；未执行仓库全量检查。

路由探针：BASELINE 拒绝聊天目录/发送；MODIFIED 允许目录/发送且拒绝创建聊天；ROLLBACK 恢复原行为与原 SHA-256。它仅证明 managed.go 的路由变化，不是整体部署回滚演练。

## 剩余小块

1. **真实界面验收已补**：2026-10-11 在受管本地环境完成设置与聊天 375/390/768/1280 四档截图和操作验证，见下方 S1 增量记录；原生设备缺口仍保留。
2. **权限终审**：跨工作区身份与外部内容边界交验收席复核，再合并。
3. **发布后验收**：已有运行环境升级完成，但 0.6.3-test.1 不含本票 CLI 参数。合入后等待自建服务自动跟进并核对版本；发新测试版并升级 grok生产服、deepseek生产服，再执行 WD → kunkunya 聊天发送。不得手工部署或改现有连通设置。

原生 App 无连通设置屏；聊天消息列表已同步来源显示。

## S1 增量：真实浏览器（2026-10-11）

- 实际运行候选 `b1892f37882e75c76baa32dc61e02f791e90e77d`，生产实现与审查候选 `a9bd9b3fe5` 完全相同。追加清理提交移除自动保存的 55 个临时文件，保留本地证据，并在 Git 本地 exclude 中排除，未 reset/rebase。
- `make up` 创建环境 `dene_1808_8db8821cdf00-319`；`make status` 核对 API 18399（PID 81167，commit b1892f3788）、Web 13319（PID 81389）归本 worktree。依赖通过 frozen lockfile 安装。
- Ego Lite 独立空间 25；实际 `innerWidth` 为 375、390、768、1280，高 900。浏览器原有 110% 缩放按实际 CSS 视口补偿，使用直接 CDP 截图，未裁剪、未改 DOM。截图逐一打开查看。
- 设置实际路由：`http://localhost:13319/dene-1808-source/settings?tab=workspace-links`。390px 观察默认开，选择本地共享项目与测试目标，关闭后提交成功；另一条保留默认开提交成功。数据库只读核对 `dene-1808-viewer-off` 为 `managed=false`、`dene-1808-viewer-on` 为 `managed=true`。仅创建本地测试连通。
- 聊天实际路由：`http://localhost:13319/dene-1808-source/chat/3e5e5f73-ecb5-4409-9539-2822e8958c78`。本地数据库 `linked_delivery` 夹具经实际服务/组件读取显示；长发起人、工作区和智能体来源完整换行，四档消息 scrollWidth 与 clientWidth 相等（334、348、702、650），页面无横向溢出。
- 390px 从设置打开侧栏、选择聊天，页面跳转且侧栏关闭；聊天详情点击“返回”回到聊天列表。响应式断点切换会重新导航，因此各档重新打开同一实际聊天路由，未以切换中的空白状态充当截图。
- [真实界面 HTML 附件](https://ai.ferryway.cc/api/attachments/01a12b2f-6353-7ad6-a715-6a406036246e/download) 包含原始四档截图、关闭提交截图、返回截图、路由、候选 SHA、数据库结果及截图哈希。附件自身已在离线 375/1280 下打开，展开/收起和键盘操作有效、图片完整，无外部资源；原始元数据见本目录 `actual-ui-manifest.json`。
- 原生缺口：`xcrun simctl list devices booted` 退出 72：`unable to find utility "simctl", not a developer tool or in PATH`。未进行 iOS/Android 真机或模拟器、主题/键盘/跨客户端重连验证。Electron 壳未单独启动；这里是共享 Web/Desktop 组件的真实 Web 路由证据。
- 本轮无生产代码变化，未重复已通过的后端/前端测试，`git diff --check` 针对交付文件验证。浏览器空间已关闭；本地服务通过 `make down` 收尾。

发布后验收仍由执行席孙悟饭承接：合入后核对服务端自动跟进版本，包含本功能的新测试版及两个运行环境就绪后，回填源方目标聊天 ID、发送 JSON（消息 ID/代发来源）、接收任务 ID 与显示/唤醒证据。不手工部署自建服务，不修改已有连通设置。本地视觉夹具没有接收运行环境，不能代表线上投递或叫醒成功。


## 主线对齐与权限复测（2026-10-11）

按 Grok Bot 的补发审查意见，在原交付分支追加合并提交，对齐 `kun`：权限基线 `de44a71f3f`，随后纳入仅文档变化的最新主线 `e70872fba5`。未 rebase、reset 或 force push，保留先前实现、清理和 S1 真实界面证据；补证支线已归类为 rescue / absorbed，交付线不变。

六处冲突保留双方有效内容：CONTEXT 的参考项目与聊天代发、ADR 的收紧白名单与新默认值、证据索引、中英文设置说明、builtin workspace-links reference。路由快照逐字比较：主线 37 条全部保留原方法、路径参数约束、名称与写入标记，仅增加 `chat.list` / `chat.get` / `chat.messages` / `chat.send` 四条。UUID 约束及固定端点拒绝仍保留，`ErrManagedRoute` 同步覆盖只读查询、附件上传和本人聊天，并提示 `multica workspace link view`。

首轮完整 workspacelink 复测发现新主线 `TestPendingLinkCannotEnableManaged` 隐含默认关闭前提；新连通默认开后，该测试里的开关允许用于关闭。测试现在显式创建 managed=false，继续验证未接受时不得开启；生产权限逻辑未因此改动。Skill 同步区分创建默认值与实际授权：仍要求 active，待接受的关闭连通不得通过 update 开启。

最终隔离数据库复测（数据库由脚本创建并已删除，未跳过）：

```sh
bash scripts/test-db.sh --quiet -- sh -c 'cd server && go test ./internal/workspacelink -count=1 -v && go test ./internal/service ./internal/handler -run "TestManagedChat|TestLinkedChatDelivery|TestLinkedDeliveryHandoff|TestManagedLinked" -count=1 -v'
```

退出 0：workspacelink 全包通过（40.443s），service 通过（4.317s），handler 通过（7.295s）。覆盖所要求的 managed_test.go、managed_chat_test.go、service_test.go 与 chat_managed_delivery_test.go；同时覆盖新主线白名单快照、固定端点、待接受权限和双向开关。实际发送验证本人聊天、审计、接收任务/集成配置及事务原子性；历史与交接验证外部内容信任边界。

CLI / 简报聚焦命令：

```sh
cd server && go test ./cmd/multica ./pkg/protocol -run 'TestManagedChat|TestCreateLinkManaged|TestChatPrompt|TestLinked' -count=1 -v
```

退出 0：CLI 1.027s，protocol 0.590s。`make sqlc` 成功，生成结果与自动合并一致。frozen-lockfile 安装完成；设置、消息列表、复制文字三个 views 测试文件共 62 项通过；`pnpm --filter @multica/views typecheck` 退出 0。未跑仓库全量检查，未重拍新主线设置页；既有 S1 图只证明记录中的旧候选，不冒充本轮截图。

本轮独立路由夹具：BASELINE routes=37 / chat_routes=0，MODIFIED routes=41 / chat_routes=4，ROLLBACK routes=37 / chat_routes=0，三个探针均 forbidden_routes_restored=0 / exit 0；回滚副本与 BASELINE 字节一致。它只证明路由表及可恢复性，不是部署回滚。完整命令、字面输出、哈希与日志随本轮 issue 回复附件交付。

权限复测结果交 Grok Bot 合并；发布后实际跨区发送与叫醒仍由孙悟饭承接，本轮未操作已有连通。


Grok Bot 同轮补充要求的服务入口整包复测也已完成：

```sh
bash scripts/test-db.sh --quiet -- sh -c 'cd server && go test ./internal/workspacelink/... ./cmd/server/... -count=1 && go test ./internal/service ./internal/handler -run "TestManagedChat|TestLinkedChatDelivery|TestLinkedDeliveryHandoff|TestManagedLinked" -count=1 -v'
```

退出 0：workspacelink 11.924s，cmd/server 32.560s，service 0.941s，handler 1.583s，隔离数据库已删除。补测第一次在迁移 344 时因本机磁盘满退出 1，尚未执行用例；删除约 4GB 超过 24 小时未使用的可重建 Go 缓存后，上述完整命令通过，保留失败与成功日志。`ErrManagedRoute` 已明确写出附件上传和只读名字/项目资源查询。`seg`、`issueRef`、`attachment.upload`、`lookup.project_resources` 与对象名审计均保留主线版本。本轮未改 daemon 心跳、离线判定或令牌代码。

最终推送前又纳入主线 `bb31d34257` 的 DENE-1806 线上证据更新；仅处理证据索引冲突，保留新主线条目及本票条目。生产代码与补充整包测试版本一致，不重复无变化的测试。

## 合并后上线验收接续（2026-10-11 17:25 UTC）

PR #634 已 squash 合入 `kun`，功能提交 `7121f543`。本轮不修改实现、不重复合并，也未发送生产验收消息。

- 已有 Linux CLI `v0.6.3-test.6` 指向 `95e4202b9c035180cc5c963bee8e768955d46d51`，不包含功能提交（祖先检查退出 1）。ai-multica `/usr/local/bin/multica version` 同样报告 test.6、commit `95e4202b9`。
- 只读自动更新回执 `/var/lib/multica/autoupdate.json`：17:22:03Z 的 `deployed_commit=95e4202b9…`，`target_commit=9e827e990a63dcc998f62f213b2784373bea5ec5`，`result=pending`；目标 backend 镜像尚不存在。没有手工启动部署或修改自动更新配置。
- 从刚 fetch 的 `origin/kun` tip `9e827e990a63dcc998f62f213b2784373bea5ec5` 创建并推送 CLI 测试标签 `v0.6.3-test.7`，包含 `7121f543`（祖先检查退出 0）。任务分支保持原位，未切分支；Desktop cut dry-run 的当前工作区检查失败，因此没有在任务分支运行正式 cut。这次只发布 CLI，Desktop 产物没有构建或宣称完成。
- 按既有借用仓库流程启动 [CLI 打包](https://github.com/kkunkunya/multica-image-builder/actions/runs/38159454642) 和 [自建镜像构建](https://github.com/kkunkunya/multica-image-builder/actions/runs/38159520015)。本记录时仍在构建，不能当作发布成功；下一轮核对 Release 的五份资产、校验和及版本，再升级生产运行环境。
- 接收候选已从本人可见聊天目录和历史确认：`01a12a08-cb06-7503-bdf2-f06c9d58d150`，孙悟空游戏，标题「game · test/dev 服务器巡检：机器人建群与跳板权限」，创建人 kunkunya。目录确认不等于已投递；必须由 WD 的真实任务令牌再次核对源方目录，再发送一次并保留消息 JSON、接收任务及来源显示证据。
- 原生设备缺口仍在：`xcrun simctl list devices booted` 报 simctl 不可用；`adb devices -l` 只有表头，没有连接设备。没有原生运行版本或截图可报。等待条件是可用 iOS 模拟器或 Android/iOS 设备及包含本功能的 App；网页截图不代替原生证据。

已登记一次性接续唤醒 `01a12bff-96df-7655-bbbf-8fa7f959eb99`，17:40:33Z 唤醒孙悟饭：核对上述构建和自动部署 → 校验并升级 grok生产服/deepseek生产服（先查是否共用守护进程和活跃任务）→ WD 真实投递与叫醒 → 原生证据。任务保持未完成，不把本轮环境检查算作线上验收通过。

知识审计：沿用已合并 ADR、CONTEXT 和平台 skill；本轮仅新增版本及验收接续事实，无新增权限或配置规则。原路由四件套从上一轮附件恢复并重新打开，补充执行 BASELINE/MODIFIED/ROLLBACK 探针，仍为 37/41/37 条路由、0/4/0 条聊天路由；这是夹具检查，不是部署回滚。
