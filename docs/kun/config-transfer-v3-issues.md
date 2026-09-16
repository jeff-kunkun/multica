# 跨环境迁移包 V3 契约：任务迁移（DENE-365）

本页是「把任务与评论一起搬到另一个 Multica 环境」的实现契约。后续实现 Stage 按本页执行，不再自行决定逐表口径、编号策略、mention 重写、收件箱范围、包结构与无副作用写入方式。本页只定义契约，不含实现代码。

前置文档：

- `docs/kun/config-export-import.md`（下称 **V1 契约**）——配置范围、秘密字段表、身份键、降级表。
- `docs/kun/config-transfer-v2.md`（下称 **V2 契约**）——方向、实现面、跨实例 ID 映射、秘密扫描、包结构、分片。

V1 / V2 已经定死的东西本页**全部沿用**，只写 V3 新增或改变的部分。

## 立项来源

kk zi 2026-09-16 真机反馈（DENE-365 描述引用的评论 `01a0ab16`）：

> 1. 聊天同步过去了，但收件箱、我的任务都没有同步过去
> 2. 工作区里面的任务、项目里面残留的任务……这些都没有同步过去

只读核实结论：这三项**一条都没导出过，不是导出失败，是压根没读**。`server/internal/service/transfer_export.go` 的读接口清单里没有 `/api/issues`、没有评论、没有通知。V2 契约 §3.3 与 §7 排除表当时是明写排除的，理由是「只迁评论不迁任务，评论无处挂载」，并点名「暂称 V3『任务迁移』」。**kk zi 已明确要求把 V3 做掉**，本页就是那个立项。

## 核对基线（2026-09-17）

- fork `kun` tip `eba2fbef94`；`server/migrations/` 至 `489_issue_draft_policy`；字段以 `server/pkg/db/generated/models.go` 为准。
- V2 实现在 `server/internal/service/transfer_{bundle,export,ids,import,redact}.go`、`server/internal/handler/workspace_transfer.go`、`server/pkg/db/queries/workspace_transfer.sql`、`server/cmd/multica/cmd_transfer.go`。
- 本页每条结论都标注了核对到的代码位置。凡写「未确认」的地方，实现 Stage 动手前必须先验证，不得按猜测实现。
- **第 11 节的 7 条未确认项已在 DENE-383（2026-09-17）逐条核实并改写成结论。** 那一轮的核实基线：fork `kun` tip `72420ece6f`；官方云侧用本机 `multica` CLI v0.4.57 对 `https://api.multica.ai` 的 `deneb-3029` 工作区做只读探测（未写入任何数据）；上游源码读 `upstream/main`（`7e4758ac1a`，`kun` 与它的 merge-base 是 `3551e72e76`）；库级事实读本机新建库 `multica_dene383`，`go run ./cmd/migrate up` 跑到 `489_issue_draft_policy` 后查 `pg_trigger`。核实过程中修正了第 7.1 节与第 8.4 节各一处**原文写错的事实**，见那两节。

## 0. 一页结论

| # | 问题 | 结论 |
| --- | --- | --- |
| 1 | 逐表口径 | `issue` / `comment` / `comment_reaction` / `issue_reaction` / `issue_to_label` / issue 与 comment 附件**导出**；`activity_log`、任务运行记录、`inbox_item`、`issue_subscriber` 源行、`issue_source_context`、PR 关联、`issue_draft`、`issue_dependency` **不导出**。`issue_counter` 不是表，是 `workspace.issue_counter` 列，不搬值只顶水位。详见第 1 节。 |
| 2 | 编号策略 | **目标工作区必须为空**（`CountWorkspaceIssues == 0`），此时 `number` 原样保留、`issue_prefix` 由 V1 已有规则一并落地、正文里的 `DENE-xxx` 自动正确。目标非空时默认 **400 拒绝** `issues` 分组；只有显式 `--renumber` 才做整体偏移，代价（正文引用全错）写在报告里。详见第 2 节。 |
| 3 | mention 重写 | 只处理 `util.MentionRe` 认识的 `member` / `agent` / `squad` / `issue` 四类。映射得到就改写 id；**映射不到一律降级为纯文本**（丢链接、留标签文字），不保留死链。理由是代码级的：残留的无法解析的根评论 mention 会让整条线程的后续路由**静默失效**。详见第 3 节。 |
| 4 | 父子与阶段 | `issue.parent_issue_id` 和 `comment.parent_id` 都有真实外键，必须**两遍写入**：第一遍全部以 `parent = NULL` 插入，第二遍回填父指针。回填解析不到时两张表口径不同：issue 置 `NULL`，**comment 上溯到最近的包内祖先**（11.7）。`stage` / `position` 随第一遍原样写入。详见第 4 节。 |
| 5 | 多态指派 | `assignee_type` ∈ `member` / `agent` / `squad`，复用 V2 的 people map 与 V1 的 agent / squad 身份键。映射不到降级为**未指派**并记报告行；`creator_*` 同规则，但兜底为导入者而不是空（列 NOT NULL）。同一套映射还要跑在 `issue.properties` 里 `actor` / `multi_actor` 两类属性的值上（1.2.1）——那是最容易漏的一处引用。详见第 5 节。 |
| 6 | 收件箱 | **不迁 `inbox_item`**。它 100% 由任务完成与 autopilot 配额产生，跨实例没有对应运行记录。但导入时**必须重建 `issue_subscriber` 的 creator / assignee 两条**，否则导入后这些票上的新活动谁都收不到通知。详见第 6 节。 |
| 7 | 无副作用写入 | 沿用 V2 的做法：`workspace_transfer.sql` 里加专用 `INSERT ... ON CONFLICT (id) DO NOTHING`，带显式 `id` 与 `created_at`，**不走** `CreateIssue` / `CreateComment` / `h.publish` / 事件总线。测试断言导入不产生任何 `agent_task_queue` 行。详见第 7 节。 |
| 8 | 体积与分片 | 复用 V2 的 `TransferMessageShardMaxBytes` / `TransferConversationsMaxBytes` / `TransferAttachmentMaxBytes` 三个常量，不另立。任务分片按「一张 issue 的全部评论不跨片」聚合。详见第 8 节。 |
| 9 | 包结构与版本 | 外层 `schema_version` 顶到 **2**，新增 `issues/` 目录与 `include` 分组 `issues`（**默认不开**）。V3 导入器必须能读 `schema_version: 1` 的 V2 包与裸 V1 JSON。详见第 9 节。 |
| 10 | 秘密边界 | `transfer_redact.go` 的模式表原样跑在 issue 标题 / 描述 / 评论正文 / 文本附件上，一个都不减。新增两条：`issue.metadata` 与 `issue.properties` 两个 JSONB 值袋要跑 V1 兜底键名清洗。详见第 10 节。 |

| 11 | 实现前的 7 项核实 | **全部有结论**（DENE-383，2026-09-17）：自建实例任务数配额默认不生效，导入不调配额检查但必须回显策略并在 `enforce` 会越线时拒绝；官方云 `GET /api/issues` 的 `limit` 静默钳 100、支持 `sort=created_at` + `direction=asc`、未观察到通用速率限制；`comments` 的 `since` 支持 RFC3339 与 RFC3339Nano；官方云属性类型集合与 `kun` 逐字相同；墓碑行为源码两侧一致但拿不到正样本，父指针按无条件上溯实现。详见第 11 节。 |

一句话的模块形状：CLI 多读四组读接口、多写一个 `issues/` 目录；服务端多一个 `POST /transfer/issues` 端点；编号、父子、mention、指派的映射全部藏在这两层内部，产品其余部分一行不改。

## 1. 逐表口径

### 1.1 先纠正三个表名

DENE-365 描述里的表名与库里的实际结构有三处出入，按实际结构执行：

| 描述里写的 | 库里实际是什么 | 影响 |
| --- | --- | --- |
| `issue_label` | `issue_label` 是**标签定义表**（V1 已导），任务与标签的关联在 `issue_to_label`（`issue_id`, `label_id` 复合主键，`001_init.up.sql`） | V3 导的是 `issue_to_label`，标签定义继续由 V1 config 部分负责 |
| `issue_property_value` | **不存在这张表**。自定义属性值存在 `issue.properties` JSONB 里，键是属性定义的 UUID（`191_issue_properties.up.sql`） | 属性值不是独立分组，是 `issue` 行的一个字段；但键要按属性名重映射（见 1.2） |
| `issue_counter` | 不是表，是 `workspace.issue_counter` 列，由 `IncrementIssueCounter` 自增（`pkg/db/queries/workspace.sql:60`） | 不导出它的值，只在导入收尾顶水位（见第 2 节） |

### 1.2 逐表清单

| 表 | 结论 | 导出字段 | 排除字段 | 理由 |
| --- | --- | --- | --- | --- |
| `issue` | 部分导出 | `id`（作 `source_id`）、`number`、`title`、`description`（经第 10 节扫描）、`status`、`priority`、`assignee_type` / `assignee_id`（引用）、`creator_type` / `creator_id`（引用）、`parent_issue_id`（引用）、`project_id`（引用）、`position`、`stage`、`start_date`、`due_date`、`created_at`、`updated_at`、`last_activity_at`、`metadata`、`properties`（键重映射，见下） | `workspace_id`（改写为目标）、`revision`（目标端从 0 起，它是乐观锁计数不是用户数据）、`origin_type` / `origin_id`（指向源实例的 autopilot 或 quick-create 任务）、`first_executed_at`（运行态）、`acceptance_criteria` / `context_refs`（见 1.3） | 时间戳原样保留，否则「这票什么时候开的、多久没动」全部失真，且 `last_activity_at` 是列表默认排序键。`status` 是自由文本列（`337_issue_status_open_check`），自定义状态键由 V1 的 `issue_status` 分组保证目标端已存在；解析不到的键在导入时降级（见 1.4）。 |
| `issue.properties`（字段） | 部分导出 | 值袋整体，但**顶层键必须重映射**，且 `actor` / `multi_actor` 两类的**值也要重映射**（见 1.2.1） | 目标端找不到同名属性的键；actor 引用映射不到的那一条值 | 不重映射的话属性值会挂在一个目标端不存在的属性 id 上，UI 读不到、也删不掉。 |
| `issue_to_label` | 全量导出 | `(issue source_id, label 身份键)`。label 身份键沿用 V1 的 `(resource_type, lower(name))` | `label_id`（源实例 id） | 标签定义已由 V1 config 部分迁过去，这里只需要重连关系。 |
| `comment` | 部分导出 | `id`（作 `source_id`）、`issue_id`（引用）、`author_type` / `author_id`（引用）、`content`（经第 3 节重写 + 第 10 节扫描）、`type`、`parent_id`（引用）、`created_at`、`updated_at`、`resolved_at` / `resolved_by_type` / `resolved_by_id`（引用） | `workspace_id`（改写为目标）、`revision`、`source_task_id`（指向源实例任务运行记录）、`quick_action_id`（源实例快捷动作 id）、`via_plugin_id`（源实例插件安装 id）、`recovery_settled_at`（运行态） | 见 1.5 关于 `type` 与 `deleted_at` 的展开。 |
| `comment_reaction` | 部分导出 | `comment_id`（引用）、`actor_type`、`actor_id`（引用）、`emoji`、`created_at` | `id`（目标端按确定性 id 重算）、`workspace_id` | actor 映射不到就**丢掉这一条 reaction**，不降级为匿名——挂在错人名下比没有更糟。 |
| `issue_reaction` | 同 `comment_reaction` | 同上，`issue_id` 代替 `comment_id` | 同上 | 同上。 |
| `attachment`（`issue_id` 或 `comment_id` 非空） | 部分导出 | `id`（作 `source_id`）、`issue_id` / `comment_id`（引用）、`filename`、`content_type`、`size_bytes`、`created_at`、`sha256`（导出时计算）；文件体按 **V2 §3.4 原样口径**（图片导体；文本类先扫描、命中即只留元数据；其他二进制与 >25 MiB 只留元数据） | `workspace_id`、`url`（源实例地址）、`uploader_type` / `uploader_id`（改写为导入者）、`chat_session_id` / `chat_message_id`、`task_id`、`source_context_id` | 与 V2 聊天附件同一条写入路径（`ImportTransferAttachment`），只是挂载列不同。 |
| `activity_log` | **不导出** | — | 全部 | 两条独立理由，任一条都足够：(a) `cmd/server/router.go` 里**没有任何 issue 活动日志的读接口**，V2 §2 定死「导出只用读接口」，CLI 根本拿不到；(b) `details` JSONB 里嵌的是源实例的任务 id、运行时 id、成员 id，跨实例全是死引用。用户看到的后果写在 1.6。 |
| `agent_task_queue` / `task_message` / `task_usage` / `task_token` | **不导出** | — | 全部 | 运行态，沿用 V2 §7 排除表。导入后每张票的「任务运行记录」标签页是空的。 |
| `inbox_item` | **不导出** | — | 全部 | 见第 6 节。 |
| `issue_subscriber` | **不导出源行**，但导入时按规则重建 | — | 全部 | 见第 6 节。 |
| `issue_source_context` / `issue_source_context_object_intent` | 不导出 | — | 全部 | 建票时抓的不可变上下文快照，含源实例的对象 id 与渠道消息引用；`GetIssue` 只在详情页返回（`IssueResponse.SourceContext` 标注 detail-only），列表拿不到，逐票拉一次成本高而收益是一段死引用快照。报告记一条 `source_context_not_migrated` 计数。 |
| `issue_pull_request` / `issue_vcs_pull_request` / `github_pull_request*` | 不导出 | — | 全部 | 指向源实例的 GitHub App 安装与 VCS 连接；V1 契约本来就只迁「连接清单」不迁凭据，PR 行迁过去点不开也刷不了状态。 |
| `issue_dependency` | 不导出 | — | 全部 | 读接口面**已核实：没有**（11.2）。`router.go` 全文没有注册任何 dependency 端点，CLI 拿不到这张表。保持不导，报告里记 `issue_dependency_not_migrated`。 |
| `issue_draft` | 不导出 | — | 全部 | 未提交的草稿是本人的临时状态，且 `489_issue_draft_policy` 刚改过策略，跨实例语义不稳。 |
| `pinned_item`（`item_type = 'issue'`） | 不导出 | — | 全部 | 个人置顶偏好，且指向源实例 issue id。V2 只迁了聊天的 `pinned_agents`，这里保持一致，不扩面。 |

### 1.2.1 `issue.properties` 的三类值，逐类怎么搬

属性类型共九种（`internal/handler/property.go:55`：`text` / `number` / `select` / `multi_select` / `date` / `checkbox` / `url` / `actor` / `multi_actor`）。按值的形状分三类处理：

| 类 | 类型 | 值长什么样 | 怎么搬 |
| --- | --- | --- | --- |
| 标量 | `text` / `number` / `date` / `checkbox` / `url` | 字符串 / 数字 / 布尔 | 原样搬。只需重映射顶层键 |
| 选项 id | `select` / `multi_select` | **选项 id 字符串**（`select`）或 id 数组（`multi_select`），id 来自 `issue_property.config`（`property.go:519`、`:528`） | 原样搬。前提是 V1 的 `issue-properties` 分组把 `config` 原样写进了目标端的同名属性——**在空工作区下成立**（V1 新建该属性、`config` 逐字节写入，选项 id 因此保留）。目标端已存在同名属性且选项 id 不同时，值会指向不存在的选项：这是第 2.2 节「目标必须为空」的又一条支撑。目标非空 + `--renumber` 时必须逐值校验选项 id 存在，不存在则丢弃该属性值并记 `property_option_unmapped` |
| **actor 引用** | `actor` / `multi_actor` | **`"member:<uuid>"` / `"agent:<uuid>"` 形式的字符串**，`multi_actor` 是它们的数组（`property.go:554`、`:564`） | **必须重映射 uuid**，规则与第 5 节的指派映射完全相同（people map / agent 身份键）。映射不到：`actor` 整个键删掉，`multi_actor` 只删那一条引用（删空后整个键删掉）。每处记一条报告行 `property_actor_unmapped` |

`actor` / `multi_actor` 这两类**不能**当标量原样搬——它们装的是源实例的成员与智能体 UUID，搬过去在目标端要么显示为空白，要么（同实例跨工作区场景）指向不相干的人。这一条容易漏，因为从 `issue.properties` 的 JSONB 外形上看不出它藏着引用。

### 1.3 `acceptance_criteria` 与 `context_refs` 为什么不导

这两列在 `001_init.up.sql` 里就存在，但 **`IssueResponse` 里没有对应字段**——`internal/handler/issue.go:935` 是全仓唯一 SELECT 到它们的地方，而 `issueToResponse` 不序列化它们。也就是说：

- 任何读接口都拿不到这两列，CLI 无从导出；
- 产品当前的「验收标准」写在 `description` 正文里（本票自己的描述就是这么写的），所以不导它们不丢用户可见内容。

前端是否仍有代码路径读这两个字段，**已核实：没有**（11.3）。`packages/` 与 `apps/` 的 `.ts` / `.tsx` 对这两个名字（含驼峰写法）零命中。

### 1.4 状态键解析不到时的降级

`issue.status` 自 `337_issue_status_open_check` 起是自由文本，工作区状态目录由 V1 的 `issue-statuses` 分组负责迁移。导入 issue 时：

- 目标工作区的 `issue_status` 目录里存在同 `key` → 原样写入；
- 不存在（例如导入者用了 `--include` 排除了 config 分组，或该自定义状态在目标端被归档删除）→ 降级为源状态的 `status_category`（V2 的 `refs` 索引里随包带上每个用到的 key 对应的 category），仍不可解析则降级为 `backlog`，每张票记一条报告行 `status_key_unmapped`。

不允许写入目标端不存在的状态键：`issue` 表虽然不再有枚举 CHECK，但列表、看板、终态判定全部要查目录，写进去的孤儿键会让这张票在任何按状态分组的视图里消失。

### 1.5 `comment.type` 与墓碑行

`comment.type` 的四个取值（`001_init.up.sql:104`）**全部导出**，包括 `status_change` 和 `system`：

- 它们只是文本行，导入后是纯展示，不带任何运行态指针（`source_task_id` 等已在排除列里）；
- 丢掉 `status_change` 会让「这张票怎么从 todo 走到 done 的」整段时间线消失，而这正是用户抱怨「任务没同步过去」时想看到的东西。

**这一条要写死给实现者**：`POST /api/issues/{id}/comments` 用 `isClientAuthorableCommentType` 拒收 `status_change` 与 `system`（理由是客户端伪造平台叙述）。V3 的导入端点是专用写入路径、**不是**那个端点，**不得复用 `isClientAuthorableCommentType` 做校验**，否则每张票的状态变更叙述会被静默丢光。

`author_type` 在评论上可以是 `member` / `agent` / `system`（`comment.sql` 多处按 `author_type = 'system'` 查询）。`system` 作者不需要映射，原样写入。

墓碑行（`deleted_at` 非空）：**导出**。它 `content` 为空、不带附件与 reaction，唯一作用是让子回复保住直接父指针（`#8296`）。不导它，第 4 节的父指针回填会在这些位置断链，整条线程被拍平。

### 1.6 排除之后用户会看到什么

这一节是给 CLI 提示文案和导入报告用的，必须逐条对用户讲清楚，不能让人以为「搬完了就一模一样」：

| 排除项 | 用户在目标端看到的 |
| --- | --- |
| `activity_log` | 票详情里的「活动」时间线是空的。状态变更仍然可见——因为 `status_change` 类型的**评论**导过去了（1.5），两者在产品里是两个不同的东西。 |
| 任务运行记录 | 每张票的任务运行记录列表为空；历史上哪个智能体跑过几轮、花了多少 token 都看不到。 |
| `inbox_item` | 收件箱是空的，从导入后的新活动开始积累（第 6 节）。 |
| `issue_source_context` | 由渠道消息 / 快捷创建生成的票，详情页不再显示「来源」卡片。 |
| PR 关联 | 票上不再挂 PR 徽标；PR 本身在 GitHub 上不受影响。 |
| 智能体续聊 | 与 V2 §4.6 相同：导入后智能体在这些票上从新会话开始，没有源端的 CLI 会话记忆。 |

「我的任务」在导入后**立刻有内容**——它读的是 `assignee_id`，第 5 节保证指派被重建。这正是 kk zi 反馈里第二条要的东西。

## 2. 编号策略

这是本页最关键的一节。

### 2.1 事实基础

- `workspace.issue_counter` 是单调自增列，`AllocateIssueNumber`（`internal/service/issue_limit.go:87`）在建票事务里 `IncrementIssueCounter` 取号，删票**不回退**计数。
- `issue` 上有唯一约束 `uq_issue_workspace_number (workspace_id, number)`（`020_issue_number.up.sql:33`）。
- 用户看到的 `DENE-365` = `workspace.issue_prefix` + `-` + `issue.number`（`issueToResponse` 里拼的）。
- V1 契约已经定了一条对我们极其有利的规则：`issue_prefix` **只在目标工作区尚无任何任务时才写入**，否则跳过并告警 `issue_prefix_skipped_target_has_issues`（`internal/service/config_import_apply.go:32`，`CountWorkspaceIssues` 判空）。

### 2.2 结论：目标工作区必须为空

**`issues` 分组的导入前置条件是 `CountWorkspaceIssues(目标工作区) == 0`。** 不满足时 `POST /transfer/issues` 返回 `400 transfer_issues_target_not_empty`，一行字说明「请先创建一个空工作区再导入任务」，**整个 issues 分组不开始**（config 与 conversations 分组不受影响，可以单独先导）。

空工作区下：

- **`number` 原样保留**，随包写入；
- `issue_prefix` 由 V1 的 config 分组在同一次导入里写入（前置条件相同，都是「目标无任务」），所以目标端的 `DENE` 前缀与源端一致；
- 因此正文、评论、mention 标签里的 `DENE-240` 文本引用**自动全部正确**，一个字都不用改写;
- 导入收尾把水位顶上去：`UPDATE workspace SET issue_counter = GREATEST(issue_counter, (SELECT COALESCE(MAX(number),0) FROM issue WHERE workspace_id = $1)) WHERE id = $1`。这条语句必须在 `issues` 分组的**最后一片**写完后跑，且必须幂等（重导同一个包再跑一次结果不变）。

为什么把「目标必须为空」定成硬前置，而不是默默偏移：一个跑了半年的工作区里，`DENE-xxx` 这类标识符散落在**几千条正文与评论**里。偏移之后这些文本引用会指向目标工作区里**另一张真实存在的票**——不是死链，是**指错**。指错的引用读起来完全正常，没有任何视觉提示，人和智能体都会照着它去改错的东西。让用户先建一个空工作区，代价是一次操作；默认偏移的代价是一整个工作区的引用静默错位。

### 2.3 `--renumber`：显式要求时才做偏移

给逃生口，但必须用户显式打开：`multica transfer import --in <pkg> --renumber`。

- 偏移量 `offset = 目标工作区当前 issue_counter`（取号水位，不是 `MAX(number)`——用水位才能保证不撞上已删除票留下的号洞之外的未来取号）；
- 新号 `new_number = offset + source_number`。**保序、保持相对间隔、不压缩**——压缩到连续会让「源端 240 和 365 之间隔了多少张票」这个信息消失，而稀疏留洞对产品没有任何代价；
- 偏移后 `issue_prefix` 按 V1 规则**不会被写入**（目标非空），所以目标端渲染出来的是目标工作区自己的前缀，例如源端 `DENE-240` 在目标端叫 `MUL-1240`；
- 导入报告必须带完整映射表 `number_map[]`：`{ source_identifier: "DENE-240", target_identifier: "MUL-1240", target_issue_id: "..." }`，每张导入的票一行。CLI 把它写成 `<in>.number-map.csv` 落盘，不只是打在终端上——几千行的表滚过去等于没有。

### 2.4 正文里的 `DENE-xxx` 文本引用：不改写

**两种情况下都不改写正文里的纯文本编号引用。**

- 保号场景（2.2）不需要改写。
- 偏移场景（2.3）**明确不改写**，理由是：`DENE-240` 在正文里没有任何结构标记，它和分支名 `feature/DENE-240`、代码块里的字符串、外部系统单号、以及纯粹在讲历史的叙述文字长得一模一样。用正则全局替换必然误伤；不替换则正文里的引用全部指向错号。两害相权，我们选择「错得可见、且有完整映射表可查」，而不是「正则改得不可预测、且没人知道哪些被改坏了」。
- 这条代价必须写进 `--renumber` 的 CLI 确认提示里，要求用户敲 `yes` 才继续：「偏移后正文与评论里的 `<源前缀>-xxx` 文本引用将全部指向错误的任务，映射表见 `<file>`。」

被结构化引用的编号——`[DENE-240](mention://issue/<uuid>)`——**会**被正确重写，走第 3 节的 mention 规则，与本条不冲突。这是保号与偏移两种场景下唯一能自动修对的引用形态。

## 3. mention 链接重写

### 3.1 认哪些形态

`server/internal/util/mention.go` 的 `MentionRe` 是全仓唯一的 mention 解析器：

```
\[@?(.+?)\]\(mention://(member|agent|squad|issue|all)/([0-9a-fA-F-]+|all)\)
```

- 认 5 类：`member` / `agent` / `squad` / `issue` / `all`；
- **`mention://project/<uuid>` 不在这个正则里**。DENE-365 描述里把它列为要处理的形态，但服务端从不解析它，它只是前端渲染的可点链接。V3 **仍然要重写它的 uuid**（否则在目标端点开是死链），但它属于「链接类」，重写失败的降级形态与副作用类不同——见 3.3。
- `all` 不带 id，原样保留。

### 3.2 重写规则

| mention 类型 | 目标 id 怎么来 | 映射不到时 |
| --- | --- | --- |
| `member` | V2 §4.3 的 people map（源 user id → 邮箱 → 目标同邮箱 user） | 降级为纯文本 |
| `agent` | V1 身份键：普通智能体按 `name`，系统智能体按 `system_key`（从 `manifest.refs.agents` / `refs.system_agents` 反查） | 降级为纯文本 |
| `squad` | V1 身份键 `name` | 降级为纯文本 |
| `issue` | 本包内的 issue 目标 id（第 9 节的确定性 id 推导）。指向**包外**的 issue（源工作区里没被导出的票，或跨工作区引用）映射不到 | 降级为纯文本 |
| `project` | V1 身份键 `title` | 降级为纯文本 |
| `all` | 不动 | — |

重写在**导出端**做还是**导入端**做：**导入端**。导出时还不知道目标工作区里叫什么名字的智能体存不存在；而且导入端已经持有 people map 与 V1 导入报告，身份键反查是现成的。导出端只负责把 `manifest.refs` 索引填全（V2 已有 `TransferRefs`，V3 给它加 `members` / `squads` / `issues` 三个 map）。

### 3.3 映射不到时：降级为纯文本，不保留死链

**降级形态写死**：`[@主力工作-贝吉塔](mention://agent/<源uuid>)` → `@主力工作-贝吉塔`。丢掉链接语法，保留方括号里的标签文字（去掉外层 `[` `]`，`@` 前缀保留）。

这条为什么必须是纯文本而不是「留着死链，反正点不开」——理由是代码级的，不是审美：

1. **残留的不可解析 mention 会让整条线程的后续路由静默失效。** `routeConversationOwnersForRoot`（`internal/handler/comment.go:2848`）在有人回复一条线程时，会重新解析**线程根评论**的正文找 mention。`routeFirstExplicitRootMentionOwner` 一旦看到 `mention://agent/...`，就返回 `hasExplicitOwner = true`；解析失败时 `ok = false`，调用方直接 `return nil, true`——**「已处理，无触发」**。结果是：导入过来的线程，人在里面回复，本该唤醒的指派人 / 会话延续路由被这条死链挡住，什么都不会发生，而且不报错。这不是「链接点不开」这种可见小问题，是一整条线程永久哑掉。降级成纯文本后，`ParseMentions` 找不到 mention，回退路由恢复正常。
2. **误触发的风险虽小但后果不可逆。** 死链里的 UUID 会被 `GetAgentInWorkspace` / `GetSquadInWorkspace` 拿去在**目标工作区**里查。跨实例场景下源 UUID 撞上目标 UUID 的概率极低，但一旦撞上就是「回复一条历史评论，凭空拉起一个不相干的智能体去跑」。这条风险不值得为了保留一个点不开的链接而留着。

`project` 类 mention 没有副作用路径，但用同一条降级规则，不为它单开一个分支——两条规则比一条规则更容易在后续改动里跑偏。

### 3.4 报告与断言

- 每一条被降级的 mention 记一条报告行：`{ entity: "comment" | "issue", source_id, mention_type, source_ref_id, reason: "mention_unmapped" }`。报告按 `mention_type` 汇总计数，不逐条打在终端上。
- **实现 Stage 必须有这条断言**：导入完成后，扫描本次写入的全部 `issue.description` 与 `comment.content`，断言其中**不存在任何 `mention://` 链接的 id 不能在目标工作区解析**。这条断言是本节的验收，不是可选的健康检查。

## 4. 父子与阶段

### 4.1 两遍写入是硬要求，不是优化

库里有两条真实外键，都早于「不加外键」这条规则：

- `issue.parent_issue_id UUID REFERENCES issue(id) ON DELETE SET NULL`（`001_init.up.sql:65`）
- `comment.parent_id UUID REFERENCES comment(id) ON DELETE CASCADE`（`017_comment_parent_id` + `018_comment_parent_cascade`）

所以：**父行不存在时插入子行会直接违反外键**。分片导入的顺序又不能保证父在子之前（第 8 节按 issue 聚合分片，但父子 issue 可能落在不同片）。

写入顺序固定为：

1. **第一遍**：本片全部 `issue` 以 `parent_issue_id = NULL` 插入，`stage` / `position` 原样写入；
2. **第一遍**：本片全部 `comment` 以 `parent_id = NULL` 插入（`issue_id` 此时已存在，因为同片的 issue 先写）；
3. **第二遍**：`finalize: true` 的最后一次请求里，用包内携带的 `(source_id, source_parent_id)` 对，把两张表的父指针一次性 `UPDATE` 回填。

回填时父指针解析不到的处置分两种，**不要混用**：

- `issue.parent_issue_id`：父 issue 不在包内（或被跳过）→ 保持 `NULL` 并记 `parent_unmapped`。issue 的父子是一层业务归属，硬挂到祖父会伪造一条不存在的从属关系。
- `comment.parent_id`：**必须沿源端父链上溯到最近的包内祖先**，一路到根都找不到才置 `NULL` 并记 `parent_unmapped`，每次上溯记一条 `parent_reparented_to_ancestor`（带跳过的层数）。理由与实测依据见 11.7——墓碑过滤和 `commentHardCap` 截断都会让父评论落在包外，置空会把整条线程拍平。

`finalize` 阶段的回填必须幂等：重导同一个包时，第二遍写的是同样的值，`UPDATE` 结果不变。

### 4.2 `stage` 与 `position`

- `stage`（`pgtype.Int4`，可空）原样写入。它只是一个分组序号，没有跨实例含义漂移。
- **`stage` 的语义后果要写进报告**：阶段屏障（`issue_child_done.go`）在子票转 `done` 时判断同 stage 是否全部收口，然后唤醒父票。导入的历史票如果本来就都是 `done`，不会触发任何唤醒——因为我们不走状态变更路径（第 7 节）。这是对的。但如果导入的是**未完成**的分阶段票，目标端的阶段屏障会从导入那一刻起正常生效，这也是对的。两种情况都不需要特殊处理，写在这里是为了让实现者不去"补一次唤醒"。
- `position` 是 `FLOAT NOT NULL`，原样写入。空工作区下不会与任何已有票的排序冲突。

## 5. 多态指派

### 5.1 映射表

`issue.assignee_type` 的 CHECK 是 `('member', 'agent', 'squad')`（`084_squad.up.sql:33`）；`creator_type` 是 `('member', 'agent')`（`001_init.up.sql`，NOT NULL）。

| 引用 | 目标 id 怎么来 | 映射不到时 |
| --- | --- | --- |
| `assignee_type = 'member'` | V2 §4.3 people map | **降级为未指派**（`assignee_type` 与 `assignee_id` 都置 NULL），报告 `assignee_unmapped` |
| `assignee_type = 'agent'` | V1 agent 身份键（`name` / `system_key`） | 同上 |
| `assignee_type = 'squad'` | V1 squad 身份键 `name` | 同上 |
| `creator_type` / `creator_id` | 同上三条规则 | **兜底为导入者**（`creator_type = 'member'`，`creator_id = 导入者`），报告 `creator_unmapped`。列是 NOT NULL，不能置空 |
| `comment.author_type` / `author_id` | `member` / `agent` 同上；`author_type = 'system'` 不映射，原样写入 | 兜底为导入者，报告 `comment_author_unmapped` |
| `comment.resolved_by_type` / `resolved_by_id` | 同上 | 三个 `resolved_*` 列**一起置空**——`comment_resolved_consistency` CHECK（`069_comment_resolved_at.up.sql:7`）要求它们同生同灭，只清 id 会违反约束。报告 `resolution_actor_unmapped` |
| reaction 的 `actor_type` / `actor_id` | 同上 | **丢掉该条 reaction**（1.2 已述） |
| `issue.properties` 里 `actor` / `multi_actor` 的值 | 同上（值形如 `"member:<uuid>"`） | `actor` 删整个键；`multi_actor` 删那一条引用，删空后删键。报告 `property_actor_unmapped`（1.2.1） |

### 5.2 为什么指派降级成未指派而不是给导入者

把一张原本指派给「主力工作-贝吉塔」的票改指给导入者，会让「我的任务」列表里凭空多出一堆不属于自己的活。未指派虽然也丢信息，但它是**诚实的空**，用户在票列表里一眼能看出哪些需要重新指派。报告里按原指派对象汇总（「12 张票原指派给 主力工作-贝吉塔，未在目标工作区找到」），用户可以批量补。

### 5.3 指派重建不得触发运行

这是第 7 节的一个具体实例，但值得在这里点名：产品里给智能体指派一张票**会起一轮任务**（这正是 `--no-start` 存在的原因）。V3 导入写的是 `assignee_id` 列本身，不走指派接口、不发 `issue:assigned` 事件，所以不会入队。测试断言见 7.3。

## 6. 收件箱（`inbox_item`）

### 6.1 结论：不迁历史通知

**不导出 `inbox_item`。** 三条理由，第一条是决定性的：

1. **它全部由运行记录产生。** 全仓 `CreateInboxItem` 的调用点只有五处，全在 `internal/service/task.go`（任务完成、quick-create 完成/失败、重试耗尽）和 `internal/service/autopilot*.go`（autopilot 完成、配额告警）。每一条 `inbox_item.details` 里都嵌着 `task_id` / `agent_id` / `autopilot_run_id`——而这些运行记录按第 1 节**不迁移**。搬过去的通知点开就是 404。
2. 已读 / 未读、归档状态是「我在源环境处理到哪了」的个人工作流状态，跨实例重放没有意义。
3. 用户的原始诉求是「我的任务看得见」，那由第 5 节的指派重建满足，不需要通知历史。

用户会看到：**导入后收件箱是空的，从导入后的新活动开始积累。** 这句话要出现在 CLI 完成提示和导入报告里。

### 6.2 但必须重建 `issue_subscriber`

这一条是本节真正的交付物，不写会出事。

`issue_subscriber` 决定「这张票有新动静时通知谁」。产品里它由**事件总线监听器**自动写入：`cmd/server/subscriber_listeners.go` 的 `registerSubscriberListeners` 订阅 `protocol.EventIssueCreated` 等事件，在建票时把 creator 与 assignee 写成订阅者。

而 V3 导入按第 7 节**刻意绕开 `h.publish`**，所以这些监听器**一个都不会跑**。后果：导入过来的几百张票，`issue_subscriber` 全空；此后任何人在这些票上评论、任何智能体跑完一轮，**谁都收不到通知**。收件箱不只是「历史空」，而是**永远不会有这些票的内容**。这就把「不迁历史通知」这个可接受的取舍，变成了一个不可接受的功能缺失。

**所以导入时必须显式重建两条订阅**，与监听器写的是同一批行：

- `(issue, creator_type, creator_id, reason: 'creator')`
- `(issue, assignee_type, assignee_id, reason: 'assignee')`，当 assignee 与 creator 不同且 `assignee_type ∈ (member, agent)` 时（`isAssignmentRecipientType` 的口径：squad 是路由对象，没有收件箱）

不重建的部分：源端的 `commenter` / `mentioned` / `manual` / `delegated` 订阅行不导出。理由是这些行的 actor 映射失败率最高（历史评论者可能根本不在目标工作区），而它们的价值远低于前两条——creator 与 assignee 覆盖了「谁该收到这张票的动静」的绝大多数。用户需要更多订阅者时，产品里本来就有订阅按钮。

重建写入走**同一条无副作用路径**（`INSERT ... ON CONFLICT DO NOTHING`），不发事件。

### 6.3 替代方案与代价（备查）

如果后续有人要求迁历史通知，代价是：要么一并迁 `agent_task_queue` 与 `task_message`（把运行态搬成历史数据，`runtime_id` / `session_id` / `work_dir` 全是死引用，且 `agent_task_queue` 有 50+ 列、多条运行态语义），要么把 `details` 里的运行引用逐条剥掉、通知变成不可点开的纯文本条目。前者是另一个量级的立项，后者交付的是一堆点不开的历史条目。两个都不值得，本页不做。

## 7. 无副作用写入

### 7.1 必须绕开的现有写路径

| 写路径 | 副作用 | V3 怎么绕 |
| --- | --- | --- |
| `service.AllocateIssueNumber` → `IncrementIssueCounter` | 编号自增 | 不调用。`number` 随包带入，收尾统一顶水位（第 2 节） |
| `db.CreateIssue` / `CreateIssueWithOrigin` | 不接受 `created_at`（列有 `DEFAULT now()`）；`last_activity_at = now()` 写死在 SQL 里 | 新增专用 `TransferInsertIssue`，显式传 `created_at` / `updated_at` / `last_activity_at` |
| `db.CreateComment` | 同一条语句里 `UPDATE issue SET updated_at = now(), revision = revision + 1, last_activity_at = GREATEST(..., now())`（`comment.sql:440`）。用它导入会把每张票的时间戳全部拍成导入时刻 | 新增专用 `TransferInsertComment`，只插 `comment`，**不碰 issue 行**。issue 的时间戳由 `TransferInsertIssue` 一次写对 |
| `handler.CreateComment` | `h.publish(EventCommentCreated)` → WebSocket 广播 + 事件总线订阅者规则；`AutoUnresolveThreadOnReply`；`triggerTasksForComment` → mention 解析 → `agent_task_queue` 入队 | 完全不走 handler 写路径。导入端点只调 service 层的专用插入 |
| `handler.CreateIssue` / `UpdateIssue` / `AssignIssue` | `h.publish(EventIssueCreated / EventIssueAssigned)` → 订阅者监听器 + 指派起跑 | 同上。订阅者由第 6.2 节显式重建 |
| `entitlement` 任务数配额（`CheckIssueCreateCapacity` / `IssueLimitReachedError`） | 达上限时拒绝建票 | **已核实（11.1）**：自建默认档这个 provider 是 nil，配额恒不生效。导入路径本来就不调 `CreateIssue` / `AllocateIssueNumber`，因此**也不额外调用** `CheckIssueCreateCapacity`；但必须在写入前读一次 `ResolveIssueCountPolicy` 并把 `{action, limit, used}` 回显进报告，`action == enforce` 且会越线时拒绝导入。口径见 11.1 |
| `DownloadAttachment` / 附件上传 | 无额外副作用 | 复用 V2 的 `ImportTransferAttachment`，只是挂载列从 `chat_*` 换成 `issue_id` / `comment_id` |

**触发器：已重新核对（DENE-383，2026-09-17），原结论的事实部分是错的，安全结论成立。**

原文写「`server/migrations/` 里没有任何 `CREATE TRIGGER ... ON issue`」——**错**。真实情况：

- `issue` 上有两个库级触发器：`trg_issue_delete_dirty_hourly`（`BEFORE DELETE`）与 `trg_issue_project_dirty_hourly`（`BEFORE UPDATE OF project_id`），建于 `102_task_usage_hourly_pipeline.up.sql:169` / `:226`；`243_workspace_teardown_dirty_trigger_guard.up.sql` 把 DELETE 那个重建成带 `WHEN (current_setting('multica.workspace_teardown', true) IS DISTINCT FROM 'on')` 守卫的版本。084 的 dashboard 同名系列已被 `103_drop_legacy_daily_rollups.up.sql:116`-`:119` 删除，所以只 grep `.up.sql` 里的 `CREATE TRIGGER` 会同时漏看守卫重建和后续删除，得出的数量两头都不对。
- `comment` 与 `attachment` 上**没有**任何触发器。

核实方式是跑完迁移查系统表，不是 grep：本机建库 `multica_dene383` → `DATABASE_URL=postgres://multica@localhost:5432/multica_dene383?sslmode=disable go run ./cmd/migrate up`（跑到 `489_issue_draft_policy`）→ 查 `pg_trigger` 里 `NOT tgisinternal` 的行。全库共 9 个非内部触发器，落在 `issue` 上的就是上面两个，`comment` / `attachment` 各 0 个。

对 V3 的影响：

- **`INSERT` 仍然安全。** 两个触发器都不挂在 INSERT 上，第 7.2 节的专用插入不会引发库级副作用——这是原结论真正要用到的部分，它成立。
- **新增一条硬约束：`project_id` 只能在 `TransferInsertIssue` 那一条 `INSERT` 里写对，不得用「先插入、后 `UPDATE issue SET project_id = ...`」的两遍写法**，否则每张票都会触发 `trg_issue_project_dirty_hourly`，往 `task_usage_hourly_dirty` 里灌一批脏键。第 4 节的两遍写入只回填 `parent_issue_id`（`TransferBackfillIssueParent`），不碰 `project_id`，现有契约形状是安全的；实现 Stage 不要顺手把 `project_id` 挪进回填。
- 导入失败请用事务 `ROLLBACK` 而不是 `DELETE FROM issue` 善后。真要删已提交的行时 `BEFORE DELETE` 会逐行入队脏键，属已知开销，不影响正确性。
- 这条核对以后按同样口径重跑（查 `pg_trigger`，不是 grep），它是「直接写库安全」这个前提的全部依据。

### 7.2 专用 SQL 的形状

沿用 `pkg/db/queries/workspace_transfer.sql` 已有的写法（V2 的 `TransferInsertChatSession` / `TransferInsertChatMessage` / `TransferInsertAttachment` 就是这个形状）：

- 显式 `id`（第 9 节的确定性推导）；
- 显式 `created_at` / `updated_at`；
- `ON CONFLICT (id) DO NOTHING`，`:execrows` 返回 0 表示已存在 → 报告计 skipped；
- 每张表一条语句，不写 CTE 联动其他表。

新增：`TransferInsertIssue`、`TransferInsertComment`、`TransferInsertIssueLabel`、`TransferInsertCommentReaction`、`TransferInsertIssueReaction`、`TransferInsertIssueSubscriber`、`TransferBackfillIssueParent`、`TransferBackfillCommentParent`、`TransferBumpIssueCounter`。`TransferInsertAttachment` 加 `issue_id` / `comment_id` 两个可空参数。

### 7.3 测试断言（实现 Stage 必须落地，任一失败阻断合并）

沿用 V2 §8.10 的做法并扩展：

1. **零入队**：导入一个含「指派给智能体的 `todo` 票 + 正文里 @ 了另一个智能体的评论 + 线程根 @ 了第三个智能体」的包，断言导入后 `agent_task_queue` 行数为 **0**。这是本节的核心断言。
2. **零广播**：断言导入过程中事件总线上没有 `issue:created` / `issue:assigned` / `comment:created` 事件。
3. **时间戳保真**：断言导入后每张 issue 的 `created_at` / `updated_at` / `last_activity_at` 与包内一致，误差 0；每条 comment 同理。
4. **订阅者重建**：断言每张导入的 issue 恰有 creator 与 assignee 两条 `issue_subscriber`（两者相同时一条），`reason` 分别为 `creator` / `assignee`。
5. **幂等**：同一个包导入两次，issue / comment / reaction / subscriber 行数不变，`issue_counter` 不变。
6. **编号前置**：目标工作区已有任务时，`POST /transfer/issues` 返回 400 `transfer_issues_target_not_empty` 且**不写任何行**。
7. **mention 全解析**：第 3.4 节的断言。
8. **父指针**：导入一个「子票在父票之后的分片里」的包，断言 finalize 后 `parent_issue_id` 全部正确；导入一个「父票不在包内」的包，断言子票 `parent_issue_id IS NULL` 且报告有 `parent_unmapped`。
9. **`status_change` 评论不被拒**：断言 `type = 'status_change'` 与 `type = 'system'` 的评论成功写入（防止实现者复用 `isClientAuthorableCommentType`，见 1.5）。
10. **墓碑保序**：导入一个含墓碑父评论的线程，断言子回复的 `parent_id` 指向墓碑而不是被拍平。

以上测试输入走 `httptest` 假读接口（与 V2 §5.4.6 同口径），不依赖真实官方云。DB 相关断言按 CLAUDE.md 用 `server/internal/testutil` 的 `dbfx.*` 与 `testutil.Call(...)`，不开新的 `INSERT ... RETURNING` / `httptest.NewRecorder()` 四件套。

## 8. 体积与分片

### 8.1 复用 V2 常量，不另立

`internal/service/transfer_bundle.go` 已有的四个常量原样复用：

| 常量 | 值 | V3 用途 |
| --- | --- | --- |
| `TransferMessageShardMaxBytes` | 16 MiB | issue 分片与 comment 分片各自的字节上限 |
| `TransferMessageShardMaxRows` | 5000 | 同上的行数上限 |
| `TransferConversationsMaxBytes` | 20 MiB | `POST /transfer/issues` 的请求体上限 |
| `TransferAttachmentMaxBytes` / `TransferAttachmentBodyMax` | 25 MiB | 附件请求与文件体上限，与 V2 共用同一条附件上传路径 |

不另立常量的理由：分片上限是由**请求体上限与内存占用**决定的，与分片里装的是聊天消息还是任务评论无关。多一套常量只会让两套值慢慢漂开。

### 8.2 聚合规则

- 一张 issue 的**全部评论**放在同一片，与 V2「一个会话的消息不跨片」同构；
- 单张 issue 的评论超过上限时单独成片，允许突破行数上限但字节仍须 ≤ 16 MiB；仍超出则按 `created_at` 顺序拆成多片，每片带相同 `issue_source_id`，导入端按 id 幂等，拆片安全；
- issue 分片与 comment 分片编号一一对应：`issues/issues-0001.jsonl` 只含 `issues/comments-0001.jsonl` 里评论所属的 issue；
- 父子跨片是允许的（第 4 节的两遍写入就是为此）。

### 8.3 估算口径

`multica transfer export --estimate` 新增 issues 部分：

```
估算字节 ≈ Σissue ( 800 + 描述字节 + 评论数 × ( 500 + 平均评论字节 ) ) + issue/comment 附件文件体总字节
800 / 500 = 行内 JSON 键与元数据开销（issue 行的字段比 comment 行多）
描述字节 / 平均评论字节：中文按 UTF-8 每字 3 字节计
评论数：抽样每张 issue 的一次 ListComments（上限 2000）取实际条数
附件：按 size_bytes 求和，只计第 1.2 节会导出文件体的类型
```

实际压缩率**未确认**（与 V2 §7.1 同状态）。实现 Stage 用 `--estimate` 对真实工作区跑一次并把数字记进 PR 描述。

### 8.4 导出端的分页与三个真实坑

CLI 只能用读接口，这里有三条必须按写的方式做，否则会静默丢数据。三条都在官方云上实测过，实测口径见 11.4 / 11.5：

**issue 列表分页。** `GET /api/issues` 是 `LIMIT/OFFSET`，`limit` 上限 100，且是**静默钳位**——服务端把超限值改写成 100 而不是报错（`internal/handler/issue.go:1253`、`:1260`，`ListIssues`），所以导出端不能指望靠 400 发现自己越界。响应体带 `total` / `has_more` / `limit` / `offset`。默认不带任何过滤时返回**全部状态**（含 `done` / `cancelled`）。

翻页必须用 `?sort=created_at&direction=asc`。两处原文写错、这里一并纠正：

- **参数名是 `direction`，不是 `dir`**（`issue.go:1357`）。写成 `dir` 会被静默忽略，落回该排序列的默认方向。
- **不带 `sort` 时的默认排序是 `position`，不是 `last_activity_at`**（`issue.go:1310`）。`position` 由拖拽和新建改写，同样不能用来翻页；`last_activity_at` 只是它们之中更明显的一个坏选择。

用 `created_at` 升序是对的：ORDER BY 末尾固定追加 `i.created_at DESC, i.id DESC` 作为唯一末位键（`issue.go:1569`），升序翻页下新建的票排在最后、不会顶掉前面的页。

**`total` 不能当「导完了」的断言。** 它是一次独立计数，导出期间源端并发建票就会变。实测（2026-09-17，官方云 `api.multica.ai`，`deneb-3029` 工作区）：`--sort created_at --direction asc --limit 100` 翻 4 页拿到 **387 个互不重复的 id**（`sort | uniq -d` 为空），同一时段 `total` 从 384 涨到 390。收尾条件用 `has_more == false`，完整性用 id 集合去重来保证，不要拿 `total` 做相等断言。

**评论分页的 `since` 边界。** `GET /api/issues/{id}/comments` 默认路径返回**最新 2000 条**（`commentHardCap = 2000`），没有向前翻页的游标。超过 2000 条的 issue 必须用 `?since=<RFC3339Nano>` 反复拉：该查询是 `created_at > $3 ORDER BY created_at ASC, id ASC LIMIT 2001`（`comment.sql:61`）。

坑在于谓词是 `created_at > $3` 且**没有 id 作为并列键**：如果第 N 页的最后一条与下一条 `created_at` **完全相同**，以它的时间戳作为下一页的 `since` 会把那条并列的评论**永久跳过**。处置：每页取完后，把 `since` 设为**本页最后一条的 `created_at`**，并把本页已见的 comment id 记入一个集合；下一页返回的行按 id 去重后再追加。这样并列行会在下一页被重新看到并去重，不会丢。**这条不是理论风险**——批量导入或同一轮任务连发多条评论时，`created_at` 撞在同一微秒是现实情况。

**导出端不得传 `fold`。** `fold=true` 会把每条**已解决**线程折叠成「根 + 结论」，其余回复根本不返回（`internal/handler/comment.go:477` 起；服务端默认 `false`，只有显式传 `true` 才折叠）。CLI 的默认列表视图是折叠的，导出端必须走不带 `fold` 的原始路径，否则已解决线程的中间评论会被静默丢光——而这正是「任务同步过去了但讨论少了一半」的成因。`fold` 与 `since` / `tail` / `roots_only` 互斥，传了会 400，所以 `since` 翻页路径天然安全。

`--estimate` 与正式导出共用同一套翻页代码，避免两处对边界的理解不一致。

### 8.5 断点续传

与 V2 §7.3 同机制，`<out>.partial/state.json` 增加已完成的 issue id 集合与每张 issue 已拉到的评论水位（`last_created_at` + 已见 id 集合）。未完成的 issue 从头重拉评论（单张 issue 重拉代价小）。

## 9. 包结构与版本

### 9.1 结论：`schema_version` 顶到 2

不是「在 V1 结构上加 `issues` 分组」，也不是新开一个 format。

- 外层 `format` 保持 `multica.workspace-transfer`（V2 的值），`schema_version` **从 1 顶到 2**；
- `schema_version: 2` 的包多出一个 `issues/` 目录和 `manifest.options.include` 里的 `issues` 分组；
- 内层 `config.json` 继续是 `multica.workspace-config` / `schema_version: 1`，**V1 的 `ConfigBundleSchemaVersion` 常量不动**（与 V2 §6.4 同口径：跨实例需要的新东西放外层文件，不往 V1 bundle 里塞）。

为什么不新开 format：V3 包的 config 与 conversations 两部分与 V2 **逐字节同构**，新开 format 会让导入端出现两条几乎相同的解析路径。顶 `schema_version` 加一个可选目录，是唯一不产生并行实现的做法。

### 9.2 兼容矩阵（`--in` 的旧包不能失效，这是硬要求）

| 场景 | 结论 |
| --- | --- |
| V3 导入器读 `schema_version: 2` 包 | 能。完整路径 |
| V3 导入器读 `schema_version: 1` 包（V2 导出的） | **必须能**。`issues/` 目录不存在 → `issues` 分组视为未包含，其余照常。实现上就是「目录缺失 = 空分组」，不需要版本分支 |
| V3 导入器读裸 V1 JSON（`--in <v1.json>`） | **必须能**。沿用 V2 §6.4 的规则不变 |
| V2 导入器读 `schema_version: 2` 包 | **不能**，返回 `transfer_bundle_version_unsupported`（V2 §6.4 已定：不识别的外层版本不降级解析）。这是可接受的——目标端是我们自己的 `kun` 实例，升级它是我们能控制的 |
| V3 导出器产出 `schema_version` | 未带 `--include issues`（默认）时产出 **1**；带 `issues` 时产出 **2**。这样日常导出的包仍然能被未升级的目标端读，只有真的要搬任务时才需要两端都是新版 |

最后一条是本节的关键设计：**版本号跟着内容走，不跟着 CLI 版本走**。

### 9.3 `include` 分组与默认值

```
multica transfer export --include config,conversations,attachments          # 默认，与 V2 相同
multica transfer export --include config,conversations,attachments,issues   # 带任务
```

**`issues` 默认不开。** 三条理由：

1. 带任务的包体积是不带的数倍到数十倍，日常「把配置搬过去」的场景不该被迫等它；
2. `issues` 分组有「目标工作区必须为空」这个硬前置（第 2 节），默认开会让绝大多数导入直接 400；
3. 默认不开 = 默认产出 `schema_version: 1` = 默认兼容未升级的目标端（9.2 末条）。

`--include issues` 时 CLI 必须提示：「任务分组要求目标工作区没有任何任务。」

### 9.4 目录与行格式

```
manifest.json                       schema_version: 2
config.json                         V1 bundle 原样
people.json                         V2 §4.3
runtime_profiles.json               V2 §4.4
preferences.json                    V2
conversations/sessions-*.jsonl      V2
conversations/messages-*.jsonl      V2
issues/issues-0001.jsonl            每行一张 issue                   ← V3 新增
issues/comments-0001.jsonl          每行一条评论，按 issue 聚合        ← V3 新增
issues/relations.jsonl              标签关联与 reaction               ← V3 新增
attachments/index.jsonl             V2 的行加 issue_id / comment_id
attachments/blobs/<sha256>          V2
secrets_omitted.json                全包汇总
```

`issues/issues-*.jsonl` 一行：

```json
{"source_id":"01a0ab1b-cea6-7196-9bf5-bdb2faa6357e","number":365,"title":"【Stage 6·契约】V3 任务迁移契约","description":"...","status":"todo","status_category":"todo","priority":"high","assignee_type":"agent","assignee_id":"0df3cdc8-...","creator_type":"agent","creator_id":"1cbd7845-...","parent_issue_id":"01a0a4b6-...","project_id":"92a1a586-...","position":-19,"stage":6,"start_date":null,"due_date":null,"created_at":"2026-09-16T16:45:21Z","updated_at":"2026-09-16T16:45:21Z","last_activity_at":"2026-09-16T16:45:21.440168Z","metadata":{},"properties":{}}
```

`issues/comments-*.jsonl` 一行：

```json
{"source_id":"01a0ab16-...","issue_id":"01a0ab1b-...","author_type":"member","author_id":"c924599a-...","content":"聊天同步过去了，但收件箱、我的任务都没有同步过去","type":"comment","parent_id":null,"created_at":"2026-09-16T16:40:00Z","updated_at":"2026-09-16T16:40:00Z","resolved_at":null,"resolved_by_type":null,"resolved_by_id":null,"deleted_at":null,"attachment_ids":[]}
```

`issues/relations.jsonl` 一行（三种 `kind` 混排，导入端按 `kind` 分发）：

```json
{"kind":"issue_label","issue_id":"01a0ab1b-...","label_resource_type":"issue","label_name":"契约"}
{"kind":"issue_reaction","issue_id":"01a0ab1b-...","actor_type":"member","actor_id":"c924599a-...","emoji":"👍","created_at":"2026-09-16T17:00:00Z"}
{"kind":"comment_reaction","comment_id":"01a0ab16-...","actor_type":"agent","actor_id":"1cbd7845-...","emoji":"✅","created_at":"2026-09-16T17:01:00Z"}
```

`manifest.refs` 在 V2 的 `agents` / `system_agents` / `projects` 之外新增 `members`（源 user id → 邮箱）、`squads`（源 id → name）、`issues`（源 id → `{number, identifier}`）、`issue_statuses`（key → category）。这四个 map 是导入端做 mention 重写（第 3 节）与状态降级（1.4）的全部依据，随每个分片一起上传，服务端不需要记住前一个请求的结果——与 V2 的无状态导入一致。

### 9.5 确定性 id

`transfer_ids.go` 新增三个命名空间常量，与已有三个同样「一旦发布不得修改」：

```
NSTransferIssue            → TransferIssueID(targetWorkspaceID, sourceID)
NSTransferComment          → TransferCommentID(targetWorkspaceID, sourceID)
NSTransferReaction         → TransferReactionID(targetWorkspaceID, sourceID)
```

推导式与 V2 一致：`UUIDv5(NS, 目标工作区 id + "/" + 源 id)`。issue 附件复用已有的 `NSTransferAttachment`（同一张 `attachment` 表，同一个命名空间，避免两个空间对同一张表发号）。

`issue_subscriber` 与 `issue_to_label` 没有独立 id 列（前者按 `ON CONFLICT` 的自然键，后者是复合主键），不需要命名空间。

### 9.6 服务端端点

新增一个，权限与 V2 相同（目标工作区 `owner` / `admin`，agent actor 一律 403）：

| 端点 | 作用 |
| --- | --- |
| `POST /api/workspaces/{id}/transfer/issues` | 接收一个任务分片（issue 行 + comment 行 + relations 行 + 本片 `refs`），幂等写入。支持 `dry_run`、`finalize`。`finalize: true` 时执行第 4.1 节的父指针回填、第 2.2 节的 `issue_counter` 顶水位、第 6.2 节的订阅者重建，并发一次工作区级任务列表失效事件 |

导入顺序固定：`transfer/config`（必须先 apply 成功，否则标签 / 项目 / 智能体 / 状态目录都还不存在）→ `transfer/issues`（逐片）→ `transfer/conversations`（逐片）→ `transfer/attachments`（逐个）→ `transfer/issues` 带 `finalize: true`。

附件放在 issues 之后：`attachment.comment_id` 有外键指向 `comment(id)`（`029_attachment.up.sql:5`），评论必须先在。

## 10. 秘密边界

### 10.1 沿用的部分

- `transfer_redact.go` 的 11 条高置信模式（`private_key` / `anthropic_key` / `openai_key` / `github_token` / `slack_token` / `aws_access_key` / `google_api_key` / `jwt` / `multica_token` / `bearer` / `assignment`）原样跑在：`issue.title`、`issue.description`、`comment.content`、以及文本类 issue/comment 附件上。**一条都不减**，不提供关闭开关（V2 §5.3 的口径不变）。
- 命中片段原位替换为 `[REDACTED:<kind>]`，每处登记一条 `secrets_omitted`，`hint` 只含 `kinds` 与 `count`，不含片段、前缀或长度。
- 文本类附件命中任何模式 → **整个文件不导文件体**，只留元数据（V2 §3.4）。

### 10.2 V3 新增的两处

任务正文与评论里夹带密钥的概率确实比聊天高——粘日志、粘 `.env`、粘 curl 命令都发生在任务讨论里。但**现有模式表已经覆盖了这些形态**（`assignment` 模式匹配 `KEY=value` / `"key": "value"` / `key: value` 且值长度 ≥ 8，覆盖了粘 `.env` 的主要形态；`bearer` 覆盖粘 curl）。所以结论是：**模式表不扩，但扫描面要扩两处**。

1. **`issue.metadata` 与 `issue.properties` 两个 JSONB 值袋**必须跑 V1 的**兜底键名清洗**（`token` / `secret` / `password` / `api_key` / `credential` 等词表）。这两个袋子是用户和智能体可以自由写入的 KV，V1 的清洗逻辑对 `config.json` 整棵树跑，V3 要把它也套在这两棵子树上。键名命中即整个值置 `null` 并登记，不做正文级的片段替换——JSONB 值可能是结构化的，片段替换会破坏结构。
2. **`comment.content` 的扫描顺序必须在 mention 重写之前**。理由：重写会改变字符串，如果先重写再扫描，`secrets_omitted` 里记录的位置与实际不符；更实际的是，重写可能把一段 `[REDACTED:jwt]` 之后的内容拼接进不同的上下文，让 `assignment` 模式的边界匹配行为改变。定死顺序：**扫描 → 重写**，两步都在导入端按同一份内容做一次。

> 注：扫描发生在**导出端**（CLI，与 V2 一致，包内不得出现明文），mention 重写发生在**导入端**（第 3.2 节）。所以这两步天然不在同一侧，上面这条顺序约束实际是自动满足的。写在这里是为了防止实现者出于性能考虑把扫描挪到导入端——**不允许**，那会让包本身带着明文密钥落盘。

### 10.3 验收断言（在 V2 §5.4 基础上追加）

11. **任务金丝雀**：一张 issue 描述含 `ghp_` 开头的假 token；一条评论含 `-----BEGIN OPENSSH PRIVATE KEY-----` 假私钥块与 `API_KEY=CANARY_ISSUE_9c0d`；一张 issue 的 `metadata` 里有 `{"deploy_token": "CANARY_META_1e2f"}`；一个挂在评论上的 `.env` 附件含 `SECRET=CANARY_ICOMMENT_3a4b`。
12. 对**解压后的整个包**（所有文件逐字节）做子串断言：上述所有金丝雀原值不出现，`***` 不出现。
13. 每处被脱敏的位置在 `secrets_omitted.json` 中恰有一条记录；含金丝雀的 `.env` 附件没有文件体。
14. 导入端拒收：`transfer/issues` 请求体里 `metadata` 带未清洗的秘密键 → 400，且不写任何行。

## 11. 已核实项（原「未确认项」）

V2 §8 的 10 条未确认项**继续有效**，不在本轮范围内。本页新增的 7 条已在 **DENE-383（2026-09-17）** 逐条核实，全部给出结论；核实基线与只读边界见开头「核对基线」一节。官方云侧只做读，未写入任何数据。

**一页结论**

| # | 问题 | 结论 | 实现侧要做的事 |
| --- | --- | --- | --- |
| 11.1 | 自建实例的任务数配额 | 自建默认档 provider 为 nil，配额恒不生效 | 不调用配额检查，但报告必须回显策略；`enforce` 且会越线时拒绝导入 |
| 11.2 | `issue_dependency` 读接口 | 没有 | 不导出 |
| 11.3 | 前端是否读 `acceptance_criteria` / `context_refs` | 没有 | 不导出 |
| 11.4 | 官方云 `GET /api/issues` | `limit` 静默钳 100；支持 `sort=created_at` + `direction=asc`；未观察到速率限制 | 按 8.4 改写后的口径翻页 |
| 11.5 | 官方云 `GET /api/issues/{id}/comments` 的 `since` | 支持，RFC3339 与 RFC3339Nano 都收 | 按 8.4 实现去重翻页；不传 `fold` |
| 11.6 | 官方云属性类型集合 | 与 `kun` 逐字相同（9 种） | 白名单投影安全；`property_type_unknown` 保留为防御分支 |
| 11.7 | 官方云是否返回墓碑评论 | 源码两侧一致「返回」，但**没拿到正样本** | 父指针无条件实现「上溯最近可解析祖先」 |

### 11.1 任务数配额在自建实例上的行为 — 已核实：默认不生效

**结论：未配 `MULTICA_CLOUD_URL` 的自建实例上，`entitlement.Provider` 是 `nil`，`CheckIssueCreateCapacity` 恒返回 nil，任务数配额根本不存在。**

- `entitlement.New` 在 `BaseURL` 为空时返回一个 `enabled` 为零值的客户端（`internal/entitlement/client.go:58`-`:60`），`Enabled()` 因此为 false（`client.go:95`）；`cmd/server/router.go:459` 只在 `entitlementClient.Enabled()` 为真时才把它挂到 `h.IssueService.Entitlements`（`router.go:462`）。provider 保持 nil。
- provider 为 nil 时 `ResolveIssueCountPolicy` 直接返回 `ActionOff`（`internal/service/issue_limit.go:34`-`:35`），`CheckIssueCreateCapacity` 立刻返回 nil（`issue_limit.go:67`），`AllocateIssueNumber` 也不做任何计数。
- 自建默认就是空值：`docker-compose.selfhost.yml:120` 写的是 `${MULTICA_CLOUD_URL:-}`，`.env.example:216` 为空，Helm 默认渲染出 `MULTICA_CLOUD_URL: ""`（`scripts/helm-config.test.sh:36` 就是断言这一点）。
- **即使自建实例配了 `MULTICA_CLOUD_URL` 也基本拦不住**：Cloud 不可达、策略过期、版本回退、`observe` 档，全部 fail-open 成 `ActionOff`（`client.go:97` 的 Gate 分支表 + `issue_limit.go:44`-`:57` 的 `default` 分支）。只有「`enforce` 且 `limit > 0`」这一种组合会真的拦票。

实测（本机，2026-09-17）：

```
(cd server && DATABASE_URL=postgres://multica@localhost:5432/multica_dene383?sslmode=disable \
  go test ./internal/service -run 'IssueCountPolicy|IssueCountLimit' -count=1 -v)
# PASS TestResolveIssueCountPolicyUsesOnlyCloudInstruction
# PASS TestIssueCountLimitSerializesConcurrentCreatesAndDeleteFreesCapacity
(cd server && go test ./internal/entitlement \
  -run 'TestDefaultConfigIsDisabledAndPerformsNoIO|TestConnectedConfigValidation|TestColdFailuresFailOpen' -count=1 -v)
# PASS（含 12 个 fail-open 子用例：unauthorized / not_found / server_error / 过期 TTL / cloud_observe_action ...）
```

前一个测试里的 `disabled := ResolveIssueCountPolicy(ctx, nil, workspaceID)` 断言就是本条结论本身（`internal/service/issue_limit_test.go:64`）。

**实现口径（写死）：**

1. V3 导入路径本来就不走 `CreateIssue` / `AllocateIssueNumber`，因此**不额外调用** `CheckIssueCreateCapacity`——它是商业化闸门，不是数据完整性约束。
2. 但**不许静默越线**：写入任何行之前先读一次 `ResolveIssueCountPolicy(ctx, h.Entitlements, wsUUID)`，把 `{action, limit, used}` 写进导入报告。
3. `action == enforce` 且 `used + 包内 issue 数 > limit` 时**拒绝整批导入**，返回报告行 `issue_limit_would_exceed`（带 `limit` / `used` / `incoming`），不写任何行。这与第 7.3 节第 6 条「目标非空则 400 且不写任何行」是同一种前置闸门。
4. 目标端现成的只读口径是 `GET /api/workspaces/{id}/issues/limit-usage`（`internal/handler/issue_limit.go:19`，挂在 `router.go:1875`）：不 enforce 时返回 **204**，enforce 时返回 `{used, limit}`。CLI 侧做导入前预检可以直接用它，不必自己算。

### 11.2 `issue_dependency` 是否有可用读接口 — 已核实：没有

（沿用上一轮结论）`cmd/server/router.go` 全文没有任何 dependency 端点注册，CLI 拿不到这张表。1.2 的「不导出」结论成立。

### 11.3 前端是否仍在读 `issue.acceptance_criteria` / `context_refs` — 已核实：没有

（沿用上一轮结论）`packages/` 与 `apps/` 的 `.ts` / `.tsx` 里对这两个名字（含驼峰写法）零命中，`IssueResponse` 也不序列化它们。1.3 的「不导出不丢用户可见内容」结论成立。

### 11.4 官方云 `GET /api/issues` 的实际行为 — 已核实

只读实测（`multica` CLI v0.4.57 → `https://api.multica.ai`，`deneb-3029` 工作区，2026-09-17）加官方源码（`upstream/main` = `7e4758ac1a`）交叉验证，三问逐条：

- **`limit` 上限 = 100，且静默钳位。** 官方源码 `server/internal/handler/issue.go:1263`-`:1272`（upstream）与 `kun` 的 `:1253`-`:1261` **逐字相同**：默认 100，`> 100` 直接改写成 100，不报 400。`--limit 100` 实测正常返回。CLI 自己会在 `> 100` 时客户端报错（`--limit must be between 1 and 100`），所以用 CLI 探不到服务端的钳位行为，结论来自源码。
- **支持 `sort=created_at` + `direction=asc`。** 实测三组：`--sort created_at --direction asc` → DENE-2 起升序；`--direction desc` → 最新在前；不带 `--direction` → 默认 ASC（`issue.go:1350`-`:1352`，只有 `last_activity_at` 默认 DESC）。参数名是 `direction`，`dir` 无效，见 8.4。
- **未观察到速率限制。** 官方 `router.go` 只在 auth（`RATE_LIMIT_AUTH`，5/min）、auth-verify（20/min）、contact-sales（5/h）、邀请、webhook、plugin API（`RATE_LIMIT_PLUGIN_API`，120/min）上挂限流中间件；`/api` 的通用业务路由链上没有任何 `middleware.RateLimit`。实测两轮：387 次串行 `issue comment list` 用时 439 s（≈0.9 req/s）零限流，仅 2 次网络超时；60 次 12 并发突发用时 6 s（≈10 req/s）零错误。

**给实现 Stage 的口径**：仍然按「遇到 429 就指数退避」实现，但不要为此把导出降速到串行——10 req/s 已实测可用。V2 §8.9 那条速率限制未确认项在 issue 端点上按本条收敛。

### 11.5 官方云的 `ListComments` 是否支持 `since` — 已核实：支持

- 实测：`multica issue comment list <id> --since 2000-01-01T00:00:00Z` 返回全量 24 条；`--since 2026-09-16T00:00:00.000000001Z`（RFC3339**Nano**）同样被接受并正常过滤。两种精度都收。
- 官方源码对得上：`upstream/main` 的 `server/pkg/db/queries/comment.sql:61` 就是 `ListCommentsSinceForIssue`，注释写明「Powers the CLI's `--since` agent-polling flow」，谓词 `created_at > $3 ORDER BY created_at ASC, id ASC LIMIT $4` 与 `kun` 一致。
- 因此 8.4 的「并列 `created_at` 会被永久跳过」这个坑在官方云上**同样存在**，去重翻页是必须的，不是防御性冗余。
- `export_gaps` 的 `comment_window_truncated` 仍然保留：它现在的触发条件不再是「云端不支持 `since`」，而是「单张 issue 评论数超 `commentHardCap = 2000` 且 `since` 翻页在导出窗口内没追平」。

### 11.6 官方云的属性类型集合是否与 `kun` 一致 — 已核实：逐字相同

`upstream/main:server/internal/handler/property.go:55` 与 `kun` 的同名同行**逐字相同**：

```go
var validPropertyTypes = []string{"text", "number", "select", "multi_select", "date", "checkbox", "url", "actor", "multi_actor"}
```

九种类型没有任何差集，1.2.1 的三类值形状（标量 / 选项 id / actor 引用）对官方云成立，白名单投影不会丢类型。

限制说明：探测用的官方云工作区 `property list` 返回 `[]`（该工作区没有自定义属性），所以这条是**源码级**核实，不是数据级核实；在只读约束下无法通过创建属性来验证接口拒绝面。`export_gaps` 的 `property_type_unknown` **保留**为防御分支——上游随时可能加第十种类型，而导出端是白名单投影，加了也不会报错，只会静默丢。

### 11.7 官方云的墓碑评论行为 — 结论：按「返回墓碑」实现，但父指针必须无条件上溯

**源码结论（两侧一致）：默认列表路径返回墓碑行。**

- `upstream/main:server/pkg/db/queries/comment.sql:1` 的 `ListCommentsForIssue` **没有** `deleted_at IS NULL` 条件，与 `kun` 一致；
- `upstream/main:server/internal/handler/comment.go` 里 `commentToResponse` 把 `DeletedAt` 序列化出去（`deleted_at,omitempty`），响应构建路径上没有任何按 `DeletedAt` 过滤的分支。

**但这条拿不到正样本，必须写明。** 墓碑的产生条件是「删除一条已经有回复的评论」，而只读约束下不能在官方云制造一条。实测把 `deneb-3029` 工作区**全部 387 张 issue 的评论**拉了一遍（387 次 `issue comment list --output json`，`deleted_at` 因 `omitempty` 只在非空时出现），**零命中**——该工作区历史上没有产生过墓碑，所以既没有反例，也没有正例。CLI 的 `--workspace-id` / `MULTICA_WORKSPACE_ID` 在 Agent 运行环境下被任务上下文钉死，换工作区取样也做不到。

**实现口径（写死，不依赖本条结论的对错）：第 4 节的父指针回填一律实现「上溯到最近的可解析祖先」，不置空。**

理由是这条降级无论墓碑问题的答案是什么都必须存在：

1. 若官方云确实过滤墓碑，被删父评论的整串回复会在回填时断链、线程被拍平——契约 §11.7 原文指定的就是这个方向；
2. 即使返回墓碑，单张 issue 评论数超 `commentHardCap = 2000` 时默认路径只给最新 2000 条，老线程根同样会落在窗口外，父指针照样解析不到；
3. 上溯的代价只是导入端多一次 map 查找，而置空的代价是用户永久丢失线程结构。

具体规则：`parent_id` 在包内解析不到时，沿源端的父链向上找第一个**包内存在**的祖先作为新的 `parent_id`；一路到根都找不到才置空并记 `parent_unmapped`；每次发生上溯记一条 `parent_reparented_to_ancestor`，报告里给出被跳过的层数。第 7.3 节第 10 条「墓碑保序」的断言照旧，另加一条：构造一个「父评论不在包内」的线程，断言子回复挂到最近的包内祖先而不是被置空。

## 12. 给后续 Stage 的交接清单

后端（`server/`，仅 `kun`）：

- `internal/service/transfer_bundle.go`：新增 `TransferIssueRow` / `TransferCommentRow` / `TransferRelationRow` / `TransferIssuesRequest` / `TransferIssuesReport`；`TransferRefs` 加 `Members` / `Squads` / `Issues` / `IssueStatuses`；`TransferBundleSchemaVersion` 改为按内容取 1 或 2。
- `internal/service/transfer_ids.go`：新增三个命名空间与三个推导函数（9.5）。
- `internal/service/transfer_import.go`：新增 `ImportTransferIssues`，含 mention 重写、状态降级、指派映射、两遍写入的第一遍；`finalize` 分支做父指针回填 + 顶水位 + 订阅者重建。**comment 的父指针回填走「上溯最近包内祖先」**（4.1 / 11.7）。
- **DENE-383 核实后新增的三条硬约束**（不照做就会踩已知坑）：
  1. `project_id` 必须在 `TransferInsertIssue` 那一条 `INSERT` 里写对，**不得**事后 `UPDATE issue SET project_id`——`issue` 上有 `trg_issue_project_dirty_hourly`（7.1）。
  2. 导入前读一次 `ResolveIssueCountPolicy` 并回显 `{action, limit, used}`；`enforce` 且会越线时返回 `issue_limit_would_exceed` 且不写任何行（11.1）。
  3. 导出评论时**不得传 `fold` 参数**，否则已解决线程的中间评论会被静默丢光（8.4 / 11.5）。
- `internal/handler/workspace_transfer.go`：新增 `ImportWorkspaceTransferIssues`，权限同 V2。
- `cmd/server/router.go`：`POST /transfer/issues` 挂在与 V2 三个端点相同的 admin 分组内。
- `pkg/db/queries/workspace_transfer.sql`：7.2 列出的 9 条新语句 + `TransferInsertAttachment` 加两列。**不写任何联动其他表的 CTE。**
- 迁移：**本功能不需要任何数据库迁移**（没有新表、没有新列）。如果实现过程中发现需要，先回到本页评审——那说明契约有缺口。

CLI（`server/cmd/multica/`）：

- `transfer export` 新增 `issues` include 分组；四组读接口翻页（`GET /api/issues`、`GET /api/issues/{id}/comments`、`GET /api/issues/{id}/attachments`、`GET /api/issues/{id}/labels`），按 8.4 的三个坑实现分页。
- `transfer import` 新增 issues 分片上传与 `--renumber`（含 2.4 的确认提示与映射表落盘）。
- `--estimate` 覆盖 issues（8.3）。
- 用 `httptest` 模拟「上游形态」的读接口做导出测试，不访问真实官方云。
- 新增 CLI 标志后同步更新 `server/internal/service/builtin_skills/*` 下相关 `references/<domain>.md`（CLAUDE.md 规则）。

文档：

- 本页与 `docs/kun/config-transfer-runbook.md` 同步：运行手册要加「导入任务前先建空工作区」这一步。

跨环境验收（真机）：

- 从官方云工作区导出（带 `--include issues`）→ 导入 `ai.ferryway.cc` 的**新建空工作区**，核对：
  - 任务数与 `manifest.stats.issues` 一致，`DENE-xxx` 编号与源端逐一相同；
  - 「我的任务」立刻有内容（这是 kk zi 反馈的验收点）；
  - 随机抽 3 张有线程的票，评论顺序、作者、父子缩进正确；
  - 在一条导入过来的线程里回复一句，确认该回复能正常唤醒指派的智能体（验证第 3.3 节的降级确实解除了路由死锁）；
  - 收件箱为空，但上一条产生的新活动**能进收件箱**（验证第 6.2 节的订阅者重建）；
  - 重导同一个包无重复、`issue_counter` 不变。
