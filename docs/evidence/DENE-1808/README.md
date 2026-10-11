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
