# 跨环境迁移包 V2 前置核实 B：目标端导入面（DENE-244）

本页核实契约 `docs/kun/config-transfer-v2.md` §8 中与**目标端导入写入**相关的 4 项（§8.5、§8.7、§8.8、§8.10），外加导入构造 INSERT 所需的一张表结构清单。只读代码核实，不含实现。

核对基线：fork `kun` tip（本页落盘时）`be8e7da2b`；字段以 `server/pkg/db/generated/models.go` 与 `server/migrations/` 为准。不访问官方云。

每项结构：**结论 / 证据 / 对实现的影响**。

## 1. §8.10 导入写入副作用

### 结论

现有聊天写路径 `POST /api/chat/sessions/{id}/messages` **不能**用于导入历史消息。它会以导入者身份新建一条 `role = user` 消息，并在同一事务里入队 `agent_task_queue`，随后触发标题生成、实时推送、任务广播与 daemon 唤醒。

导入端可以用**新增的专用 sqlc 查询**直接 `INSERT` 而**完全不触发**这些副作用。`chat_session` / `chat_message` / `attachment` 三张表上**没有**数据库触发器；副作用全部在 Go handler / `TaskService` 层。禁止复用 `SendChatMessage`、`SendDirectChatMessage`、`CreateChatMessage`、`CreateChatSession`、`CreateAttachment`。

### 证据：现有写路径的副作用触发点

入口：`server/cmd/server/router.go:2249` → `Handler.SendChatMessage`。

| 副作用 | 是否触发 | 触发点 | 说明 |
| --- | --- | --- | --- |
| `agent_task_queue` 入队 | **会** | `server/internal/service/task.go:2343-2367` `CreateChatTask` + `SetChatTaskInputOwnerSelf`；提交后 `task.go:2470-2471` `broadcastTaskEvent(EventTaskQueued)` + `NotifyTaskEnqueued` | 每条发送必建一条 queued 任务。`NotifyTaskEnqueued`（`task.go:6913-6916`）bumps 空 claim 缓存并踢 daemon WS。 |
| 标题生成 | **会**（首条公开 user 消息） | 事务内确定性标题：`task.go:2388-2394` `InitializeChatSessionTitle`；handler 异步 LLM：`server/internal/handler/chat.go:1001-1010` `maybeGenerateChatTitleAsync`（实现 `chat_title.go:92`）；渠道标题事件：`chat.go:964` `ChannelChatTitleInitialized` → `chat_title.go:40` 发 `chat:session_updated` | 只在「此前没有公开 user 消息」时跑（`chat.go:920-923` `ChatSessionHasPublicUserMessage`）。导入整段历史若走此路径，会把第一条当「开场」改标题。 |
| 未读计数 | **发送路径本身不写未读列**；完成路径会让未读变多 | 未读是列表查询派生，不是写时戳列：`server/pkg/db/queries/chat.sql:46-49` `count(*) … role = 'assistant' AND created_at > cs.last_read_at`。发送只写 user 行 + `TouchChatSession`（`task.go:2453`，`updated_at = now()`）。助手行在任务完成时由 `writeChatCompletionOutcome` 写入；注释见 `task.go:4416-4421` | 走发送路径：user 行不增加未读；等任务跑完写出 assistant 行才会涨。直接 INSERT 历史 assistant 行时，若 `created_at` 早于 `last_read_at`（默认 `now()`），未读仍为 0。 |
| 实时推送 | **会** | `chat.go:990-999` `publishChat(EventChatMessage, …)`（`handler.go:785` 进 Bus）。任务侧 `task.go:2470` `EventTaskQueued` | 每条发送立刻广播一条 `chat:message`。导入若走此路径，会向在线客户端刷屏。 |
| 渠道投递 | **发送路径不投递** | `SendDirectChatMessage`（`task.go:2274-2472`）无 `channel_task_delivery` / outbound 调用。渠道出站发生在助手完成 / 渠道引擎，不在 web 发送 | 现有 web 发送不会把这条 user 消息推到飞书 / Slack。导入更不应走渠道引擎。 |
| 分析埋点 | **会** | `chat.go:977-988` `analytics.ChatMessageSent` | 非产品可见副作用，导入仍应避开。 |
| onboarding 收养 | **可能** | `task.go:2378-2383` `AdoptOrphanOnboardingKickoff` | 仅 Mika 开场会话；导入路径不应触碰。 |

`chat_session` / `chat_message` / `attachment` 的迁移里没有 `CREATE TRIGGER`。仓库里的触发器挂在 `agent_task_queue`、plugin、usage rollup 等表上，不挂这三张对话表。因此「只 INSERT 行、不调 service」即可跳过上表全部 Go 副作用。

现有插入查询**不够用**，不能当导入内核：

- `CreateChatSession`（`chat.sql:1-4`）从 `agent.runtime_id` 回填 `runtime_id`，不写 `created_at` / `updated_at` / `status` / `pinned_at` / `explicitly_created_at`。
- `CreateChatMessage`（`chat.sql:480-505`）不接受 `created_at`（走列默认 `now()`），会毁掉历史时间顺序。
- `CreateAttachment`（`attachment.sql:1-26`）不写 `chat_message_id` / `created_at`，且会 bump `issue.revision` / `comment.revision`。

### 对实现的影响

服务端 `transfer/conversations` / `transfer/attachments` 必须新增专用 sqlc，形态：

```sql
INSERT INTO chat_session (id, workspace_id, agent_id, creator_id, title, status,
    pinned_at, is_agent_intro, explicitly_created_at, project_id, created_at, updated_at)
VALUES (...)
ON CONFLICT (id) DO NOTHING;

INSERT INTO chat_message (id, chat_session_id, role, content, message_kind,
    failure_reason, elapsed_ms, created_at, quick_actions)
VALUES (...)
ON CONFLICT (id) DO NOTHING;

INSERT INTO attachment (id, workspace_id, chat_session_id, chat_message_id,
    uploader_type, uploader_id, filename, url, content_type, size_bytes, created_at)
VALUES (...)
ON CONFLICT (id) DO NOTHING;
```

显式列（相对契约 §3.2）：

- 会话：`id`（确定性 UUIDv5）、`workspace_id`（目标）、`creator_id`（导入者）、`agent_id` / `project_id`（映射后）、`title` / `status` / `pinned_at` / `is_agent_intro` / `explicitly_created_at` / `created_at` / `updated_at`。**不要写** `session_id`、`work_dir`、`runtime_id`（保持 NULL）。`last_read_at` 用默认 `now()` 或显式写成导入时刻，使历史 assistant 行 `created_at > last_read_at` 为假，导入后视为已读。`unread_since` 保持 NULL。
- 消息：`id`、`chat_session_id`、`role`、`content`、`message_kind`、`failure_reason`、`elapsed_ms`、`created_at`。`task_id` NULL；`quick_actions` `'[]'`；渠道列全 NULL / `channel_ingested = false`。
- 附件：见第 5 项。`url` 是 NOT NULL 无默认，有文件体时写目标存储 URL；无文件体时仍须给一个非空占位（空字符串在 schema 上合法），下载路径按 URL 解析存储 key，占位值应让下载失败而不是去拉源实例。

契约 §2.4 的 `finalize: true` 只发一次工作区级列表失效（`chat:session_updated` 或等价 invalidate），不要在每条 INSERT 后 `publishChat`。测试必须断言：导入前后 `agent_task_queue` 行数增量 = 0。

---

## 2. §8.5 续聊回退语义

### 结论

**不能宣称「上下文无缝」。** 对外口径必须是契约 §4.6 的保守句：**记录可见，智能体从新会话开始。**

`daemon_chat_resume_fallback_test.go` 对应的回退路径**只恢复 CLI 续聊指针**（`session_id` / `work_dir`），**不会**把 Multica 侧历史消息注入提示词。注入量 = **0 条历史**。当前回合提示词只含本回合 user 消息（`ListChatInputMessages` 按 `task_id` 取，不是按会话全文）。

导入后 `session_id` / `work_dir` / `runtime_id` 均为 NULL，且不迁 `agent_task_queue`，回退查询也找不到任何历史 task 行，指针仍空。用户在目标端发第一条新消息时，智能体以全新 CLI 会话启动，提示词里只有这一条新消息。

### 证据

回退是否需要，只看指针空不空，与 transcript 无关：

```2278:2280:server/internal/handler/daemon.go
func chatSessionResumeFallbackNeeded(priorSessionID, priorWorkDir string) bool {
	return priorSessionID == "" || priorWorkDir == ""
}
```

测试（`daemon_chat_resume_fallback_test.go:37-56`）只断言「session 或 workdir 缺一即 true」，不涉及历史消息。

指针来源（同 runtime 才用 `chat_session.session_id`）：`daemon.go:3022-3036`。缺指针时回退：

```3091:3107:server/internal/handler/daemon.go
if task.ChannelContextRevision.Valid || chatSessionResumeFallbackNeeded(resp.PriorSessionID, resp.PriorWorkDir) {
    ...
    prior, err := h.Queries.GetLastChatTaskSession(...)
    ...
    if resp.PriorSessionID == "" && prior.RuntimeID == task.RuntimeID {
        resp.PriorSessionID = prior.SessionID.String
    }
    if prior.WorkDir.Valid && resp.PriorWorkDir == "" {
        resp.PriorWorkDir = prior.WorkDir.String
    }
}
```

`GetLastChatTaskSession`（`chat.sql:1127-1133`）扫的是 **`agent_task_queue` 里最近一条记下了 `session_id` 的任务**，不是 `chat_message`。导入不迁任务表 → 此查询 `ErrNoRows` → miss。

本回合提示词输入：

- `daemon.go:3052-3055`：`ListChatInputMessages(task.ChatInputTaskID)`。
- `chat.sql:837-847`：`SELECT * FROM chat_message WHERE task_id = $1 AND role = 'user'`。只取**本任务**的 user 行。
- `daemon.go:3177-3189`：把这些行拼进 `resp.ChatMessage`。
- `server/internal/daemon/prompt.go:695`：`User message:\n%s`，即当前回合正文。web 直连聊天（`ChatChannelType == ""`）**不会**在冷启动提示词里命令 `multica chat history`（`prompt.go:639-663` 整段 gated 在渠道类型上；`writeWorkflowChat` 在 `runtime_config_sections.go:542-551` 也不提 history）。

连续性通知**不是**历史注入，且导入后通常不会出现：

- 文案 `SessionContinuityNoticeChatTranscript`（`runtime_config_sections.go:514-515`）告诉智能体「用 `multica chat history` 自己去读」，并不把 transcript 塞进 prompt。
- 该通知只在 `PriorSessionResumeUnavailable` 时追加（`prompt.go:68-70`）。该标志来自 `GetLatestChatTaskRolloutMissing`（`agent.sql:1177-1191`），看最近一条 **terminal task** 的 `session_rollout_missing`。无任务行时 `err != nil`，`daemon.go:3128-3129` 不置位。

因此：回退既不注入历史，导入场景下连「去读 history」的提示都不会自动出现。智能体工作记忆（CLI 本地会话里试过什么、排除了什么）必然丢失。

### 对实现的影响

- 操作手册与 UI 文案禁止写「上下文无缝 / 接着聊」。写「聊天记录、标题、时间顺序在目标端完整可见；智能体从新会话开始，不会带着源机器上的续聊记忆」。
- 不要为实现「无缝」去改 claim 路径、把全文 transcript 注入 prompt——那超出 V2 范围，且会与 `ListChatInputMessages` 的「本回合输入批次」契约冲突。
- 若产品以后想让导入后的第一轮自动 `multica chat history`，那是独立增强，不是本核实的默认行为。当前 web 冷启动 prompt 没有这条命令。

---

## 3. §8.7 `runtime_profile.display_name` 唯一性

### 结论

**在工作区内唯一。** 契约 §4.4 把 `display_name` 当作身份键**可以成立**。约束是大小写敏感的 `UNIQUE (workspace_id, display_name)`，应用层把唯一冲突映射为 HTTP 409。

### 证据

表定义（`server/migrations/120_runtime_profile.up.sql:32-65`）：

```sql
display_name TEXT NOT NULL,
...
UNIQUE (workspace_id, display_name)
```

后续迁移没有改这条唯一约束，也没有改成 `lower(display_name)`。PostgreSQL 对 `TEXT` 的 UNIQUE 默认大小写敏感：`Foo` 与 `foo` 可共存。

应用层：

- 创建：`server/internal/handler/runtime_profile.go:187-189`，`isUniqueViolation` → 409 `"a runtime profile with this display_name already exists"`。
- 更新：同文件 `325-327`，同样 409。
- 写入前 `TrimSpace`（`145`、`289`），不做大小写折叠。

### 对实现的影响

- `transfer/config` 导入 `runtime_profile` 用 `display_name`（trim 后原样）做身份键，与 DB 唯一约束对齐。
- `on_conflict` 的 collide 检测应对 **精确字符串**（trim 后），不要自行 `lower()`，否则会把 DB 允许并存的 `Foo`/`foo` 误判为冲突。
- 冲突时走 V1 的 `fail | overwrite | rename | skip`；overwrite 更新同名行，不要再 INSERT 一条去撞 23505。

---

## 4. §8.8 token 明文前缀

### 结论

契约 §5.3 `multica_token` 扫描至少要覆盖下面两个（本项点名的 PAT 与 daemon token）：

| 种类 | 前缀字面量 | 生成函数 |
| --- | --- | --- |
| 个人访问令牌（PAT） | `mul_` | `GeneratePATToken` |
| daemon token | `mdt_` | `GenerateDaemonToken` |

同一产品里还有这些明文前缀，建议一并纳入 `multica_token`（漏放代价同样是密钥外泄）：

| 种类 | 前缀字面量 | 出处 |
| --- | --- | --- |
| 任务作用域 agent token | `mat_` | `GenerateAgentTaskToken` |
| Cloud Node PAT | `mcn_` | `auth.CloudPATPrefix` |
| plugin install token | `mpi_` | `plugin_token.go` `installTokenPrefix` |
| plugin callback token | `mpc_` | `plugin_token.go` `callbackTokenPrefix` |

**最小必扫：`mul_`、`mdt_`。推荐同 kind 一并扫：`mat_`、`mcn_`、`mpi_`、`mpc_`。**

本页只写前缀形态，不含任何真实 token。

### 证据

```60:75:server/internal/auth/jwt.go
// GeneratePATToken creates a new personal access token: "mul_" + 40 random hex chars.
func GeneratePATToken() (string, error) {
    ...
    return "mul_" + hex.EncodeToString(b), nil
}

// GenerateDaemonToken creates a new daemon auth token: "mdt_" + 40 random hex chars.
func GenerateDaemonToken() (string, error) {
    ...
    return "mdt_" + hex.EncodeToString(b), nil
}
```

同文件 `78-88`：`GenerateAgentTaskToken` → `"mat_" + hex`。

`server/internal/auth/cloud_pat.go:18-24`：`const CloudPATPrefix = "mcn_"`。CLI 登录接受的 PAT 前缀是 `mul_` 与 `mcn_`（`server/cmd/multica/cmd_auth.go:25-30` `loginTokenPrefixes`）。

`server/internal/service/plugin_token.go:36-37`：`installTokenPrefix = "mpi_"`，`callbackTokenPrefix = "mpc_"`。

形态：`mul_` / `mdt_` / `mat_` 后面是 40 个 hex 字符（20 字节）；`mcn_` 由 Cloud Fleet 签发；`mpi_` / `mpc_` 后面是 raw URL-safe base64。扫描实现用前缀匹配即可，不必在本页写正则。

### 对实现的影响

- 导出端内容扫描的 `multica_token` kind：命中以上前缀的片段替换为 `[REDACTED:multica_token]`。
- 金丝雀用假值，例如 `mul_` + 40 个 `a`，不要用真实签发物。
- 不要把 JWT（`eyJ` 开头）并进 `multica_token`——契约已有独立 `jwt` kind。

---

## 5. 导入写入必填列清单

「必填」= **NOT NULL 且无默认值**，INSERT 必须显式提供。其余列可省略（走默认）或按契约显式写入。唯一冲突风险以 PK / UNIQUE 为准。

### 5.1 `chat_session`

来源：`033_chat.up.sql` + `040`/`060`/`151`/`154`/`155`/`214`/`420`；结构体 `models.go:509-527`。

| 列 | 空约束 | 默认 | 导入时 |
| --- | --- | --- | --- |
| `id` | PK | `gen_random_uuid()` | **显式** UUIDv5 |
| `workspace_id` | NOT NULL | 无 | **必填** 目标工作区 |
| `agent_id` | NOT NULL | 无 | **必填** 映射后 |
| `creator_id` | NOT NULL | 无 | **必填** 导入者 |
| `title` | NOT NULL | `''` | 显式写导出值 |
| `session_id` | NULL | — | **不写**（NULL） |
| `work_dir` | NULL | — | **不写** |
| `status` | NOT NULL | `'active'` | 显式写导出值（`active`/`archived`） |
| `created_at` | NOT NULL | `now()` | **显式** 源时间 |
| `updated_at` | NOT NULL | `now()` | **显式** 源时间 |
| `unread_since` | NULL | — | 不写 |
| `runtime_id` | NULL | — | **不写** |
| `last_read_at` | NOT NULL | `now()` | 可省略（默认即「已读」）或显式导入时刻 |
| `is_agent_intro` | NOT NULL | `FALSE` | 显式写导出值 |
| `pinned_at` | NULL | — | 有则写 |
| `project_id` | NULL | — | 映射后或 NULL |
| `explicitly_created_at` | NULL | — | 有则写 |

`status` CHECK：`'active' | 'archived'`（`033_chat.up.sql:12`）。

**唯一 / 索引冲突：** 只有 `PRIMARY KEY (id)`。无其它 UNIQUE。非唯一索引：`idx_chat_session_workspace`、`idx_chat_session_creator (creator_id, workspace_id)`、`idx_chat_session_pinned`（partial `pinned_at IS NOT NULL`）、`idx_chat_session_project`（partial）。`ON CONFLICT (id) DO NOTHING` 足够幂等，不会因标题/创建者撞车。

### 5.2 `chat_message`

来源：`033` + `062`/`063`/`159`/`225`/`226`/`235`/`377`；结构体 `models.go:480-498`。

| 列 | 空约束 | 默认 | 导入时 |
| --- | --- | --- | --- |
| `id` | PK | `gen_random_uuid()` | **显式** UUIDv5 |
| `chat_session_id` | NOT NULL | 无 | **必填** 已映射会话 id |
| `role` | NOT NULL | 无 | **必填** `user` 或 `assistant` |
| `content` | NOT NULL | 无 | **必填**（扫描后正文；允许 `''`） |
| `task_id` | NULL | — | **不写** |
| `created_at` | NOT NULL | `now()` | **显式** 源时间（否则顺序失真） |
| `failure_reason` | NULL | — | 有则写 |
| `elapsed_ms` | NULL | — | 有则写 |
| `message_kind` | NOT NULL | `'message'` | 显式写导出值 |
| `channel_media_pending_until` | NULL | — | 不写 |
| `channel_ingested` | NOT NULL | `FALSE` | 可省略 |
| `quick_actions` | NOT NULL | `'[]'::jsonb` | 显式 `'[]'`（契约排除） |
| `channel_context_revision` | NULL | — | 不写 |
| `channel_outbound_type` | NULL | — | 不写 |
| `channel_outbound_installation_id` | NULL | — | 不写 |
| `channel_outbound_chat_id` | NULL | — | 不写 |
| `channel_outbound_message_ids` | NULL | — | 不写 |

`role` CHECK：`'user' | 'assistant'`（`033_chat.up.sql:24`）。`message_kind` 无 CHECK（`159_chat_message_message_kind.up.sql:14-17`）。

**唯一 / 索引冲突：** 只有 `PRIMARY KEY (id)`。非唯一：`idx_chat_message_session (chat_session_id, created_at)`、`idx_chat_message_input_owner (task_id, created_at) WHERE role = 'user'`、`idx_chat_message_assistant_task (task_id, created_at DESC) WHERE role = 'assistant'`。同会话同时间戳多行合法（Mika opening 用 +1µs 只是为了列表预览，不是唯一约束）。`ON CONFLICT (id) DO NOTHING` 足够。

### 5.3 `attachment`

来源：`029_attachment.up.sql` + `083`/`164`/`407`；结构体 `models.go:190-206`。

| 列 | 空约束 | 默认 | 导入时 |
| --- | --- | --- | --- |
| `id` | PK | `gen_random_uuid()` | **显式** UUIDv5 |
| `workspace_id` | NOT NULL | 无 | **必填** 目标工作区 |
| `issue_id` | NULL | — | **不写**（聊天附件） |
| `comment_id` | NULL | — | **不写** |
| `uploader_type` | NOT NULL | 无 | **必填** `'member'`（CHECK：`member`/`agent`） |
| `uploader_id` | NOT NULL | 无 | **必填** 导入者 user id |
| `filename` | NOT NULL | 无 | **必填** |
| `url` | NOT NULL | 无 | **必填**（有文件体：目标存储 URL；无文件体：非空占位，见 §1） |
| `content_type` | NOT NULL | 无 | **必填** |
| `size_bytes` | NOT NULL | 无 | **必填** |
| `created_at` | NOT NULL | `now()` | **显式** 源时间 |
| `chat_session_id` | NULL | — | 写映射后会话 id |
| `chat_message_id` | NULL | — | 写映射后消息 id（消息级附件） |
| `task_id` | NULL | — | **不写** |
| `source_context_id` | NULL | — | 不写 |

**唯一 / 索引冲突：** 只有 `PRIMARY KEY (id)`。非唯一：issue / comment / workspace / chat_session / chat_message / task / source_context 索引。文件体按 sha256 内容寻址是包格式去重，不是 DB 唯一。`ON CONFLICT (id) DO NOTHING` 足够。

`CreateAttachment` 会 bump issue/comment revision——导入查询不要复用它。

### 5.4 写入顺序

会话 → 消息 → 附件（附件行引用后两者的确定性 id）。应用层保证引用存在；这三张表的跨表 FK 是历史遗留（`033`/`083`），新迁移不再加 FK，但旧 FK 仍在。先插会话再插消息，否则 `chat_message.chat_session_id` 的 FK 会失败。

---

## 给 Stage 3 服务端实现票的一句话

专用 INSERT、显式 `id`+`created_at`、`ON CONFLICT (id) DO NOTHING`、不调 `SendDirectChatMessage`；`display_name` 可当 runtime_profile 身份键；扫描前缀至少 `mul_` / `mdt_`（建议加上 `mat_` / `mcn_` / `mpi_` / `mpc_`）；对外只说「记录可见，智能体从新会话开始」。
