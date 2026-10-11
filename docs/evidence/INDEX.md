# 证据索引

本索引只记录指向沉淀票附件的短指针；原始日志、截图和样本留在对应票据附件。

- DENE-1857：默认唤醒合并、连带停止、显式转工单后的唯一执行处；[验收、回归、CLI 契约与可撤回副本](https://ai.ferryway.cc/api/attachments/01a12c51-917a-7aeb-964c-1238e378017e/download)，[四档浏览器截图与验证边界](https://ai.ferryway.cc/api/attachments/01a12c4c-f9c0-7457-bc3f-87c15b413f2f/download)。最终 390px 停止/取消按钮已实测 44px，其余较早截图已标注；全套后端仍有主线已有失败与属性列表分页用例失败，未声明全套通过。 [终审返工：只递归子单，聊天止步，兄弟单保留运行/执行开关/重试的验证](https://ai.ferryway.cc/api/attachments/01a12c64-699f-7d21-b4d9-3e68d70ae5be/download)。
- DENE-1865：CLI 项目名、当前用户临时文件、钥匙状态与续期提示、服务端列表投影、一次性工作区选择；[完整测试、真实 CLI 与独立副本回滚记录（任务附件）](https://ai.ferryway.cc/api/attachments/01a12c55-6e00-7d6a-85fd-77deb84386d5/download)。52 个智能体紧凑输出 20,469 字节；670 项 CLI 与 13 项相关服务端测试通过。首次已失效且无缓存的钥匙元数据为未知，Windows 仅交叉构建。

- DENE-1863：外部 Bot 提及原文、楼层上下文、附件与原楼层回复、30 秒同来源分组；[四档真实截图与交付边界（任务附件）](https://ai.ferryway.cc/api/attachments/01a12c4f-4fad-7b53-963d-268ba1539f90/download)，[HTTP/CLI/迁移/重试/独立回执及夹具回滚验证包](https://ai.ferryway.cc/api/attachments/01a12c4f-51fb-799b-a07f-ed024b07d434/download)。提及也等待分组窗口，已明示约半分钟延迟；生产 Grok 与原生 App 未实测，终审待定。

- DENE-1864：附件大小误报修复、原始字节下载、一次重试和评论/任务批量下载；[真实附件、完整模块测试及回滚记录（任务附件）](https://ai.ferryway.cc/api/attachments/01a12c3b-d765-762d-8476-07d03d5ad1d2/download)。云电脑历史摘要失败未原地重现，独立评论入口及原始下载元数据已通过本地服务端测试。

- DENE-1862：外部 Bot 来源降级、额度/静默补发、提及额度独立与回执路径；[本地验证记录](DENE-1862/verification.md)，手机真实截图与生产 Grok 确认尚未完成。

- DENE-1849：Bot 唤醒上限同步回执、回复清零、人类豁免、可配置与权限审计；[真实 CLI、数据库与回滚验证包（任务附件）](https://ai.ferryway.cc/api/attachments/01a12c18-eb12-70ee-8751-8abb4f016f65/download)，[四档真实截图与浏览器验收边界](https://ai.ferryway.cc/api/attachments/01a12c22-cc11-7099-bd52-46c78950714e/download)。

- DENE-1852（认证故障部分）：本机排队故障根因、先留现场再恢复、失效凭据断连及桌面端恢复；[本地验证记录](DENE-1852/auth-recovery.md)。未代表自动改派、通知、自启或并发上限已完成。

- DENE-1811：跨工作区连通双向设置、CLI 实时同步与审计分页；[验收记录及四档 HTML 预览](DENE-1811/checkpoint.md)。

- DENE-1804：外部 bot 自动入门，发钥匙说明 / `multica capabilities` / 设置页「复制接入说明」三处同源，拼错子命令给建议与 JSON 错误；[预览、1280/390 稿与真实 CLI 输出](DENE-1804/preview.html)。

- DENE-1779：聊天项目筛选跟随当前对话、右侧项目变更和新建项目；手动「全部」在跳转与刷新后保留。[真实浏览器 375/390/768/1280 截图报告（任务附件）](https://ai.ferryway.cc/api/attachments/01a12929-1b8b-70e0-bb44-10f1349a356a/download)。

- DENE-1050：统一带选项提问的三面交付；[PR #448](https://github.com/jeff-kunkun/multica/pull/448)，界面预览 `preview/ask-options-card.html`。
- DENE-1051：任务完成线、锁定状态与三项预算；[PR #449](https://github.com/jeff-kunkun/multica/pull/449)，界面预览 [`docs/design/goal-section-preview.html`](../design/goal-section-preview.html)。
- DENE-1052：目标任务自动续跑、预算刹车与席位接力；[PR #452](https://github.com/jeff-kunkun/multica/pull/452)。
- DENE-1053：统一补全完成线页及四个目标入口；[PR #453](https://github.com/jeff-kunkun/multica/pull/453)。
- DENE-1201：进行中改派保护、执行席来源可见性与三面路由契约；[PR #479](https://github.com/jeff-kunkun/multica/pull/479)。
- DENE-1277：全站 Web/Desktop 响应式盘点与页面族拆分；[盘点报告](DENE-1277/report.html)，第二阶段子票 DENE-1292 至 DENE-1296 仍在进行。
- DENE-1291：共享移动外壳的返回栈、安全区、触控尺寸与窄屏承载；[PR #513](https://github.com/jeff-kunkun/multica/pull/513)，[HTML 预览](../design/dene-1291-mobile-shell-preview.html)。
- DENE-1342：巡检识别没人驱动的票、两次原席位重跑后上报父票，`multica issue dispose` 四种处置；[预览与 375/768/1280 截图](DENE-1342/preview.html)。
- DENE-1328：任务状态卡（`multica issue context`、`--decision`、拍板增改删）；[预览与 1280/390 截图](DENE-1328/preview.html)。
- DENE-1346：聊天插话 / 排队 / 打断重来与常驻停止按钮，任务页叫醒方式统一；[预览与 1280/390 截图](DENE-1346/preview.html)。
- DENE-1362：原生 App 聊天插话 / 排队 / 打断重来，回复中常驻停止按钮；模拟器截图 [回复中输入](DENE-1362/typing.webp)、[弹层](DENE-1362/sheet.webp)、[键盘弹起](DENE-1362/keyboard-stop.webp)、[插话后](DENE-1362/sent.webp)、[不支持插话](DENE-1362/no-steer.webp)。
- DENE-1349：Cursor / Copilot / CodeArts / DevEco / Antigravity / OpenClaw 用重启续接插话，同一会话 ID 继续原任务；[cursor-agent 实测与预览](DENE-1349/README.md)。
- DENE-1348：OpenCode 1.x、Pi 接通插话，Qwen Code / CodeBuddy / DSH 降级排队并写明原因；[实测记录](DENE-1348/report.md)。
- DENE-1347：ACP 类 CLI 停一步续接插话（11 个 CLI 共用一段代码），Grok 原始协议实测通过，Kimi/Hermes 按 Kun 决定不实测；[实测记录](DENE-1347/README.md)，[菜单预览（含 390px）](DENE-1347/steer-menu-preview.html)。
- DENE-1451：工作区领域列表、项目多领域、任务单领域、特化 = 基础角色 + 领域，路由与原话按任务领域落到特化；[预览与 1280/390 截图](DENE-1451/preview.html)。
- DENE-1479：Web/Desktop 选智能体统一按领域排序（负责人、验收席、@ 提及、聊天、自动化、项目负责人），tarot 对口特化在前、Multica 魔改 基础角色在前；[1280/390 截图汇总](DENE-1479/index.html)。
- DENE-1643：聊天挂连通工作区的只读参考项目（+ → 项目上下文 → 连通的项目（只读），失效标记，建连通页文案）；真实聊天页 375/390/768/1280 操作截图与服务端结果在 [live/](DENE-1643/live/steps.txt)（勾选、移除、取消共享与撤销后失效、逐个移除失效项），早期静态稿 [预览](DENE-1643/preview.html)。
- DENE-1661：收口的知识声明要对上交付文件、聊天沉淀合进主线（`multica chat sediment`），项目记忆卡片新增「最近沉淀」，子任务收口条显示实际写入的文件；[预览](DENE-1661/preview.html)，[1280](DENE-1661/preview-1280.png) / [390](DENE-1661/preview-390.png) 截图。
- DENE-1670：DENE-1659 的实页与完整链路验收——本地候选环境里跑真实 CLI → 服务端 → 数据库 → 页面的聊天沉淀（本地合入主线、远端未推送拒记、推送后记录），项目记忆卡片与收口条 375/390/768/1280 实页截图；[报告](DENE-1670/report.html)，复现脚本见报告内 scripts/。
- DENE-1665：聊天开出的单自动记下来源聊天；聊天里开单卡（谁在做、为什么、实时状态，改给我 / 改给智能体 / 撤回），任务单显示「来自聊天」；`GET /api/chat/sessions/{id}/tickets` 与 `multica chat tickets`；[1280/390 截图](DENE-1665/preview.html)。
- DENE-1672：聊天开出的任务完成 / 卡住 / 待验收时往来源聊天发回执卡（结论、PR、沉淀），`multica chat tickets` 带结果列，状态卡「来源」行与侧栏「原话」，聊天每回合带上它开出的任务现状；[375/390/768/1280 截图与 CLI 输出](DENE-1672/preview.html)。
- DENE-1677：路由定档改为选择题加规则表，判断模型只能上调一档；设置页只读规则表与自动选派评论的答题记录，[预览（含 390px）](DENE-1677/preview.html)、[桌面](DENE-1677/desktop.png)、[手机](DENE-1677/mobile.png)。
- DENE-1667：聊天底部「本聊天的单」进度条与浮窗/手机抽屉（刚变置顶），聊天里听汇报（Mermaid 图 + 要你做的 + 操作按钮，听完读掉收件箱），「听到哪了」按人 + 项目存服务端，项目页听汇报入口，`multica project report`；[预览与 375/768/1280 截图](DENE-1667/preview.html)，原生 App 的汇报图与入口在 DENE-1682。
- DENE-1647：验收席失败后的接力去向（执行记录「失败后已转给 X」/「等人决定」）和席位鉴权暂停说明；桌面与 390px 手机静态稿 [预览](DENE-1647/preview.html)、[桌面截图](DENE-1647/desktop.png)、[手机截图](DENE-1647/mobile.png)。

- DENE-1679：子任务回执汇总给父票——完成评论、状态卡「子任务回执」、来源聊天的父票回执卡与 `multica issue context --output json` 的 `children` 都列出每张子票的结论、PR、沉淀；[375/390/768/1280 截图与 CLI 输出](DENE-1679/preview.html)。
- DENE-1680：老板层沉淀与记忆卫生——项目记忆卡片「最近沉淀」显示「汇总自 DENE-N」和每条改动的动作（新建/更新/待合并/标记已被取代），`multica project memory status` 同样列出来源；[375/390/768/1280 截图与 CLI 输出](DENE-1680/preview.html)。
- DENE-1681：项目记忆卡片加沉淀与回流监控——近 14 天写入与删除行数（来源票 / 聊天）、收口没沉淀（声明无可沉淀 / 没做审计）、沉淀轮次与空转、聊天派单回流（已回报 / 无结论 / 进行中），每行可点回来源；`multica project memory monitor` 同一接口；[375/390/768/1280 截图与 CLI 输出](DENE-1681/preview.html)。
- DENE-1709：手机网页（窄屏 + 触屏）底部导航「聊天 / 任务 / 新建 / 收件箱 / 更多」，打开默认进聊天，任务页左上角返回回到来源；[390/768/1280 截图](DENE-1709/preview.html)。
- DENE-1671：连通「可托管」界面——设置里每条连通一行托管开关（服务端逐条答 `can_set_managed`，不能改的写原因），自动化详情「经连通的代办」，连通视图提示可让智能体代办；[375/768/1280 截图](DENE-1671/preview.html)。
- DENE-1682：手机 App 聊天里听汇报的 Mermaid 进度图按三组清单显示、带项目的聊天进度条出现「听汇报」；无模拟器，附 390px 浅/深色 HTML 预览图：[预览](DENE-1682/preview.html)。
- DENE-1678：PR 已合入且每个合入的 PR 合入的那个版本都审过（GitHub 对该提交的批准，或验收席在该提交时给的 `verdict: pass`）时跳过验收席直接完成；任务页「跳过验收」行、手机 App 头部、`issue close` 警告与 `issue context` 同一句原因；[真实任务详情页 375/390/768/1280 截图](DENE-1678/report.html)。
- DENE-1719：聊天底栏和卡片显示本聊天跟进的已有票（新建 / 跟进 / 手动挂上），可手动挂上和取下，`multica chat tickets add|remove` 同一接口；[390/768/1280 截图](DENE-1719/preview.html)。
- DENE-1722：路由从结果里学——升档、验收打回记为判低，同类票判低比例过阈值时新票上调一档（默认影子运行），`multica workspace routing learning` 查各类统计；[1280/390 截图](DENE-1722/preview.html)。
- DENE-1721：干活中咨询强档席位——`multica issue consult` 同步问最强档一句（每张任务默认 3 次，设置 → 智能体权限可调），时间线一行可展开看问题和回答，Web/手机 App 同步；HTML 预览稿非真实页面截图：[1280/390 预览](DENE-1721/preview.html)。

- DENE-1786：Grok 个人令牌 CLI 真机验收：[原始报告日志](https://ai.ferryway.cc/api/attachments/01a129b6-9502-7652-844c-e84d896735cb/download)，沙盒 DENE-1797；补测聊天附件的消息为 `01a129bf-1cc2-771e-b9a3-8582b957a37b`，最终回复 `01a129bf-4755-73f3-a371-3fcdc7901306`；执行信箱任务 `t-29af8208c2` 的校验回报为评论 `01a129bc-7250-731c-9ecb-f8b7b10f480a`。新进程登录通过，整机重置未测，不能据此声称全年在线。
- DENE-1787：智能体系列自动归类、手动覆盖与同档路由偏好；[含手机版的 HTML 预览](../design/prototypes/agent-series.html)。真实设置页与列表 375/390/768/1280 截图、CLI 往返、数据库与换家族验证见任务附件。
- DENE-1788：设置页「外部 Bot」（仅所有者/管理员可见）发管理员同权钥匙与接入指令，停用即 401、发新钥匙、删除，`multica bot`；[PR #626](https://github.com/jeff-kunkun/multica/pull/626)，[1280/768/390/375 截图](DENE-1788/index.html)，`multica chat new --wait` 等命令行全链路实测见 [bot-cli-e2e](DENE-1788/bot-cli-e2e.md)。
- DENE-1794：评论、聊天和改单记录「经 Bot」，Bot 留言不算人的原话，停止和删除后保留历史；[最终验收证据](https://ai.ferryway.cc/api/attachments/01a129ef-dd01-77ac-b6dd-a0eaf945f3d7/download)含任务及聊天 375/390/768/1280 实页截图、禁止消息重拉时的真实 PAT 实时显示、PostgreSQL/Redis 与 CLI 来源专项测试；[来源与部署契约](../../server/docs/bot-write-provenance.md)。后台推送与四跳限制由 DENE-1792 集成验收。

- DENE-1803：工作区全景聚合接口、CLI 与共享设置入口；[验收附件](https://ai.ferryway.cc/api/attachments/01a12a17-15bf-79fb-8d0f-bcd0526d52f6/download)含 375/390/768/1280 实页截图、筛选与刷新请求记录、真实 Bot 钥匙接口测试和本地 CLI 数量对照。完整/精简 JSON 为 3457/1730 字节（隔离测试工作区）。

- DENE-1806：项目默认参考项目、自动化继承/覆盖与每次运行重新核验；[含手机版的 HTML 预览（任务附件）](https://ai.ferryway.cc/api/attachments/01a12a26-bdc6-798a-b349-f9ed72409a6c/download)。数据库、任务认领与页面截图见任务交付附件；2026-10-11 已在升级后的 WD 真实巡检 run 简报中核验 `game`、`game-multica` 的描述、资源与项目记忆，均只进入参考段。[线上简报与升级证据（任务收尾附件）](https://ai.ferryway.cc/kunkunya/issues/01a12a20-e63b-7a09-bcd7-fafbfa684a9c)。

- DENE-1808：可托管聊天代发、新连通默认开托管（旧连通不变）；[桌面/手机 HTML 预览附件](https://ai.ferryway.cc/api/attachments/01a12a2c-418e-78c0-bef9-e084235a21f1/download)。[验证记录与验收结论](DENE-1808/README.md)：真实设置/聊天四档截图、开关提交及线上 WD 代发/叫醒已通过；Grok Bot 代 Kun 验收收单，原生截图按决定留到后续发版、不占用 Kun 前台。

## Compact cross-workspace reads

- DENE-1870：服务端紧凑读取与 Web/Desktop 共享收件箱入口，集成于父分支；[协议](../kun/fast-access.md)、[四档真实截图报告](https://ai.ferryway.cc/api/attachments/01a12c30-6781-7d33-94d8-6399c6cbba01/download)、[SQL 改前/改后与回滚记录](https://ai.ferryway.cc/api/attachments/01a12c30-67e6-7a3d-a878-d4a99839d107/download)、[最终后端回归输出](https://ai.ferryway.cc/api/attachments/01a12c30-6844-72e6-aa30-57da9aee6405/download)。隔离数据库两工作区、17 个活动运行，5 页无漏重；前后两轮 30 次本地 p95 分别为 1.043 秒 / 43.852 毫秒，不外推生产性能。375/390/768/1280 实页与 375 关闭、未读不变已核验；浏览器续页/刷新成功、独立 Electron、原生 App 和线上实测未完成，手机入口与页面缺口由 DENE-1879 接续，线上复核归父任务。
- DENE-1871：编译后 CLI → HTTP → 真实处理器 → 隔离 PostgreSQL 联调；[验证账本](https://ai.ferryway.cc/api/attachments/01a12c35-442d-76e1-af28-b8e9b107bf48/download)、[源码补丁](https://ai.ferryway.cc/api/attachments/01a12c35-3ed7-7946-9ac7-e8ea3ba2029f/download)。`changes` / `runs --active` 各一次业务请求；两工作区 17 个活动运行，分别 9231 / 8117 字节；显式范围及 4 页 20 条无漏重，误把快照收据当游标被拒。身份在认证后的测试边界注入，未验证登录/令牌或生产延迟；交回父分支集成，不代表已发布。
- DENE-1872：可靠事件续传及离线清理、超时终结、重启恢复三条实际 SQL 路径的锁等待补测；[事务、回放与回滚验证包（任务附件）](https://ai.ferryway.cc/api/attachments/01a12c52-612c-773f-9585-c27aebf4ea9d/download)、[锁等待原始样本与命令输出（补测附件）](https://ai.ferryway.cc/api/attachments/01a12c5b-d11f-7512-8269-f92e6911da6e/download)。12 组、120 次本地事务，独立测量总耗时与 `pg_locks.waitstart` / `pg_blocking_pids` 观察到的等待区间；0 表示未观察到等待，不能证明无等待。改前、改后及 down 回滚的生命周期结果一致；补测未改生产代码。父任务于 2026-10-11 19:10 UTC [确认补测已复核并进入父分支，推进第三阶段](https://ai.ferryway.cc/kunkunya/issues/01a12c04-c758-7129-86e4-0d7598c598a4)，取代子任务收口时“仍待父单复核”的阶段结论。尚未发布；小规模本地采样不代表生产性能，线上两工作区、多副本、独立 Electron 和原生设备验证仍待完成。
