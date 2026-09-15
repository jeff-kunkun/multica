# 跨环境迁移包 V2 前置核实 A：源端读接口（DENE-243）

本页回答 V2 契约 `docs/kun/config-transfer-v2.md` §8 中与**源端导出面**相关的 6 项（§8.1–8.4、§8.6、§8.9）。只核实、不实现。核对基线：

- fork `kun` tip `be8e7da2`（含 V2 契约 PR #48）
- 上游 `origin/main` `408bf2b2`

方法：对照 `server/cmd/server/router.go` 与对应 handler / sqlc；`origin/main` 与 `kun` 的路由差集用 `git show` 比较。未访问官方云、未使用真实凭据。

每项固定三段：**结论 / 证据 / 对实现的影响**。查不到的写「未确认」并给保守建议。

---

## 1. §8.1 CLI 双登录档

### 结论

**能。** `multica` CLI 可以同时持有两个（以及更多）不同服务器的登录档，互不覆盖。V2 的 `multica transfer export --profile <源> / import --profile <目标>` 不需要先补「多登录档」能力。

### 证据

路径按 profile 隔离：

| profile | 配置文件 | 状态目录（pid / log / daemon） |
| --- | --- | --- |
| 默认（`--profile` 空） | `~/.multica/config.json` | `~/.multica/` |
| 命名（如 `cloud`、`selfhost`） | `~/.multica/profiles/<name>/config.json` | `~/.multica/profiles/<name>/` |

- `CLIConfigPathForProfile` / `ProfileDir`：`server/internal/cli/config.go:228-278`
- `--profile` 是 root persistent flag：`server/cmd/multica/main.go:44`（注释写明 isolates config, daemon state, and workspaces）
- `setup` 文档示例：`multica setup self-host --profile staging --server-url https://api-staging.co`（`server/cmd/multica/cmd_setup.go:34-35`）
- 每个 profile 的 `CLIConfig` 自带 `server_url` + `token`（`config.go:23-27`），`SaveCLIConfigForProfile` 只写该 profile 自己的文件（`config.go:335-396`），默认档不会被命名档覆盖。

优先级（实现必须遵守，否则两个 profile 会串）：

| 项 | 顺序（高 → 低） |
| --- | --- |
| Token | 进程环境变量 `MULTICA_TOKEN` → 当前 `--profile` 文件的 `token`。**没有** `--token` 命令行参数。`resolveToken`：`server/cmd/multica/cmd_auth.go:73-86` |
| Server URL | `--server-url` 旗标 → `MULTICA_SERVER_URL` → 当前 `--profile` 文件的 `server_url`。`FlagOrEnv`：`server/internal/cli/flags.go:12-20`；`tryResolveExplicitServerURL` / `tryResolveProfileServerURL`：`server/cmd/multica/cmd_agent.go:316-331` |

并发与共享状态：

- 两个 CLI 进程用不同 `--profile` 时，读写的是不同文件与不同 `ProfileDir`，没有进程间共享的登录态。
- **会串的只有进程级环境变量**：同一 shell 里 `export MULTICA_TOKEN=...` 或 `export MULTICA_SERVER_URL=...` 会压过**所有** profile 文件。`transfer` 实现必须在调用时清掉这两项，或保证只靠 `--profile`，不允许用环境变量「临时改目标」。
- 守护进程任务内 `MULTICA_TASK_CONFIG_ROOT` 会把配置根改到任务私有目录（`config.go:14-19, 231-232`）；那是任务隔离，不是人类双登录档。人类本机双 profile 不受影响。

### 对实现的影响

- `transfer export --profile cloud` / `transfer import --profile selfhost` 可以直接做，不必先加多登录档。
- 命令行**不要**加明文 `--token`（契约 §2.3 已禁止）。
- 实现 Stage 测试：两个临时 HOME 下的 profile 文件互不覆盖；设 `MULTICA_TOKEN` 时必须压过 profile 文件（回归保护，不是推荐用法）。

---

## 2. §8.2 成员邮箱

### 结论

**返回。** 源实例成员列表接口返回字段名 `email`。`people.json` 可以包含被引用到的全体成员邮箱，不必降级到「只有导出者本人」。

例外：某成员的 `email` 可能是空字符串（列是 `TEXT UNIQUE NOT NULL`，允许 `""`）。空邮箱无法跨实例匹配，按契约 §4.3 降级。

### 证据

- 路由：`GET /api/workspaces/{id}/members` → `h.ListMembersWithUser`（`server/cmd/server/router.go:1577`）。`origin/main` 同样存在。
- 响应结构 `MemberWithUserResponse.Email string json:"email"`（`server/internal/handler/workspace.go:488-497`），handler 赋值 `Email: m.UserEmail`（`:521`）。
- `GET /api/me` 同样返回 `email`：`UserResponse.Email`（`server/internal/handler/auth.go:63-67, 93-96`）。
- DB：`user.email TEXT UNIQUE NOT NULL`（`server/migrations/001_init.up.sql:8`）；生成模型是 `string` 不是指针（`server/pkg/db/generated/models.go:1414`）。空串合法，只是不能有两个空串（唯一约束）。

未实测官方云响应体（禁止访问）。`kun` 与 `origin/main` 该 handler 同形，官方云跑上游代码时应同样返回 `email`。

### 对实现的影响

- 导出 `people.json`：对 bundle 里实际引用到的 `user_id`，从 `GET /api/workspaces/{id}/members` 按 `user_id` 取 `email` + `role`。
- `email == ""` 的成员：不写入 `people.json`，导入按 `member_unmapped` 降级；导出者本人仍走 `GET /api/me` → 一律映射为导入者（契约 §4.2），不看邮箱。
- `--no-people` 行为不变。

---

## 3. §8.3 读接口覆盖矩阵（V1 全部实体分组）

### 结论

上游 `origin/main` **有** V1 第 1 节全部实体分组所需的读接口（`kun` 多出来的只是 `GET .../config/export`，V2 导出不得调用）。没有整组 `read_api_missing`。

有三处**字段级**缺口，实现必须按缺口处理，不要猜：

| 缺口 | 归类 | 处理 |
| --- | --- | --- |
| `workspace.attribution_fail_closed` 不在 `GET /api/workspaces/{id}` | 字段缺失（非整组 `read_api_missing`） | 该字段不导出，保持目标默认值 |
| `skill.plugin_installation_id` 不在技能读接口 | 字段缺失 | 技能接口不暴露归属，但可经 `GET /plugins` 的 `resources` 反查排除；仅在插件接口不可用时降级 |
| V1 `system_agents` 若按内部字段 `kind = 'system'` 判定，在产品里几乎是空集 | 契约已改为 `system_key` 判定，见 §4 / DENE-258 | 把 `system_key` 非空且不以 `agent_builder:` 开头的行（产品上即 Mika）从 agents 列表拆到 `system_agents` |

其余分组的契约导出字段都能从读接口拿到（部分需要详情接口或二次 GET，不是缺失）。

`plugin_installation_config` / `plugin_grant`：迁移 `344_plugin_v2_reset` 已删表，V1 契约已声明不再涉及。矩阵里标「不适用」。

### 证据（路由差集）

对 `server/cmd/server/router.go` 比较 `HEAD`（kun）与 `origin/main`：kun 多出的 GET 只有 `/config/export`。下列读路径两边都有。

### 覆盖表

「覆盖契约字段」只问**读接口能否拿到 V1 §1 列出的导出字段**（不含 `source_id` 这种导出端改写）。秘密字段按契约本来就不该从读接口拿明文。

| 分组（V1 `entities` / 清单） | 读接口 | `origin/main` | `kun` | 覆盖契约导出字段 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| `workspace` | `GET /api/workspaces/{id}` | 有 | 有 | `settings`（JSON 整体，含 `always_redact_env`）、`context`、`repos`、`issue_prefix`；`name`/`slug`/`description`/`avatar_url` 作 source 元数据 | **`attribution_fail_closed` 不在 `WorkspaceResponse`**（`workspace.go:97-108`）。列在 DB / V1 bundle 里，读接口不回。 |
| `labels`（`issue_label`） | `GET /api/labels` | 有 | 有 | `name`、`color`、`description`、`resource_type` | 无 |
| `issue_statuses` | `GET /api/issue-statuses` | 有 | 有 | `key`、`name`、`description`、`category`、`color`、`position`、`is_system`；`archived_at` 可转 `archived` | 无。默认列表含归档态需看 query；没有则带 `archived_at` 的行仍可能出现，实现按 V1 默认跳过归档。 |
| `issue_properties` | `GET /api/properties` | 有 | 有 | `name`、`type`、`description`、`icon`、`config`（含 `options[]`）、`position` | 无 |
| `skills` + `files` + `label_ids` | `GET /api/skills`（无 `content`）+ `GET /api/skills/{id}`（有 `content`）+ `GET /api/skills/{id}/files` + `GET /api/skills/{id}/labels` | 有 | 有 | `name`、`description`、`content`、`config`、files `path`/`content`、label ids | **列表/详情都不返回 `plugin_installation_id`**（`skill.go:46-56, 167-178`）。V1 SQL 导出用 `plugin_installation_id IS NULL`（`config_transfer.sql:81-86`）。CLI 不靠该字段：用 `GET /api/workspaces/{id}/plugins` 的 `resources`（`type == "skill"` 的 `key` 即技能名）反查排除。该接口 503 / 不可用时全部导出并在 manifest 记警告。 |
| `mcp_servers`（`workspace_mcp_server`，仅清单） | `GET /api/workspaces/{id}/mcp-servers` | 有 | 有 | `name`、`transport`（`mcpTransportOf`）。`config` write-only，符合契约 | 无 |
| `agents`（无 `system_key`）+ skills + label_ids + mcp_servers + invocation_targets | `GET /api/agents`（含 skills 摘要、`invocation_targets`、`custom_args`、`runtime_config`、`disabled_runtime_skills`、`composio_toolkit_allowlist`）+ `GET /api/agents/{id}/labels` + `GET /api/agents/{id}/mcp-servers` | 有 | 有 | 契约列出的用户智能体字段都能拿到。`custom_env` 只有 `has_custom_env` / `custom_env_key_count`。`runtime_config.gateway.token` 为 `***`。`mcp_config` 可能按 `always_redact_env` / 非 owner 脱敏 | 列表只含产品可见行（SQL 仍是 `kind = 'user'`，`agent.sql:1-4`），**含 Mika**。CLI 分组用 `system_key`：无 `system_key` 留在 `agents`。`custom_args` 可能是明文，必须走 V2 §5.2 白名单 + `redactSecretArgs`。label_ids / mcp 绑定要二次 GET。可见性：普通成员看不到别人的 private agent，导出必须用 owner/admin。 |
| `agent_invocation_target`（内嵌在 agents） | 随 `GET /api/agents` / `GET /api/agents/{id}` 的 `invocation_targets[]`：`target_type`、`target_id` | 有 | 有 | 覆盖。`workspace` 行 `target_id` 可空 | 无独立路由，不需要。 |
| `system_agents`（契约：`system_key` 非空且不以 `agent_builder:` 开头） | 无单独列表。从 `GET /api/agents` 按 `system_key` 拆出。内部 `kind = 'system'` 的 builder 载体走 `GET /api/agent-builder/sessions`，不导出 | 有（同上） | 有 | 产品常量 `system_key` 集合见 §4。`AgentResponse` 有 `system_key`、没有 `kind`。读接口拿得到 Mika（`system_key = "mika"`） | **V2 CLI 可按契约分组**。V1 服务端 SQL（`ExportSystemAgents` 过滤 `kind = 'system'`）仍漏掉 Mika，那是 DENE-259 的代码缺陷，本页不改代码。见 §4。 |
| `squads` + `members` | `GET /api/squads`（含 `members[]`：`member_type`/`member_id`/`role`） | 有 | 有 | `name`、`description`、`instructions`、`avatar_url`、`leader_id`、members | 无。`role = 'leader'` 行在列表里也有，导入时按 V1 跳过以免与创建路径重复。 |
| `projects` + `resources` | `GET /api/projects` + `GET /api/projects/{id}/resources` | 有 | 有 | 项目字段全覆盖。`resource_type`、`resource_ref`、`label`、`position` | 无。`local_directory.daemon_id` 在 `resource_ref` JSON 里，跨实例按 V1/V2 跳过。 |
| `autopilots` + triggers + subscribers + collaborators | `GET /api/autopilots`（含 `subscribers`）+ `GET /api/autopilots/{id}`（含 `triggers`、`collaborators`） | 有 | 有 | 契约字段覆盖。`webhook_token` 对 writer 会返回（秘密，导出必须丢掉并登记）。`signing_secret` 不在读接口 | 列表**不含** collaborators / 完整 triggers，必须逐条 GET 详情。 |
| `autopilot_collaborator`（内嵌） | `GET /api/autopilots/{id}` → `collaborators[]`：`user_type`、`user_id`（`granted_by` 导入改写，可读可丢） | 有 | 有 | 覆盖 | 无独立 GET 列表；详情即可。 |
| `quick_actions` | `GET /api/quick-actions` | 有 | 有 | `name`、`description`、`assignee_type`、`assignee_id`、`prompt`、`visibility`、`status` | SQL 已限制 `visibility = 'public' OR created_by_id = viewer`（`quick_action.sql:10-13`），与 V1「public + 导出者自己的 private」一致。 |
| `issue_views` | `GET /api/issue-views?scope_type=...` | 有 | 有 | `name`、`scope_type`、`scope_id`、`scope_variant`、`visibility`、`definition_version`、`query`、`display` | **必须带 `scope_type`**（缺则 400，`issue_view.go:240-244`）。实现要分别拉 `workspace` / `project`（每个项目一次）等 scope。SQL 返回「自己的 + `visibility = 'workspace'`」（`issue_view.sql:13-17`），private/`my` 只有导出者自己的，符合 V1。单 scope 上限 200。 |
| `integrations.workspace_mcp_server` | 同 `mcp_servers` | 有 | 有 | `name`、`transport` | 无 |
| `integrations.vcs_connection` | `GET /api/workspaces/{id}/vcs/connections` | 有 | 有 | `provider`、`instance_url`、`account_login`。无 token | **官方云 `VCSIntegrationEnabled` 为 false 时返回空列表 + `available: false`**（`vcs.go:116-127`）。这是部署开关，不是缺路由；记空清单即可，不要报 `read_api_missing`。 |
| `integrations.github_installation` | `GET /api/workspaces/{id}/github/installations` | 有 | 有 | `account_login`、`account_type`。非 admin 会把 `installation_id` 抹掉，契约本就不导该字段 | 无 |
| `integrations.channel_installation` | 按渠道：`GET .../lark/installations`、`/slack/installations`、`/wecom/installations`、`/dingtalk/installations`、`/telegram/installations` | 有 | 有 | 均含 `agent_id`；渠道类型由路径决定，映射到契约 `channel_type` | 无统一 `GET /channel-installations`。实现要打 5 个列表。`config` / token 不在这些响应里。 |
| `plugins_to_reinstall`（`plugin_installation`） | `GET /api/workspaces/{id}/plugins` | 有 | 有 | `plugin_key`、`version`、`enabled`、`granted_scopes`、`config`（非 secret） | **feature flag `PluginsV1` 默认 false**（`featureflags/keys.go:57-59`）。关闭时 503「Plugin management is not enabled」。官方云是否打开**未确认**（禁止打官方云）。关掉时该组记 `export_gaps` `read_api_error` status 503，其余组继续。 |
| `plugin_installation_config` | 无（表已删） | 不适用 | 不适用 | — | 迁移 `344_plugin_v2_reset.up.sql` DROP。不进 `export_gaps`。 |
| `plugin_grant` | 无（表已删） | 不适用 | 不适用 | — | 同上。现行 `granted_scopes` 在 `plugin_installation` 读接口里。 |

### 对实现的影响

- 导出路径只走上表读接口，**永不调用** `GET .../config/export`。
- `issue_views` 要按 scope 枚举，不能一次 GET 无参数。
- `autopilots` 必须 `list` + 逐条 `GET /{id}`。
- `skills`：详情才有 `content`。优先用 `GET /api/workspaces/{id}/plugins` 的 `resources`（`type == "skill"` 的 `key`）反查排除插件技能；该接口 503（PluginsV1 关闭）或不可用时**全部导出**，并在 `manifest.export_gaps` 记警告（建议 `plugin_skills_unfiltered`），导入报告必须回显。不要静默丢技能。
- `workspace.attribution_fail_closed`：不导出。
- 智能体：从 `GET /api/agents` 按 `system_key` 分组（非空且不以 `agent_builder:` 开头 → `system_agents`）；`***` 与明文 `custom_args` 按 V2 §5.2 处理。不要读 `kind`。

---

## 4. §8.4 `system_key` 集合

### 结论

**上游 `origin/main` 与 `kun` 的产品 `system_key` 常量一致。** 差集为空。

| 来源 | `kun` | `origin/main` |
| --- | --- | --- |
| 产品常量 | `mika` | `mika` |
| 运行时动态 | `agent_builder:<uuid>`（每个 builder 会话一条，`kind = 'system'`） | 同左 |

对契约 §1.2 `system_agent_not_in_target` 的实际影响：

- **V2 CLI（DENE-258 修订后）**：按 `system_key` 非空且不以 `agent_builder:` 开头分组，能把 Mika 放进 `system_agents`，导入按 `system_key` patch。
- **V1 服务端导出（代码未改，DENE-259）**：`ExportSystemAgents` SQL 仍要 `kind = 'system' AND system_key NOT LIKE 'agent_builder:%'`（`config_transfer.sql:129-137`），而 Mika 被明确建成 `kind = 'user'`（`builtin_agents.go:8-16`），所以 V1 同实例导出仍不会用 `system_key` 去 patch Mika。

### 证据

- `const MikaSystemKey = "mika"`：`server/internal/service/builtin_agents.go:16`（`origin/main` 同一行同一值）。
- 注释写死：`kind = 'system'` 表示「不可见执行载体」，Mika 必须相反，所以留在 `kind = 'user'`。
- Builder：`agent_builder.go:116` `fmt.Sprintf("agent_builder:%s", flowID)`。V1 导出排除 `agent_builder:%`。
- 仓库里没有第三个产品常量 `*SystemKey`。测试里出现的 `label_cleanup_probe` 等是夹具，不是产品键。
- `GET /api/agents` 的 SQL：`kind = 'user'`（`agent.sql:1-4`），因此 **Mika 会出现在 agents 列表，`system_key: "mika"`**（`AgentResponse.SystemKey`，`agent.go:98-102, 251`）。

### 对实现的影响

- 官方云 → kun：不会因为「kun 多一个 mika / 官方云多一个 mika」触发 `system_agent_not_in_target`。
- CLI **不得**按内部字段 `kind = 'system'` 分组：读接口不暴露 `kind`，那样 `entities.system_agents` 会是 `[]`，Mika 会当普通用户智能体按 `name` 导入，**丢掉 system 指令层**。实现必须：`GET /api/agents` 里 `system_key` 非空且不以 `agent_builder:` 开头的行改放到 `system_agents`，只投影 V1 系统智能体字段（`system_key`、`instructions`、`model`、`thinking_level`、`service_tier`、`conversation_starters`、`disabled_runtime_skills`）。产品上目前就是 `system_key == "mika"`。
- 契约已按 DENE-258 改为 `system_key` 判定。V1 服务端 SQL 仍用 `kind = 'system'`，留给 DENE-259。

---

## 5. §8.6 正文附件链接形态

### 结论

聊天正文里的附件引用**不是单一形态**，导入端**可以安全改写其中一类**，其余原样保留并计数。

会出现的形态：

1. **站点相对路径** `/api/attachments/{uuid}/download` — 新内容的稳定形态；聊天输入测试把这个当 persisted markdown（`packages/views/chat/components/chat-input.test.tsx` 附近 `STABLE_MARKDOWN_LINK`）。
2. **绝对 API URL** `{MULTICA_PUBLIC_URL}/api/attachments/{uuid}/download` — `buildMarkdownURL` 在配了 `PublicURL` 时的首选（官方云 / 多数自建会走这条）。
3. **公开存储 URL**（已是绝对、无签名 query 的 CDN / 公有桶 URL）— `storageURLIsPubliclyReadable` 为真时直接持久化 `a.Url`，**不是** download 路径。
4. 消息还有独立的 `attachments[]`（id / filename / markdown_url / …），不依赖正文解析。

**可以安全改写：** 形态 1 和 2。仓库已有解析函数 `util.AttachmentIDFromDownloadURL`，同时接受相对路径和 `http(s)` 绝对 URL。

**不能当附件 id 改写：** 形态 3、任意其它 URL、过期签名 URL。按契约确认前的规则：原样保留，`content_has_source_urls` +1。

### 证据

- 稳定路径构造：`server/internal/util/attachment_url.go:13-18` → `/api/attachments/` + id + `/download`。
- 解析（相对 + 绝对）：`attachment_url.go:20-47`。
- `markdown_url` 合同：`server/internal/handler/file.go:83-108, 236-247`。优先级：公开存储 URL → `PublicURL + relPath` → 相对路径兜底。
- 聊天消息响应：`content` 原样 + `Attachments []AttachmentResponse`（`chat.go:2041-2054`）。
- 客户端把 `markdown_url` 写入正文（issue/comment/chat 同一套 attachment 组件）。Desktop 无同源 rewrite 时更常是绝对 URL（MUL-3192）。

未对真实官方云工作区抽样正文（禁止访问）。上面是服务端合同 + 客户端测试，足以决定改写规则。

### 对实现的影响

- 导入改写：对 `chat_message.content`（以及会话 title，若有）用 `AttachmentIDFromDownloadURL`；命中则换成目标附件的 download 路径（导入后新 id 的相对或目标 `PublicURL` 绝对路径，二选一，同一包内保持一种）。
- 未命中的 URL 不改，计数 `content_has_source_urls`。
- 文件体仍走 `GET /api/attachments/{id}/download`（认证下载），不要用正文里的 CDN URL 当权威。
- 契约 §3.4 末尾那条未确认项：可以关。规则就是「download 路径可改写，其它 URL 原样」。

---

## 6. §8.9 速率限制与分页上限

### 结论

**官方云读接口速率限制：未确认**（代码与公开文档都没有对 `/api/agents`、`/api/chat/sessions` 这类会话读接口的通用限额；本票禁止打官方云实测）。

**分页上限（代码常量，kun 与 `origin/main` 同形）：**

| 接口 | 默认 | 上限 | 超限行为 |
| --- | --- | --- | --- |
| `GET /api/chat/sessions` | 无分页 | **无 LIMIT** | 一次返回该用户在该工作区的全部会话（`ListAllChatSessionsByCreator` / `ListChatSessionsByCreator` 无 `LIMIT`，`chat.sql:72-107`；handler 不读 `limit`，`chat.go:155-243`） |
| `GET /api/chat/sessions/{id}/messages/page` | **50** | **100** | `limit` 缺省 50；`1..100` 有效；`0` 或 `>100` 或非整数 → **400 `invalid limit`**（不是截断）。`parseChatMessagesPageParams`：`chat.go:1047-1054`。SQL `LIMIT $2`：`chat.sql:1053`。 |

其它相关上限（实现会碰到，但不在本项必答里）：`GET /api/issues` 把 `limit` **截断到 100**；`GET /api/issue-views` 单 scope 200；邀请 / 登录 / Plugin Action API 另有限额。

代码里挂了 Redis 限流的是：

- 登录：`RATE_LIMIT_AUTH` 默认 5/分钟，`RATE_LIMIT_AUTH_VERIFY` 默认 20/分钟（`router.go:1383-1390`）
- Plugin Action API：`RATE_LIMIT_PLUGIN_API` 默认 120/分钟（`router.go:1496`）
- 邀请、webhook

**Workspace 成员读接口（agents / chat / labels / …）在 Go 路由上没有通用 RateLimit 中间件。** 官方云前面是否还有 Cloudflare / 网关限额，查不到。

### 证据

- 文档只描述 auth / invitation 限流：`apps/docs/content/docs/environment-variables.mdx`「Redis and rate limiting」。
- 聊天分页解析见上。V2 契约 §3.1 写「limit 上限 100」与代码一致；默认 50 契约没写，实现应显式传 `limit=100`。

### 对实现的影响

- 官方云速率：**按未确认处理**，沿用契约 §7.1：429 / 503 指数退避（1s 起、上限 60s、尊重 `Retry-After`），翻页并发不超过 4。不要假设「没有 429」。
- `GET /api/chat/sessions`：一次拉全量，不要自己造 cursor。会话极多时可能是大 JSON，这是已知成本，不是缺分页 API。
- `messages/page`：始终 `limit=100`。不要传 101（会 400）。游标 `before_created_at` + `before_id`。
- 保守取值建议（官方云未确认时写进实现）：读 QPS 按 ≤ 4 并发、单连接；连续 429 则把并发降到 1。

---

## 给 CLI 实现票的对照清单

1. 双 `--profile` 可用；清掉 `MULTICA_TOKEN` / `MULTICA_SERVER_URL` 再跑 transfer。
2. `people.json` 从 `GET /api/workspaces/{id}/members` 的 `email` 填；空邮箱降级。
3. 按第 3 节表打读接口；`attribution_fail_closed` 不导；skills 经 `GET /plugins` 的 `resources` 反查排除，接口不可用时全部导出并记警告；Mika（`system_key` 非空且不以 `agent_builder:` 开头）从 agents 列表拆到 `system_agents`。
4. `system_key` 两边都是 `{mika}` + 动态 `agent_builder:*`。
5. 正文只改写 `/api/attachments/{uuid}/download`（相对或绝对）；其它 URL 计数保留。
6. 会话列表无分页；消息页 `limit=100`；官方云速率未确认，按 429 退避。

## 契约错位（DENE-258 已改正文）

1. ~~V1 把系统智能体定义成 `kind = 'system'`，产品唯一的 `system_key`（`mika`）却是 `kind = 'user'`。~~ 契约改为 `system_key` 非空且不以 `agent_builder:` 开头。V1 服务端 SQL 仍按 `kind = 'system'` 导出，代码缺陷见 DENE-259。
2. ~~V1 要求不导出 `plugin_installation_id IS NOT NULL` 的技能，读接口不暴露该字段。~~ V1 该行不动（服务端有 DB 列）。V2 CLI 经 `GET /plugins` 的 `resources` 反查排除；插件接口不可用时全部导出并记警告。
