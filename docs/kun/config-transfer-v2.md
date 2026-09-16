# 跨环境迁移包 V2 契约（DENE-240 / DENE-241）

本页是「把配置 + 对话打包，导出到另一个 Multica 环境」的实现契约。后续实现 Stage 按本页执行，不再自行决定方向、实现面、对话范围、ID 映射、秘密边界、包结构与分批方式。本页只定义契约，不含实现代码。

前置文档：`docs/kun/config-export-import.md`（下称 **V1 契约**）。V1 契约里已定死的配置范围、秘密字段表、冲突词表、身份键、降级表，本页**全部沿用**，只写 V2 新增或改变的部分。

核对基线（2026-09-15）：

- fork `kun` tip `32fd66b6`：`server/migrations/` 至 `470_agent_runtime_plan_limits`，字段以 `server/pkg/db/generated/models.go` 为准；V1 实现在 `server/internal/service/config_{bundle,export,import,import_apply}.go`。
- 上游 `origin/main` `408bf2b2`：`server/cmd/server/router.go` 中**不存在** `config/export` 路由，官方云没有导出端点。

凡本页写「未确认」的地方，实现 Stage 动手前必须先验证，不得按猜测实现。

## 0. 一页结论

| # | 问题 | 结论 |
| --- | --- | --- |
| 1 | 方向 | V2 首发且只保证一个方向：**任意 Multica 实例（含官方云）→ 运行 `kun` 的自建实例**。自建 → 自建走同一条路。**不做**自建 → 官方云（官方云没有导入端点，我们也改不了官方云）。双向留作「上游接受导入端点」之后再议。 |
| 2 | 实现面 | **导出在客户端，导入在服务端**。导出 = `multica` CLI 用登录态调用源实例**已有的读接口**，在本地打包；导入 = `kun` 新增 `/transfer/*` 端点，CLI 只负责分片上传。官方云无导出端点这条硬约束，靠「只用读接口」绕开，不要求官方云升级。 |
| 3 | 对话范围 | `chat_session` 部分导出、`chat_message` 部分导出、聊天附件按类型导出；**只导出导出者本人的会话**（读接口本身就只返回本人会话）。issue 评论线程**不算**对话，V2 不导出（`comment*` 挂在任务上，任务不迁移）。 |
| 4 | ID 重映射 | user 按**邮箱**跨实例匹配；workspace 由导入者选定；agent / project / squad 等沿用 V1 身份键；聊天会话与消息的目标 id = `UUIDv5(目标工作区 id, 源 id)`，确定性、可重放、不建映射表。`agent_runtime` 永不迁移，`runtime_profile` 迁移定义但不绑定机器。 |
| 5 | 秘密边界 | 沿用 V1 的 `null` 占位 + `secrets_omitted` 登记；导出端改为**字段白名单投影**（不在白名单的字段不进包）；对话正文与文本附件跑高置信密钥模式扫描，命中片段替换为 `[REDACTED:<kind>]`，**不提供关闭开关**。验收断言：包内不得出现任何密钥明文。 |
| 6 | 包结构与版本 | 新格式 `multica.workspace-transfer`，`schema_version: 1`；包是一个 zip，内含一份**原样的 V1 配置 bundle**（`config.json`）+ 对话 JSONL 分片 + 附件。V2 导入器能读裸 V1 JSON；V1 导入器能读解压出来的 `config.json`，但读不了整个 zip。 |
| 7 | 体积与分批 | 对话按 JSONL 分片（每片 ≤ 16 MiB 且 ≤ 5000 条消息）；导出本地断点续传；导入按片上传、按确定性 id 幂等，所以「重跑即续传、重导即补增量」。**不做**实时同步、源端删改回传、服务端导入会话状态。 |

一句话的模块形状：CLI 暴露两个命令 `multica transfer export` / `multica transfer import`，服务端暴露三个端点，映射、脱敏、幂等全部藏在这两层内部。

## 1. 方向

### 1.1 结论

- **首发方向成立：官方云工作区 → 自建实例。** kk zi 正在切到自建实例（`ai.ferryway.cc`），这是真实需求。
- **目标端必须是 `kun`**（含 `/transfer/*` 端点的版本）。源端可以是官方云、上游自建、或 `kun` 自建。
- **不要求双向。** 自建 → 官方云需要官方云提供导入端点，这不在我们控制范围内，V2 不做。
- 自建 → 自建（例如换服务器）与首发方向走完全相同的路径，不另写一套。

### 1.2 两端能力差异的处理

| 差异 | 处理 |
| --- | --- |
| 源端没有 `config/export`（官方云、上游自建） | 导出端**从不调用** `config/export`，即使源端是 `kun` 也不调，保证只有一条导出代码路径。 |
| 源端版本比 `kun` 新（官方云领先，读接口多出字段） | 导出端做字段白名单投影（第 5.2 节），多出的字段不进包，不报错。 |
| 源端版本比 `kun` 旧或某个读接口不存在 | 该实体分组不导出，在 `manifest.export_gaps` 里登记 `{group, reason: "read_api_missing" \| "read_api_error", status}`，其余分组照常。 |
| 源端有、目标端没有的系统智能体 `system_key` | 导入时按 V1 分组（`system_key` 非空且不以 `agent_builder:` 开头，见 1.3）在目标工作区找不到同键即跳过，报告 `system_agent_not_in_target`。两边的 `system_key` 集合是否一致，**未确认**。 |
| 插件贡献技能 | V1 服务端导出按 `plugin_installation_id IS NULL` 过滤（V1 契约该行不动）。V2 CLI 走读接口，技能响应不暴露该字段。优先用 `GET /api/workspaces/{id}/plugins` 的 `resources` 反查排除（见 1.3）；该接口 503（PluginsV1 关闭）或不可用时全部导出，并在 `manifest.export_gaps` 记一条警告，导入报告必须回显。 |
| 目标端不是 `kun` 或版本过旧 | `/transfer/*` 返回 404 时，CLI 报 `target_unsupported`，一行字说明需要 `kun` 自建实例，不做降级。 |
| 两端都是 `kun` | 仍然走同一条客户端导出路径；V1 的同实例 `config/export` / `config/import` 保留不动，服务「同实例复刻」这个老场景。 |

### 1.3 读接口侧的分组与排除

V1 服务端导出有 DB 列。V2 CLI 只有读接口。下面两条是 CLI 必须遵守的判定，**不得**再用内部字段 `kind` 做任何导出分组。

**系统智能体。** `kind` 是内部字段、读接口不暴露（`AgentResponse` 只有 `system_key`）。CLI 从 `GET /api/agents` 分组：

- `system_key` 非空且不以 `agent_builder:` 开头 → 进 `entities.system_agents`，只投影 V1 系统智能体字段（`system_key`、`instructions`、`model`、`thinking_level`、`service_tier`、`conversation_starters`、`disabled_runtime_skills`）。
- 无 `system_key` → 进 `entities.agents`，按普通智能体导出。
- `agent_builder:` 前缀不会出现在该列表（读接口只回产品可见行）；即使出现也不导出。

导入仍按 V1：目标工作区找不到同 `system_key` 即跳过，报告 `system_agent_not_in_target`。

**插件贡献技能。** V1 服务端导出按 `plugin_installation_id IS NULL` 过滤。V2 CLI：技能读接口不暴露 `plugin_installation_id`。优先用 `GET /api/workspaces/{id}/plugins` 的 `resources` 反查：`type == "skill"`（`ResourceSkill`）的 `key` 即技能名（安装时 `name := resource.Key`），工作区内技能名唯一，名字命中任一安装的技能资源 key 则排除。该接口 503（PluginsV1 特性开关关闭）或不可用时，退化为全部导出，并在 `manifest.export_gaps` 记一条警告。警告字段落点由实现者定（建议 `export_gaps` 增 `reason: "plugin_skills_unfiltered"`，或新增 `warnings[]`），但必须能在导入报告里显示。

## 2. 实现面

### 2.1 候选与取舍

| 方案 | 能否从官方云导出 | 取舍 |
| --- | --- | --- |
| A. 服务端导出（沿用 V1 端点） | 不能。官方云跑上游代码，没有端点 | 排除。 |
| B. 要求官方云升级 | 不可控 | 排除。 |
| C. Desktop 在渲染进程里拉读接口、本地打包 | 能 | 对话量大时要翻几百页、下附件、断点续传；放在 Electron 渲染进程里既难续传又占内存；Web 端做不了。适合作为 C 之上的 UI 壳，不适合承载逻辑。 |
| **D. CLI 调读接口、本地打包（推荐）** | 能 | 无头、可断点续传、可在任何机器跑；与服务端同一个 Go module，可以直接复用 V1 的 bundle 结构体与 `redactSecretArgs` 等脱敏函数，导出与导入的规则只有一份源码。 |

**推荐 D**：导出逻辑放在 `multica` CLI；Desktop 以后要做按钮，调同一个 CLI 能力或同一份 Go 逻辑的封装，不另写 TS 实现。

### 2.2 为什么导入必须在服务端

- 导入对话要写入**历史**消息：保留 `role = assistant`、原始 `created_at`、不触发智能体任务、不生成标题、不推送渠道。现有 `POST /api/chat/sessions/{id}/messages` 只会以导入者身份新建一条 user 消息并排一个任务，做不到。
- 成员按邮箱映射需要查目标实例的 `user` 与 `member` 表，客户端没有这个权限面。
- 所以导入端点只能加在 `kun` 上，这与「目标端必须是 `kun`」一致。

### 2.3 CLI 形态

```
multica transfer export --profile <源实例登录档> --workspace <slug> --out <file.zip> [--include config,conversations,attachments] [--estimate]
multica transfer import --profile <目标实例登录档> --workspace <slug> --in <file.zip> [--dry-run] [--on-conflict fail|overwrite|rename|skip]
```

- `--estimate` 只统计会话数、消息数与估算体积（第 7.1 节），不写包。
- 包文件以 `0600` 权限写入；中间态放在 `<out>.partial/` 目录，完成后原子重命名。
- 凭据只来自 CLI 已有的登录档，不接受命令行明文 token 参数。CLI 能否同时保存两个不同服务器的登录档，**未确认**；若不能，实现 Stage 先补「多登录档」再做本功能，不允许退化为明文 token 参数。

### 2.4 服务端端点（仅 `kun`）

全部挂在 `/api/workspaces/{id}` 分组内，权限与 V1 相同：目标工作区 `owner` / `admin`，agent actor 一律 403。

| 端点 | 作用 |
| --- | --- |
| `POST /api/workspaces/{id}/transfer/config` | 接收 `manifest` + `people` + `runtime_profiles` + `config`（V1 bundle），先做成员邮箱映射与 `runtime_profile` 预处理，再调用 V1 导入内核。支持 `dry_run`、`on_conflict`、`options`，响应 = V1 导入报告 + `people_map` + `runtimes_to_bind`。 |
| `POST /api/workspaces/{id}/transfer/conversations` | 接收一个对话分片（会话 + 消息 + 本片引用索引），幂等写入。支持 `dry_run`。 |
| `POST /api/workspaces/{id}/transfer/attachments` | `multipart/form-data`，一次一个附件：元数据 JSON + 文件体，幂等写入。 |

`transfer/config` 请求体的 `options` 就是 V1 的 `ConfigImportRequest.options`，逐字段透传进导入内核（DENE-363）。整个 `options` 对象缺席时取零值，等于 DENE-363 之前的行为：自动化全部落 `paused`、工作区设置照落、issue 前缀不动，因此老客户端不受影响。

| `options` 字段 | 对应 CLI flag | 默认 | 语义 |
| --- | --- | --- | --- |
| `activate_autopilots` | `--activate-autopilots` | CLI 与卡片默认 `true`；服务端缺席即 `false` | `true` 时按源状态导入，自动化导入后立即参与触发；`false` 时全部以 `paused` 写入，并在 `config_report.warnings` 里追一条 `autopilots_imported_paused`。 |
| `apply_workspace_settings` | `--apply-workspace-settings` | `true`（服务端 `nil` 也视为 `true`） | `false` 时不写 `workspace` 批次，目标工作区设置原样保留。 |
| `apply_issue_prefix` | `--apply-issue-prefix` | 内核与 V1 `/config/import`：`false`；`transfer import` 与卡片：包带 `issues` 分组时 `true`，否则 `false`（DENE-404） | `true` 且工作区设置生效时，若目标工作区任务数为 0 则改用源端 issue 前缀，否则跳过并追一条 `issue_prefix_skipped_target_has_issues`。 |
| `auto_bind_runtimes` | `--auto-bind-runtimes` | CLI 与卡片默认 `true`；服务端 `null` 也视为 `true` | `true` 时按第 4.4.1 节的三档规则**写入**绑定：只有一个候选的智能体直接绑好，多个候选一律不动。`false` 时只产出报告，全部留给用户点。只被 V2 `transfer/config` 读取，V1 `/config/import` 无运行时绑定概念。 |

CLI 与 Desktop 迁移卡片用同一套默认值；卡片只在用户改动默认值时才把对应 flag 传给 CLI（`--activate-autopilots=false` / `--apply-workspace-settings=false` / `--auto-bind-runtimes=false`），所以不带这些 flag 的旧 CLI 仍能跑默认导入。`--apply-issue-prefix` 是唯一例外（DENE-404）：它的默认值跟着包里的 `issues` 分组走，而卡片看不到包的分组，所以卡片**总是**显式传这个开关（勾上 `--apply-issue-prefix`，取消 `--apply-issue-prefix=false`）。内核默认值没变——不带这个 flag 的老客户端仍是不采用前缀。预览报告把 `autopilots` 批次折算成一行「自动化：导入 N 条，其中 M 条已暂停」。

自动绑定只在**非 dry-run** 的 `transfer/config` 里执行，且排在配置导入提交之后：dry-run 只产出计划（每行 `status: pending`），真正的 apply 才会写 `agent.runtime_id`；绑定失败不回滚已经成功的导入，只在对应行上留下 `reason_code: runtime_bind_failed`。

多候选（或用户关掉开关）的行由 `POST /api/workspaces/{id}/transfer/bind-runtimes` 收口：请求体 `{"bindings":[{"agent_id","runtime_id"}]}`，响应逐条给 `bound` / `error_code` / `error`，一条失败不影响其余。CLI 对应 `multica transfer bind-runtimes --workspace <slug> --bind <agent_id>=<runtime_id> ...`（可重复）。

导入顺序固定：`transfer/config`（必须先 apply 成功）→ `transfer/conversations`（逐片）→ `transfer/attachments`（逐个）→ 最后一次 `transfer/conversations` 带 `finalize: true`，服务端只发一次工作区级聊天列表失效事件。

## 3. 对话范围

### 3.1 通用规则

- 只导出**导出者本人创建**的会话。`GET /api/chat/sessions` 走 `ListAllChatSessionsByCreator`，读接口本身只返回本人会话；别人的聊天是别人的隐私，不迁移。
- 导入后会话的 `creator_id` 一律写为**导入者**（迁移的是同一个人）。
- 已归档会话（`status = 'archived'`）默认导出（聊天记录是用户资产，和配置不同）；`--exclude-archived` 可排除。
- 消息分页走 `GET /api/chat/sessions/{id}/messages/page`（`limit` 上限 100，游标 `before_created_at` + `before_id`）。
- 所有「运行态指针」一律不迁移：智能体 CLI 的续聊指针、工作目录、任务队列、渠道路由。

### 3.2 逐表清单

| 表 | 结论 | 导出字段 | 排除字段 | 理由 |
| --- | --- | --- | --- | --- |
| `chat_session` | 部分导出 | `id`（作 `source_id`）、`agent_id`（引用）、`project_id`（引用）、`title`、`status`、`pinned_at`、`is_agent_intro`、`explicitly_created_at`、`created_at`、`updated_at`；`channel_source.channel_type` 仅作说明 | `workspace_id`（改写为目标）、`creator_id`（改写为导入者）、`session_id`（智能体 CLI 续聊指针，绑定源机器）、`work_dir`（源机器路径）、`runtime_id`（机器绑定）、`unread_since`、`last_read_at`（导入后视为已读） | 保留时间戳是为了列表排序与「这是什么时候聊的」不失真。渠道来源的会话（飞书 / Slack 等）导入后变成普通 Multica 聊天，不再与外部群绑定。 |
| `chat_message` | 部分导出 | `id`（作 `source_id`）、`chat_session_id`（引用）、`role`、`content`（经第 5.3 节扫描）、`message_kind`、`failure_reason`、`elapsed_ms`、`created_at`、附件引用 `attachment_ids[]` | `task_id`（任务不迁移，置空）、`quick_actions`（由当轮任务生成的后续动作，点了会在目标端跑任务，导入为 `[]`）、`channel_media_pending_until`、`channel_ingested`、`channel_context_revision`、`channel_outbound_*` | `role` 与 `created_at` 必须原样保留，否则对话顺序与说话人错乱。`failure_reason` 保留，失败气泡照旧显示。读接口过滤掉的 onboarding kickoff 行本来就拿不到，不导出。 |
| `attachment`（`chat_session_id` 或 `chat_message_id` 非空） | 部分导出 | `id`（作 `source_id`）、`chat_session_id` / `chat_message_id`（引用）、`filename`、`content_type`、`size_bytes`、`created_at`、`sha256`（导出时计算）；文件体按第 3.4 节 | `workspace_id`、`url`（源实例地址，跨实例无效）、`uploader_type` / `uploader_id`（改写为导入者）、`issue_id`、`comment_id`、`task_id`、`source_context_id` | 文件体经 `GET /api/attachments/{id}/download` 获取，写回目标实例存储后生成新的 url。 |
| `chat_pinned_agent` | 部分导出 | `agent_id`（引用）、`position` | `workspace_id`、`user_id`（改写为导入者）、`created_at` | V1 当它是「别人的偏好」排除；V2 迁移的是导出者本人，接口也只返回本人的，放进 `preferences.pinned_agents`。 |
| `chat_draft_restore` | 不导出 | — | 全部 | 取消任务时的临时草稿恢复，一次性消费。 |
| `channel_chat_*`、`channel_task_delivery`、`lark_chat_*` | 不导出 | — | 全部 | 渠道路由与投递状态，绑定外部租户。 |
| `agent_task_queue` 中的聊天任务 | 不导出 | — | 全部 | 运行态。导出时仍在跑的一轮，只导出已经落库的消息，报告 `session_had_pending_task`。 |

### 3.3 issue 评论线程算不算「对话」

**结论：不算，V2 不导出 `comment`、`comment_reaction` 及评论附件。**

理由：

1. `comment.issue_id` 必填，评论依附于任务；任务、任务编号、状态流转、指派、活动日志都不在 V1 / V2 范围内。只迁评论不迁任务，评论无处挂载。
2. 迁移任务是完整的数据迁移（编号冲突、`issue_counter`、多态指派、子任务、PR 关联、`activity_log`），规模和风险都是另一个量级，应单独立项（暂称 V3「任务迁移」），不塞进 V2。
3. 用户说的「对话」在产品里对应右侧 Chat（与智能体的会话），与评论线程是两个入口。

> **口径变更（kk zi 2026-09-16，DENE-365）：V3 已立项，任务与评论要迁。**
> 本节的结论在 V2 的范围内**仍然成立**——V2 包不含任务与评论，V2 的导出与导入路径一行不改。变的是「V3 何时做」：kk zi 真机反馈「收件箱、我的任务、工作区与项目里的任务都没同步过去」后，V3 已单独立项，契约见 `docs/kun/config-transfer-v3-issues.md`。
> 该契约与本节的衔接点：V3 把包的外层 `schema_version` 顶到 2 并新增 `issues/` 目录，`include` 分组 `issues` **默认不开**——不带它时 V3 导出器产出的仍是 `schema_version: 1` 的 V2 包。所以本页定义的包格式不失效，未升级的目标端也仍然能读日常导出的包。

### 3.4 附件文件体的范围

| 附件类型 | 处理 |
| --- | --- |
| `image/*` | 导出文件体。 |
| 文本类（`text/*`、`application/json`、`application/x-yaml`、`application/xml`，以及扩展名 `.md` `.txt` `.env` `.json` `.yaml` `.yml` `.toml` `.ini` `.log` `.csv`） | 导出文件体，先按第 5.3 节扫描；**命中任何密钥模式的整个文件不导出**，只保留元数据并登记 `secrets_omitted`。 |
| 其他二进制（zip、pdf、octet-stream、音视频……） | 只导出元数据，不导文件体，报告 `attachment_body_not_exported`。原因：无法扫描是否夹带密钥，不能满足第 5 节验收断言。 |
| 单文件 > 25 MiB | 只导出元数据。 |

没有文件体的附件导入后，消息里对应位置显示一个不可下载的附件卡片（文件名 + 「未迁移」）。消息正文里指向源实例附件地址的 Markdown 链接如何识别与改写，**未确认**（需先确认聊天正文里附件是以 `/api/attachments/{id}/download` 相对路径还是完整 URL 出现）；确认前导入端原样保留正文，并在报告里记 `content_has_source_urls` 计数。

## 4. 跨实例 ID 重映射

### 4.1 总原则

- V1 的映射表、拓扑写入顺序、降级表（V1 契约第 4 节）**原样适用于配置部分**。
- V2 只额外解决三件事：**成员跨实例身份**、**机器绑定实体**、**对话的无状态幂等 id**。
- 不新增持久化映射表。所有映射要么靠身份键现查，要么靠确定性 id 推导。

### 4.2 逐类映射规则

| 源实体 | 目标 id 怎么来 | 无法映射时 |
| --- | --- | --- |
| workspace | 导入者在 CLI 指定的目标工作区（必须已存在，由导入者提前创建） | 目标工作区不存在或无权限 → 404，整个导入不开始 |
| user（成员引用） | `people.json` 里源 user 的 `email`，在目标实例查同邮箱（大小写不敏感）的 user，且该 user 是目标工作区成员 → 替换为该 user id | 按 V1 降级表处理该引用（丢弃 / 置空 / 跳过整条），报告 `unmapped_refs` 记 `ref_type: "member"`，**只回显源 id，不回显邮箱** |
| user = 导出者本人（`manifest.source.exported_by`） | 一律映射为**导入者**，不看邮箱是否一致 | — |
| 普通智能体（无 `system_key`） | V1 身份键 `name`：`transfer/config` 导入后，按 `config.json` 中该 `source_id` 的 `name` 在目标工作区查 | 对话分片里引用它的会话整条跳过，报告 `agent_unmapped`；修好后重导同一分片即可补上 |
| 系统智能体（`system_key` 非空且不以 `agent_builder:` 开头） | `system_key` | 同上 |
| project / squad / label / skill 等 | V1 身份键（`title` / `name` / `(resource_type, lower(name))`） | V1 降级表；会话的 `project_id` 无法映射时置空，会话照常导入 |
| `chat_session.id` | `UUIDv5(NS_TRANSFER_CHAT_SESSION, 目标工作区 id + "/" + 源 id)` | —（永远可算） |
| `chat_message.id` | `UUIDv5(NS_TRANSFER_CHAT_MESSAGE, 目标工作区 id + "/" + 源 id)` | 所属会话被跳过时，消息一并跳过 |
| 聊天 `attachment.id` | `UUIDv5(NS_TRANSFER_ATTACHMENT, 目标工作区 id + "/" + 源 id)` | 所属消息被跳过时一并跳过 |
| `agent_runtime` | **不迁移** | 导入后所有智能体 `runtime_bound = false`（与 V1 一致） |
| `runtime_profile` | 迁移定义（第 4.4 节），身份键见下 | 冲突按 `on_conflict` |
| `project_resource.local_directory.daemon_id` | 跨实例必然不存在 | 一律跳过该资源，报告 `daemon_not_in_target` |

三个 `NS_TRANSFER_*` 命名空间 UUID 在实现 Stage 定为代码常量，一旦发布不得修改（修改会让重导产生重复数据）。

为什么对话用确定性 id 而不是保留源 id：同一个包导入同一实例的两个工作区时，源 id 会撞主键；加入目标工作区 id 推导后，同一工作区重导命中同一行（幂等），不同工作区互不干扰。

### 4.3 `people.json`

```json
[
  { "source_user_id": "c924599a-9548-4fc1-9146-a3645ecbb0c6", "email": "owner@example.com", "role": "owner" },
  { "source_user_id": "7d3b0c1e-2222-7000-8000-000000000b02", "email": "teammate@example.com", "role": "member" }
]
```

- 只包含 bundle 里**实际被引用到**的 user，不是整个成员列表。
- 字段只有 `source_user_id`、`email`、`role`（仅作 UI 展示），**不含**姓名、头像、描述等资料。
- 邮箱属于个人信息：导出前 CLI 提示「包内含 N 个成员邮箱，用于在新环境对应成员」；`--no-people` 可去掉该文件，代价是除导出者本人外所有成员引用降级。
- 源实例成员列表接口是否返回邮箱，**未确认**；若不返回，`people.json` 只能包含导出者本人（`GET /api/me`），报告里提示其余成员引用会降级。

### 4.4 `runtime_profile` 与运行时

| 表 | 结论 | 导出字段 | 排除字段 | 理由 |
| --- | --- | --- | --- | --- |
| `runtime_profile` | 部分导出 | `display_name`、`protocol_family`、`command_name`、`description`、`fixed_args`（经 `redactSecretArgs`）、`visibility`、`enabled` | `id`（作 `source_id`）、`workspace_id`、`created_by`（改写为导入者）、`created_at`、`updated_at` | 换环境时用户要在新机器重装守护进程，自定义运行时定义可以省去重录。身份键暂定 `display_name`，数据库是否有唯一约束**未确认**。 |
| `agent_runtime` | 不导出实体，只导出**绑定提示** | `runtimes_hint[]`：`{ source_runtime_id, provider, runtime_mode, profile_source_id, display_name }`（`display_name` 取 `custom_name` 否则 `name`）；`agent_hints[]`：`{ source_agent_id, source_runtime_id, provider, runtime_mode, profile_source_id, profile_name, runtime_name }` | `daemon_id`、`legacy_daemon_id`、`device_info`、`metadata`、`owner_id`、`status`、`last_seen_at`、`plan_limits` | 守护进程注册绑定具体机器与 daemon token，跨实例没有意义；`device_info` / `metadata` 可能含主机名与路径，不进包。 |

`agent_hints[]` 是 DENE-364 为「可执行绑定」补的**每个智能体到源运行时的链接**：导出端用 `GET /api/agents` 的 `runtime_id` 关联 `GET /api/runtimes` 的 `provider` / `runtime_mode` / `profile_id`，再把 `profile_id` 折成 `runtime_profile.display_name`。没有它，目标端只知道智能体的 `runtime_mode`，无法判断 provider 与自定义 profile，就只能猜——所以缺 `agent_hints` 的老包一律走「零候选」分支并写明原因，不猜。

### 4.4.1 运行时绑定三档规则（DENE-364，kk zi 2026-09-16 口径变更）

导入报告 `runtimes_to_bind[]`：每个导入的智能体一行，给出源端 `provider` / `runtime_mode` / 自定义 profile 名，并列出目标工作区里**导入者可见**（`owner_id = 导入者` 或 `visibility = public`）的运行时作为候选。候选集按 `provider` + `runtime_mode` + 自定义 profile 名三项全等匹配（内置运行时视为 profile 名为空）。

| 候选数 | 行为 | 报告 |
| --- | --- | --- |
| 恰好 1 个 | `auto_bind_runtimes` 为真（默认）时**直接写入** `agent.runtime_id`，走与智能体编辑页相同的写入路径与权限校验（`canUseRuntimeForAgent`：他人私有运行时不可用）。 | `status: bound`，带 `bound_runtime_id` / `bound_runtime_name` |
| 多个 | **不猜**。不写任何指针，由 Desktop 卡片一次性列出下拉让用户点完（`POST /transfer/bind-runtimes`）。 | `status: pending` + `candidates[]` / `candidate_ids[]` |
| 0 个 | 不静默跳过，记一条可读原因。 | `status: no_candidate` + `reason_code` + `reason` |

零候选的 `reason_code`：

- `no_runtime_for_provider`：目标实例上没有匹配 `provider` / `runtime_mode` / profile 的运行时，`reason` 写明 `provider=xxx` 与「先把本机 daemon 连到目标实例」。
- `runtime_provider_unknown`：包内没有该智能体的 `agent_hints`（老包），无法判断 provider，要求从源环境重新导出。
- `runtime_bind_failed`：规则命中唯一候选但写入失败（权限、运行时消失、数据库），`reason` 带原始原因，该行仍留在 `pending` 供人工处理。

自动绑定**只绑定目标实例上已经存在、导入者可见的运行时**，不创建运行时、不迁移任何凭据，只写 `agent_id → runtime_id` 引用。源端的 `runtime_mode` 也随绑定一起写回，与智能体编辑页移动运行时时的行为一致。

### 4.5 降级表（V2 新增项）

| 场景 | 降级 | 报告 code |
| --- | --- | --- |
| 成员邮箱在目标实例不存在 / 不是目标工作区成员 | 按 V1 降级表处理该引用 | `member_unmapped` |
| 会话引用的智能体不在目标工作区 | 整条会话及其消息、附件跳过 | `agent_unmapped` |
| 会话引用的项目不在目标工作区 | `project_id` 置空，会话照常导入 | `project_unmapped` |
| 源会话来自外部渠道 | 作为普通聊天导入 | `channel_source_detached` |
| 附件没有文件体（第 3.4 节） | 只建元数据，不可下载 | `attachment_body_not_exported` |
| 源端某个读接口缺失 | 该分组不在包内 | `export_gap:<group>` |
| `local_directory` 项目资源 | 跳过 | `daemon_not_in_target` |
| 目标端同 `system_key` 系统智能体不存在 | 跳过 patch | `system_agent_not_in_target` |

### 4.6 「无缝衔接」的真实边界

- 聊天记录、置顶、标题、时间顺序在目标端完整可见。
- **智能体的会话记忆不能靠续聊指针延续**：`session_id` 是源机器上智能体 CLI 的本地会话，不迁移。导入后第一轮新消息，守护进程找不到可续的会话。仓库里存在续聊失败回退路径（`server/internal/handler/daemon_chat_resume_fallback_test.go`），但它是否会把 Multica 侧的历史消息重新注入提示词、注入多少条，**未确认**。实现 Stage 必须先验证：若回退不注入历史，本功能的对外说明要改为「记录可见，智能体从新会话开始」，不得宣称上下文无缝。

## 5. 秘密边界

### 5.1 沿用 V1 的部分

- V1 契约第 2.1 节的秘密字段表全部适用，占位固定为 JSON `null`，逐条登记 `secrets_omitted`，`hint` 只允许数量与传输类型。
- `custom_args` 与 `runtime_profile.fixed_args` 用 V1 已实现的 `redactSecretArgs`（`server/internal/service/config_bundle.go`）脱敏；导入端用 `restoreSecretArgs` 解析占位，占位符不得到达智能体命令行。
- V1 的兜底键名清洗（`token`、`secret`、`password`……）对 `config.json` 整棵树照跑。
- 导入端点拒收秘密：请求体中出现非 `null` 的 V1 秘密字段，返回 400 `config_bundle_contains_secret`（与 V1 相同的码）。

### 5.2 客户端导出的新增风险与处置

V1 在服务端「SQL 不 SELECT 秘密列」得到结构性保证。V2 在客户端读的是**别人实现的读接口**，这些接口对 owner 可能返回比预期更多的内容（例如上游 `main` 的智能体接口原样返回 `custom_args`，`runtime_config.gateway.token` 以 `***` 掩码返回）。处置：

1. **白名单投影**：导出端对每个实体只拷贝 V1 契约第 1 节「导出字段」列出的键，其余一律丢弃。不写「删掉哪些键」，只写「保留哪些键」。
2. 投影后再跑 V1 键名清洗兜底。
3. `***` 掩码值视同秘密，置 `null` 并登记，不得出现在包内。

### 5.3 对话正文的密钥扫描

对 `chat_message.content`、`chat_session.title`、文本类附件跑高置信模式扫描。首批模式（实现 Stage 可补充，不可删减）：

| kind | 识别特征（描述，不是最终正则） |
| --- | --- |
| `private_key` | `-----BEGIN ... PRIVATE KEY-----` 到对应 END 块 |
| `anthropic_key` / `openai_key` | `sk-ant-` 前缀；`sk-` / `sk-proj-` 前缀加长随机串 |
| `github_token` | `ghp_` `gho_` `ghu_` `ghs_` `ghr_` `github_pat_` 前缀 |
| `slack_token` | `xoxb-` `xoxp-` `xoxa-` `xapp-` 前缀 |
| `aws_access_key` | `AKIA` / `ASIA` 加 16 位大写字母数字 |
| `google_api_key` | `AIza` 加 35 位 |
| `jwt` | 三段 base64url，以 `eyJ` 开头 |
| `multica_token` | 本产品个人访问令牌与 daemon token 的前缀（具体前缀**未确认**，实现 Stage 从 token 生成代码取） |
| `assignment` | 键名命中 V1 清洗词表的 `KEY=value` / `"key": "value"` / `key: value`，值长度 ≥ 8 |
| `bearer` | `Authorization: Bearer <值>` / `Bearer <长随机串>` |

处置：

- 命中片段原位替换为 `[REDACTED:<kind>]`，消息其余内容保留。
- 每条被改写的消息登记一条 `secrets_omitted`：`{ entity: "chat_message", source_id, field: "content", reason: "content_pattern", hint: { kinds: ["github_token"], count: 2 } }`。`hint` 不含片段、前缀或长度。
- **不提供关闭扫描的开关。** 误伤（把普通文本当密钥）的代价是聊天记录里一小段变成占位，可接受；漏放的代价是密钥外泄，不可接受。
- 扫描不处理隐私（姓名、电话、地址、业务内容）。CLI 在导出开始前与完成后各提示一次：「包内含完整聊天记录与成员邮箱，等同聊天记录本身，请按敏感文件保管，不要上传到公开位置。」

### 5.4 验收断言（实现 Stage 必须落地，任一失败阻断合并）

1. **配置金丝雀**：复用 V1 契约第 2.4 节的五个金丝雀，外加 `custom_args` 中 `--api-key CANARY_ARG_1a2b`、`runtime_profile.fixed_args` 中 `TOKEN=CANARY_PROFILE_3c4d`。
2. **对话金丝雀**：一条消息正文含 `ghp_` 开头的假 token、一段 `-----BEGIN OPENSSH PRIVATE KEY-----` 假私钥块、`API_KEY=CANARY_CHAT_5e6f`；一个 `.env` 文本附件含 `SECRET=CANARY_FILE_7a8b`。
3. 对**解压后的整个包**（所有文件逐字节）做子串断言：上述所有金丝雀原值不出现，`***` 不出现。
4. 每处被脱敏的位置在 `secrets_omitted` 中恰有一条记录；含金丝雀的 `.env` 附件没有文件体。
5. 导入端拒收：`transfer/config` 请求体带非 `null` `custom_env` 返回 400 且不写库。
6. 以上测试的输入走「假读接口」（`httptest` 服务端模拟源实例响应，包括上游返回明文 `custom_args` 与 `***` 掩码的形态），不依赖真实官方云。

## 6. 包结构与版本

### 6.1 容器

一个 zip 文件，建议文件名 `multica-transfer-{slug}-{YYYYMMDD}.zip`：

```
manifest.json                       必需，第一个写入
config.json                         V1 bundle 原样（format = multica.workspace-config, schema_version = 1）
people.json                         第 4.3 节；--no-people 时不存在
runtime_profiles.json               第 4.4 节导出字段 + runtimes_hint
preferences.json                    { "pinned_agents": [...] }
conversations/sessions-0001.jsonl   每行一个会话
conversations/messages-0001.jsonl   每行一条消息；按会话聚合，一个会话的消息不跨片
attachments/index.jsonl             每行一个附件元数据
attachments/blobs/<sha256>          文件体，按内容寻址去重
secrets_omitted.json                全包汇总（含 config.json 内的登记，便于导入报告合并）
```

为什么是 zip + JSONL 而不是单个 JSON：对话可能上百 MB，单个 JSON 必须整体读入内存才能解析；JSONL 可以逐行流式读、按片上传；zip 可随机访问单个文件，Go 标准库原生支持。

为什么 `config.json` 原样嵌入 V1 bundle：配置部分的范围、占位、身份键、导入内核全部复用 V1，一行不重写；V2 只在外面加跨实例需要的东西。

### 6.2 `manifest.json`

```json
{
  "format": "multica.workspace-transfer",
  "schema_version": 1,
  "bundle_id": "01a0a4c0-1111-7000-8000-0000000000c0",
  "exported_at": "2026-09-15T12:00:00Z",
  "exporter": { "kind": "cli", "version": "v0.43.0" },
  "source": {
    "base_url_host": "multica.ai",
    "server_version": "unknown",
    "workspace_id": "41a8b48e-4c0b-498a-9097-bb694f80b4b9",
    "slug": "deneb",
    "name": "Deneb",
    "exported_by": "c924599a-9548-4fc1-9146-a3645ecbb0c6"
  },
  "options": { "include": ["config", "conversations", "attachments"], "exclude_archived_chats": false, "people": true },
  "files": [
    { "path": "config.json", "sha256": "…", "bytes": 48213 },
    { "path": "conversations/sessions-0001.jsonl", "sha256": "…", "bytes": 91234, "rows": 212 },
    { "path": "conversations/messages-0001.jsonl", "sha256": "…", "bytes": 15873201, "rows": 4988 },
    { "path": "conversations/messages-0002.jsonl", "sha256": "…", "bytes": 6203115, "rows": 2101 }
  ],
  "refs": {
    "agents": { "01a0a3f2-0000-7000-8000-000000000a01": { "name": "主力工作-贝吉塔" } },
    "system_agents": { "01a0a3f2-0000-7000-8000-000000000a09": { "system_key": "mika" } },
    "projects": { "01a0a3f2-0000-7000-8000-000000000fa1": { "title": "Multica 魔改" } }
  },
  "export_gaps": [
    { "group": "issue_views", "reason": "read_api_error", "status": 500 },
    { "group": "skills", "reason": "plugin_skills_unfiltered", "status": 503 }
  ],
  "stats": { "chat_sessions": 212, "chat_messages": 7089, "attachments": 64, "attachment_bodies": 51, "secrets_redacted_in_content": 3 }
}
```

- `source.base_url_host` 只记主机名，不记完整 URL、不记登录档名。`server_version` 读不到时为 `"unknown"`。
- `refs` 是对话分片解析引用所需的最小索引（源 id → 身份键），导入对话时随每个分片一起上传，服务端不需要记住 `transfer/config` 的结果。
- `files[].sha256` 用于导入前完整性校验；任一文件校验失败，导入不开始（`transfer_bundle_corrupt`）。
- `export_gaps[].reason` 除 `read_api_missing` / `read_api_error` 外，允许实现者新增。插件接口不可用、技能未按 1.3 过滤时必须记一条（建议 `plugin_skills_unfiltered`），且导入报告必须回显；这条不等于把整个 `skills` 组丢掉。
- 顶层与行对象允许未知键（忽略）；缺必填键报 `transfer_bundle_invalid`。

### 6.3 行格式

`conversations/sessions-*.jsonl` 一行：

```json
{"source_id":"01a0a3f2-3333-7000-8000-000000000101","agent_id":"01a0a3f2-0000-7000-8000-000000000a01","project_id":"01a0a3f2-0000-7000-8000-000000000fa1","title":"排查 daemon 重连","status":"active","pinned_at":"2026-09-10T03:12:00Z","is_agent_intro":false,"explicitly_created_at":"2026-09-09T14:01:22Z","created_at":"2026-09-09T14:01:22Z","updated_at":"2026-09-10T03:12:00Z","channel_type":null,"had_pending_task":false}
```

`conversations/messages-*.jsonl` 一行：

```json
{"source_id":"01a0a3f2-3333-7000-8000-000000000201","chat_session_id":"01a0a3f2-3333-7000-8000-000000000101","role":"user","message_kind":"message","content":"daemon 连不上，日志里有 GITHUB_TOKEN=[REDACTED:assignment]，帮我看看","failure_reason":null,"elapsed_ms":null,"created_at":"2026-09-09T14:01:22Z","attachment_ids":["01a0a3f2-3333-7000-8000-000000000301"]}
{"source_id":"01a0a3f2-3333-7000-8000-000000000202","chat_session_id":"01a0a3f2-3333-7000-8000-000000000101","role":"assistant","message_kind":"message","content":"先确认守护进程版本……","failure_reason":null,"elapsed_ms":38120,"created_at":"2026-09-09T14:02:00Z","attachment_ids":[]}
```

`attachments/index.jsonl` 一行：

```json
{"source_id":"01a0a3f2-3333-7000-8000-000000000301","chat_session_id":"01a0a3f2-3333-7000-8000-000000000101","chat_message_id":"01a0a3f2-3333-7000-8000-000000000201","filename":"daemon.log","content_type":"text/plain","size_bytes":20431,"created_at":"2026-09-09T14:01:20Z","sha256":"9f2c…","body":"attachments/blobs/9f2c…","body_omitted_reason":null}
```

`preferences.json`：

```json
{ "pinned_agents": [ { "agent_id": "01a0a3f2-0000-7000-8000-000000000a01", "position": 1 } ] }
```

`secrets_omitted.json`（节选）：

```json
[
  { "entity": "agent", "source_id": "01a0a3f2-0000-7000-8000-000000000a01", "name": "主力工作-贝吉塔", "field": "custom_env", "reason": "secret_material", "hint": { "key_count": 3 } },
  { "entity": "chat_message", "source_id": "01a0a3f2-3333-7000-8000-000000000201", "field": "content", "reason": "content_pattern", "hint": { "kinds": ["assignment"], "count": 1 } },
  { "entity": "attachment", "source_id": "01a0a3f2-3333-7000-8000-000000000302", "name": "prod.env", "field": "body", "reason": "content_pattern", "hint": { "kinds": ["assignment"], "count": 4 } }
]
```

### 6.4 版本口径

| 问题 | 结论 |
| --- | --- |
| 是否新开 `schema_version` | **新开格式，不改 V1**。外层 `format = multica.workspace-transfer`，`schema_version` 从 1 起；内层 `config.json` 继续是 `multica.workspace-config` / `schema_version: 1`。V1 的 `ConfigBundleSchemaVersion` 常量不动。 |
| 不识别的外层版本 | 400 `transfer_bundle_version_unsupported`，不降级解析（与 V1 口径一致）。 |
| V2 导入器读 V1 包 | **能**。`multica transfer import --in <v1.json>` 识别为裸 V1 bundle，只走 `transfer/config`，没有 `people.json`，成员引用按「同实例同 id」处理（即 V1 语义）。 |
| V1 导入器读 V2 包 | **整个 zip 不能**（`config_bundle_invalid`）。把 zip 里的 `config.json` 解压出来喂给 V1 设置页的导入**能**，但跨实例场景下成员 id 全部对不上，所有成员引用降级；UI 不做特别提示，这是 V1 已声明的边界。 |
| `config.json` 以后要加跨实例字段怎么办 | 放在外层文件（如 `people.json`），不往 V1 bundle 里塞。确实需要改 V1 bundle 结构时，V1 自己升 `schema_version: 2`，外层 manifest 通过 `files[]` 声明内层版本。 |

## 7. 体积与分批

### 7.1 体积估算口径

`multica transfer export --estimate` 只调用列表接口，不下载消息正文与附件：

```
估算字节 ≈ Σ会话 ( 消息数 × 平均单条字节 ) + 附件文件体总字节 + config.json 字节
平均单条字节 = 400（行内 JSON 键与元数据开销） + 正文字节
正文字节：抽样每个会话最近一页（≤ 100 条）的 content UTF-8 字节均值；中文按 UTF-8 每字 3 字节计
附件：按 size_bytes 求和，只计第 3.4 节会导出文件体的类型
```

参考量级（**示例推算，不是实测**）：200 个会话 × 平均 60 条消息 × 平均 2 KB ≈ 24 MB；加 50 个图片附件 × 500 KB ≈ 25 MB；zip 压缩后文本部分通常显著变小，压缩率**未确认**。实际分布在实现 Stage 用 `--estimate` 对真实工作区跑一次并记录进 PR 描述。

官方云读接口的速率限制**未确认**：导出端对 429 / 503 做指数退避（起始 1s，上限 60s，尊重 `Retry-After`），不并发翻页超过 4 路。

### 7.2 分片规则

- 消息分片：每片 ≤ 16 MiB **且** ≤ 5000 条消息；一个会话的全部消息放在同一片。单会话超过上限时，该会话单独成片并允许突破条数上限，但字节仍须 ≤ 16 MiB；仍超出则按时间顺序拆成多片，每片带相同 `chat_session_id`，导入端按消息幂等写入，拆片安全。
- 会话分片与消息分片编号一一对应：`sessions-0001.jsonl` 只含 `messages-0001.jsonl` 里消息所属的会话。
- 附件逐个上传，单个请求 ≤ 25 MiB。
- `transfer/config` 请求体沿用 V1 上限 20 MiB；`transfer/conversations` 单请求上限 20 MiB（16 MiB 分片 + 引用索引余量）。

### 7.3 断点续传与增量

| 环节 | 机制 |
| --- | --- |
| 导出中断 | `<out>.partial/state.json` 记录已完成的会话 id 与已下载的附件 sha256；重跑同一命令跳过已完成项，未完成会话从头翻页（单会话重翻代价小，不保存页内游标）。 |
| 导入中断 | 不需要服务端状态：会话、消息、附件的目标 id 是确定性推导的，服务端写入用 `INSERT … ON CONFLICT (id) DO NOTHING`；重跑整个导入即续传。CLI 本地记录已成功的分片序号，只是为了少传，不是正确性前提。 |
| 增量 | 源端之后又聊了新消息：重新导出一个新包，再导入。已有会话与消息命中同 id 跳过，新消息与新会话写入。 |
| 冲突 | 对话实体没有「同名冲突」概念，`on_conflict` 只作用于 `transfer/config`。对话固定为「已存在即跳过」，**不覆盖**目标端已有消息。 |

### 7.4 V2 不做

| 不做 | 原因 / 何时再议 |
| --- | --- |
| 自建 → 官方云 | 官方云无导入端点，不可控；等上游接受 `/transfer/*` 再议 |
| issue、评论、评论表情、任务迁移 | 数据迁移而非配置 / 聊天迁移，单独立项。**V3 已立项并出契约**（kk zi 2026-09-16，DENE-365）：见 `docs/kun/config-transfer-v3-issues.md`。V2 本身的范围不变 |
| 活动日志（`activity_log`）、任务运行记录迁移 | **V3 也不做**，是永久排除项而非延后项：`activity_log` 没有任何读接口，且 `details` 里嵌的是源实例运行态 id；任务运行记录同理。理由见 V3 契约第 1.2 / 1.6 节 |
| 收件箱历史通知（`inbox_item`） | **V3 也不做**：全部由任务完成与 autopilot 配额产生，跨实例没有对应运行记录。V3 改为在导入时重建 `issue_subscriber`，让导入后的新活动能正常进收件箱。理由见 V3 契约第 6 节 |
| 别人的聊天会话 | 读接口拿不到，且属于他人隐私 |
| 源端删除 / 编辑消息同步到目标端 | 需要变更追踪；V2 只追加 |
| 实时或定时双向同步 | 需要持久映射表与冲突合并 |
| 智能体 CLI 续聊指针（`session_id` / `work_dir`）迁移 | 绑定源机器，跨机器无意义；上下文延续见第 4.6 节 |
| ~~自动绑定运行时~~ | **已从排除表移除（DENE-364，kk zi 2026-09-16）**：改为第 4.4.1 节的三档规则——唯一候选自动绑、多候选不猜、零候选记原因，`auto_bind_runtimes` 默认开。仍然不做的是「自动创建 / 自动迁移运行时」本身：绑定只写 `agent_id → runtime_id` 引用，运行时必须已在目标实例上存在且导入者可见。 |
| 密钥随包迁移、口令加密包 | 没有通用 secrets-at-rest 基础设施（沿用 V1 结论）；V2 包本身不加密，靠文件权限与提示 |
| 关闭对话密钥扫描 | 与验收断言冲突 |
| 无法扫描的二进制附件文件体 | 无法证明不含密钥 |
| 服务端导入会话 / 导入进度表 | 确定性 id 已经提供幂等与续传 |
| Desktop / Web 导出按钮 | **Desktop 导出/导入按钮**：V2 追加交付，形态是同一个 CLI 的壳（见同 Stage 姊妹票）。**Web 按钮不做**——官方云没有服务端导出端点，网页端也做不了本地打包。 |
| 导入撤销 | 沿用 V1：配置按批次事务，对话只追加；撤销需要快照 |

## 8. 未确认项（实现前必须验证）

1. `multica` CLI 能否同时持有两个不同服务器的登录档（第 2.3 节）。
2. 源实例成员列表接口是否返回邮箱（第 4.3 节）。
3. 上游 `main` 的读接口能否覆盖 V1 契约第 1 节全部实体分组，尤其 `agent_invocation_target`、`autopilot_collaborator`、`workspace.settings`、集成清单；缺口进 `export_gaps`，实现 Stage 先产出一张「分组 → 读接口 → 是否可用」矩阵。
4. 官方云与 `kun` 的 `system_key` 集合是否一致（第 1.2 节）。
5. 续聊失败回退是否注入 Multica 侧历史消息、注入多少（第 4.6 节）——决定能否对用户说「上下文无缝」。
6. 聊天正文中附件链接的形态（相对路径 / 完整 URL / 附件 id），决定能否在导入时改写（第 3.4 节）。
7. `runtime_profile.display_name` 在工作区内是否唯一（第 4.4 节）。
8. 本产品个人访问令牌与 daemon token 的明文前缀（第 5.3 节 `multica_token` 模式）。
9. 官方云读接口速率限制与分页上限（第 7.1 节）。
10. 导入历史消息时是否会被现有写路径的副作用（标题生成、未读计数、实时推送、渠道投递）触发；导入端必须走不带副作用的专用写入，实现 Stage 逐一确认并在测试里断言无任务入队。

## 9. 给后续 Stage 的交接清单

后端（`server/`，仅 `kun`）：

- 新增 `internal/service/transfer_*.go`：manifest 校验、成员邮箱映射、确定性 id 推导、对话分片幂等写入、附件写入；`transfer/config` 在内部调用 V1 导入内核，不复制其逻辑。
- 新增 `internal/handler/workspace_transfer.go` 三个端点，权限同 V1。
- 专用 sqlc 查询：带显式 `id` 与 `created_at` 的会话 / 消息 / 附件插入，`ON CONFLICT (id) DO NOTHING`；禁止复用会入队任务的发送路径。
- 测试：第 5.4 节全部断言；同一分片导入两次结果不变；同一包导入两个工作区互不冲突；会话引用智能体缺失时整条跳过且报告正确；导入不产生任何 `agent_task_queue` 行。

CLI（`server/cmd/multica/`）：

- `transfer export` / `transfer import` 两个子命令；字段白名单投影；复用 `service` 包内的脱敏函数与 bundle 结构体；`.partial` 断点续传；`--estimate`。
- 从 `GET /api/agents` 分组时用 `system_key`（非空且不以 `agent_builder:` 开头 → `system_agents`），不要读 `kind`。
- 技能排除走 `GET /api/workspaces/{id}/plugins` 的 `resources`；503 / 不可用时全部导出并在 `export_gaps`（或 `warnings[]`）记警告，导入报告回显。
- 用 `httptest` 模拟「上游形态」的读接口做导出测试，不访问真实官方云。
- 新增 CLI 命令后同步更新 `server/internal/service/builtin_skills/*` 下相关 `references/<domain>.md`（CLAUDE.md 规则）。

跨环境验收（真机）：

- 从官方云工作区导出 → 导入 `ai.ferryway.cc` 的新工作区，核对：会话数 / 消息数与 `manifest.stats` 一致；随机抽 3 个会话顺序与说话人正确；`secrets_to_fill` 与 `runtimes_to_bind` 清单可逐项处理；重导同一包无重复。
